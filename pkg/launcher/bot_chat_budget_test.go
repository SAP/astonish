package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotChatFactorySharedAuxiliaryBudget(t *testing.T) {
	f := newBotFactoryFixture(t)
	limit := int64(36)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	f.cfg.WrapModel = func(_ context.Context, llm model.LLM, b execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(llm, b, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 2, nil }})
	}
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	request := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("observe", "user")}}
	for _, llm := range []model.LLM{resources.Agent.LLM, resources.Agent.SubAgentManager.LLM} {
		for _, err := range llm.GenerateContent(f.ctx, request, false) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// An ambient override must not replace the metered owner model during compaction.
	viewerModel := &botFactoryTestModel{}
	if _, err := resources.Agent.Compactor.LLM(agent.WithLLM(f.ctx, viewerModel), "compact"); err != nil {
		t.Fatal(err)
	}
	if viewerModel.calls != 0 || f.resolver.model.calls != 3 {
		t.Fatalf("calls owner=%d viewer=%d", f.resolver.model.calls, viewerModel.calls)
	}
	usage := meter.Usage()
	if usage.InputTokens != 6 || usage.OutputTokens != 30 || usage.CostMicros != nil {
		t.Fatalf("combined usage=%+v", usage)
	}
	if _, err := resources.Agent.SubAgentManager.Compactor.LLM(f.ctx, "blocked auxiliary"); !errors.Is(err, bot.ErrBudgetExceeded) {
		t.Fatalf("budget not shared: %v", err)
	}
	if f.resolver.model.calls != 3 {
		t.Fatal("exhausted child compaction reached provider")
	}
	if request.Config != nil {
		t.Fatal("shared request mutated by per-run decorator")
	}
}

func TestBotChatFactoryUnknownCostFailsBeforeAuxiliary(t *testing.T) {
	f := newBotFactoryFixture(t)
	cost := int64(10)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxModelCostMicros: &cost}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	f.cfg.WrapModel = func(_ context.Context, llm model.LLM, b execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(llm, b, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true})
	}
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	if _, err := resources.Agent.Compactor.LLM(f.ctx, "no known price"); !errors.Is(err, bot.ErrBudgetUnknownCost) {
		t.Fatal(err)
	}
	if f.resolver.model.calls != 0 {
		t.Fatal("unknown-price provider used under hard cap")
	}
}
