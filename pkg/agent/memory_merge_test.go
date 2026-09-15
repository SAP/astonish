package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	mem "github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
)

func TestMemoryMergerFailsClosedWhenScenarioUpsertFails(t *testing.T) {
	memStore := &failingScenarioUpsertStore{listErr: errors.New("list unavailable")}
	merger := &MemoryMerger{}

	_, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content:  "Use the noVNC ticket endpoint, then open the websocket with the returned ticket.",
		Category: "proxmox-console-access",
	})
	if err == nil {
		t.Fatal("expected scenario upsert failure")
	}
	if memStore.addCalled {
		t.Fatal("SaveOrMerge inserted raw memory after scenario-card upsert failed")
	}
}

func TestMemoryMergerDiscardsUncardableMemory(t *testing.T) {
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}

	result, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content:  "A temporary outage did not work during a failed attempt.",
		Category: "temporary-outage",
	})
	if err != nil {
		t.Fatalf("SaveOrMerge returned error: %v", err)
	}
	if result.Action != "discarded" {
		t.Fatalf("Action = %q, want discarded", result.Action)
	}
	if memStore.addCalled {
		t.Fatal("SaveOrMerge inserted raw memory for uncardable input")
	}
}

func TestMemoryMergerCanMarkTraceBackedScenarioCardsVerified(t *testing.T) {
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}

	result, err := merger.SaveOrMergeWithStatus(context.Background(), memStore, store.MemoryEntry{
		Content:  "Use the noVNC ticket endpoint, then open the websocket with the returned ticket.",
		Category: "proxmox-console-access",
	}, mem.ScenarioCardStatusVerified)
	if err != nil {
		t.Fatalf("SaveOrMergeWithStatus returned error: %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("Action = %q, want created", result.Action)
	}
	if !strings.Contains(memStore.added.Content, "status: verified") {
		t.Fatalf("saved card was not marked verified: %s", memStore.added.Content)
	}
}

func TestMemoryMergerUsesTargetMemoryScope(t *testing.T) {
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}
	ctx := store.WithMemoryScope(context.Background(), store.MemoryScopePersonal)

	_, err := merger.SaveOrMerge(ctx, memStore, store.MemoryEntry{
		Content:  "Use GET https://github.wdf.sap.corp/api/v3/issues for assigned GitHub Enterprise issues.",
		Category: "infrastructure-sap-github-enterprise",
	})
	if err != nil {
		t.Fatalf("SaveOrMerge returned error: %v", err)
	}
	card, ok := mem.ParseScenarioCard(memStore.added.Content)
	if !ok {
		t.Fatalf("saved content is not a scenario card: %s", memStore.added.Content)
	}
	if card.Scope != string(store.MemoryScopePersonal) {
		t.Fatalf("Scope = %q, want personal", card.Scope)
	}
}

func TestPlatformReflectorMarksExistingSessionCardsVerified(t *testing.T) {
	card := mem.ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use the noVNC ticket endpoint."},
		Status:            mem.ScenarioCardStatusDraft,
	}
	memStore := &failingScenarioUpsertStore{entries: map[string]store.MemorySearchResult{
		"card-1": {
			ID:        "card-1",
			Snippet:   mem.RenderScenarioCard(card),
			Category:  mem.ScenarioCardCategory,
			SessionID: "session-1",
		},
	}}

	(&PlatformReflector{}).markScenarioCardsVerifiedForSession(context.Background(), memStore, "session-1")
	updated, ok := mem.ParseScenarioCard(memStore.entries["card-1"].Snippet)
	if !ok {
		t.Fatalf("updated memory is not a scenario card: %s", memStore.entries["card-1"].Snippet)
	}
	if updated.Status != mem.ScenarioCardStatusVerified {
		t.Fatalf("card was not verified: %#v", updated)
	}
}

type failingScenarioUpsertStore struct {
	listErr   error
	addCalled bool
	added     store.MemoryEntry
	entries   map[string]store.MemorySearchResult
}

func (f *failingScenarioUpsertStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *failingScenarioUpsertStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *failingScenarioUpsertStore) Add(_ context.Context, entry store.MemoryEntry) error {
	f.addCalled = true
	f.added = entry
	return nil
}

