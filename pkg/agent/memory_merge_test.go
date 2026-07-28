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
