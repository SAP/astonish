package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
)

func TestBotWorkTemporalScopeSchema(t *testing.T) {
	s := botWorkResponseSchema(botWorkPreparePrompt)
	criterion := s["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)
	scope := criterion["properties"].(map[string]any)["fulfillment_scope"].(map[string]any)
	values := scope["enum"].([]string)
	if len(values) != 2 || values[0] != string(store.BotWorkCriterionCurrentTurn) || values[1] != string(store.BotWorkCriterionFutureTaskDefinition) {
		t.Fatal(values)
	}
	var legacy store.BotWorkCriterion
	if err := json.Unmarshal([]byte(`{"id":"legacy","source_ids":["assignment"],"text":"Deliver report"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.EffectiveFulfillmentScope() != store.BotWorkCriterionCurrentTurn {
		t.Fatal("legacy scope changed")
	}
}

func TestBotWorkTemporalScopeGuidance(t *testing.T) {
	for name, prompt := range map[string]string{"prepare": botWorkPreparePrompt, "review": botWorkReviewPrompt, "pending": botWorkPendingReviewPrompt} {
		for _, required := range []string{"fulfillment_scope", "original", "scheduled", "actual"} {
			if !strings.Contains(strings.ToLower(prompt), required) {
				t.Errorf("%s missing %s", name, required)
			}
		}
	}
	for _, required := range []string{"exact persisted", "never pass a missing actually-requested report", "not fetching weather or producing a report NOW", "deferred_application", "No claimed applied result"} {
		if !strings.Contains(botWorkPendingReviewPrompt, required) {
			t.Errorf("missing temporal safety guidance %q", required)
		}
	}
}
