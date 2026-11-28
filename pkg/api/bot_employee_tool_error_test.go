package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestBotRecoverableToolFeedbackDiagnosticWrappers(t *testing.T) {
	native := errors.New("native callback failed")
	feedback := &botToolFeedbackError{cause: native, recoverable: true}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"diagnostic", fmt.Errorf("chat runner: %w", feedback), true},
		{"nested-join", fmt.Errorf("producer: %w", errors.Join(feedback, fmt.Errorf("tool: %w", feedback))), true},
		{"native", fmt.Errorf("chat runner: %w", native), false},
		{"terminal-feedback", fmt.Errorf("chat runner: %w", &botToolFeedbackError{cause: native}), false},
		{"mixed-native", fmt.Errorf("chat runner: %w", errors.Join(feedback, native)), false},
		{"mixed-cancel", fmt.Errorf("chat runner: %w", errors.Join(feedback, context.Canceled)), false},
		{"mixed-budget", fmt.Errorf("chat runner: %w", errors.Join(feedback, bot.ErrBudgetExceeded)), false},
		{"mixed-policy", fmt.Errorf("chat runner: %w", errors.Join(feedback, &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyDeny}})), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := botOnlyRecoverableToolFeedback(tc.err); got != tc.want {
				t.Fatalf("recoverable=%t want=%t error=%v", got, tc.want, tc.err)
			}
			if !errors.Is(tc.err, native) && tc.name != "mixed-cancel" && tc.name != "mixed-budget" && tc.name != "mixed-policy" {
				t.Fatal("diagnostic wrapper lost native identity")
			}
		})
	}
}

// Recovery eligibility does not clear Completion.Err or its native identity.
func TestBotToolErrorTrustedCorrection(t *testing.T) {
	for _, disposition := range []bot.ToolFeedbackDisposition{bot.ToolFeedbackCorrectCall, bot.ToolFeedbackUnresolvedTarget, bot.ToolFeedbackTerminal} {
		t.Run(string(disposition), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx, _ = bot.WithToolOutcomes(ctx)
			journal := &botAgentReadJournal{}
			ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			call := &genai.FunctionCall{ID: "correction", Name: "execute_tool", Args: map[string]any{"name": "read_file", "arguments": map[string]any{}}}
			journal.capture(ctx, call)
			bot.RecordToolOutcome(ctx, call.ID, bot.NewToolNonexecutionOutcome(ctx, call.ID, "read_file", map[string]any{}, disposition))
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = ctx
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: map[string]any{"error": errors.New("invalid call")}}}}}}})
			if got := cr.completion.Err == nil; got != (disposition != bot.ToolFeedbackTerminal) {
				t.Fatalf("recoverable=%v error=%v", got, cr.completion.Err)
			}
			if journal.effectState() != store.BotReportEffectsReadOnly {
				t.Fatal("known rejection became uncertain effect")
			}
		})
	}
}

func TestBotEmployeeToolErrorRecoveryBoundary(t *testing.T) {
	for _, mode := range []string{"read-feedback", "execution-state", "native-error", "policy", "cancel", "budget", "approval", "denied", "context-required", "context-stale", "model-error", "runner-error", "sink-error", "unknown-effects", "uncertain-effects", "changed", "unverified", "artifact", "pending", "missing-response", "missing-final", "missing-event", "blank-final", "legacy", "no-work"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Read sources"})
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
			journal := &botAgentReadJournal{}
			ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = ctx
			call := &genai.FunctionCall{ID: "read", Name: "read_file", Args: map[string]any{"path": "source"}}
			journal.capture(ctx, call)
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}})
			response := map[string]any{"error": "source unavailable"}
			var native error
			switch mode {
			case "native-error":
				native = errors.New("callback failure")
			case "policy":
				native = &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyDeny}}
			case "cancel":
				native = context.Canceled
			case "budget":
				native = bot.ErrBudgetExceeded
			case "execution-state":
				response["state"] = agent.ProgressiveStateExecutionError
			case "approval":
				response["state"] = agent.ProgressiveStateApprovalRequired
			case "denied":
				response["state"] = agent.ProgressiveStateAuthorizationDenied
			case "context-required":
				response["state"] = agent.ProgressiveStateContextRequired
			case "context-stale":
				response["state"] = agent.ProgressiveStateContextStale
			}
			if native != nil {
				response["error"] = native
			}
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: response}}}}}})
			cr.observeCompletion(&session.Event{ID: "final", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("The source is unavailable; I cannot establish the answer.", "model")}})
			switch mode {
			case "model-error":
				cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{ErrorCode: "provider-failure"}})
			case "runner-error", "sink-error":
				cr.recordRunError(errors.New("persistence failed"))
			}
			c := cr.Completion()
			if c.Err == nil || (native != nil && !errors.Is(c.Err, native)) {
				t.Fatalf("lost diagnostic identity: %v", c.Err)
			}
			switch mode {
			case "unknown-effects":
				c.Report.EffectState = store.BotReportEffectsUnknown
			case "uncertain-effects":
				c.Report.EffectState = store.BotReportEffectsUncertain
			case "changed":
				c.Report.Changed = []store.BotEvidence{{Summary: "write"}}
			case "unverified":
				c.Report.Unverified = []string{"uncertain effect"}
			case "artifact":
				c.Report.Artifacts = []store.BotArtifact{{}}
			case "pending":
				c.pendingControls = []botWorkPendingControl{{}}
			case "missing-response":
				c.Usage.ToolCalls++
			case "missing-final":
				c.Final = false
			case "missing-event":
				c.FinalEventID = ""
			case "blank-final":
				c.FinalText = " "
			case "legacy":
				req.Work.Protocol = ""
			case "no-work":
				req.Work = nil
			}
			want := mode == "read-feedback" || mode == "execution-state"
			if got := botCanRetainReadOnlyFinal(c, req, c.Report); got != want {
				t.Fatalf("retained=%v want=%v completion=%+v", got, want, c)
			}
		})
	}
}

