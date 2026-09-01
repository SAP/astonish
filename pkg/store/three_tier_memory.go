package store

import (
	"context"
	"fmt"
	"reflect"
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
	Embed    EmbedFunc
}

// threeTierMemoryStore implements ThreeTierSearcher by querying personal,
// team, and org MemoryStore instances in parallel and merging results with
// tier-based score weighting.
type threeTierMemoryStore struct {
	personal MemoryStore
	team     MemoryStore
	org      MemoryStore
	embed    EmbedFunc
}

// NewThreeTierSearcher creates a ThreeTierSearcher from three memory stores.
// Any store may be nil; nil stores are skipped during search. When Embed is
// configured, all non-nil stores must support prepared queries.
func NewThreeTierSearcher(cfg ThreeTierMemoryStoreConfig) ThreeTierSearcher {
	return &threeTierMemoryStore{
		personal: cfg.Personal,
		team:     cfg.Team,
		org:      cfg.Org,
		embed:    cfg.Embed,
	}
}

func (t *threeTierMemoryStore) SearchAllTiers(ctx context.Context, query string, maxResults int, minScore float64) ([]MemorySearchResult, error) {
	return t.searchAllTiers(ctx, query, maxResults, minScore, "")
}

func (t *threeTierMemoryStore) SearchAllTiersByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	return t.searchAllTiers(ctx, query, maxResults, minScore, category)
}

func (t *threeTierMemoryStore) PrepareQuery(ctx context.Context, semanticQuery, keywordQuery string) (PreparedMemoryQuery, error) {
	query := PreparedMemoryQuery{SemanticQuery: semanticQuery, KeywordQuery: keywordQuery}
	if semanticQuery == "" {
		return query, nil
	}
	if t.embed == nil {
		return PreparedMemoryQuery{}, fmt.Errorf("prepare memory query: embedding function is not configured")
	}
	embedding, err := t.embed(ctx, semanticQuery)
	if err != nil {
		return PreparedMemoryQuery{}, fmt.Errorf("prepare memory query embedding: %w", err)
	}
	if len(embedding) == 0 {
		return PreparedMemoryQuery{}, fmt.Errorf("prepare memory query embedding: empty embedding")
	}
	query.Embedding = embedding
	query.EmbeddingIdentity = reflect.ValueOf(t.embed).Pointer()
	return query, nil
}

func (t *threeTierMemoryStore) SearchAllTiersPrepared(ctx context.Context, query PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	return t.searchPreparedAllTiers(ctx, query, maxResults, minScore, category)
}

// searchAllTiers runs Search or SearchByCategory on each non-nil tier in
// parallel, applies tier weighting, deduplicates by snippet, and returns
// the top results sorted by weighted score.
func (t *threeTierMemoryStore) searchAllTiers(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	if t.embed != nil {
		prepared, err := t.PrepareQuery(ctx, query, query)
		if err != nil {
			return nil, err
		}
		return t.searchPreparedAllTiers(ctx, prepared, maxResults, minScore, category)
	}
	return t.searchAllTiersWith(ctx, query, nil, maxResults, minScore, category)
}

func (t *threeTierMemoryStore) searchPreparedAllTiers(ctx context.Context, query PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	if query.SemanticQuery != "" && len(query.Embedding) == 0 {
		return nil, fmt.Errorf("search prepared memory query: semantic query requires embedding")
	}
	return t.searchAllTiersWith(ctx, query.KeywordQuery, &query, maxResults, minScore, category)
}

func (t *threeTierMemoryStore) searchAllTiersWith(ctx context.Context, query string, prepared *PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
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
	errorsByTier := make([]error, len(tiers))

	for tierIndex, tr := range tiers {
		if tr.store == nil {
			continue
		}
		wg.Add(1)
		go func(index int, s MemoryStore, weight float64, scope string) {
			defer wg.Done()

			var results []MemorySearchResult
			var err error
			if prepared != nil {
				preparedStore, ok := s.(PreparedMemoryStore)
				if !ok {
					err = fmt.Errorf("%s memory store does not support prepared queries", scope)
				} else {
					results, err = preparedStore.SearchPrepared(ctx, *prepared, perTierMax, 0, category)
				}
			} else if category == "" {
				results, err = s.Search(ctx, query, perTierMax, 0) // don't filter by minScore yet
			} else {
				results, err = s.SearchByCategory(ctx, query, perTierMax, 0, category)
			}

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errorsByTier[index] = err
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
		}(tierIndex, tr.store, tr.weight, tr.scope)
	}

	wg.Wait()

	for _, err := range errorsByTier {
		if err != nil {
			return nil, err
		}
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

	// Normalize scores to [0, 1.0]. Tier weighting can push scores above 1.0
	// (e.g. personal weight 1.2 × normalized 1.0 = 1.2) which is correct
	// for ranking but must not leak into the display layer as >100%.
	// Re-normalize proportionally so the top result maps to 1.0 and relative
	// differences between results are preserved.
	if len(filtered) > 0 && filtered[0].Score > 1.0 {
		maxScore := filtered[0].Score // already sorted DESC
		for i := range filtered {
			filtered[i].Score /= maxScore
		}
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
