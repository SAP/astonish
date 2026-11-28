// Package sandbox — backend-agnostic ToolNodePool implementation.
//
// backendPool / backendNodeClient satisfy ToolNodePool / ToolNodeClient
// (node_interfaces.go) by delegating to an arbitrary sandbox.Backend. This
// is the Phase E slice-2 deliverable described in
// docs/architecture/sandbox-backends.md §11: a pool that NodeTool can
// consume WITHOUT knowing which backend (Incus, K8s, mock) fulfils it.
//
// Scope (slice 2):
//
//   - Only the tool-call hot path is wired here. Template build, fleet
//     provisioning, org networking etc. remain the caller's job (they
//     live on Backend directly).
//   - Per-call execution model: each Call() spawns a fresh
//     `astonish node` process inside the sandbox via Backend.Exec,
//     feeds it exactly one NDJSON request on stdin, reads one response
//     from stdout. This is slower than the persistent-NDJSON model used
//     by Incus's *NodeClient (one process, many calls) but avoids the
//     PTY canonical-mode CR/LF munging that would corrupt framing on
//     the K8s backend (exec.go:273 hardcodes Tty:true for interactive
//     streams; non-TTY stdin via Exec is framing-safe).
//
//     Trade-off is explicit: ~50ms overhead per tool call vs. the
//     correctness risk of PTY byte mangling. Revisit in Phase F if
//     per-call latency becomes a bottleneck — the most likely fix is a
//     backend-side FIFO-based persistent worker, not reverting to PTY.
//
//   - Session lifecycle: BindSession creates and starts a session
//     lazily, once, per sessionID (matches LazyNodeClient semantics so
//     the interface contract is uniform across backends). Session
//     destruction happens on pool Cleanup — matching NodeClientPool.
//
// Non-goals (slice 2):
//
//   - NOT a drop-in replacement for *NodeClientPool. Chat still uses
//     the Incus-bound pool (chat_factory.go keeps the concrete type).
//   - NO org network provisioning, NO port exposure, NO save-as. Those
//     callers talk to Backend directly already.
//   - NO retry on "container gone" mid-call. Backend.Exec surfaces the
//     underlying error verbatim; higher-level retry policy stays in
//     flow/tool code, not here.

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// The NDJSON request/response shapes are defined once on the package in
// node.go (nodeRequest / nodeResponse). backend_pool.go reuses them so
// there is no drift between the Incus-bound *NodeClient path and the
// backend-agnostic path below. Both talk to the same `astonish node`
// server in cmd/astonish/node.go.

// backendExecTimeout caps an individual per-tool Exec. Tools that need longer
// (e.g. npm install under shell_command) should pass their own ctx via the
// tool args — this is a backstop against runaway exec streams on a broken
// backend, not a per-tool policy.
const backendExecTimeout = 5 * time.Minute

// backendNodeClient implements ToolNodeClient over a sandbox.Backend.
//
// One instance per sessionID. Construction is cheap (no I/O); BindSession
// triggers the first network round-trip. Safe for concurrent Call().
type backendReadinessState uint8

const (
	backendUnbound backendReadinessState = iota
	backendPreparing
	backendReady
	backendFailed
	backendClosed
)

// Each attempt owns its result and signal. Waiters never observe a later
// generation's result or close a channel that another generation replaced.
type backendPrepareAttempt struct {
	initial    bool // first provisioning has not successfully created the owner
	created    bool // initial CreateSession succeeded; published with done
	generation uint64
	done       chan struct{}
	cancel     context.CancelFunc
	err        error // published before done closes
}

type backendNodeClient struct {
	backend    Backend
	sessionID  string // immutable owner: pool construction or first direct bind
	templateID string
	layerChain []string
	image      string
	limits     ResourceLimits
	sessionEnv map[string]string
	botHome    *BotHomeBinding

	mu                    sync.Mutex
	state                 backendReadinessState
	generation            uint64
	attempt               *backendPrepareAttempt
	closedDone            chan struct{}
	networkAllowEndpoints []NetworkAllowEndpoint
}

