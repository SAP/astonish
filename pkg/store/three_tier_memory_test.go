package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockMemoryStore is a simple in-memory MemoryStore for testing.
type mockMemoryStore struct {
	entries []MemoryEntry
	scope   string
}

func newMockMemoryStore(scope string) *mockMemoryStore {
	return &mockMemoryStore{scope: scope}
}

func (m *mockMemoryStore) Search(_ context.Context, query string, maxResults int, _ float64) ([]MemorySearchResult, error) {
	return m.search(query, maxResults, "")
}

func (m *mockMemoryStore) SearchByCategory(_ context.Context, query string, maxResults int, _ float64, category string) ([]MemorySearchResult, error) {
	return m.search(query, maxResults, category)
}

func (m *mockMemoryStore) search(query string, maxResults int, category string) ([]MemorySearchResult, error) {
	var results []MemorySearchResult
	for i, e := range m.entries {
		if category != "" && e.Category != category {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(e.Content), strings.ToLower(query)) {
			continue
		}
		results = append(results, MemorySearchResult{
			ID:       fmt.Sprintf("%s-%d", m.scope, i),
			Snippet:  e.Content,
			Category: e.Category,
			Score:    0.8 - float64(i)*0.1, // decreasing score
			Scope:    m.scope,
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

func (m *mockMemoryStore) Add(_ context.Context, entry MemoryEntry) error {
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockMemoryStore) Delete(_ context.Context, id string) error {
	for i, e := range m.entries {
		entryID := fmt.Sprintf("%s-%d", m.scope, i)
		if entryID == id {
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			return nil
		}
		_ = e
	}
	return fmt.Errorf("not found: %s", id)
}

func (m *mockMemoryStore) List(_ context.Context, category string, limit, offset int) ([]MemorySearchResult, error) {
	var results []MemorySearchResult
	skipped := 0
	for i, e := range m.entries {
		if category != "" && e.Category != category {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		results = append(results, MemorySearchResult{
			ID:       fmt.Sprintf("%s-%d", m.scope, i),
			Snippet:  e.Content,
			Category: e.Category,
			Score:    1.0,
			Scope:    m.scope,
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (m *mockMemoryStore) Count() int {
	return len(m.entries)
}

func (m *mockMemoryStore) Get(_ context.Context, id string) (*MemorySearchResult, error) {
	for i, e := range m.entries {
		entryID := fmt.Sprintf("%s-%d", m.scope, i)
		if entryID == id {
			return &MemorySearchResult{
				ID:       entryID,
				Snippet:  e.Content,
				Category: e.Category,
				Score:    1.0,
				Scope:    m.scope,
			}, nil
		}
	}
	return nil, nil
}

func (m *mockMemoryStore) Update(_ context.Context, id string, content string, category string) error {
	for i := range m.entries {
		entryID := fmt.Sprintf("%s-%d", m.scope, i)
		if entryID == id {
			m.entries[i].Content = content
			m.entries[i].Category = category
			return nil
		}
	}
	return fmt.Errorf("not found: %s", id)
}

func (m *mockMemoryStore) ListBySession(_ context.Context, sessionID string) ([]MemorySearchResult, error) {
	var results []MemorySearchResult
	for i, e := range m.entries {
		if e.SessionID == sessionID {
			results = append(results, MemorySearchResult{
				ID:        fmt.Sprintf("%s-%d", m.scope, i),
				Snippet:   e.Content,
				Category:  e.Category,
				Score:     1.0,
				Scope:     m.scope,
				SessionID: e.SessionID,
			})
		}
	}
	return results, nil
}

func (m *mockMemoryStore) Close() error {
	return nil
}

// --------------------------------------------------------------------------
// 5.8: Memory isolation tests
// --------------------------------------------------------------------------

func TestThreeTier_IsolationPersonalNotLeakedToTeam(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")
	org := newMockMemoryStore("org")

	// Add to personal only
	personal.Add(context.Background(), MemoryEntry{Content: "my SSH key passphrase is hunter2", Category: "secrets"})

	// Team and org should have zero entries
	if team.Count() != 0 {
		t.Fatal("expected team store to be empty")
	}
	if org.Count() != 0 {
		t.Fatal("expected org store to be empty")
	}

	// Direct search on team should find nothing
	teamResults, err := team.Search(context.Background(), "SSH key", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(teamResults) != 0 {
		t.Fatalf("expected 0 team results, got %d", len(teamResults))
	}
}

func TestThreeTier_IsolationTeamNotLeakedToOrg(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")
	org := newMockMemoryStore("org")

	_ = personal

	// Add to team only
	team.Add(context.Background(), MemoryEntry{Content: "team standup is at 9am daily", Category: "processes"})

	// Org should have zero entries
	if org.Count() != 0 {
		t.Fatal("expected org store to be empty")
	}
	orgResults, err := org.Search(context.Background(), "standup", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgResults) != 0 {
		t.Fatalf("expected 0 org results, got %d", len(orgResults))
	}
}

func TestThreeTier_IsolationOrgNotLeakedToPersonal(t *testing.T) {
	personal := newMockMemoryStore("personal")
	org := newMockMemoryStore("org")

	// Add to org only
	org.Add(context.Background(), MemoryEntry{Content: "company VPN requires certificate auth", Category: "infrastructure"})

	// Personal should have zero entries
	personalResults, err := personal.Search(context.Background(), "VPN", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(personalResults) != 0 {
		t.Fatalf("expected 0 personal results, got %d", len(personalResults))
	}
}

func TestThreeTier_DeleteIsolation(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")

	personal.Add(context.Background(), MemoryEntry{Content: "personal note", Category: "notes"})
	team.Add(context.Background(), MemoryEntry{Content: "team note", Category: "notes"})

	// Delete from team should not affect personal
	err := team.Delete(context.Background(), "team-0")
	if err != nil {
		t.Fatal(err)
	}

	if personal.Count() != 1 {
		t.Fatalf("expected personal to still have 1 entry, got %d", personal.Count())
	}
	if team.Count() != 0 {
		t.Fatalf("expected team to have 0 entries, got %d", team.Count())
	}
}

// --------------------------------------------------------------------------
// 5.9: Cross-tier search accuracy + weighting tests
// --------------------------------------------------------------------------

func TestThreeTierSearcher_CrossTierResults(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")
	org := newMockMemoryStore("org")

	personal.Add(context.Background(), MemoryEntry{Content: "deploy using kubectl apply", Category: "tools"})
	team.Add(context.Background(), MemoryEntry{Content: "deploy pipeline runs on Jenkins", Category: "infrastructure"})
	org.Add(context.Background(), MemoryEntry{Content: "deploy to production requires approval", Category: "processes"})

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     team,
		Org:      org,
	})

	results, err := searcher.SearchAllTiers(context.Background(), "deploy", 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 cross-tier results, got %d", len(results))
	}

	// Verify all three scopes are represented
	scopes := make(map[string]bool)
	for _, r := range results {
		scopes[r.Scope] = true
	}
	for _, expected := range []string{"personal", "team", "org"} {
		if !scopes[expected] {
			t.Errorf("missing scope %q in results", expected)
		}
	}
}

func TestThreeTierSearcher_WeightingOrder(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")
	org := newMockMemoryStore("org")

	// All three tiers have the same base score (0.8) for the first result.
	// After weighting: personal=0.8*1.2=0.96, team=0.8*1.0=0.80, org=0.8*0.8=0.64
	personal.Add(context.Background(), MemoryEntry{Content: "API endpoint is /v2/users", Category: "api"})
	team.Add(context.Background(), MemoryEntry{Content: "API endpoint uses OAuth2", Category: "api"})
	org.Add(context.Background(), MemoryEntry{Content: "API rate limit is 1000/min", Category: "api"})

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     team,
		Org:      org,
	})

	results, err := searcher.SearchAllTiers(context.Background(), "API", 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) < 3 {
		t.Fatalf("expected at least 3 results, got %d", len(results))
	}

	// Personal should be ranked first due to 1.2x weighting
	if results[0].Scope != "personal" {
		t.Errorf("expected personal to be ranked first, got %q (score=%.4f)", results[0].Scope, results[0].Score)
	}

	// Team should be ranked second due to 1.0x weighting
	if results[1].Scope != "team" {
		t.Errorf("expected team to be ranked second, got %q (score=%.4f)", results[1].Scope, results[1].Score)
	}

	// Org should be ranked last due to 0.8x weighting
	if results[2].Scope != "org" {
		t.Errorf("expected org to be ranked last, got %q (score=%.4f)", results[2].Scope, results[2].Score)
	}
}

func TestThreeTierSearcher_CategoryFilter(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")
	org := newMockMemoryStore("org")

	personal.Add(context.Background(), MemoryEntry{Content: "my git config", Category: "tools"})
	personal.Add(context.Background(), MemoryEntry{Content: "my API preferences", Category: "api"})
	team.Add(context.Background(), MemoryEntry{Content: "team coding standards", Category: "tools"})
	org.Add(context.Background(), MemoryEntry{Content: "org security policy", Category: "security"})

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     team,
		Org:      org,
	})

	// Filter by "tools" category
	results, err := searcher.SearchAllTiersByCategory(context.Background(), "", 10, 0, "tools")
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 tools-category results, got %d", len(results))
	}

	for _, r := range results {
		if r.Category != "tools" {
			t.Errorf("expected category 'tools', got %q", r.Category)
		}
	}
}

func TestThreeTierSearcher_NilStoresSkipped(t *testing.T) {
	personal := newMockMemoryStore("personal")
	personal.Add(context.Background(), MemoryEntry{Content: "personal data", Category: "notes"})

	// Team and org are nil — should not panic
	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     nil,
		Org:      nil,
	})

	results, err := searcher.SearchAllTiers(context.Background(), "personal", 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result from personal-only, got %d", len(results))
	}
	if results[0].Scope != "personal" {
		t.Errorf("expected scope 'personal', got %q", results[0].Scope)
	}
}

func TestThreeTierSearcher_MaxResultsLimit(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")
	org := newMockMemoryStore("org")

	// Add many entries to each tier
	for i := 0; i < 10; i++ {
		personal.Add(context.Background(), MemoryEntry{Content: fmt.Sprintf("personal item %d about golang", i), Category: "notes"})
		team.Add(context.Background(), MemoryEntry{Content: fmt.Sprintf("team item %d about golang", i), Category: "notes"})
		org.Add(context.Background(), MemoryEntry{Content: fmt.Sprintf("org item %d about golang", i), Category: "notes"})
	}

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     team,
		Org:      org,
	})

	results, err := searcher.SearchAllTiers(context.Background(), "golang", 5, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) > 5 {
		t.Fatalf("expected at most 5 results, got %d", len(results))
	}
}

