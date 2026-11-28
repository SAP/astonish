package launcher

import (
	"context"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

func TestPrivateWorkerModeFromOriginalRequest(t *testing.T) {
	for _, planning := range []bool{false, true} {
		f := newBotFactoryFixture(t)
		f.req.PlanMode = planning
		b := &botFactoryBinding{owner: f.ctx, req: f.req, workerOnly: true}
		invocation := agent.WithPromptOverrides(context.Background(), &agent.PromptOverrides{PlanMode: !planning})
		bound := b.bind(invocation)
		po := agent.PromptOverridesFromContext(bound)
		if po == nil || po.PlanMode != planning {
			t.Fatalf("original PlanMode=%v, rebound=%+v", planning, po)
		}
		rebound := b.bind(bound)
		if agent.PromptOverridesFromContext(rebound).PlanMode != planning {
			t.Fatal("resume rebind lost original mode")
		}
	}
}

// The captured owner includes the trusted scoped Inputs envelope. A later viewer
// cannot replace its skills, tools, execution flags or the durable worker mode.
func TestPrivateWorkerSharedBindingPreservesTrustedOverrides(t *testing.T) {
	for _, planning := range []bool{false, true} {
		f := newBotFactoryFixture(t)
		f.req.PlanMode = planning
		base := &agent.PromptOverrides{
			SkillIndex: "owner skill index", SessionContext: "owner A2A context",
			PinnedToolGroups: []string{"a2a", "owner-mcp"}, AdditionalTools: []tool.Tool{nil},
			PlanMode: !planning, ApprovedPlanExecution: true,
			ApprovedPlanCompleted: true, ApprovedPlanExecutionExplicit: true,
		}
		b := &botFactoryBinding{owner: agent.WithPromptOverrides(store.WithServices(f.ctx, f.services), base), req: f.req, workerOnly: true, verifyIsolation: func(context.Context) error { return nil }}
		shared := &chatRuntimeBinding{hooks: ChatRuntimeHooks{Context: botSharedBinding{b}}}
		viewer := agent.WithPromptOverrides(context.Background(), &agent.PromptOverrides{
			PlanMode: !planning, AskMode: true, GraphPlanMode: true,
			SkillIndex: "viewer skill index", PinnedToolGroups: []string{"viewer"},
		})
		for attempt := 0; attempt < 2; attempt++ {
			bound, release, err := shared.bind(viewer)
			if err != nil {
				t.Fatal(err)
			}
			po := agent.PromptOverridesFromContext(bound)
			if po == nil {
				t.Fatal("missing overrides")
			}
			want := *base
			want.PlanMode = planning
			want.Bot = po.Bot
			want.SchedulerHints = ""
			if po.Bot == nil || !reflect.DeepEqual(*po, want) {
				t.Fatalf("shared bind dropped trusted envelope or accepted viewer values: got=%+v want=%+v", po, want)
			}
			if base.Bot != nil || base.PlanMode != !planning {
				t.Fatal("trusted base mutated")
			}
			viewer = bound
			release()
		}
	}
}
