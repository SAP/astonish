package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

func TestBotPreflightSharesDeclarationThroughFactory(t *testing.T) {
	f := newBotFactoryFixture(t)
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, declarations: &botDeclarationReads{}}
	request := &model.LLMRequest{}
	declared := b.declarations.bind(b, request)
	b.verifyIsolation = func(context.Context) error {
		if entstore.WithBotContentReadOperationFromContext(context.Background(), declared.owner) == context.Background() {
			t.Fatal("declaration scope closed before factory preflight")
		}
		return nil
	}
	llm := &botFactoryLLM{LLM: &declarationBoundaryModel{t: t}, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(f.ctx, request, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBotPreflightPromptDeclarationsBudgetLifetime(t *testing.T) {
	f := newBotFactoryFixture(t)
	b := &botFactoryBinding{owner: store.WithServices(f.ctx, f.services), req: f.req, declarations: &botDeclarationReads{}, verifyIsolation: func(context.Context) error { return nil }}
	prompt := b.declarations.begin(b.owner)
	request := &model.LLMRequest{}
	declared := b.declarations.bind(b, request)
	if prompt != declared.owner {
		t.Fatal("prompt scope not borrowed by declarations")
	}
	checked := false
	budget, err := provider.NewBudgetLLM(&declarationBoundaryModel{t: t}, f.req.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(ctx context.Context, _ *model.LLMRequest) (int64, error) {
		checked = true
		if entstore.WithBotContentReadOperationFromContext(context.Background(), prompt) == context.Background() || entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
			t.Fatal("scope not shared through budget")
		}
		return 1, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &botFactoryLLM{LLM: &botProgressLLM{LLM: budget, binding: b}, binding: b, researchOnly: true}
	for _, err := range llm.GenerateContent(f.ctx, request, false) {
		if err != nil {
			t.Fatal(err)
		}
		if entstore.WithBotContentReadOperationFromContext(context.Background(), prompt) != context.Background() {
			t.Fatal("scope survived yield")
		}
	}
	if !checked {
		t.Fatal("budget not reached")
	}
}
