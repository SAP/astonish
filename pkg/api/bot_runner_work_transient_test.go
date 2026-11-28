package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Every invocation asserts tools are disabled; the retry must preserve the exact
// request rather than replay production, add evidence, or normalize bad output.
type transientAssessmentModel struct {
	*workStreamModel
	request    *model.LLMRequest
	afterFirst func()
	identical  bool
}

func (m *transientAssessmentModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if m.request == nil {
		m.request = request
	} else if m.identical && m.request != request {
		m.t.Error("transport retry changed immutable request")
	}
	sequence := m.workStreamModel.GenerateContent(ctx, request, stream)
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range sequence {
			if m.calls == 1 && err != nil && m.afterFirst != nil {
				m.afterFirst()
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func TestBotWorkAssessmentTransientRetry(t *testing.T) {
	transient := llmerror.NewLLMError("test", 503, "private-message", "private-body")
	for _, tc := range []struct {
		name    string
		second  workStreamEvent
		wantErr bool
	}{
		{"valid", workStreamText(`{"value":"complete"}`, false, 7, 4), false},
		{"two-failures-held", workStreamEvent{response: workStreamText("", true, 7, 4).response, err: transient}, true},
		{"invalid-json-no-third-call", workStreamText(`{`, false, 7, 4), true},
		{"invalid-schema-no-third-call", workStreamText(`{"value":1}`, false, 7, 4), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			llm := &transientAssessmentModel{workStreamModel: &workStreamModel{t: t, attempts: [][]workStreamEvent{
				{workStreamText(`{"value":"discarded"}`, true, 4, 2), {response: workStreamText("", true, 5, 3).response, err: transient}}, {tc.second},
			}}, identical: true}
			out := struct {
				Value string `json:"value"`
			}{Value: "unchanged"}
			usage := store.BotUsage{InputTokens: 10, OutputTokens: 20, ToolCalls: 2}
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			want := "complete"
			if tc.wantErr {
				want = "unchanged"
			}
			if (err != nil) != tc.wantErr || out.Value != want || llm.calls != 2 || usage.InputTokens != 22 || usage.OutputTokens != 27 || usage.ToolCalls != 2 {
				t.Fatalf("output=%+v calls=%d usage=%+v err=%v", out, llm.calls, usage, err)
			}
		})
	}
}

func TestBotWorkAssessmentTransientNonRetryable(t *testing.T) {
	for _, callErr := range []error{
		errors.New("timeout: 503 connection reset"), context.Canceled, context.DeadlineExceeded,
		bot.ErrBudgetExceeded, store.ErrBotForbidden, store.ErrBotConflict,
		llmerror.NewLLMError("test", 400, "schema", "private"), llmerror.NewLLMError("test", 401, "auth", "private"), llmerror.NewLLMError("test", 403, "permission", "private"),
		llmerror.NewLLMError("test", 408, "timeout", "private"), llmerror.NewLLMError("test", 500, "unspecified", "private"),
		&llmerror.LLMError{StatusCode: 400, Retryable: true},
		llmerror.NewInterruptedStream(llmerror.NewLLMError("test", 503, "partial", "private")),
		errors.Join(context.Canceled, llmerror.NewLLMError("test", 503, "cancel", "private")),
		errors.Join(bot.ErrBudgetExceeded, llmerror.NewLLMError("test", 503, "budget", "private")),
	} {
		t.Run(fmt.Sprintf("%T-%v", callErr, callErr), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{{{err: callErr}}}}
			out := struct {
				Value string `json:"value"`
			}{Value: "unchanged"}
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			if !errors.Is(err, callErr) || llm.calls != 1 || out.Value != "unchanged" {
				t.Fatalf("calls=%d out=%+v err=%v", llm.calls, out, err)
			}
		})
	}
}

func TestBotWorkAssessmentTransientClassification(t *testing.T) {
	for _, err := range []error{
		llmerror.NewLLMError("test", 429, "", ""), llmerror.NewLLMError("test", 502, "", ""), llmerror.NewLLMError("test", 503, "", ""), llmerror.NewLLMError("test", 504, "", ""),
		&net.OpError{Op: "read", Net: "tcp", Err: syscall.ETIMEDOUT}, fmt.Errorf("wrapped: %w", syscall.ECONNRESET),
	} {
		t.Run(fmt.Sprintf("%T-%v", err, err), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{{{err: err}}, {workStreamText(`{"value":"complete"}`, false, 1, 1)}}}
			var out any
			var usage store.BotUsage
			if failure := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage); failure != nil || llm.calls != 2 {
				t.Fatalf("calls=%d err=%v", llm.calls, failure)
			}
		})
	}
}

type transientAssessmentBudget struct {
	execution.RunBudget
	err error
}

func (b *transientAssessmentBudget) Check(ctx context.Context) error {
	if b.err != nil {
		return b.err
	}
	return b.RunBudget.Check(ctx)
}

type transientAssessmentAuthorization struct{ err error }

