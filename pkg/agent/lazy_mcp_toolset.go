package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
)

// sessionMCPState holds MCP server state for a single session.
// In sandbox mode, each session gets its own container/pod and thus its own
// MCP server process. This struct tracks the per-session transport and tools.
type sessionMCPState struct {
	transport *sandbox.BackendMCPTransport
	liveTools map[string]tool.Tool
	startErr  error
	started   bool
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
// process (running in its own container/pod). The per-session state is stored in
// the perSession map. In host mode, a single global state is used (the host
// runs one MCP server process shared across all sessions).
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
	mu        sync.Mutex
	manager   *mcp.Manager         // host-mode MCP manager
	liveTools map[string]tool.Tool // host-mode: name -> real MCP tool
	startErr  error                // host-mode: cached error if server failed to start
	started   bool                 // host-mode: whether the server was started

	// --- Sandbox mode state (per-session MCP servers) ---
	// perSession is keyed by session ID and holds the MCP state for each session.
	// Protected by mu. Only populated when sandboxBackend is set.
	perSession map[string]*sessionMCPState
}

// NewLazyMCPToolset creates a toolset that uses cached metadata for declarations
// and lazily connects to the MCP server only when a tool is actually invoked.
func NewLazyMCPToolset(serverName string, entries []cache.ToolEntry,
	serverCfg config.MCPServerConfig, debugMode bool) *LazyMCPToolset {
	return &LazyMCPToolset{
		serverName: serverName,
		serverCfg:  serverCfg,
		entries:    entries,
		debugMode:  debugMode,
	}
}

// SetSandboxPool enables sandbox-aware MCP server startup. When set, stdio
// transport MCP servers will be started inside the session's container/pod
// instead of on the host. SSE transport servers are unaffected (they connect
// to remote URLs and don't need containerization).
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

// isSandboxMode returns true if the MCP server should run inside a container/pod.
// True when a sandbox backend is set AND the transport is stdio.
func (l *LazyMCPToolset) isSandboxMode() bool {
	if l.isSSETransport() {
		return false
	}
	return l.sandboxBackend != nil
}

// isSSETransport returns true if the MCP server uses SSE transport (remote URL).
// SSE servers always stay on the host — only stdio servers get containerized.
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
// In sandbox mode, each session gets its own MCP server process inside its
// own container. In host mode, a single global process is used.
func (l *LazyMCPToolset) ensureServerStarted(ctx context.Context, sessionID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Sandbox mode: per-session state
	if l.isSandboxMode() && sessionID != "" {
		return l.ensureServerStartedSandbox(ctx, sessionID)
	}

	// Host mode: global state
	if l.started {
		return l.startErr
	}

	if l.debugMode {
		slog.Debug("starting MCP server on demand (host)", "component", "lazy-mcp", "server", l.serverName)
	}

	return l.startOnHost(ctx)
}

// ensureServerStartedSandbox handles sandbox-mode startup with per-session state.
// Caller must hold l.mu.
func (l *LazyMCPToolset) ensureServerStartedSandbox(ctx context.Context, sessionID string) error {
	// Check if this session already has state
	if l.perSession != nil {
		if state, ok := l.perSession[sessionID]; ok && state.started {
			return state.startErr
		}
	}

	if l.debugMode {
		slog.Debug("starting MCP server on demand (sandbox)", "component", "lazy-mcp", "server", l.serverName, "session", sessionID)
	}

	// Choose: fleet direct client vs pool
	if l.sandboxClient != nil {
		return l.startInContainerDirect(ctx, sessionID)
	}
	return l.startInContainer(ctx, sessionID)
}

