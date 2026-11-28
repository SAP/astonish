package provider_test

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"iter"
	"testing"
	"time"
)

func TestBotProgressBudgetModelOperationTimeout(t *testing.T) {
	p := execution.NewBotProgressRuntimePolicy()
	p.GenerationHealth = nil // Explicit historical elapsed-time policy.
	p.ModelCallTimeoutSeconds = 1
	cap := int64(110)
	b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300, RuntimePolicy: p, MaxTokens: &cap}, time.Now().Add(-6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	m := &budgetTestLLM{generate: func(c context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
		if d, ok := c.Deadline(); !ok || time.Until(d) > time.Second {
			t.Error("operation timeout missing")
		}
		return func(yield func(*model.LLMResponse, error) bool) { <-c.Done(); yield(nil, c.Err()) }
	}}
	wrapped := wrap(t, m, b, budgetOptions())
	if _, err := consume(wrapped, context.Background(), &model.LLMRequest{}, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if _, err := consume(wrapped, context.Background(), &model.LLMRequest{}, false); !errors.Is(err, bot.ErrBudgetExceeded) {
		t.Fatalf("cumulative quota: %v", err)
	}
	if m.calls.Load() != 1 {
		t.Fatal("second provider call bypassed budget")
	}
}
