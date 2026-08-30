package entstore

import (
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/store"
)

// scoredResult pairs a document ID with a similarity score.
type scoredResult struct {
	ID    string
	Score float64
}

func sqliteFTSQuery(text string) string {
	words := tsvectorWordRe.FindAllString(text, -1)
	if len(words) == 0 {
		return ""
	}
	terms := make([]string, 0, len(words))
	for _, word := range words {
		terms = append(terms, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " OR ")
}

func sortMemoryResults(results []store.MemorySearchResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID
	})
}

// vectorIndex maintains an in-memory cache of embeddings for fast brute-force
// cosine similarity search. It is lazily loaded from the database on first
// search and incrementally updated on write operations.
//
// This is used for SQLite backends where pgvector is not available.
type vectorIndex struct {
	mu      sync.RWMutex
	vectors map[string][]float32
	loaded  bool
}

func newVectorIndex() *vectorIndex {
	return &vectorIndex{
		vectors: make(map[string][]float32),
	}
}

// search performs brute-force cosine similarity search against the index.
func (vi *vectorIndex) search(query []float32, maxResults int, minScore float64) []scoredResult {
	vi.mu.RLock()
	defer vi.mu.RUnlock()

	var results []scoredResult
	for id, vec := range vi.vectors {
		score := cosineSimilarity(query, vec)
		if score >= minScore {
			results = append(results, scoredResult{ID: id, Score: score})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID
	})
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// add inserts or updates an embedding in the index.
func (vi *vectorIndex) add(id string, vec []float32) {
	if vec == nil {
		return
	}
	vi.mu.Lock()
	vi.vectors[id] = vec
	vi.mu.Unlock()
}

// remove deletes an embedding from the index.
func (vi *vectorIndex) remove(id string) {
	vi.mu.Lock()
	delete(vi.vectors, id)
	vi.mu.Unlock()
}

// isLoaded returns whether the index has been populated from the database.
func (vi *vectorIndex) isLoaded() bool {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return vi.loaded
}

// markLoaded marks the index as fully loaded.
func (vi *vectorIndex) markLoaded() {
	vi.mu.Lock()
	vi.loaded = true
	vi.mu.Unlock()
}

// cosineSimilarity computes cosine similarity between two vectors.
// Vectors are assumed to be L2-normalized (as produced by sentence-transformers),
// in which case this is equivalent to the dot product.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// deserializeEmbedding converts a little-endian byte slice to a float32 slice.
func deserializeEmbedding(data []byte) []float32 {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil
	}
	vec := make([]float32, len(data)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// rrfFuse merges two ranked result lists using Reciprocal Rank Fusion.
// k is the RRF parameter (typically 60).
func rrfFuse(vectorResults, keywordResults []scoredResult, k int, maxResults int) []scoredResult {
	scores := make(map[string]float64)
	for rank, r := range vectorResults {
		scores[r.ID] += 1.0 / float64(k+rank+1)
	}
	for rank, r := range keywordResults {
		scores[r.ID] += 1.0 / float64(k+rank+1)
	}

	results := make([]scoredResult, 0, len(scores))
	for id, score := range scores {
		results = append(results, scoredResult{ID: id, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID
	})
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// normalizeSingleSource normalizes scores from a single ranked list so the
// top result has score ≈ 1.0 and subsequent results are proportional. This
// is used when only one search source (FTS5 or vector) produced results —
// applying RRF to a single list would collapse all scores to tiny 1/(k+rank)
// values that fail downstream minScore filters.
func normalizeSingleSource(results []scoredResult, maxResults int) []scoredResult {
	if len(results) == 0 {
		return nil
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	// Results are pre-sorted by score DESC from ftsSearch.
	maxScore := results[0].Score
	if maxScore <= 0 {
		// All scores are zero/negative — assign positional scores instead.
		for i := range results {
			results[i].Score = 1.0 / float64(i+1)
		}
		return results
	}
	for i := range results {
		results[i].Score = results[i].Score / maxScore
	}
	return results
}
