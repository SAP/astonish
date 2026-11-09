package provider_test

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type budgetTestLLM struct {
	calls    atomic.Int64
	generate func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error]
}

func (*budgetTestLLM) Name() string { return "budget-test" }
func (m *budgetTestLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.calls.Add(1)
	return m.generate(ctx, req, stream)
}
func meter(t *testing.T, tokens int64) *bot.SharedRunBudget {
	t.Helper()
	b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &tokens}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func budgetOptions() provider.BudgetLLMOptions {
	return provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 10, nil }}
}
func wrap(t *testing.T, m model.LLM, b *bot.SharedRunBudget, opts provider.BudgetLLMOptions) *provider.BudgetLLM {
	t.Helper()
	llm, err := provider.NewBudgetLLM(m, b, opts)
	if err != nil {
		t.Fatal(err)
	}
	return llm
}
func consume(llm model.LLM, ctx context.Context, req *model.LLMRequest, stream bool) ([]*model.LLMResponse, error) {
	var responses []*model.LLMResponse
	var result error
	for r, err := range llm.GenerateContent(ctx, req, stream) {
		if r != nil {
			responses = append(responses, r)
		}
		result = errors.Join(result, err)
	}
	return responses, result
}
func usageResponse(in, out int32) *model.LLMResponse {
	return &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: in, CandidatesTokenCount: out}}
}

func TestBudgetLLMReserveClampPreservesRequestAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "single"}[stream], func(t *testing.T) {
			b := meter(t, 40)
			req := &model.LLMRequest{Model: "effective", Config: &genai.GenerateContentConfig{MaxOutputTokens: 100, SystemInstruction: genai.NewContentFromText("instructions", genai.RoleUser)}, Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}, Tools: map[string]any{"tool": "value"}}
			final := usageResponse(10, 5)
			final.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "x"}}}}}
			final.TurnComplete = true
			m := &budgetTestLLM{generate: func(_ context.Context, got *model.LLMRequest, gotStream bool) iter.Seq2[*model.LLMResponse, error] {
				if got == req || got.Config == req.Config || got.Config.MaxOutputTokens != 30 || got.Config.SystemInstruction != req.Config.SystemInstruction || got.Model != req.Model || got.Tools["tool"] != "value" || gotStream != stream {
					t.Errorf("request not preserved/clamped: %+v", got)
				}
				if u := b.Usage(); u.InputTokens != 10 || u.OutputTokens != 30 {
					t.Errorf("provider invoked before reservation: %+v", u)
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					if stream {
						if !yield(usageResponse(10, 2), nil) {
							return
						}
					}
					if !yield(final, nil) {
						return
					}
					if stream {
						yield(final, nil)
					}
				}
			}}
			llm := wrap(t, m, b, budgetOptions())
			responses, err := consume(llm, context.Background(), req, stream)
			if err != nil {
				t.Fatal(err)
			}
			if len(responses) == 0 || responses[len(responses)-1] != final || req.Config.MaxOutputTokens != 100 || llm.Name() != m.Name() {
				t.Fatal("response/request changed")
			}
			u := b.Usage()
			if u.InputTokens != 10 || u.OutputTokens != 5 || u.CostMicros != nil {
				t.Fatalf("cumulative usage=%+v", u)
			}
		})
	}
}

