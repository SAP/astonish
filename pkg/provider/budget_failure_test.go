package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/model"
)

func TestBudgetLLMTypedDenial(t *testing.T) {
	for _, stream := range []bool{false, true} {
		b := meter(t, 9)
		m := &budgetTestLLM{}
		responses, err := consume(wrap(t, m, b, budgetOptions()), context.Background(), &model.LLMRequest{}, stream)
		var failure *bot.BudgetLimitError
		if !errors.As(err, &failure) || !errors.Is(err, bot.ErrBudgetExceeded) || failure.Limit != 9 || failure.Used != 0 || *failure.InputEstimate != 10 || *failure.Requested != 110 {
			t.Fatalf("lost evidence %v", err)
		}
		if m.calls.Load() != 0 || len(responses) != 0 || b.Usage().InputTokens != 0 {
			t.Fatal("denial generated response/usage")
		}
	}
}
