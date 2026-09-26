package entstore

import (
	"context"
	"testing"

	"github.com/SAP/astonish/ent/team/fleetplan"
	"github.com/SAP/astonish/pkg/fleet"
)

// A minimal but valid plan body: FleetConfig.Validate requires a name, at least
// one agent, and each agent needs identity + behaviors + a tool selection.
func validPlanAgents() map[string]fleet.FleetAgentConfig {
	return map[string]fleet.FleetAgentConfig{
		"a": {
			Name:       "A",
			Identity:   "i",
			Behaviors:  "b",
			Tools:      fleet.ToolsConfig{All: true},
			TaskPolicy: &fleet.AgentTaskPolicy{Claims: []string{"general"}},
		},
	}
}

// TestFleetPlanStore_SaveReconcilesNameIntoBody verifies that Save writes the
// resolved name into the definition body (falling back to the key when the body
// carries no name), so a later GetPlan sees a non-empty fleet name.
func TestFleetPlanStore_SaveReconcilesNameIntoBody(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetPlanStore{client: client}

	// Plan whose top-level Name is empty but key is set — mimics the frontend
	// state that never received a name from the read path.
	plan := &fleet.FleetPlan{
		Key: "nameless-plan",
		FleetConfig: fleet.FleetConfig{
			Agents: validPlanAgents(),
		},
	}
	if err := s.Save(ctx, plan); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The persisted definition body must carry a non-empty name (backfilled from key).
	row, err := client.FleetPlan.Query().Where(fleetplan.KeyEQ("nameless-plan")).Only(ctx)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if got, _ := row.Definition["name"].(string); got == "" {
		t.Fatalf("stored definition body name is empty; want non-empty (from key)")
	}

	// And GetPlan must return a plan that passes Validate (no "fleet name is required").
	got, ok := s.GetPlan(ctx, "nameless-plan")
	if !ok {
		t.Fatal("GetPlan: not found")
	}
	fp, ok := got.(*fleet.FleetPlan)
	if !ok {
		t.Fatalf("GetPlan type = %T, want *fleet.FleetPlan", got)
	}
	if fp.Name == "" {
		t.Fatal("GetPlan returned empty Name")
	}
	if err := fp.Validate(); err != nil {
		t.Fatalf("returned plan failed Validate: %v", err)
	}
}

// TestFleetPlanStore_GetPlanHealsLegacyRow verifies that a legacy row — where the
// display name lives only in the `name` column and the definition body has no
// name — is healed on read so downstream validation (the agent PATCH handler)
// does not fail with "fleet name is required".
func TestFleetPlanStore_GetPlanHealsLegacyRow(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetPlanStore{client: client}

	// Seed a legacy-shaped row directly: name column set, body has no "name".
	body := map[string]any{
		"key": "legacy-plan",
		"agents": map[string]any{
			"a": map[string]any{
				"name":        "A",
				"identity":    "i",
				"behaviors":   "b",
				"tools":       true,
				"task_policy": map[string]any{"claims": []any{"general"}},
			},
		},
	}
	_, err := client.FleetPlan.Create().
		SetKey("legacy-plan").
		SetName("Legacy Display Name").
		SetDefinition(body).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	got, ok := s.GetPlan(ctx, "legacy-plan")
	if !ok {
		t.Fatal("GetPlan: not found")
	}
	fp, ok := got.(*fleet.FleetPlan)
	if !ok {
		t.Fatalf("GetPlan type = %T, want *fleet.FleetPlan", got)
	}
	if fp.Name != "Legacy Display Name" {
		t.Fatalf("healed Name = %q, want %q", fp.Name, "Legacy Display Name")
	}
	if err := fp.Validate(); err != nil {
		t.Fatalf("healed plan failed Validate: %v", err)
	}
}
