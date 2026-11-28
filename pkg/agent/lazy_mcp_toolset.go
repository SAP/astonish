package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
)

const (
	// maxMCPRetries is the maximum number of retry attempts for transient MCP
	// server failures before giving up permanently.
	maxMCPRetries = 3

	// mcpRetryBackoff is the minimum time between retry attempts to avoid
	// hammering a server that may be recovering.
	mcpRetryBackoff = 2 * time.Second
)

// sessionMCPState holds MCP server state for a single session.
// In sandbox mode, each logical session gets its own MCP process, but delegates
// can share the parent's physical sandbox. State and cleanup stay logical.
type sessionMCPState struct {
	transport *sandbox.BackendMCPTransport
	liveTools map[string]tool.Tool
	startErr  error
	started   bool
	ownerID   string // physical sandbox; map key remains the logical session
	permanent bool   // configuration/credential errors must not be retried
}

// isTransientMCPError returns true if the error is likely transient and worth
// retrying. Transient errors include context cancellation, timeouts, and
// network-level failures that may resolve on their own.
func isTransientMCPError(err error) bool {
	if err == nil {
		return false
	}

	// Check standard context errors (works through wrapping via errors.Is).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check for network-level errors that are commonly transient.
	// These may come from the MCP SDK transport layer wrapped in fmt.Errorf.
	msg := strings.ToLower(err.Error())
	transientPatterns := []string{
		"connection refused",
		"connection reset",
		"broken pipe",
		"eof",
		"timeout",
		"i/o timeout",
	}
	for _, pattern := range transientPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}

	return false
}

// LazyMCPToolset presents cached MCP tools without connecting to the server.
// At startup, it creates lightweight proxy tools from cached metadata (instant).
// When a tool is actually invoked by the LLM, it lazily starts the real MCP
// server, gets the live tool, and executes it. The connection is kept alive
// for subsequent calls in the same session.
//
// Sandbox awareness: when a sandbox Backend is set via SetSandboxBackend(),
// stdio transport MCP servers are started inside the session's container/pod
// using BackendMCPTransport instead of on the host. SSE transport servers always
// stay on the host (they connect to remote URLs).
//
// Per-session state: in sandbox mode, each session gets its own MCP server
// process (running in its canonical owner's sandbox). The per-session state is stored in
// the perSession map. In host mode, a single global state is used (the host
// runs one MCP server process shared across all sessions).
//
// Retry resilience (host mode): transient startup failures (context canceled,
// timeouts, connection refused, etc.) are retried up to maxMCPRetries times
// with mcpRetryBackoff between attempts. Permanent errors (config not found,
// server disabled, credential resolution failures) are never retried.
// In sandbox mode, callers use CleanupSession to remove per-session state,
// which allows re-initialization on the next tool call.
type LazyMCPToolset struct {
	serverName string
	serverCfg  config.MCPServerConfig
	entries    []cache.ToolEntry
	debugMode  bool

	// sandboxBackend, when non-nil, causes stdio MCP servers to be started
	// inside the session's container/pod instead of on the host.
	// Works for both Incus and K8s backends via the abstract Backend interface.
	sandboxBackend sandbox.Backend

	// sandboxPool, when non-nil, is used to ensure the container is ready
	// before starting the MCP server (pool manages per-session containers).
	sandboxPool sandbox.ToolNodePool

	// sandboxClient, when non-nil, is used for fleet sessions where each
	// fleet session has its own dedicated container.
	sandboxClient *sandbox.LazyNodeClient

	// credentialResolver expands {{CREDENTIAL:name:field}} in MCP env at
	// process start. Context-scoped stores take precedence when present.
	credentialResolver credentials.CredentialResolver

	// --- Host mode state (single global MCP server) ---
	mu          sync.Mutex
	manager     *mcp.Manager         // host-mode MCP manager
	liveTools   map[string]tool.Tool // host-mode: name -> real MCP tool
	startErr    error                // host-mode: cached error if server failed to start
	started     bool                 // host-mode: whether the server was started
	retryCount  int                  // host-mode: number of retry attempts after transient failures
	lastAttempt time.Time            // host-mode: when the last startup attempt was made

	// --- Sandbox mode state (per-session MCP servers) ---
	// perSession is keyed by session ID and holds the MCP state for each session.
	// Protected by mu. Only populated when sandboxBackend is set.
	perSession map[string]*sessionMCPState
}