// Exercise tool execution -> final capture -> employee message -> restored commit
// validation, rather than injecting an already-successful producer result.
func TestBotEmployeeToolErrorFinalCommunication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		recover bool
	}{
		{"specific-blocker", "I’m unable to produce a verified current research-and-model-release briefing right now: the configured news search is unavailable, and source retrieval is consequently blocked. I don’t want to substitute unverified headlines or stale information.", false},
		{"useful-recovery", "Delivery is Friday according to the available local source. The first source was unavailable.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			req.Run.Intent = store.BotIntentRegular
			req.Instructions = "Read the sources and report what you can establish."
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			saved := &agentExecutionWorkStore{state: w}
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
			toolCalls := 0
			action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read a controlled source"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
					return nil, err
				}
				toolCalls++
				if toolCalls == 1 {
					return map[string]any{"error": "configured source unavailable"}, nil
				}
				return map[string]any{"content": "Delivery is Friday."}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			turns := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "source.txt"}), TextTurn(tc.answer))
			wantCalls := 2
			if tc.recover {
				turns = NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "source.txt"}), ToolCallTurn("read_file", map[string]any{"path": "alternate.txt"}), TextTurn(tc.answer))
				wantCalls = 3
			}
			calls := 0
			llm := &agentCheckModel{run: func(ctx context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				calls++
				return turns.GenerateContent(ctx, q, false)
			}}
			var cleaned atomic.Int64
			factory := botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned)
			adapter, err := NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
				r, e := factory(ctx, req)
				if e == nil {
					r.Work = saved
				}
				return r, e
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil || result.Status != store.RunPartial || result.Report.Summary != tc.answer {
				t.Fatalf("actual employee final discarded: result=%+v err=%v", result, err)
			}
			if calls != wantCalls || toolCalls != wantCalls-1 || result.Usage.ToolCalls != int64(toolCalls) || saved.state.EmployeeReviewAttempts != 0 || saved.state.Review != nil {
				t.Fatalf("replayed tools, lost accounting or added mandatory review: calls=%d tools=%d usage=%+v work=%+v", calls, toolCalls, result.Usage, saved.state)
			}
			if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.EmployeeMessage.Text != tc.answer || result.Report.Failure != nil {
				t.Fatalf("generic fallback replaced employee: %+v", result.Report)
			}
			if saved.state.Candidate == nil || saved.state.Candidate.EventID == "" || saved.state.Candidate.Text != tc.answer || !saved.state.Candidate.ReadOnlyResult {
				t.Fatalf("final candidate not retained: %+v", saved.state)
			}
			raw, err := json.Marshal(saved.state)
			if err != nil {
				t.Fatal(err)
			}
			var restored store.BotWorkState
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(result.Report)
			if err != nil {
				t.Fatal(err)
			}
			var report store.BotReport
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			if err := store.ValidateBotWorkCompletion(&restored, store.BotRunCommit{Lease: req.Run.Lease, Status: result.Status, Report: report}); err != nil {
				t.Fatalf("restored commit rejected: %v", err)
			}
			if err := report.ValidateEmployeeMessage(result.Status, &restored); err != nil {
				t.Fatalf("restored employee message rejected: %v", err)
			}
		})
	}
}
