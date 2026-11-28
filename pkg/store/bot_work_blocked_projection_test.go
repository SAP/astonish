package store

import "testing"

func TestBotBlockedResponseProjectionRejectsUnboundState(t *testing.T) {
	for _, scenario := range []string{"text", "review-digest", "missing-review-criterion", "future-definition", "source-evidence", "observation"} {
		t.Run(scenario, func(t *testing.T) {
			w := blockedWorkFixture()
			switch scenario {
			case "text":
				w.Candidate.Text = "Email was sent"
			case "review-digest":
				w.Review.CandidateDigest = "another-candidate"
			case "missing-review-criterion":
				w.Review.Criteria = nil
			case "future-definition":
				w.Criteria[0].FulfillmentScope = BotWorkCriterionFutureTaskDefinition
			case "source-evidence":
				w.Criteria[0].VerificationKind = BotWorkVerificationSourceURL
			case "observation":
				w.Candidate.Observations[0].Source = ""
				w.Candidate.Digest = w.Candidate.ContentDigest()
				w.Review.CandidateDigest = w.Candidate.Digest
			}
			if w.VerifiedBlockedResponse() != nil {
				t.Fatal("unbound or inapplicable explanation projected")
			}
		})
	}
}
