package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestIsTransientMCPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "context.Canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "context.DeadlineExceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("failed to init MCP session: %w", context.Canceled),
			want: true,
		},
		{
			name: "deeply wrapped context.Canceled",
			err:  fmt.Errorf("failed to get tools from MCP server 'codegraph': failed to list MCP tools: %w", fmt.Errorf("failed to init MCP session: %w", context.Canceled)),
			want: true,
		},
		{
			name: "wrapped context.DeadlineExceeded",
			err:  fmt.Errorf("operation timed out: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "connection refused",
			err:  fmt.Errorf("dial tcp 127.0.0.1:8080: connection refused"),
			want: true,
		},
		{
			name: "connection reset",
			err:  fmt.Errorf("read tcp: connection reset by peer"),
			want: true,
		},
		{
			name: "broken pipe",
			err:  fmt.Errorf("write: broken pipe"),
			want: true,
		},
		{
			name: "EOF",
			err:  fmt.Errorf("unexpected EOF"),
			want: true,
		},
		{
			name: "timeout",
			err:  fmt.Errorf("i/o timeout waiting for server"),
			want: true,
		},
		{
			name: "permanent - server not found",
			err:  fmt.Errorf("server 'foo' not found in config"),
			want: false,
		},
		{
			name: "permanent - credential resolution failed",
			err:  fmt.Errorf("failed to resolve credentials for MCP server 'foo': credential not found"),
			want: false,
		},
		{
			name: "permanent - server disabled",
			err:  fmt.Errorf("server 'foo' is disabled"),
			want: false,
		},
		{
			name: "permanent - unsupported transport",
			err:  fmt.Errorf("unsupported transport type: grpc"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isTransientMCPError(tt.err)
			if got != tt.want {
				t.Errorf("isTransientMCPError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestLazyMCPToolset_RetryOnTransientError(t *testing.T) {
	t.Parallel()

	// Create a toolset with a dummy config that will fail to start
	// (no real server running), simulating the state after a transient failure.
	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary-that-will-fail",
		},
		entries:   []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode: false,
		// Simulate a previous transient failure.
		started:     true,
		startErr:    fmt.Errorf("failed to get tools from MCP server 'test-server': %w", context.Canceled),
		retryCount:  0,
		lastAttempt: time.Now().Add(-5 * time.Second), // Well past backoff.
	}

	// Call ensureServerStarted — it should attempt a retry (and fail again
	// because there's no real server, but the retry count should increment).
	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// The call should fail (no real server), but the retry should have been attempted.
	if err == nil {
		t.Fatal("expected error from ensureServerStarted, got nil")
	}

	// Verify retry was attempted: retryCount should have incremented.
	toolset.mu.Lock()
	rc := toolset.retryCount
	la := toolset.lastAttempt
	toolset.mu.Unlock()

	if rc != 1 {
		t.Errorf("expected retryCount=1 after retry attempt, got %d", rc)
	}

	// lastAttempt should have been updated to a recent time.
	if time.Since(la) > 5*time.Second {
		t.Errorf("lastAttempt was not updated during retry (age: %v)", time.Since(la))
	}
}

func TestLazyMCPToolset_NoRetryOnPermanentError(t *testing.T) {
	t.Parallel()

	permanentErr := fmt.Errorf("server 'test-server' not found in config")

	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary",
		},
		entries:     []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode:   false,
		started:     true,
		startErr:    permanentErr,
		retryCount:  0,
		lastAttempt: time.Now().Add(-10 * time.Second), // Well past backoff.
	}

	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// Should return the same permanent error without retry.
	if err != permanentErr {
		t.Errorf("expected permanent error to be returned directly, got: %v", err)
	}

	// retryCount should not have changed.
	toolset.mu.Lock()
	rc := toolset.retryCount
	toolset.mu.Unlock()

	if rc != 0 {
		t.Errorf("expected retryCount=0 (no retry for permanent error), got %d", rc)
	}
}

