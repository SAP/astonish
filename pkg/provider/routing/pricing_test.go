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
		{"anthropic--claude-sonnet-4", "claudesonnet4"},
		{"anthropic--claude-4.5-haiku", "claude45haiku"},
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

// TestLookupCost_InternalIDsMatchOpenRouter verifies SAP AI Core double-dash
// IDs and version-reordered names resolve to OpenRouter catalog prices so
// HasPricing can become true and the turn summary can show cost savings.
func TestLookupCost_InternalIDsMatchOpenRouter(t *testing.T) {
	pc := &PricingCache{
		costs: map[string]ModelCost{
			"anthropic/claude-sonnet-4.5": {PromptCost: 0.000003, CompletionCost: 0.000015},
			"anthropic/claude-haiku-4.5":  {PromptCost: 0.000001, CompletionCost: 0.000005},
			"anthropic/claude-opus-4.5":   {PromptCost: 0.000015, CompletionCost: 0.000075},
		},
		loadedAt: time.Now(),
		cacheTTL: defaultPricingCacheTTL,
	}
	ctx := context.Background()

	tests := []struct {
		provider, model string
		wantPrompt      float64
	}{
		{"sap-ai-core", "anthropic--claude-4.5-haiku", 0.000001},
		{"sap-ai-core", "anthropic--claude-sonnet-4.5", 0.000003},
		{"anthropic", "claude-4.5-haiku", 0.000001},
		{"anthropic", "claude-haiku-4.5", 0.000001},
		{"anthropic", "claude-4.5-sonnet", 0.000003},
		{"anthropic", "claude-opus-4.5", 0.000015},
	}
	for _, tc := range tests {
		cost, ok := pc.LookupCost(ctx, tc.provider, tc.model)
		if !ok {
			t.Errorf("LookupCost(%q, %q): no match", tc.provider, tc.model)
			continue
		}
		if cost.PromptCost != tc.wantPrompt {
			t.Errorf("LookupCost(%q, %q) prompt = %v; want %v", tc.provider, tc.model, cost.PromptCost, tc.wantPrompt)
		}
	}

	opus, opusOK := pc.LookupCost(ctx, "anthropic", "claude-opus-4.5")
	haiku, haikuOK := pc.LookupCost(ctx, "sap-ai-core", "anthropic--claude-4.5-haiku")
	if !opusOK || !haikuOK {
		t.Fatal("expected opus and haiku lookups to succeed")
	}
	if opus.PromptCost == haiku.PromptCost {
		t.Error("haiku lookup collapsed onto opus pricing")
	}
}

