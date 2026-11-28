package store

import (
	"errors"
	"strings"
	"testing"
)

func TestBotWorkReviewValidation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*BotWorkState)
	}{
		{"digest", "review.candidate_digest", func(w *BotWorkState) { w.Review.CandidateDigest = "private-secret" }},
		{"id", "criteria[0].criterion_id: must reference", func(w *BotWorkState) { w.Review.Criteria[0].CriterionID = "private-secret" }},
		{"excluded", "criterion_id: must reference", func(w *BotWorkState) { w.Criteria[0].Excluded = true }},
		{"duplicate", "criteria[1].criterion_id: must be unique", func(w *BotWorkState) { w.Review.Criteria = append(w.Review.Criteria, w.Review.Criteria[0]) }},
		{"missing", "must cover every applicable criterion", func(w *BotWorkState) { w.Review.Criteria = nil }},
		{"explanation", "criteria[0].explanation", func(w *BotWorkState) { w.Review.Criteria[0].Explanation = " " }},
		{"verdict", "criteria[0].verdict", func(w *BotWorkState) { w.Review.Criteria[0].Verdict = "private-secret" }},
		{"method", "criteria[0].method", func(w *BotWorkState) { w.Review.Criteria[0].Method = "private-secret" }},
		{"emptyrefs", "criteria[0].evidence_refs: required", func(w *BotWorkState) { w.Review.Criteria[0].EvidenceRefs = nil }},
		{"refs", "criteria[0].evidence_refs[0]", func(w *BotWorkState) { w.Review.Criteria[0].EvidenceRefs = []string{"private-secret"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &BotWorkState{Criteria: []BotWorkCriterion{{ID: "c1"}}, Candidate: &BotWorkCandidate{Digest: "current"}, Review: &BotWorkReview{CandidateDigest: "current", Criteria: []BotCriterionAssessment{{CriterionID: "c1", Verdict: BotCriterionSatisfied, Method: BotAssessmentModel, EvidenceRefs: []string{"candidate:current"}, Explanation: "private-secret"}}}}
			if err := w.validateReview(); err != nil {
				t.Fatal(err)
			}
			tc.mutate(w)
			err := w.validateReview()
			if !errors.Is(err, ErrBotInvalid) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("unsafe or imprecise error: %v", err)
			}
		})
	}
}
