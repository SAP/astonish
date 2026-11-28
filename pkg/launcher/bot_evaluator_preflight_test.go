package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
	"testing"
)

func TestBotPreflightEvaluatorScopeTransfer(t *testing.T) {
	f := newBotFactoryFixture(t)
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, declarations: &botDeclarationReads{}, verifyIsolation: func(context.Context) error { return nil }}
	caller, end := entstore.WithBotContentReadOperation(f.ctx)
	closed := false
	caller = provider.WithModelPreflightEnd(caller, func() { end(); closed = true })
	checked := false
	budget, err := provider.NewBudgetLLM(&declarationBoundaryModel{t: t}, f.req.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(ctx context.Context, _ *model.LLMRequest) (int64, error) {
		checked = true
		if closed || entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
			t.Fatal("evaluator scope closed before budget preflight")
		}
		return 1, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &botFactoryLLM{LLM: &botProgressLLM{LLM: budget, binding: b}, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(caller, &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
		if !closed {
			t.Fatal("caller scope survived consumer yield")
		}
	}
	if !checked || !closed {
		t.Fatal("missing preflight or cleanup")
	}
}
