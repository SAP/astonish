package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"iter"
	"strings"
	"testing"
)

func TestBotEmployeeFormattingPreservesClaims(t *testing.T) {
	w := store.NewBotAgentWorkState(store.BotWorkContract{})
	w.Candidate = &store.BotWorkCandidate{Text: "The index rose 2%.\nSource: https://example.com/close"}
	for _, text := range []string{"**The index rose 2%.**\n\nSource: https://example.com/close", "The index rose 8%.\nSource: https://example.com/close", "The index rose 2%.\nSource: https://example.com/other"} {
		got := botEmployeeSameClaims(w.Candidate.Text, text)
		want := text == "**The index rose 2%.**\n\nSource: https://example.com/close"
		if got != want {
			t.Fatalf("claim preservation %q: got %v want %v", text, got, want)
		}
	}
}

type employeeClassifierFunc func(context.Context, string, json.RawMessage) (execution.ActionDescriptor, error)

func (f employeeClassifierFunc) DescribeAction(c context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
	return f(c, n, a)
}

func TestBotEmployeeResearchClassifierRejectsWrites(t *testing.T) {
	for _, effect := range []execution.ActionEffect{execution.EffectRead, execution.EffectReversibleWrite, execution.EffectSend, execution.EffectSchedule, execution.EffectUnknown} {
		c := botEmployeeReadClassifier{base: employeeClassifierFunc(func(_ context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
			return execution.ActionDescriptor{Tool: n, Arguments: a, Effect: effect, ConstraintsVerified: true}, nil
		})}
		_, err := c.DescribeAction(context.Background(), "web_fetch", json.RawMessage(`{}`))
		if (err == nil) != (effect == execution.EffectRead) {
			t.Fatalf("effect %s: %v", effect, err)
		}
	}
}

func TestBotEmployeeFormattingOneAssessment(t *testing.T) {
	for _, mode := range []string{"pass", "missing-content", "changed-fact"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Explain your limitation clearly"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			saved := &agentExecutionWorkStore{state: w}
			draft := w.Clone()
			draft.Phase = store.BotWorkFinished
			draft.AgentWork.Disposition = store.AgentWorkAnswered
			draft.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "I could not establish the market move."}
			draft.Candidate.Digest = draft.Candidate.ContentDigest()
			calls := 0
			llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					var in botWorkContentInput
					_ = json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in)
					assess := func(state *store.BotWorkState) botWorkContentReview {
						r := botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: in.Candidate.Digest}}
						for _, c := range in.Criteria {
							r.Criteria = append(r.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Honest specific limitation"})
						}
						for _, item := range botContentInput(state).Items {
							r.ContentAssessments = append(r.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Specific limitation, no factual claim"})
						}
						return r
					}
					formatted := in.BotWorkState.Clone()
					formatted.Candidate.Text = "**I could not establish the market move.**"
					if mode == "changed-fact" {
						formatted.Candidate.Text = "The market rose 8%."
					}
					fr := assess(formatted)
					if mode == "missing-content" {
						fr.ContentAssessments = nil
					}
					response := botEmployeeAssessment{botWorkContentReview: assess(in.BotWorkState), BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairFormatting}, FormattedText: formatted.Candidate.Text, FormattedReview: &fr}
					raw, _ := json.Marshal(response)
					yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
				}
			}}
			result := bot.RunResult{Status: store.RunSucceeded}
			next, err := reviewBotEmployeeResult(ctx, req, &BotChatResources{Work: saved}, llm, w, draft, &result)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("redundant model round: %d", calls)
			}
			if mode == "pass" {
				if !next.HasAcceptedEmployeeReview() || result.Report.Summary != "**I could not establish the market move.**" {
					t.Fatalf("not finalized: %+v", result)
				}
			} else if result.Status == store.RunSucceeded || next.HasAcceptedEmployeeReview() {
				t.Fatal("invalid formatting bypassed gate")
			}
		})
	}
}

