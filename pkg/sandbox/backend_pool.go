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
type backendNodeClient struct {
	backend    Backend
	sessionID  string   // set by BindSession; stable after first call
	templateID string   // may be empty → backend default
	layerChain []string // pre-resolved chain (K8s); nil → derive from templateID
	image      string   // per-template container image (OpenShell); empty → backend default
	limits     ResourceLimits
	sessionEnv map[string]string

	mu       sync.Mutex
	bound    bool          // BindSession has run
	bindDone chan struct{} // closed once the create/start round-trip finishes
	bindErr  error         // captured on bindDone close
	closed   bool          // Cleanup has run
	// networkAllowEndpoints are baked into OpenShell CreateSession when set
	// before BindSession completes provisioning (see SetNetworkAllowEndpoints).
	networkAllowEndpoints []NetworkAllowEndpoint
}

// newBackendNodeClient constructs a client. The session is NOT created until
// BindSession is called; the client is inert until then so the caller may
// defer provisioning until it knows the LLM actually needs a tool.
func newBackendNodeClient(b Backend, templateID string, layerChain []string, image string, limits ResourceLimits, sessionEnv map[string]string) *backendNodeClient {
	return &backendNodeClient{
		backend:    b,
		templateID: templateID,
		layerChain: layerChain,
		image:      image,
		limits:     limits,
		sessionEnv: sessionEnv,
	}
}

// BindSession creates + starts the backend session for sessionID if this is
// the first call. Subsequent calls (any sessionID) are no-ops — matches
// LazyNodeClient.BindSession semantics so ToolNodeClient behaves uniformly.
//
// The actual Backend.CreateSession / StartSession runs in a background
// goroutine so the LLM call overlaps with container provisioning, mirroring
// the Incus path's latency optimisation.
func (c *backendNodeClient) BindSession(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	if c.bound || c.closed {
		c.mu.Unlock()
		return
	}
	c.sessionID = sessionID
	c.bound = true
	c.bindDone = make(chan struct{})
	c.mu.Unlock()

	go c.provision(sessionID)
}

// resetBind clears a completed bind so BindSession can provision again.
// Used when the underlying sandbox was destroyed (e.g. ephemeral adaptive
// scheduler runs) but this client is still cached in the pool.
func (c *backendNodeClient) resetBind() {
	c.mu.Lock()
	done := c.bindDone
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	if done != nil {
		<-done // wait for in-flight provision before flipping bound
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.bound = false
	c.bindDone = nil
	c.bindErr = nil
	c.sessionID = ""
}

// sessionStillReady reports whether the backend still has a usable sandbox
// for sessionID after a prior successful bind.
func (c *backendNodeClient) sessionStillReady(sessionID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state, err := c.backend.SessionState(ctx, sessionID)
	if err != nil {
		return false
	}
	switch state {
	case SessionStateRunning, SessionStateCreating, SessionStateResuming:
		return true
	default:
		return false
	}
}

// EnsureReady satisfies ToolNodeClient. It triggers provisioning (via
// BindSession) and blocks until the sandbox session is fully running.
// Returns nil on success or the provisioning error.
//
// If a prior bind completed but the sandbox is gone (destroyed externally
// or by an ephemeral adaptive run), EnsureReady resets the bind and
// provisions a fresh session. An in-flight first bind is not treated as
// "gone" — SessionState would still be absent until CreateSession finishes.
func (c *backendNodeClient) EnsureReady(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("backend node client: no session bound (empty session ID)")
	}

	c.mu.Lock()
	alreadyBound := c.bound && !c.closed
	done := c.bindDone
	c.mu.Unlock()

	if alreadyBound && done != nil {
		select {
		case <-done:
			// Prior bind finished — re-provision if the sandbox disappeared.
			c.mu.Lock()
			priorErr := c.bindErr
			c.mu.Unlock()
			if priorErr != nil || !c.sessionStillReady(sessionID) {
				slog.Info("backend node client: re-binding after sandbox gone",
					"component", "sandbox", "session", shortSession(sessionID))
				c.resetBind()
			}
		default:
			// First provision still running — wait below, do not reset.
		}
	}

	c.BindSession(sessionID)

	c.mu.Lock()
	done = c.bindDone
	closed := c.closed
	c.mu.Unlock()

	if done == nil {
		return fmt.Errorf("backend node client: no session bound (empty session ID)")
	}
	if closed {
		return fmt.Errorf("backend node client: closed")
	}
	<-done

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bindErr
}

// SetNetworkAllowEndpoints stashes DB allow rules for CreateSession. Must be
// called before BindSession's provision goroutine reads the spec (typically
// from NodeTool.ProcessRequest before BindSession). Ignored after bind starts
// if create already used an empty list — PreSeed-before-Call covers that case.
func (c *backendNodeClient) SetNetworkAllowEndpoints(endpoints []NetworkAllowEndpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bound {
		// Provision may already be running; still store for a late CreateSession
		// that has not yet copied the slice (provision reads under lock below).
		if len(c.networkAllowEndpoints) > 0 {
			return
		}
	}
	c.networkAllowEndpoints = append([]NetworkAllowEndpoint(nil), endpoints...)
}