// startOnHost starts the MCP server on the host using the stored server config.
// Uses the config already resolved from the DB at factory time (not the filesystem).
// Caller must hold l.mu.
func (l *LazyMCPToolset) startOnHost(ctx context.Context) error {
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
		slog.Warn("lazy MCP server startup failed", mcp.FailureLogAttrs(l.serverName, l.serverCfg, l.startErr, nil)...)
		l.started = true
		return l.startErr
	}

	// Resolve all tools from the live server
	minCtx := &minimalReadonlyContext{Context: ctx}
	liveToolList, err := namedToolset.Toolset.Tools(minCtx)
	if err != nil {
		mgr.Cleanup()
		l.startErr = fmt.Errorf("failed to get tools from MCP server '%s': %w", l.serverName, err)
		slog.Warn("lazy MCP server tool listing failed", mcp.FailureLogAttrs(l.serverName, l.serverCfg, l.startErr, namedToolset.Stderr)...)
		l.started = true
		return l.startErr
	}

	l.liveTools = make(map[string]tool.Tool, len(liveToolList))
	for _, t := range liveToolList {
		l.liveTools[t.Name()] = t
	}

	l.manager = mgr
	l.started = true

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
	client := l.sandboxPool.GetOrCreate(sessionID)
	if client == nil {
		return l.setSessionError(sessionID, fmt.Errorf("sandbox pool returned nil client for session %s", sessionID))
	}

	if err := client.EnsureReady(sessionID); err != nil {
		return l.setSessionError(sessionID, fmt.Errorf("failed to get container for MCP server '%s': %w", l.serverName, err))
	}

	return l.startInSandbox(ctx, sessionID)
}

// startInContainerDirect starts the MCP server inside a specific container
// using a direct LazyNodeClient (fleet mode). Caller must hold l.mu.
func (l *LazyMCPToolset) startInContainerDirect(ctx context.Context, sessionID string) error {
	_, err := l.sandboxClient.EnsureContainerReady(sessionID)
	if err != nil {
		return l.setSessionError(sessionID, fmt.Errorf("failed to get container for MCP server '%s': %w", l.serverName, err))
	}

	return l.startInSandbox(ctx, sessionID)
}

// startInSandbox is the shared implementation for starting an MCP server
// inside a sandbox using the abstract Backend interface. Both pool-based
// and direct-client paths converge here. Works for both Incus and K8s.
// Caller must hold l.mu.
func (l *LazyMCPToolset) startInSandbox(ctx context.Context, sessionID string) error {
	if l.debugMode {
		slog.Debug("connecting MCP server in sandbox", "component", "lazy-mcp", "server", l.serverName, "session", sessionID)
	}

	serverCfg, err := l.resolvedServerConfig(ctx)
	if err != nil {
		return l.setSessionError(sessionID, fmt.Errorf("failed to resolve credentials for MCP server '%s': %w", l.serverName, err))
	}

	// Create backend-agnostic transport (uses Backend.ExecInteractive)
	// Use resolved env; diagnostics still redact using the original stored cfg
	// so placeholder strings are not treated as secrets to scrub.
	transport, stderrBuf := sandbox.NewBackendMCPTransport(l.sandboxBackend, sessionID, serverCfg)

	// Create ADK mcptoolset using our transport
	toolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		transport.Close()
		stderr := mcp.DiagnosticStderrForServer(stderrBuf, serverCfg)
		stdout := mcp.DiagnosticOutputForServer(transport.StdoutDiagnostics, serverCfg)
		startErr := fmt.Errorf("failed to create toolset for MCP server '%s' in sandbox: %w (stderr: %s, stdout: %s)", l.serverName, err, stderr, stdout)
		attrs := mcp.FailureLogAttrs(l.serverName, l.serverCfg, startErr, stderrBuf)
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
		attrs := mcp.FailureLogAttrs(l.serverName, l.serverCfg, toolsErr, stderrBuf)
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

// Run lazily starts the MCP server (if not already started) and delegates to the real tool.
// The session ID is extracted from the tool.Context and passed through to
// ensureServerStarted, which uses it to get the correct container when sandbox is enabled.
func (p *lazyProxyTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	// Defense-in-depth: verify MCP server access before connecting/executing.
	// Even if a tool was injected into the LLM request (e.g., via a stale
	// ToolIndex match), block execution if the user's team doesn't have
	// access to this MCP server.
	if !isMCPServerAccessible(ctx, p.toolset.serverName) {
		return map[string]any{
			"error": fmt.Sprintf("access denied: MCP server '%s' is not available for your team", p.toolset.serverName),
		}, nil
	}

	sessionID := ctx.SessionID()
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
	return runner.Run(ctx, args)
}
