package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// Same configured constructor/catalog as the service parity suite, with real
// ExecuteTool in independent disposable node processes. No process implementation
// or ownership policy is stubbed. Membership is real SQLite; lease snapshots and
// single-use exact-grant consumption use the existing authority fixtures.
type processParityRuntime struct {
	r          *servicesParityRuntime
	node       *processParityNode
	scope      chatruntime.ResourceScope
	mode       string
	membership liveMembershipFixture
}

func newProcessParityRuntime(t *testing.T, scope chatruntime.ResourceScope, mode string) *processParityRuntime {
	t.Helper()
	r := newServicesParityRuntime(t, scope, mode)
	p := &processParityRuntime{r: r, scope: scope, mode: mode}
	p.membership = newLiveMembershipFixture(t, r, scope)
	// Installs the real interactive admission-registry authorizer/current member
	// callback before replacing only the node transport in the configured recipe.
	liveMembershipReconstruct(t, r, p.membership, scope, mode)
	p.node = newProcessParityNode(t, r.node.session, r.node.root)
	prepare := r.f.cfg.Prepare
	r.f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scoped *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scoped)
		if err == nil {
			safe.Catalog = &ChatCatalogResources{Pool: productionCapabilityPool{backend: r.node.backend, client: p.node}, Backend: r.node.backend}
			safe.PolicyRuntime = agent.BotPolicyRuntime{Classifier: BotCatalogueClassifier{}, Guard: r.guard}
		}
		return safe, err
	}
	p.rebuild(t)
	return p
}
func (p *processParityRuntime) rebuild(t *testing.T) {
	t.Helper()
	r, f := p.r, p.r.f
	if p.mode != "ordinary-chat" {
		built, err := f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		r.chat = built.Agent
		t.Cleanup(built.Cleanup)
		return
	}
	cfg := &config.AppConfig{}
	pool := productionCapabilityPool{backend: r.node.backend, client: p.node}
	resources := ChatCatalogResources{Pool: pool, Backend: r.node.backend}
	inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: p.scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	if p.scope == chatruntime.ResourceScopePersonal {
		factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: p.node.root}, resources, nil)
		built, err := factory(f.ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: f.resolver.model, SessionService: f.sessions, SessionID: p.node.session, UserID: f.req.Principal.Subject, AppName: "astonish"})
		if err != nil {
			t.Fatal(err)
		}
		r.chat = built.ChatAgent
		t.Cleanup(built.Cleanup)
	} else {
		// No public ordinary team Studio route exists. This is the same shared
		// constructor with an explicitly resolved team scope, not HTTP admission.
		built, err := NewScopedChatRuntime(f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: p.node.root}, Resources: &resources, LLM: f.resolver.model, Sessions: f.sessions, SessionID: p.node.session, UseScopedEngines: true}, ChatRuntimeHooks{})
		if err != nil {
			t.Fatal(err)
		}
		r.chat = built.ChatAgent
		t.Cleanup(built.Cleanup)
	}
}
func (p *processParityRuntime) approve(t *testing.T, name string, args map[string]any) {
	t.Helper()
	if p.mode != "autonomous-bot" {
		return
	}
	// A process ID is learned from the actual start result. Issue a NEW exact
	// approval and reconstruct the same run; never predict IDs or mutate a policy.
	drillGrants(t, p.r.f, map[string][]map[string]any{name: {args}})
	p.rebuild(t)
}
func (p *processParityRuntime) invoke(t *testing.T, ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	entry := p.r.chat.ToolIndex.GetToolEntry(name)
	if entry == nil {
		t.Fatalf("missing configured %s", name)
	}
	shared, ok := entry.Tool.(*chatRuntimeTool)
	if !ok {
		t.Fatalf("%s bypasses shared binding: %T", name, entry.Tool)
	}
	if _, ok := shared.chatRuntimeRunnable.(*sandbox.NodeTool); !ok {
		t.Fatalf("%s is not shared NodeTool: %T", name, shared.chatRuntimeRunnable)
	}
	return p.r.invoke(t, ctx, name, args)
}
func (p *processParityRuntime) call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	out, err := p.invoke(t, p.r.f.ctx, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out["error"] != nil && out["error"] != "" {
		t.Fatalf("%s: %v", name, out)
	}
	return out
}
func (p *processParityRuntime) assertDeniedBeforeNode(t *testing.T, ctx context.Context, name string, args map[string]any) {
	t.Helper()
	calls, grants := len(p.node.calls), len(p.r.guard.consumed)
	if _, err := p.invoke(t, ctx, name, args); err == nil {
		t.Fatalf("%s escaped authority", name)
	}
	if len(p.node.calls) != calls || len(p.r.guard.consumed) != grants {
		t.Fatalf("%s denial dispatched/consumed grant", name)
	}
}

