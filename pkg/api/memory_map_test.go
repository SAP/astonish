package api

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

func TestBuildMemoryMapFlagsScatteredTransientAndTrialErrorRisks(t *testing.T) {
	memories := []store.MemorySearchResult{
		{
			ID:        "personal-1",
			Snippet:   "Proxmox VM console access: use the noVNC ticket endpoint and then open the console websocket.",
			Category:  "proxmox-console-access",
			Scope:     "personal",
			CreatedAt: "2026-07-27T10:00:00Z",
			SessionID: "session-a",
		},
		{
			ID:        "team-1",
			Snippet:   "Proxmox VM console access had a temporary 503 outage; do not use the API when it happens.",
			Category:  "proxmox-console-access",
			Scope:     "team",
			CreatedAt: "2026-07-27T11:00:00Z",
			SessionID: "session-b",
		},
		{
			ID:        "team-2",
			Snippet:   "Proxmox VM console access tried shell scraping but it did not work; final path uses noVNC ticket.",
			Category:  "proxmox-console-access",
			Scope:     "team",
			CreatedAt: "2026-07-27T12:00:00Z",
			SessionID: "session-c",
		},
	}

	report := BuildMemoryMap(memories)
	if report.Stats.TotalMemories != 3 {
		t.Fatalf("TotalMemories = %d, want 3", report.Stats.TotalMemories)
	}
	if report.Stats.GroupCount != 1 {
		t.Fatalf("GroupCount = %d, want 1", report.Stats.GroupCount)
	}

	group := report.Groups[0]
	if group.Key != "proxmox-console-access" {
		t.Fatalf("group key = %q, want proxmox-console-access", group.Key)
	}
	if group.MemoryCount != 3 {
		t.Fatalf("MemoryCount = %d, want 3", group.MemoryCount)
	}
	if group.Representative.ID != "team-2" {
		t.Fatalf("representative = %q, want newest memory team-2", group.Representative.ID)
	}

	wantFlags := map[string]bool{
		"duplicate_risk":         false,
		"scattered_topic":        false,
		"transient_failure_risk": false,
		"trial_error_risk":       false,
	}
	for _, flag := range group.Flags {
		if _, ok := wantFlags[flag.Type]; ok {
			wantFlags[flag.Type] = true
		}
	}
	for flagType, seen := range wantFlags {
		if !seen {
			t.Fatalf("missing flag %q in %#v", flagType, group.Flags)
		}
	}
	if report.Stats.DuplicateRiskCount != 1 || report.Stats.ScatteredTopicCount != 1 || report.Stats.TransientRiskCount != 1 || report.Stats.TrialErrorRiskCount != 1 {
		t.Fatalf("unexpected stats: %#v", report.Stats)
	}
}

func TestBuildMemoryMapGroupsScenarioCardWithSourceMemories(t *testing.T) {
	card := memory.ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use the noVNC ticket endpoint."},
		Status:            memory.ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"raw-1"},
	}
	report := BuildMemoryMap([]store.MemorySearchResult{
		{
			ID:       "card-1",
			Snippet:  memory.RenderScenarioCard(card),
			Category: memory.ScenarioCardCategory,
			Scope:    "team",
		},
		{
			ID:       "raw-1",
			Snippet:  "Proxmox console access uses the noVNC ticket endpoint.",
			Category: "proxmox-console-access",
			Scope:    "personal",
		},
	})
	if len(report.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(report.Groups))
	}
	group := report.Groups[0]
	if !group.HasScenarioCard || group.ScenarioCardID != "card-1" {
		t.Fatalf("scenario card metadata not set: %#v", group)
	}
	foundScenarioFlag := false
	for _, flag := range group.Flags {
		if flag.Type == "scenario_card" {
			foundScenarioFlag = true
		}
	}
	if !foundScenarioFlag {
		t.Fatalf("missing scenario_card flag: %#v", group.Flags)
	}
}