// TestCostSavingsPct verifies cost savings calculation.
func TestCostSavingsPct(t *testing.T) {
	strongCost := ModelCost{PromptCost: 0.01}
	mediumCost := ModelCost{PromptCost: 0.003}
	weakCost := ModelCost{PromptCost: 0.001}
	freeCost := ModelCost{PromptCost: 0}

	// Helper: call with zero tokens to exercise the call-count fallback path.
	callFallback := func(strong, medium, weak ModelCost, sCalls, mCalls, wCalls int64) float64 {
		return CostSavingsPct(strong, medium, weak, sCalls, mCalls, wCalls, 0, 0, 0, 0, 0, 0)
	}

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
			name:   "all_strong_calls",
			strong: strongCost, medium: mediumCost, weak: weakCost,
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
			name:   "zero_strong_cost",
			strong: freeCost, medium: mediumCost, weak: weakCost,
			sCalls: 3, mCalls: 2, wCalls: 1,
			wantZero: true,
		},
		{
			name:   "zero_total_calls",
			strong: strongCost, medium: mediumCost, weak: weakCost,
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
			got := callFallback(tt.strong, tt.medium, tt.weak, tt.sCalls, tt.mCalls, tt.wCalls)
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

// TestCostSavingsPct_TokenWeighted verifies that the token-weighted path
// produces accurate savings when real token counts are provided.
func TestCostSavingsPct_TokenWeighted(t *testing.T) {
	strongCost := ModelCost{PromptCost: 0.01, CompletionCost: 0.03}
	weakCost := ModelCost{PromptCost: 0.001, CompletionCost: 0.003}
	noMedium := ModelCost{}

	t.Run("token_weighted_basic", func(t *testing.T) {
		// 1 strong call: 1000 prompt + 200 completion tokens
		// 2 weak calls: 5000 prompt + 1000 completion tokens each
		// actual = 1000*0.01 + 200*0.03 + (5000+5000)*0.001 + (1000+1000)*0.003
		//        = 10 + 6 + 10 + 6 = 32
		// all-strong = (1000+5000+5000)*0.01 + (200+1000+1000)*0.03
		//            = 110 + 66 = 176 (wrong, let me recalculate)
		// actual = 1000*0.01 + 200*0.03 + 10000*0.001 + 2000*0.003
		//        = 10 + 6 + 10 + 6 = 32
		// allStrong = 11000*0.01 + 2200*0.03 = 110 + 66 = 176
		// savings ≈ (1 - 32/176)*100 ≈ 81.8%
		got := CostSavingsPct(strongCost, noMedium, weakCost,
			1, 0, 2,
			1000, 200, // strong tokens
			0, 0, // medium tokens
			10000, 2000, // weak tokens (5000+5000 prompt, 1000+1000 completion)
		)
		if got < 80 || got > 84 {
			t.Errorf("token_weighted_basic: got %.2f%%; want ~81.8%%", got)
		}
	})

	t.Run("token_weighted_all_weak", func(t *testing.T) {
		// 0 strong, 0 medium, 10000 weak prompt + 2000 weak completion tokens
		// actual = 10000*0.001 + 2000*0.003 = 10 + 6 = 16
		// allStrong = 10000*0.01 + 2000*0.03 = 100 + 60 = 160
		// savings ≈ (1 - 16/160)*100 = 90%
		got := CostSavingsPct(strongCost, noMedium, weakCost,
			0, 0, 5,
			0, 0,
			0, 0,
			10000, 2000,
		)
		if got < 88 || got > 92 {
			t.Errorf("token_weighted_all_weak: got %.2f%%; want ~90%%", got)
		}
	})

	t.Run("token_weighted_completion_matters", func(t *testing.T) {
		// Completion tokens are expensive (3x the prompt rate). A weak call
		// with many completion tokens still saves less than the naive call count implies.
		// 0 strong, 0 medium, 1000 weak prompt + 10000 weak completion
		// actual = 1000*0.001 + 10000*0.003 = 1 + 30 = 31
		// allStrong = 1000*0.01 + 10000*0.03 = 10 + 300 = 310
		// savings ≈ (1 - 31/310)*100 = 90%
		got := CostSavingsPct(strongCost, noMedium, weakCost,
			0, 0, 1,
			0, 0,
			0, 0,
			1000, 10000,
		)
		if got < 88 || got > 92 {
			t.Errorf("token_weighted_completion_matters: got %.2f%%; want ~90%%", got)
		}
	})

	t.Run("token_weighted_3tier_mixed", func(t *testing.T) {
		mediumCost := ModelCost{PromptCost: 0.003, CompletionCost: 0.009}
		// strong: 2000 prompt + 400 completion
		// medium: 3000 prompt + 600 completion
		// weak:   5000 prompt + 1000 completion
		// actual = 2000*0.01+400*0.03 + 3000*0.003+600*0.009 + 5000*0.001+1000*0.003
		//        = (20+12) + (9+5.4) + (5+3) = 32 + 14.4 + 8 = 54.4
		// allStrong = (2000+3000+5000)*0.01 + (400+600+1000)*0.03
		//           = 100 + 60 = 160
		// savings = (1 - 54.4/160)*100 ≈ 66%
		got := CostSavingsPct(strongCost, mediumCost, weakCost,
			1, 1, 1,
			2000, 400,
			3000, 600,
			5000, 1000,
		)
		if got < 64 || got > 68 {
			t.Errorf("token_weighted_3tier_mixed: got %.2f%%; want ~66%%", got)
		}
	})

	t.Run("fallback_to_calls_when_no_tokens", func(t *testing.T) {
		// Zero token counts → should use call-count fallback (prompt cost only)
		// 0 strong, 0 medium, 5 weak calls
		// actual = 5*0.001 = 0.005; allStrong = 5*0.01 = 0.05; savings = 90%
		got := CostSavingsPct(strongCost, noMedium, weakCost,
			0, 0, 5,
			0, 0, 0, 0, 0, 0,
		)
		if got < 88 || got > 92 {
			t.Errorf("fallback_to_calls: got %.2f%%; want ~90%%", got)
		}
	})

	t.Run("zero_strong_cost_with_tokens", func(t *testing.T) {
		// Strong prompt cost 0 but completion cost non-zero → savings can still be computed.
		// actual = 5000*0.001 + 1000*0.003 = 5 + 3 = 8
		// allStrong = (0+0+5000)*0 + (0+0+1000)*0.03 = 0 + 30 = 30
		// savings ≈ (1 - 8/30)*100 ≈ 73.33%
		zeroStrong := ModelCost{PromptCost: 0, CompletionCost: 0.03}
		got := CostSavingsPct(zeroStrong, noMedium, weakCost,
			0, 0, 5,
			0, 0, 0, 0, 5000, 1000,
		)
		want := (1 - 8.0/30.0) * 100
		if got < want-0.01 || got > want+0.01 {
			t.Errorf("zero_strong_cost_with_tokens: got %.2f; want ~%.2f", got, want)
		}
	})
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
