package openshell

import (
	"context"
	"io"
	"time"
)

// GatewayClient is the interface the backend uses to communicate with the
// OpenShell Gateway. It abstracts the gRPC transport so the backend can
// be tested without a live gateway.
//
// The real implementation (client_grpc.go) will use OpenShell's proto-generated
// stubs. Tests use a mock.
//
// Reference: docs/architecture/openshell-sandbox-backend.md §7, §8.
type GatewayClient interface {
	// CreateSandbox requests the gateway to create a new sandbox.
	// Returns the gateway-assigned sandbox ID.
	CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*CreateSandboxResponse, error)

	// DeleteSandbox requests deletion of a sandbox by its ID.
	DeleteSandbox(ctx context.Context, sandboxID string) error

	// GetSandboxStatus retrieves the current status of a sandbox.
	GetSandboxStatus(ctx context.Context, sandboxID string) (*SandboxStatus, error)

	// ExecCommand runs a command in a sandbox and returns the result.
	// For synchronous (non-streaming) execution.
	ExecCommand(ctx context.Context, sandboxID string, req ExecRequest) (*ExecResponse, error)

	// ExecStream starts a bidirectional streaming exec session.
	// Returns a stream that the caller reads/writes. Caller MUST close the stream.
	ExecStream(ctx context.Context, sandboxID string, req ExecRequest) (ExecStreamConn, error)

	// ListSandboxes returns all sandboxes known to the gateway, optionally
	// filtered by a Kubernetes-style label selector (e.g., "purpose=chat").
	// An empty selector returns all sandboxes.
	ListSandboxes(ctx context.Context, labelSelector string) ([]SandboxSummary, error)

	// GetDraftPolicy returns pending (or filtered) draft policy proposals for a sandbox.
	// The supervisor automatically generates proposals when it detects network denials.
	// statusFilter can be "pending", "approved", "rejected", or "" for all.
	GetDraftPolicy(ctx context.Context, sandboxName string, statusFilter string) (*DraftPolicyResponse, error)

	// ApproveDraftChunk approves a single draft policy chunk, causing the gateway
	// to merge the proposed endpoint into the sandbox's active network policy.
	ApproveDraftChunk(ctx context.Context, sandboxName string, chunkID string) (*ApproveChunkResponse, error)

	// RejectDraftChunk rejects a draft policy chunk with an optional reason.
	RejectDraftChunk(ctx context.Context, sandboxName string, chunkID string, reason string) error

	// UpdateConfig applies incremental policy merge operations to a running sandbox.
	// Use this for broader-pattern approvals that don't correspond to a specific draft chunk.
	UpdateConfig(ctx context.Context, sandboxName string, ops []PolicyMergeOp) (*UpdateConfigResponse, error)

	// GetPolicyStatus queries the load status of a specific policy version.
	// Used to confirm the sandbox proxy has actually loaded a new policy
	// before allowing the agent to retry a previously-blocked request.
	GetPolicyStatus(ctx context.Context, sandboxName string, version uint32) (*PolicyStatusResponse, error)

	// WatchSandbox opens a server-streaming connection that delivers sandbox events
	// including draft policy updates (denial notifications). The caller must close
	// the returned stream when done.
	WatchSandbox(ctx context.Context, sandboxName string, opts WatchOpts) (SandboxEventStream, error)

	// Close releases any resources held by the client (e.g., gRPC connection).
	Close() error

	// --- OpenShell Providers (Phase 2) --------------------------------

	// CreateProvider registers a named provider with credential key-value pairs.
	CreateProvider(ctx context.Context, name, providerType string, credentials map[string]string) error

	// DeleteProvider removes a provider by name.
	DeleteProvider(ctx context.Context, name string) error

	// AttachSandboxProvider binds a provider to a sandbox for per-process env injection.
	AttachSandboxProvider(ctx context.Context, sandboxName, providerName string) error

	// DetachSandboxProvider unbinds a provider from a sandbox.
	DetachSandboxProvider(ctx context.Context, sandboxName, providerName string) error
}

// DraftPolicyResponse contains the draft policy chunks returned by GetDraftPolicy.
type DraftPolicyResponse struct {
	// Chunks are the draft policy proposals.
	Chunks []PolicyChunkInfo
	// DraftVersion is the current draft version counter.
	DraftVersion uint64
	// LastAnalyzedAtMs is when the last analysis cycle completed (ms since epoch).
	LastAnalyzedAtMs int64
}

// PolicyChunkInfo is a single draft policy proposal from the supervisor.
type PolicyChunkInfo struct {
	// ID is the unique chunk identifier (used to approve/reject).
	ID string
	// Status is "pending", "approved", or "rejected".
	Status string
	// RuleName is the network_policies map key (e.g., "astonish-egress").
	RuleName string
	// Host is the proposed endpoint host pattern.
	Host string
	// Port is the proposed endpoint port.
	Port uint32
	// Binary is the binary path that triggered the denial.
	Binary string
	// Rationale is a human-readable explanation of the proposal.
	Rationale string
	// SecurityNotes contains any security concerns flagged by analysis.
	SecurityNotes string
	// HitCount is how many times this endpoint was denied.
	HitCount int32
	// CreatedAtMs is the chunk creation timestamp.
	CreatedAtMs int64
}

