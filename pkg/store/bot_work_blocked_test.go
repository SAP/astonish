package store

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
)

func blockedWorkFixture() *BotWorkState {
	w := NewBotWorkState(BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Add email delivery", Charter: "Be truthful"})
	w.Phase = BotWorkBlocked
	w.ResponseOutcome = BotWorkResponseVerifiedBlocker
	w.Criteria = []BotWorkCriterion{{ID: "email", SourceIDs: []string{"assignment", "charter"}, Text: "Add email delivery"}}
	w.Candidate = &BotWorkCandidate{Revision: 1, EventID: "answer", Text: "Email delivery could not be added. Transport is unavailable; an administrator must enable the email channel. No delivery configuration was changed.", Observations: []BotEvidence{{Kind: BotEvidenceObservation, Source: "runtime", ActorID: uuid.New(), ObservedAt: time.Now()}}, RuntimeBlockers: []BotWorkBlocker{NewBotOwnerEmailTransportBlocker("observation:0")}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.Review = &BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []BotCriterionAssessment{{CriterionID: "email", Verdict: BotCriterionFailed, Method: BotAssessmentModel, Explanation: "Email not added"}}, BlockedResponse: &BotBlockedResponseAssessment{BlockerID: "owner-email-transport", CriterionIDs: []string{"email"}, SourceID: "assignment", SourceQuote: "Add email delivery", Verdict: BotCriterionSatisfied, Method: BotAssessmentModel, Explanation: "Useful truthful explanation, with required next action"}}
	w.Unresolved = []string{"Email not added"}
	return w
}

func TestBotBlockedWorkValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*BotWorkState)
	}{
		{"invented-blocker", func(w *BotWorkState) { w.Candidate.RuntimeBlockers[0].Kind = "invented" }},
		{"invented-action", func(w *BotWorkState) { w.Candidate.RuntimeBlockers[0].RequiredAction = "Give me mailbox credentials" }},
		{"missing-observation", func(w *BotWorkState) { w.Candidate.RuntimeBlockers[0].ObservationRef = "observation:7" }},
		{"no-runtime-evidence", func(w *BotWorkState) { w.Candidate.RuntimeBlockers = nil }},
		{"invented-quote", func(w *BotWorkState) { w.Review.BlockedResponse.SourceQuote = "Email all customers" }},
		{"unrelated-news", func(w *BotWorkState) {
			w.Contract.Instructions = "Give news"
			w.RequirementsDigest = botWorkDigest(w.Contract)
		}},
		{"success-verdict", func(w *BotWorkState) {
			w.Review.Criteria[0].Verdict = BotCriterionSatisfied
			w.Review.Criteria[0].EvidenceRefs = []string{"candidate:" + w.Candidate.Digest}
		}},
		{"task-success", func(w *BotWorkState) { w.Phase = BotWorkAccepted }},
		{"changed-candidate", func(w *BotWorkState) { w.Candidate.Text = "Email sent" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := blockedWorkFixture()
			if err := w.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.mutate(w)
			if w.Validate() == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
	w := blockedWorkFixture()
	raw, _ := json.Marshal(w)
	var restored BotWorkState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
	if restored.VerifiedBlockedResponse().Text != w.Candidate.Text {
		t.Fatal("exact text not durable")
	}
	// A correctable second failure must not be hidden by the real email blocker.
	w.Criteria = append(w.Criteria, BotWorkCriterion{ID: "format", Text: "Markdown table", SourceIDs: []string{"assignment"}})
	w.Review.Criteria = append(w.Review.Criteria, BotCriterionAssessment{CriterionID: "format", Verdict: BotCriterionFailed, Method: BotAssessmentModel, Explanation: "Missing table"})
	if w.HasVerifiedBlockedResponse() {
		t.Fatal("correctable failure waived")
	}
}

func TestBotBlockedWorkCompletionExactBinding(t *testing.T) {
	for _, mode := range []string{"valid", "summary", "digest", "success", "artifact", "projection", "omit-quality"} {
		t.Run(mode, func(t *testing.T) {
			w := blockedWorkFixture()
			q := BotRunCommit{Status: RunPartial, Report: BotReport{Summary: w.Candidate.Text, Quality: &BotQualityReport{ResponseOutcome: BotWorkResponseVerifiedBlocker, BlockedResponse: w.VerifiedBlockedResponse()}}}
			switch mode {
			case "summary":
				q.Report.Summary = "Email sent"
			case "digest":
				q.Report.Quality.BlockedResponse.CandidateDigest = "other"
			case "success":
				q.Status = RunSucceeded
			case "artifact":
				q.Report.Artifacts = []BotArtifact{{ID: uuid.New()}}
			case "projection":
				q.Report.Quality.BlockedResponse.Assessment.Explanation = "substituted"
			case "omit-quality":
				q.Report.Quality = nil
			}
			err := ValidateBotWorkCompletion(w, q)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("binding %s: %v", mode, err)
			}
		})
	}
}
