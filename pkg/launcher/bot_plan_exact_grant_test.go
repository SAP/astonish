package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type planExactGrantGuard struct {
	grants map[string]execution.ScopedGrant
	calls  map[string]int
}

func (g *planExactGrantGuard) Authorize(_ context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	digest, _ := action.Digest()
	g.calls[action.Tool]++
	grant, ok := g.grants[digest]
	if !ok {
		return execution.PolicyDecision{}, fmt.Errorf("missing or consumed exact grant")
	}
	delete(g.grants, digest)
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest, GrantID: grant.ID}, nil
}

// Exercise the real scoped plan sub-dispatch with an already-consumed outer
// handoff. Its write must consume its own exact grant, never reuse the outer one.
func TestBotPlanPersistenceExactGrant(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprintf("write-approved=%v", approved), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			writeArgs := map[string]any{"path": "/isolated/PLAN.md", "content": "exact plan bytes"}
			guard := &planExactGrantGuard{grants: map[string]execution.ScopedGrant{}, calls: map[string]int{}}
			spec := execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"plan_probe", "write_file"}}
			for name, args := range map[string]map[string]any{"plan_probe": {}, "write_file": writeArgs} {
				if name == "write_file" && !approved {
					continue
				}
				raw, _ := json.Marshal(args)
				action := execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectUnknown}
				digest, _ := action.Digest()
				grant := execution.ScopedGrant{ID: uuid.New(), Identity: spec.Identity, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}
				spec.Grants = append(spec.Grants, grant)
				guard.grants[digest] = grant
			}
			var err error
			f.req.Policy, err = execution.NewRunPolicy(spec)
			if err != nil {
				t.Fatal(err)
			}
			var binding *chatRuntimeBinding
			probe, err := functiontool.New(functiontool.Config{Name: "plan_probe", Description: "Plan sub-dispatch fixture"}, func(ctx tool.Context, _ map[string]any) (map[string]any, error) {
				p := scopedChatPlan{binding: binding, invocation: ctx}
				return p.call("write_file", writeArgs)
			})
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			write := basicPlanWrite(t)
			client := &basicPlanNode{call: func(name string, args map[string]any) (json.RawMessage, error) {
				writes++
				return write(name, args)
			}}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(ctx, req, scope)
				if err != nil {
					return nil, err
				}
				safe.PolicyRuntime = agent.BotPolicyRuntime{Guard: guard, Classifier: BotCatalogueClassifier{}}
				safe.Catalog.Pool = &basicPlanPool{client: client}
				safe.Catalog.Groups = map[string]*agent.ToolGroup{"plan_fixture": {Name: "plan_fixture", Tools: []tool.Tool{probe}}}
				return safe, nil
			}
			r, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Cleanup()
			binding = r.Agent.ToolIndex.GetToolEntry("write_file").Tool.(*chatRuntimeTool).binding
			ctx := execution.WithBotRunIdentity(f.ctx, spec.Identity)
			ctx = execution.WithRunPolicy(ctx, f.req.Policy)
			ctx = agent.WithBotPolicyRuntime(ctx, *r.PolicyRuntime)
			invocation := basicToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: ctx, session: f.req.Run.SessionID.String(), functionCallID: "plan-probe"}}
			var bridge botRunnableTool
			for _, candidate := range r.Agent.Tools {
				if candidate.Name() == "execute_tool" {
					bridge = candidate.(botRunnableTool)
				}
			}
			_, err = bridge.Run(invocation, map[string]any{"name": "plan_probe", "arguments": map[string]any{}})
			if (err == nil) != approved {
				t.Fatalf("approved=%v err=%v", approved, err)
			}
			want := 0
			if approved {
				want = 1
			}
			if guard.calls["plan_probe"] != 1 || guard.calls["write_file"] != want || writes != want {
				t.Fatalf("authorizations=%v writes=%d", guard.calls, writes)
			}
			if approved {
				if _, err := (&scopedChatPlan{binding: binding, invocation: invocation}).call("write_file", writeArgs); err == nil || writes != 1 {
					t.Fatalf("replayed plan write: %v calls=%d", err, writes)
				}
			}
		})
	}
}
