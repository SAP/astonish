package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// ToolWithDeclaration is the interface for tools that can declare their schema.
// Canonical definition lives in pkg/common; aliased here for convenience.
type ToolWithDeclaration = common.ToolWithDeclaration

// NodeTool wraps an existing tool.Tool and proxies its Run() method to a
// ToolNodeClient (astonish node inside a container, backed by Incus,
// K8s+Sysbox, or mock). It preserves the original tool's Name(),
// Description(), Declaration(), and IsLongRunning() so the ADK sees it
// as a normal tool.
//
// On the first Run() call, the client creates the session container
// and starts the astonish node process. The session ID is obtained from
// tool.Context.SessionID().
//
// This follows the same structural interface pattern as ADK's mcpTool and
// Astonish's ProtectedTool/sanitizedTool — it satisfies FunctionTool and
// RequestProcessor by implementing the methods directly.
//
// Two shapes are supported for vending clients:
//
//   - pool (chat/flow sessions): ToolNodePool dispenses one client per
//     sessionID; most production callers.
//   - lazyClient (fleet sessions): a pre-built *LazyNodeClient pinned
//     to a single fleet session; fleet never fans out across sessions.
//
// The interface type on `pool` is the Phase E bridge: *NodeClientPool
// (Incus) and backendPool (K8s/mock via sandbox.Backend) both satisfy
// ToolNodePool, so SetupFlowSandbox can dispatch on backend kind
// without this file knowing which fulfilled it.
type NodeTool struct {
	tool.Tool // Embed the original tool for Name(), Description(), IsLongRunning()

	pool       ToolNodePool    // for chat/flow sessions (per-session containers)
	lazyClient *LazyNodeClient // for fleet sessions (dedicated per-session client)
}

// NewNodeTool creates a NodeTool that proxies the given tool through a
// concrete Incus-bound pool. Kept as-is for chat_factory.go and other
// callers that construct *NodeClientPool directly; internally it wraps
// the pool in the ToolNodePool adapter so the rest of NodeTool only
// ever sees the interface.
//
// New callers that construct a ToolNodePool directly (e.g. the
// backend-agnostic pool from NewBackendPool) should use
// NewNodeToolWithPool instead.
func NewNodeTool(original tool.Tool, pool *NodeClientPool) *NodeTool {
	return &NodeTool{
		Tool: original,
		pool: AsNodePool(pool),
	}
}

// NewNodeToolWithPool creates a NodeTool that proxies through any
// ToolNodePool implementation. Introduced in Phase E (slice 4) so the
// flow path can wire either an Incus NodeClientPool (via AsNodePool)
// or a backend-agnostic backendPool (via NewBackendPool) uniformly.
func NewNodeToolWithPool(original tool.Tool, pool ToolNodePool) *NodeTool {
	return &NodeTool{
		Tool: original,
		pool: pool,
	}
}

// NewNodeToolWithClient creates a NodeTool that proxies through a single
// LazyNodeClient. Used by fleet sessions where one dedicated container
// is created per fleet session.
func NewNodeToolWithClient(original tool.Tool, client *LazyNodeClient) *NodeTool {
	return &NodeTool{
		Tool:       original,
		lazyClient: client,
	}
}

// Declaration returns the original tool's function declaration (schema).
// This is required by ADK's FunctionTool interface.
func (nt *NodeTool) Declaration() *genai.FunctionDeclaration {
	if dt, ok := nt.Tool.(ToolWithDeclaration); ok {
		return dt.Declaration()
	}
	return nil
}

// ProcessRequest packs the tool declaration into the LLM request.
// This replicates ADK's internal toolutils.PackTool logic since that
// package is internal and cannot be imported directly.
//
// Additionally, it eagerly triggers container creation via BindSession.
// ProcessRequest is called before the LLM call, so the container starts
// cloning in the background while the LLM generates its response. By the
// time the first tool call arrives, the container is likely already running.
// When NetworkPolicyStores are on the context, allow endpoints are stashed
// on the client before BindSession so OpenShell CreateSandbox can bake them in.
func (nt *NodeTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	// Eagerly bind the session — starts container creation in background.
	// Idempotent: only the first call per session triggers init.
	if ctx != nil {
		if sessionID := ctx.SessionID(); sessionID != "" {
			client := nt.getClientFromContext(ctx, sessionID)
			if client != nil {
				nt.applyNetworkAllowEndpoints(ctx, client)
				client.BindSession(sessionID)
			}
		}
	}

	if req.Tools == nil {
		req.Tools = make(map[string]any)
	}

	name := nt.Name()
	if _, ok := req.Tools[name]; ok {
		return fmt.Errorf("duplicate tool: %q", name)
	}
	req.Tools[name] = nt

	decl := nt.Declaration()
	if decl == nil {
		return nil
	}

	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}

	// Find an existing genai.Tool with FunctionDeclarations
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