func TestChatRuntimeConfiguredProcessAndFileExecution(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				p := newProcessParityRuntime(t, scope, mode)
				path := filepath.Join(p.node.root, "parity.txt")
				start := map[string]any{"command": processParityCommand, "working_dir": p.node.root, "background": true}
				p.approve(t, "shell_command", start)
				p.membership.revoke()
				p.assertDeniedBeforeNode(t, p.r.f.ctx, "shell_command", start)
				p.membership.restore()
				if mode != "ordinary-chat" {
					lease := p.r.f.req.Run.Lease
					p.r.f.req.Run.Lease.Fence++
					p.assertDeniedBeforeNode(t, p.r.f.ctx, "shell_command", start)
					p.r.f.req.Run.Lease = lease
				}
				if mode == "autonomous-bot" {
					changed := map[string]any{"command": processParityCommand + "; printf unapproved", "working_dir": p.node.root, "background": true}
					p.assertDeniedBeforeNode(t, p.r.f.ctx, "shell_command", changed)
				}
				grantsBeforeStart := len(p.r.guard.consumed)
				started := p.call(t, "shell_command", start)
				if mode == "autonomous-bot" && len(p.r.guard.consumed) != grantsBeforeStart+1 {
					t.Fatal("start did not consume exact grant")
				}
				id, _ := started["session_id"].(string)
				if id == "" {
					t.Fatalf("no process ID: %v", started)
				}
				p.node.handshake(t, id, "ready\r\n")
				// A separate real node owns a different process namespace. A genuine ID
				// from this node must be unknown there, even with identical public args.
				foreign := newProcessParityNode(t, uuid.NewString(), t.TempDir())
				foreignRaw, err := foreign.Call(foreign.session, "shell_command", map[string]any{"command": processParityCommand, "working_dir": foreign.root, "background": true})
				if err != nil {
					t.Fatal(err)
				}
				var foreignStarted map[string]any
				if err := json.Unmarshal(foreignRaw, &foreignStarted); err != nil {
					t.Fatal(err)
				}
				foreignID, _ := foreignStarted["session_id"].(string)
				if foreignID == "" || foreignID == id {
					t.Fatal("invalid foreign process fixture")
				}
				foreign.handshake(t, foreignID, "ready\r\n")
				for _, name := range []string{"process_read", "process_write", "process_kill"} {
					foreignArgs := map[string]any{"session_id": foreignID}
					if name == "process_write" {
						foreignArgs["input"] = "forbidden\n"
					}
					p.approve(t, name, foreignArgs)
					if _, err := p.invoke(t, p.r.f.ctx, name, foreignArgs); err == nil || !strings.Contains(err.Error(), "not found") {
						t.Fatalf("configured foreign process %s: %v", name, err)
					}
					args := map[string]any{"session_id": id}
					if name == "process_write" {
						args["input"] = "foreign\n"
					}
					if _, err := foreign.Call(foreign.session, name, args); err == nil || !strings.Contains(err.Error(), "not found") {
						t.Fatalf("foreign namespace %s: %v", name, err)
					}
				}
				foreignCheck, err := foreign.Call(foreign.session, "process_read", map[string]any{"session_id": foreignID})
				if err != nil {
					t.Fatal(err)
				}
				var foreignState map[string]any
				if err := json.Unmarshal(foreignCheck, &foreignState); err != nil {
					t.Fatal(err)
				}
				if foreignState["running"] != true || strings.Contains(string(foreignCheck), "forbidden") {
					t.Fatalf("foreign process affected: %s", foreignCheck)
				}
				actions := []struct {
					name string
					args map[string]any
					want string
				}{
					{"process_read", map[string]any{"session_id": id}, "ready"},
					{"process_write", map[string]any{"session_id": id, "input": "parity-token\n"}, ""},
					{"process_list", map[string]any{"filter": id}, id},
					{"write_file", map[string]any{"path": path, "content": "before\n"}, ""},
					{"edit_file", map[string]any{"path": path, "old_string": "before", "new_string": "after"}, ""},
					{"read_file", map[string]any{"path": path, "force": true}, "after"},
					{"process_kill", map[string]any{"session_id": id}, "killed"},
				}
				for _, action := range actions {
					p.approve(t, action.name, action.args)
					// Fresh grants survive actual membership removal/restoration. Retain the
					// same executable; positive execution follows the denial without rebuilding.
					p.membership.revoke()
					p.assertDeniedBeforeNode(t, p.r.f.ctx, action.name, action.args)
					p.membership.restore()
					foreignPrincipal := p.r.f.req.Principal
					foreignPrincipal.TeamSlug = "foreign-team"
					foreignContext, err := execution.WithPrincipal(context.Background(), foreignPrincipal)
					if err != nil {
						t.Fatal(err)
					}
					p.assertDeniedBeforeNode(t, foreignContext, action.name, action.args)
					cancelled, cancel := context.WithCancel(p.r.f.ctx)
					cancel()
					p.assertDeniedBeforeNode(t, cancelled, action.name, action.args)
					if mode != "ordinary-chat" {
						lease := p.r.f.req.Run.Lease
						p.r.f.req.Run.Lease.Fence++
						p.assertDeniedBeforeNode(t, p.r.f.ctx, action.name, action.args)
						p.r.f.req.Run.Lease = lease
					}
					if mode == "autonomous-bot" && action.name != "read_file" {
						changed := make(map[string]any, len(action.args))
						for k, v := range action.args {
							changed[k] = v
						}
						switch action.name {
						case "process_read", "process_kill":
							changed["session_id"] = "not-approved"
						case "process_write":
							changed["input"] = "not-approved\n"
						case "process_list":
							changed["filter"] = "not-approved"
						case "write_file":
							changed["content"] = "not-approved"
						case "edit_file":
							changed["new_string"] = "not-approved"
						}
						p.assertDeniedBeforeNode(t, p.r.f.ctx, action.name, changed)
					}
					before := len(p.r.guard.consumed)
					out := p.call(t, action.name, action.args)
					raw, _ := json.Marshal(out)
					if action.want != "" && !strings.Contains(string(raw), action.want) {
						t.Fatalf("%s result=%s", action.name, raw)
					}
					if action.name == "process_list" {
						if out["count"] != float64(1) || strings.Contains(string(raw), foreignID) {
							t.Fatalf("process listing escaped node namespace: %s", raw)
						}
					}
					if action.name == "process_write" {
						p.node.handshake(t, id, "ack:parity-token\r\n")
					}
					if mode == "autonomous-bot" && action.name != "read_file" {
						if len(p.r.guard.consumed) != before+1 {
							t.Fatalf("%s did not consume exact grant", action.name)
						}
						p.assertDeniedBeforeNode(t, p.r.f.ctx, action.name, action.args)
					}
				}
				body, err := os.ReadFile(path)
				if err != nil || string(body) != "after\n" {
					t.Fatalf("exact file readback=%q err=%v", body, err)
				}
				p.approve(t, "process_read", map[string]any{"session_id": id})
				out := p.call(t, "process_read", map[string]any{"session_id": id})
				if out["running"] != false || out["exit_code"] == nil {
					t.Fatalf("owned child not reaped: %v", out)
				}
				// A stopped configured transport cannot revive the local tool fallback.
				p.approve(t, "write_file", map[string]any{"path": path, "content": "host-fallback"})
				p.node.stopped = true
				if _, err := p.invoke(t, p.r.f.ctx, "write_file", map[string]any{"path": path, "content": "host-fallback"}); err == nil {
					t.Fatal("unavailable node succeeded")
				}
				body, err = os.ReadFile(path)
				if err != nil || string(body) != "after\n" {
					t.Fatal("unavailable node used host fallback")
				}
				if p.r.f.resolver.model.calls != 0 {
					t.Fatal("fixture unexpectedly called provider")
				}
			})
		}
	}
}