func TestBudgetLLMRetryMissingUsageAndChildReuse(t *testing.T) {
	b := meter(t, 220)
	failure := errors.New("retryable provider failure")
	m := &budgetTestLLM{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, failure) }
	}}
	parent := wrap(t, m, b, budgetOptions())
	child := wrap(t, m, b, budgetOptions())
	for _, llm := range []model.LLM{parent, child} {
		if _, err := consume(llm, context.Background(), &model.LLMRequest{}, false); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	if _, err := consume(child, context.Background(), &model.LLMRequest{}, false); !errors.Is(err, bot.ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if m.calls.Load() != 2 {
		t.Fatalf("provider calls=%d", m.calls.Load())
	}
	if u := b.Usage(); u.InputTokens != 20 || u.OutputTokens != 200 || u.CostMicros != nil {
		t.Fatalf("missing usage refunded: %+v", u)
	}
}

func TestBudgetLLMCancellationAndConsumerStopSettle(t *testing.T) {
	for _, mode := range []string{"before", "during", "consumer", "missing", "zero", "partial"} {
		t.Run(mode, func(t *testing.T) {
			b := meter(t, 1000)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := &budgetTestLLM{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					switch mode {
					case "during":
						cancel()
						yield(nil, ctx.Err())
					case "consumer":
						yield(usageResponse(10, 2), nil)
					case "missing":
						yield(&model.LLMResponse{}, nil)
					case "partial":
						r := usageResponse(10, 2)
						r.Partial = true
						yield(r, nil)
					case "zero":
						yield(usageResponse(0, 0), nil)
					}
				}
			}}
			llm := wrap(t, m, b, budgetOptions())
			if mode == "before" {
				cancel()
			}
			if mode == "consumer" {
				for range llm.GenerateContent(ctx, &model.LLMRequest{}, true) {
					break
				}
			} else {
				_, err := consume(llm, ctx, &model.LLMRequest{}, true)
				if (mode == "before" || mode == "during") && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			u := b.Usage()
			if mode == "before" {
				if m.calls.Load() != 0 || u.InputTokens != 0 {
					t.Fatal("cancelled request reached provider")
				}
			} else if u.InputTokens != 10 || u.OutputTokens != 100 || u.CostMicros != nil {
				t.Fatalf("unproven charge refunded: %+v", u)
			}
		})
	}
}

func TestBudgetLLMUnsupportedClampAndUnknownCost(t *testing.T) {
	b := meter(t, 40)
	m := &budgetTestLLM{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) { yield(usageResponse(10, 1), nil) }
	}}
	opts := budgetOptions()
	opts.SupportsOutputLimit = false
	if _, err := consume(wrap(t, m, b, opts), context.Background(), &model.LLMRequest{}, false); err == nil {
		t.Fatal("unsupported clamp allowed")
	}
	if m.calls.Load() != 0 || b.Usage().InputTokens != 0 {
		t.Fatal("unsupported clamp invoked provider or charged")
	}
	costLimit := int64(1000)
	b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxModelCostMicros: &costLimit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consume(wrap(t, m, b, budgetOptions()), context.Background(), &model.LLMRequest{}, false); !errors.Is(err, bot.ErrBudgetUnknownCost) {
		t.Fatal(err)
	}
	if m.calls.Load() != 0 {
		t.Fatal("unknown price invoked provider")
	}
	opts = budgetOptions()
	opts.Price = func(in, out int64) (*int64, error) { n := in + out; return &n, nil }
	if _, err := consume(wrap(t, m, b, opts), context.Background(), &model.LLMRequest{}, false); err != nil {
		t.Fatal(err)
	}
	if u := b.Usage(); u.CostMicros == nil || *u.CostMicros != 11 {
		t.Fatalf("priced usage=%+v", u)
	}
}

func TestBudgetLLMConcurrentIteratorReuse(t *testing.T) {
	b := meter(t, 2200)
	m := &budgetTestLLM{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) { yield(usageResponse(10, 5), nil) }
	}}
	llm := wrap(t, m, b, budgetOptions())
	seq := llm.GenerateContent(context.Background(), &model.LLMRequest{}, true)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for _, err := range seq {
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if u := b.Usage(); u.InputTokens != 200 || u.OutputTokens != 100 {
		t.Fatalf("usage=%+v", u)
	}
}

func TestBudgetLLMDefaultEstimatorAndValidation(t *testing.T) {
	if _, err := provider.NewBudgetLLM(nil, nil, provider.BudgetLLMOptions{}); err == nil {
		t.Fatal("nil dependencies accepted")
	}
	b := meter(t, 10000)
	m := &budgetTestLLM{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) { yield(usageResponse(20, 2), nil) }
	}}
	opts := budgetOptions()
	opts.EstimateInput = nil
	llm := wrap(t, m, b, opts)
	for _, req := range []*model.LLMRequest{nil, {Config: &genai.GenerateContentConfig{CachedContent: "remote"}}, {Contents: []*genai.Content{{Parts: []*genai.Part{{InlineData: &genai.Blob{Data: []byte("image")}}}}}}} {
		if _, err := consume(llm, context.Background(), req, false); err == nil {
			t.Fatal("unbounded input accepted")
		}
	}
	if m.calls.Load() != 0 {
		t.Fatal("invalid request invoked provider")
	}
	if _, err := consume(llm, context.Background(), &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}}, false); err != nil {
		t.Fatal(err)
	}
}
