package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// The live first schedule call reserved 80920 and settled 32824+194.
// Its rejected follow-up was not captured: this is a synthetic append-only
// full-history reproduction of that shape, not a replay of its exact prompt.
func TestBudgetLLMObservedFullHistoryContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "stream"}[stream], func(t *testing.T) {
			b := meter(t, 100000)
			req := &model.LLMRequest{Model: "effective", Contents: []*genai.Content{genai.NewContentFromText("x", genai.RoleUser)}}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			req.Contents[0].Parts[0].Text = strings.Repeat("x", 80920-4096-64-len(data)+1)
			call := 0
			m := &budgetTestLLM{generate: func(_ context.Context, got *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
				call++
				u := b.Usage()
				if call == 1 && u.InputTokens+u.OutputTokens != 80920 {
					t.Fatalf("first hold=%+v", u)
				}
				if got.Config.MaxOutputTokens <= 0 || got.Config.MaxOutputTokens > 4096 {
					t.Fatalf("unbounded output: %d", got.Config.MaxOutputTokens)
				}
				if mTotal := u.InputTokens + u.OutputTokens; mTotal > 100000 {
					t.Fatalf("reservation exceeded finite cap: %d", mTotal)
				}
				return func(yield func(*model.LLMResponse, error) bool) { yield(usageResponse(32824, 194), nil) }
			}}
			opts := budgetOptions()
			opts.EstimateInput = nil
			opts.MaxOutputTokens = 4096
			llm := wrap(t, m, b, opts)
			if _, err := consume(llm, context.Background(), req, stream); err != nil {
				t.Fatal(err)
			}
			if u := b.Usage(); u.InputTokens+u.OutputTokens != 33018 {
				t.Fatalf("first settlement=%+v", u)
			}
			req.Contents = append(req.Contents, &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "search_tools", Args: map[string]any{"query": "ask user a clarifying question with choices"}}}}}, &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "search_tools", Response: map[string]any{"error": "tool outside policy"}}}}})
			if _, err := consume(llm, context.Background(), req, stream); err != nil {
				t.Fatalf("full-history continuation falsely exhausted after 33018 settled tokens: %v", err)
			}
			if m.calls.Load() != 2 {
				t.Fatalf("calls=%d", m.calls.Load())
			}
			if _, err := consume(llm, context.Background(), req, stream); err != nil {
				t.Fatalf("remaining finite allowance should fit third call: %v", err)
			}
			if _, err := consume(llm, context.Background(), req, stream); !errors.Is(err, bot.ErrBudgetExceeded) {
				t.Fatalf("finite cap must still reject fourth call: %v", err)
			}
			if m.calls.Load() != 3 {
				t.Fatalf("rejected call reached provider: %d", m.calls.Load())
			}
		})
	}
}