// NewLazyMCPToolset creates a toolset that uses cached metadata for declarations
// and lazily connects to the MCP server only when a tool is actually invoked.
func (*LazyMCPToolset) BotMetadataOnly() bool { return true }

func NewLazyMCPToolset(serverName string, entries []cache.ToolEntry,
	serverCfg config.MCPServerConfig, debugMode bool) *LazyMCPToolset {
	serverCfg.Args = slices.Clone(serverCfg.Args)
	serverCfg.Env = maps.Clone(serverCfg.Env)
	if serverCfg.Enabled != nil {
		enabled := *serverCfg.Enabled
		serverCfg.Enabled = &enabled
	}
	entries = slices.Clone(entries)
	for i := range entries {
		entries[i].InputSchema = slices.Clone(entries[i].InputSchema)
	}
	return &LazyMCPToolset{
		serverName: serverName,
		serverCfg:  serverCfg,
		entries:    entries,
		debugMode:  debugMode,
	}
}

// SetSandboxPool enables sandbox-aware MCP execution. Stdio servers run inside
// the session sandbox; remote transports use the same sandbox's node protocol,
// credential vault and network policy rather than the daemon's network.
//
// Accepts any ToolNodePool implementation (Incus via AsNodePool, or the
// backend-agnostic backendPool from NewBackendPool). The Backend is extracted
// via pool.GetBackend() for creating BackendMCPTransport instances.
//
// This is called after the sandbox pool is created in chat_factory.go.
func (l *LazyMCPToolset) SetSandboxPool(pool sandbox.ToolNodePool) {
	l.sandboxPool = pool
	if pool != nil {
		l.sandboxBackend = pool.GetBackend()
	}
}

// SetSandboxClient enables sandbox-aware MCP server startup using a specific
// LazyNodeClient (rather than a pool). Used for fleet sessions where each
// fleet session has its own dedicated container.
func (l *LazyMCPToolset) SetSandboxClient(client *sandbox.LazyNodeClient, backend sandbox.Backend) {
	l.sandboxClient = client
	l.sandboxBackend = backend
}

// SetCredentialResolver registers a fallback credential store for expanding
// MCP env placeholders. When a tool call has a request-scoped store on the
// context, that store is preferred.
func (l *LazyMCPToolset) SetCredentialResolver(resolver credentials.CredentialResolver) {
	if l == nil {
		return
	}
	l.credentialResolver = resolver
}

// CloneForFleet creates a new LazyMCPToolset with the same server config and
// cached metadata but with a fleet-specific sandbox client. The clone is
// independent — starting/cleaning it up doesn't affect the original.
// Used by wireFleetSandbox to give each fleet session its own MCP server
// processes running in the fleet's dedicated container/pod.
func (l *LazyMCPToolset) CloneForFleet(client *sandbox.LazyNodeClient, backend sandbox.Backend) *LazyMCPToolset {
	clone := NewLazyMCPToolset(l.serverName, l.entries, l.serverCfg, l.debugMode)
	clone.SetSandboxClient(client, backend)
	clone.SetCredentialResolver(l.credentialResolver)
	return clone
}

// resolvedServerConfig expands credential placeholders in the stored server
// config for process start. Prefers the context credential store when set.
func (l *LazyMCPToolset) resolvedServerConfig(ctx context.Context) (config.MCPServerConfig, error) {
	resolver := l.credentialResolver
	if cs := store.CredentialStoreFromContext(ctx); cs != nil {
		resolver = credentials.NewStoreAdapter(cs)
	}
	return mcp.ResolveMCPServerConfig(l.serverCfg, resolver)
}

// isSandboxMode reports stdio process routing. Remote node calls are handled
// separately by lazyProxyTool.Run and never enter the stdio initializer.
func (l *LazyMCPToolset) isSandboxMode() bool {
	return l.hasSandbox() && !l.isRemoteTransport()
}

func (l *LazyMCPToolset) hasSandbox() bool {
	return l.sandboxBackend != nil || l.sandboxPool != nil || l.sandboxClient != nil
}

func (l *LazyMCPToolset) isRemoteTransport() bool {
	return l.isSSETransport() || l.serverCfg.Transport == "streamableHTTP"
}

// isSSETransport reports the legacy remote SSE transport.
func (l *LazyMCPToolset) isSSETransport() bool {
	return l.serverCfg.Transport == "sse"
}

// Name returns the MCP server name.
func (l *LazyMCPToolset) Name() string {
	return l.serverName
}

