package routing

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/provider/openrouter"
)

// ModelCost holds parsed pricing for a model (USD per token).
type ModelCost struct {
	PromptCost     float64 // USD per input token
	CompletionCost float64 // USD per output token
}

// PricingCache provides model pricing with disk-backed caching and fuzzy name matching.
type PricingCache struct {
	mu        sync.RWMutex
	costs     map[string]ModelCost // OpenRouter model ID → cost
	loadedAt  time.Time
	cachePath string
	cacheTTL  time.Duration
}

const defaultPricingCacheTTL = 24 * time.Hour
const pricingCacheVersion = 1
const pricingCacheFilename = "pricing_cache.json"

// pricingDiskCache is the JSON structure persisted to disk.
type pricingDiskCache struct {
	Version   int                  `json:"version"`
	UpdatedAt time.Time            `json:"updated_at"`
	Models    map[string]ModelCost `json:"models"`
}

// NewPricingCache creates a new PricingCache that persists to cacheDir.
func NewPricingCache(cacheDir string) *PricingCache {
	return &PricingCache{
		cachePath: filepath.Join(cacheDir, pricingCacheFilename),
		cacheTTL:  defaultPricingCacheTTL,
	}
}

// LookupCost returns the ModelCost for the given provider/model combination.
// It tries an exact OpenRouter-style ID (provider/model) first, then fuzzy matching.
// Returns (ModelCost{}, false) when pricing data is unavailable.
func (pc *PricingCache) LookupCost(ctx context.Context, providerName, modelName string) (ModelCost, bool) {
	pc.ensureLoaded(ctx)

	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pc.costs == nil {
		return ModelCost{}, false
	}

	// Try exact OpenRouter-style ID first: "provider/model"
	if providerName != "" {
		if cost, ok := pc.costs[providerName+"/"+modelName]; ok {
			return cost, true
		}
	}

	// Try exact match with just the model name (in case it already includes provider prefix)
	if cost, ok := pc.costs[modelName]; ok {
		return cost, true
	}

	// Fuzzy match
	return pc.fuzzyMatchLocked(modelName)
}

// ensureLoaded populates the cache from disk or API if it's empty or stale.
// Called before every lookup; safe for concurrent callers.
func (pc *PricingCache) ensureLoaded(ctx context.Context) {
	// Fast path: already loaded and fresh.
	pc.mu.RLock()
	if pc.costs != nil && time.Since(pc.loadedAt) < pc.cacheTTL {
		pc.mu.RUnlock()
		return
	}
	pc.mu.RUnlock()

	// Slow path: acquire write lock and reload.
	pc.mu.Lock()
	defer pc.mu.Unlock()

	// Double-check after acquiring write lock.
	if pc.costs != nil && time.Since(pc.loadedAt) < pc.cacheTTL {
		return
	}

	// Try loading from disk first.
	if pc.loadFromDiskLocked() {
		return
	}

	// Fetch from API.
	pc.fetchFromAPILocked(ctx)
}

// loadFromDiskLocked reads the disk cache and populates pc.costs if valid.
// Must be called with pc.mu held for writing.
// Returns true if a valid non-expired cache was loaded.
func (pc *PricingCache) loadFromDiskLocked() bool {
	if pc.cachePath == "" {
		return false
	}

	data, err := os.ReadFile(pc.cachePath)
	if err != nil {
		return false // file absent or unreadable — not an error
	}

	var disk pricingDiskCache
	if err := json.Unmarshal(data, &disk); err != nil {
		slog.Debug("pricing cache: corrupt disk cache, ignoring", "path", pc.cachePath, "error", err)
		return false
	}

	if disk.Version != pricingCacheVersion {
		slog.Debug("pricing cache: version mismatch, ignoring", "got", disk.Version, "want", pricingCacheVersion)
		return false
	}

	if time.Since(disk.UpdatedAt) > pc.cacheTTL {
		slog.Debug("pricing cache: disk cache expired", "age", time.Since(disk.UpdatedAt).Round(time.Minute))
		return false
	}

	if len(disk.Models) == 0 {
		return false
	}

	pc.costs = disk.Models
	pc.loadedAt = disk.UpdatedAt
	slog.Debug("pricing cache: loaded from disk", "path", pc.cachePath, "models", len(pc.costs))
	return true
}

