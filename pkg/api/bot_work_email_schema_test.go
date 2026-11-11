package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBotBlockedDependencyReviewSchema(t *testing.T) {
	schema := botWorkResponseSchema(botWorkBlockedReviewPrompt)
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"dependency_purpose", "dependency_verdict", "configure_delivery", "send", "save_preference", "unrelated", "source_quote"} {
		if !strings.Contains(string(raw), field) || !strings.Contains(botWorkBlockedReviewPrompt, field) {
			t.Fatalf("schema/prompt missing %s", field)
		}
	}
	assessment := schema["properties"].(map[string]any)["blocked_response"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
	required := assessment["required"].([]string)
	for _, field := range []string{"dependency_purpose", "dependency_verdict", "source_quote"} {
		found := false
		for _, got := range required {
			found = found || got == field
		}
		if !found {
			t.Fatalf("not required: %s", field)
		}
	}
}