func TestBuildMemoryHealthRecommendsScenarioCardCreation(t *testing.T) {
	report := BuildMemoryMap([]store.MemorySearchResult{
		{ID: "m1", Snippet: "Use the noVNC ticket endpoint for Proxmox console access.", Category: "proxmox-console-access", Scope: "team"},
		{ID: "m2", Snippet: "Earlier trial and error tried scraping but final path uses noVNC.", Category: "proxmox-console-access", Scope: "personal"},
	})
	health := BuildMemoryHealth(report, true, mustParseTime(t, "2026-07-27T12:00:00Z"))
	if health.RecommendationCount != 1 {
		t.Fatalf("RecommendationCount = %d, want 1: %#v", health.RecommendationCount, health.Recommendations)
	}
	rec := health.Recommendations[0]
	if rec.Type != "create_scenario_card" || rec.TargetScope != "team" {
		t.Fatalf("unexpected recommendation: %#v", rec)
	}
	if rec.Card.CanonicalKey != "proxmox-console-access" || len(rec.Card.RecommendedRecipe) == 0 {
		t.Fatalf("invalid draft card: %#v", rec.Card)
	}
}

func TestBuildMemoryHealthRecommendsExistingCardUpdate(t *testing.T) {
	card := memory.ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use the noVNC ticket endpoint."},
		Status:            memory.ScenarioCardStatusDraft,
		SourceMemoryIDs:   []string{"raw-1"},
	}
	report := BuildMemoryMap([]store.MemorySearchResult{
		{ID: "card-1", Snippet: memory.RenderScenarioCard(card), Category: memory.ScenarioCardCategory, Scope: "team"},
		{ID: "raw-1", Snippet: "Use the noVNC ticket endpoint.", Category: "proxmox-console-access", Scope: "team"},
		{ID: "raw-2", Snippet: "Open the websocket with the returned ticket.", Category: "proxmox-console-access", Scope: "team"},
	})
	health := BuildMemoryHealth(report, true, mustParseTime(t, "2026-07-27T12:00:00Z"))
	if health.RecommendationCount != 1 {
		t.Fatalf("RecommendationCount = %d, want 1: %#v", health.RecommendationCount, health.Recommendations)
	}
	rec := health.Recommendations[0]
	if rec.Type != "update_scenario_card" || len(rec.MemoryIDs) != 1 || rec.MemoryIDs[0] != "raw-2" {
		t.Fatalf("unexpected recommendation: %#v", rec)
	}
	if len(rec.Card.SourceMemoryIDs) != 2 {
		t.Fatalf("merged card sources = %#v, want raw-1 and raw-2", rec.Card.SourceMemoryIDs)
	}
}

func TestBuildMemoryHealthDoesNotRepeatReviewForIncorporatedScenarioCard(t *testing.T) {
	card := memory.ScenarioCard{
		CanonicalKey:                  "proxmox-console-access",
		Scope:                         "team",
		Title:                         "Proxmox Console Access",
		RecommendedRecipe:             []string{"Use the noVNC ticket endpoint."},
		CautionsOrConditionalFailures: []string{"Temporary 503 outages should be rechecked before changing the path."},
		Status:                        memory.ScenarioCardStatusDraft,
		SourceMemoryIDs:               []string{"raw-1", "raw-2"},
	}
	report := BuildMemoryMap([]store.MemorySearchResult{
		{ID: "card-1", Snippet: memory.RenderScenarioCard(card), Category: memory.ScenarioCardCategory, Scope: "team"},
		{ID: "raw-1", Snippet: "Use the noVNC ticket endpoint.", Category: "proxmox-console-access", Scope: "team"},
		{ID: "raw-2", Snippet: "Earlier trial and error failed during a transient outage, but final path uses noVNC.", Category: "proxmox-console-access", Scope: "team"},
	})
	health := BuildMemoryHealth(report, true, mustParseTime(t, "2026-07-27T12:00:00Z"))
	if health.RecommendationCount != 0 {
		t.Fatalf("RecommendationCount = %d, want 0 for already incorporated source memories: %#v", health.RecommendationCount, health.Recommendations)
	}
	if report.Stats.TrialErrorRiskCount == 0 || report.Stats.TransientRiskCount == 0 {
		t.Fatalf("advanced map should still expose diagnostic risk flags: %#v", report.Stats)
	}
}

