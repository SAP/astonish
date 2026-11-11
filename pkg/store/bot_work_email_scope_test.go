package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func scopedEmailWork() *BotWorkState {
	w := blockedWorkFixture()
	w.Contract.Instructions = "can you update it to send via e-mail too ?"
	w.RequirementsDigest = botWorkDigest(w.Contract)
	w.Criteria[0].FulfillmentScope = BotWorkCriterionFutureTaskDefinition
	a := w.Review.BlockedResponse
	a.SourceQuote = w.Contract.Instructions
	a.DependencyPurpose = BotWorkDependencyConfigureDelivery
	a.DependencyVerdict = BotCriterionSatisfied
	return w
}

func TestBotEmailDependencyPurposeAndScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*BotWorkState)
		valid  bool
	}{
		{"configure-future-delivery-now", func(w *BotWorkState) {}, true},
		{"send-current", func(w *BotWorkState) {
			w.Criteria[0].FulfillmentScope = BotWorkCriterionCurrentTurn
			w.Review.BlockedResponse.DependencyPurpose = BotWorkDependencySend
		}, true},
		{"future-send-not-configuration", func(w *BotWorkState) { w.Review.BlockedResponse.DependencyPurpose = BotWorkDependencySend }, false},
		{"legacy-future-is-not-authority", func(w *BotWorkState) {
			w.Review.BlockedResponse.DependencyPurpose = ""
			w.Review.BlockedResponse.DependencyVerdict = ""
		}, false},
		{"save-preference-not-blocked", func(w *BotWorkState) {
			w.Contract.Instructions = "Save my e-mail preference for future reports; do not configure delivery now."
			w.RequirementsDigest = botWorkDigest(w.Contract)
			w.Review.BlockedResponse.SourceQuote = w.Contract.Instructions
			w.Review.BlockedResponse.DependencyPurpose = BotWorkDependencySavePreference
		}, false},
		{"email-metadata-not-blocked", func(w *BotWorkState) { w.Review.BlockedResponse.DependencyPurpose = BotWorkDependencyUnrelated }, false},
		{"unconfirmed-dependency", func(w *BotWorkState) { w.Review.BlockedResponse.DependencyVerdict = BotCriterionUnverified }, false},
		{"invented-action-quote", func(w *BotWorkState) { w.Review.BlockedResponse.SourceQuote = "Configure email now" }, false},
		{"goal-only", func(w *BotWorkState) { w.Criteria[0].SourceIDs = []string{"charter"} }, false},
		{"mechanical-field", func(w *BotWorkState) { w.Criteria[0].VerificationKind = BotWorkVerificationSourceURL }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := scopedEmailWork()
			tc.mutate(w)
			if got := w.Validate(); (got == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, got)
			}
			if w.HasVerifiedBlockedResponse() != tc.valid {
				t.Fatal("explanation gate disagrees")
			}
		})
	}
}

func TestBotEmailFallbackRequiresIndependentRelevance(t *testing.T) {
	w := scopedEmailWork()
	w.ResponseOutcome = ""
	w.Review.BlockedResponse.Verdict = BotCriterionFailed
	if w.Validate() != nil || w.HasVerifiedBlockedResponse() || w.ConfirmedRequiredBlocker() == nil {
		t.Fatal("relevance lost with rejected explanation")
	}
	report := BotReport{Summary: "Unverified", Quality: &BotQualityReport{Outcome: BotWorkBlocked}}
	m := NewBotEmployeeFallback(RunPartial, report, BotEmployeeReasonEmailTransport)
	report.EmployeeMessage = &m
	if !strings.Contains(m.Text, "administrator") || !strings.Contains(m.Text, "transport is unavailable") {
		t.Fatal(m.Text)
	}
	if err := report.ValidateEmployeeMessage(RunPartial, w); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	var restored BotReport
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.ValidateEmployeeMessageIntegrity(RunPartial); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"no-work", "no-review", "unconfirmed", "save-preference", "unrelated", "legacy", "stale-digest", "wrong-quote", "no-observation"} {
		t.Run(mode, func(t *testing.T) {
			next := w.Clone()
			switch mode {
			case "no-work":
				next = nil
			case "no-review":
				next.Review = nil
			case "unconfirmed":
				next.Review.BlockedResponse.DependencyVerdict = BotCriterionUnverified
			case "save-preference":
				next.Review.BlockedResponse.DependencyPurpose = BotWorkDependencySavePreference
				next.Review.BlockedResponse.DependencyVerdict = BotCriterionFailed
			case "unrelated":
				next.Review.BlockedResponse.DependencyPurpose = BotWorkDependencyUnrelated
				next.Review.BlockedResponse.DependencyVerdict = BotCriterionFailed
			case "legacy":
				next.Review.BlockedResponse.DependencyPurpose = ""
				next.Review.BlockedResponse.DependencyVerdict = ""
			case "stale-digest":
				next.Candidate.Text = "Email sent"
			case "wrong-quote":
				next.Review.BlockedResponse.SourceQuote = "Email sent"
			case "no-observation":
				next.Candidate.Observations = nil
			}
			if next.ConfirmedRequiredBlocker() != nil || report.ValidateEmployeeMessage(RunPartial, next) == nil {
				t.Fatal("unconfirmed relevance authorized fallback")
			}
		})
	}
}