func TestLazyMCPToolset_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()

	transientErr := fmt.Errorf("failed to start MCP server 'test-server': %w", context.Canceled)

	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary",
		},
		entries:     []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode:   false,
		started:     true,
		startErr:    transientErr,
		retryCount:  maxMCPRetries, // Already exhausted all retries.
		lastAttempt: time.Now().Add(-10 * time.Second),
	}

	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// Should return the cached error without retry.
	if err != transientErr {
		t.Errorf("expected cached transient error after max retries, got: %v", err)
	}

	// retryCount should not have changed.
	toolset.mu.Lock()
	rc := toolset.retryCount
	toolset.mu.Unlock()

	if rc != maxMCPRetries {
		t.Errorf("expected retryCount=%d (unchanged), got %d", maxMCPRetries, rc)
	}
}

func TestLazyMCPToolset_BackoffRespected(t *testing.T) {
	t.Parallel()

	transientErr := fmt.Errorf("failed to start MCP server 'test-server': %w", context.Canceled)

	toolset := &LazyMCPToolset{
		serverName: "test-server",
		serverCfg: config.MCPServerConfig{
			Command: "nonexistent-binary",
		},
		entries:     []cache.ToolEntry{{Name: "test_tool", Description: "test"}},
		debugMode:   false,
		started:     true,
		startErr:    transientErr,
		retryCount:  1,
		lastAttempt: time.Now(), // Just now — backoff not elapsed.
	}

	ctx := context.Background()
	err := toolset.ensureServerStarted(ctx, "")

	// Should return the cached error without retry (backoff not elapsed).
	if err != transientErr {
		t.Errorf("expected cached error during backoff period, got: %v", err)
	}

	// retryCount should not have changed.
	toolset.mu.Lock()
	rc := toolset.retryCount
	toolset.mu.Unlock()

	if rc != 1 {
		t.Errorf("expected retryCount=1 (unchanged during backoff), got %d", rc)
	}
}

func TestLazyMCPToolset_SuccessResetsRetryCount(t *testing.T) {
	t.Parallel()

	// This test verifies that a successful startup resets the retry count.
	// We can't easily test a full successful MCP startup without a real server,
	// but we can verify the field is set correctly after construction.
	toolset := NewLazyMCPToolset("test-server", nil, config.MCPServerConfig{}, false)
	if toolset.retryCount != 0 {
		t.Errorf("expected initial retryCount=0, got %d", toolset.retryCount)
	}
	if !toolset.lastAttempt.IsZero() {
		t.Errorf("expected initial lastAttempt to be zero, got %v", toolset.lastAttempt)
	}
}

// Embedding the interfaces makes unexpected calls fail rather than silently
// accepting a host fallback or a different pool routing path.
type lazyMCPTestClient struct {
	sandbox.ToolNodeClient
	owner    string
	readyIDs []string
	readyErr error
}

func (c *lazyMCPTestClient) SandboxSessionID() string { return c.owner }
func (c *lazyMCPTestClient) EnsureReady(id string) error {
	c.readyIDs = append(c.readyIDs, id)
	return c.readyErr
}
func (c *lazyMCPTestClient) EnsureReadyContext(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.EnsureReady(id)
}

type lazyMCPTestPool struct {
	sandbox.ToolNodePool
	client                   *lazyMCPTestClient
	backend                  *lazyMCPTestBackend
	logical, template, image string
	chain                    []string
	org, team                string
}

func (p *lazyMCPTestPool) SetSessionScope(id, org, team string) {
	p.logical, p.org, p.team = id, org, team
}
func (p *lazyMCPTestPool) GetOrCreateWithTemplate(id, tpl string) sandbox.ToolNodeClient {
	p.logical, p.template = id, tpl
	return p.client
}

func (p *lazyMCPTestPool) GetBackend() sandbox.Backend { return p.backend }
func (p *lazyMCPTestPool) GetOrCreate(id string) sandbox.ToolNodeClient {
	p.logical = id
	return p.client
}
func (p *lazyMCPTestPool) GetOrCreateWithImage(id, tpl string, chain []string, image string) sandbox.ToolNodeClient {
	p.logical, p.template, p.chain, p.image = id, tpl, chain, image
	return p.client
}

type lazyMCPTestBackend struct {
	sandbox.Backend
	owners        []string
	serve         bool
	failStarts    int
	failCalls     atomic.Bool
	failHandshake atomic.Bool
	calls         atomic.Int32
	streams       []net.Conn
}

