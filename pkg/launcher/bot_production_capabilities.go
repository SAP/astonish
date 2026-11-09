package launcher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	astonishmcp "github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// BotHTTPTool provisions Chat's credential vault into the run sandbox. Token
// refresh, file uploads and HTTP itself stay in the node, behind its network
// policy. In particular no credential Resolve call is made on the daemon.
func BotHTTPTool(backend sandbox.Backend, client sandbox.ToolNodeClient, sessionID string) (tool.Tool, error) {
	if backend == nil || client == nil || sessionID == "" {
		return nil, store.ErrBotUnsupported
	}
	schema, err := functiontool.New(functiontool.Config{Name: "http_request", Description: "Make an HTTP request in the run sandbox using owner-scoped credentials"}, tools.HttpRequest)
	if err != nil {
		return nil, err
	}
	return &botHTTPTool{botSandboxTool: &botSandboxTool{Tool: schema, client: client, sessionID: sessionID}, backend: backend}, nil
}

type botHTTPTool struct {
	*botSandboxTool
	backend sandbox.Backend
}

func (t *botHTTPTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil || store.CredentialStoreFromContext(ctx) == nil {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Hydrate before provisioning so even failed backend diagnostics are scrubbed.
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(store.CredentialStoreFromContext(ctx))
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err := common.ValidateToolArguments(t.Tool, values); err != nil {
		return nil, err
	}
	// Keep placeholders symbolic across the daemon boundary. The node resolves
	// them from this run's vault (including token refresh) and redacts fresh tokens.
	if err := sandbox.SyncSessionCredentialVault(ctx, t.backend, nil, t.sessionID); err != nil {
		return nil, fmt.Errorf("credential vault: %s", redactor.Redact(err.Error()))
	}
	result, err := t.botSandboxTool.Run(ctx, values)
	if err != nil {
		return nil, fmt.Errorf("sandbox HTTP: %s", redactor.Redact(err.Error()))
	}
	return redactor.RedactMap(result), nil
}

// BotMCPTools is metadata-only. Missing metadata exposes a separately reviewed
// discovery capability; preparation never starts servers or provisions credentials.
func BotMCPTools(ctx context.Context, pool sandbox.ToolNodePool, sessionID string, stores ...store.MCPServerStore) ([]tool.Tool, func(), error) {
	cleanup := func() {}
	if pool == nil || pool.GetBackend() == nil || sessionID == "" {
		return nil, cleanup, store.ErrBotUnsupported
	}
	if store.CredentialStoreFromContext(ctx) == nil {
		return nil, cleanup, store.ErrBotForbidden
	}
	client := pool.GetOrCreate(sessionID)
	if client == nil {
		return nil, cleanup, store.ErrBotUnsupported
	}
	servers := map[string]store.MCPServer{}
	for _, s := range stores {
		if s == nil {
			continue
		}
		rows, err := s.List(ctx)
		if err != nil {
			return nil, cleanup, err
		}
		for _, row := range rows {
			servers[row.Name] = row
		}
	}
	var result []tool.Tool
	for _, s := range servers {
		if !s.IsEnabled() {
			continue
		}
		cfg := botMCPConfig(s)
		var entries []cache.ToolEntry
		if len(s.CachedTools) > 0 {
			if err := json.Unmarshal(s.CachedTools, &entries); err != nil {
				return nil, cleanup, fmt.Errorf("invalid cached MCP declarations for %q", s.Name)
			}
		}
		if len(entries) == 0 {
			identity := "mcp-discovery:" + s.Name
			schema, err := functiontool.New(functiontool.Config{Name: identity, Description: "MCP metadata unavailable. Request exact approval to start this configured server and discover its tools. This does not approve any tool calls; rebuild the catalogue after discovery."}, func(tool.Context, struct{}) (map[string]any, error) { return nil, store.ErrBotForbidden })
			if err != nil {
				return nil, cleanup, err
			}
			result = append(result, &botMCPTool{botRunnableTool: schema.(botRunnableTool), sessionID: sessionID, identity: identity, backend: pool.GetBackend(), client: client, config: cfg, serverName: s.Name, stores: stores, action: astonishmcp.NodeDiscover})
			continue
		}
		// Use Chat's declaration proxy only; never connect its host transport.
		lazy := agent.NewLazyMCPToolset(s.Name, entries, cfg, false)
		proxies, err := lazy.Tools(nil)
		if err != nil {
			return nil, cleanup, err
		}
		for _, p := range proxies {
			runnable, ok := p.(botRunnableTool)
			if !ok {
				return nil, cleanup, store.ErrBotUnsupported
			}
			result = append(result, &botMCPTool{botRunnableTool: runnable, sessionID: sessionID, identity: agent.BotToolIdentity("mcp:"+s.Name, p.Name()), backend: pool.GetBackend(), client: client, config: cfg, toolName: p.Name(), serverName: s.Name, stores: stores, action: astonishmcp.NodeCall})
		}
	}
	return result, cleanup, nil
}

type botMCPTool struct {
	botRunnableTool
	sessionID  string
	identity   string
	backend    sandbox.Backend
	client     sandbox.ToolNodeClient
	config     config.MCPServerConfig
	toolName   string
	serverName string
	stores     []store.MCPServerStore
	action     astonishmcp.NodeAction
}

func (t *botMCPTool) Name() string { return t.identity }
func (t *botMCPTool) Declaration() *genai.FunctionDeclaration {
	d := *t.botRunnableTool.Declaration()
	d.Name = t.Name()
	return &d
}
func (t *botMCPTool) ProcessRequest(ctx tool.Context, r *model.LLMRequest) error {
	return (&botSandboxTool{Tool: t}).ProcessRequest(ctx, r)
}

type botMCPAuthorization struct {
	target      *botMCPTool
	fingerprint string
	check       func(context.Context) error
}

func (t *botMCPTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	auth, ok := ctx.Value(botMCPAuthorizedKey{}).(botMCPAuthorization)
	if !ok || auth.target != t || auth.check == nil {
		return nil, store.ErrBotForbidden
	}
	if auth.fingerprint != botMCPFingerprint(t.config) {
		return nil, store.ErrBotConflict
	}
	selected, err := t.currentStore(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err = common.ValidateToolArguments(t, values); err != nil {
		return nil, err
	}
	if t.action == astonishmcp.NodeDiscover && len(values) != 0 {
		return nil, store.ErrBotInvalid
	}
	result, err := botMCPNodeRequest(ctx, t.backend, t.client, t.sessionID, astonishmcp.NodeRequest{Action: t.action, Config: t.config, Tool: t.toolName, Arguments: values})
	if err != nil {
		return nil, err
	}
	if t.action == astonishmcp.NodeDiscover {
		// Cache metadata only after successful reviewed discovery; a subsequent
		// catalogue construction exposes calls, each requiring its own approval.
		if _, err := t.currentStore(ctx); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(result["tools"])
		if err != nil {
			return nil, err
		}
		if err := selected.UpdateCachedTools(ctx, t.serverName, raw); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func botMCPNodeRequest(ctx context.Context, backend sandbox.Backend, client sandbox.ToolNodeClient, id string, request astonishmcp.NodeRequest) (map[string]any, error) {
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(cs)
	if err := sandbox.SyncSessionCredentialVault(ctx, backend, nil, id); err != nil {
		return nil, fmt.Errorf("MCP vault: %s", redactor.Redact(err.Error()))
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	// PushFile may block past cancellation, lease revocation, or a target edit.
	// Revalidate the captured authorization immediately before node dispatch;
	// this refreshes the authoritative binding, not the already-consumed grant.
	auth, ok := ctx.Value(botMCPAuthorizedKey{}).(botMCPAuthorization)
	if !ok || auth.target == nil || auth.check == nil {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if auth.fingerprint != botMCPFingerprint(request.Config) {
		return nil, store.ErrBotConflict
	}
	if _, err := auth.target.currentStore(ctx); err != nil {
		return nil, fmt.Errorf("MCP target: %s", redactor.Redact(err.Error()))
	}
	if err := auth.check(ctx); err != nil {
		return nil, fmt.Errorf("MCP authority: %s", redactor.Redact(err.Error()))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err = client.Call(id, astonishmcp.NodeOperation, values)
	if err != nil {
		return nil, fmt.Errorf("MCP node: %s", redactor.Redact(err.Error()))
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("invalid MCP node response")
	}
	return redactor.RedactMap(result), nil
}
