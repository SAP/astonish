package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/store"
)

func TestFleetTemplateStore_BundledImmutableAndWins(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetTemplateStore{client: client}

	// Insert an orphan DB row under a bundled key — must be ignored on Get/List.
	_, err := client.FleetTemplate.Create().
		SetKey("software-dev").
		SetName("Orphan Override").
		SetDefinition(map[string]any{"name": "Orphan Override", "agents": map[string]any{}}).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	got, ok := s.GetFleet(ctx, "software-dev")
	if !ok {
		t.Fatal("expected bundled software-dev")
	}
	cfg, ok := got.(*fleet.FleetConfig)
	if !ok {
		t.Fatalf("GetFleet type = %T, want *fleet.FleetConfig", got)
	}
	if cfg.Name == "Orphan Override" {
		t.Fatal("bundled content must win over orphan DB row")
	}

	summaries := s.ListFleets(ctx)
	var sawBundled, sawOrphanName bool
	for _, sum := range summaries {
		if sum.Key == "software-dev" {
			if sum.Source != "bundled" {
				t.Fatalf("source = %q, want bundled", sum.Source)
			}
			if sum.Name == "Orphan Override" {
				sawOrphanName = true
			}
			sawBundled = true
		}
	}
	if !sawBundled {
		t.Fatal("list missing software-dev")
	}
	if sawOrphanName {
		t.Fatal("list must not surface orphan DB name for bundled key")
	}

	if err := s.Save(ctx, "software-dev", &fleet.FleetConfig{Name: "Nope"}); !errors.Is(err, store.ErrBundledTemplateImmutable) {
		t.Fatalf("Save error = %v, want ErrBundledTemplateImmutable", err)
	}
	if err := s.Delete(ctx, "software-dev"); !errors.Is(err, store.ErrBundledTemplateImmutable) {
		t.Fatalf("Delete error = %v, want ErrBundledTemplateImmutable", err)
	}

	// Custom key still works.
	custom := &fleet.FleetConfig{
		Name: "My Fleet",
		Agents: map[string]fleet.FleetAgentConfig{
			"a": {Name: "A", Identity: "i", Behaviors: "b", Tools: fleet.ToolsConfig{All: true}},
		},
	}
	if err := s.Save(ctx, "my-fleet", custom); err != nil {
		t.Fatalf("Save custom: %v", err)
	}
	gotCustom, ok := s.GetFleet(ctx, "my-fleet")
	if !ok {
		t.Fatal("expected custom fleet")
	}
	def, ok := gotCustom.(map[string]any)
	if !ok {
		t.Fatalf("custom GetFleet type = %T", gotCustom)
	}
	if def["name"] != "My Fleet" {
		t.Fatalf("custom name = %v", def["name"])
	}
}

// TestFleetTemplateStore_GetFleetHealsLegacyRow verifies a legacy custom template
// row (display name only in the `name` column, definition body has no name) is
// healed on read so callers that validate the config see a non-empty name.
func TestFleetTemplateStore_GetFleetHealsLegacyRow(t *testing.T) {
	ctx := context.Background()
	_, client := setupTeamStore(t)
	s := &teamFleetTemplateStore{client: client}

	_, err := client.FleetTemplate.Create().
		SetKey("legacy-tmpl").
		SetName("Legacy Template").
		SetDefinition(map[string]any{"agents": map[string]any{}}). // no "name" in body
		Save(ctx)
	if err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	got, ok := s.GetFleet(ctx, "legacy-tmpl")
	if !ok {
		t.Fatal("GetFleet: not found")
	}
	def, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("GetFleet type = %T, want map[string]any", got)
	}
	if def["name"] != "Legacy Template" {
		t.Fatalf("healed name = %v, want %q", def["name"], "Legacy Template")
	}
}