func (b *lazyMCPTestBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *lazyMCPTestBackend) ExecStreaming(_ context.Context, id string, _ sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	b.owners = append(b.owners, id)
	if b.failStarts > 0 {
		b.failStarts--
		return nil, io.EOF
	}
	if !b.serve {
		return nil, fmt.Errorf("fixture permanent startup failure")
	}
	client, server := net.Pipe()
	b.streams = append(b.streams, server)
	go func() {
		defer server.Close()
		dec, enc := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if dec.Decode(&msg) != nil {
				return
			}
			if len(msg.ID) == 0 {
				continue
			}
			var result any
			switch msg.Method {
			case "initialize":
				if b.failHandshake.Swap(false) {
					return
				}
				result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "read_owner", "description": "fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
			case "tools/call":
				b.calls.Add(1)
				if b.failCalls.Load() {
					return
				} // uncertain execution: effect happened, response lost
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": id}}}
			default:
				result = map[string]any{}
			}
			if enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result}) != nil {
				return
			}
		}
	}()
	return &lazyMCPFixtureStream{Conn: client}, nil
}

type lazyMCPFixtureStream struct{ net.Conn }

func (*lazyMCPFixtureStream) Resize(int, int) error { return nil }
func (*lazyMCPFixtureStream) Wait() (int, error)    { return 0, nil }
func lazyMCPRoutingFixture(t *testing.T) (*LazyMCPToolset, *lazyMCPTestPool) {
	t.Helper()
	p := &lazyMCPTestPool{client: &lazyMCPTestClient{owner: "parent"}, backend: &lazyMCPTestBackend{}}
	l := NewLazyMCPToolset("fixture", nil, config.MCPServerConfig{Command: "fixture-mcp"}, false)
	l.SetSandboxPool(p)
	t.Cleanup(l.Cleanup)
	return l, p
}
func TestLazyMCPToolset_SandboxAliasUsesCanonicalOwner(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	if err := l.ensureServerStarted(context.Background(), "child"); err == nil {
		t.Fatal("expected fixture startup error")
	}
	if len(p.backend.owners) != 1 || p.backend.owners[0] != "parent" {
		t.Fatalf("physical startup owners = %v", p.backend.owners)
	}
	if p.logical != "child" || len(p.client.readyIDs) != 1 || p.client.readyIDs[0] != "parent" {
		t.Fatalf("logical=%q readiness=%v", p.logical, p.client.readyIDs)
	}
}
func TestLazyMCPToolset_SandboxStartupPreservesContextRouting(t *testing.T) {
	for _, imageRoute := range []bool{false, true} {
		t.Run(fmt.Sprintf("image=%t", imageRoute), func(t *testing.T) {
			l, p := lazyMCPRoutingFixture(t)
			ctx := store.WithSandboxTemplate(context.Background(), "custom")
			ctx = store.WithOrgSlug(ctx, "org")
			ctx = store.WithTeamSlug(ctx, "team")
			if imageRoute {
				ctx = store.WithSandboxLayerChain(ctx, []string{"@base", "layer"})
				ctx = store.WithSandboxImage(ctx, "custom-image")
			}
			_ = l.ensureServerStarted(ctx, "child")
			if p.logical != "child" || p.template != "custom" || p.org != "org" || p.team != "team" {
				t.Fatalf("routing logical=%q template=%q org=%q team=%q", p.logical, p.template, p.org, p.team)
			}
			if imageRoute && (len(p.chain) != 2 || p.image != "custom-image") {
				t.Fatalf("routing image=%q chain=%v", p.image, p.chain)
			}
		})
	}
}
func TestLazyMCPToolset_SandboxMissingIdentityNeverUsesHost(t *testing.T) {
	l, _ := lazyMCPRoutingFixture(t)
	if err := l.ensureServerStarted(context.Background(), ""); err == nil {
		t.Fatal("missing sandbox identity accepted")
	}
	if l.started || l.manager != nil {
		t.Fatal("sandbox fell back to host startup")
	}
}

