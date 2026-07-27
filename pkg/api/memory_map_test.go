package api

import (
	"testing"

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

func TestBuildMemoryMapUsesFallbackForEmptyContent(t *testing.T) {
	report := BuildMemoryMap([]store.MemorySearchResult{{ID: "m1", Scope: "personal"}})
	if len(report.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(report.Groups))
	}
	if report.Groups[0].Key != "uncategorized" {
		t.Fatalf("key = %q, want uncategorized", report.Groups[0].Key)
	}
}
