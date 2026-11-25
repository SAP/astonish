package provider_test

import (
	"context"
	"github.com/SAP/astonish/pkg/provider"
	"google.golang.org/adk/model"
	"iter"
	"testing"
)

func TestBudgetLLMTransferredPreflightLifetime(t *testing.T) {
	for _, mode := range []string{"success", "nil", "budget"} {
		t.Run(mode, func(t *testing.T) {
			closed := false
			ctx := provider.WithModelPreflightEnd(context.Background(), func() { closed = true })
			opts := budgetOptions()
			opts.EstimateInput = func(context.Context, *model.LLMRequest) (int64, error) {
				if closed {
					t.Fatal("closed before estimation")
				}
				return 10, nil
			}
			tokens := int64(1000)
			if mode == "budget" {
				tokens = 1
			}
			inner := &budgetTestLLM{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
				if !closed {
					t.Fatal("scope survived provider creation")
				}
				return func(y func(*model.LLMResponse, error) bool) { y(usageResponse(10, 1), nil) }
			}}
			llm := wrap(t, inner, meter(t, tokens), opts)
			req := &model.LLMRequest{}
			if mode == "nil" {
				req = nil
			}
			for range llm.GenerateContent(ctx, req, false) {
				if !closed {
					t.Fatal("scope survived consumer yield")
				}
			}
			if !closed {
				t.Fatal("scope not closed")
			}
		})
	}
}