func TestScenarioCardUpdateContentConvertsRawEditAndDiscardsUncardableEdit(t *testing.T) {
	content, category, ok := scenarioCardUpdateContent("team", store.MemoryEntry{
		Content:  "Use the noVNC ticket endpoint for Proxmox console access.",
		Category: "proxmox-console-access",
	})
	if !ok {
		t.Fatal("expected raw edit with a successful path to become a scenario card")
	}
	if category != memory.ScenarioCardCategory || !memory.IsScenarioCard(store.MemorySearchResult{Snippet: content, Category: category}) {
		t.Fatalf("update did not produce a scenario card: category=%q content=%q", category, content)
	}

	_, _, ok = scenarioCardUpdateContent("team", store.MemoryEntry{
		Content:  "A temporary outage did not work during a failed attempt.",
		Category: "temporary-outage",
	})
	if ok {
		t.Fatal("expected uncardable edit to be discarded")
	}
}

func TestDeleteSourceIDsFromStoresDeletesOnlyRawSources(t *testing.T) {
	ctx := context.Background()
	personal := &deletingMemoryStore{entries: map[string]store.MemorySearchResult{
		"raw-personal": {ID: "raw-personal", Snippet: "Use the noVNC ticket endpoint.", Category: "proxmox", Scope: "personal"},
	}}
	team := &deletingMemoryStore{entries: map[string]store.MemorySearchResult{
		"card-team": {ID: "card-team", Snippet: memory.RenderScenarioCard(memory.ScenarioCard{
			CanonicalKey:      "proxmox-console-access",
			Title:             "Proxmox Console Access",
			RecommendedRecipe: []string{"Use the noVNC ticket endpoint."},
			Status:            memory.ScenarioCardStatusDraft,
		}), Category: memory.ScenarioCardCategory, Scope: "team"},
		"raw-team": {ID: "raw-team", Snippet: "Open the websocket with the returned ticket.", Category: "proxmox", Scope: "team"},
	}}
	deleted := deleteSourceIDsFromStores(ctx, []string{"raw-personal", "card-team", "raw-team", "raw-team"}, []store.MemoryStore{personal, team})
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 raw sources", deleted)
	}
	if !personal.deleted["raw-personal"] || !team.deleted["raw-team"] {
		t.Fatalf("raw source memories were not deleted: personal=%#v team=%#v", personal.deleted, team.deleted)
	}
	if team.deleted["card-team"] {
		t.Fatal("scenario card source was deleted")
	}
}

func TestBuildMemoryMapUsesFallbackForEmptyContent(t *testing.T) {
	report := BuildMemoryMap([]store.MemorySearchResult{{ID: "m1", Scope: "personal"}})
	if len(report.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(report.Groups))
	}
	if report.Groups[0].Key != "uncategorized" {
		t.Fatalf("key = %q, want uncategorized", report.Groups[0].Key)
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("failed to parse time: %v", err)
	}
	return parsed
}

type deletingMemoryStore struct {
	entries map[string]store.MemorySearchResult
	deleted map[string]bool
}

func (d *deletingMemoryStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (d *deletingMemoryStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (d *deletingMemoryStore) Add(context.Context, store.MemoryEntry) error { return nil }

func (d *deletingMemoryStore) Get(_ context.Context, id string) (*store.MemorySearchResult, error) {
	entry, ok := d.entries[id]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (d *deletingMemoryStore) Update(context.Context, string, string, string) error { return nil }

func (d *deletingMemoryStore) Delete(_ context.Context, id string) error {
	if d.deleted == nil {
		d.deleted = make(map[string]bool)
	}
	d.deleted[id] = true
	delete(d.entries, id)
	return nil
}

func (d *deletingMemoryStore) List(context.Context, string, int, int) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (d *deletingMemoryStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (d *deletingMemoryStore) Count() int { return len(d.entries) }

func (d *deletingMemoryStore) Close() error { return nil }