func (a *transientAssessmentAuthorization) Authorize(context.Context, execution.Capability) error {
	return a.err
}
func (a *transientAssessmentAuthorization) AuthorizeTool(context.Context, string) error { return a.err }

func TestBotWorkAssessmentTransientBoundary(t *testing.T) {
	for _, boundary := range []string{"canceled", "authority", "budget"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			auth := &transientAssessmentAuthorization{}
			req.Authorization = auth
			budget := &transientAssessmentBudget{RunBudget: req.Budget}
			req.Budget = budget
			var want error
			llm := &transientAssessmentModel{workStreamModel: &workStreamModel{t: t, attempts: [][]workStreamEvent{{{err: llmerror.NewLLMError("test", 503, "", "")}}}}, afterFirst: func() {
				switch boundary {
				case "canceled":
					cancel()
					want = context.Canceled
				case "authority":
					auth.err = store.ErrBotForbidden
					want = store.ErrBotForbidden
				case "budget":
					budget.err = bot.ErrBudgetExceeded
					want = bot.ErrBudgetExceeded
				}
			}}
			var out any
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			if !errors.Is(err, want) || llm.calls != 1 || out != nil {
				t.Fatalf("calls=%d out=%v want=%v err=%v", llm.calls, out, want, err)
			}
		})
	}
}

// Exercise the actual employee publication/withholding path on the already
// captured candidate: no producer or research runner is available to replay.
func TestBotWorkAssessmentTransientEmployeeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		twice       bool
		mkErr       func() error
		effectState store.BotReportEffectState
	}{
		{"two-errors-false", false, func() error { return llmerror.NewLLMError("test", 503, "", "") }, store.BotReportEffectsReadOnly},
		{"two-errors-true", true, func() error { return llmerror.NewLLMError("test", 503, "", "") }, store.BotReportEffectsReadOnly},
		{"interrupted-stream", true, func() error {
			return llmerror.NewInterruptedStream(llmerror.NewLLMError("test", 503, "partial", "private"))
		}, store.BotReportEffectsReadOnly},
		{"unknown-effects-graceful", true, func() error { return llmerror.NewLLMError("test", 503, "", "") }, store.BotReportEffectsUnknown},
		{"interrupted-unknown-effects", true, func() error {
			return llmerror.NewInterruptedStream(llmerror.NewLLMError("test", 503, "partial", "private"))
		}, store.BotReportEffectsUnknown},
		{"quote-not-captured-protocol", true, func() error { return botContentProtocolFailure(botContentQuoteMismatch, "item", "source", 0, 0) }, store.BotReportEffectsReadOnly},
		{"candidate-identity-criteria", true, func() error { return errors.New("invalid assessment identity, coverage or evidence") }, store.BotReportEffectsReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			text := "IBM released downloadable model weights."
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "AI News"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: text, Bodies: map[string]string{"tool-response:source": `{"content":"IBM released downloadable model weights."}`}}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			w.Criteria = botEmployeeReviewCriteria(w)
			in := botContentInput(w)
			answer := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairNone}}
			answer.CandidateDigest = w.Candidate.Digest
			for _, c := range in.Criteria {
				answer.Criteria = append(answer.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Useful supported selected news."})
			}
			for _, item := range in.Items {
				a := botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Captured source establishes release."}
				for ref := range in.Sources {
					a.Sources = append(a.Sources, botWorkContentSource{Ref: ref, Quote: text})
				}
				answer.ContentAssessments = append(answer.ContentAssessments, a)
			}
			raw, err := json.Marshal(answer)
			if err != nil {
				t.Fatal(err)
			}
			transient := tc.mkErr()
			second := workStreamText(string(raw), false, 7, 4)
			if tc.twice {
				second.err = transient
			}
			// InterruptedStreamError is not retryable by botWorkJSON, so the
			// review model is called only once (not twice) for that case.
			wantCalls := 2
			wantInputTokens := int64(12)
			wantOutputTokens := int64(7)
			isNonRetryable := !botReviewTransientModelError(transient)
			if isNonRetryable {
				wantCalls = 1
				wantInputTokens = 5
				wantOutputTokens = 3
			}
			llm := &transientAssessmentModel{workStreamModel: &workStreamModel{t: t, attempts: [][]workStreamEvent{{{response: workStreamText("", true, 5, 3).response, err: transient}}, {second}}}, identical: !isNonRetryable}
			saved := &agentExecutionWorkStore{state: w, requirePrincipal: true}
			result := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text, EffectState: tc.effectState}, Usage: store.BotUsage{ToolCalls: 2}}
			final, err := reviewBotEmployeeResult(ctx, req, &BotChatResources{Work: saved}, llm, w, w.Clone(), &result)
			if err != nil || llm.calls != wantCalls || result.Usage.InputTokens != wantInputTokens || result.Usage.OutputTokens != wantOutputTokens || result.Usage.ToolCalls != 2 || final.EmployeeReviewAttempts != 1 || final.Repairs != 0 {
				t.Fatalf("calls=%d result=%+v final=%+v err=%v", llm.calls, result, final, err)
			}
			if tc.twice {
				// Review failed twice but the candidate is a safe read-only
				// text answer: deliver it as partial with a caveat rather
				// than withholding useful work.
				if result.Status != store.RunPartial || result.Report.Summary != text || final.HasAcceptedEmployeeReview() {
					t.Fatal("two transport failures should deliver safe candidate as partial")
				}
				found := false
				for _, u := range result.Report.Unverified {
					if strings.Contains(u, "review was unavailable") {
						found = true
					}
				}
				if !found {
					t.Fatal("missing review-unavailable caveat in unverified")
				}
			} else if result.Status != store.RunSucceeded || result.Report.Summary != text || !final.HasAcceptedEmployeeReview() {
				t.Fatalf("valid recovered review not delivered: %+v", result)
			}
		})
	}
}

