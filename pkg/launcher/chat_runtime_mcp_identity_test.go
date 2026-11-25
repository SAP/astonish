package launcher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type mcpIdentityStartupBackend struct {
	sandbox.Backend
	afterStart func()
}

func (b *mcpIdentityStartupBackend) ExecStreaming(ctx context.Context, id string, spec sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	stream, err := b.Backend.ExecStreaming(ctx, id, spec)
	if err == nil {
		b.afterStart()
	}
	return stream, err
}

func grantSharedMCP(t *testing.T, r *servicesParityRuntime, server store.MCPServer, identity string) execution.ScopedGrant {
	t.Helper()
	action := execution.ActionDescriptor{Tool: identity, Arguments: json.RawMessage(`{}`), Effect: execution.EffectUnknown, TargetFingerprint: botMCPFingerprint(botMCPConfig(server))}
	digest, err := action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id := r.f.req.Policy.Identity()
	grant := execution.ScopedGrant{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}
	r.f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{identity}, Grants: []execution.ScopedGrant{grant}})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

// Only the qualified executable and exact symbolic configuration are approved.
// The fixture is an in-process MCP protocol peer, not physical compute.
func TestChatRuntimeMCPExactQualifiedIdentity(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, scenario := range []string{"exact", "bridge", "exact-capacity", "arguments", "command", "server-args", "url", "disabled", "env", "server-name", "tool-name", "bare-grant", "cancelled", "startup-config", "startup-lease"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				r := newServicesParityRuntime(t, scope, "autonomous-bot")
				server := store.MCPServer{Name: "identity", Command: "fixture-mcp", Env: map[string]string{"MAIL_SECRET": "{{CREDENTIAL:mail:password}}"}, CachedTools: json.RawMessage(`[{"name":"read_owner","description":"Identity probe","inputSchema":{"type":"object","properties":{}}}]`)}
				servers := newMemMCPStore(server)
				r.f.services.TeamMCPServers = servers
				identity := "mcp:identity/read_owner"
				approved := identity
				switch scenario {
				case "bare-grant":
					approved = "read_owner"
				case "tool-name":
					approved = "mcp:identity/other_tool"
				case "server-name":
					approved = "mcp:other/read_owner"
				}
				grant := grantSharedMCP(t, r, server, approved)
				spec := r.f.req.Version.Budget
				one := int64(2)
				if scenario == "exact-capacity" {
					one = 1
				}
				spec.MaxToolCalls = &one
				budget, err := bot.NewRunBudget(spec, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				r.f.req.Budget = budget
				if strings.HasPrefix(scenario, "startup-") {
					prepare := r.f.cfg.Prepare
					r.f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
						safe, err := prepare(ctx, req, scope)
						if err != nil {
							return safe, err
						}
						backend := &mcpIdentityStartupBackend{Backend: r.node.backend, afterStart: func() {
							if scenario == "startup-config" {
								changed := server
								changed.Command = "replacement"
								servers.servers[server.Name] = changed
							} else {
								r.f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
							}
						}}
						resources := *safe.Catalog
						resources.Pool = productionCapabilityPool{backend: backend, client: r.node}
						resources.Backend = backend
						safe.Catalog = &resources
						return safe, nil
					}
				}
				runtime, err := r.f.construct(t)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(runtime.Cleanup)
				r.chat = runtime.Agent
				if len(r.guard.consumed) != 0 || r.node.backend.starts != 0 {
					t.Fatal("construction consumed approval or started MCP")
				}
				args := map[string]any{}
				callCtx := r.f.ctx
				switch scenario {
				case "arguments":
					args["unreviewed"] = true
				case "command":
					changed := server
					changed.Command = "other-mcp"
					servers.servers[server.Name] = changed
				case "server-args":
					changed := server
					changed.Args = []string{"--unreviewed"}
					servers.servers[server.Name] = changed
				case "url":
					changed := server
					changed.URL = "https://unreviewed.invalid/mcp"
					servers.servers[server.Name] = changed
				case "disabled":
					changed := server
					disabled := false
					changed.Enabled = &disabled
					servers.servers[server.Name] = changed
				case "env":
					server.Env["MAIL_SECRET"] = "{{CREDENTIAL:other:password}}"
				case "cancelled":
					cancelled, cancel := context.WithCancel(callCtx)
					cancel()
					callCtx = cancelled
				}
				var out map[string]any
				if scenario == "bridge" {
					executable := r.chat.ToolIndex.GetToolEntry("read_owner").Tool.(*chatRuntimeTool)
					bound, release, bindErr := executable.binding.bind(callCtx)
					if bindErr != nil {
						t.Fatal(bindErr)
					}
					defer release()
					base := runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: bound}, sid: r.node.session}}}
					call := servicesParityContext{Context: base, owner: r.f.req.Principal.Subject, sid: r.node.session}
					bridge, bridgeErr := agent.NewProgressiveToolBridge(r.chat.ToolIndex)
					if bridgeErr != nil {
						t.Fatal(bridgeErr)
					}
					for _, tool := range bridge {
						if tool.Name() == "execute_tool" {
							out, err = tool.(chatRuntimeRunnable).Run(call, map[string]any{"name": "read_owner", "arguments": args})
						}
					}
				} else {
					out, err = r.invoke(t, callCtx, "read_owner", args)
				}
				raw, _ := json.Marshal(out)
				if scenario != "exact" && scenario != "exact-capacity" && scenario != "bridge" {
					if err == nil {
						t.Fatalf("mismatched MCP invocation succeeded: %s", raw)
					}
					if strings.HasPrefix(scenario, "startup-") {
						if len(r.guard.consumed) != 1 || r.node.backend.starts != 1 || budget.Usage().ToolCalls != 1 {
							t.Fatal("startup denial double consumed or skipped admission")
						}
						return
					}
					if len(r.guard.consumed) != 0 || r.node.backend.starts != 0 || budget.Usage().ToolCalls != 0 {
						t.Fatal("denial consumed approval/budget or started MCP")
					}
					return
				}
				if err != nil || !strings.Contains(string(raw), r.node.session) {
					t.Fatalf("qualified exact approval did not execute real MCP: output=%s err=%v", raw, err)
				}
				if len(r.guard.consumed) != 1 || !r.guard.consumed[grant.ID] || r.node.backend.starts != 1 || budget.Usage().ToolCalls != 1 {
					t.Fatalf("approval/transport/budget counts: grants=%d starts=%d usage=%+v", len(r.guard.consumed), r.node.backend.starts, budget.Usage())
				}
				if len(r.guard.seen) != 1 || r.guard.seen[0] != identity {
					t.Fatalf("execution borrowed unqualified authority: %v", r.guard.seen)
				}
				// The exact case retains spare capacity: failure proves grant replay rejection.
				if _, err := r.invoke(t, r.f.ctx, "read_owner", map[string]any{}); err == nil {
					t.Fatal("single-use approval replayed")
				}
				runtime.Cleanup()
				if _, err := r.invoke(t, r.f.ctx, "read_owner", map[string]any{}); err == nil {
					t.Fatal("cleaned runtime executed MCP")
				}
			})
		}
	}
}
