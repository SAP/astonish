package launcher

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestMandatedResumeSchemaRequiresExplicitTargets(t *testing.T) {
	resolved, err := botMandatedProposalInputSchema().Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, targets := range []string{"", `,"resume_targets":null`, `,"resume_targets":{}`, `,"resume_targets":{"schedule_ids":["` + uuid.NewString() + `"]}`} {
		var args any
		if err := json.Unmarshal([]byte(`{"payload":{"operation":"resume_tasks","reason":"Resume weather"`+targets+`}}`), &args); err != nil {
			t.Fatal(err)
		}
		valid := targets != "" && targets != `,"resume_targets":null` && targets != `,"resume_targets":{}`
		if err := resolved.Validate(args); (err == nil) != valid {
			t.Fatalf("targets %q: %v", targets, err)
		}
	}
	// Constructing a mandated schema must not mutate the legacy schema.
	legacy, err := botProposalInputSchema().Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = legacy.Validate(map[string]any{"payload": map[string]any{"operation": string(store.BotControlResume), "reason": "Legacy lifecycle review"}}); err != nil {
		t.Fatal(err)
	}
}
