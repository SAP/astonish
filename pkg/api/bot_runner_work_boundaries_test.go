package api

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotWorkCapturedIdentityAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"exact-event", "uncertainty-despite-pass", "producer-error-no-drafts", "candidate-size-limit", "missing-event"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			ws := &workMemoryStore{state: req.Work.Clone()}
			mdl := &workAssessmentModel{t: t}
			calls := 0
			result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, mdl, false, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				calls++
				cr := &ChatRunner{ctx: ctx}
				cr.observeCompletion(&session.Event{ID: "observed-final-event", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("correct", "model")}})
				*c = cr.Completion()
				r := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "correct"}, Usage: store.BotUsage{InputTokens: 7, OutputTokens: 3, ToolCalls: 1}}
				switch mode {
				case "uncertainty-despite-pass":
					r.Report.Unverified = []string{"send outcome unknown"}
				case "producer-error-no-drafts":
					r.Status = store.RunFailed
					r.Report.Artifacts = []store.BotArtifact{{ID: uuid.New(), HomeID: req.Bot.Home.ID, RelativePath: "draft.md"}}
					return r, errors.New("producer failed")
				case "candidate-size-limit":
					c.reportBodies = map[string]string{"tool-response:large": strings.Repeat("x", store.BotWorkMaxCandidateBytes)}
				case "missing-event":
					c.FinalEventID = ""
				}
				return r, nil
			})
			if calls != 1 {
				t.Fatalf("replayed %d times", calls)
			}
			if result.Usage.InputTokens < 8 || result.Usage.ToolCalls != 1 {
				t.Fatalf("lost accounting: %+v", result.Usage)
			}
			if mode == "exact-event" {
				if err != nil || result.Status != store.RunSucceeded || ws.state.Candidate.EventID != "observed-final-event" {
					t.Fatalf("not bound to actual event: %+v %v", result, err)
				}
			} else if result.Status == store.RunSucceeded || ws.state.Phase == store.BotWorkAccepted || len(result.Report.Artifacts) != 0 {
				t.Fatalf("unsafe publication: %+v %v", result, err)
			}
			if mode == "uncertainty-despite-pass" && !strings.Contains(fmt.Sprint(result.Report.Unverified), "send outcome unknown") {
				t.Fatal("lost uncertainty")
			}
		})
	}
}

func TestBotWorkResumedRepairKeepsFindings(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	w.Phase = store.BotWorkRepairing
	w.Repairs = 1
	w.Criteria = []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "Use actual source content"}}
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "prior-final", Text: "defective", Bodies: map[string]string{"tool-response:source": "actual source: correct"}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.Unresolved = []string{"Persisted specific correction"}
	req.Work = w.Clone()
	ws := &workMemoryStore{state: w}
	mdl := &workAssessmentModel{t: t}
	result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, mdl, false, func(q bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		if !strings.Contains(q.Instructions, "Persisted specific correction") {
			t.Fatal("resumed repair lost persisted findings")
		}
		c.FinalEventID = "resumed-final"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "correct"}}, nil
	})
	if err != nil || result.Status != store.RunSucceeded || !mdl.captured || ws.state.Repairs != 1 {
		t.Fatalf("resume lost evidence/bound: %+v %v", result, err)
	}
}

type workErrorResponseModel struct{}

func (workErrorResponseModel) Name() string { return "error-response" }
func (workErrorResponseModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{ErrorCode: "provider_error", ErrorMessage: "failed after usage", Content: genai.NewContentFromText(`{"criteria":[]}`, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 4, CandidatesTokenCount: 2}}, nil)
	}
}
func TestBotWorkAssessmentErrorAccounting(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	var output any
	var usage store.BotUsage
	err := botWorkJSON(ctx, req, workErrorResponseModel{}, botWorkPreparePrompt, req.Work, &output, &usage)
	if err == nil || usage.InputTokens != 4 || usage.OutputTokens != 2 {
		t.Fatalf("error or accounting lost: %v %+v", err, usage)
	}
}
