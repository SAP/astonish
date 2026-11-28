package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/adk/session"
)

type sharedPlanContext struct{ runtimeTestToolContext }

func (sharedPlanContext) UserID() string         { return "plan-user" }
func (sharedPlanContext) AppName() string        { return "astonish" }
func (sharedPlanContext) AgentName() string      { return "chat" }
func (sharedPlanContext) FunctionCallID() string { return "plan-call" }

type sharedPlanNode struct {
	sandbox.ToolNodeClient
	writes, verifies int
	markdown         string
	exit             int
}

func (*sharedPlanNode) BindSession(string)       {}
func (*sharedPlanNode) EnsureReady(string) error { return nil }
func (n *sharedPlanNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	if id != "runtime-test-session" {
		return nil, fmt.Errorf("wrong session %s", id)
	}
	switch name {
	case "write_file":
		if args["path"] != "/workspace/PLAN.md" {
			return nil, fmt.Errorf("wrong plan target %v", args)
		}
		n.writes++
		n.markdown, _ = args["content"].(string)
		return json.RawMessage(`{"success":true}`), nil
	case "shell_command":
		if args["command"] != "printf verified" || args["working_dir"] != "/workspace" {
			return nil, fmt.Errorf("wrong verification %v", args)
		}
		n.verifies++
		return json.Marshal(map[string]any{"exit_code": n.exit, "stdout": "verified"})
	}
	return nil, fmt.Errorf("unexpected operation %s", name)
}

type sharedPlanPool struct {
	continuationHTTPPool
	node *sharedPlanNode
}

func (p *sharedPlanPool) GetOrCreate(string) sandbox.ToolNodeClient { return p.node }
func (p *sharedPlanPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p.node
}
func (p *sharedPlanPool) GetOrCreateWithImage(string, string, []string, string) sandbox.ToolNodeClient {
	return p.node
}
func (*sharedPlanPool) GetBackend() sandbox.Backend { return nil }

type sharedPlanAuthorization struct {
	deny  string
	calls []string
}

func (a *sharedPlanAuthorization) Authorize(_ context.Context, name string, _ any) error {
	a.calls = append(a.calls, name)
	if name == a.deny {
		return fmt.Errorf("denied %s", name)
	}
	return nil
}

