package store

import "testing"

func TestBotBlockedResponseProjectionDoesNotAliasPrivateReview(t *testing.T) {
	w := blockedWorkFixture()
	projection := w.VerifiedBlockedResponse()
	if projection == nil {
		t.Fatal("fixture is not verified")
	}
	projection.Assessment.CriterionIDs[0] = "forged"
	if w.Review.BlockedResponse.CriterionIDs[0] != "email" || w.Validate() != nil {
		t.Fatal("public projection mutated private review")
	}
	if err := ValidateBotWorkCompletion(w, BotRunCommit{Status: RunPartial, Report: BotReport{Summary: w.Candidate.Text, Quality: &BotQualityReport{ResponseOutcome: BotWorkResponseVerifiedBlocker, BlockedResponse: projection}}}); err == nil {
		t.Fatal("mutated projection accepted")
	}
}