// fetchFromAPILocked fetches model pricing from OpenRouter and updates pc.costs.
// Must be called with pc.mu held for writing.
func (pc *PricingCache) fetchFromAPILocked(ctx context.Context) {
	// Use empty API key — OpenRouter's pricing endpoint is public.
	metadata, err := openrouter.FetchModelsMetadata(ctx, "")
	if err != nil {
		slog.Debug("pricing cache: API fetch failed", "error", err)
		return
	}

	costs := make(map[string]ModelCost, len(metadata))
	for id, m := range metadata {
		promptCost, pErr := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completionCost, cErr := strconv.ParseFloat(m.Pricing.Completion, 64)
		if pErr != nil || cErr != nil {
			continue // skip models with unparseable pricing
		}
		costs[id] = ModelCost{
			PromptCost:     promptCost,
			CompletionCost: completionCost,
		}
	}

	if len(costs) == 0 {
		slog.Debug("pricing cache: no parseable pricing data from API")
		return
	}

	pc.costs = costs
	pc.loadedAt = time.Now()
	slog.Debug("pricing cache: fetched from API", "models", len(costs))

	// Persist to disk (best-effort, don't block).
	go pc.saveToDisk(costs)
}

// saveToDisk writes the current costs map to disk atomically.
// Best-effort: logs warnings on failure but never returns errors.
func (pc *PricingCache) saveToDisk(costs map[string]ModelCost) {
	if pc.cachePath == "" {
		return
	}

	disk := pricingDiskCache{
		Version:   pricingCacheVersion,
		UpdatedAt: time.Now(),
		Models:    costs,
	}

	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		slog.Warn("pricing cache: marshal failed", "error", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(pc.cachePath), 0o755); err != nil {
		slog.Warn("pricing cache: cannot create dir", "path", filepath.Dir(pc.cachePath), "error", err)
		return
	}

	tmp := pc.cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("pricing cache: write failed", "path", tmp, "error", err)
		return
	}

	if err := os.Rename(tmp, pc.cachePath); err != nil {
		slog.Warn("pricing cache: rename failed", "error", err)
		_ = os.Remove(tmp)
		return
	}

	slog.Debug("pricing cache: saved to disk", "path", pc.cachePath, "models", len(costs))
}

// fuzzyMatchLocked finds the best pricing match for modelName using normalized name comparison.
// Must be called with pc.mu held for at least reading.
func (pc *PricingCache) fuzzyMatchLocked(modelName string) (ModelCost, bool) {
	if len(pc.costs) == 0 {
		return ModelCost{}, false
	}

	normalized := normalizeName(modelName)
	if normalized == "" {
		return ModelCost{}, false
	}

	// Pass 1: exact normalized match or substring containment.
	var bestKey string
	var bestLen int
	for key := range pc.costs {
		keyNorm := normalizeName(key)
		if keyNorm == "" {
			continue
		}
		if keyNorm == normalized {
			// Exact match — return immediately.
			return pc.costs[key], true
		}
		// Version-order independent on the dashed form so "claude-4.5-haiku"
		// matches "claude-haiku-4.5" (compacting first would glue "claudehaiku").
		if tokenBagsEqual(stripProviderAndVariants(key), stripProviderAndVariants(modelName)) {
			return pc.costs[key], true
		}
		// Substring: one contains the other.
		if strings.Contains(keyNorm, normalized) || strings.Contains(normalized, keyNorm) {
			// Prefer the longer common segment (larger overlap).
			shorter := len(normalized)
			if len(keyNorm) < shorter {
				shorter = len(keyNorm)
			}
			if shorter > bestLen {
				bestLen = shorter
				bestKey = key
			}
		}
	}
	if bestKey != "" {
		return pc.costs[bestKey], true
	}

	// Pass 2: longest common prefix > 60% of shorter name.
	bestLen = 0
	bestKey = ""
	for key := range pc.costs {
		keyNorm := normalizeName(key)
		if keyNorm == "" {
			continue
		}
		prefixLen := commonPrefixLen(normalized, keyNorm)
		shorter := len(normalized)
		if len(keyNorm) < shorter {
			shorter = len(keyNorm)
		}
		// Require > 60% of the shorter name to match.
		if shorter > 0 && prefixLen*10 > shorter*6 && prefixLen > bestLen {
			bestLen = prefixLen
			bestKey = key
		}
	}
	if bestKey != "" {
		return pc.costs[bestKey], true
	}

	return ModelCost{}, false
}