// TestBotWorkAssessmentContextExpiredDelivery verifies that when the review call
// fails because the context deadline expired but a safe candidate already exists,
// the candidate is still delivered as partial rather than being classified as
// "interrupted" with a generic fallback message.
func TestBotWorkAssessmentContextExpiredDelivery(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	text := "Tech sector shows mixed signals amid AI spending surge."
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Stock Market News"})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: text, Bodies: map[string]string{"tool-response:source": `{"content":"Tech sector shows mixed signals."}`}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.Criteria = botEmployeeReviewCriteria(w)
	// Simulate a context that expires during the review call.
	ctx, cancel := context.WithCancel(ctx)
	cancel() // context is already cancelled when review runs
	transient := llmerror.NewInterruptedStream(context.DeadlineExceeded)
	llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{{{err: transient}}}}
	saved := &agentExecutionWorkStore{state: w, requirePrincipal: true}
	result := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text, EffectState: store.BotReportEffectsUnknown}, Usage: store.BotUsage{ToolCalls: 1}}
	final, err := reviewBotEmployeeResult(ctx, req, &BotChatResources{Work: saved}, llm, w, w.Clone(), &result)
	if err != nil {
		t.Fatalf("context-expired review should gracefully deliver, got err=%v", err)
	}
	if result.Status != store.RunPartial {
		t.Fatalf("expected RunPartial, got %v", result.Status)
	}
	if result.Report.Summary != text {
		t.Fatalf("expected candidate text, got %q", result.Report.Summary)
	}
	found := false
	for _, u := range result.Report.Unverified {
		if strings.Contains(u, "review was unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing review-unavailable caveat in unverified")
	}
	if final.HasAcceptedEmployeeReview() {
		t.Fatal("should not have accepted review")
	}
	result.Report.Work = store.BuildBotWorkProjection(final)
	msg := bot.ComposeEmployeeMessage(ctx, result.Status, result.Report, nil, bot.EmployeeMessageOptions{Work: final})
	result.Report.EmployeeMessage = &msg
	if msg.Origin != store.BotEmployeeMessageAgent || msg.Text != text {
		t.Fatalf("expected agent employee message with candidate text, got %+v", msg)
	}
	if final.Candidate.ReadOnlyResult || final.HasReadOnlyEmployeeResult() {
		t.Fatal("historical review candidate was relabeled as a fresh attested result")
	}
	uncertain := result.Report
	uncertain.EffectState = store.BotReportEffectsUncertain
	uncertain.EmployeeMessage = nil
	warning := bot.ComposeEmployeeMessage(ctx, result.Status, uncertain, nil, bot.EmployeeMessageOptions{Work: final})
	if warning.Origin != store.BotEmployeeMessageFallback || warning.Reason != store.BotEmployeeReasonAgentUnverifiedEffects {
		t.Fatalf("historical compatibility suppressed uncertain-effect warning: %+v", warning)
	}
	if err := result.Report.ValidateEmployeeMessage(result.Status, final); err != nil {
		t.Fatalf("ValidateEmployeeMessage failed: %v", err)
	}
}

func TestBotWorkAssessmentTransientUnsafeFrames(t *testing.T) {
	for _, response := range []*model.LLMResponse{
		{Interrupted: true}, {ErrorCode: "failed"},
		{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "forbidden"}}}}},
		workStreamText(strings.Repeat("x", store.BotWorkMaxContractBytes+1), true, 0, 0).response,
	} {
		t.Run(fmt.Sprintf("interrupted-%t-code-%s-bytes-%d", response.Interrupted, response.ErrorCode, len(fmt.Sprint(response.Content))), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{{{response: response, err: llmerror.NewLLMError("test", 503, "", "")}}}}
			var out any
			var usage store.BotUsage
			if err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage); err == nil || llm.calls != 1 || out != nil {
				t.Fatalf("unsafe frame retried/accepted: calls=%d err=%v", llm.calls, err)
			}
		})
	}
}

func TestBotWorkAssessmentTransientSharesFormatSlot(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{{workStreamText(`{`, false, 4, 2)}, {{err: llmerror.NewLLMError("test", 503, "", "")}}}}
	var out any
	var usage store.BotUsage
	if err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage); err == nil || llm.calls != 2 || out != nil {
		t.Fatalf("calls=%d out=%v err=%v", llm.calls, out, err)
	}
}