// Tools returns proxy tools built from cached metadata. No MCP connection is needed.
func (l *LazyMCPToolset) Tools(_ adkagent.ReadonlyContext) ([]tool.Tool, error) {
	proxies := make([]tool.Tool, len(l.entries))
	for i := range l.entries {
		proxies[i] = &lazyProxyTool{
			toolset: l,
			entry:   &l.entries[i],
		}
	}
	return proxies, nil
}

// ensureServerStarted connects to the MCP server if not already connected.
// Thread-safe: only one goroutine will start the server.
//
// In sandbox mode, each logical session gets its own MCP server process inside
// its canonical owner's container. In host mode, a single global process is used.
//
// Host-mode retry: if the previous startup failed with a transient error
// (context canceled, timeout, connection refused, etc.), the next call will
// automatically retry up to maxMCPRetries times with mcpRetryBackoff between
// attempts. Permanent errors are returned immediately without retry.
func (l *LazyMCPToolset) ensureServerStarted(ctx context.Context, sessionID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.hasSandbox() && l.isRemoteTransport() {
		return fmt.Errorf("remote sandbox MCP requires node dispatch")
	}

	// Sandbox mode: per-session state
	if l.isSandboxMode() {
		if sessionID == "" {
			return fmt.Errorf("sandbox MCP requires a session identity")
		}
		return l.ensureServerStartedSandbox(ctx, sessionID)
	}

	// Host mode: global state
	if l.started {
		// Success path: server is running fine.
		if l.startErr == nil {
			return nil
		}

		// Failure path: check if the error is transient and we can retry.
		if isTransientMCPError(l.startErr) && l.retryCount < maxMCPRetries && time.Since(l.lastAttempt) >= mcpRetryBackoff {
			slog.Info("retrying MCP server connection (transient failure)",
				"component", "lazy-mcp", "server", l.serverName,
				"attempt", l.retryCount+1, "maxRetries", maxMCPRetries,
				"previousError", l.startErr)

			// Clean up old manager if it exists.
			if l.manager != nil {
				l.manager.Cleanup()
				l.manager = nil
			}

			// Reset state so startOnHost can run again.
			l.started = false
			l.startErr = nil
			l.liveTools = nil
			l.retryCount++
			// Fall through to startOnHost below.
		} else {
			// Permanent error, max retries exhausted, or backoff not elapsed.
			return l.startErr
		}
	}

	if l.debugMode {
		slog.Debug("starting MCP server on demand (host)", "component", "lazy-mcp", "server", l.serverName)
	}

	return l.startOnHost(ctx)
}

// ensureServerStartedSandbox handles sandbox-mode startup with per-session state.
// Caller must hold l.mu.
func (l *LazyMCPToolset) ensureServerStartedSandbox(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Readiness must be rechecked even when this logical session has tools.
	if l.sandboxClient != nil {
		return l.startInContainerDirect(ctx, sessionID)
	}
	return l.startInContainer(ctx, sessionID)
}

