// Package openshell — client_grpc.go implements the GatewayClient interface
// using NVIDIA OpenShell's gRPC API (openshell.v1.OpenShell service).
//
// The client handles connection lifecycle, TLS/mTLS configuration, and maps
// our domain types to the generated proto request/response messages.
//
// Reference: docs/architecture/openshell-sandbox-backend.md §6.

package openshell

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	pb "github.com/SAP/astonish/pkg/sandbox/openshell/gen/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

// GRPCClientConfig configures the gRPC gateway client connection.
type GRPCClientConfig struct {
	// Addr is the gateway gRPC endpoint (host:port).
	Addr string

	// TLS enables TLS. When false, an insecure connection is used.
	TLS bool

	// ClientCertPath / ClientKeyPath configure mTLS client authentication.
	ClientCertPath string
	ClientKeyPath  string

	// CACertPath is the CA certificate for verifying the gateway. Empty uses system CAs.
	CACertPath string

	// AuthToken is a static bearer token sent in the Authorization header.
	AuthToken string
}

// grpcGatewayClient implements GatewayClient using the NVIDIA OpenShell gRPC API.
type grpcGatewayClient struct {
	conn   *grpc.ClientConn
	client pb.OpenShellClient
	cfg    GRPCClientConfig
}

// NewGRPCGatewayClient creates a new gRPC client connected to the OpenShell gateway.
func NewGRPCGatewayClient(cfg GRPCClientConfig) (GatewayClient, error) {
	if cfg.Addr == "" {
		return nil, errors.New("sandbox/openshell: GRPCClientConfig.Addr is required")
	}

	dialOpts, err := buildDialOptions(cfg)
	if err != nil {
		return nil, fmt.Errorf("sandbox/openshell: build dial options: %w", err)
	}

	conn, err := grpc.NewClient(cfg.Addr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("sandbox/openshell: dial gateway %s: %w", cfg.Addr, err)
	}

	return &grpcGatewayClient{
		conn:   conn,
		client: pb.NewOpenShellClient(conn),
		cfg:    cfg,
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *grpcGatewayClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func buildDialOptions(cfg GRPCClientConfig) ([]grpc.DialOption, error) {
	var opts []grpc.DialOption

	if !cfg.TLS {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		tlsCfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}

		// Load CA cert if provided.
		if cfg.CACertPath != "" {
			caCert, err := os.ReadFile(cfg.CACertPath)
			if err != nil {
				return nil, fmt.Errorf("read CA cert %s: %w", cfg.CACertPath, err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse CA cert %s", cfg.CACertPath)
			}
			tlsCfg.RootCAs = pool
		}

		// Load mTLS client cert if provided.
		if cfg.ClientCertPath != "" && cfg.ClientKeyPath != "" {
			cert, err := tls.LoadX509KeyPair(cfg.ClientCertPath, cfg.ClientKeyPath)
			if err != nil {
				return nil, fmt.Errorf("load client cert: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}

		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	}

	// Add bearer token interceptor if configured.
	if cfg.AuthToken != "" {
		opts = append(opts, grpc.WithUnaryInterceptor(bearerTokenUnaryInterceptor(cfg.AuthToken)))
		opts = append(opts, grpc.WithStreamInterceptor(bearerTokenStreamInterceptor(cfg.AuthToken)))
	}

	// Enable gRPC keepalive to prevent the h2 connection from being killed
	// by the OpenShell supervisor's idle timeout (~145s observed). Pings
	// every 30s keep the connection alive during long-running streaming
	// operations (MCP server exec, interactive terminals).
	opts = append(opts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                30 * time.Second, // Send ping every 30s if no activity
		Timeout:             10 * time.Second, // Wait 10s for ping ACK
		PermitWithoutStream: true,             // Ping even when no active RPCs
	}))

	return opts, nil
}

func bearerTokenUnaryInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func bearerTokenStreamInterceptor(token string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// ---------------------------------------------------------------------------
// GatewayClient interface implementation
// ---------------------------------------------------------------------------

func (c *grpcGatewayClient) CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*CreateSandboxResponse, error) {
	template := &pb.SandboxTemplate{
		Image:       req.Image,
		Environment: req.Env,
		Labels:      req.Labels,
	}
	if len(req.DriverConfig) > 0 {
		ds, err := structpb.NewStruct(req.DriverConfig)
		if err != nil {
			return nil, fmt.Errorf("gateway CreateSandbox: encode driver_config: %w", err)
		}
		template.DriverConfig = ds
	}

	pbReq := &pb.CreateSandboxRequest{
		Name:   req.Name,
		Labels: req.Labels,
		Spec: &pb.SandboxSpec{
			Template: template,
			Policy:   mapPolicyToProto(req.Policy),
		},
	}

	resp, err := c.client.CreateSandbox(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gateway CreateSandbox: %w", err)
	}

	sandbox := resp.GetSandbox()
	if sandbox == nil {
		return nil, errors.New("gateway CreateSandbox: nil sandbox in response")
	}

	meta := sandbox.GetMetadata()
	status := sandbox.GetStatus()

	return &CreateSandboxResponse{
		SandboxID: meta.GetName(),
		GatewayID: meta.GetId(),
		PodName:   status.GetAgentPod(),
	}, nil
}

func (c *grpcGatewayClient) DeleteSandbox(ctx context.Context, sandboxID string) error {
	// The gateway uses sandbox name for deletion, not ID.
	// We store the sandbox name in ContainerName in our session registry.
	_, err := c.client.DeleteSandbox(ctx, &pb.DeleteSandboxRequest{
		Name: sandboxID,
	})
	if err != nil {
		return fmt.Errorf("gateway DeleteSandbox: %w", err)
	}
	return nil
}

func (c *grpcGatewayClient) GetSandboxStatus(ctx context.Context, sandboxID string) (*SandboxStatus, error) {
	resp, err := c.client.GetSandbox(ctx, &pb.GetSandboxRequest{
		Name: sandboxID,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway GetSandbox: %w", err)
	}

	sandbox := resp.GetSandbox()
	if sandbox == nil {
		return nil, errors.New("gateway GetSandbox: nil sandbox in response")
	}

	meta := sandbox.GetMetadata()
	status := sandbox.GetStatus()
	phase := status.GetPhase()

	return &SandboxStatus{
		State:        mapPhaseToState(phase),
		Message:      phaseMessage(phase, status),
		PodName:      status.GetAgentPod(),
		GatewayID:    meta.GetId(),
		DriverConfig: sandbox.GetSpec().GetTemplate().GetDriverConfig().AsMap(),
		Env:          sandbox.GetSpec().GetTemplate().GetEnvironment(),
		Labels:       sandbox.GetSpec().GetTemplate().GetLabels(),
	}, nil
}

func (c *grpcGatewayClient) ExecCommand(ctx context.Context, sandboxID string, req ExecRequest) (*ExecResponse, error) {
	pbReq := &pb.ExecSandboxRequest{
		SandboxId:   sandboxID,
		Command:     req.Command,
		Workdir:     req.WorkDir,
		Environment: req.Env,
		Tty:         req.TTY,
		Cols:        uint32(req.Cols),
		Rows:        uint32(req.Rows),
	}

	// Read stdin if provided.
	if req.Stdin != nil {
		data, err := io.ReadAll(req.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		pbReq.Stdin = data
	}

	stream, err := c.client.ExecSandbox(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gateway ExecSandbox: %w", err)
	}

	var stdout, stderr bytes.Buffer
	var exitCode int32

	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gateway ExecSandbox recv: %w", err)
		}

		switch payload := event.GetPayload().(type) {
		case *pb.ExecSandboxEvent_Stdout:
			stdout.Write(payload.Stdout.GetData())
		case *pb.ExecSandboxEvent_Stderr:
			stderr.Write(payload.Stderr.GetData())
		case *pb.ExecSandboxEvent_Exit:
			exitCode = payload.Exit.GetExitCode()
		}
	}

	return &ExecResponse{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: int(exitCode),
	}, nil
}

func (c *grpcGatewayClient) ExecStream(ctx context.Context, sandboxID string, req ExecRequest) (ExecStreamConn, error) {
	stream, err := c.client.ExecSandboxInteractive(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway ExecSandboxInteractive: %w", err)
	}

	// Send the initial start message.
	startMsg := &pb.ExecSandboxInput{
		Payload: &pb.ExecSandboxInput_Start{
			Start: &pb.ExecSandboxRequest{
				SandboxId:   sandboxID,
				Command:     req.Command,
				Workdir:     req.WorkDir,
				Environment: req.Env,
				Tty:         req.TTY,
				Cols:        uint32(req.Cols),
				Rows:        uint32(req.Rows),
			},
		},
	}
	if err := stream.Send(startMsg); err != nil {
		return nil, fmt.Errorf("gateway ExecSandboxInteractive send start: %w", err)
	}

	conn := &grpcExecStreamConn{
		stream:         stream,
		separateStderr: req.SeparateStderr,
		exitCode:       -1,
	}
	conn.cond = sync.NewCond(&conn.mu)
	// Start background reader.
	go conn.readLoop()

	return conn, nil
}

// ---------------------------------------------------------------------------
// grpcExecStreamConn — implements ExecStreamConn
// ---------------------------------------------------------------------------

type grpcExecStreamConn struct {
	stream         pb.OpenShell_ExecSandboxInteractiveClient
	separateStderr io.Writer // non-nil → stderr routed here, not mixed into buf
	buf            bytes.Buffer
	mu             sync.Mutex
	cond           *sync.Cond
	exitCode       int
	closed         bool
	readErr        error
}

func (c *grpcExecStreamConn) readLoop() {
	for {
		event, err := c.stream.Recv()
		if err != nil {
			c.mu.Lock()
			if err != io.EOF {
				c.readErr = err
			}
			c.closed = true
			c.cond.Broadcast()
			c.mu.Unlock()
			return
		}

		c.mu.Lock()
		switch payload := event.GetPayload().(type) {
		case *pb.ExecSandboxEvent_Stdout:
			c.buf.Write(payload.Stdout.GetData())
		case *pb.ExecSandboxEvent_Stderr:
			if c.separateStderr != nil {
				// Route stderr to the dedicated writer (not mixed into stdout).
				// This is critical for machine protocols like MCP JSON-RPC
				// where stderr contamination breaks JSON parsing.
				c.separateStderr.Write(payload.Stderr.GetData())
			} else {
				c.buf.Write(payload.Stderr.GetData())
			}
		case *pb.ExecSandboxEvent_Exit:
			c.exitCode = int(payload.Exit.GetExitCode())
			c.closed = true
		}
		c.cond.Broadcast()
		c.mu.Unlock()
	}
}

func (c *grpcExecStreamConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for c.buf.Len() == 0 && !c.closed {
		c.cond.Wait()
	}

	if c.buf.Len() > 0 {
		return c.buf.Read(p)
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	return 0, io.EOF
}

func (c *grpcExecStreamConn) Write(p []byte) (int, error) {
	msg := &pb.ExecSandboxInput{
		Payload: &pb.ExecSandboxInput_Stdin{
			Stdin: p,
		},
	}
	if err := c.stream.Send(msg); err != nil {
		return 0, fmt.Errorf("send stdin: %w", err)
	}
	return len(p), nil
}

func (c *grpcExecStreamConn) Resize(cols, rows int) error {
	msg := &pb.ExecSandboxInput{
		Payload: &pb.ExecSandboxInput_Resize{
			Resize: &pb.ExecSandboxWindowResize{
				Cols: uint32(cols),
				Rows: uint32(rows),
			},
		},
	}
	return c.stream.Send(msg)
}

func (c *grpcExecStreamConn) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitCode
}

func (c *grpcExecStreamConn) Close() error {
	return c.stream.CloseSend()
}

// ---------------------------------------------------------------------------
// ListSandboxes
// ---------------------------------------------------------------------------

const (
	gatewayInventoryPageSize = 500
	gatewayInventoryMaxPages = 1000
)

func (c *grpcGatewayClient) ListSandboxes(ctx context.Context, labelSelector string) ([]SandboxSummary, error) {
	// The gateway exposes offsets, not snapshot tokens or a total. An empty
	// page ends traversal; a short page may merely reflect a server-side cap.
	// This is not atomic detachment evidence for home purge.
	summaries := make([]SandboxSummary, 0)
	seenIDs := make(map[string]struct{})
	seenNames := make(map[string]struct{})
	var offset uint32
	for page := 0; page < gatewayInventoryMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("gateway ListSandboxes: %w", err)
		}
		resp, err := c.client.ListSandboxes(ctx, &pb.ListSandboxesRequest{
			LabelSelector: labelSelector,
			Limit:         gatewayInventoryPageSize,
			Offset:        offset,
		})
		if err != nil {
			return nil, fmt.Errorf("gateway ListSandboxes offset %d: %w", offset, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("gateway ListSandboxes: %w", err)
		}
		if resp == nil {
			return nil, errors.New("gateway ListSandboxes: nil response")
		}
		sandboxes := resp.GetSandboxes()
		if len(sandboxes) == 0 {
			return summaries, nil
		}
		if len(sandboxes) > gatewayInventoryPageSize {
			return nil, errors.New("gateway ListSandboxes: page exceeds requested limit")
		}
		for _, s := range sandboxes {
			meta := s.GetMetadata()
			if meta.GetId() == "" || meta.GetName() == "" {
				return nil, errors.New("gateway ListSandboxes: missing sandbox identity")
			}
			if _, exists := seenIDs[meta.GetId()]; exists {
				return nil, errors.New("gateway ListSandboxes: repeated sandbox ID; inventory is unstable or offset ignored")
			}
			if _, exists := seenNames[meta.GetName()]; exists {
				return nil, errors.New("gateway ListSandboxes: repeated sandbox name; inventory is unstable")
			}
			seenIDs[meta.GetId()] = struct{}{}
			seenNames[meta.GetName()] = struct{}{}
			summary := SandboxSummary{
				ID:     meta.GetId(),
				Name:   meta.GetName(),
				Labels: meta.GetLabels(),
				Phase:  s.GetStatus().GetPhase().String(),
			}
			if ms := meta.GetCreatedAtMs(); ms > 0 {
				summary.CreatedAt = time.UnixMilli(ms)
			}
			summaries = append(summaries, summary)
		}
		// The fixed page/request bounds keep this sum below uint32 overflow.
		offset += uint32(len(sandboxes))
	}
	return nil, errors.New("gateway ListSandboxes: page budget exhausted before empty terminal page")
}

// ---------------------------------------------------------------------------
// Draft Policy & Network Approval
// ---------------------------------------------------------------------------

func (c *grpcGatewayClient) GetDraftPolicy(ctx context.Context, sandboxName string, statusFilter string) (*DraftPolicyResponse, error) {
	resp, err := c.client.GetDraftPolicy(ctx, &pb.GetDraftPolicyRequest{
		Name:         sandboxName,
		StatusFilter: statusFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway GetDraftPolicy: %w", err)
	}

	chunks := make([]PolicyChunkInfo, 0, len(resp.GetChunks()))
	for _, ch := range resp.GetChunks() {
		info := PolicyChunkInfo{
			ID:            ch.GetId(),
			Status:        ch.GetStatus(),
			RuleName:      ch.GetRuleName(),
			Binary:        ch.GetBinary(),
			Rationale:     ch.GetRationale(),
			SecurityNotes: ch.GetSecurityNotes(),
			HitCount:      ch.GetHitCount(),
			CreatedAtMs:   ch.GetCreatedAtMs(),
		}
		// Extract host/port from the proposed rule's first endpoint.
		if rule := ch.GetProposedRule(); rule != nil {
			if eps := rule.GetEndpoints(); len(eps) > 0 {
				info.Host = eps[0].GetHost()
				info.Port = eps[0].GetPort()
			}
		}
		chunks = append(chunks, info)
	}

	return &DraftPolicyResponse{
		Chunks:           chunks,
		DraftVersion:     resp.GetDraftVersion(),
		LastAnalyzedAtMs: resp.GetLastAnalyzedAtMs(),
	}, nil
}

func (c *grpcGatewayClient) ApproveDraftChunk(ctx context.Context, sandboxName string, chunkID string) (*ApproveChunkResponse, error) {
	resp, err := c.client.ApproveDraftChunk(ctx, &pb.ApproveDraftChunkRequest{
		Name:    sandboxName,
		ChunkId: chunkID,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway ApproveDraftChunk: %w", err)
	}
	return &ApproveChunkResponse{
		PolicyVersion: resp.GetPolicyVersion(),
		PolicyHash:    resp.GetPolicyHash(),
	}, nil
}

func (c *grpcGatewayClient) RejectDraftChunk(ctx context.Context, sandboxName string, chunkID string, reason string) error {
	_, err := c.client.RejectDraftChunk(ctx, &pb.RejectDraftChunkRequest{
		Name:    sandboxName,
		ChunkId: chunkID,
		Reason:  reason,
	})
	if err != nil {
		return fmt.Errorf("gateway RejectDraftChunk: %w", err)
	}
	return nil
}

func (c *grpcGatewayClient) UpdateConfig(ctx context.Context, sandboxName string, ops []PolicyMergeOp) (*UpdateConfigResponse, error) {
	pbOps := make([]*pb.PolicyMergeOperation, 0, len(ops))
	for _, op := range ops {
		switch op.Type {
		case PolicyMergeAddEndpoint:
			port := op.Endpoint.Port
			if port == 0 {
				port = 443
			}
			pbOps = append(pbOps, &pb.PolicyMergeOperation{
				Operation: &pb.PolicyMergeOperation_AddRule{
					AddRule: &pb.AddNetworkRule{
						RuleName: op.RuleName,
						Rule: &pb.NetworkPolicyRule{
							Name: op.RuleName,
							Endpoints: []*pb.NetworkEndpoint{
								{Host: op.Endpoint.Host, Port: port},
							},
							Binaries: []*pb.NetworkBinary{
								{Path: "/**"},
							},
						},
					},
				},
			})
		case PolicyMergeRemoveEndpoint:
			port := op.Endpoint.Port
			if port == 0 {
				port = 443
			}
			pbOps = append(pbOps, &pb.PolicyMergeOperation{
				Operation: &pb.PolicyMergeOperation_RemoveEndpoint{
					RemoveEndpoint: &pb.RemoveNetworkEndpoint{
						RuleName: op.RuleName,
						Host:     op.Endpoint.Host,
						Port:     port,
					},
				},
			})
		}
	}

	resp, err := c.client.UpdateConfig(ctx, &pb.UpdateConfigRequest{
		Name:            sandboxName,
		MergeOperations: pbOps,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway UpdateConfig: %w", err)
	}

	return &UpdateConfigResponse{PolicyVersion: resp.GetVersion()}, nil
}

func (c *grpcGatewayClient) GetPolicyStatus(ctx context.Context, sandboxName string, version uint32) (*PolicyStatusResponse, error) {
	resp, err := c.client.GetSandboxPolicyStatus(ctx, &pb.GetSandboxPolicyStatusRequest{
		Name:    sandboxName,
		Version: version,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway GetPolicyStatus: %w", err)
	}

	result := &PolicyStatusResponse{
		ActiveVersion: resp.GetActiveVersion(),
	}
	if rev := resp.GetRevision(); rev != nil {
		switch rev.GetStatus() {
		case pb.PolicyStatus_POLICY_STATUS_LOADED:
			result.Status = "loaded"
		case pb.PolicyStatus_POLICY_STATUS_PENDING:
			result.Status = "pending"
		case pb.PolicyStatus_POLICY_STATUS_FAILED:
			result.Status = "failed"
		case pb.PolicyStatus_POLICY_STATUS_SUPERSEDED:
			result.Status = "superseded"
		default:
			result.Status = "unknown"
		}
	}
	return result, nil
}

func (c *grpcGatewayClient) WatchSandbox(ctx context.Context, sandboxName string, opts WatchOpts) (SandboxEventStream, error) {
	stream, err := c.client.WatchSandbox(ctx, &pb.WatchSandboxRequest{
		Id:           sandboxName,
		FollowEvents: opts.FollowEvents,
		EventTail:    opts.EventTail,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway WatchSandbox: %w", err)
	}
	return &grpcSandboxEventStream{stream: stream}, nil
}

// grpcSandboxEventStream implements SandboxEventStream over a gRPC server stream.
type grpcSandboxEventStream struct {
	stream pb.OpenShell_WatchSandboxClient
}

func (s *grpcSandboxEventStream) Recv() (*SandboxEvent, error) {
	event, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}

	switch payload := event.GetPayload().(type) {
	case *pb.SandboxStreamEvent_DraftPolicyUpdate:
		return &SandboxEvent{
			Type: SandboxEventDraftUpdate,
			DraftPolicyUpdate: &DraftPolicyUpdateInfo{
				DraftVersion: payload.DraftPolicyUpdate.GetDraftVersion(),
				NewChunks:    payload.DraftPolicyUpdate.GetNewChunks(),
				TotalPending: payload.DraftPolicyUpdate.GetTotalPending(),
				Summary:      payload.DraftPolicyUpdate.GetSummary(),
			},
		}, nil
	default:
		return &SandboxEvent{Type: SandboxEventOther}, nil
	}
}

func (s *grpcSandboxEventStream) Close() error {
	// Closing the context that created the stream will terminate it.
	// For server streams, there's no explicit CloseSend — the context cancellation handles it.
	return nil
}

func (c *grpcGatewayClient) CreateProvider(ctx context.Context, name, providerType string, credentials map[string]string) error {
	if name == "" {
		return fmt.Errorf("gateway CreateProvider: name is required")
	}
	if credentials == nil {
		credentials = map[string]string{}
	}
	_, err := c.client.CreateProvider(ctx, &pb.CreateProviderRequest{
		Provider: &pb.Provider{
			Metadata:    &pb.ObjectMeta{Name: name},
			Type:        providerType,
			Credentials: credentials,
		},
	})
	if err != nil {
		return fmt.Errorf("gateway CreateProvider: %w", err)
	}
	return nil
}

func (c *grpcGatewayClient) DeleteProvider(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := c.client.DeleteProvider(ctx, &pb.DeleteProviderRequest{Name: name})
	if err != nil {
		return fmt.Errorf("gateway DeleteProvider: %w", err)
	}
	return nil
}

func (c *grpcGatewayClient) AttachSandboxProvider(ctx context.Context, sandboxName, providerName string) error {
	if sandboxName == "" || providerName == "" {
		return fmt.Errorf("gateway AttachSandboxProvider: sandbox and provider names are required")
	}
	_, err := c.client.AttachSandboxProvider(ctx, &pb.AttachSandboxProviderRequest{
		SandboxName:  sandboxName,
		ProviderName: providerName,
	})
	if err != nil {
		return fmt.Errorf("gateway AttachSandboxProvider: %w", err)
	}
	return nil
}

func (c *grpcGatewayClient) DetachSandboxProvider(ctx context.Context, sandboxName, providerName string) error {
	if sandboxName == "" || providerName == "" {
		return nil
	}
	_, err := c.client.DetachSandboxProvider(ctx, &pb.DetachSandboxProviderRequest{
		SandboxName:  sandboxName,
		ProviderName: providerName,
	})
	if err != nil {
		return fmt.Errorf("gateway DetachSandboxProvider: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mapPhaseToState converts NVIDIA's SandboxPhase to our SandboxState.
func mapPhaseToState(phase pb.SandboxPhase) SandboxState {
	switch phase {
	case pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING:
		return SandboxStateCreating
	case pb.SandboxPhase_SANDBOX_PHASE_READY:
		return SandboxStateRunning
	case pb.SandboxPhase_SANDBOX_PHASE_ERROR:
		return SandboxStateFailed
	case pb.SandboxPhase_SANDBOX_PHASE_DELETING:
		return SandboxStateStopped
	case pb.SandboxPhase_SANDBOX_PHASE_UNKNOWN, pb.SandboxPhase_SANDBOX_PHASE_UNSPECIFIED:
		return SandboxStateGone
	default:
		return SandboxStateGone
	}
}

func phaseMessage(phase pb.SandboxPhase, status *pb.SandboxStatus) string {
	if status == nil {
		return phase.String()
	}
	// Check conditions for more detailed messages.
	for _, cond := range status.GetConditions() {
		if cond.GetStatus() == "False" && cond.GetMessage() != "" {
			return cond.GetMessage()
		}
	}
	return phase.String()
}

// mapPolicyToProto converts the domain SandboxPolicySpec to the proto
// SandboxPolicy message. Returns nil when spec is nil (gateway applies
// its default policy).
func mapPolicyToProto(spec *SandboxPolicySpec) *pb.SandboxPolicy {
	if spec == nil {
		return nil
	}
	p := &pb.SandboxPolicy{
		Version: spec.Version,
	}
	if spec.Landlock != nil {
		p.Landlock = &pb.LandlockPolicy{
			Compatibility: spec.Landlock.Compatibility,
		}
	}
	if spec.Filesystem != nil {
		p.Filesystem = &pb.FilesystemPolicy{
			IncludeWorkdir: spec.Filesystem.IncludeWorkdir,
			ReadOnly:       spec.Filesystem.ReadOnly,
			ReadWrite:      spec.Filesystem.ReadWrite,
		}
	}
	if spec.Process != nil {
		p.Process = &pb.ProcessPolicy{
			RunAsUser:  spec.Process.RunAsUser,
			RunAsGroup: spec.Process.RunAsGroup,
		}
	}
	if len(spec.NetworkPolicies) > 0 {
		p.NetworkPolicies = make(map[string]*pb.NetworkPolicyRule, len(spec.NetworkPolicies))
		for k, v := range spec.NetworkPolicies {
			rule := &pb.NetworkPolicyRule{
				Name: v.Name,
			}
			for _, ep := range v.Endpoints {
				port := ep.Port
				if port == 0 {
					port = 443
				}
				rule.Endpoints = append(rule.Endpoints, &pb.NetworkEndpoint{
					Host: ep.Host,
					Port: port,
				})
			}
			for _, bin := range v.Binaries {
				rule.Binaries = append(rule.Binaries, &pb.NetworkBinary{
					Path: bin,
				})
			}
			p.NetworkPolicies[k] = rule
		}
	}
	return p
}
