package provider_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"google.golang.org/adk/model"
)

type preflightTestKey struct{}
type preflightTestBudget struct {
	*bot.SharedRunBudget
	t                        *testing.T
	active                   bool
	begins, checks, reserves int
	fail                     bool
}

func (b *preflightTestBudget) BeginModelPreflight(ctx context.Context) (context.Context, func()) {
	b.begins++
	if b.active {
		b.t.Fatal("overlapping preflight")
	}
	b.active = true
	return context.WithValue(ctx, preflightTestKey{}, b.begins), func() { b.active = false }
}
func (b *preflightTestBudget) Check(ctx context.Context) error {
	b.checks++
	if !b.active || ctx.Value(preflightTestKey{}) != b.begins {
		b.t.Fatal("check outside preflight")
	}
	if b.fail {
		return errors.New("revoked")
	}
	return b.SharedRunBudget.Check(ctx)
}
func (b *preflightTestBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	b.reserves++
	if !b.active || ctx.Value(preflightTestKey{}) != b.begins {
		b.t.Fatal("reserve outside preflight")
	}
	return b.SharedRunBudget.ReserveModel(ctx, r)
}
func TestBudgetLLMPreflightEndsBeforeProviderAndConsumer(t *testing.T) {
	for _, stream := range []bool{false, true} {
		b := &preflightTestBudget{SharedRunBudget: meter(t, 1000), t: t}
		m := &budgetTestLLM{generate: func(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
			if b.active || ctx.Value(preflightTestKey{}) != nil {
				t.Fatal("scope reached provider")
			}
			return func(yield func(*model.LLMResponse, error) bool) { yield(usageResponse(10, 5), nil) }
		}}
		llm, err := provider.NewBudgetLLM(m, b, budgetOptions())
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			b.fail = attempt == 1
			for _, err := range llm.GenerateContent(t.Context(), &model.LLMRequest{}, stream) {
				if b.active {
					t.Fatal("scope survived consumer yield")
				}
				if (err != nil) != b.fail {
					t.Fatalf("fresh revocation check: %v", err)
				}
			}
		}
		if b.begins != 2 || b.checks != 2 || b.reserves != 1 || m.calls.Load() != 1 {
			t.Fatalf("preflight counts: %+v", b)
		}
	}
}