// readySandboxTools reuses only a healthy connection to the same physical
// owner. Startup/handshake is safe to retry; user methods never enter this loop.
// Caller holds l.mu. The context bounds readiness, backoff and handshake.
func (l *LazyMCPToolset) readySandboxTools(ctx context.Context, logicalID, ownerID string) error {
	if state := l.perSession[logicalID]; state != nil {
		if state.permanent {
			return state.startErr
		}
		if state.started && state.startErr == nil && state.ownerID == ownerID && state.transport != nil && !state.transport.IsClosed() {
			return nil
		}
		if state.transport != nil {
			_ = state.transport.Close()
		}
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := l.startInSandbox(ctx, logicalID, ownerID)
		if err == nil {
			return nil
		}
		state := l.perSession[logicalID]
		if state.permanent || !isTransientMCPError(err) || attempt >= maxMCPRetries {
			return err
		}
		timer := time.NewTimer(mcpRetryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// startOnHost starts the MCP server on the host using the stored server config.
// Uses the config already resolved from the DB at factory time (not the filesystem).
// Caller must hold l.mu.
func (l *LazyMCPToolset) startOnHost(ctx context.Context) error {
	l.lastAttempt = time.Now()

	serverCfg, err := l.resolvedServerConfig(ctx)
	if err != nil {
		l.startErr = fmt.Errorf("failed to resolve credentials for MCP server '%s': %w", l.serverName, err)
		slog.Warn("lazy MCP server credential resolution failed", mcp.FailureLogAttrs(l.serverName, l.serverCfg, l.startErr, nil)...)
		l.started = true
		return l.startErr
	}

	cfg := &config.MCPConfig{
		MCPServers: map[string]config.MCPServerConfig{
			l.serverName: serverCfg,
		},
	}
	mgr := mcp.NewManagerFromConfig(cfg)
	// Env is already resolved above; keep manager resolver nil so we do not
	// depend on a second pass. Plain values pass through.

	namedToolset, err := mgr.InitializeSingleToolset(ctx, l.serverName)
	if err != nil {
		mgr.Cleanup()
		l.startErr = fmt.Errorf("failed to start MCP server '%s': %w", l.serverName, err)
		slog.Warn("lazy MCP server startup failed", mcp.FailureLogAttrs(l.serverName, serverCfg, l.startErr, nil)...)
		l.started = true
		return l.startErr
	}

	// Resolve all tools from the live server
	minCtx := &minimalReadonlyContext{Context: ctx}
	liveToolList, err := namedToolset.Toolset.Tools(minCtx)
	if err != nil {
		mgr.Cleanup()
		l.startErr = fmt.Errorf("failed to get tools from MCP server '%s': %w", l.serverName, err)
		slog.Warn("lazy MCP server tool listing failed", mcp.FailureLogAttrs(l.serverName, serverCfg, l.startErr, namedToolset.Stderr)...)
		l.started = true
		return l.startErr
	}

	l.liveTools = make(map[string]tool.Tool, len(liveToolList))
	for _, t := range liveToolList {
		l.liveTools[t.Name()] = t
	}

	l.manager = mgr
	l.started = true
	l.retryCount = 0 // Reset retry count on success for future transient failures.

	if l.debugMode {
		slog.Debug("MCP server started (host)", "component", "lazy-mcp", "server", l.serverName, "tools", len(l.liveTools))
	}

	return nil
}

// startInContainer starts the MCP server inside the session's sandbox
// using the abstract Backend. Uses the pool to ensure the container/pod is ready
// first. Caller must hold l.mu.
func (l *LazyMCPToolset) startInContainer(ctx context.Context, sessionID string) error {
	// Get or create the ToolNodeClient for this session, then wait for the
	// container to be ready. Only waits for container creation, NOT node startup.
	if l.sandboxPool == nil {
		return fmt.Errorf("sandbox MCP pool is not configured")
	}
	client := sandbox.GetPoolClientFromContext(ctx, l.sandboxPool, sessionID)
	if client == nil {
		return fmt.Errorf("sandbox pool returned nil client for session %s", sessionID)
	}

	if err := sandbox.EnsureToolClientReady(ctx, client, sessionID); err != nil {
		return fmt.Errorf("failed to get container for MCP server '%s': %w", l.serverName, err)
	}

	return l.readySandboxTools(ctx, sessionID, sandbox.CanonicalSessionID(client, sessionID))
}

// startInContainerDirect starts the MCP server inside a specific container
// using a direct LazyNodeClient (fleet mode). Caller must hold l.mu.
func (l *LazyMCPToolset) startInContainerDirect(ctx context.Context, sessionID string) error {
	client := l.sandboxClient
	if err := sandbox.EnsureToolClientReady(ctx, client, sessionID); err != nil {
		return fmt.Errorf("failed to get container for MCP server '%s': %w", l.serverName, err)
	}

	return l.readySandboxTools(ctx, sessionID, sandbox.CanonicalSessionID(client, sessionID))
}

// startInSandbox is the shared implementation for starting an MCP server
// inside a sandbox using the abstract Backend interface. Both pool-based
// and direct-client paths converge here. Works for both Incus and K8s.
// Caller must hold l.mu.
func (l *LazyMCPToolset) startInSandbox(ctx context.Context, sessionID, ownerID string) error {
	if l.debugMode {
		slog.Debug("connecting MCP server in sandbox", "component", "lazy-mcp", "server", l.serverName, "session", sessionID)
	}

	serverCfg, err := l.resolvedServerConfig(ctx)
	if err != nil {
		err = l.setSessionError(sessionID, fmt.Errorf("failed to resolve credentials for MCP server '%s': %w", l.serverName, err))
		l.perSession[sessionID].permanent = true
		return err
	}
	if strings.TrimSpace(serverCfg.Command) == "" || (serverCfg.Enabled != nil && !*serverCfg.Enabled) || (serverCfg.Transport != "" && serverCfg.Transport != "stdio") {
		err = l.setSessionError(sessionID, fmt.Errorf("invalid or disabled sandbox MCP server '%s' configuration", l.serverName))
		l.perSession[sessionID].permanent = true
		return err
	}

	// Create backend-agnostic transport (uses Backend.ExecStreaming).
	// Diagnostics use the resolved config so real credential values emitted by
	// child processes are scrubbed before logging.
	transport, stderrBuf := sandbox.NewBackendMCPTransport(l.sandboxBackend, ownerID, serverCfg)

	// Create ADK mcptoolset using our transport
	toolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		transport.Close()
		stderr := mcp.DiagnosticStderrForServer(stderrBuf, serverCfg)
		stdout := mcp.DiagnosticOutputForServer(transport.StdoutDiagnostics, serverCfg)
		startErr := fmt.Errorf("failed to create toolset for MCP server '%s' in sandbox: %w (stderr: %s, stdout: %s)", l.serverName, err, stderr, stdout)
		attrs := mcp.FailureLogAttrs(l.serverName, serverCfg, startErr, stderrBuf)
		attrs = append(attrs, "stdout", stdout)
		slog.Warn("sandbox MCP server startup failed", attrs...)
		return l.setSessionError(sessionID, startErr)
	}

	// Resolve all tools from the live server
	minCtx := &minimalReadonlyContext{Context: ctx}
	liveToolList, err := toolset.Tools(minCtx)
	if err != nil {
		transport.Close()
		stderr := mcp.DiagnosticStderrForServer(stderrBuf, serverCfg)
		stdout := mcp.DiagnosticOutputForServer(transport.StdoutDiagnostics, serverCfg)
		toolsErr := fmt.Errorf("failed to get tools from MCP server '%s' in sandbox: %w (stderr: %s, stdout: %s)", l.serverName, err, stderr, stdout)
		attrs := mcp.FailureLogAttrs(l.serverName, serverCfg, toolsErr, stderrBuf)
		attrs = append(attrs, "stdout", stdout)
		slog.Warn("sandbox MCP server tool listing failed", attrs...)
		return l.setSessionError(sessionID, toolsErr)
	}

	liveTools := make(map[string]tool.Tool, len(liveToolList))
	for _, t := range liveToolList {
		liveTools[t.Name()] = t
	}

	// Store per-session state
	if l.perSession == nil {
		l.perSession = make(map[string]*sessionMCPState)
	}
	l.perSession[sessionID] = &sessionMCPState{
		transport: transport,
		ownerID:   ownerID,
		liveTools: liveTools,
		started:   true,
	}

	if l.debugMode {
		slog.Debug("MCP server started (sandbox)", "component", "lazy-mcp", "server", l.serverName, "session", sessionID, "tools", len(liveTools))
	}

	return nil
}

// setSessionError records a startup error for a session. Caller must hold l.mu.
func (l *LazyMCPToolset) setSessionError(sessionID string, err error) error {
	if l.perSession == nil {
		l.perSession = make(map[string]*sessionMCPState)
	}
	l.perSession[sessionID] = &sessionMCPState{
		startErr: err,
		started:  true,
	}
	return err
}

// getLiveTool returns the real MCP tool, starting the server if needed.
func (l *LazyMCPToolset) getLiveTool(ctx context.Context, name string, sessionID string) (tool.Tool, error) {
	if err := l.ensureServerStarted(ctx, sessionID); err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Sandbox mode: look up per-session tools
	if l.isSandboxMode() && sessionID != "" {
		if state, ok := l.perSession[sessionID]; ok && state.liveTools != nil {
			t, ok := state.liveTools[name]
			if !ok {
				return nil, fmt.Errorf("tool '%s' not found on MCP server '%s' (session=%s, available: %d tools)",
					name, l.serverName, sessionID, len(state.liveTools))
			}
			return t, nil
		}
		return nil, fmt.Errorf("no MCP state for session %s on server '%s'", sessionID, l.serverName)
	}

	// Host mode: global tools
	t, ok := l.liveTools[name]
	if !ok {
		return nil, fmt.Errorf("tool '%s' not found on MCP server '%s' (available: %d tools)",
			name, l.serverName, len(l.liveTools))
	}
	return t, nil
}

// Cleanup closes all MCP server connections.
func (l *LazyMCPToolset) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Host mode cleanup
	if l.manager != nil {
		l.manager.Cleanup()
		l.manager = nil
	}

	// Sandbox mode cleanup: close all per-session transports
	for sid, state := range l.perSession {
		if state.transport != nil {
			if err := state.transport.Close(); err != nil {
				slog.Warn("failed to close container transport", "component", "lazy-mcp", "server", l.serverName, "session", sid, "error", err)
			}
		}
	}
	l.perSession = nil
}

// CleanupSession closes the MCP server connection for a specific session.
// Used when a session is deleted or its container is being destroyed.
func (l *LazyMCPToolset) CleanupSession(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if state, ok := l.perSession[sessionID]; ok {
		if state.transport != nil {
			if err := state.transport.Close(); err != nil {
				slog.Warn("failed to close container transport", "component", "lazy-mcp", "server", l.serverName, "session", sessionID, "error", err)
			}
		}
		delete(l.perSession, sessionID)
	}
}

// ToolCount returns the number of cached tools for this server.
func (l *LazyMCPToolset) ToolCount() int {
	return len(l.entries)
}

// lazyProxyTool is a lightweight proxy for a single MCP tool.
// It uses cached metadata for declarations and lazily delegates Run() to the real tool.
type lazyProxyTool struct {
	toolset *LazyMCPToolset
	entry   *cache.ToolEntry
}

func (p *lazyProxyTool) Name() string {
	return p.entry.Name
}

func (p *lazyProxyTool) Description() string {
	return p.entry.Description
}

func (p *lazyProxyTool) IsLongRunning() bool {
	return false
}

// Declaration returns a function declaration built from cached metadata.
func (p *lazyProxyTool) Declaration() *genai.FunctionDeclaration {
	decl := &genai.FunctionDeclaration{
		Name:        p.entry.Name,
		Description: p.entry.Description,
	}

	// Restore schema from cache
	if len(p.entry.InputSchema) > 0 {
		var schema any
		if err := json.Unmarshal(p.entry.InputSchema, &schema); err == nil {
			decl.ParametersJsonSchema = schema
		}
	}

	// If no cached schema, set a minimal valid object schema
	if decl.ParametersJsonSchema == nil {
		decl.ParametersJsonSchema = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	return decl
}

// ProcessRequest packs the cached tool declaration into the LLM request.
// Same approach as sanitizedTool: replicate PackTool logic to use our Declaration().
func (p *lazyProxyTool) ProcessRequest(_ tool.Context, req *model.LLMRequest) error {
	if req.Tools == nil {
		req.Tools = make(map[string]any)
	}

	name := p.Name()
	if _, ok := req.Tools[name]; ok {
		return fmt.Errorf("duplicate tool: %q", name)
	}
	req.Tools[name] = p

	decl := p.Declaration()
	if decl == nil {
		return nil
	}

	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}

	var funcTool *genai.Tool
	for _, gt := range req.Config.Tools {
		if gt != nil && gt.FunctionDeclarations != nil {
			funcTool = gt
			break
		}
	}
	if funcTool == nil {
		req.Config.Tools = append(req.Config.Tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{decl},
		})
	} else {
		funcTool.FunctionDeclarations = append(funcTool.FunctionDeclarations, decl)
	}

	return nil
}