func (f *failingScenarioUpsertStore) Get(_ context.Context, id string) (*store.MemorySearchResult, error) {
	entry, ok := f.entries[id]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (f *failingScenarioUpsertStore) Update(_ context.Context, id, content, category string) error {
	if f.entries == nil {
		f.entries = make(map[string]store.MemorySearchResult)
	}
	entry := f.entries[id]
	entry.ID = id
	entry.Snippet = content
	entry.Category = category
	f.entries[id] = entry
	return nil
}

func (f *failingScenarioUpsertStore) Delete(context.Context, string) error { return nil }

func (f *failingScenarioUpsertStore) List(context.Context, string, int, int) ([]store.MemorySearchResult, error) {
	return nil, f.listErr
}

func (f *failingScenarioUpsertStore) ListBySession(_ context.Context, sessionID string) ([]store.MemorySearchResult, error) {
	var out []store.MemorySearchResult
	for _, entry := range f.entries {
		if entry.SessionID == sessionID {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (f *failingScenarioUpsertStore) Count() int { return 0 }

func (f *failingScenarioUpsertStore) Close() error { return nil }

func TestSaveOrMergeWithStatus_DoesNotDiscardOperationalKnowledge(t *testing.T) {
	// Content that uses negative phrasing but contains resolution indicators
	// should NOT be discarded — it's operational knowledge.
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}

	result, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content: strings.Join([]string{
			"- Do not use the admin token, use the service-account credential instead",
			"- The endpoint does not exist at /v1, use /v2/servers instead",
		}, "\n"),
		Category: "infrastructure/api-access",
	})
	if err != nil {
		t.Fatalf("SaveOrMerge returned error: %v", err)
	}
	if result.Action == "discarded" {
		t.Fatal("operational knowledge with resolution indicators should NOT be discarded")
	}
	if !memStore.addCalled {
		t.Fatal("expected memory to be saved (Add called)")
	}
}

func TestSaveOrMergeWithStatus_DiscardsEmptyContent(t *testing.T) {
	// Content that produces no extractable bullets (all whitespace/empty lines)
	// should be discarded.
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}

	result, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content:  "   \n  \n  ",
		Category: "test",
	})
	if err != nil {
		t.Fatalf("SaveOrMerge returned error: %v", err)
	}
	if result.Action != "discarded" {
		t.Fatalf("Action = %q, want discarded for empty content", result.Action)
	}
}

func TestSaveOrMergeWithStatus_DiscardsNonEphemeralCautionsWithNoResolution(t *testing.T) {
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}

	result, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content: strings.Join([]string{
			"- The default configuration is incorrect for production environments",
			"- The API schema does not match the documentation",
		}, "\n"),
		Category: "workarounds/api-quirks",
	})
	if err != nil {
		t.Fatalf("SaveOrMerge returned error: %v", err)
	}
	if result.Action != "discarded" {
		t.Fatalf("Action = %q, want discarded", result.Action)
	}
	if memStore.addCalled {
		t.Fatal("bad-path-only memory should not be stored")
	}
}

func TestMemoryIngestionEndToEndDropsBadPaths(t *testing.T) {
	merger := &MemoryMerger{}

	mixedStore := &failingScenarioUpsertStore{}
	result, err := merger.SaveOrMergeWithStatus(context.Background(), mixedStore, store.MemoryEntry{
		Content: strings.Join([]string{
			"- Use GET https://github.wdf.sap.corp/api/v3/issues with X-Auth-Token: {{CREDENTIAL:sap-github-bearer:token}}",
			"- Caution: the sap-github credential does NOT work on api.github.com",
		}, "\n"),
		Category: "infrastructure/github-enterprise-issues",
	}, mem.ScenarioCardStatusVerified)
	if err != nil {
		t.Fatalf("mixed SaveOrMergeWithStatus returned error: %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("mixed Action = %q, want created", result.Action)
	}
	card, ok := mem.ParseScenarioCard(mixedStore.added.Content)
	if !ok {
		t.Fatalf("stored content is not a scenario card: %s", mixedStore.added.Content)
	}
	if strings.Contains(mixedStore.added.Content, "Cautions or conditional failures") || strings.Contains(mixedStore.added.Content, "does NOT work") {
		t.Fatalf("stored card contains bad-path content: %s", mixedStore.added.Content)
	}
	if !strings.Contains(strings.Join(card.RecommendedRecipe, "\n"), "github.wdf.sap.corp") {
		t.Fatalf("stored card lost working endpoint: %#v", card.RecommendedRecipe)
	}

	badOnlyStore := &failingScenarioUpsertStore{}
	result, err = merger.SaveOrMerge(context.Background(), badOnlyStore, store.MemoryEntry{
		Content:  "- The default configuration is incorrect for production\n- The schema does not match the documentation",
		Category: "configuration-failures",
	})
	if err != nil {
		t.Fatalf("bad-only SaveOrMerge returned error: %v", err)
	}
	if result.Action != "discarded" || badOnlyStore.addCalled {
		t.Fatalf("bad-only result = %#v addCalled=%v, want discarded without storage", result, badOnlyStore.addCalled)
	}

	alternativeStore := &failingScenarioUpsertStore{}
	result, err = merger.SaveOrMerge(context.Background(), alternativeStore, store.MemoryEntry{
		Content:  "- Do not use sap-github, use sap-github-bearer instead",
		Category: "infrastructure/github-credential",
	})
	if err != nil {
		t.Fatalf("working-alternative SaveOrMerge returned error: %v", err)
	}
	if result.Action == "discarded" || !strings.Contains(alternativeStore.added.Content, "sap-github-bearer") {
		t.Fatalf("working alternative was not stored: result=%#v content=%q", result, alternativeStore.added.Content)
	}
}

func TestSaveOrMergeWithStatus_DiscardsEphemeralCautionsOnly(t *testing.T) {
	// Content where ALL lines are ephemeral cautions should still be discarded.
	memStore := &failingScenarioUpsertStore{}
	merger := &MemoryMerger{}

	result, err := merger.SaveOrMerge(context.Background(), memStore, store.MemoryEntry{
		Content:  "A temporary outage did not work during a failed attempt.",
		Category: "temporary-outage",
	})
	if err != nil {
		t.Fatalf("SaveOrMerge returned error: %v", err)
	}
	if result.Action != "discarded" {
		t.Fatalf("Action = %q, want discarded for ephemeral-only content", result.Action)
	}
}
