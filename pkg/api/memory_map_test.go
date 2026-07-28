package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

func TestInvalidateMemoryHealthCacheClearsEntries(t *testing.T) {
	memoryHealthCache.Lock()
	memoryHealthCache.entries = map[string]memoryHealthCacheEntry{
		"org:team:user": {snapshot: "old"},
	}
	memoryHealthCache.Unlock()

	invalidateMemoryHealthCache()

	if size := memoryHealthCacheSize(); size != 0 {
		t.Fatalf("memory health cache size = %d, want 0", size)
	}
}

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
	if rec.Type != "update_scenario_card" || len(rec.MemoryIDs) != 2 {
		t.Fatalf("unexpected recommendation: %#v", rec)
	}
	if len(rec.Card.SourceMemoryIDs) != 2 {
		t.Fatalf("merged card sources = %#v, want raw-1 and raw-2", rec.Card.SourceMemoryIDs)
	}
}

func TestBuildMemoryHealthRecommendsCleanupForIncorporatedRawScenarioCardSources(t *testing.T) {
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
	if health.RecommendationCount != 1 {
		t.Fatalf("RecommendationCount = %d, want 1 cleanup recommendation for remaining raw source memories: %#v", health.RecommendationCount, health.Recommendations)
	}
	rec := health.Recommendations[0]
	if rec.Type != "cleanup_raw_sources" || len(rec.MemoryIDs) != 2 {
		t.Fatalf("unexpected cleanup recommendation: %#v", rec)
	}
	if report.Stats.TrialErrorRiskCount == 0 || report.Stats.TransientRiskCount == 0 {
		t.Fatalf("advanced map should still expose diagnostic risk flags: %#v", report.Stats)
	}
}

func TestBuildMemoryHealthRecommendsDuplicateScenarioCardMergeAcrossCanonicalKeys(t *testing.T) {
	lbaas := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Scope:             "personal",
		Title:             "OpenStack LBaaS load balancer list in QA-DE-1",
		RecommendedRecipe: []string{"Use the openstack-keystone credential and GET https://loadbalancer.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            memory.ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"raw-1"},
	}
	octavia := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-octavia-load",
		Scope:             "personal",
		Title:             "OpenStack Octavia load balancer lookup",
		RecommendedRecipe: []string{"List load balancers through Octavia using the openstack-keystone token at https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Conditions:        []string{"Use qa-de-1 OpenStack."},
		Status:            memory.ScenarioCardStatusDraft,
		SourceMemoryIDs:   []string{"raw-2"},
	}
	report := BuildMemoryMap([]store.MemorySearchResult{
		{ID: "card-1", Snippet: memory.RenderScenarioCard(lbaas), Category: memory.ScenarioCardCategory, Scope: "personal"},
		{ID: "card-2", Snippet: memory.RenderScenarioCard(octavia), Category: memory.ScenarioCardCategory, Scope: "personal"},
	})
	health := BuildMemoryHealth(report, false, mustParseTime(t, "2026-07-27T12:00:00Z"))
	if health.RecommendationCount != 1 {
		t.Fatalf("RecommendationCount = %d, want 1 duplicate-card recommendation: %#v", health.RecommendationCount, health.Recommendations)
	}
	rec := health.Recommendations[0]
	if rec.Type != "merge_duplicate_scenario_cards" {
		t.Fatalf("Type = %q, want merge_duplicate_scenario_cards: %#v", rec.Type, rec)
	}
	if len(rec.DuplicateCardIDs) != 1 || rec.DuplicateCardIDs[0] != "card-2" {
		t.Fatalf("DuplicateCardIDs = %#v, want card-2", rec.DuplicateCardIDs)
	}
	if rec.Card.Status != memory.ScenarioCardStatusVerified {
		t.Fatalf("merged card status = %q, want verified", rec.Card.Status)
	}
	if len(rec.ResolverSignals) == 0 || rec.MatchScore.Decision != "merge" {
		t.Fatalf("resolver metadata missing: signals=%#v match=%#v", rec.ResolverSignals, rec.MatchScore)
	}
}

func TestBuildMemoryHealthDoesNotMergeDifferentOpenStackResources(t *testing.T) {
	loadBalancer := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Scope:             "personal",
		Title:             "OpenStack load balancers in QA-DE-1",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Status:            memory.ScenarioCardStatusVerified,
	}
	compute := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-nova-servers",
		Scope:             "personal",
		Title:             "OpenStack compute servers in QA-DE-1",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://compute.qa-de-1.cloud.sap/v2.1/servers."},
		Status:            memory.ScenarioCardStatusVerified,
	}
	report := BuildMemoryMap([]store.MemorySearchResult{
		{ID: "card-1", Snippet: memory.RenderScenarioCard(loadBalancer), Category: memory.ScenarioCardCategory, Scope: "personal"},
		{ID: "card-2", Snippet: memory.RenderScenarioCard(compute), Category: memory.ScenarioCardCategory, Scope: "personal"},
	})
	health := BuildMemoryHealth(report, false, mustParseTime(t, "2026-07-27T12:00:00Z"))
	for _, rec := range health.Recommendations {
		if rec.Type == "merge_duplicate_scenario_cards" {
			t.Fatalf("unexpected duplicate-card recommendation for distinct resources: %#v", rec)
		}
	}
}