func invokeLazyMCP(t *testing.T, l *LazyMCPToolset, id string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &lazyProxyTool{toolset: l, entry: &cache.ToolEntry{Name: "read_owner"}}
	_, err := p.Run(&minimalReadonlyContext{Context: ctx, sessionID: id}, map[string]any{})
	return err
}
func TestLazyMCPToolset_SharedOwnerKeepsLogicalSessionState(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.serve = true
	for _, id := range []string{"parent", "child"} {
		if err := invokeLazyMCP(t, l, id); err != nil {
			t.Fatal(err)
		}
	}
	parent, child := l.perSession["parent"], l.perSession["child"]
	if parent == child || parent.transport == child.transport || parent.ownerID != "parent" || child.ownerID != "parent" {
		t.Fatal("logical states not isolated from shared physical owner")
	}
	l.CleanupSession("child")
	if !child.transport.IsClosed() || parent.transport.IsClosed() {
		t.Fatal("logical cleanup affected wrong transport")
	}
	if err := invokeLazyMCP(t, l, "parent"); err != nil {
		t.Fatal(err)
	}
	if len(p.backend.owners) != 2 {
		t.Fatalf("healthy parent was restarted: %v", p.backend.owners)
	}
	if len(p.client.readyIDs) != 3 {
		t.Fatalf("cached state skipped readiness: %v", p.client.readyIDs)
	}
}
func TestLazyMCPToolset_SandboxReconnectsBeforeNextInvocation(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.serve = true
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	old := l.perSession["child"].transport
	_ = p.backend.streams[0].Close()
	deadline := time.Now().Add(time.Second)
	for !old.IsClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !old.IsClosed() {
		t.Fatal("EOF did not become terminal")
	}
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	if len(p.backend.owners) != 2 || p.backend.owners[1] != "parent" || p.backend.calls.Load() != 2 {
		t.Fatalf("starts=%v calls=%d", p.backend.owners, p.backend.calls.Load())
	}
}
func TestLazyMCPToolset_DoesNotReplaySentMethod(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.serve = true
	p.backend.failCalls.Store(true)
	if err := invokeLazyMCP(t, l, "child"); err == nil {
		t.Fatal("lost response must surface error")
	}
	if p.backend.calls.Load() != 1 || len(p.backend.owners) != 1 {
		t.Fatal("uncertain method was replayed")
	}
	p.backend.failCalls.Store(false)
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	if p.backend.calls.Load() != 2 || len(p.backend.owners) != 2 {
		t.Fatal("next invocation did not reconnect")
	}
}
func TestLazyMCPToolset_SandboxRetriesOnlyStartup(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.serve = true
	p.backend.failStarts = 1
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	if len(p.backend.owners) != 2 || p.backend.calls.Load() != 1 {
		t.Fatal("startup retry must dispatch method exactly once")
	}
}
func TestLazyMCPToolset_SandboxReadinessFailureBlocksCachedDispatch(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.serve = true
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	p.client.readyErr = errors.New("overlay not ready")
	if err := invokeLazyMCP(t, l, "child"); err == nil {
		t.Fatal("cached tool bypassed readiness")
	}
	if p.backend.calls.Load() != 1 {
		t.Fatal("method dispatched despite readiness failure")
	}
	p.client.readyErr = nil
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	if len(p.backend.owners) != 1 || p.backend.calls.Load() != 2 {
		t.Fatal("readiness failure discarded healthy connection")
	}
}

func TestLazyMCPToolset_SandboxRetriesHandshakeBeforeDispatch(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.serve = true
	p.backend.failHandshake.Store(true)
	if err := invokeLazyMCP(t, l, "child"); err != nil {
		t.Fatal(err)
	}
	if len(p.backend.owners) != 2 || p.backend.calls.Load() != 1 {
		t.Fatal("handshake retry must dispatch user method exactly once")
	}
}

func TestLazyMCPToolset_SandboxConfigFailureIsPermanent(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	l.serverCfg.Command = ""
	for i := 0; i < 2; i++ {
		if err := l.ensureServerStarted(context.Background(), "child"); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if len(p.backend.owners) != 0 || !l.perSession["child"].permanent || l.manager != nil {
		t.Fatal("permanent configuration failure started a process")
	}
}

func TestLazyMCPToolset_SandboxStartupRetryBoundedByContext(t *testing.T) {
	l, p := lazyMCPRoutingFixture(t)
	p.backend.failStarts = 100
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.ensureServerStarted(ctx, "child"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if len(p.backend.owners) != 1 {
		t.Fatal("backoff ignored cancellation")
	}
}
