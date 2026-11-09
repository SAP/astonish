package launcher

import (
	"context"
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
	if result.Status != store.RunSucceeded || len(result.Report.Unverified) != 0 {
		t.Fatalf("successful scoped discovery downgraded: status=%s report=%+v", result.Status, result.Report)
	}
	// The runner counts all three responses; only the target spends action quota.
	if llm.calls != 4 || *calls != 1 || result.Usage.ToolCalls != 3 || meter.Usage().ToolCalls != 1 {
		t.Fatalf("duplicate execution or metadata charged as action: rounds=%d calls=%d usage=%+v", llm.calls, *calls, result.Usage)
	}
	if len(result.Report.Changed) != 0 || len(result.Report.Artifacts) != 0 || result.Report.NextRunAt != nil || len(result.Report.Unchanged) != 3 {
		t.Fatalf("metadata fabricated effects or lost target observation: %+v", result.Report)
	}
	for i, name := range []string{"search_tools", "describe_tools"} {
		e := result.Report.Unchanged[i]
		if !bot.ValidObservation(e) || !strings.Contains(e.Source, name+"/discovery-") || !strings.Contains(e.Summary, "metadata") {
			t.Fatalf("missing exact-call metadata observation: %+v", e)
		}
	}
	if result.Report.Unchanged[2].Source != "fixture://permitted-action" {
		t.Fatalf("target ledger replaced: %+v", result.Report)
	}
}
