package provider

import (
	"context"
	"os"
	"strings"
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

// ResolveFromStaticMap uses model name patterns to estimate context window.
// This covers providers that don't expose metadata APIs (Anthropic, OpenAI, xAI, etc.)
// and acts as a fast fallback for providers whose APIs might be temporarily unreachable.
// Exported for use in TUI display hints; prefer ResolveContextWindow for authoritative values.
func ResolveFromStaticMap(modelName string) int {
	m := strings.ToLower(modelName)

	// Claude family
	if strings.Contains(m, "claude") {
		if strings.Contains(m, "haiku") {
			return 200_000
		}
		if strings.Contains(m, "sonnet") || strings.Contains(m, "opus") {
			return 200_000
		}
		if strings.Contains(m, "claude-3") || strings.Contains(m, "claude-4") {
			return 200_000
		}
		if strings.Contains(m, "claude-2") {
			return 100_000
		}
		return 200_000
	}

	// GPT family
	if strings.Contains(m, "gpt-5") {
		return 272_000
	}
	if strings.Contains(m, "gpt-4.1") {
		return 1_047_576
	}
	if strings.Contains(m, "gpt-4o") {
		return 128_000
	}
	if strings.Contains(m, "gpt-4-turbo") || strings.Contains(m, "gpt-4-1106") || strings.Contains(m, "gpt-4-0125") {
		return 128_000
	}
	if strings.Contains(m, "gpt-4") {
		return 8_192
	}
	if strings.Contains(m, "gpt-3.5-turbo") {
		return 16_385
	}
	if strings.Contains(m, "o1") || strings.Contains(m, "o3") || strings.Contains(m, "o4") {
		return 200_000
	}

	// Gemini family
	if strings.Contains(m, "gemini-2") || strings.Contains(m, "gemini-1.5-pro") {
		return 2_000_000
	}
	if strings.Contains(m, "gemini-1.5-flash") {
		return 1_000_000
	}
	if strings.Contains(m, "gemini-1.0") || strings.Contains(m, "gemini-pro") {
		return 32_000
	}

	// Llama family
	if strings.Contains(m, "llama-3.3") || strings.Contains(m, "llama-3.1") {
		return 131_072
	}
	if strings.Contains(m, "llama-3") || strings.Contains(m, "llama3") {
		return 8_192
	}

	// Mistral family
	if strings.Contains(m, "mistral-large") || strings.Contains(m, "mistral-medium") {
		return 128_000
	}
	if strings.Contains(m, "mixtral") {
		return 32_768
	}
	if strings.Contains(m, "mistral") {
		return 32_000
	}

	// Grok
	if strings.Contains(m, "grok") {
		return 131_072
	}

	// DeepSeek
	if strings.Contains(m, "deepseek") {
		return 128_000
	}

	// Qwen
	if strings.Contains(m, "qwen") {
		return 128_000
	}

	return 0 // unknown — fall through to default
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
