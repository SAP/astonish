package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestEmployeeEmailFallbackRejectsUnconfirmedDependency(t *testing.T) {
	for _, mode := range []string{"confirmed", "unrelated", "preference", "unverified", "absent", "stale"} {
		t.Run(mode, func(t *testing.T) {
			w := store.NewBotWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "can you update it to send via e-mail too ?", Charter: "Be truthful"})
			w.Phase = store.BotWorkBlocked
			w.Unresolved = []string{"Email not configured"}
			w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "Add email delivery", FulfillmentScope: store.BotWorkCriterionFutureTaskDefinition}}
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "answer", Text: "REJECTED: email sent", RuntimeBlockers: []store.BotWorkBlocker{store.NewBotOwnerEmailTransportBlocker("observation:0")}, Observations: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "runtime", ActorID: uuid.New(), ObservedAt: time.Now()}}}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			a := &store.BotBlockedResponseAssessment{BlockerID: "owner-email-transport", CriterionIDs: []string{"c1"}, SourceID: "assignment", SourceQuote: w.Contract.Instructions, DependencyPurpose: store.BotWorkDependencyConfigureDelivery, DependencyVerdict: store.BotCriterionSatisfied, Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "REVIEWER_ONLY: unsupported success"}
			w.Review = &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "Not configured"}}, BlockedResponse: a}
			switch mode {
			case "unrelated":
				a.DependencyPurpose = store.BotWorkDependencyUnrelated
				a.DependencyVerdict = store.BotCriterionFailed
			case "preference":
				a.DependencyPurpose = store.BotWorkDependencySavePreference
				a.DependencyVerdict = store.BotCriterionFailed
			case "unverified":
				a.DependencyVerdict = store.BotCriterionUnverified
			case "absent":
				w.Review.BlockedResponse = nil
			case "stale":
				w.Candidate.Text = "different"
			}
			if mode == "confirmed" {
				if err := w.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			report := store.BotReport{Summary: "Unverified result"}
			message := ComposeEmployeeMessage(context.Background(), store.RunPartial, report, nil, EmployeeMessageOptions{Work: w})
			if message.Origin != store.BotEmployeeMessageFallback || strings.Contains(message.Text, "REJECTED") || strings.Contains(message.Text, "REVIEWER_ONLY") {
				t.Fatal(message)
			}
			if strings.Contains(message.Text, "administrator") != (mode == "confirmed") {
				t.Fatalf("relevance=%s: %s", mode, message.Text)
			}
			report.EmployeeMessage = &message
			if err := report.ValidateEmployeeMessage(store.RunPartial, w); err != nil {
				t.Fatal(err)
			}
		})
	}
}
