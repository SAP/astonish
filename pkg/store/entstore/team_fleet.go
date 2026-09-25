package entstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/fleetplan"
	"github.com/SAP/astonish/ent/team/fleettemplate"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/store"
)

// ===========================================================================
// teamFleetTemplateStore implements store.FleetTemplateStore
// ===========================================================================

type teamFleetTemplateStore struct {
	client *teament.Client
}

var _ store.FleetTemplateStore = (*teamFleetTemplateStore)(nil)

func (s *teamFleetTemplateStore) GetFleet(ctx context.Context, key string) (any, bool) {
	// Bundled templates always win — ignore any same-key DB orphan row.
	if bundled, loadErr := fleet.LoadBundledConfigs(); loadErr == nil {
		if f, ok := bundled[key]; ok {
			return f, true
		}
	}

	ent, err := s.client.FleetTemplate.Query().
		Where(fleettemplate.KeyEQ(key)).
		Only(ctx)
	if err != nil {
		return nil, false
	}
	return ent.Definition, true
}

func (s *teamFleetTemplateStore) ListFleets(ctx context.Context) []store.FleetTemplateSummary {
	var summaries []store.FleetTemplateSummary
	bundledKeySet := fleet.BundledKeys()

	if bundled, err := fleet.LoadBundledConfigs(); err == nil {
		for key, cfg := range bundled {
			summary := store.FleetTemplateSummary{
				Key:         key,
				Name:        cfg.Name,
				Description: cfg.Description,
				AgentCount:  len(cfg.Agents),
				Source:      "bundled",
			}
			for _, a := range cfg.Agents {
				summary.AgentNames = append(summary.AgentNames, a.Name)
			}
			summaries = append(summaries, summary)
		}
	}

	// Custom DB templates only — skip keys that collide with bundled (orphans stay in DB).
	templates, err := s.client.FleetTemplate.Query().
		Order(fleettemplate.ByKey()).
		All(ctx)
	if err != nil {
		return summaries
	}

	for _, t := range templates {
		if _, isBundled := bundledKeySet[t.Key]; isBundled {
			continue
		}
		summary := store.FleetTemplateSummary{
			Key:    t.Key,
			Name:   t.Name,
			Source: "custom",
		}
		if desc, ok := t.Definition["description"].(string); ok {
			summary.Description = desc
		}
		summary.AgentCount, summary.AgentNames = fleetTemplateAgentInfo(t.Definition)
		summaries = append(summaries, summary)
	}

	return summaries
}

