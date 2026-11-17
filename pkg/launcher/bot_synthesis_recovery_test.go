package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type synthesisRecoveryModel struct {
	requests []*model.LLMRequest
	generate func(int, func(*model.LLMResponse, error) bool)
}

func (*synthesisRecoveryModel) Name() string { return "recovery" }
func (m *synthesisRecoveryModel) GenerateContent(_ context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, req)
		m.generate(len(m.requests), yield)
	}
}

func TestBotSynthesisOutputLimitRecovery(t *testing.T) {
	for _, name := range []string{"visible", "typed_visible", "typed_visible_trailing", "reasoning_only", "partial_max", "typed_empty", "missing_metadata", "partial_only", "twice", "trailing_error", "trailing_interrupted", "trailing_filtered", "max_then_stop", "bounded_draft"} {
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			meter, budgetErr := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
			if budgetErr != nil {
				t.Fatal(budgetErr)
			}
			f.req.Budget = meter
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			llm := got.Agent.LLM.(*botFactoryLLM)
			llm.binding.synthesisReserve.final = true
			raw := &synthesisRecoveryModel{}
			drained := 0
			raw.generate = func(call int, yield func(*model.LLMResponse, error) bool) {
				defer func() { drained++ }()
				if call == 2 {
					if drained != 1 {
						t.Fatal("correction dispatched before drain")
					}
					if name != "twice" {
						yield(completeSynthesisResponse(), nil)
						return
					}
				}
				r := &model.LLMResponse{Content: genai.NewContentFromText("retained visible draft", "model"), FinishReason: genai.FinishReasonMaxTokens}
				var firstErr error
				switch name {
				case "typed_visible", "typed_visible_trailing":
					firstErr = &provider.EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens, HasVisibleOutput: true}
				case "reasoning_only", "typed_empty":
					r.Content.Parts = []*genai.Part{{Text: "PRIVATE_REASONING", Thought: true}}
					if name == "typed_empty" {
						firstErr = emptySynthesisError()
					}
				case "partial_max":
					r.Partial = true
				case "missing_metadata", "partial_only":
					r.FinishReason = ""
					r.Partial = name == "partial_only"
				case "bounded_draft":
					r.Content.Parts = []*genai.Part{{Text: "PRIVATE_REASONING", Thought: true}, {Text: strings.Repeat("界", 12000)}}
				}
				yield(r, firstErr)
				switch name {
				case "trailing_error", "typed_visible_trailing":
					yield(nil, context.Canceled)
				case "trailing_interrupted":
					yield(&model.LLMResponse{Interrupted: true}, nil)
				case "trailing_filtered":
					yield(&model.LLMResponse{FinishReason: genai.FinishReasonSafety}, nil)
				case "max_then_stop":
					yield(completeSynthesisResponse(), nil)
				}
			}
			llm.LLM = raw
			query := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("original retained evidence", "user")}, Config: &genai.GenerateContentConfig{MaxOutputTokens: 128}}
			var finalCount int
			var failure error
			for response, err := range llm.GenerateContent(f.ctx, query, true) {
				failure = errors.Join(failure, err)
				if response != nil && response.Content != nil {
					finalCount++
					if response.Content.Parts[0].Text != completeSynthesisResponse().Content.Parts[0].Text {
						t.Fatal("incomplete output published")
					}
				}
			}
			terminal := name == "typed_visible_trailing" || name == "missing_metadata" || name == "partial_only" || strings.HasPrefix(name, "trailing_")
			wantCalls := 2
			if terminal {
				wantCalls = 1
			}
			if len(raw.requests) != wantCalls || drained != wantCalls {
				t.Fatalf("calls=%d drained=%d want=%d error=%v", len(raw.requests), drained, wantCalls, failure)
			}
			if terminal || name == "twice" {
				if failure == nil || finalCount != 0 {
					t.Fatalf("finals=%d failure=%v", finalCount, failure)
				}
				if !strings.Contains(failure.Error(), "reason=") {
					t.Fatalf("missing diagnostic evidence: %v", failure)
				}
				if strings.Contains(failure.Error(), "retained visible draft") || strings.Contains(failure.Error(), "PRIVATE_REASONING") {
					t.Fatal("payload in diagnostic")
				}
			} else if failure != nil || finalCount != 1 {
				t.Fatalf("finals=%d failure=%v", finalCount, failure)
			}
			if wantCalls == 2 {
				retry := raw.requests[1]
				if retry.Config.MaxOutputTokens != 128 || len(retry.Config.Tools) != 0 {
					t.Fatal("changed ceiling or tools")
				}
				correction := retry.Contents[len(retry.Contents)-1].Parts[0].Text
				if strings.Contains(correction, "PRIVATE_REASONING") || len(correction) > 9500 {
					t.Fatal("unbounded or private draft")
				}
				if !strings.Contains(correction, "complete") || !strings.Contains(correction, "concise") {
					t.Fatal("missing complete concise rewrite instruction")
				}
				if name == "visible" && !strings.Contains(correction, "retained visible draft") {
					t.Fatal("visible draft lost")
				}
			}
			if len(query.Contents) != 1 || query.Config.MaxOutputTokens != 128 {
				t.Fatal("caller request mutated")
			}
		})
	}
}