// normalizeName produces a canonical lowercase string with all separators and
// provider prefixes removed. Used for fuzzy model name matching.
//
// Examples:
//
//	"claude-sonnet-4"            → "claudesonnet4"
//	"anthropic/claude-sonnet-4"  → "claudesonnet4"
//	"anthropic--claude-sonnet-4" → "claudesonnet4"  (internal double-dash format)
//	"openai/gpt-4o-mini"         → "gpt4omini"
//	"gpt-4o-mini"                → "gpt4omini"
//	"meta-llama/llama-3.1-70b"   → "llama3170b"
func normalizeName(s string) string {
	s = stripProviderAndVariants(s)
	return strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(s)
}

// stripProviderAndVariants lowercases s and removes vendor prefixes (slash or
// double-dash) and variant suffixes, but keeps hyphens/dots so token-bag
// matching can still see word boundaries ("claude-4.5-haiku" vs "claude-haiku-4.5").
func stripProviderAndVariants(s string) string {
	s = strings.ToLower(s)

	// SAP AI Core and similar providers embed the vendor as "vendor--model".
	if idx := strings.Index(s, "--"); idx >= 0 {
		s = s[idx+2:]
	}

	knownPrefixes := []string{
		"anthropic/",
		"openai/",
		"google/",
		"meta-llama/",
		"mistralai/",
		"x-ai/",
		"deepseek/",
		"qwen/",
		"cohere/",
		"microsoft/",
		"amazon/",
		"ai21/",
		"nvidia/",
		"perplexity/",
		"01-ai/",
		"nousresearch/",
		"teknium/",
		"allenai/",
	}
	for _, prefix := range knownPrefixes {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}

	variantSuffixes := []string{":free", ":beta", ":extended", ":thinking", ":online"}
	for _, suf := range variantSuffixes {
		if strings.HasSuffix(s, suf) {
			s = strings.TrimSuffix(s, suf)
			break
		}
	}
	return s
}

// alphaNumTokens splits a name into letter-runs and digit-runs, skipping
// separators. "claude-4.5-haiku" → ["claude","4","5","haiku"].
func alphaNumTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	lastKind := 0 // 0 none, 1 letter, 2 digit
	for i := 0; i < len(s); i++ {
		c := s[i]
		kind := 0
		switch {
		case c >= 'a' && c <= 'z':
			kind = 1
		case c >= '0' && c <= '9':
			kind = 2
		default:
			// Separators are token boundaries so "claude-haiku" stays two words.
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			lastKind = 0
			continue
		}
		if lastKind != 0 && kind != lastKind {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
		cur.WriteByte(c)
		lastKind = kind
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func tokenBagKey(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	sorted := append([]string(nil), tokens...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

// tokenBagsEqual reports whether two normalized names contain the same
// letter/digit runs regardless of order. This matches OpenRouter IDs like
// "claude-haiku-4.5" against internal IDs like "claude-4.5-haiku".
func tokenBagsEqual(a, b string) bool {
	ta := alphaNumTokens(a)
	tb := alphaNumTokens(b)
	if len(ta) < 2 || len(tb) < 2 {
		return false
	}
	return tokenBagKey(ta) == tokenBagKey(tb)
}

// commonPrefixLen returns the number of characters in the common prefix of a and b.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// CostSavingsPct computes what percentage of cost was saved compared to
// routing all calls through the strong model.
//
// Uses prompt cost only for the ratio since prompt cost dominates and
// completion cost scales proportionally in typical usage.
//
// Returns 0 when pricing is unknown (zero strong cost) or no calls were made.
func CostSavingsPct(strongCost, mediumCost, weakCost ModelCost, strongCalls, mediumCalls, weakCalls int64) float64 {
	totalCalls := strongCalls + mediumCalls + weakCalls
	if totalCalls == 0 || strongCost.PromptCost <= 0 {
		return 0
	}

	actualCost := float64(strongCalls)*strongCost.PromptCost +
		float64(mediumCalls)*mediumCost.PromptCost +
		float64(weakCalls)*weakCost.PromptCost

	allStrongCost := float64(totalCalls) * strongCost.PromptCost

	if allStrongCost <= 0 {
		return 0
	}

	savings := (1 - actualCost/allStrongCost) * 100
	if savings < 0 {
		return 0
	}
	return savings
}