// ApproveChunkResponse is returned after successfully approving a draft chunk.
type ApproveChunkResponse struct {
	// PolicyVersion is the new active policy version after the merge.
	PolicyVersion uint32
	// PolicyHash is the SHA-256 hash of the new policy.
	PolicyHash string
}

// UpdateConfigResponse is returned after a successful UpdateConfig call.
type UpdateConfigResponse struct {
	// PolicyVersion is the new active policy version.
	PolicyVersion uint32
}

// PolicyStatusResponse is returned by GetPolicyStatus.
type PolicyStatusResponse struct {
	// ActiveVersion is the policy version currently loaded by the sandbox proxy.
	ActiveVersion uint32
	// Status is the load status of the queried version: "pending", "loaded", "failed", "superseded".
	Status string
}

// PolicyMergeOp describes a single incremental change to a sandbox's network policy.
type PolicyMergeOp struct {
	// Type is the operation type.
	Type PolicyMergeOpType
	// RuleName is the network policy rule name (e.g., "astonish-egress").
	RuleName string
	// Endpoint is used for AddEndpoint operations.
	Endpoint *EndpointSpec
	// Binary is used for AddEndpoint operations (path glob for allowed binaries).
	Binary string
}

// PolicyMergeOpType enumerates the supported merge operations.
type PolicyMergeOpType int

const (
	// PolicyMergeAddEndpoint adds an endpoint to an existing network rule.
	PolicyMergeAddEndpoint PolicyMergeOpType = iota
	// PolicyMergeRemoveEndpoint removes an endpoint from an existing network rule.
	PolicyMergeRemoveEndpoint
)

// WatchOpts configures what events to receive on a WatchSandbox stream.
type WatchOpts struct {
	// FollowEvents subscribes to platform events (including denial notifications).
	FollowEvents bool
	// EventTail replays the last N events before live streaming.
	EventTail uint32
}

// SandboxEventStream is a server-streaming connection delivering sandbox events.
type SandboxEventStream interface {
	// Recv blocks until the next event is available or the stream ends.
	Recv() (*SandboxEvent, error)
	// Close terminates the stream.
	Close() error
}

// SandboxEvent is a single event from a WatchSandbox stream.
type SandboxEvent struct {
	// Type identifies the event payload.
	Type SandboxEventType
	// DraftPolicyUpdate is populated when Type == SandboxEventDraftUpdate.
	DraftPolicyUpdate *DraftPolicyUpdateInfo
}

// SandboxEventType classifies sandbox stream events.
type SandboxEventType int

const (
	// SandboxEventDraftUpdate indicates new draft policy proposals are available.
	SandboxEventDraftUpdate SandboxEventType = iota
	// SandboxEventOther covers status, log, and warning events (not used by denial detection).
	SandboxEventOther
)

// DraftPolicyUpdateInfo carries details about a draft policy change notification.
type DraftPolicyUpdateInfo struct {
	// DraftVersion is the new draft version.
	DraftVersion uint64
	// NewChunks is the number of new chunks added.
	NewChunks uint32
	// TotalPending is the total number of pending chunks.
	TotalPending uint32
	// Summary is a human-readable description.
	Summary string
}

// CreateSandboxRequest contains the parameters for creating a new sandbox.
type CreateSandboxRequest struct {
	// Name is the desired sandbox name (e.g., "astn-sess-<session-id[:8]>").
	Name string

	// Image is the container image for the sandbox pod.
	Image string

	// Env is the environment variables to set in the sandbox container.
	Env map[string]string

	// Labels are the Kubernetes labels applied to the sandbox pod.
	Labels map[string]string

	// Policy is the sandbox security policy (Landlock, filesystem, process).
	// When nil, the gateway applies its default policy.
	Policy *SandboxPolicySpec

	// DriverConfig is the OpenShell driver-keyed config envelope (top-level
	// keys are compute-driver names such as "kubernetes"). The gateway
	// forwards only the block matching the active driver. Used for PVC
	// mounts (cert bundles) and other driver-owned create-time knobs.
	DriverConfig map[string]any

	// NodeSelector constrains where the pod is scheduled.
	NodeSelector map[string]string

	// Tolerations for the pod scheduling.
	Tolerations []Toleration
}

// SandboxPolicySpec defines the security policy applied inside the sandbox
// by the OpenShell supervisor. This is a domain-level type mapped to the
// proto SandboxPolicy in client_grpc.go.
type SandboxPolicySpec struct {
	// Version is the policy schema version (currently 1).
	Version uint32

	// Landlock controls Linux Landlock LSM enforcement.
	Landlock *LandlockSpec

	// Filesystem defines read/write access rules.
	Filesystem *FilesystemSpec

	// Process configures user/group identity for sandboxed processes.
	Process *ProcessSpec

	// NetworkPolicies defines named network access rules.
	NetworkPolicies map[string]*NetworkPolicySpec
}

