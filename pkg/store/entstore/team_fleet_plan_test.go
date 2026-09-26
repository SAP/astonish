package entstore

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/ent/team/fleetplan"
	"github.com/SAP/astonish/pkg/fleet"

	"gopkg.in/yaml.v3"
)

// validPlan returns a minimal FleetPlan that passes FleetConfig.Validate().
func validPlan(key, name string) *fleet.FleetPlan {
	plan := &fleet.FleetPlan{Key: key, Name: name}
	plan.FleetConfig = fleet.FleetConfig{
		Name: name,
		Agents: map[string]fleet.FleetAgentConfig{
			"po": {
				Name:       "Product Owner",
				Identity:   "i",
				Behaviors:  "b",
				Tools:      fleet.ToolsConfig{All: true},
				TaskPolicy: &fleet.AgentTaskPolicy{Claims: []string{"code.write"}},
			},
		},
	}
	return plan
}

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

func TestFleetPlanStore_GetPlanYAMLFromDefinition(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetPlanStore{client: client}

	// Save a plan the way the UI does — only the definition is written; yaml_content stays nil.
	if err := s.Save(ctx, validPlan("my-plan", "My Plan")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	yamlStr, err := s.GetPlanYAML(ctx, "my-plan")
	if err != nil {
		t.Fatalf("GetPlanYAML: %v", err)
	}
	if strings.TrimSpace(yamlStr) == "" {
		t.Fatal("GetPlanYAML returned empty; expected YAML serialized from definition")
	}
	if !strings.Contains(yamlStr, "My Plan") {
		t.Fatalf("YAML missing plan name:\n%s", yamlStr)
	}
	if !strings.Contains(yamlStr, "po:") {
		t.Fatalf("YAML missing agent:\n%s", yamlStr)
	}
}

func TestFleetPlanStore_GetPlanYAMLHealsName(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetPlanStore{client: client}

	// Legacy row: definition body has no top-level name; only the name column carries it.
	if _, err := client.FleetPlan.Create().
		SetKey("legacy").
		SetName("Legacy Name").
		SetDefinition(map[string]any{
			"key": "legacy",
			"agents": map[string]any{
				"po": map[string]any{"name": "PO", "identity": "i", "behaviors": "b", "tools": true},
			},
		}).
		Save(ctx); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	yamlStr, err := s.GetPlanYAML(ctx, "legacy")
	if err != nil {
		t.Fatalf("GetPlanYAML: %v", err)
	}
	if !strings.Contains(yamlStr, "Legacy Name") {
		t.Fatalf("YAML did not heal name from column:\n%s", yamlStr)
	}
}

func TestFleetPlanStore_SavePlanYAMLReconcilesDefinition(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetPlanStore{client: client}

	if err := s.Save(ctx, validPlan("my-plan", "My Plan")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Edit the YAML (change description) and save it back.
	edited := validPlan("my-plan", "My Plan")
	edited.Description = "edited via yaml"
	out, err := yaml.Marshal(edited)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := s.SavePlanYAML(ctx, "my-plan", string(out)); err != nil {
		t.Fatalf("SavePlanYAML: %v", err)
	}

	// The change must land in the definition (source of truth), visible via GetPlan.
	got, ok := s.GetPlan(ctx, "my-plan")
	if !ok {
		t.Fatal("GetPlan not found after SavePlanYAML")
	}
	plan, ok := got.(*fleet.FleetPlan)
	if !ok {
		t.Fatalf("GetPlan type = %T", got)
	}
	if plan.Description != "edited via yaml" {
		t.Fatalf("definition not reconciled; description = %q", plan.Description)
	}

	// yaml_content is stored too.
	ent, err := client.FleetPlan.Query().Where(fleetplan.KeyEQ("my-plan")).Only(ctx)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if ent.YamlContent == nil || !strings.Contains(*ent.YamlContent, "edited via yaml") {
		t.Fatal("yaml_content not stored on SavePlanYAML")
	}
}

func TestFleetPlanStore_SavePlanYAMLValidates(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetPlanStore{client: client}

	if err := s.Save(ctx, validPlan("my-plan", "My Plan")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Invalid YAML syntax is rejected.
	if err := s.SavePlanYAML(ctx, "my-plan", "name: [unterminated"); err == nil {
		t.Fatal("expected error for invalid YAML")
	}

	// Structurally valid but fails validation (no name).
	if err := s.SavePlanYAML(ctx, "my-plan", "key: my-plan\nagents: {}\n"); err == nil {
		t.Fatal("expected validation error for name-less plan")
	}
}