func newBackendNodeClient(b Backend, templateID string, layerChain []string, image string, limits ResourceLimits, sessionEnv map[string]string) *backendNodeClient {
	env := make(map[string]string, len(sessionEnv))
	for k, v := range sessionEnv {
		env[k] = v
	}
	return &backendNodeClient{
		backend: b, templateID: templateID,
		layerChain: append([]string(nil), layerChain...), image: image,
		limits: limits, sessionEnv: env,
	}
}

// SandboxSessionID returns the stable physical owner, never a delegate ID.
func (c *backendNodeClient) SandboxSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// BindSession only warms the first attempt; readiness calls coordinate retries.
func (c *backendNodeClient) BindSession(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != backendUnbound {
		return
	}
	if c.sessionID == "" {
		c.sessionID = sessionID
	}
	c.startPrepareLocked()
}

// startPrepareLocked snapshots all inputs before starting backend I/O.
func (c *backendNodeClient) startPrepareLocked() *backendPrepareAttempt {
	c.generation++
	ctx, cancel := context.WithTimeout(context.Background(), backendExecTimeout)
	initial := c.state == backendUnbound || (c.state == backendFailed && c.attempt.initial && !c.attempt.created)
	a := &backendPrepareAttempt{initial: initial, generation: c.generation, done: make(chan struct{}), cancel: cancel}
	if c.closedDone == nil {
		c.closedDone = make(chan struct{})
	}
	c.attempt, c.state = a, backendPreparing
	env := make(map[string]string, len(c.sessionEnv))
	for k, v := range c.sessionEnv {
		env[k] = v
	}
	spec := SessionSpec{
		SessionID: c.sessionID, Type: SessionTypeChat, TemplateID: c.templateID,
		LayerChain: append([]string(nil), c.layerChain...), Image: c.image,
		Limits: c.limits, Env: env, BotHome: c.botHome.Clone(),
		NetworkAllowEndpoints: append([]NetworkAllowEndpoint(nil), c.networkAllowEndpoints...),
	}
	// BotHome is bound only after the caller authorizes the owner and run lease.
	// Carry that pinned identity into backend isolation metadata as well as the
	// retained-home mount. Personal mode intentionally has no org/team scope.
	if spec.BotHome != nil {
		spec.OrgSlug = spec.BotHome.Scope.OrgID
		spec.TeamSlug = spec.BotHome.Scope.TeamID
		spec.UserID = spec.BotHome.Scope.UserID
	}
	go c.provision(ctx, c.backend, spec, a)
	return a
}

func (c *backendNodeClient) EnsureReady(sessionID string) error {
	return c.EnsureReadyContext(context.Background(), sessionID)
}

// EnsureReadyContext joins a shared bounded preparation. Cancelling one waiter
// does not cancel preparation needed by another. A completed attempt is checked
// again on the next call, including backend overlay readiness when running.
func (c *backendNodeClient) EnsureReadyContext(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.state == backendClosed {
		c.mu.Unlock()
		return fmt.Errorf("backend node client: closed")
	}
	if c.sessionID == "" {
		if sessionID == "" {
			c.mu.Unlock()
			return fmt.Errorf("backend node client: no session bound (empty session ID)")
		}
		c.sessionID = sessionID
	}
	a := c.attempt
	if c.state != backendPreparing {
		a = c.startPrepareLocked()
	}
	closedDone := c.closedDone
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-closedDone:
		return fmt.Errorf("backend node client: closed")
	case <-a.done:
		c.mu.Lock()
		closed := c.state == backendClosed
		c.mu.Unlock()
		if closed {
			return fmt.Errorf("backend node client: closed")
		}
		return a.err
	}
}

// SetNetworkAllowEndpoints supplies allow rules for the next create snapshot.
// Already captured attempts are immutable; later recovery retains these rules.
func (c *backendNodeClient) SetNetworkAllowEndpoints(endpoints []NetworkAllowEndpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == backendClosed || (c.state != backendUnbound && len(c.networkAllowEndpoints) > 0) {
		return
	}
	c.networkAllowEndpoints = append([]NetworkAllowEndpoint(nil), endpoints...)
}

func (c *backendNodeClient) GetBackend() Backend { return c.backend }

