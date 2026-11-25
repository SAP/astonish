package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Protocol acceptance, not physical container evidence. The same production
// adapters are resolved by each factory. Only endpoints, repositories and node
// transport are controlled fixtures; no replacement tool is registered.
func TestChatRuntimeCapabilityParityConfiguredProtocols(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot", "autonomous-approved"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("HOME", dir)
				t.Setenv("XDG_CONFIG_HOME", dir)
				f := newBotFactoryFixture(t)
				if scope == chatruntime.ResourceScopeTeam {
					f.req.Bot.Scope = store.BotScopeTeam
				}
				var mcpCalls, a2aCalls atomic.Int32
				server := sdk.NewServer(&sdk.Implementation{Name: "three-mode", Version: "1"}, nil)
				sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "Controlled protocol echo"}, func(ctx context.Context, req *sdk.CallToolRequest, args struct {
					Message string `json:"message"`
				}) (*sdk.CallToolResult, any, error) {
					if args.Message != "three-mode-question" {
						t.Errorf("wrong MCP arguments: %q", args.Message)
					}
					mcpCalls.Add(1)
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "three-mode-mcp-result"}}}, nil, nil
				})
				endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
				defer endpoint.Close()
				f.services.TeamMCPServers = newMemMCPStore(store.MCPServer{Name: "three-mode", Transport: "streamableHTTP", URL: endpoint.URL, CachedTools: json.RawMessage(`[{"name":"echo","description":"Controlled protocol echo","inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}}]`)})
				var remote *httptest.Server
				remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json" {
						_ = json.NewEncoder(w).Encode(map[string]any{"name": "three-mode", "url": remote.URL, "version": "1", "protocolVersion": "0.3.0", "capabilities": map[string]any{"streaming": false}, "skills": []any{}})
						return
					}
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					raw, _ := json.Marshal(request)
					if request["method"] != "message/send" || !strings.Contains(string(raw), "three-mode-question") {
						t.Errorf("wrong A2A request: %s", raw)
					}
					a2aCalls.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"kind": "task", "id": "three-mode-task", "status": map[string]any{"state": "completed", "message": map[string]any{"role": "agent", "parts": []any{map[string]any{"kind": "text", "text": "three-mode-a2a-result"}}}}}})
				}))
				defer remote.Close()
				f.services.TeamA2AAgents = &capabilityHTTPA2AStore{value: store.A2AAgent{Name: "three_mode", URL: remote.URL, Timeout: "5s"}}
				cfg := &config.AppConfig{}
				node := &capabilityHTTPNode{root: dir, mcpURL: endpoint.URL}
				pool := &capabilityHTTPPool{node: node}
				registry, err := sandbox.NewTemplateRegistry()
				if err != nil {
					t.Fatal(err)
				}
				if err := registry.Add(&sandbox.TemplateMeta{Name: "three-mode-template", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				resources := ChatCatalogResources{Pool: pool, TemplateRegistry: registry, Backend: &scopedBrowserTestBackend{t: t, owner: f.req.Principal.Subject}}
				grantGuard := &drillGuard{consumed: map[uuid.UUID]bool{}}
				if mode == "autonomous-approved" {
					// Exact, single-use grants only for the actions proven below.
					// MCP remains denied: its qualified identity requires a separate
					// classifier contract and must not borrow a native-tool grant.
					drillGrants(t, f, map[string][]map[string]any{
						"a2a/a2a_three_mode":     {{"message": "three-mode-question"}},
						"list_sandbox_templates": {{}},
					})
				}
				var chat *agent.ChatAgent
				var cleanup func()
				if mode == "ordinary-chat" {
					inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = inputs.Close() })
					factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg}, resources, nil)
					runtime, err := factory(f.ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: provider.NewPlaceholderLLM(), SessionService: f.sessions, SessionID: f.req.Run.SessionID.String(), UserID: f.req.Principal.Subject, AppName: "astonish"})
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.ChatAgent, runtime.Cleanup
				} else {
					if mode == "interactive-bot" {
						setBotSharedInteractive(f)
					} else if mode == "autonomous-bot" {
						f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"echo", "a2a/a2a_three_mode", "list_sandbox_templates"}})
						if err != nil {
							t.Fatal(err)
						}
					}
					prepare := f.cfg.Prepare
					f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
						safe, err := prepare(ctx, req, scope)
						if err == nil {
							safe.ChatConfig, safe.Catalog = cfg, &resources
							safe.PolicyRuntime.Classifier = BotCatalogueClassifier{}
							if mode == "autonomous-approved" {
								safe.PolicyRuntime.Guard = grantGuard
							}
						}
						return safe, err
					}
					runtime, err := f.construct(t)
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.Agent, runtime.Cleanup
				}
				t.Cleanup(cleanup)
				if mcpCalls.Load() != 0 || a2aCalls.Load() != 0 || len(node.names) != 0 {
					t.Fatal("constructor invoked a capability")
				}
				invoke := func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
					t.Helper()
					entry := chat.ToolIndex.GetToolEntry(name)
					if entry == nil {
						t.Fatalf("missing configured tool %s", name)
					}
					if _, ok := entry.Tool.(*chatRuntimeTool); !ok {
						t.Fatalf("%s bypassed shared wrapper: %T", name, entry.Tool)
					}
					return entry.Tool.(chatRuntimeRunnable).Run(runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ctx}, sid: f.req.Run.SessionID.String()}}}, args)
				}
				cases := []struct {
					name, want string
					args       map[string]any
				}{
					{"echo", "three-mode-mcp-result", map[string]any{"message": "three-mode-question"}},
					{"a2a_three_mode", "three-mode-a2a-result", map[string]any{"message": "three-mode-question"}},
					{"list_sandbox_templates", "three-mode-template", map[string]any{}},
				}
				if mode == "autonomous-approved" {
					// An argument change must not consume or broaden the exact grant.
					if _, err := invoke(f.ctx, "a2a_three_mode", map[string]any{"message": "unreviewed-question"}); err == nil || a2aCalls.Load() != 0 || len(grantGuard.consumed) != 0 {
						t.Fatal("changed arguments escaped exact authorization")
					}
				}
				if mode == "interactive-bot" || mode == "autonomous-approved" {
					// Exercise stale authority BEFORE consuming grants. Restoring the
					// fixture lease below must allow these same actions to execute;
					// denial cannot be attributed to an already-used approval.
					f.req.Run.Lease.Fence++
					for _, c := range cases[1:] {
						if _, err := invoke(f.ctx, c.name, c.args); err == nil {
							t.Fatalf("fresh-grant stale lease %s dispatched", c.name)
						}
					}
					f.req.Run.Lease.Fence--
					if a2aCalls.Load() != 0 || len(node.names) != 0 || len(grantGuard.consumed) != 0 {
						t.Fatal("stale authority performed work or consumed a fresh grant")
					}
				}
				for _, c := range cases {
					result, err := invoke(f.ctx, c.name, c.args)
					// All three are unknown effects to the current autonomous classifier.
					// An allowlist is not an exact approval; do not bypass this guard.
					if mode == "autonomous-bot" || (mode == "autonomous-approved" && c.name == "echo") {
						var denied *agent.BotPolicyError
						if !errors.As(err, &denied) {
							t.Fatalf("autonomous %s did not return policy denial: result=%v error=%v", c.name, result, err)
						}
						if mode == "autonomous-bot" && c.name == "a2a_three_mode" && denied.Decision.Kind != execution.PolicyRequestApproval {
							t.Fatalf("qualified allowlisted A2A did not require exact approval: %+v", denied.Decision)
						}
					} else {
						raw, _ := json.Marshal(result)
						if err != nil || !strings.Contains(string(raw), c.want) {
							t.Fatalf("%s: %s %v", c.name, raw, err)
						}
					}
				}
				if mode == "autonomous-approved" {
					if len(grantGuard.consumed) != 2 {
						t.Fatalf("exact grants consumed=%d", len(grantGuard.consumed))
					}
					for _, c := range cases[1:] {
						if _, err := invoke(f.ctx, c.name, c.args); err == nil {
							t.Fatalf("single-use %s grant replayed", c.name)
						}
					}
				}
				expected := int32(1)
				if mode == "autonomous-bot" {
					expected = 0
				}
				expectedMCP := expected
				if mode == "autonomous-approved" {
					expectedMCP = 0
				}
				if mcpCalls.Load() != expectedMCP || a2aCalls.Load() != expected {
					t.Fatalf("protocol calls MCP=%d A2A=%d want=%d", mcpCalls.Load(), a2aCalls.Load(), expected)
				}
				before := len(node.names)
				cancelled, cancel := context.WithCancel(f.ctx)
				cancel()
				foreign := f.req.Principal
				foreign.TeamSlug = "other-team"
				foreignCtx, err := execution.WithPrincipal(t.Context(), foreign)
				if err != nil {
					t.Fatal(err)
				}
				for _, ctx := range []context.Context{cancelled, foreignCtx} {
					for _, c := range cases {
						if _, err := invoke(ctx, c.name, c.args); err == nil {
							t.Fatalf("cancelled/foreign %s dispatched", c.name)
						}
					}
				}
				if mode != "ordinary-chat" {
					f.req.Run.Lease.Fence++
					for _, c := range cases {
						if _, err := invoke(f.ctx, c.name, c.args); err == nil {
							t.Fatalf("stale lease %s dispatched", c.name)
						}
					}
				}
				cleanup()
				cleanup()
				if mode != "ordinary-chat" {
					// Bot cleanup revokes its owner context; ordinary Chat's request
					// cancellation is transport-owned and was exercised above.
					for _, c := range cases {
						if _, err := invoke(f.ctx, c.name, c.args); err == nil {
							t.Fatalf("closed runtime %s dispatched", c.name)
						}
					}
				}
				if len(node.names) != before || mcpCalls.Load() != expectedMCP || a2aCalls.Load() != expected {
					t.Fatal("denial performed protocol work")
				}
				for _, sid := range node.sessions {
					if sid != f.req.Run.SessionID.String() {
						t.Fatal("MCP escaped session identity")
					}
				}
				if mode != "ordinary-chat" && (f.scopeCleaned != 1 || f.safeCleaned != 1) {
					t.Fatal("cleanup was not exactly once")
				}
			})
		}
	}
}
