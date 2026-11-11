package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestBotCheckpointFailureClassification(t *testing.T) {
	cause := &bot.CheckpointResponseError{Reason: bot.CheckpointResponseMultipleCalls, ToolCalls: 2}
	wrapped := fmt.Errorf("private wrapper: %w", cause)
	result, err := botChatFailure(bot.ErrorBlocked, wrapped)
	var got *bot.CheckpointResponseError
	if !errors.As(err, &got) || got != cause || result.Failure.Class != bot.ErrorPermanent || result.Report.Failure == nil {
		t.Fatalf("lost identity: %+v %v", result, err)
	}
	if strings.Contains(result.Report.Failure.TechnicalDetails, "private") {
		t.Fatal("wrapper leaked")
	}
	result, _ = botChatFailure(bot.ErrorPermanent, errors.Join(context.Canceled, cause))
	if result.Status != store.RunInterrupted || result.Report.Failure == nil || result.Report.Failure.Kind != store.BotReportFailureInterrupted {
		t.Fatal("cancellation lost precedence")
	}
}

func TestBotCheckpointFailureLookalikeUsesNormalError(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	ctx = bot.WithApprovalStore(ctx, &resumeTestStore{})
	cause := &bot.CheckpointResponseError{Reason: bot.CheckpointResponseMultipleCalls, ToolCalls: 2}
	llm := NewMockLLM(&MockTurn{ErrorMessage: cause.Error()})
	var cleaned atomic.Int64
	adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, nil, &cleaned))
	if err != nil {
		t.Fatal(err)
	}
	rawErrors := 0
	result, err := adapter.Start(ctx, req, func(_ context.Context, e bot.RuntimeEvent) error {
		if e.Type == "error" {
			rawErrors++
		}
		return nil
	})
	if err == nil || result.Report.Failure == nil || result.Report.Failure.Kind != store.BotReportFailureOperational || result.Report.ValidateEmployeeMessageIntegrity(result.Status) != nil || rawErrors != 1 {
		t.Fatalf("lookalike suppressed/classified: %+v errors=%d", result, rawErrors)
	}
	stored, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	persisted := 0
	for e := range stored.Session.Events().All() {
		if e.Content != nil {
			for _, p := range e.Content.Parts {
				if strings.Contains(p.Text, "[Error:") {
					persisted++
				}
			}
		}
	}
	if persisted != 1 {
		t.Fatalf("ordinary error persistence changed: %d", persisted)
	}
}

// Run the real capture decorator and ChatRunner. The rejected response mixes
// an otherwise allowed read with a disallowed send; neither may run.
func TestBotCheckpointFailureAdapter(t *testing.T) {
	for _, prior := range []bool{false, true} {
		for _, oversized := range []bool{false, true} {
			name := "batch"
			if oversized {
				name = "oversized"
			}
			if prior {
				name += "/prior-work"
			}
			t.Run(name, func(t *testing.T) {
				ctx, req, ss := botChatTestRequest(t)
				approvals := &resumeTestStore{}
				ctx = bot.WithApprovalStore(ctx, approvals)
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
				var cleaned, reads, sends atomic.Int64
				read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled read"}, func(c tool.Context, _ map[string]any) (map[string]any, error) {
					reads.Add(1)
					bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Earlier receipt observed", Source: "fixture://receipt", ObservedAt: time.Now(), ActorID: req.Bot.OwnerID}}})
					return map[string]any{"content": "earlier receipt"}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				send, err := functiontool.New(functiontool.Config{Name: "send_email", Description: "Must not execute"}, func(tool.Context, map[string]any) (map[string]any, error) {
					sends.Add(1)
					return map[string]any{"sent": true}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				rejected := MultiToolCallTurn(&genai.FunctionCall{ID: "reject-read", Name: "read_file", Args: map[string]any{"secret": "must-not-leak"}}, &genai.FunctionCall{ID: "reject-send", Name: "send_email", Args: map[string]any{"secret": "must-not-leak"}})
				if oversized {
					rejected = TextTurn(strings.Repeat("x", 4<<20))
				}
				turns := []*MockTurn{}
				if prior {
					turns = append(turns, ToolCallTurn("read_file", map[string]any{}))
				}
				turns = append(turns, rejected, TextTurn("must never retry"))
				llm := NewMockLLM(turns...)
				adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{read, send}, &cleaned))
				if err != nil {
					t.Fatal(err)
				}
				var events []bot.RuntimeEvent
				result, err := adapter.Start(ctx, req, func(_ context.Context, e bot.RuntimeEvent) error { events = append(events, e); return nil })
				if !errors.Is(err, store.ErrBotUnsupported) || result.Status != store.RunFailed || result.Failure == nil || result.Failure.Class != bot.ErrorPermanent || !result.Failure.TerminalForAttempt {
					t.Fatalf("unexpected failure: %+v %v", result, err)
				}
				if result.Report.Failure == nil || result.Report.Failure.Kind != store.BotReportFailureExecutionFormat {
					t.Fatalf("missing typed report: %+v", result.Report)
				}
				for _, want := range []string{"could not be processed", "None of the tool requests from that response were executed", "No approval is needed", "not been automatically retried"} {
					if !strings.Contains(result.Report.Summary, want) {
						t.Errorf("missing %q: %s", want, result.Report.Summary)
					}
				}
				if strings.Contains(result.Report.Summary+result.Report.Failure.TechnicalDetails, "must-not-leak") {
					t.Fatal("arguments leaked")
				}
				expected := int64(0)
				if prior {
					expected = 1
				}
				if reads.Load() != expected || sends.Load() != 0 || len(llm.Calls) != int(expected)+1 || cleaned.Load() != 1 {
					t.Fatalf("read=%d send=%d model=%d cleanup=%d", reads.Load(), sends.Load(), len(llm.Calls), cleaned.Load())
				}
				if len(result.Report.Unchanged) != int(expected) || result.Usage.ToolCalls != expected {
					t.Fatalf("earlier evidence lost: %+v", result)
				}
				if approvals.a != nil || len(result.Report.NeedsApproval) != 0 || len(result.Checkpoint) != 0 {
					t.Fatal("format failure created approval")
				}
				for _, e := range events {
					if e.Type == "error" {
						t.Errorf("duplicate raw error: %s", e.Payload)
					}
				}
				stored, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
				if err != nil {
					t.Fatal(err)
				}
				for e := range stored.Session.Events().All() {
					if e.Content != nil {
						for _, p := range e.Content.Parts {
							if strings.Contains(p.Text, "[Error:") {
								t.Errorf("duplicate persisted error: %s", p.Text)
							}
						}
					}
				}
				if len(events) == 0 || events[len(events)-1].Type != "done" {
					t.Fatal("missing done")
				}
			})
		}
	}
}
