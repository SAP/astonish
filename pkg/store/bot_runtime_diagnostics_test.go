package store

import (
	"errors"
	"strings"
	"testing"
)

func TestBotWorkCriterionValidationDiagnostics(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*BotWorkState)
	}{
		{"kind", "criteria[0].verification_kind", func(w *BotWorkState) { w.Criteria[0].VerificationKind = "private-input" }},
		{"scope", "criteria[0].fulfillment_scope", func(w *BotWorkState) { w.Criteria[0].FulfillmentScope = "private-input" }},
		{"id", "criteria[0].id", func(w *BotWorkState) { w.Criteria[0].ID = "" }},
		{"duplicate", "criteria[1].id", func(w *BotWorkState) { w.Criteria = append(w.Criteria, w.Criteria[0]) }},
		{"text", "criteria[0].text", func(w *BotWorkState) { w.Criteria[0].Text = " " }},
		{"sources", "criteria[0].source_ids", func(w *BotWorkState) { w.Criteria[0].SourceIDs = nil }},
		{"unknown", "criteria[0].source_ids[0]", func(w *BotWorkState) { w.Criteria[0].SourceIDs = []string{"private-input"} }},
		{"reason", "criteria[0].exclusion_reason", func(w *BotWorkState) { w.Criteria[0].Excluded = true }},
		{"exclusion", "criteria[0].source_ids[0]", func(w *BotWorkState) { w.Criteria[0].Excluded = true; w.Criteria[0].ExclusionReason = "private-input" }},
		{"coverage", "source coverage", func(w *BotWorkState) { w.Criteria[0].SourceIDs = []string{"assignment"} }},
		{"empty", "at least one applicable criterion", func(w *BotWorkState) { w.Criteria = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewBotWorkState(attachmentWorkContract())
			w.Phase = BotWorkProducing
			w.Criteria = []BotWorkCriterion{{ID: "private-input", Text: "private-input", SourceIDs: []string{"assignment", "charter"}}}
			tc.change(w)
			err := w.Validate()
			if !errors.Is(err, ErrBotInvalid) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-input") {
				t.Fatalf("unsafe or imprecise error: %v; want %s", err, tc.want)
			}
		})
	}
}