// MCPToolBinding exposes policy metadata only for the genuine lazy MCP proxy.
// Its private target prevents arbitrary tools from claiming terminal ownership.
// Schema sanitization is transparent; no executable or transport is replaced.
type MCPToolBinding struct{ proxy *lazyProxyTool }

func LazyMCPToolBinding(t tool.Tool) (MCPToolBinding, bool) {
	switch t := t.(type) {
	case *lazyProxyTool:
		return MCPToolBinding{proxy: t}, true
	case *sanitizedTool:
		return LazyMCPToolBinding(t.Tool)
	default:
		return MCPToolBinding{}, false
	}
}

func (b MCPToolBinding) Identity() string {
	return BotToolIdentity("mcp:"+b.proxy.toolset.serverName, b.proxy.Name())
}

func mcpPolicyFingerprint(cfg config.MCPServerConfig) string {
	if cfg.Transport == "" {
		cfg.Transport = "stdio"
	}
	enabled := cfg.IsEnabled()
	cfg.Enabled = &enabled
	raw, _ := json.Marshal(cfg)
	sum := sha256.Sum256(raw)
	return "mcp-config-v1:" + hex.EncodeToString(sum[:])
}

// DescribeAction validates the current scoped symbolic configuration against the
// executable snapshot. Tool annotations and names never establish read authority.
func (b MCPToolBinding) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if b.proxy == nil || name != b.Identity() {
		return execution.ActionDescriptor{}, botPolicyDenied("MCP executable identity changed")
	}
	stores := store.MCPServerStoresFromContext(ctx)
	if stores == nil {
		return execution.ActionDescriptor{}, botPolicyDenied("missing scoped MCP stores")
	}
	var current *store.MCPServer
	for _, s := range []store.MCPServerStore{stores.Platform, stores.Org, stores.Team} {
		if s == nil {
			continue
		}
		row, err := s.Get(ctx, b.proxy.toolset.serverName)
		if err != nil {
			return execution.ActionDescriptor{}, err
		}
		if row != nil {
			current = row
		}
	}
	if current == nil || current.Name != b.proxy.toolset.serverName || !current.IsEnabled() {
		return execution.ActionDescriptor{}, botPolicyDenied("MCP server unavailable")
	}
	enabled := current.IsEnabled()
	transport := current.Transport
	if transport == "" {
		transport = "stdio"
	}
	cfg := config.MCPServerConfig{Command: current.Command, Args: current.Args, Env: current.Env, Transport: transport, URL: current.URL, Enabled: &enabled}
	fingerprint := mcpPolicyFingerprint(b.proxy.toolset.serverCfg)
	if mcpPolicyFingerprint(cfg) != fingerprint {
		return execution.ActionDescriptor{}, botPolicyDenied("MCP server configuration changed")
	}
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown, TargetFingerprint: fingerprint}, nil
}