// Run proxies the tool call to the container's astonish node process.
// On the first call, the LazyNodeClient creates the session container and
// starts the node. The session ID is extracted from tool.Context.SessionID().
func (nt *NodeTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	// Extract session ID from ADK context for container routing
	var sessionID string
	if ctx != nil {
		sessionID = ctx.SessionID()
	}
	if sessionID == "" {
		// No session ID — fall back to running on the original tool locally
		if runner, ok := nt.Tool.(interface {
			Run(tool.Context, any) (map[string]any, error)
		}); ok {
			return runner.Run(ctx, args)
		}
		return nil, fmt.Errorf("tool %q: no session ID and no local fallback", nt.Name())
	}

	client := nt.getClientFromContext(ctx, sessionID)
	if client == nil {
		return nil, fmt.Errorf("tool %q: no sandbox client available for session %s", nt.Name(), sessionID)
	}

	// Convert args to map[string]interface{}
	argsMap, ok := args.(map[string]interface{})
	if !ok {
		// Try JSON round-trip for struct args
		data, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal args: %w", err)
		}
		argsMap = make(map[string]interface{})
		if err := json.Unmarshal(data, &argsMap); err != nil {
			return nil, fmt.Errorf("failed to unmarshal args to map: %w", err)
		}
	}

	// Inject caller identity for per-agent cache scoping inside the container.
	// The container-side node handler extracts and removes this before dispatching.
	if ctx != nil {
		if agentName := ctx.AgentName(); agentName != "" {
			argsMap["_caller"] = agentName
		}
	}

	nt.applyNetworkAllowEndpoints(ctx, client)

	// Ensure sandbox is up, then PreSeed DB allow rules BEFORE any tool Exec
	// so the first Keystone/HTTP CONNECT is not CONNECT-403.
	if err := client.EnsureReady(sessionID); err != nil {
		return nil, err
	}
	ensureNetworkPolicyPreSeed(ctx, sessionID)

	// Provision session credential vault only for http_request so a PushFile
	// failure cannot brick shell/file tools. Keystone/OAuth stay in-sandbox.
	if nt.Name() == "http_request" {
		if err := SyncSessionCredentialVault(ctx, nt.backendForPush(client), nil, sessionID); err != nil {
			return nil, err
		}
	}

	// Send to node (lazy init on first call)
	rawResult, err := client.Call(sessionID, nt.Name(), argsMap)
	if err != nil {
		return nil, err
	}

	// Unmarshal the result
	var result map[string]any
	if rawResult != nil {
		if err := json.Unmarshal(rawResult, &result); err != nil {
			// If it's not a map, wrap it
			return map[string]any{"result": json.RawMessage(rawResult)}, nil
		}
	}

	return result, nil
}

// networkAllowEndpointSetter is implemented by backendNodeClient so OpenShell
// CreateSession can bake DB allow rules into the initial sandbox policy.
type networkAllowEndpointSetter interface {
	SetNetworkAllowEndpoints(endpoints []NetworkAllowEndpoint)
}

func (nt *NodeTool) applyNetworkAllowEndpoints(ctx context.Context, client ToolNodeClient) {
	if ctx == nil || client == nil {
		return
	}
	setter, ok := client.(networkAllowEndpointSetter)
	if !ok {
		return
	}
	eps := collectAllowEndpointsFromContext(ctx)
	if len(eps) == 0 {
		return
	}
	setter.SetNetworkAllowEndpoints(eps)
}

// collectAllowEndpointsFromContext mirrors netpolicy.CollectAllowEndpoints
// without importing netpolicy (avoids sandbox ↔ openshell import cycle).
func collectAllowEndpointsFromContext(ctx context.Context) []NetworkAllowEndpoint {
	nps := store.NetworkPolicyStoresFromContext(ctx)
	if nps == nil {
		return nil
	}
	var out []NetworkAllowEndpoint
	for _, s := range []store.NetworkPolicyStore{nps.Platform, nps.Org, nps.Team} {
		if s == nil {
			continue
		}
		rules, err := s.List(ctx)
		if err != nil {
			continue
		}
		for _, r := range rules {
			if r.Action == store.NetworkPolicyAllow {
				out = append(out, NetworkAllowEndpoint{Host: r.Host, Port: r.Port})
			}
		}
	}
	return out
}

// SyncSessionCredentialVault materializes the host CredentialStore into the
// sandbox at sessioncreds.VaultPath. Host only provisions bytes — it does not
// Resolve or perform Keystone/HTTP. No-op when no store or no PushFile backend.
func SyncSessionCredentialVault(ctx context.Context, backend Backend, ensureReady func(sessionID string) error, sessionID string) error {
	if ctx == nil {
		return nil
	}
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		return nil
	}
	if backend == nil {
		return nil
	}
	if ensureReady != nil {
		if err := ensureReady(sessionID); err != nil {
			return fmt.Errorf("session credential vault: ensure ready: %w", err)
		}
	}
	data, err := sessioncreds.Serialize(ctx, cs)
	if err != nil {
		return fmt.Errorf("session credential vault: serialize: %w", err)
	}
	if err := backend.PushFile(ctx, sessionID, sessioncreds.VaultPath, bytes.NewReader(data), sessioncreds.VaultFileMode); err != nil {
		return fmt.Errorf("session credential vault: push %s: %w", sessioncreds.VaultPath, err)
	}
	return nil
}

