package api

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

// notificationReviewedReport models the worker's fenced assessment transitions;
// notification transport tests do not run a model or alter acceptance storage.
func notificationReviewedReport(t *testing.T, ctx context.Context, repo store.BotStore, lease store.BotLease, report store.BotReport) store.BotReport {
	t.Helper()
	ws := repo.(store.BotWorkStore)
	w, err := ws.LoadWork(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	save := func(n *store.BotWorkState) {
		t.Helper()
		w, err = ws.SaveWork(ctx, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *n})
		if err != nil {
			t.Fatal(err)
		}
	}
	n := w.Clone()
	n.Phase = store.BotWorkProducing
	sources := []string{"assignment", "charter"}
	for _, g := range w.Contract.Goals {
		sources = append(sources, "goal:"+g.ID.String())
	}
	n.Criteria = []store.BotWorkCriterion{{ID: "notification", SourceIDs: sources, Text: "Report the controlled observation for notification delivery"}}
	save(n)
	n = w.Clone()
	n.Phase = store.BotWorkCandidateReady
	n.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: report.Summary}
	n.Candidate.Digest = n.Candidate.ContentDigest()
	save(n)
	n = w.Clone()
	n.Phase = store.BotWorkReviewing
	save(n)
	n = w.Clone()
	n.Phase = store.BotWorkAccepted
	n.Review = &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "notification", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Controlled observation fixture"}}}
	save(n)
	report.Quality = &store.BotQualityReport{SchemaVersion: w.SchemaVersion, WorkRevision: w.Revision, RequirementsDigest: w.RequirementsDigest, CandidateDigest: w.Candidate.Digest, Outcome: w.Phase, Criteria: w.Review.Criteria}
	return report
}
