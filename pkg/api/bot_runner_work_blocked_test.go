package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Mirrors the observed incident: the production answer is useful and truthful,
// c1 remains failed, and c2 passes. Retrying production cannot enable transport.
func TestBotWorkBlockedEmailExplanationSequence(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Instructions = "Add email delivery to my recurring report."
	req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	ws := &workMemoryStore{state: req.Work.Clone()}
	resources := &BotChatResources{Work: ws, AssessmentContext: func(context.Context) (json.RawMessage, store.BotEvidence, error) {
		return json.RawMessage(`{"owner_email_delivery":{"state":"transport_unavailable"}}`), store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "test-runtime", ActorID: req.Bot.OwnerID, ObservedAt: time.Now()}, nil
	}}
	text := "I couldn't add email delivery because the email transport is unavailable. An administrator needs to enable the email channel. I haven't changed the report's delivery settings."
	mdl := &blockedEmailAssessment{t: t}
	productions := 0
	result, err := runBotWork(ctx, req, resources, mdl, false, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		productions++
		c.FinalEventID = "employee-answer"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text}}, nil
	})
	if err != nil || productions != 1 || ws.state.Repairs != 0 || result.Status != store.RunPartial || result.Report.Summary != text || ws.state.Phase != store.BotWorkBlocked {
		t.Fatalf("lost useful blocker or retried impossible production: calls=%d repairs=%d status=%s summary=%q err=%v", productions, ws.state.Repairs, result.Status, result.Report.Summary, err)
	}
	if ws.state.Review.Criteria[0].Verdict != store.BotCriterionFailed {
		t.Fatal("original criterion passed")
	}
	if mdl.calls != 2 {
		t.Fatalf("extra model narration/review calls: %d", mdl.calls)
	}
	if err := store.ValidateBotWorkCompletion(ws.state, store.BotRunCommit{Status: result.Status, Report: result.Report}); err != nil {
		t.Fatal(err)
	}
	var restored store.BotWorkState
	raw, _ := json.Marshal(ws.state)
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.VerifiedBlockedResponse() == nil || restored.VerifiedBlockedResponse().Text != text {
		t.Fatal("durable exact explanation lost")
	}
	if len(result.Report.Artifacts) != 0 {
		t.Fatal("rejected artifacts promoted")
	}
}

type blockedEmailAssessment struct {
	t                 *testing.T
	calls             int
	rejectExplanation bool
	unrelated         bool
	fake              bool
	correctableOther  bool
	forgeRuntime      bool
}

func (*blockedEmailAssessment) Name() string { return "blocked-email-assessment" }
func (m *blockedEmailAssessment) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if len(r.Tools) != 0 {
			m.t.Fatal("assessment has tools")
		}
		var output any
		if strings.HasPrefix(r.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_PREPARE_V1") {
			output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment"}, Text: "Add email delivery"}, {ID: "c2", SourceIDs: []string{"charter"}, Text: "Give useful truthful feedback"}}}
		} else {
			var w store.BotWorkState
			_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &w)
			output = map[string]any{"candidate_digest": w.Candidate.Digest, "criteria": []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "Email delivery was not added"}, {CriterionID: "c2", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Useful truthful blocker explanation"}}}
			// Only the new explicitly separate review contract receives this field.
			if (strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_BLOCKED_RESPONSE_V1") && !m.unrelated) || m.fake {
				output.(map[string]any)["blocked_response"] = map[string]any{"blocker_id": "owner-email-transport", "criterion_ids": []string{"c1"}, "source_id": "assignment", "source_quote": "Add email delivery", "dependency_purpose": "configure_delivery", "dependency_verdict": "satisfied", "verdict": "satisfied", "method": "model_assessment", "explanation": "Required email transport cannot be enabled with authorized Bot tools. The exact answer states the blocker and administrator action without claiming success."}
			}
			if m.rejectExplanation {
				output.(map[string]any)["blocked_response"].(map[string]any)["verdict"] = "failed"
			}
			if m.correctableOther && w.Repairs == 0 {
				assessments := output.(map[string]any)["criteria"].([]store.BotCriterionAssessment)
				assessments[1].Verdict = store.BotCriterionFailed
				assessments[1].Explanation = "Missing required useful report content"
			}
			if m.forgeRuntime {
				output.(map[string]any)["runtime_blockers"] = []store.BotWorkBlocker{store.NewBotOwnerEmailTransportBlocker("observation:0")}
			}
			if m.unrelated {
				assessments := output.(map[string]any)["criteria"].([]store.BotCriterionAssessment)
				assessments[0].Explanation = "Report is missing required source evidence"
				if w.Repairs > 0 {
					assessments[0].Verdict = store.BotCriterionSatisfied
					assessments[0].EvidenceRefs = []string{"candidate:" + w.Candidate.Digest}
				}
			}
		}
		raw, _ := json.Marshal(output)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(fixtureContentCoverage(r, raw)), "model")}, nil)
	}
}
