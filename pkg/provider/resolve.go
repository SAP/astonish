package provider

import (
	"context"
	"log/slog"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

// ResolveEffectiveConfig builds an AppConfig by cascading provider settings
// from the database. This is the single source of truth for provider resolution
// used by both the Studio chat and channel message paths.
//
// The 3-tier cascade applies layers in priority order (higher overrides lower):
//  1. Platform settings (visible to all orgs/teams)
//  2. Org settings (visible to all teams in the org)
//  3. Team settings (specific to this team)
//
// Each layer's DefaultProvider/DefaultModel override the previous if non-empty.
// Provider configs are additive (merged by name).
//
// Any of the store parameters may be nil (skipped).
func ResolveEffectiveConfig(
	ctx context.Context,
	platformSettings store.PlatformSettingsStore,
	orgSettings store.OrgSettingsStore,
	teamSettings store.SettingsStore,
) *config.AppConfig {
	appCfg := &config.AppConfig{}

	// Layer 1: Platform settings (base defaults for everything)
	if platformSettings != nil {
		if ps, err := platformSettings.Get(ctx); err == nil && ps != nil {
			applyProviderLayer(appCfg, ps.Providers, ps.DefaultProvider, ps.DefaultModel)
			applyPlatformWebSettings(appCfg, ps)
		} else if err != nil {
			slog.Warn("failed to read platform settings for provider resolution", "error", err)
		}
	}

	// Layer 2: Org settings (overrides platform for this org's teams)
	if orgSettings != nil {
		if os, err := orgSettings.Get(ctx); err == nil && os != nil {
			applyProviderLayer(appCfg, os.Providers, os.DefaultProvider, os.DefaultModel)
		} else if err != nil {
			slog.Warn("failed to read org settings for provider resolution", "error", err)
		}
	}

	// Layer 3: Team settings (highest priority, overrides everything)
	if teamSettings != nil {
		if ts, err := teamSettings.Get(ctx); err == nil && ts != nil {
			applyTeamProviderLayer(appCfg, ts)
		} else if err != nil {
			slog.Warn("failed to read team settings for provider resolution", "error", err)
		}
	}

	return appCfg
}

// UserDefaultSettings is the narrow contract ApplyUserDefault consumes:
// per-user default provider/model accessors that return "" for "inherit".
// Kept as an interface so pkg/provider does not import pkg/store.
type UserDefaultSettings interface {
	GetDefaultProvider() string
	GetDefaultModel() string
}

// ApplyUserDefault overlays a user's personal default onto an already-resolved
// AppConfig (Platform → Org → Team → UserDefault). Empty-string fields
// inherit from the cascade below; non-empty fields override
// cfg.General.DefaultProvider / DefaultModel. The additive cfg.Providers
// map is never touched. Returns the same *AppConfig pointer received
// (mutate-in-place). Nil us is a safe no-op. Idempotent.
func ApplyUserDefault(cfg *config.AppConfig, us UserDefaultSettings) *config.AppConfig {
	if us == nil {
		return cfg
	}
	if p := us.GetDefaultProvider(); p != "" {
		cfg.General.DefaultProvider = p
	}
	if m := us.GetDefaultModel(); m != "" {
		cfg.General.DefaultModel = m
	}
	return cfg
}

// ApplyProviderOverride overlays a per-session/per-app pin onto an
// already-resolved AppConfig — the innermost cascade layer
// (Platform → Org → Team → UserDefault → ProviderOverride). Empty-string
// fields inherit from the cascade below (this is the "unpin"/"clear"
// contract); non-empty fields override cfg.General.DefaultProvider /
// DefaultModel. The additive cfg.Providers map is never touched. Returns
// the same *AppConfig pointer received (mutate-in-place). Idempotent.
func ApplyProviderOverride(cfg *config.AppConfig, provider, model string) *config.AppConfig {
	if provider != "" {
		cfg.General.DefaultProvider = provider
	}
	if model != "" {
		cfg.General.DefaultModel = model
	}
	return cfg
}

// applyProviderLayer merges a provider configuration layer into the app config.
// Providers are additive by name; defaults override only if non-empty.
func applyProviderLayer(appCfg *config.AppConfig, providers map[string]store.ProviderConfig, defaultProvider, defaultModel string) {
	if defaultProvider != "" {
		appCfg.General.DefaultProvider = defaultProvider
	}
	if defaultModel != "" {
		appCfg.General.DefaultModel = defaultModel
	}
	if len(providers) > 0 {
		if appCfg.Providers == nil {
			appCfg.Providers = make(map[string]config.ProviderConfig)
		}
		for name, provCfg := range providers {
			appCfg.Providers[name] = config.ProviderConfig(provCfg)
		}
	}
}

// applyPlatformWebSettings overlays platform-level web search settings onto the app config.
func applyPlatformWebSettings(appCfg *config.AppConfig, ps *store.PlatformSettings) {
	if ps.WebSearchTool != "" {
		appCfg.General.WebSearchTool = ps.WebSearchTool
	}
	if ps.WebExtractTool != "" {
		appCfg.General.WebExtractTool = ps.WebExtractTool
	}
	if ps.PerplexityWebSearch != nil {
		appCfg.PerplexityWebSearch = config.PerplexityWebSearchConfig{
			Provider:          ps.PerplexityWebSearch.Provider,
			Model:             ps.PerplexityWebSearch.Model,
			SearchContextSize: ps.PerplexityWebSearch.SearchContextSize,
			MaxResults:        ps.PerplexityWebSearch.MaxResults,
		}
	}
}

// applyTeamProviderLayer overlays team-level settings onto the app config.
// Only non-zero/non-empty team values override the host defaults.
func applyTeamProviderLayer(appCfg *config.AppConfig, ts *store.TeamSettings) {
	applyProviderLayer(appCfg, ts.Providers, ts.DefaultProvider, ts.DefaultModel)

	if ts.WebSearchTool != "" {
		appCfg.General.WebSearchTool = ts.WebSearchTool
	}
	if ts.WebExtractTool != "" {
		appCfg.General.WebExtractTool = ts.WebExtractTool
	}
	if ts.ContextLength > 0 {
		appCfg.General.ContextLength = ts.ContextLength
	}
}