func (c *backendNodeClient) provision(ctx context.Context, backend Backend, spec SessionSpec, a *backendPrepareAttempt) {
	defer a.cancel()
	err := func() error {
		// A new owner is not registered yet on authorization-enforcing backends.
		// Initial creation is intentional, not recovery inferred from an error.
		if a.initial {
			if _, err := backend.CreateSession(ctx, spec); err != nil {
				return fmt.Errorf("backend create session: %w", err)
			}
			a.created = true
			if err := backend.StartSession(ctx, spec.SessionID); err != nil {
				return fmt.Errorf("backend start session: %w", err)
			}
			if err := backend.WaitForSessionReady(ctx, spec.SessionID); err != nil {
				return fmt.Errorf("backend wait for session ready: %w", err)
			}
			return nil
		}
		return prepareBackendSession(ctx, backend, spec)
	}()
	c.mu.Lock()
	defer c.mu.Unlock()
	a.err = err
	if c.state != backendClosed && c.generation == a.generation {
		if err != nil {
			c.state = backendFailed
		} else {
			c.state = backendReady
		}
	}
	close(a.done)
}

func prepareBackendSession(ctx context.Context, backend Backend, spec SessionSpec) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := backend.SessionState(ctx, spec.SessionID)
		if err != nil {
			return fmt.Errorf("backend inspect session: %w", err)
		}
		switch state {
		case SessionStateGone:
			if _, err := backend.CreateSession(ctx, spec); err != nil {
				return fmt.Errorf("backend create session: %w", err)
			}
			if err := backend.StartSession(ctx, spec.SessionID); err != nil {
				return fmt.Errorf("backend start session: %w", err)
			}
		case SessionStateStopped: // Includes evicted sessions with preserved data.
			if err := backend.StartSession(ctx, spec.SessionID); err != nil {
				return fmt.Errorf("backend start session: %w", err)
			}
		case SessionStateRunning, SessionStateCreating, SessionStateResuming:
			// Running is not sufficient: the overlay/entrypoint must be ready.
		case SessionStateEvicting:
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		default:
			return fmt.Errorf("backend session has unsupported state %q", state)
		}
		if err := backend.WaitForSessionReady(ctx, spec.SessionID); err != nil {
			return fmt.Errorf("backend wait for session ready: %w", err)
		}
		return nil
	}
}

// Call executes toolName inside the bound sandbox and returns the raw JSON
// result. If BindSession has not been called (safety net), Call binds
// synchronously first.
//
// Execution shape: one Backend.Exec per call, running `astonish node`.
// Stdin carries a single NDJSON request; stdout carries the ready line plus
// one NDJSON response. Stderr is logged but not propagated — in-sandbox
// logs stay in-sandbox unless debug is explicitly wired.
func (c *backendNodeClient) Call(sessionID, toolName string, args map[string]interface{}) (json.RawMessage, error) {
	// Readiness belongs here as well as in NodeTool: direct callers must
	// recover before dispatch. Never retry after Exec may have started.
	if err := c.EnsureReady(sessionID); err != nil {
		return nil, err
	}
	owner := c.SandboxSessionID()

	// Build NDJSON request. Request ID is synthetic — the one-shot Exec
	// only ever sees a single request/response pair, but the node server
	// requires a non-empty id.
	req := nodeRequest{
		ID:   "1",
		Tool: toolName,
		Args: args,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal node request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	ctx, cancel := context.WithTimeout(context.Background(), backendExecTimeout)
	defer cancel()

	spec := ExecSpec{
		Command: []string{"astonish", "node"},
		Stdin:   bytes.NewReader(reqBytes),
	}
	res, err := c.backend.Exec(ctx, owner, spec)
	if err != nil {
		return nil, fmt.Errorf("backend exec: %w", err)
	}

	if len(res.Stderr) > 0 {
		slog.Debug("backend node stderr", "component", "sandbox",
			"session", shortSession(owner),
			"tool", toolName,
			"stderr", truncate(string(res.Stderr), 512))
	}

	// Parse NDJSON output. The node server emits:
	//   {"ready":true}\n
	//   {"id":"1","result":...}  OR  {"id":"1","error":"..."}\n
	// Skip any line that lacks an "id" field (so the ready line is ignored
	// regardless of ordering), take the first line that has one.
	return parseNodeResponse(res.Stdout, res.ExitCode)
}

// close is called by the pool on Cleanup. It destroys the backend session.
// Safe to call multiple times; subsequent calls are no-ops.
func (c *backendNodeClient) close() {
	c.mu.Lock()
	if c.state == backendClosed {
		c.mu.Unlock()
		return
	}
	c.markClosedLocked()
	sessionID, a := c.sessionID, c.attempt
	c.mu.Unlock()
	// Cancellation prevents normal backends from creating after teardown. If a
	// backend ignores cancellation, defer destruction until it actually exits.
	destroy := func() {
		if sessionID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.backend.DestroySession(ctx, sessionID); err != nil {
			slog.Warn("backend destroy session failed", "component", "sandbox", "session", shortSession(sessionID), "error", err)
		}
	}
	if a != nil {
		select {
		case <-a.done:
		case <-time.After(10 * time.Second):
			slog.Warn("backend node client: preparation did not complete before close", "component", "sandbox", "session", shortSession(sessionID))
			go func() { <-a.done; destroy() }()
			return
		}
	}
	destroy()
}

// markClosedLocked is terminal: a late preparation cannot publish ready.
func (c *backendNodeClient) markClosedLocked() {
	c.state = backendClosed
	if c.closedDone == nil {
		c.closedDone = make(chan struct{})
	}
	close(c.closedDone)
	if c.attempt != nil {
		c.attempt.cancel()
	}
}

// discard marks the client unusable without destroying the stable owner.
func (c *backendNodeClient) discard() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != backendClosed {
		c.markClosedLocked()
	}
}