// GetBackend exposes the underlying Backend for credential file PushFile and similar.
func (c *backendNodeClient) GetBackend() Backend {
	return c.backend
}

// provision does the CreateSession + StartSession round trip. Any error is
// captured on c.bindErr; bindDone is always closed exactly once.
func (c *backendNodeClient) provision(sessionID string) {
	defer close(c.bindDone)

	// Cap provisioning at the backend-exec timeout; CreateSession on a
	// sick cluster should fail loudly rather than block forever.
	ctx, cancel := context.WithTimeout(context.Background(), backendExecTimeout)
	defer cancel()

	spec := SessionSpec{
		SessionID:  sessionID,
		Type:       SessionTypeChat,
		TemplateID: c.templateID,
		LayerChain: c.layerChain,
		Image:      c.image,
		Limits:     c.limits,
		Env:        c.sessionEnv,
	}
	c.mu.Lock()
	if len(c.networkAllowEndpoints) > 0 {
		spec.NetworkAllowEndpoints = append([]NetworkAllowEndpoint(nil), c.networkAllowEndpoints...)
	}
	c.mu.Unlock()
	if _, err := c.backend.CreateSession(ctx, spec); err != nil {
		c.mu.Lock()
		c.bindErr = fmt.Errorf("backend create session: %w", err)
		c.mu.Unlock()
		return
	}
	// StartSession is idempotent per the Backend contract; calling it here
	// is defensive: some backends (mock) consider CreateSession sufficient,
	// others (k8s pod) need an explicit Start to move from Pending→Running.
	if err := c.backend.StartSession(ctx, sessionID); err != nil {
		c.mu.Lock()
		c.bindErr = fmt.Errorf("backend start session: %w", err)
		c.mu.Unlock()
		return
	}
	// Wait for the sandbox to reach Running state before allowing Exec.
	// Required by the Backend contract (see Backend.WaitForSessionReady doc):
	// "Callers that need to exec into a session immediately after
	// CreateSession MUST call this first."
	// On Incus this returns in <1s; on K8s/OpenShell it polls until the
	// pod reaches Running phase (image pull + scheduling may take seconds).
	if err := c.backend.WaitForSessionReady(ctx, sessionID); err != nil {
		c.mu.Lock()
		c.bindErr = fmt.Errorf("backend wait for session ready: %w", err)
		c.mu.Unlock()
		return
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
	// Safety net: ProcessRequest should have called BindSession already,
	// but the contract permits skipping it. Bind synchronously here.
	c.BindSession(sessionID)

	c.mu.Lock()
	done := c.bindDone
	closed := c.closed
	c.mu.Unlock()
	if done == nil {
		return nil, fmt.Errorf("backend node client: no session bound (empty session ID)")
	}
	if closed {
		return nil, fmt.Errorf("backend node client: closed")
	}
	<-done

	c.mu.Lock()
	if c.bindErr != nil {
		err := c.bindErr
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()

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
	res, err := c.backend.Exec(ctx, c.sessionID, spec)
	if err != nil {
		return nil, fmt.Errorf("backend exec: %w", err)
	}

	if len(res.Stderr) > 0 {
		slog.Debug("backend node stderr", "component", "sandbox",
			"session", shortSession(c.sessionID),
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
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	sessionID := c.sessionID
	done := c.bindDone
	c.mu.Unlock()

	// If bind never finished, wait briefly so we don't orphan a
	// half-created session.
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			slog.Warn("backend node client: bind did not complete before close",
				"component", "sandbox", "session", shortSession(sessionID))
		}
	}
	if sessionID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.backend.DestroySession(ctx, sessionID); err != nil {
		slog.Warn("backend destroy session failed", "component", "sandbox",
			"session", shortSession(sessionID), "error", err)
	}
}

// discard marks the client unusable without calling Backend.DestroySession.
// Used by pool.Remove after the caller already destroyed the sandbox
// (ephemeral adaptive runs) so we do not race a second destroy.
func (c *backendNodeClient) discard() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

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
	for scanner.Scan() {
		line := scanner.Bytes()
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
	backend Backend
	limits  ResourceLimits

	mu      sync.Mutex
	clients map[string]*backendNodeClient // keyed by sessionID
	closed  bool
}

// NewBackendPool constructs a ToolNodePool backed by the given Backend.
// Passing a nil backend returns nil so callers can check for nil pool
// regardless of backend kind.
//
// limits is the per-session resource cap applied on CreateSession. Pass
// EffectiveLimits(&cfg.Sandbox) for parity with the Incus path.
func NewBackendPool(b Backend, limits ResourceLimits) ToolNodePool {
	if b == nil {
		return nil
	}
	return &backendPool{
		backend: b,
		limits:  limits,
		clients: make(map[string]*backendNodeClient),
	}
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
