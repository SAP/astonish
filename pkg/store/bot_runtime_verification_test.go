package store

import "testing"

func TestBotWorkVerification(t *testing.T) {
	for _, kind := range []BotWorkVerificationKind{"", BotWorkVerificationModel, BotWorkVerificationSourceURL, BotWorkVerificationRetrievalTime, "unknown"} {
		w := NewBotWorkState(attachmentWorkContract())
		w.Phase = BotWorkProducing
		w.Criteria = []BotWorkCriterion{{ID: "c", SourceIDs: []string{"assignment", "charter"}, Text: "Requirement", VerificationKind: kind}}
		if (w.Validate() == nil) != (kind != "unknown") {
			t.Fatalf("kind validation %q", kind)
		}
		if kind == "unknown" {
			continue
		}
		next := w.Clone()
		next.Phase = BotWorkBlocked
		next.Unresolved = []string{"blocked"}
		next.Criteria[0].VerificationKind = BotWorkVerificationSourceURL
		if kind == BotWorkVerificationSourceURL {
			next.Criteria[0].VerificationKind = BotWorkVerificationModel
		}
		if ValidateBotWorkUpdate(w, next) == nil {
			t.Fatal("verification policy mutated after extraction")
		}
	}
	if (BotWorkCriterion{}).EffectiveVerificationKind() != BotWorkVerificationModel {
		t.Fatal("legacy default changed")
	}
}
