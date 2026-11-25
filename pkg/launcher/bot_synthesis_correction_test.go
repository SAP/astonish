package launcher

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This controlled model settles each attempted response on the same run meter,
// including the rejected tool response, before exposing it to the factory.
type synthesisCorrectionModel struct {
	t           *testing.T
	meter       *bot.SharedRunBudget
	requests    []*model.LLMRequest
	streams     []bool
	ignoreTwice bool
}

func (*synthesisCorrectionModel) Name() string { return "synthesis-correction" }
func (m *synthesisCorrectionModel) GenerateContent(ctx context.Context, q *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, q)
		m.streams = append(m.streams, stream)
		r, err := m.meter.ReserveModel(ctx, execution.BudgetReservation{InputTokens: 10, OutputTokens: 10})
		if err != nil {
			yield(nil, err)
			return
		}
		if err := m.meter.SettleModel(ctx, r.ID, execution.BudgetUsage{InputTokens: 10, OutputTokens: 10}); err != nil {
			yield(nil, err)
			return
		}
		if len(m.requests) == 1 || m.ignoreTwice {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file"}}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Only retained evidence; missing sources remain unavailable.", "model"), TurnComplete: true}, nil)
	}
}
func synthesisCorrectionText(q *model.LLMRequest) string {
	var text string
	for _, c := range q.Contents {
		for _, p := range c.Parts {
			text += p.Text
		}
	}
	return text
}

func TestBotSynthesisBoundedCorrection(t *testing.T) {
	for _, twice := range []bool{false, true} {
		name := "corrects_once"
		if twice {
			name = "rejects_twice"
		}
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
			llm.binding.synthesisReserve.final = true
			raw := &synthesisCorrectionModel{t: t, meter: meter, ignoreTwice: twice}
			llm.LLM = raw
			q := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Summarize retained evidence.", "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file"}}}}}}
			var final int
			var failure error
			for response, err := range llm.GenerateContent(f.ctx, q, true) {
				if err != nil {
					failure = err
					continue
				}
				if botResponseHasToolCall(response) {
					t.Fatal("tool call escaped to dispatcher")
				}
				if response != nil && response.TurnComplete {
					final++
				}
			}
			if twice {
				if !errors.Is(failure, errBotSynthesisToolCall) || final != 0 {
					t.Fatalf("final=%d error=%v", final, failure)
				}
			} else if failure != nil || final != 1 {
				t.Fatalf("final=%d error=%v", final, failure)
			}
			if len(raw.requests) != 2 || raw.streams[0] || raw.streams[1] {
				t.Fatalf("calls=%d streams=%v", len(raw.requests), raw.streams)
			}
			for _, r := range raw.requests {
				if len(r.Config.Tools) != 0 || !strings.Contains(synthesisCorrectionText(r), "Synthesis reserve has begun") {
					t.Fatal("request not final-only")
				}
			}
			if !strings.Contains(synthesisCorrectionText(raw.requests[1]), "Your tool request was not executed") {
				t.Fatal("missing bounded correction")
			}
			if len(q.Config.Tools) != 1 || len(q.Contents) != 1 {
				t.Fatal("caller request mutated")
			}
			if u := meter.Usage(); u.InputTokens != 20 || u.OutputTokens != 20 || u.ToolCalls != 0 {
				t.Fatalf("same meter usage=%+v", u)
			}
			read := &synthesisBlockingRead{}
			ctx := botFactoryHandoffContext{base: context.Background(), session: f.req.Run.SessionID.String(), functionCallID: "rejected"}
			result, err := llm.binding.runSynthesisTool(ctx, read, map[string]any{})
			if err != nil || result["status"] != "not_executed" || read.calls != 0 {
				t.Fatalf("dispatch=%d result=%v error=%v", read.calls, result, err)
			}
		})
	}
}

func TestBotSynthesisAssessmentPreservesRequest(t *testing.T) {
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
	llm.binding.synthesisReserve.final = true
	raw := &synthesisCorrectionModel{t: t, meter: meter}
	llm.LLM = raw
	q := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Assess the evidence.", "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file"}}}}}}
	responses := 0
	for response, err := range llm.GenerateContent(bot.WithModelCallPurpose(f.ctx, bot.ModelCallPurposeAssessment), q, true) {
		if err != nil {
			t.Fatal(err)
		}
		if !botResponseHasToolCall(response) {
			t.Fatal("assessment response altered")
		}
		responses++
	}
	if responses != 1 || len(raw.requests) != 1 || raw.requests[0] != q || !raw.streams[0] {
		t.Fatal("assessment was rewritten or corrected")
	}
	if strings.Contains(synthesisCorrectionText(raw.requests[0]), "Synthesis reserve") || len(raw.requests[0].Config.Tools) != 1 {
		t.Fatal("assessment inherited synthesis directive or tool stripping")
	}
}