func TestBuildMemoryHealthRecommendsSingleRawMemoryCardOnlyAction(t *testing.T) {
	report := BuildMemoryMap([]store.MemorySearchResult{
		{ID: "raw-1", Snippet: "Use the noVNC ticket endpoint for Proxmox console access.", Category: "proxmox-console-access", Scope: "team"},
	})
	health := BuildMemoryHealth(report, true, mustParseTime(t, "2026-07-27T12:00:00Z"))
	if health.RecommendationCount != 1 {
		t.Fatalf("RecommendationCount = %d, want 1 card-only recommendation for single raw memory: %#v", health.RecommendationCount, health.Recommendations)
	}
	if health.Recommendations[0].Type != "create_scenario_card" || health.Recommendations[0].MemoryIDs[0] != "raw-1" {
		t.Fatalf("unexpected recommendation: %#v", health.Recommendations[0])
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

func TestPreferredPreservedScenarioCardIDSkipsDuplicateIDs(t *testing.T) {
	kept := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-lbaas-load",
		Title:             "OpenStack LBaaS load balancer list in QA-DE-1",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://loadbalancer.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            memory.ScenarioCardStatusVerified,
	}
	duplicate := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-octavia-load",
		Title:             "OpenStack Octavia load balancer lookup",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Conditions:        []string{"Applies in qa-de-1."},
		Status:            memory.ScenarioCardStatusDraft,
	}
	team := &deletingMemoryStore{entries: map[string]store.MemorySearchResult{
		"card-keep":   {ID: "card-keep", Snippet: memory.RenderScenarioCard(kept), Category: memory.ScenarioCardCategory, Scope: "team"},
		"card-delete": {ID: "card-delete", Snippet: memory.RenderScenarioCard(duplicate), Category: memory.ScenarioCardCategory, Scope: "team"},
	}}
	req, err := http.NewRequest(http.MethodPost, "/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	merged := memory.MergeScenarioCards(kept, duplicate)
	merged.CanonicalKey = kept.CanonicalKey
	merged.Title = kept.Title
	merged.RecommendedRecipe = []string{"Use openstack-keystone and GET https://loadbalancer.qa-de-1.cloud.sap/v2/lbaas/loadbalancers."}
	preserved := preferredPreservedScenarioCardID(req.Context(), team, []string{"card-delete"}, merged)
	if preserved != "card-keep" {
		t.Fatalf("preserved = %q, want card-keep", preserved)
	}
}

func TestDeleteDuplicateScenarioCardsDeletesOnlyExplicitScenarioCards(t *testing.T) {
	duplicate := memory.ScenarioCard{
		CanonicalKey:      "infrastructure-openstack-octavia-load",
		Title:             "OpenStack Octavia load balancer lookup",
		RecommendedRecipe: []string{"Use openstack-keystone and GET https://octavia.qa-de-1.cloud.sap/v2.0/lbaas/loadbalancers."},
		Status:            memory.ScenarioCardStatusDraft,
	}
	team := &deletingMemoryStore{entries: map[string]store.MemorySearchResult{
		"card-delete": {ID: "card-delete", Snippet: memory.RenderScenarioCard(duplicate), Category: memory.ScenarioCardCategory, Scope: "team"},
		"card-keep":   {ID: "card-keep", Snippet: memory.RenderScenarioCard(duplicate), Category: memory.ScenarioCardCategory, Scope: "team"},
		"raw-team":    {ID: "raw-team", Snippet: "raw memory", Category: "openstack", Scope: "team"},
	}}
	req, err := http.NewRequest(http.MethodPost, "/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	deleted := deleteDuplicateScenarioCards(req, &store.Services{Memory: team}, &PlatformUser{ID: "user-1"}, []string{"card-delete", "raw-team", "card-keep", "card-delete"}, "card-keep")
	if deleted != 1 {
		t.Fatalf("deleted = %d, want one explicit duplicate scenario card", deleted)
	}
	if !team.deleted["card-delete"] {
		t.Fatalf("duplicate card was not deleted: %#v", team.deleted)
	}
	if team.deleted["raw-team"] || team.deleted["card-keep"] {
		t.Fatalf("deleted protected/non-card memory: %#v", team.deleted)
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

func (d *deletingMemoryStore) List(_ context.Context, category string, _, _ int) ([]store.MemorySearchResult, error) {
	var out []store.MemorySearchResult
	for _, entry := range d.entries {
		if category == "" || entry.Category == category {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (d *deletingMemoryStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (d *deletingMemoryStore) Count() int { return len(d.entries) }

func (d *deletingMemoryStore) Close() error { return nil }