// LandlockSpec controls Landlock enforcement behaviour.
type LandlockSpec struct {
	// Compatibility mode: "best_effort" degrades gracefully when the kernel
	// does not support Landlock; "hard_requirement" crashes if unavailable.
	Compatibility string
}

// FilesystemSpec restricts filesystem access within the sandbox.
type FilesystemSpec struct {
	// IncludeWorkdir automatically adds the workdir as read-write.
	IncludeWorkdir bool
	// ReadOnly paths accessible in read-only mode.
	ReadOnly []string
	// ReadWrite paths accessible in read-write mode.
	ReadWrite []string
}

// ProcessSpec configures the sandboxed process identity.
type ProcessSpec struct {
	RunAsUser  string
	RunAsGroup string
}

// NetworkPolicySpec defines allowed network endpoints for a named policy.
type NetworkPolicySpec struct {
	// Name is a human-readable identifier.
	Name string
	// Endpoints are structured host:port entries the sandbox may connect to.
	Endpoints []EndpointSpec
	// Binaries are path globs for binaries allowed to use these endpoints.
	// "/**" allows any binary.
	Binaries []string
}

// EndpointSpec is a single allowed network endpoint with host and port.
type EndpointSpec struct {
	Host string
	Port uint32 // 0 = defaults to 443 at proto mapping time
}

// Toleration mirrors corev1.Toleration for decoupling from k8s types.
type Toleration struct {
	Key      string
	Operator string
	Value    string
	Effect   string
}

// CreateSandboxResponse is returned after a sandbox is successfully created.
type CreateSandboxResponse struct {
	// SandboxID is the sandbox name (canonical lookup key for Get/Delete).
	SandboxID string

	// GatewayID is the gateway-assigned UUID (required for ExecSandbox).
	GatewayID string

	// PodName is the Kubernetes pod name (set by the K8s driver).
	PodName string
}

// SandboxState represents the lifecycle state of a sandbox.
type SandboxState string

const (
	SandboxStateCreating SandboxState = "creating"
	SandboxStateRunning  SandboxState = "running"
	SandboxStateStopped  SandboxState = "stopped"
	SandboxStateFailed   SandboxState = "failed"
	SandboxStateGone     SandboxState = "gone"
)

// SandboxStatus contains the current state of a sandbox.
type SandboxStatus struct {
	// Persisted template evidence for safe retained-home adoption.
	DriverConfig map[string]any
	Env          map[string]string
	Labels       map[string]string
	State        SandboxState
	Message      string
	PodName      string
	GatewayID    string // gateway-assigned UUID (for ExecSandbox)
}

// ExecRequest contains the parameters for executing a command in a sandbox.
type ExecRequest struct {
	// Command is the command to execute (e.g., ["/usr/local/bin/astonish", "node"]).
	Command []string

	// Env is additional environment variables for the command.
	Env map[string]string

	// WorkDir is the working directory for the command.
	WorkDir string

	// Stdin, when non-nil, is fed to the command's stdin.
	Stdin io.Reader

	// TTY requests a pseudo-terminal allocation.
	TTY bool

	// Cols and Rows set the initial terminal size when TTY is true.
	Cols int
	Rows int

	// SeparateStderr, when non-nil, receives stderr output instead of
	// mixing it into the stdout stream. Critical for machine protocols
	// (MCP JSON-RPC) where stderr contamination breaks parsing.
	SeparateStderr io.Writer
}

// ExecResponse is the result of a synchronous exec.
type ExecResponse struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ExecStreamConn is a bidirectional streaming connection for interactive
// or long-running exec sessions.
type ExecStreamConn interface {
	// Read reads from the command's stdout (and stderr if not separated).
	io.Reader

	// Write writes to the command's stdin.
	io.Writer

	// Resize sends a terminal resize event (for PTY sessions).
	Resize(cols, rows int) error

	// ExitCode returns the command's exit code after the stream is closed.
	// Returns -1 if the command hasn't exited yet.
	ExitCode() int

	// Close terminates the exec session and releases resources.
	Close() error
}

// SandboxSummary is a lightweight view of a sandbox returned by ListSandboxes.
// Used for GC reconciliation (comparing gateway state against DB session records).
type SandboxSummary struct {
	// ID is the gateway-assigned UUID.
	ID string

	// Name is the sandbox name (Kubernetes pod name, e.g., "astn-sess-abc12345").
	Name string

	// Labels are the Kubernetes labels on the sandbox pod.
	Labels map[string]string

	// CreatedAt is when the sandbox was created.
	CreatedAt time.Time

	// Phase is the sandbox lifecycle phase (e.g., "RUNNING", "CREATING", "FAILED").
	Phase string
}