func TestBotSynthesisRecoveryEvidencePrivacy(t *testing.T) {
	var draft strings.Builder
	botSynthesisAppendDraft(&draft, strings.Repeat("界", 12000))
	if draft.Len() > botSynthesisDraftBytes || !utf8.ValidString(draft.String()) {
		t.Fatal("invalid bounded draft")
	}
	botSynthesisAppendDraft(&draft, "remaining")
	if draft.Len() > botSynthesisDraftBytes {
		t.Fatal("draft grew beyond limit")
	}
	var decoded string
	if err := json.Unmarshal([]byte(botSynthesisQuoteDraft(draft.String())), &decoded); err != nil || decoded != draft.String() {
		t.Fatal("draft quote failed")
	}

	limit := &provider.EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens, HasVisibleOutput: true}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"typed", limit, true}, {"wrapped", fmt.Errorf("wrapped: %w", limit), true},
		{"joined", errors.Join(limit, context.Canceled), false},
		{"interrupted", llmerror.NewInterruptedStream(limit), false},
		{"refused", &provider.EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens, Refused: true}, false},
		{"tools", &provider.EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens, HasToolCalls: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := botSynthesisOutputLimitError(tc.err); got != tc.want {
				t.Fatalf("recoverable=%t want=%t", got, tc.want)
			}
		})
	}

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
	llm := got.Agent.LLM.(*botFactoryLLM)
	llm.binding.synthesisReserve.final = true
	llm.LLM = &synthesisRecoveryModel{generate: func(_ int, yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("SECRET_TEXT", "model"), ErrorCode: "SECRET_CODE", FinishReason: "SECRET_FINISH"}, errors.New("SECRET_ERROR"))
	}}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	for response, err := range llm.GenerateContent(f.ctx, &model.LLMRequest{}, false) {
		if response != nil || err == nil {
			t.Fatal("unsafe result escaped")
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("error payload leaked")
		}
		if !errors.Is(err, errBotSynthesisIncomplete) {
			t.Fatal("lost sentinel")
		}
	}
	if strings.Contains(logs.String(), "SECRET") || !strings.Contains(logs.String(), "finish_other=1") || !strings.Contains(logs.String(), "error_code=1") {
		t.Fatalf("unsafe or missing diagnostic: %s", logs.String())
	}
}

// Keep independent typed-empty and transport errors distinguishable: a joined
// cancellation is never permission to repeat a metered generation.
func TestBotSynthesisRecoveryTrailingTypedFailure(t *testing.T) {
	f := newBotFactoryFixture(t)
	meter, budgetErr := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
	if budgetErr != nil {
		t.Fatal(budgetErr)
	}
	f.req.Budget = meter
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	llm := got.Agent.LLM.(*botFactoryLLM)
	llm.binding.synthesisReserve.final = true
	raw := &synthesisRecoveryModel{generate: func(_ int, yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{FinishReason: genai.FinishReasonMaxTokens}, &provider.EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens})
		yield(nil, context.Canceled)
	}}
	llm.LLM = raw
	var failure error
	for response, err := range llm.GenerateContent(f.ctx, &model.LLMRequest{}, false) {
		if response != nil {
			t.Fatal("failed response escaped")
		}
		failure = errors.Join(failure, err)
	}
	if len(raw.requests) != 1 || !errors.Is(failure, context.Canceled) {
		t.Fatalf("requests=%d failure=%v", len(raw.requests), failure)
	}
}