type mcpTerminalCheckKey struct{}
type mcpTerminalCheck struct {
	proxy    *lazyProxyTool
	check    func(context.Context) error
	evidence *botLazyHandoff
}

// Prepare authorizes once at the owner-bound accounting boundary. Progressive
// dispatch evidence is consumed and renewed for this same terminal only; direct
// calls perform full authorization. The private one-use token still binds exact
// identity, effective arguments, policy and symbolic target at the lazy boundary.
func (b MCPToolBinding) Prepare(ctx tool.Context, args any, check func(context.Context) error) (tool.Context, error) {
	if b.proxy == nil || check == nil {
		return nil, botPolicyDenied("missing MCP binding")
	}
	var prepared tool.Context
	var err error
	if handoff, ok := ctx.Value(botLazyHandoffKey{}).(*botLazyHandoff); ok {
		if err = AuthorizeBotBoundTool(ctx, ctx, b.Identity(), args, mcpPolicyFingerprint(b.proxy.toolset.serverCfg)); err != nil {
			return nil, err
		}
		prepared, err = botAuthorizedInvocationContext(ctx, b.Identity(), args, handoff.action)
	} else {
		prepared, err = authorizeBotInvocation(ctx, b.Identity(), args)
	}
	if err != nil {
		return nil, err
	}
	evidence, ok := prepared.Value(botLazyHandoffKey{}).(*botLazyHandoff)
	if !ok {
		return nil, botPolicyDenied("missing MCP authorization evidence")
	}
	return &botPolicyToolContext{Context: prepared, context: context.WithValue(prepared, mcpTerminalCheckKey{}, mcpTerminalCheck{b.proxy, check, evidence})}, nil
}