// Protocol-level backend fixture, not physical container or HTTP evidence. The
// catalog's actual NodeTools dispatch through the same guard as ordinary calls.
func TestChatRuntimeSharedPlanLifecycle(t *testing.T) {
	f := newBotFactoryFixture(t)
	cfg := &config.AppConfig{}
	inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer inputs.Close()
	sessions := session.InMemoryService()
	if _, err := sessions.Create(t.Context(), &session.CreateRequest{AppName: "astonish", UserID: "plan-user", SessionID: "runtime-test-session"}); err != nil {
		t.Fatal(err)
	}
	node := &sharedPlanNode{exit: 1}
	auth := &sharedPlanAuthorization{}
	makeRuntime := func() *ChatFactoryResult {
		t.Helper()
		r, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: "/workspace", AutoApprove: true}, Resources: &ChatCatalogResources{Pool: &sharedPlanPool{node: node}}, LLM: provider.NewPlaceholderLLM(), Sessions: sessions, SessionID: "runtime-test-session"}, ChatRuntimeHooks{Authorization: auth})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Cleanup)
		return r
	}
	r := makeRuntime()
	ctx := sharedPlanContext{runtimeTestToolContext{lifetime: t.Context()}}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		entry := r.ChatAgent.ToolIndex.GetToolEntry(name)
		if entry == nil {
			t.Fatalf("missing shared tool %s", name)
		}
		out, err := entry.Tool.(chatRuntimeRunnable).Run(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	args := map[string]any{"goal": "Controlled plan", "context": "Controlled context", "what_not_to_do": "No host calls", "verification": "Observe controlled verifier", "steps": []any{map[string]any{"name": "verify", "description": "Verify scope", "details": "Run controlled sandbox verifier", "summary": "Prove scoped verification", "outcome": "Verified", "files": []any{map[string]any{"path": "evidence.txt", "kind": "new"}}, "verify": "printf verified", "verify_kind": "behavior"}}}
	// Refusing a mirror write must not publish authoritative plan state.
	auth.deny = "write_file"
	if out := call("announce_plan", args); out["status"] != agent.PlanRuntimePersistFailed || node.writes != 0 {
		t.Fatalf("write denial published plan %v", out)
	}
	auth.deny = ""
	if out := call("announce_plan", args); out["status"] != "ok" {
		t.Fatalf("announce %v", out)
	}
	if out := call("announce_completion", map[string]any{"outcome_observed": "premature"}); out["status"] == "ok" {
		t.Fatal("premature completion")
	}
	if out := call("update_plan", map[string]any{"step": "verify", "status": "running"}); out["status"] != "ok" {
		t.Fatalf("running %v", out)
	}
	if out := call("update_plan", map[string]any{"step": "verify", "status": "complete"}); out["status"] != agent.PlanStepVerifyFailed {
		t.Fatalf("failed verify %v", out)
	}
	// A new attempt reads the authoritative snapshot, never the editable mirror.
	r.Cleanup()
	node.markdown = "untrusted edited plan"
	r = makeRuntime()
	node.exit = 0
	auth.deny = "shell_command"
	previous := node.verifies
	if out := call("update_plan", map[string]any{"step": "verify", "status": "complete"}); out["status"] == "ok" || node.verifies != previous {
		t.Fatalf("policy bypass %v", out)
	}
	auth.deny = ""
	if out := call("update_plan", map[string]any{"step": "verify", "status": "complete"}); out["status"] != "ok" {
		t.Fatalf("verify %v", out)
	}
	if out := call("announce_completion", map[string]any{"outcome_observed": "Scoped plan verified", "unverified": "Physical container"}); out["status"] != "ok" {
		t.Fatalf("complete %v", out)
	}
	if node.verifies != 2 || !strings.Contains(node.markdown, "## Results") || !strings.Contains(node.markdown, "Scoped plan verified") {
		t.Fatalf("missing evidence %d %s", node.verifies, node.markdown)
	}
	// Authoritative nil must survive Inputs.Bind and must not restore saved state.
	ctx.lifetime = agent.WithPlanRuntime(t.Context(), nil)
	if out := call("announce_plan", args); out["status"] != "no_active_plan" {
		t.Fatalf("bound nil revived plan %v", out)
	}
	// The trusted initializer cannot replace an existing binding (including nil)
	// and cannot silently omit its required transport runtime.
	for _, test := range []struct {
		name      string
		ctx       context.Context
		wantCalls int
	}{
		{"bound-nil", agent.WithPlanRuntime(t.Context(), nil), 0},
		{"bound-runtime", agent.WithPlanRuntime(t.Context(), agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{})), 0},
		{"nil-result", t.Context(), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			initializer := api.StudioChatPlanInitializer(func(func(string) (int, string, error)) (*agent.PlanRuntime, error) {
				calls++
				return nil, nil
			})
			attempt, err := NewScopedChatRuntime(test.ctx, ChatRuntimeDependencies{PlanInitializer: initializer, Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: "/workspace"}, Resources: &ChatCatalogResources{Pool: &sharedPlanPool{node: node}}, LLM: provider.NewPlaceholderLLM(), Sessions: sessions, SessionID: "runtime-test-session"}, ChatRuntimeHooks{})
			if attempt != nil {
				attempt.Cleanup()
			}
			if err == nil || attempt != nil || calls != test.wantCalls {
				t.Fatalf("initializer crossed authority boundary: attempt=%v err=%v calls=%d", attempt, err, calls)
			}
		})
	}
}
