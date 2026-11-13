package entstore

import (
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotBlockedExplanationCommitPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, initial, lease, now := botRunFixture(t, scope)
			pinLegacyWorkFixture(t, repo, worker, lease)
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Fixture ended"}}); err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(owner, store.BotRunRequest{BotID: initial.BotID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "email-blocker", Instructions: "Add email delivery"})
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := execution.BotRunIdentityFromContext(worker)
			identity.RunID = run.ID
			worker = execution.WithBotRunIdentity(owner, identity)
			lease, err = repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "worker", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			// The blocker assessment below is a historical legacy protocol record.
			pinLegacyWorkFixture(t, repo, worker, lease)
			w, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			save := func(next *store.BotWorkState) {
				t.Helper()
				updated, e := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *next})
				if e != nil {
					t.Fatal(e)
				}
				w = updated
			}
			next := w.Clone()
			next.Phase = store.BotWorkProducing
			next.Criteria = []store.BotWorkCriterion{{ID: "email", SourceIDs: []string{"assignment", "charter"}, Text: "Add email delivery"}}
			save(next)
			next = w.Clone()
			next.Phase = store.BotWorkCandidateReady
			text := "I couldn't add email delivery: transport is unavailable. An administrator must enable the email channel. Delivery settings were not changed."
			next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "employee-answer", Text: text, Observations: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "runtime", ActorID: uuid.MustParse(identity.ActorID), ObservedAt: *now}}, RuntimeBlockers: []store.BotWorkBlocker{store.NewBotOwnerEmailTransportBlocker("observation:0")}}
			next.Candidate.Digest = next.Candidate.ContentDigest()
			save(next)
			next = w.Clone()
			next.Phase = store.BotWorkReviewing
			save(next)
			next = w.Clone()
			next.Phase = store.BotWorkBlocked
			next.ResponseOutcome = store.BotWorkResponseVerifiedBlocker
			next.Unresolved = []string{"Email not added"}
			next.Review = &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "email", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "Email not added"}}, BlockedResponse: &store.BotBlockedResponseAssessment{BlockerID: "owner-email-transport", CriterionIDs: []string{"email"}, SourceID: "assignment", SourceQuote: "Add email delivery", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, Explanation: "Truthful useful blocker response"}}
			save(next)
			quality := &store.BotQualityReport{SchemaVersion: w.SchemaVersion, WorkRevision: w.Revision, RequirementsDigest: w.RequirementsDigest, CandidateDigest: w.Candidate.Digest, Outcome: w.Phase, Criteria: w.Review.Criteria, Repairs: w.Repairs, Limitations: w.Unresolved, ResponseOutcome: store.BotWorkResponseVerifiedBlocker, BlockedResponse: w.VerifiedBlockedResponse()}
			q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: text, Unverified: w.Unresolved, Quality: quality}}
			bad := q
			bad.Report.Summary = "Email sent"
			if err := repo.CommitRun(worker, bad); err == nil {
				t.Fatal("substituted explanation committed")
			}
			bad = q
			bad.Status = store.RunSucceeded
			if err := repo.CommitRun(worker, bad); err == nil {
				t.Fatal("blocker became success")
			}
			if err := repo.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != store.RunPartial || saved.Report.Summary != text || saved.Report.Quality.BlockedResponse.Text != text || saved.Report.Quality.Criteria[0].Verdict != store.BotCriterionFailed {
				t.Fatalf("durable blocker lost: %+v", saved)
			}
			reloaded, err := repo.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil || reloaded.Work.VerifiedBlockedResponse() == nil {
				t.Fatalf("work reload: %v", err)
			}
		})
	}
}