func (p *lazyProxyTool) checkTerminal(ctx tool.Context, args any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if binding, ok := ctx.Value(mcpTerminalCheckKey{}).(mcpTerminalCheck); ok {
		digest, err := botInvocationDigest(BotToolIdentity("mcp:"+p.toolset.serverName, p.Name()), args)
		if binding.proxy != p || err != nil || digest != binding.evidence.digest {
			return botPolicyDenied("MCP terminal changed")
		}
		if err := binding.check(ctx); err != nil {
			return err
		}
		return binding.evidence.validateTarget(ctx)
	}
	return nil
}

// Run lazily starts the MCP server (if not already started) and delegates to the real tool.
// The session ID is extracted from the tool.Context and passed through to
// ensureServerStarted, which uses it to get the correct container when sandbox is enabled.
func (p *lazyProxyTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("MCP invocation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Defense-in-depth: verify MCP server access before connecting/executing.
	// Even if a tool was injected into the LLM request (e.g., via a stale
	// ToolIndex match), block execution if the user's team doesn't have
	// access to this MCP server.
	if !isMCPServerAccessible(ctx, p.toolset.serverName) {
		return map[string]any{
			"error": fmt.Sprintf("access denied: MCP server '%s' is not available for your team", p.toolset.serverName),
		}, nil
	}

	if err := p.checkTerminal(ctx, args); err != nil {
		return nil, err
	}
	if err := authorizeBotLazyInvocation(ctx, BotToolIdentity("mcp:"+p.toolset.serverName, p.Name()), args); err != nil {
		return nil, err
	}

	sessionID := ctx.SessionID()
	if p.toolset.hasSandbox() && p.toolset.isRemoteTransport() {
		return p.runRemoteInSandbox(ctx, args)
	}
	liveTool, err := p.toolset.getLiveTool(ctx, p.entry.Name, sessionID)
	if err != nil {
		return nil, err
	}
	// The live MCP tool implements Run via the FunctionTool interface
	runner, ok := liveTool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		return nil, fmt.Errorf("live tool '%s' does not implement Run", p.entry.Name)
	}
	// Capture this invocation's connection so an old failing call cannot close
	// a concurrently replaced logical session. Never replay after Run begins.
	var transport *sandbox.BackendMCPTransport
	p.toolset.mu.Lock()
	if state := p.toolset.perSession[sessionID]; state != nil && state.liveTools[p.entry.Name] == liveTool {
		transport = state.transport
	}
	p.toolset.mu.Unlock()
	if err := p.checkTerminal(ctx, args); err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, args)
	if transport != nil && (transport.IsClosed() || isTransientMCPError(err)) {
		_ = transport.Close()
	}
	return result, err
}

