package api

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotEmployeeEmptyResearchJournal(t *testing.T) {
	for _, mode := range []string{"completed", "ordinary", "error", "blocked", "not-final", "empty-text", "missing-event", "missing-response", "orphan-response", "effect", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			j := &botAgentReadJournal{}
			c := ChatCompletion{Final: true, FinalEventID: "final", FinalText: "A useful revision from retained evidence."}
			switch mode {
			case "error":
				c.Err = errors.New("failed completion")
			case "blocked":
				c.Blocked = true
			case "not-final":
				c.Final = false
			case "empty-text":
				c.FinalText = " "
			case "missing-event":
				c.FinalEventID = ""
			case "missing-response":
				j.seen = map[string]bool{"call": true}
				j.calls = map[string]botAgentReadCall{"call": {name: "web_fetch", read: true}}
				c.Usage.ToolCalls = 1
			case "orphan-response":
				j.take(&genai.FunctionResponse{ID: "orphan", Name: "web_fetch"})
				c.toolResponses = 1
			case "effect":
				c.Report.Changed = []store.BotEvidence{{Summary: "sent"}}
			case "unverified":
				c.Report.Unverified = []string{"missing receipt"}
			}
			if mode != "ordinary" {
				j.completeConstrainedText(c)
			}
			want := store.BotReportEffectsUnknown
			if mode == "completed" {
				want = store.BotReportEffectsReadOnly
			}
			if mode == "missing-response" || mode == "orphan-response" {
				want = store.BotReportEffectsUncertain
			}
			if got := j.effectState(); got != want {
				t.Fatalf("effect state %q want %q", got, want)
			}
		})
	}
}

func TestBotEmployeeEmptyResearchAttempt(t *testing.T) {
	for _, mode := range []string{"narrowed", "ordinary", "error", "changed-policy"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			req.Policy = policy
			req.Mandate = &bot.MandateExecution{}
			req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "AI News"})
			req.Work.Candidate = &store.BotWorkCandidate{Text: "previous briefing"}
			ctx = execution.WithRunPolicy(ctx, policy)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: employeeGuard{}, Classifier: employeeClassifierFunc(func(_ context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
				return execution.ActionDescriptor{Tool: n, Arguments: a, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
			})})
			if mode != "ordinary" {
				child, directed, e := botEmployeeResearchRequest(ctx, req, BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch, MissingDimensions: []string{"verified news"}, SourceNeeds: []string{"source evidence"}, PublicQueries: []string{"AI news"}})
				if e != nil {
					t.Fatal(e)
				}
				ctx, req = child, directed.Request
			}
			if mode == "changed-policy" {
				req.Policy = policy
				ctx = execution.WithRunPolicy(ctx, policy)
			}
			llm := &agentCheckModel{run: func(_ context.Context, _ *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					if mode == "error" {
						yield(nil, errors.New("model failed"))
						return
					}
					yield(&model.LLMResponse{Content: genai.NewContentFromText("Useful revised briefing from existing evidence.", "model")}, nil)
				}
			}}
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
			var cleaned atomic.Int64
			resources, e := botChatTestFactory(t, ss, llm, nil, &cleaned)(ctx, req)
			if e != nil {
				t.Fatal(e)
			}
			existing, e := ss.Get(ctx, &session.GetRequest{AppName: resources.AppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
			if e != nil {
				t.Fatal(e)
			}
			var sequence int64
			var completion ChatCompletion
			result, e := runBotChatAttempt(ctx, req, resources, func(context.Context, bot.RuntimeEvent) error { return nil }, nil, existing, nil, false, &sequence, &completion)
			if mode == "error" {
				if e == nil || result.Report.EffectState == store.BotReportEffectsReadOnly {
					t.Fatal("failed attempt attested read-only")
				}
				return
			}
			want := store.BotReportEffectsUnknown
			if mode == "narrowed" {
				want = store.BotReportEffectsReadOnly
			}
			if e != nil || result.Status != store.RunSucceeded || result.Report.EffectState != want {
				t.Fatalf("result %v %+v want effect %q", e, result, want)
			}
			if completion.Report.EffectState != want {
				t.Fatal("completion/report mismatch")
			}
		})
	}
}

func TestBotEmployeeScopePrompt(t *testing.T) {
	for _, prompt := range []string{botWorkReviewPrompt + botEmployeeAssessmentGuidance, botEmployeeReviewCriteria(store.NewBotAgentWorkState(store.BotWorkContract{}))[0].Text} {
		for _, rule := range []string{"useful relevant source-grounded briefing", "not an exhaustive", "employee assumptions", "unavailable source"} {
			if !strings.Contains(prompt, rule) {
				t.Errorf("review lacks scope rule %q", rule)
			}
		}
	}
}
