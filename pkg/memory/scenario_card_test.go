package memory

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestScenarioCardDraftSeparatesRecommendedPathFromTransientFailures(t *testing.T) {
	card := DraftScenarioCardFromMemories("proxmox-console-access", "team", []store.MemorySearchResult{
		{
			ID:        "m1",
			Snippet:   "Use the noVNC ticket endpoint, then open the console websocket with the returned ticket.",
			Category:  "proxmox",
			Scope:     "team",
			SessionID: "session-1",
		},
		{
			ID:       "m2",
			Snippet:  "The API had a temporary 503 outage; do not use this as a permanent avoidance rule.",
			Category: "proxmox",
			Scope:    "team",
		},
	})

	if card.CanonicalKey != "proxmox-console-access" {
		t.Fatalf("CanonicalKey = %q", card.CanonicalKey)
	}
	if len(card.RecommendedRecipe) != 1 {
		t.Fatalf("RecommendedRecipe = %#v, want one efficient path", card.RecommendedRecipe)
	}
	if len(card.CautionsOrConditionalFailures) != 1 {
		t.Fatalf("CautionsOrConditionalFailures = %#v, want one conditional caution", card.CautionsOrConditionalFailures)
	}
	if card.SourceMemoryIDs[0] != "m1" || card.SourceMemoryIDs[1] != "m2" {
		t.Fatalf("SourceMemoryIDs = %#v", card.SourceMemoryIDs)
	}
}

func TestScenarioCardRenderParseRoundTrip(t *testing.T) {
	original := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use noVNC ticket endpoint.", "Open websocket with ticket."},
		Conditions:        []string{"Requires console permission."},
		Verification:      []string{"Console opened successfully."},
		Status:            ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"m1"},
	}

	parsed, ok := ParseScenarioCard(RenderScenarioCard(original))
	if !ok {
		t.Fatal("expected scenario card to parse")
	}
	if parsed.CanonicalKey != original.CanonicalKey || parsed.Title != original.Title || parsed.Status != original.Status {
		t.Fatalf("parsed card mismatch: %#v", parsed)
	}
	if len(parsed.RecommendedRecipe) != 2 {
		t.Fatalf("RecommendedRecipe = %#v", parsed.RecommendedRecipe)
	}
}

func TestUpsertScenarioCardMergesExistingCard(t *testing.T) {
	ctx := context.Background()
	store := &fakeMemoryStore{}
	first := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use noVNC ticket endpoint."},
		Status:            ScenarioCardStatusDraft,
		SourceMemoryIDs:   []string{"m1"},
	}
	result, err := UpsertScenarioCard(ctx, store, first)
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("first action = %q", result.Action)
	}

	second := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Scope:             "team",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Open websocket with returned ticket."},
		Status:            ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"m2"},
	}
	result, err = UpsertScenarioCard(ctx, store, second)
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	if result.Action != "merged" {
		t.Fatalf("second action = %q", result.Action)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want one merged scenario card", len(store.entries))
	}
	merged, ok := ParseScenarioCard(store.entries[0].Snippet)
	if !ok {
		t.Fatal("merged entry did not parse as scenario card")
	}
	if merged.Status != ScenarioCardStatusVerified {
		t.Fatalf("Status = %q, want verified", merged.Status)
	}
	if len(merged.RecommendedRecipe) != 2 || len(merged.SourceMemoryIDs) != 2 {
		t.Fatalf("merged card did not combine fields: %#v", merged)
	}
}

func TestFilterPreferredScenarioResultsSuppressesSupersededRawMemories(t *testing.T) {
	card := ScenarioCard{
		CanonicalKey:      "proxmox-console-access",
		Title:             "Proxmox Console Access",
		RecommendedRecipe: []string{"Use noVNC ticket endpoint."},
		Status:            ScenarioCardStatusVerified,
		SourceMemoryIDs:   []string{"raw-1"},
	}
	results := []store.MemorySearchResult{
		{ID: "raw-1", Snippet: "Use noVNC ticket endpoint.", Category: "proxmox", Score: 0.99},
		{ID: "card-1", Snippet: RenderScenarioCard(card), Category: ScenarioCardCategory, Score: 0.80},
	}
	filtered := FilterPreferredScenarioResults(results)
	if len(filtered) != 1 || filtered[0].ID != "card-1" {
		t.Fatalf("filtered = %#v, want only scenario card", filtered)
	}
}

func TestHasUsableScenarioRecipeRejectsPlaceholderOnlyCard(t *testing.T) {
	if HasUsableScenarioRecipe(ScenarioCard{RecommendedRecipe: []string{ScenarioCardPlaceholderRecipe}}) {
		t.Fatal("placeholder-only scenario card should not be usable durable memory")
	}
	if !HasUsableScenarioRecipe(ScenarioCard{RecommendedRecipe: []string{"Use the noVNC ticket endpoint."}}) {
		t.Fatal("real recommended recipe should be usable durable memory")
	}
}

type fakeMemoryStore struct {
	entries []store.MemorySearchResult
}

func (f *fakeMemoryStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *fakeMemoryStore) SearchByCategory(context.Context, string, int, float64, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *fakeMemoryStore) Add(_ context.Context, entry store.MemoryEntry) error {
	f.entries = append(f.entries, store.MemorySearchResult{
		ID:       "entry-1",
		Snippet:  entry.Content,
		Category: entry.Category,
	})
	return nil
}

func (f *fakeMemoryStore) Get(_ context.Context, id string) (*store.MemorySearchResult, error) {
	for i := range f.entries {
		if f.entries[i].ID == id {
			return &f.entries[i], nil
		}
	}
	return nil, nil
}

func (f *fakeMemoryStore) Update(_ context.Context, id, content, category string) error {
	for i := range f.entries {
		if f.entries[i].ID == id {
			f.entries[i].Snippet = content
			f.entries[i].Category = category
		}
	}
	return nil
}

func (f *fakeMemoryStore) Delete(context.Context, string) error { return nil }

func (f *fakeMemoryStore) List(_ context.Context, category string, _, _ int) ([]store.MemorySearchResult, error) {
	var out []store.MemorySearchResult
	for _, entry := range f.entries {
		if category == "" || entry.Category == category {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (f *fakeMemoryStore) ListBySession(context.Context, string) ([]store.MemorySearchResult, error) {
	return nil, nil
}

func (f *fakeMemoryStore) Count() int { return len(f.entries) }

func (f *fakeMemoryStore) Close() error { return nil }
