package store

import (
	"encoding/json"
	"testing"
)

func TestBotWorkCriterionScope(t *testing.T) {
	for _, scope := range []BotWorkCriterionScope{"", BotWorkCriterionCurrentTurn, BotWorkCriterionFutureTaskDefinition, "invalid"} {
		w := NewBotWorkState(attachmentWorkContract())
		w.Phase = BotWorkProducing
		w.Criteria = []BotWorkCriterion{{ID: "c", SourceIDs: []string{"assignment", "charter"}, Text: "Retain original standard", FulfillmentScope: scope}}
		if (w.Validate() == nil) != (scope != "invalid") {
			t.Fatalf("validation scope=%q", scope)
		}
		raw, _ := json.Marshal(w)
		var copy BotWorkState
		if err := json.Unmarshal(raw, &copy); err != nil {
			t.Fatal(err)
		}
		if copy.Criteria[0].FulfillmentScope != scope || copy.RequirementsDigest != w.RequirementsDigest {
			t.Fatal("scope or source digest lost")
		}
		if scope != "invalid" {
			next := w.Clone()
			next.Criteria[0].FulfillmentScope = BotWorkCriterionFutureTaskDefinition
			if scope == BotWorkCriterionFutureTaskDefinition {
				next.Criteria[0].FulfillmentScope = BotWorkCriterionCurrentTurn
			}
			if ValidateBotWorkUpdate(w, next) == nil {
				t.Fatal("scope changed after extraction")
			}
		}
	}
	if (BotWorkCriterion{}).EffectiveFulfillmentScope() != BotWorkCriterionCurrentTurn {
		t.Fatal("legacy default changed")
	}
}