// maxNodeResponseLineBytes bounds one NDJSON line, excluding its LF/CRLF
// delimiter. Tool results routinely exceed bufio.Scanner's 64 KiB default.
const maxNodeResponseLineBytes = 10 * 1024 * 1024

// parseNodeResponse scans NDJSON stdout for the first response line with an
// "id" field and returns its Result / Error. The ready message is ignored
// (it has no id). exitCode is included in the error when there is no parsable
// response — helps operators diagnose container-side panics.
//
// The implementation is line-oriented: it splits stdout on newlines, attempts
// JSON unmarshal per line, and silently skips lines that are not valid JSON.
// This makes it robust to non-JSON prefix garbage (e.g. ANSI banners, locale
// messages, debug prints) that may appear on stdout before or between the
// protocol messages.
func parseNodeResponse(stdout []byte, exitCode int) (json.RawMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	// Allow the full JSON budget plus CRLF. Check the actual token length
	// separately so the delimiter allowance cannot admit oversized JSON.
	scanner.Buffer(make([]byte, 64*1024), maxNodeResponseLineBytes+2)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > maxNodeResponseLineBytes {
			return nil, fmt.Errorf("backend exec: node response line exceeds %d bytes (exit=%d)", maxNodeResponseLineBytes, exitCode)
		}
		if len(line) == 0 {
			continue
		}

		// Fast reject: valid NDJSON always starts with '{'.
		if line[0] != '{' {
			continue
		}

		var probe struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			// Looks like it starts with '{' but isn't valid JSON — skip.
			continue
		}
		if probe.ID == "" {
			continue // ready line or diagnostic
		}
		if probe.Error != "" {
			return nil, fmt.Errorf("node: %s", probe.Error)
		}
		return probe.Result, nil
	}
	if err := scanner.Err(); err != nil {
		if err == bufio.ErrTooLong {
			return nil, fmt.Errorf("backend exec: node response line exceeds %d bytes (exit=%d): %w", maxNodeResponseLineBytes, exitCode, err)
		}
		return nil, fmt.Errorf("backend exec: scan node response (exit=%d): %w", exitCode, err)
	}
	// If we get here, no valid response line was found.
	// Include a truncated sample of stdout for diagnostic context.
	sample := string(stdout)
	if len(sample) > 256 {
		sample = sample[:256] + "..."
	}
	return nil, fmt.Errorf("backend exec: no node response in stdout (exit=%d, %d bytes, sample=%q)",
		exitCode, len(stdout), sample)
}