// runRemoteInSandbox sends symbolic configuration to the existing node handler.
// Each call owns its connection there; neither an uncertain call nor a denied
// node request may be replayed on the daemon host.
func (p *lazyProxyTool) runRemoteInSandbox(ctx tool.Context, args any) (map[string]any, error) {
	id := ctx.SessionID()
	if id == "" {
		return nil, fmt.Errorf("sandbox MCP requires a session identity")
	}
	l := p.toolset
	if !l.serverCfg.IsEnabled() || strings.TrimSpace(l.serverCfg.URL) == "" {
		return nil, fmt.Errorf("invalid or disabled remote MCP server %q", l.serverName)
	}
	values, ok := args.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("MCP tool %q requires object arguments", p.Name())
	}
	var client sandbox.ToolNodeClient
	if l.sandboxClient != nil {
		client = l.sandboxClient
	} else if l.sandboxPool != nil {
		client = sandbox.GetPoolClientFromContext(ctx, l.sandboxPool, id)
	}
	// Seed only the existing scoped allow rules, never the MCP endpoint itself.
	if setter, ok := client.(interface {
		SetNetworkAllowEndpoints([]sandbox.NetworkAllowEndpoint)
	}); ok {
		if stores := store.NetworkPolicyStoresFromContext(ctx); stores != nil {
			var endpoints []sandbox.NetworkAllowEndpoint
			for _, s := range []store.NetworkPolicyStore{stores.Platform, stores.Org, stores.Team} {
				if s == nil {
					continue
				}
				rules, err := s.List(ctx)
				if err != nil {
					return nil, err
				}
				for _, rule := range rules {
					if rule.Action == store.NetworkPolicyAllow {
						endpoints = append(endpoints, sandbox.NetworkAllowEndpoint{Host: rule.Host, Port: rule.Port})
					}
				}
			}
			setter.SetNetworkAllowEndpoints(endpoints)
		}
	}
	if err := sandbox.EnsureToolClientReady(ctx, client, id); err != nil {
		return nil, err
	}
	owner := sandbox.CanonicalSessionID(client, id)
	if sandbox.NetworkPolicyPreSeeder != nil {
		sandbox.NetworkPolicyPreSeeder(ctx, owner)
	}
	if err := sandbox.SyncSessionCredentialVault(ctx, l.sandboxBackend, nil, owner); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !isMCPServerAccessible(ctx, l.serverName) {
		return nil, fmt.Errorf("access denied: MCP server %q", l.serverName)
	}
	raw, err := json.Marshal(mcp.NodeRequest{Action: mcp.NodeCall, Config: l.serverCfg, Tool: p.Name(), Arguments: values})
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if err := p.checkTerminal(ctx, args); err != nil {
		return nil, err
	}
	if caller, ok := client.(sandbox.ContextCallingToolClient); ok {
		raw, err = caller.CallContext(ctx, id, mcp.NodeOperation, request)
	} else {
		raw, err = client.Call(id, mcp.NodeOperation, request)
	}
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("invalid MCP node response")
	}
	return result, nil
}
