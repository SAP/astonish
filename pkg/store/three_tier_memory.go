package store

import (
	"context"
	"sort"
	"sync"
)

// Tier weight constants for cross-tier search scoring.
const (
	WeightPersonal = 1.2
	WeightTeam     = 1.0
	WeightOrg      = 0.8
)

// ThreeTierMemoryStoreConfig configures the three-tier memory search.
type ThreeTierMemoryStoreConfig struct {
	Personal MemoryStore
	Team     MemoryStore
	Org      MemoryStore
}

// threeTierMemoryStore implements ThreeTierSearcher by querying personal,
// team, and org MemoryStore instances in parallel and merging results with
// tier-based score weighting.
type threeTierMemoryStore struct {
	personal MemoryStore
	team     MemoryStore
	org      MemoryStore
}

// NewThreeTierSearcher creates a ThreeTierSearcher from three memory stores.
// Any store may be nil; nil stores are silently skipped during search.
func NewThreeTierSearcher(cfg ThreeTierMemoryStoreConfig) ThreeTierSearcher {
	return &threeTierMemoryStore{
		personal: cfg.Personal,
		team:     cfg.Team,
		org:      cfg.Org,
	}
}

func (t *threeTierMemoryStore) SearchAllTiers(ctx context.Context, query string, maxResults int, minScore float64) ([]MemorySearchResult, error) {
	return t.searchAllTiers(ctx, query, maxResults, minScore, "")
}

func (t *threeTierMemoryStore) SearchAllTiersByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	return t.searchAllTiers(ctx, query, maxResults, minScore, category)
}

// searchAllTiers runs Search or SearchByCategory on each non-nil tier in
// parallel, applies tier weighting, deduplicates by snippet, and returns
// the top results sorted by weighted score.
func (t *threeTierMemoryStore) searchAllTiers(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	// Build the list of (store, weight, scope) tuples
	type tier struct {
		store  MemoryStore
		weight float64
		scope  string
	}
	tiers := []tier{
		{t.personal, WeightPersonal, string(MemoryScopePersonal)},
		{t.team, WeightTeam, string(MemoryScopeTeam)},
		{t.org, WeightOrg, string(MemoryScopeOrg)},
	}

	// Request more results from each tier than needed (we'll trim after merge)
	perTierMax := maxResults * 2
	if perTierMax < 10 {
		perTierMax = 10
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	var allResults []MemorySearchResult
	var firstErr error

	for _, tr := range tiers {
		if tr.store == nil {
			continue
		}
		wg.Add(1)
		go func(s MemoryStore, weight float64, scope string) {
			defer wg.Done()

			var results []MemorySearchResult
			var err error
			if category == "" {
				results, err = s.Search(ctx, query, perTierMax, 0) // don't filter by minScore yet
			} else {
				results, err = s.SearchByCategory(ctx, query, perTierMax, 0, category)
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}

			// Apply tier weight and set scope
			for i := range results {
				results[i].Score *= weight
				if results[i].Scope == "" {
					results[i].Scope = scope
				}
			}
			allResults = append(allResults, results...)
		}(tr.store, tr.weight, tr.scope)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	// Deduplicate by snippet (prefer higher score)
	seen := make(map[string]int) // snippet -> index in deduped
	var deduped []MemorySearchResult
	// Sort by score DESC first so the higher-score version wins. Use stable
	// tie-breakers so equivalent scores do not depend on goroutine completion
	// order, which made identical memory_search calls appear inconsistent.
	sort.SliceStable(allResults, func(i, j int) bool {
		if allResults[i].Score != allResults[j].Score {
			return allResults[i].Score > allResults[j].Score
		}
		if allResults[i].Scope != allResults[j].Scope {
			return tierSortRank(allResults[i].Scope) < tierSortRank(allResults[j].Scope)
		}
		if allResults[i].CreatedAt != allResults[j].CreatedAt {
			return allResults[i].CreatedAt > allResults[j].CreatedAt
		}
		return allResults[i].ID < allResults[j].ID
	})
	for _, r := range allResults {
		if _, exists := seen[r.Snippet]; !exists {
			seen[r.Snippet] = len(deduped)
			deduped = append(deduped, r)
		}
	}

	// Filter by minScore
	var filtered []MemorySearchResult
	for _, r := range deduped {
		if r.Score >= minScore {
			filtered = append(filtered, r)
		}
	}

	// Limit to maxResults
	if len(filtered) > maxResults {
		filtered = filtered[:maxResults]
	}

	return filtered, nil
}

func tierSortRank(scope string) int {
	switch scope {
	case string(MemoryScopePersonal):
		return 0
	case string(MemoryScopeTeam):
		return 1
	case string(MemoryScopeOrg):
		return 2
	default:
		return 3
	}
}