func TestThreeTierSearcher_DeduplicatesSnippets(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")

	// Same content in both tiers
	personal.Add(context.Background(), MemoryEntry{Content: "deploy with kubectl apply -f manifest.yaml", Category: "tools"})
	team.Add(context.Background(), MemoryEntry{Content: "deploy with kubectl apply -f manifest.yaml", Category: "tools"})

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     team,
		Org:      nil,
	})

	results, err := searcher.SearchAllTiers(context.Background(), "kubectl", 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Should be deduplicated to 1 result (personal wins due to higher weight)
	if len(results) != 1 {
		t.Fatalf("expected 1 deduplicated result, got %d", len(results))
	}
	if results[0].Scope != "personal" {
		t.Errorf("expected personal to win dedup (higher weight), got %q", results[0].Scope)
	}
}

func TestThreeTierSearcher_TieOrderIsStable(t *testing.T) {
	personal := newMockMemoryStore("personal")
	team := newMockMemoryStore("team")

	personal.Add(context.Background(), MemoryEntry{Content: "github enterprise issues api", Category: "memory"})
	team.Add(context.Background(), MemoryEntry{Content: "github enterprise issues api team", Category: "memory"})

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     team,
	})

	var firstOrder []string
	for i := 0; i < 20; i++ {
		results, err := searcher.SearchAllTiers(context.Background(), "github enterprise issues api", 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
		order := []string{results[0].ID, results[1].ID}
		if i == 0 {
			firstOrder = order
			continue
		}
		if order[0] != firstOrder[0] || order[1] != firstOrder[1] {
			t.Fatalf("unstable order: first=%v current=%v", firstOrder, order)
		}
	}
}

func TestThreeTierSearcher_MinScoreFilter(t *testing.T) {
	personal := newMockMemoryStore("personal")
	// score will be 0.8 * 1.2 = 0.96
	personal.Add(context.Background(), MemoryEntry{Content: "high score item", Category: "notes"})
	// score will be 0.7 * 1.2 = 0.84
	personal.Add(context.Background(), MemoryEntry{Content: "lower score item", Category: "notes"})

	searcher := NewThreeTierSearcher(ThreeTierMemoryStoreConfig{
		Personal: personal,
		Team:     nil,
		Org:      nil,
	})

	// High minScore should filter out the second result
	results, err := searcher.SearchAllTiers(context.Background(), "score item", 10, 0.90)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result above minScore 0.90, got %d", len(results))
	}
}