func TestBotEmployeeGuidedResearchMissingBindingFailsClosed(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	feedback := BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch, MissingDimensions: []string{"market breadth"}, SourceNeeds: []string{"primary closing statistics"}, PublicQueries: []string{"market closing breadth"}}
	req.Mandate = &bot.MandateExecution{}
	req.Work = store.NewBotAgentWorkState(store.BotWorkContract{})
	req.Work.Candidate = &store.BotWorkCandidate{Text: "draft"}
	if _, _, err := botEmployeeResearchRequest(ctx, req, feedback); err == nil {
		t.Fatal("missing live guard accepted")
	}
	_ = agent.BotPolicyRuntime{}
	if strings.Contains(req.Instructions, "Continue only") {
		t.Fatal("original request mutated")
	}
}
func TestBotEmployeeGuidedResearchUncertainNoReplay(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Research market breadth"})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	saved := &agentExecutionWorkStore{state: w}
	draft := w.Clone()
	draft.Phase = store.BotWorkFinished
	draft.AgentWork.Disposition = store.AgentWorkPartial
	draft.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "uncertain", Text: "I could not establish market breadth."}
	draft.Candidate.Digest = draft.Candidate.ContentDigest()
	dispatches := 0
	ctx = WithBotEmployeeResearchRunner(ctx, func(context.Context, BotEmployeeResearchRequest) (BotEmployeeResearchResult, error) {
		dispatches++
		return BotEmployeeResearchResult{}, nil
	})
	llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			var in botWorkContentInput
			_ = json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in)
			r := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch, MissingDimensions: []string{"breadth"}, SourceNeeds: []string{"closing statistics"}, PublicQueries: []string{"market breadth"}}}
			r.CandidateDigest = in.Candidate.Digest
			for _, c := range in.Criteria {
				r.Criteria = append(r.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Representative coverage missing"})
			}
			for _, i := range in.Items {
				r.ContentAssessments = append(r.ContentAssessments, botWorkContentAssessment{ItemID: i.ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Honest limitation"})
			}
			raw, _ := json.Marshal(r)
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
		}
	}}
	result := bot.RunResult{Status: store.RunPartial, Report: store.BotReport{Unverified: []string{"exact action receipt uncertain"}}}
	next, err := reviewBotEmployeeResult(ctx, req, &BotChatResources{Work: saved}, llm, w, draft, &result)
	if err != nil {
		t.Fatal(err)
	}
	if dispatches != 0 || next.Repairs != 0 {
		t.Fatal("uncertain exact action triggered research")
	}
	productions := 0
	_, _, err = runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, next, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
		productions++
		return bot.RunResult{}, nil
	}, llm)
	if err == nil || productions != 0 {
		t.Fatal("uncertain exact action replayed producer")
	}
}

type employeeGuard struct{}

func (employeeGuard) Authorize(_ context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d, _ := a.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: d}, nil
}

func TestBotEmployeeResearchNarrowedFactoryHandoff(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch", "write_file", "bot_schedule_create"}})
	if err != nil {
		t.Fatal(err)
	}
	req.Policy = policy
	ctx = execution.WithRunPolicy(ctx, policy)
	ctx = execution.WithBotRunIdentity(ctx, policy.Identity())
	req.Mandate = &bot.MandateExecution{}
	req.Work = store.NewBotAgentWorkState(store.BotWorkContract{})
	req.Work.Candidate = &store.BotWorkCandidate{Text: "draft"}
	runtime := agent.BotPolicyRuntime{Guard: employeeGuard{}, Classifier: employeeClassifierFunc(func(_ context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
		return execution.ActionDescriptor{Tool: n, Arguments: a, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	})}
	ctx = agent.WithBotPolicyRuntime(ctx, runtime)
	f := BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch, MissingDimensions: []string{"breadth"}, SourceNeeds: []string{"closing statistics"}, PublicQueries: []string{"market breadth"}}
	child, got, err := botEmployeeResearchRequest(ctx, req, f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.Budget != req.Budget || got.Request.Mandate != req.Mandate || got.Request.Run.ID != req.Run.ID || got.Request.Policy == req.Policy || execution.RunPolicyFromContext(child) != got.Request.Policy {
		t.Fatal("run authority/budget handoff mismatch")
	}
	if !strings.Contains(got.Request.Instructions, "market breadth") || !strings.Contains(got.Request.Instructions, "closing statistics") {
		t.Fatal("directed research lost exact gaps")
	}
	for _, name := range []string{"perplexity_web_search", "web_fetch", "write_file", "bot_schedule_create"} {
		a := execution.ActionDescriptor{Tool: name, Arguments: json.RawMessage(`{}`), Effect: execution.EffectRead, ConstraintsVerified: true}
		d := execution.EvaluateRunPolicy(child, a, req.Now)
		if (d.Kind == execution.PolicyAllow) != (name == "perplexity_web_search" || name == "web_fetch") {
			t.Fatalf("tool %s: %+v", name, d)
		}
	}
}

func TestBotEmployeeRepairKindRejectsUnknown(t *testing.T) {
	if err := (botEmployeeRepairFeedback{Kind: "guess"}).validate(); err == nil {
		t.Fatal("unknown repair kind accepted")
	}
	if err := (botEmployeeRepairFeedback{Kind: botEmployeeRepairResearch}).validate(); err == nil {
		t.Fatal("undirected research accepted")
	}
}
