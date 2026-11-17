package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise the production factory composition, rather than replacing BudgetLLM
// with the raw model: recovery must see evidence after a typed recoverable error.
func TestBotSynthesisTrailingBudgetFailure(t *testing.T) {
	for _, finish := range []genai.FinishReason{genai.FinishReasonStop, genai.FinishReasonMaxTokens} {
		for _, trailing := range []string{"cancellation", "provider_error", "interrupted", "error_code", "unsafe_finish"} {
			t.Run(string(finish)+"/"+trailing, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				f.req.Budget = meter
				var trailingErr error
				var trailingResponse *model.LLMResponse
				switch trailing {
				case "cancellation":
					// The provider can report cancellation independently of the caller context.
					trailingErr = context.Canceled
				case "provider_error":
					trailingErr = errors.New("trailing provider failure")
				case "interrupted":
					trailingResponse = &model.LLMResponse{Interrupted: true}
				case "error_code":
					trailingResponse = &model.LLMResponse{ErrorCode: "unsafe"}
				case "unsafe_finish":
					trailingResponse = &model.LLMResponse{FinishReason: genai.FinishReasonSafety}
				}
				drained := false
				raw := &synthesisEmptyModel{generate: func(call int, yield func(*model.LLMResponse, error) bool) {
					if call > 1 {
						yield(completeSynthesisResponse(), nil)
						return
					}
					if !yield(&model.LLMResponse{FinishReason: finish}, &provider.EmptyCompletionError{Provider: "controlled", FinishReason: finish}) {
						return
					}
					if !yield(trailingResponse, trailingErr) {
						return
					}
					drained = true
				}}
				f.cfg.WrapModel = func(_ context.Context, _ model.LLM, budget execution.RunBudget) (model.LLM, error) {
					return provider.NewBudgetLLM(raw, budget, provider.BudgetLLMOptions{
						MaxOutputTokens: 128, SupportsOutputLimit: true,
						EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 10, nil },
					})
				}
				got, err := f.construct(t)
				if err != nil {
					t.Fatal(err)
				}
				defer got.Cleanup()
				llm := got.Agent.LLM.(*botFactoryLLM)
				llm.binding.synthesisReserve.final = true
				var failure error
				failures, published := 0, 0
				for response, err := range llm.GenerateContent(f.ctx, &model.LLMRequest{}, true) {
					if err != nil {
						failures++
						failure = errors.Join(failure, err)
					}
					if response != nil {
						published++
					}
				}
				if raw.calls != 1 || !drained || failures != 1 || published != 0 {
					t.Fatalf("calls=%d drained=%t failures=%d published=%d; unsafe attempt must not correct or publish", raw.calls, drained, failures, published)
				}
				if trailingErr != nil && !errors.Is(failure, trailingErr) {
					t.Fatalf("trailing failure lost: %v", failure)
				}
				if u := meter.Usage(); u.InputTokens != 10 || u.OutputTokens != 128 || u.ToolCalls != 0 {
					t.Fatalf("failed attempt reservation not retained: %+v", u)
				}
			})
		}
	}
}
