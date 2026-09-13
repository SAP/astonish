package provider

import (
	"context"
	"os"
	"sync"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider/google"
	"github.com/SAP/astonish/pkg/provider/groq"
	"github.com/SAP/astonish/pkg/provider/openrouter"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/store"
)

// SetSAPModelLimitsStore wires the durable learned-limits store into the SAP
// AI Core provider (used to persist maxOutputTokens from Vertex 400s).
func SetSAPModelLimitsStore(s store.ModelLimitsStore) {
	sap.SetModelLimitsStore(s)
}

const DefaultContextWindow = 200_000

// ResolveContextWindow determines the context window size for a given provider+model.
// It uses a 4-tier fallback:
//  1. Explicit config.yaml override (general.context_length)
//  2. Provider API metadata (cached, 1hr TTL)
//  3. Static model family map (instant, no API call)
//  4. Default: 200,000 tokens
func ResolveContextWindow(ctx context.Context, providerName, modelName string, cfg *config.AppConfig) int {
	// Tier 1: Explicit config override
	if cfg != nil && cfg.General.ContextLength > 0 {
		return cfg.General.ContextLength
	}

	// Tier 2: Provider API metadata
	if apiVal := resolveFromProviderAPI(ctx, providerName, modelName, cfg); apiVal > 0 {
		return apiVal
	}

	// Tier 3: Static model family map
	if staticVal := ResolveFromStaticMap(modelName); staticVal > 0 {
		return staticVal
	}

	// Tier 4: Default
	return DefaultContextWindow
}

// resolveFromProviderAPI queries the provider's metadata API for context window info.
// Returns 0 if unavailable. Results are cached by the individual providers (1hr TTL).
func resolveFromProviderAPI(ctx context.Context, providerName, modelName string, cfg *config.AppConfig) int {
	if cfg == nil {
		return 0
	}

	_, instance, ok := resolveProviderInstance(providerName, cfg)
	if !ok {
		return 0
	}

	providerType := config.GetProviderType(providerName, instance)
	if providerType == "" {
		return 0
	}

	switch providerType {
	case "sap_ai_core":
		// SAP uses a hardcoded map — instant, no API call
		mc := sap.GetModelConfig(modelName)
		if mc.ContextWindow > 0 {
			return mc.ContextWindow
		}

	case "openrouter":
		apiKey := getProviderKey(cfg, providerName, "api_key", "OPENROUTER_API_KEY")
		if apiKey == "" {
			return 0
		}
		meta, ok := openrouter.GetModelMetadata(ctx, apiKey, modelName)
		if ok && meta.ContextLength > 0 {
			return meta.ContextLength
		}

	case "google_genai", "gemini":
		apiKey := getProviderKey(cfg, providerName, "api_key", "GOOGLE_API_KEY")
		if apiKey == "" {
			return 0
		}
		models, err := google.ListModelsWithMetadata(ctx, apiKey)
		if err != nil {
			return 0
		}
		for _, m := range models {
			if m.ID == modelName {
				return m.InputTokenLimit
			}
		}

	case "groq":
		apiKey := getProviderKey(cfg, providerName, "api_key", "GROQ_API_KEY")
		if apiKey == "" {
			return 0
		}
		models, err := groq.ListModelsWithMetadata(ctx, apiKey)
		if err != nil {
			return 0
		}
		for _, m := range models {
			if m.ID == modelName {
				return m.ContextWindow
			}
		}
	}

	return 0
}

// getProviderKey reads a key from config or falls back to an env var.
func getProviderKey(cfg *config.AppConfig, providerName, keyField, envVar string) string {
	if cfg != nil {
		if _, inst, ok := resolveProviderInstance(providerName, cfg); ok {
			if v := inst[keyField]; v != "" {
				return v
			}
		}
	}
	return envLookup(envVar)
}

// envLookup reads an environment variable. Replaceable for testing.
var envLookup = os.Getenv

