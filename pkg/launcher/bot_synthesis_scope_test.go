package launcher

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func TestBotEmptyCompletionOutsideSynthesisDoesNotRetry(t *testing.T) {
	for _, name := range []string{"research", "assessment", "progress"} {
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = meter
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			llm := botFactoryModelForTest(t, got.Agent.LLM)
			ctx := context.Context(f.ctx)
			switch name {
			case "assessment":
				ctx = bot.WithModelCallPurpose(ctx, bot.ModelCallPurposeAssessment)
				llm.binding.synthesisReserve.final = true
			case "progress":
				ctx = bot.WithModelCallPurpose(ctx, bot.ModelCallPurposeProgress)
				llm.binding.synthesisReserve.final = true
			}
			raw := &synthesisEmptyModel{generate: func(_ int, yield func(*model.LLMResponse, error) bool) { yield(nil, emptySynthesisError()) }}
			llm.LLM = raw
			failures := 0
			for _, err := range llm.GenerateContent(ctx, &model.LLMRequest{}, false) {
				if err != nil {
					failures++
				}
			}
			if raw.calls != 1 || failures != 1 {
				t.Fatalf("calls=%d failures=%d", raw.calls, failures)
			}
		})
	}
}
