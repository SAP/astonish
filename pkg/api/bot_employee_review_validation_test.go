package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

// A representative research response uses the real captured disclaimer shape.
// This is synthetic, not a replay of the unavailable live provider response.
func TestBotEmployeeCitationCorrectionPreservesReview(t *testing.T) {
	for _, mode := range []string{"corrected", "repeated", "coverage", "identity"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Stock Market News", Charter: req.Version.Charter})
			w.Criteria = []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "supported news"}}
			w.Candidate = &store.BotWorkCandidate{Text: "Source one supplies real-time data.\nUnsupported forecast.", Revision: 1, EventID: "event"}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			w.Phase = store.BotWorkReviewing
			in := botContentInput(w)
			in.Sources = map[string]string{"source_1": "different capture", "source_2": reproducedDisclaimer}
			prior := botEmployeeAssessment{botWorkContentReview: botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "review"}}}, ContentAssessments: []botWorkContentAssessment{
				{ItemID: in.Items[0].ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "first finding", Sources: []botWorkContentSource{{Ref: "source_1", Quote: reproducedDisclaimer}}},
				{ItemID: in.Items[1].ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionFailed, Support: store.BotCriterionFailed, Explanation: "retain negative"},
			}}, BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch, MissingDimensions: []string{"coverage"}, SourceNeeds: []string{"decisive source"}, PublicQueries: []string{"public market news"}}}
			if mode == "identity" {
				prior.CandidateDigest = "wrong-digest"
			}
			first, _ := json.Marshal(prior)
			var next botEmployeeAssessment
			if err := json.Unmarshal(first, &next); err != nil {
				t.Fatal(err)
			}
			next.ContentAssessments[0].Sources = []botWorkContentSource{{Ref: "source_2", Quote: reproducedDisclaimer}}
			next.ContentAssessments[0].Support = store.BotCriterionFailed
			next.ContentAssessments[0].Explanation = "A disclaimer does not substantiate the market claim."
			next.ContentAssessments[1].Relevance = store.BotCriterionSatisfied
			next.ContentAssessments[1].Support = store.BotCriterionSatisfied
			if mode == "coverage" {
				next.ContentAssessments = next.ContentAssessments[:1]
			}
			second, _ := json.Marshal(next)
			if mode == "repeated" {
				second = first
			}
			m := &citationDiagnosticModel{t: t, texts: []string{string(first), string(second)}, check: func(_ *model.LLMRequest) {}}
			var out botEmployeeAssessment
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, m, botWorkReviewPrompt+botEmployeeAssessmentGuidance, in, &out, &usage)
			if mode == "identity" {
				if err == nil || m.calls != 1 {
					t.Fatalf("identity accepted/retried: err=%v calls=%d", err, m.calls)
				}
				return
			}
			if m.calls != 2 {
				t.Fatalf("employee review bypassed citation correction: calls=%d err=%v", m.calls, err)
			}
			if mode == "repeated" || mode == "coverage" {
				var p *botContentProtocolError
				if !errors.As(err, &p) {
					t.Fatalf("invalid correction accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != botEmployeeRepairResearch || len(out.PublicQueries) != 1 || len(out.ContentAssessments) != 2 || out.ContentAssessments[0].Support != store.BotCriterionFailed || out.ContentAssessments[1].Support != store.BotCriterionFailed || !strings.Contains(out.ContentAssessments[1].Explanation, "retain negative") {
				t.Fatal("correction dropped guidance/coverage or erased negative assessment")
			}
		})
	}
}