func shortSession(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// Pool
// ---------------------------------------------------------------------------

// backendPool is a ToolNodePool backed by a single sandbox.Backend. One pool
// per flow/chat context; the pool manages per-session *backendNodeClient
// instances and destroys them on Cleanup.
//
// The pool does NOT own the Backend. Multiple pools may share a Backend
// (e.g. one pool per concurrent flow), and the Backend outlives any pool.
// Cleanup only tears down sessions created by this pool's clients.
type backendPool struct {
	backend  Backend
	limits   ResourceLimits
	registry *SessionRegistry

	mu      sync.Mutex
	clients map[string]*backendNodeClient // keyed by sessionID
	homes   map[string]*BotHomeBinding    // staged without pinning template selection
	closed  bool
}

// NewBackendPool constructs a ToolNodePool backed by the given Backend.
// Passing a nil backend returns nil so callers can check for nil pool
// regardless of backend kind.
//
// limits is the per-session resource cap applied on CreateSession. Pass
// EffectiveLimits(&cfg.Sandbox) for parity with the Incus path.
func NewBackendPool(b Backend, limits ResourceLimits, registries ...*SessionRegistry) ToolNodePool {
	if b == nil {
		return nil
	}
	var registry *SessionRegistry
	if len(registries) > 0 {
		registry = registries[0]
	}
	return &backendPool{
		backend:  b,
		limits:   limits,
		registry: registry,
		clients:  make(map[string]*backendNodeClient),
	}
}

// SetBotHome must run before BindSession/EnsureReady. Rebinding the same value
// is idempotent; changing a prepared owner is rejected, including via aliases.
func (p *backendPool) SetBotHome(sessionID string, binding *BotHomeBinding) error {
	if binding == nil {
		return nil
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if _, ok := p.backend.(BotHomeBackend); !ok || p.backend.Kind() != binding.Backend {
		return ErrBotHomeUnsupported
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || sessionID == "" {
		return fmt.Errorf("sandbox: bot home requires an open pool and session ID")
	}
	c := p.clients[sessionID]
	if c == nil {
		if p.homes == nil {
			p.homes = make(map[string]*BotHomeBinding)
		}
		if old := p.homes[sessionID]; old != nil && !old.Equal(binding) {
			return fmt.Errorf("sandbox: bot home binding is immutable")
		}
		p.homes[sessionID] = binding.Clone()
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.botHome.Equal(binding) {
		return nil
	}
	if c.state != backendUnbound || c.botHome != nil {
		return fmt.Errorf("sandbox: bot home binding is immutable after binding or preparation")
	}
	c.botHome = binding.Clone()
	return nil
}

func (p *backendPool) GetOrCreate(sessionID string) ToolNodeClient {
	return p.getOrCreate(sessionID, "", nil, "")
}

func (p *backendPool) GetOrCreateWithTemplate(sessionID, template string) ToolNodeClient {
	return p.getOrCreate(sessionID, template, nil, "")
}

func (p *backendPool) GetOrCreateWithChain(sessionID, template string, chain []string) ToolNodeClient {
	return p.getOrCreate(sessionID, template, chain, "")
}

func (p *backendPool) GetOrCreateWithImage(sessionID, template string, chain []string, image string) ToolNodeClient {
	return p.getOrCreate(sessionID, template, chain, image)
}

// GetBackend returns the underlying Backend used by this pool. MCP lazy
// toolsets use this to create BackendMCPTransport instances for interactive
// exec streams into sandbox sessions.
func (p *backendPool) GetBackend() Backend {
	return p.backend
}

func (p *backendPool) SetSessionScope(sessionID, orgSlug, teamSlug string) {
	if p.registry != nil {
		p.registry.SetSessionScope(sessionID, orgSlug, teamSlug)
	}
}

func (p *backendPool) getOrCreate(sessionID, template string, chain []string, image string) ToolNodeClient {
	if sessionID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if c, ok := p.clients[sessionID]; ok {
		// Template is pinned at create time (LazyNodeClient contract).
		// Silently ignore a mismatched override on re-fetch.
		if template != "" && template != c.templateID {
			slog.Debug("backend pool: ignoring template override on existing client",
				"component", "sandbox",
				"session", shortSession(sessionID),
				"existing", c.templateID,
				"requested", template)
		}
		return c
	}
	c := newBackendNodeClient(p.backend, template, chain, image, p.limits, nil)
	c.sessionID = sessionID // pin before any aliased child can bind
	c.botHome = p.homes[sessionID].Clone()
	delete(p.homes, sessionID)
	p.clients[sessionID] = c
	return c
}

// Remove drops the cached client for sessionID (and any Alias entries that
// share the same client). Does NOT destroy the backend session — callers
// that own lifecycle (adaptive DestroySandbox) destroy first, then Remove.
func (p *backendPool) Remove(sessionID string) {
	if sessionID == "" {
		return
	}
	p.mu.Lock()
	delete(p.homes, sessionID)
	c, ok := p.clients[sessionID]
	if !ok {
		p.mu.Unlock()
		return
	}
	for id, other := range p.clients {
		if other == c {
			delete(p.clients, id)
		}
	}
	p.mu.Unlock()
	c.discard()
}

// Cleanup destroys every client the pool vended, then marks the pool
// closed. After Cleanup, GetOrCreate returns nil. The pool's Backend is
// NOT closed (it is not owned).
func (p *backendPool) Cleanup() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	clients := p.clients
	p.clients = nil
	p.homes = nil
	p.mu.Unlock()

	// Destroy in parallel — session destroy can be slow (k8s pod delete +
	// CephFS upper-layer persist) and flow Cleanup() blocks the caller.
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(cc *backendNodeClient) {
			defer wg.Done()
			cc.close()
		}(c)
	}
	wg.Wait()
}

// Alias maps a child session ID to the same backendNodeClient as the parent
// session. This enables sub-agents (which get fresh session IDs via
// delegate_task) to share the parent's sandbox pod instead of creating a new
// one. Thread-safe.
//
// If the parent session has no client yet, Alias is a no-op — the child will
// create its own sandbox via getOrCreate on the first tool call.
func (p *backendPool) Alias(childSessionID, parentSessionID string) {
	if childSessionID == "" || parentSessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	parent, ok := p.clients[parentSessionID]
	if !ok || parent == nil {
		return
	}
	p.clients[childSessionID] = parent
}

// Compile-time assertions mirroring node_interfaces.go's block for
// *LazyNodeClient. Keep these next to the concrete types so breakage
// surfaces at the defining package, not the consumer.
var (
	_ ToolNodeClient = (*backendNodeClient)(nil)
	_ ToolNodePool   = (*backendPool)(nil)
	_ ToolNodePool   = (*singleClientPool)(nil)
)

// NewPinnedBackendClient creates a backend node client with optional session env
// for fleet sessions that pin to a single sandbox session ID.
func NewPinnedBackendClient(b Backend, templateID string, sessionEnv map[string]string, limits ResourceLimits) ToolNodeClient {
	return newBackendNodeClient(b, templateID, nil, "", limits, sessionEnv)
}

// singleClientPool implements ToolNodePool with one pre-bound client (fleet sessions).
type singleClientPool struct {
	client  ToolNodeClient
	backend Backend
	closed  bool
}

// NewSingleClientPool returns a ToolNodePool that always vends the same client.
func NewSingleClientPool(client ToolNodeClient, backend Backend) ToolNodePool {
	return &singleClientPool{client: client, backend: backend}
}

func (p *singleClientPool) GetOrCreate(_ string) ToolNodeClient {
	if p == nil || p.closed {
		return nil
	}
	return p.client
}

func (p *singleClientPool) GetOrCreateWithTemplate(_ string, _ string) ToolNodeClient {
	return p.GetOrCreate("")
}

func (p *singleClientPool) GetOrCreateWithChain(_ string, _ string, _ []string) ToolNodeClient {
	return p.GetOrCreate("")
}

func (p *singleClientPool) GetOrCreateWithImage(_ string, _ string, _ []string, _ string) ToolNodeClient {
	return p.GetOrCreate("")
}

func (p *singleClientPool) GetBackend() Backend {
	if p == nil {
		return nil
	}
	return p.backend
}

func (p *singleClientPool) Cleanup() {
	p.closed = true
}

func (p *singleClientPool) Alias(_, _ string) {}

// Remove is a no-op for fleet single-client pools (the client is pinned).
func (p *singleClientPool) Remove(_ string) {}

// SetSessionScope is a no-op for fleet single-client pools. Fleet sessions bind
// their own registry and org/team at wiring time (wireFleetSandbox), so there
// is no per-session tenant resolution to perform here.
func (p *singleClientPool) SetSessionScope(_, _, _ string) {}
