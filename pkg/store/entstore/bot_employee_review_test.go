package entstore

import (
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// Store-only fixtures explicitly supply the assessment that the API model performs.
// Never use this helper in rejection tests: those must exercise the missing gate.
func acceptEmployeeReviewFixture(w *store.BotWorkState) {
	if w.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 || w.Candidate == nil {
		return
	}
	sources := []string{"assignment", "charter"}
	for _, goal := range w.Contract.Goals {
		sources = append(sources, "goal:"+goal.ID.String())
	}
	for _, attachment := range w.Contract.Attachments {
		sources = append(sources, attachment.SourceID)
	}
	w.Criteria = []store.BotWorkCriterion{{ID: "employee-assignment", SourceIDs: sources, Text: "Assess the complete original assignment and all pinned sources"}}
	w.Review = &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "employee-assignment", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Deterministic store fixture assessment of the complete candidate"}}}
}

func TestBotEmployeeReviewPendingReservationReload(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, _, lease, _ := botRunFixture(t, scope)
			w, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			next := w.Clone()
			next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "reserved-review", Text: "Private draft"}
			next.Candidate.Digest = next.Candidate.ContentDigest()
			w, err = repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *next})
			if err != nil {
				t.Fatal(err)
			}
			next = w.Clone()
			next.EmployeeReviewAttempts = 1
			next.EmployeeReviewPending = true
			_, err = repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *next})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := repo.LoadWork(worker, lease)
			if err != nil || !loaded.EmployeeReviewPending || loaded.EmployeeReviewAttempts != 1 {
				t.Fatalf("reservation lost: %+v %v", loaded, err)
			}
			retry := loaded.Clone()
			retry.EmployeeReviewAttempts++
			if _, err := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: loaded.Revision, State: *retry}); err == nil {
				t.Fatal("uncertain attempt replayed")
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: loaded.Candidate.Text}}); err == nil {
				t.Fatal("pending review published")
			}
		})
	}
}

func TestBotEmployeeReviewCommitRejectsStaleDigest(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, _, lease, _ := botRunFixture(t, scope)
			w, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if w.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 {
				t.Fatal("new run did not pin review policy")
			}
			next := w.Clone()
			next.Phase = store.BotWorkWorking
			next.AgentWork.Disposition = store.AgentWorkAnswered
			next.Criteria = []store.BotWorkCriterion{{ID: "assignment", SourceIDs: []string{"assignment", "charter"}, Text: "Answer the complete original assignment"}}
			next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "candidate", Text: "Inspected answer"}
			next.Candidate.Digest = next.Candidate.ContentDigest()
			w, err = repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *next})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: w.Candidate.Text, Work: store.BuildBotWorkProjection(w)}}
			if err := repo.CommitRun(worker, q); err == nil {
				t.Fatal("unreviewed candidate committed through partial status")
			}
			next = w.Clone()
			next.Phase = store.BotWorkFinished
			next.Review = &store.BotWorkReview{CandidateDigest: next.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "assignment", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + next.Candidate.Digest}, Explanation: "Complete assignment assessed"}}}
			stale := next.Clone()
			stale.Candidate.Text = "Substituted unreviewed answer"
			stale.Candidate.Digest = stale.Candidate.ContentDigest()
			if _, err := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *stale}); err == nil {
				t.Fatal("stale review accepted after bytes changed")
			}
			w, err = repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *next})
			if err != nil {
				t.Fatal(err)
			}
			q.Status = store.RunSucceeded
			q.Report.Work = store.BuildBotWorkProjection(w)
			forged := q
			forged.Report.Summary = "Other candidate"
			if err := repo.CommitRun(worker, forged); err == nil {
				t.Fatal("substituted candidate committed")
			}
			if err := repo.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
		})
	}
}
