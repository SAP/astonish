package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type boundaryOwnerModel struct {
	calls   int
	failure error
}

func (*boundaryOwnerModel) Name() string { return "owner" }
func (m *boundaryOwnerModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.failure != nil {
			yield(nil, m.failure)
			return
		}
		var source struct {
			SourceDigest string `json:"source_digest"`
			TextDigest   string `json:"text_digest"`
			Facts        struct {
				SourceDigest string `json:"source_digest"`
			} `json:"facts"`
		}
		_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &source)
		response := map[string]any{"source_digest": source.SourceDigest, "outcome": "incomplete", "text": "I couldn't complete the work because an operational failure stopped it. A safe specific cause is unavailable; please review the run diagnostics before deciding how to continue."}
		if source.TextDigest != "" {
			response = map[string]any{"source_digest": source.Facts.SourceDigest, "text_digest": source.TextDigest, "outcome": "incomplete", "approved": true}
		}
		raw, _ := json.Marshal(response)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1, CandidatesTokenCount: 1}}, nil)
	}
}

func TestEmployeeSetupFallbackEnvelope(t *testing.T) {
	for _, cause := range []error{errors.New("SECRET setup URL"), context.DeadlineExceeded, context.Canceled, bot.ErrBudgetExceeded} {
		result, err := botChatFactoryFailure(context.Background(), cause)
		if err == nil || result.Report.EmployeeMessage == nil {
			t.Fatal("missing setup failure")
		}
		message := result.Report.EmployeeMessage
		if message.Origin != store.BotEmployeeMessageFallback || strings.Contains(message.Text, "SECRET") {
			t.Fatalf("unsafe setup envelope: %+v", message)
		}
		if cause != context.Canceled && cause != bot.ErrBudgetExceeded && message.FailureKind != store.BotReportFailureSetup {
			t.Fatal("setup classification lost")
		}
		if err := result.Report.ValidateEmployeeMessage(result.Status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmployeeNarrationUsesExistingBudgetReservation(t *testing.T) {
	for _, limit := range []int64{1, 300} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			owner := &boundaryOwnerModel{}
			llm, err := provider.NewBudgetLLM(owner, budget, provider.BudgetLLMOptions{MaxOutputTokens: 256, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 2, nil }})
			if err != nil {
				t.Fatal(err)
			}
			usage := store.BotUsage{}
			message := bot.ComposeEmployeeMessage(context.Background(), store.RunFailed, store.BotReport{}, nil, bot.EmployeeMessageOptions{LLM: llm, Boundary: budget.Check, Usage: &usage})
			if limit == 1 {
				if owner.calls != 0 || message.Origin != store.BotEmployeeMessageFallback {
					t.Fatalf("exhausted reservation invoked owner: %+v calls=%d", message, owner.calls)
				}
			} else if owner.calls != 2 || message.Origin != store.BotEmployeeMessageModel || usage.InputTokens != 2 || usage.OutputTokens != 2 {
				t.Fatalf("missing metered narration: %+v %+v calls=%d", message, usage, owner.calls)
			}
		})
	}
}

func TestEmployeeContinuationAggregatesBeforeEnvelope(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunSucceeded, store.RunFailed, store.RunWaitingApproval} {
		t.Run(string(status), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = context.WithValue(ctx, botResumeInteractionKey{}, &store.BotApproval{})
			req.Run.Report = store.BotReport{Changed: []store.BotEvidence{{Summary: "SECRET prior evidence"}}, Unverified: []string{"SECRET prior uncertainty"}}
			owner := &boundaryOwnerModel{}
			result, _ := runBotWork(ctx, req, &BotChatResources{}, owner, false, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
				if status == store.RunFailed {
					return botChatFailure(bot.ErrorPermanent, errors.New("SECRET provider failure"))
				}
				return bot.RunResult{Status: status, Report: store.BotReport{Summary: "SECRET draft", Changed: []store.BotEvidence{{Summary: "SECRET current evidence"}}}}, nil
			})
			if result.Report.ValidateEmployeeMessageIntegrity(result.Status) != nil || len(result.Report.Unverified) != 1 || len(result.Report.Changed) == 0 {
				t.Fatalf("continuation source not bound after aggregation: %+v", result)
			}
			if status == store.RunSucceeded && result.Status != store.RunPartial {
				t.Fatal("prior uncertainty promoted to success")
			}
			if owner.calls != 0 || strings.Contains(bot.RenderReport(store.BotRun{Status: result.Status, Report: result.Report}), "SECRET") {
				t.Fatal("legacy continuation narrated or leaked private data")
			}
		})
	}
}

func TestBotWorkTypedPreparationFailureEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		kind  store.BotReportFailureKind
	}{
		{"unknown-budget", bot.ErrBudgetUnknownCost, store.BotReportFailureBudget},
		{"checkpoint", &bot.CheckpointResponseError{Reason: bot.CheckpointResponseMultipleCalls}, store.BotReportFailureExecutionFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			ws := &workMemoryStore{state: req.Work.Clone()}
			result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, &boundaryOwnerModel{failure: tc.cause}, false, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
				t.Fatal("producer ran after failed preparation")
				return bot.RunResult{}, nil
			})
			if err == nil || result.Status != store.RunFailed || result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.FailureKind != tc.kind {
				t.Fatalf("typed failure lost: %+v err=%v", result, err)
			}
			if err := result.Report.ValidateEmployeeMessage(result.Status, ws.state); err != nil {
				t.Fatal(err)
			}
		})
	}
}