func fleetTemplateAgentInfo(definition map[string]any) (int, []string) {
	agentsRaw, ok := definition["agents"]
	if !ok || agentsRaw == nil {
		return 0, nil
	}
	switch agents := agentsRaw.(type) {
	case map[string]any:
		names := make([]string, 0, len(agents))
		for _, a := range agents {
			if agentMap, ok := a.(map[string]any); ok {
				if name, ok := agentMap["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
		return len(agents), names
	case []any:
		names := make([]string, 0, len(agents))
		for _, a := range agents {
			if agentMap, ok := a.(map[string]any); ok {
				if name, ok := agentMap["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
		return len(agents), names
	default:
		return 0, nil
	}
}

func (s *teamFleetTemplateStore) Save(ctx context.Context, key string, fleetCfg any) error {
	if fleet.IsBundledKey(key) {
		return store.ErrBundledTemplateImmutable
	}

	definition, err := toMapAny(fleetCfg)
	if err != nil {
		return fmt.Errorf("entstore: FleetTemplateStore.Save: %w", err)
	}

	name := key
	if n, ok := definition["name"].(string); ok && n != "" {
		name = n
	}

	// Try update first.
	n, err := s.client.FleetTemplate.Update().
		Where(fleettemplate.KeyEQ(key)).
		SetName(name).
		SetDefinition(definition).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: FleetTemplateStore.Save: update: %w", err)
	}
	if n == 0 {
		_, err = s.client.FleetTemplate.Create().
			SetKey(key).
			SetName(name).
			SetDefinition(definition).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("entstore: FleetTemplateStore.Save: create: %w", err)
		}
	}
	return nil
}

func (s *teamFleetTemplateStore) Delete(ctx context.Context, key string) error {
	if fleet.IsBundledKey(key) {
		return store.ErrBundledTemplateImmutable
	}
	_, err := s.client.FleetTemplate.Delete().
		Where(fleettemplate.KeyEQ(key)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("entstore: FleetTemplateStore.Delete: %w", err)
	}
	return nil
}

func (s *teamFleetTemplateStore) Count(ctx context.Context) int {
	count, err := s.client.FleetTemplate.Query().Count(ctx)
	if err != nil {
		return 0
	}
	return count
}

func (s *teamFleetTemplateStore) Reload(_ context.Context) error {
	// No-op for DB-backed store.
	return nil
}

// ===========================================================================
// teamFleetPlanStore implements store.FleetPlanStore
// ===========================================================================

type teamFleetPlanStore struct {
	client *teament.Client
}

var _ store.FleetPlanStore = (*teamFleetPlanStore)(nil)

func (s *teamFleetPlanStore) GetPlan(ctx context.Context, key string) (any, bool) {
	ent, err := s.client.FleetPlan.Query().
		Where(fleetplan.KeyEQ(key)).
		Only(ctx)
	if err != nil {
		return nil, false
	}

	// Deserialize the stored map[string]any back into *fleet.FleetPlan so that
	// callers (e.g., dbPlanAccessAdapter) can type-assert to the concrete type.
	data, err := json.Marshal(ent.Definition)
	if err != nil {
		return ent.Definition, true
	}
	var plan fleet.FleetPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return ent.Definition, true
	}
	return &plan, true
}

func (s *teamFleetPlanStore) ListPlans(ctx context.Context) []store.FleetPlanSummary {
	plans, err := s.client.FleetPlan.Query().
		Order(fleetplan.ByKey()).
		All(ctx)
	if err != nil {
		return nil
	}

	summaries := make([]store.FleetPlanSummary, 0, len(plans))
	for _, p := range plans {
		summary := store.FleetPlanSummary{
			Key:  p.Key,
			Name: p.Name,
		}
		if desc, ok := p.Definition["description"].(string); ok {
			summary.Description = desc
		}
		if ct, ok := p.Definition["channel_type"].(string); ok {
			summary.ChannelType = ct
		}
		if cf, ok := p.Definition["created_from"].(string); ok {
			summary.CreatedFrom = cf
		}
		// Count agents from definition.
		if agents, ok := p.Definition["agents"].([]any); ok {
			summary.AgentCount = len(agents)
			for _, a := range agents {
				if agentMap, ok := a.(map[string]any); ok {
					if name, ok := agentMap["name"].(string); ok {
						summary.AgentNames = append(summary.AgentNames, name)
					}
				}
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func (s *teamFleetPlanStore) Save(ctx context.Context, plan any) error {
	definition, err := toMapAny(plan)
	if err != nil {
		return fmt.Errorf("entstore: FleetPlanStore.Save: %w", err)
	}

	key, _ := definition["key"].(string)
	if key == "" {
		return fmt.Errorf("entstore: FleetPlanStore.Save: missing 'key' in plan definition")
	}
	name := key
	if n, ok := definition["name"].(string); ok && n != "" {
		name = n
	}

	// Try update first.
	n, updateErr := s.client.FleetPlan.Update().
		Where(fleetplan.KeyEQ(key)).
		SetName(name).
		SetDefinition(definition).
		Save(ctx)
	if updateErr != nil {
		return fmt.Errorf("entstore: FleetPlanStore.Save: update: %w", updateErr)
	}
	if n == 0 {
		create := s.client.FleetPlan.Create().
			SetKey(key).
			SetName(name).
			SetDefinition(definition)

		if createdBy, ok := definition["created_by"].(string); ok {
			if uid, err := uuid.Parse(createdBy); err == nil {
				create.SetCreatedBy(uid)
			}
		}

		_, err = create.Save(ctx)
		if err != nil {
			return fmt.Errorf("entstore: FleetPlanStore.Save: create: %w", err)
		}
	}
	return nil
}

func (s *teamFleetPlanStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.FleetPlan.Delete().
		Where(fleetplan.KeyEQ(key)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("entstore: FleetPlanStore.Delete: %w", err)
	}
	return nil
}

func (s *teamFleetPlanStore) Count(ctx context.Context) int {
	count, err := s.client.FleetPlan.Query().Count(ctx)
	if err != nil {
		return 0
	}
	return count
}

func (s *teamFleetPlanStore) Reload(_ context.Context) error {
	// No-op for DB-backed store.
	return nil
}

func (s *teamFleetPlanStore) GetPlanYAML(ctx context.Context, key string) (string, error) {
	ent, err := s.client.FleetPlan.Query().
		Where(fleetplan.KeyEQ(key)).
		Only(ctx)
	if err != nil {
		if teament.IsNotFound(err) {
			return "", fmt.Errorf("fleet plan %q not found", key)
		}
		return "", fmt.Errorf("entstore: FleetPlanStore.GetPlanYAML: %w", err)
	}

	// definition is the source of truth (kept in sync by every editor tab), so
	// serialize it to YAML. This keeps the YAML tab consistent with the others and
	// covers plans created in the UI that never wrote yaml_content.
	if yamlStr, ok := planYAMLFromDefinition(ent.Definition, key, ent.Name); ok {
		return yamlStr, nil
	}
	if ent.YamlContent != nil {
		return *ent.YamlContent, nil
	}
	return "", nil
}

// planYAMLFromDefinition deserializes a stored definition map into a FleetPlan,
// heals its identity from the column values, and marshals it back to YAML.
func planYAMLFromDefinition(definition map[string]any, key, name string) (string, bool) {
	data, err := json.Marshal(definition)
	if err != nil {
		return "", false
	}
	var plan fleet.FleetPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return "", false
	}
	if plan.Key == "" {
		plan.Key = key
	}
	if plan.Name == "" {
		if name != "" {
			plan.Name = name
		} else {
			plan.Name = key
		}
	}
	out, err := yaml.Marshal(&plan)
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (s *teamFleetPlanStore) SavePlanYAML(ctx context.Context, key string, yamlContent string) error {
	// Parse the YAML into a plan and reconcile it into the definition (source of
	// truth) so edits reach every other tab and execution, not just yaml_content.
	var plan fleet.FleetPlan
	if err := yaml.Unmarshal([]byte(yamlContent), &plan); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	plan.Key = key
	if err := plan.Validate(); err != nil {
		return err
	}

	if err := s.Save(ctx, &plan); err != nil {
		return err
	}

	// Store the raw text too; GET regenerates from definition, so this is never
	// served stale after edits from other tabs.
	if _, err := s.client.FleetPlan.Update().
		Where(fleetplan.KeyEQ(key)).
		SetYamlContent(yamlContent).
		Save(ctx); err != nil {
		return fmt.Errorf("entstore: FleetPlanStore.SavePlanYAML: %w", err)
	}
	return nil
}

// --- Helpers ---

// toMapAny converts a value to map[string]any via JSON round-trip.
func toMapAny(v any) (map[string]any, error) {
	switch val := v.(type) {
	case map[string]any:
		return val, nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal to json: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("unmarshal to map: %w", err)
		}
		return m, nil
	}
}
