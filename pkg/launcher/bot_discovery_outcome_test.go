package launcher

import (
	"context"
	"encoding/json"

	"github.com/SAP/astonish/pkg/question"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func TestBotDiscoveryOutcomeThroughFactoryRunner(t *testing.T) {
	f, _, calls := discoveryMandateFixture(t)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	llm := &discoveryContractModel{t: t, finishAfterAction: true}
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunWaitingApproval || len(result.Report.NeedsApproval) != 1 {
		t.Fatalf("successful scoped discovery downgraded: status=%s report=%+v", result.Status, result.Report)
	}
	// The dispatch is counted by the runner; only clarification spends action quota.
	if llm.calls != 3 || *calls != 1 || result.Usage.ToolCalls != 4 || meter.Usage().ToolCalls != 1 {
		t.Fatalf("duplicate execution or metadata charged as action: rounds=%d calls=%d usage=%+v", llm.calls, *calls, result.Usage)
	}
	observations := requireEmployeeDispatchObservation(t, result.Report.Unchanged)
	if len(result.Report.Changed) != 0 || len(result.Report.Artifacts) != 0 || result.Report.NextRunAt != nil || len(observations) != 2 {
		t.Fatalf("metadata fabricated effects or lost target observation: %+v", result.Report)
	}
	for i, name := range []string{"search_tools", "describe_tools"} {
		e := observations[i]
		if !bot.ValidObservation(e) || !strings.Contains(e.Source, name+"/discovery-") || !strings.Contains(e.Summary, "metadata") {
			t.Fatalf("missing exact-call metadata observation: %+v", e)
		}
	}
	for _, unverified := range result.Report.Unverified {
		if strings.Contains(unverified, "search_tools") || strings.Contains(unverified, "describe_tools") {
			t.Fatalf("verified discovery classified as uncertain: %s", unverified)
		}
	}
	repo := f.services.PersonalBots.(*discoveryQuestionStore)
	if repo.saved == nil || repo.saved.ID != result.Report.NeedsApproval[0] || repo.saved.Interaction == nil || repo.saved.Interaction.Validate() != nil {
		t.Fatalf("real question checkpoint missing: %+v", repo.saved)
	}
	var q question.Question
	if err := json.Unmarshal(repo.saved.Interaction.Payload, &q); err != nil || q.Prompt != "Which forecast should I use?" {
		t.Fatalf("question payload: %+v, %v", q, err)
	}
}
