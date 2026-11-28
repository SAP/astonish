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
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type workMemoryStore struct{ state *store.BotWorkState }

func (s *workMemoryStore) LoadWork(context.Context, store.BotLease) (*store.BotWorkState, error) {
	return s.state.Clone(), nil
}
func (s *workMemoryStore) SaveWork(_ context.Context, u store.BotWorkUpdate) (*store.BotWorkState, error) {
	if s.state.Revision != u.ExpectedRevision {
		return nil, store.ErrBotConflict
	}
	if err := store.ValidateBotWorkUpdate(s.state, &u.State); err != nil {
		return nil, err
	}
	s.state = u.State.Clone()
	s.state.Revision++
	return s.state.Clone(), nil
}

type workAssessmentModel struct {
	t        *testing.T
	calls    int
	fail     bool
	captured bool
}

func (*workAssessmentModel) Name() string { return "assessment" }
func (m *workAssessmentModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if len(r.Tools) != 0 {
			m.t.Error("reviewer obtained tools")
		}
		var out any
		if strings.HasPrefix(r.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_PREPARE_V1") {
			out = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "Use actual source content"}}}
		} else {
			var w store.BotWorkState
			if err := json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &w); err != nil {
				m.t.Fatal(err)
			}
			m.captured = w.Candidate.Bodies["tool-response:source"] == "actual source: correct"
			verdict := store.BotCriterionSatisfied
			if m.fail || w.Candidate.Text != "correct" {
				verdict = store.BotCriterionFailed
			}
			out = store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Use correct, not defective source content"}}}
		}
		raw, _ := json.Marshal(out)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(fixtureContentCoverage(r, raw)), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1, CandidatesTokenCount: 1, TotalTokenCount: 2}}, nil)
	}
}

func TestBotWorkControllerSafety(t *testing.T) {
	for _, mode := range []string{"repair", "exact-action", "uncertainty-no-replay", "source-capture", "no-review-budget", "unavailable-store", "interrupted-no-replay"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			ws := &workMemoryStore{state: req.Work.Clone()}
			resources := &BotChatResources{Work: ws}
			mdl := &workAssessmentModel{t: t}
			var llm model.LLM = mdl
			if mode == "no-review-budget" {
				limit := int64(3)
				budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				req.Budget = budget
				llm, err = provider.NewBudgetLLM(mdl, budget, provider.BudgetLLMOptions{MaxOutputTokens: 1, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 1, nil }})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unavailable-store" {
				resources.Work = nil
			}
			if mode == "interrupted-no-replay" {
				ws.state.Phase = store.BotWorkInterrupted
				ws.state.Unresolved = []string{"uncertain previous effect"}
			}
			calls := 0
			result, err := runBotWork(ctx, req, resources, llm, mode == "exact-action", func(q bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				calls++
				if calls > 1 && !strings.Contains(q.Instructions, "Use correct, not defective") {
					t.Error("specific finding lost")
				}
				c.FinalEventID = fmt.Sprintf("actual-final-%d", calls)
				c.reportBodies = map[string]string{"tool-response:source": "actual source: correct"}
				text := "defective"
				if calls > 1 || mode == "source-capture" {
					text = "correct"
				}
				report := store.BotReport{Summary: text}
				if mode == "uncertainty-no-replay" {
					report.Unverified = []string{"send outcome unknown"}
				}
				return bot.RunResult{Status: store.RunSucceeded, Report: report}, nil
			})
			if result.Report.EmployeeMessage == nil {
				t.Fatal("missing universal employee boundary")
			}
			if err := result.Report.ValidateEmployeeMessage(result.Status, ws.state); err != nil {
				t.Fatalf("unbound employee message: %v", err)
			}
			if mode == "repair" || mode == "source-capture" {
				if result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAssessed || result.Report.EmployeeMessage.Text != result.Report.Summary {
					t.Fatal("assessed text was rewritten")
				}
				if err != nil || result.Status != store.RunSucceeded || ws.state.Phase != store.BotWorkAccepted || !mdl.captured {
					t.Fatalf("missing accepted actual-content review: %+v %v", result, err)
				}
			} else if result.Status == store.RunSucceeded || ws.state.Phase == store.BotWorkAccepted {
				t.Fatalf("unsafe success: %+v %v", result, err)
			}
			if mode == "unavailable-store" || mode == "interrupted-no-replay" {
				if calls != 0 {
					t.Fatal("replayed unavailable/interrupted work")
				}
			} else if mode != "repair" && calls != 1 {
				t.Fatalf("unexpected replay %d", calls)
			}
			if mode == "no-review-budget" && (mdl.calls != 1 || !errors.Is(err, bot.ErrBudgetExceeded) || result.Report.Failure == nil) {
				t.Fatalf("budget escaped: calls=%d %+v", mdl.calls, result)
			}
			if mode == "uncertainty-no-replay" && !strings.Contains(fmt.Sprint(result.Report.Unverified), "send outcome unknown") {
				t.Fatal("erased uncertainty")
			}
		})
	}
}

func TestBotWorkModelBudgetBoundary(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	limit := int64(1)
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Budget = budget
	mdl := &workAssessmentModel{t: t}
	llm, err := provider.NewBudgetLLM(mdl, budget, provider.BudgetLLMOptions{MaxOutputTokens: 1, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 1, nil }})
	if err != nil {
		t.Fatal(err)
	}
	var output any
	var usage store.BotUsage
	err = botWorkJSON(ctx, req, llm, botWorkPreparePrompt, req.Work, &output, &usage)
	if !errors.Is(err, bot.ErrBudgetExceeded) || mdl.calls != 0 {
		t.Fatalf("assessment escaped shared meter: %v calls=%d", err, mdl.calls)
	}
	var _ execution.RunBudget = budget
}
