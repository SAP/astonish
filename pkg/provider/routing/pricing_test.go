package routing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNormalizeName verifies canonical name normalization for fuzzy matching.
func TestNormalizeName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4", "claudesonnet4"},
		{"anthropic/claude-sonnet-4", "claudesonnet4"},
		{"openai/gpt-4o-mini", "gpt4omini"},
		{"gpt-4o-mini", "gpt4omini"},
		{"meta-llama/llama-3.1-70b", "llama3170b"},
		{"llama-3.1-70b", "llama3170b"},
		{"Claude Sonnet 4", "claudesonnet4"},
		{"google/gemini-pro", "geminipro"},
		{"mistralai/mistral-7b-instruct", "mistral7binstruct"},
		{"deepseek/deepseek-chat", "deepseekchat"},
		{"x-ai/grok-2", "grok2"},
		{"gpt-4o", "gpt4o"},
		{"claude-opus-4-5", "claudeopus45"},
		// Variant suffixes
		{"openai/gpt-4o-mini:free", "gpt4omini"},
		{"anthropic/claude-3-haiku:beta", "claude3haiku"},
		// Unknown provider prefix (generic slash strip)
		{"somevendor/some-model-v2", "somemodelv2"},
	}
	for _, tc := range cases {
		got := normalizeName(tc.input)
		if got != tc.want {
			t.Errorf("normalizeName(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// TestFuzzyMatch verifies that the fuzzy matching finds the right OpenRouter model.
func TestFuzzyMatch(t *testing.T) {
	// Build a cache with known entries.
	pc := &PricingCache{
		costs: map[string]ModelCost{
			"anthropic/claude-sonnet-4": {PromptCost: 0.000003, CompletionCost: 0.000015},
			"openai/gpt-4o-mini":        {PromptCost: 0.00000015, CompletionCost: 0.0000006},
			"anthropic/claude-opus-4":   {PromptCost: 0.000015, CompletionCost: 0.000075},
			"google/gemini-pro":         {PromptCost: 0.000001, CompletionCost: 0.000002},
		},
		loadedAt: time.Now(),
		cacheTTL: defaultPricingCacheTTL,
	}

	t.Run("exact_openrouter_id", func(t *testing.T) {
		cost, ok := pc.fuzzyMatchLocked("anthropic/claude-sonnet-4")
		if !ok {
			t.Fatal("expected match for exact OpenRouter ID")
		}
		if cost.PromptCost != 0.000003 {
			t.Errorf("prompt cost = %v; want 0.000003", cost.PromptCost)
		}
	})

	t.Run("provider_stripped", func(t *testing.T) {
		cost, ok := pc.fuzzyMatchLocked("claude-sonnet-4")
		if !ok {
			t.Fatal("expected match for provider-stripped name")
		}
		if cost.PromptCost != 0.000003 {
			t.Errorf("prompt cost = %v; want 0.000003", cost.PromptCost)
		}
	})

	t.Run("substring_match", func(t *testing.T) {
		cost, ok := pc.fuzzyMatchLocked("gpt-4o-mini")
		if !ok {
			t.Fatal("expected match for gpt-4o-mini")
		}
		if cost.PromptCost != 0.00000015 {
			t.Errorf("prompt cost = %v; want 0.00000015", cost.PromptCost)
		}
	})

	t.Run("no_match", func(t *testing.T) {
		_, ok := pc.fuzzyMatchLocked("totally-unknown-model-xyz-9999")
		if ok {
			t.Fatal("expected no match for unknown model")
		}
	})

	t.Run("opus_match", func(t *testing.T) {
		cost, ok := pc.fuzzyMatchLocked("claude-opus-4")
		if !ok {
			t.Fatal("expected match for claude-opus-4")
		}
		if cost.PromptCost != 0.000015 {
			t.Errorf("prompt cost = %v; want 0.000015", cost.PromptCost)
		}
	})
}

// TestCostSavingsPct verifies cost savings calculation.
func TestCostSavingsPct(t *testing.T) {
	strongCost := ModelCost{PromptCost: 0.01}
	mediumCost := ModelCost{PromptCost: 0.003}
	weakCost := ModelCost{PromptCost: 0.001}
	freeCost := ModelCost{PromptCost: 0}

	tests := []struct {
		name     string
		strong   ModelCost
		medium   ModelCost
		weak     ModelCost
		sCalls   int64
		mCalls   int64
		wCalls   int64
		wantMin  float64
		wantMax  float64
		wantZero bool
	}{
		{
			name:     "all_strong_calls",
			strong:   strongCost, medium: mediumCost, weak: weakCost,
			sCalls: 5, mCalls: 0, wCalls: 0,
			wantZero: true, // 0% savings when all strong
		},
		{
			name:   "all_weak_calls_10x_cheaper",
			strong: ModelCost{PromptCost: 0.01}, medium: freeCost, weak: ModelCost{PromptCost: 0.001},
			sCalls: 0, mCalls: 0, wCalls: 10,
			wantMin: 89, wantMax: 91, // ~90% savings
		},
		{
			name:   "mixed_50_50_strong_weak",
			strong: ModelCost{PromptCost: 0.01}, medium: freeCost, weak: ModelCost{PromptCost: 0.001},
			sCalls: 5, mCalls: 0, wCalls: 5,
			wantMin: 44, wantMax: 46, // ~45% savings
		},
		{
			name:     "zero_strong_cost",
			strong:   freeCost, medium: mediumCost, weak: weakCost,
			sCalls: 3, mCalls: 2, wCalls: 1,
			wantZero: true,
		},
		{
			name:     "zero_total_calls",
			strong:   strongCost, medium: mediumCost, weak: weakCost,
			sCalls: 0, mCalls: 0, wCalls: 0,
			wantZero: true,
		},
		{
			name:   "3tier_mixed",
			strong: strongCost, medium: mediumCost, weak: weakCost,
			sCalls: 1, mCalls: 1, wCalls: 1,
			// actual = 0.01 + 0.003 + 0.001 = 0.014; all-strong = 0.03; savings ≈ 53.3%
			wantMin: 52, wantMax: 55,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CostSavingsPct(tt.strong, tt.medium, tt.weak, tt.sCalls, tt.mCalls, tt.wCalls)
			if tt.wantZero {
				if got != 0 {
					t.Errorf("CostSavingsPct = %.2f; want 0", got)
				}
				return
			}
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("CostSavingsPct = %.2f; want [%.0f, %.0f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestPricingCache_DiskRoundTrip verifies that costs survive a save/load cycle.
func TestPricingCache_DiskRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Write a valid disk cache manually.
	costsCopy := map[string]ModelCost{
		"openai/gpt-4o":      {PromptCost: 0.00001, CompletionCost: 0.00003},
		"openai/gpt-4o-mini": {PromptCost: 0.00000015, CompletionCost: 0.0000006},
	}
	disk := pricingDiskCache{
		Version:   pricingCacheVersion,
		UpdatedAt: time.Now(),
		Models:    costsCopy,
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pricingCacheFilename), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Create a new cache that loads from disk.
	pc2 := NewPricingCache(dir)
	pc2.mu.Lock()
	loaded := pc2.loadFromDiskLocked()
	pc2.mu.Unlock()

	if !loaded {
		t.Fatal("expected loadFromDiskLocked to return true")
	}

	pc2.mu.RLock()
	cost, ok := pc2.costs["openai/gpt-4o-mini"]
	pc2.mu.RUnlock()

	if !ok {
		t.Fatal("expected openai/gpt-4o-mini to be in loaded cache")
	}
	if cost.PromptCost != 0.00000015 {
		t.Errorf("prompt cost = %v; want 0.00000015", cost.PromptCost)
	}
}

// TestPricingCache_GracefulDegradation verifies no panic when API is unavailable and no disk cache.
func TestPricingCache_GracefulDegradation(t *testing.T) {
	dir := t.TempDir()
	// No disk cache file.

	// Use a context that will cause the API call to fail quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	pc := NewPricingCache(dir)
	// Should not panic regardless of API availability.
	_, _ = pc.LookupCost(ctx, "anthropic", "claude-sonnet-4")
}

// TestPricingCache_ExpiredDiskCache verifies that an expired disk cache is not loaded.
func TestPricingCache_ExpiredDiskCache(t *testing.T) {
	dir := t.TempDir()

	disk := pricingDiskCache{
		Version:   pricingCacheVersion,
		UpdatedAt: time.Now().Add(-25 * time.Hour), // 25h ago → expired
		Models: map[string]ModelCost{
			"openai/gpt-4o": {PromptCost: 0.00001},
		},
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pricingCacheFilename), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pc := NewPricingCache(dir)
	pc.mu.Lock()
	loaded := pc.loadFromDiskLocked()
	pc.mu.Unlock()

	if loaded {
		t.Error("expected expired disk cache to NOT be loaded")
	}
}
