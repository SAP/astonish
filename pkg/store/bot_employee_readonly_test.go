package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestBotReadOnlyEmployeeCompletionBinding(t *testing.T) {
	old := NewBotAgentWorkState(BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Read the source"})
	old.EmployeeReviewPolicy = BotEmployeeReviewEvidenceBoundV1
	next := old.Clone()
	next.Phase = BotWorkFinished
	next.AgentWork.Disposition = AgentWorkAnswered
	next.Candidate = &BotWorkCandidate{Revision: 1, EventID: "final", Text: "Observed source", ReadOnlyResult: true}
	next.Candidate.Digest = next.Candidate.ContentDigest()
	unattested := *next.Candidate
	unattested.ReadOnlyResult = false
	if unattested.ContentDigest() == next.Candidate.Digest {
		t.Fatal("read-only attestation is not bound into candidate digest")
	}
	if err := ValidateBotWorkUpdate(old, next); err != nil {
		t.Fatal(err)
	}
	report := BotReport{Summary: next.Candidate.Text, EffectState: BotReportEffectsReadOnly, Work: BuildBotWorkProjection(next)}
	commit := BotRunCommit{Lease: BotLease{BotID: old.Contract.BotID, RunID: old.Contract.RunID}, Status: RunSucceeded, Report: report}
	if err := ValidateBotWorkCompletion(next, commit); err != nil {
		t.Fatal(err)
	}
	if next.HasAcceptedEmployeeReview() {
		t.Fatal("bypass fabricated review acceptance")
	}
	for _, mode := range []string{"unknown", "uncertain", "confirmed", "changed", "unverified", "artifact", "approval", "stale-digest", "pending-review", "review-attempt", "criteria", "pending-control", "historical-candidate", "foreign-lease", "summary-drift", "projection-drift"} {
		t.Run(mode, func(t *testing.T) {
			w := next.Clone()
			q := commit
			q.Report = report
			switch mode {
			case "unknown":
				q.Report.EffectState = BotReportEffectsUnknown
			case "uncertain":
				q.Report.EffectState = BotReportEffectsUncertain
			case "confirmed":
				q.Report.EffectState = BotReportEffectsConfirmed
			case "changed":
				q.Report.Changed = []BotEvidence{{Summary: "mutation"}}
			case "unverified":
				q.Report.Unverified = []string{"uncertain receipt"}
			case "artifact":
				q.Report.Artifacts = []BotArtifact{{}}
			case "approval":
				q.Report.NeedsApproval = []uuid.UUID{uuid.New()}
			case "stale-digest":
				w.Candidate.Text = "Other answer"
			case "pending-review":
				w.EmployeeReviewPending = true
			case "review-attempt":
				w.EmployeeReviewAttempts = 1
			case "criteria":
				w.Criteria = []BotWorkCriterion{{}}
			case "pending-control":
				w.Candidate.Bodies = map[string]string{"pending-control:0": "{}"}
				w.Candidate.Digest = w.Candidate.ContentDigest()
			case "historical-candidate":
				w.Candidate.ReadOnlyResult = false
				w.Candidate.Digest = w.Candidate.ContentDigest()
			case "foreign-lease":
				q.Lease.RunID = uuid.New()
			case "summary-drift":
				q.Report.Summary = "Different answer"
			case "projection-drift":
				q.Report.Work = nil
			}
			if err := ValidateBotWorkCompletion(w, q); err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
	// A candidate captured under the historical review contract cannot be relabeled.
	historical := old.Clone()
	historical.Candidate = next.Candidate
	historical.Candidate.ReadOnlyResult = false
	historical.Candidate.Digest = historical.Candidate.ContentDigest()
	relabeled := historical.Clone()
	relabeled.Phase = BotWorkFinished
	relabeled.AgentWork.Disposition = AgentWorkAnswered
	relabeled.Candidate.ReadOnlyResult = true
	relabeled.Candidate.Revision++
	relabeled.Candidate.Digest = relabeled.Candidate.ContentDigest()
	if err := ValidateBotWorkUpdate(historical, relabeled); err == nil {
		t.Fatal("relabelled persisted candidate")
	}
}
