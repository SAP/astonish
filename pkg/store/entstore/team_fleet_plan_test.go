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
