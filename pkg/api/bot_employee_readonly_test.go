package api

import (
	"context"
	"encoding/json"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Run the real adapter and tool journal, not an injected already-classified result.
func TestBotEmployeeReadOnlyPublishesWithoutReview(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	req.Run.Intent = store.BotIntentRegular
	req.Instructions = "Read the local source and report the delivery date."
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	saved := &agentExecutionWorkStore{state: w}
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read the controlled source"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
			return nil, err
		}
		return map[string]any{"content": "Delivery is Friday."}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const answer = "Delivery is Friday."
	turns := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "source.txt"}), TextTurn(answer))
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
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.Status != store.RunSucceeded || result.Report.Summary != answer || result.Report.EffectState != store.BotReportEffectsReadOnly {
		t.Fatalf("ordinary read required review or lost candidate: calls=%d result=%+v", calls, result)
	}
	if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.EmployeeMessage.Text != answer {
		t.Fatalf("candidate not delivered as employee message: %+v", result.Report.EmployeeMessage)
	}
	if saved.state.EmployeeReviewAttempts != 0 || saved.state.Review != nil || saved.state.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 {
		t.Fatalf("review history fabricated or policy removed: %+v", saved.state)
	}
	// Round-trip the persisted candidate and exercise the real commit gate.
	raw, err := json.Marshal(saved.state)
	if err != nil {
		t.Fatal(err)
	}
	var restored store.BotWorkState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBotWorkCompletion(&restored, store.BotRunCommit{Lease: req.Run.Lease, Status: result.Status, Report: result.Report}); err != nil {
		t.Fatalf("delivered candidate cannot commit after restore: %v", err)
	}
	// A terminal candidate cannot replay its producer.
	_, _, err = runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, saved.state, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
		t.Fatal("replayed producer")
		return bot.RunResult{}, nil
	}, llm)
	if err == nil {
		t.Fatal("terminal work became executable")
	}
}

// Keep an explicit reviewer model available for unsafe/unclassified results.
func TestBotEmployeeReviewBypassRequiresPositiveReadOnlyResult(t *testing.T) {
	for _, mode := range []string{"read-only", "unknown", "uncertain", "confirmed", "changed", "unverified", "artifact", "approval", "pending-control", "explicit-criteria"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Report observations"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			if mode == "explicit-criteria" {
				w.Criteria = []store.BotWorkCriterion{{ID: "owner-observations", SourceIDs: []string{"assignment", "charter"}, Text: "Report observations with source support."}}
			}
			saved := &agentExecutionWorkStore{state: w}
			calls := 0
			reviewer := &agentCheckModel{run: func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					yield(&model.LLMResponse{Content: genai.NewContentFromText("invalid review", "model")}, nil)
				}
			}}
			_, _, _ = runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				c.FinalEventID = "answer"
				r := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Observed source.", EffectState: store.BotReportEffectsReadOnly}}
				switch mode {
				case "unknown":
					r.Report.EffectState = store.BotReportEffectsUnknown
				case "uncertain":
					r.Report.EffectState = store.BotReportEffectsUncertain
				case "confirmed":
					r.Report.EffectState = store.BotReportEffectsConfirmed
				case "changed":
					r.Report.Changed = []store.BotEvidence{{Summary: "Write observed"}}
				case "unverified":
					r.Report.Unverified = []string{"Uncertain effect remains"}
				case "artifact":
					r.Report.Artifacts = []store.BotArtifact{{}}
				case "approval":
					r.Report.NeedsApproval = []store.RunID{req.Run.ID}
				case "pending-control":
					c.pendingControls = append(c.pendingControls, botWorkPendingControl{})
				}
				return r, nil
			}, reviewer)
			if mode == "explicit-criteria" && (len(saved.state.Criteria) != 1 || saved.state.Criteria[0].ID != "owner-observations" || calls == 0) {
				t.Fatalf("explicit criteria were replaced or reviewer bypassed: calls=%d criteria=%+v", calls, saved.state.Criteria)
			}
			if mode == "read-only" && calls != 0 {
				t.Fatalf("read-only result called reviewer %d times", calls)
			}
			if mode != "read-only" && saved.state.HasReadOnlyEmployeeResult() {
				t.Fatalf("%s bypassed review reservation", mode)
			}
			if mode != "read-only" && mode != "artifact" && saved.state.EmployeeReviewAttempts == 0 {
				t.Fatalf("%s did not reserve review", mode)
			}
		})
	}
}