// curatedContextWindows is a manually maintained map of exact model names to
// verified context-window sizes (in tokens). This is the authoritative fallback
// when a provider API doesn't expose context-window metadata. Values come from
// official model documentation and are periodically updated.
//
// Entries use the exact model ID as returned by the provider's list-models API.
// SAP-prefixed models (e.g. "anthropic--claude-4.6-opus") are covered by the
// SAP catalog in sap.GetModelConfig, not this map.
var curatedContextWindows = map[string]int{
	// Anthropic Claude — direct API names
	"claude-4.8-opus":     200_000,
	"claude-4.6-opus":     200_000,
	"claude-4.5-sonnet":   200_000,
	"claude-4-sonnet":     200_000,
	"claude-4-opus":       200_000,
	"claude-3.7-sonnet":   200_000,
	"claude-3.5-sonnet":   200_000,
	"claude-3-sonnet":     200_000,
	"claude-3-haiku":      200_000,
	"claude-3-opus":       200_000,

	// OpenAI
	"gpt-5":        272_000,
	"gpt-5-nano":   272_000,
	"gpt-5-mini":   272_000,
	"gpt-5.6-sol":  272_000,
	"gpt-4.1":      1_047_576,
	"gpt-4.1-nano": 1_047_576,
	"gpt-4.1-mini": 1_047_576,
	"gpt-4o":       128_000,
	"gpt-4o-mini":  128_000,
	"gpt-4-turbo":  128_000,
	"gpt-4":        8_192,
	"gpt-3.5-turbo": 16_385,
	"o1":           200_000,
	"o1-mini":      128_000,
	"o3":           200_000,
	"o3-mini":      200_000,
	"o4-mini":      200_000,

	// Google Gemini — inputTokenLimit from Google API
	"gemini-2.5-pro":       1_048_576,
	"gemini-2.5-flash":     1_048_576,
	"gemini-2.0-flash":     1_048_576,
	"gemini-1.5-pro":       1_048_576,
	"gemini-1.5-flash":     1_048_576,
	"gemini-1.0-pro":       32_000,

	// xAI Grok — from xAI documentation
	"grok-4.6":                       500_000,
	"grok-4.5":                       500_000,
	"grok-4.3":                       1_000_000,
	"grok-4.20-0309-non-reasoning":   2_000_000,
	"grok-4.20-0309-reasoning":       2_000_000,
	"grok-4.20-multi-agent-0309":     2_000_000,
	"grok-3":                         131_072,
	"grok-3-mini":                    131_072,
	"grok-build-0.1":                 131_072,

	// Meta Llama
	"llama-3.3-70b":          131_072,
	"llama-3.1-8b":           131_072,
	"llama-3.1-70b":          131_072,
	"llama-3.1-405b":         131_072,

	// Mistral
	"mistral-large-latest":  128_000,
	"mistral-medium-latest": 128_000,
	"mixtral-8x7b":          32_768,
	"mistral-7b":            32_000,

	// DeepSeek
	"deepseek-chat":     128_000,
	"deepseek-reasoner": 128_000,
	"deepseek-coder":    128_000,

	// Qwen
	"qwen-2.5-72b":   128_000,
	"qwen-2.5-coder": 128_000,

	// Perplexity
	"sonar":     128_000,
	"sonar-pro": 200_000,
}

// ResolveFromStaticMap looks up the context window for a model by exact name
// match against the curated map. Returns 0 when the model is not found.
// Exported for use in TUI display hints; prefer ResolveContextWindow for
// authoritative values that also query provider APIs and config overrides.
func ResolveFromStaticMap(modelName string) int {
	if v, ok := curatedContextWindows[modelName]; ok {
		return v
	}
	return 0
}

// contextWindowCache caches resolved values per provider+model to avoid repeated API calls.
var (
	cwCacheMu sync.RWMutex
	cwCache   = make(map[string]int)
)

// ResolveContextWindowCached is like ResolveContextWindow but caches the result.
// Use this for repeated lookups (e.g., per-turn compaction checks).
func ResolveContextWindowCached(ctx context.Context, providerName, modelName string, cfg *config.AppConfig) int {
	key := providerName + ":" + modelName

	cwCacheMu.RLock()
	if v, ok := cwCache[key]; ok {
		cwCacheMu.RUnlock()
		return v
	}
	cwCacheMu.RUnlock()

	val := ResolveContextWindow(ctx, providerName, modelName, cfg)

	cwCacheMu.Lock()
	cwCache[key] = val
	cwCacheMu.Unlock()

	return val
}

// InvalidateContextWindowCache clears the cache. Call on model hot-swap.
func InvalidateContextWindowCache() {
	cwCacheMu.Lock()
	cwCache = make(map[string]int)
	cwCacheMu.Unlock()
}