func (nt *NodeTool) backendForPush(client ToolNodeClient) Backend {
	if nt.pool != nil {
		if b := nt.pool.GetBackend(); b != nil {
			return b
		}
	}
	type backendGetter interface {
		GetBackend() Backend
	}
	if g, ok := client.(backendGetter); ok {
		return g.GetBackend()
	}
	return nil
}

// getClientFromContext returns the ToolNodeClient for the given session.
// If the NodeTool was created with a pool (chat/flow sessions), it gets
// or creates a client from the pool, using any sandbox template found in
// the context. If created with a direct LazyNodeClient (fleet sessions),
// returns that (which already satisfies ToolNodeClient via the Phase E
// compile-time assertion in node_interfaces.go).
func (nt *NodeTool) getClientFromContext(ctx context.Context, sessionID string) ToolNodeClient {
	if nt.lazyClient != nil {
		return nt.lazyClient
	}
	if nt.pool != nil {
		tpl := store.SandboxTemplateFromContext(ctx)
		chain := store.SandboxLayerChainFromContext(ctx)
		image := store.SandboxImageFromContext(ctx)
		// Resolve the caller's tenant from the live request context and record
		// it for this session BEFORE the pool creates the client. Platform pools
		// use this to persist the container record into the caller's own team
		// schema. Without this, the pool would use whatever registry/team was
		// bound when the process-wide shared chat agent was first constructed,
		// sending every team's containers into a single schema (cross-tenant bug).
		orgSlug := store.OrgSlugFromContext(ctx)
		teamSlug := store.TeamSlugFromContext(ctx)
		if orgSlug != "" || teamSlug != "" {
			nt.pool.SetSessionScope(sessionID, orgSlug, teamSlug)
		}
		if len(chain) > 0 || image != "" {
			// Use the full-context method when we have a chain and/or image.
			return nt.pool.GetOrCreateWithImage(sessionID, tpl, chain, image)
		}
		if tpl != "" {
			return nt.pool.GetOrCreateWithTemplate(sessionID, tpl)
		}
		return nt.pool.GetOrCreate(sessionID)
	}
	return nil
}

// containerTools lists tool names that should execute inside the container.
// Tools NOT in this set run on the host (memory, credentials, scheduler, etc.).
var containerTools = map[string]bool{
	"read_file":                 true,
	"write_file":                true,
	"edit_file":                 true,
	"file_tree":                 true,
	"grep_search":               true,
	"find_files":                true,
	"repo_map":                  true,
	"code_definition":           true,
	"code_references":           true,
	"shell_command":             true,
	"process_read":              true,
	"process_write":             true,
	"process_list":              true,
	"process_kill":              true,
	"http_request":              true,
	"web_fetch":                 true,
	"read_pdf":                  true,
	"filter_json":               true,
	"git_diff_add_line_numbers": true,
}

// WrapToolsWithNode wraps tools with NodeTool proxies using a concrete
// Incus-bound pool, selectively. Only tools listed in containerTools
// are wrapped — they run inside the container. Host-side tools (memory,
// credentials, scheduler, delegate_tasks, browser, email, etc.) pass
// through unwrapped and continue to run on the host.
//
// Kept for backward compatibility with chat_factory and other callers
// that hold *NodeClientPool directly. Internally forwards to
// WrapToolsWithPool via the AsNodePool adapter.
func WrapToolsWithNode(tools []tool.Tool, pool *NodeClientPool) []tool.Tool {
	return WrapToolsWithPool(tools, AsNodePool(pool))
}

// WrapToolsWithPool is the backend-agnostic variant of
// WrapToolsWithNode. It accepts any ToolNodePool implementation, so the
// flow setup can pass either the Incus pool (wrapped via AsNodePool) or
// the K8s backend pool (NewBackendPool) without touching this file's
// wrapping logic.
//
// Same selective-wrapping rule as WrapToolsWithNode: tools in
// containerTools are wrapped; all others pass through.
func WrapToolsWithPool(tools []tool.Tool, pool ToolNodePool) []tool.Tool {
	wrapped := make([]tool.Tool, len(tools))
	for i, t := range tools {
		if containerTools[t.Name()] {
			wrapped[i] = NewNodeToolWithPool(t, pool)
		} else {
			wrapped[i] = t // pass through unwrapped
		}
	}
	return wrapped
}

// WrapToolsWithNodeClient wraps tools with NodeTool proxies using a single
// LazyNodeClient, selectively. Same filtering as WrapToolsWithNode but uses
// a dedicated client instead of a pool. Used by fleet sessions where each
// session has its own LazyNodeClient.
func WrapToolsWithNodeClient(tools []tool.Tool, lazyClient *LazyNodeClient) []tool.Tool {
	wrapped := make([]tool.Tool, len(tools))
	for i, t := range tools {
		if containerTools[t.Name()] {
			wrapped[i] = NewNodeToolWithClient(t, lazyClient)
		} else {
			wrapped[i] = t // pass through unwrapped
		}
	}
	return wrapped
}
