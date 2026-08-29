package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// PlatformProvidersRequest is the request body for saving platform provider and web-search settings.
type PlatformProvidersRequest struct {
	Providers           map[string]map[string]string       `json:"providers,omitempty"`
	DefaultProvider     string                             `json:"default_provider,omitempty"`
	DefaultModel        string                             `json:"default_model,omitempty"`
	WebSearchTool       string                             `json:"web_search_tool,omitempty"`
	WebExtractTool      string                             `json:"web_extract_tool,omitempty"`
	PerplexityWebSearch *store.PerplexityWebSearchSettings `json:"perplexity_web_search,omitempty"`
}

// GetPlatformProvidersHandler handles GET /api/settings/platform/providers.
// Returns the platform-level provider configuration (secrets masked).
// Requires: Platform Admin
func GetPlatformProvidersHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || svc.PlatformSettings == nil {
		respondError(w, http.StatusServiceUnavailable, "Platform settings not available")
		return
	}

	settings, err := svc.PlatformSettings.Get(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load platform settings: "+err.Error())
		return
	}

	// Mask secrets for the response
	masked := maskProviderSecrets(settings.Providers)

	respondJSON(w, http.StatusOK, map[string]any{
		"providers":             masked,
		"default_provider":      settings.DefaultProvider,
		"default_model":         settings.DefaultModel,
		"web_search_tool":       settings.WebSearchTool,
		"web_extract_tool":      settings.WebExtractTool,
		"perplexity_web_search": settings.PerplexityWebSearch,
	})
}

// SavePlatformProvidersHandler handles PUT /api/settings/platform/providers.
// Saves platform-level provider configuration (encrypts secrets).
// Requires: Platform Admin
func SavePlatformProvidersHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.PlatformSettings == nil {
		respondError(w, http.StatusServiceUnavailable, "Platform settings not available")
		return
	}

	var req PlatformProvidersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// Load existing to preserve secrets for masked fields
	existing, _ := svc.PlatformSettings.Get(r.Context())
	if existing == nil {
		existing = &store.PlatformSettings{}
	}

	providers := existing.Providers
	if req.Providers != nil {
		providers = mergeProviders(existing.Providers, req.Providers)
	}

	// Merge: update providers, preserving existing secrets for masked values
	// and preserving unrelated platform configuration.
	settings := &store.PlatformSettings{
		DefaultProvider:     req.DefaultProvider,
		DefaultModel:        req.DefaultModel,
		Providers:           providers,
		DefaultBrandTheme:   existing.DefaultBrandTheme,
		WebSearchTool:       existing.WebSearchTool,
		WebExtractTool:      existing.WebExtractTool,
		PerplexityWebSearch: existing.PerplexityWebSearch,
		Channels:            existing.Channels,
		Auth:                existing.Auth,
	}
	if req.WebSearchTool != "" {
		settings.WebSearchTool = req.WebSearchTool
	}
	if req.WebExtractTool != "" {
		settings.WebExtractTool = req.WebExtractTool
	}
	if req.PerplexityWebSearch != nil {
		settings.PerplexityWebSearch = req.PerplexityWebSearch
	}

	if err := svc.PlatformSettings.Save(r.Context(), settings); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save platform settings: "+err.Error())
		return
	}

	cm := GetChatManager()

	// Provider config changes require pool invalidation so cached LLM instances
	// with old API keys/URLs are dropped. Default-only changes don't need this
	// since per-request resolution (ResolveEffectiveConfig) picks up the new
	// default automatically from the DB on every chat message.
	if len(req.Providers) > 0 {
		slog.Info("[settings] platform provider configs changed, invalidating LLM pool")
		cm.InvalidateLLMPool()
		// Also hot-swap the fallback singleton LLM (used in non-platform mode
		// and as the default when pool resolution isn't available).
		cm.Reset()
	} else {
		slog.Info("[settings] platform defaults changed (no provider config change)",
			"provider", req.DefaultProvider, "model", req.DefaultModel)
		// Invalidate pool so any entries for the OLD default are refreshed
		// if the admin also changed provider configs previously.
		cm.InvalidateLLMPool()
		// Hot-swap the singleton LLM for non-platform/personal mode fallback.
		if req.DefaultProvider != "" && req.DefaultModel != "" {
			if err := cm.HotSwapLLM(r.Context(), req.DefaultProvider, req.DefaultModel); err != nil {
				slog.Warn("[settings] hot-swap on platform defaults change failed (falling back to next-request re-init)",
					"provider", req.DefaultProvider, "model", req.DefaultModel, "error", err)
			}
		}
	}

	// Invalidate channel LLM pool so channels pick up the new provider config
	if cm := GetChannelManager(); cm != nil {
		cm.InvalidateLLMPool()
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetOrgProvidersHandler handles GET /api/settings/org/providers.
// Returns the org-level provider configuration (secrets masked).
// Requires: Org Admin
func GetOrgProvidersHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || svc.OrgSettings == nil {
		respondError(w, http.StatusServiceUnavailable, "Org settings not available")
		return
	}

	settings, err := svc.OrgSettings.Get(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load org settings: "+err.Error())
		return
	}

	// Mask secrets for the response
	masked := maskProviderSecrets(settings.Providers)

	respondJSON(w, http.StatusOK, map[string]any{
		"providers":        masked,
		"default_provider": settings.DefaultProvider,
		"default_model":    settings.DefaultModel,
	})
}

// SaveOrgProvidersHandler handles PUT /api/settings/org/providers.
// Saves org-level provider configuration (encrypts secrets).
// Requires: Org Admin
func SaveOrgProvidersHandler(w http.ResponseWriter, r *http.Request) {
	if RequireOrgAdmin(w, r) == nil {
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.OrgSettings == nil {
		respondError(w, http.StatusServiceUnavailable, "Org settings not available")
		return
	}

	var req PlatformProvidersRequest // same shape for org level
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// Load existing to preserve secrets for masked fields
	existing, _ := svc.OrgSettings.Get(r.Context())
	if existing == nil {
		existing = &store.OrgSettings{}
	}

	settings := &store.OrgSettings{
		DefaultProvider: req.DefaultProvider,
		DefaultModel:    req.DefaultModel,
		Providers:       mergeProviders(existing.Providers, req.Providers),
	}

	if err := svc.OrgSettings.Save(r.Context(), settings); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save org settings: "+err.Error())
		return
	}

	cm := GetChatManager()

	// Same logic as platform: invalidate pool on provider config changes,
	// hot-swap singleton on default-only changes.
	if len(req.Providers) > 0 {
		slog.Info("[settings] org provider configs changed, invalidating LLM pool")
		cm.InvalidateLLMPool()
		cm.Reset()
	} else {
		slog.Info("[settings] org defaults changed (no provider config change)",
			"provider", req.DefaultProvider, "model", req.DefaultModel)
		cm.InvalidateLLMPool()
		if req.DefaultProvider != "" && req.DefaultModel != "" {
			if err := cm.HotSwapLLM(r.Context(), req.DefaultProvider, req.DefaultModel); err != nil {
				slog.Warn("[settings] hot-swap on org defaults change failed (falling back to next-request re-init)",
					"provider", req.DefaultProvider, "model", req.DefaultModel, "error", err)
			}
		}
	}

	// Invalidate channel LLM pool so channels pick up the new provider config
	if cm := GetChannelManager(); cm != nil {
		cm.InvalidateLLMPool()
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// mergeProviders merges incoming provider configs with existing ones,
// preserving secret values when the incoming value is masked (****...).
func mergeProviders(existing, incoming map[string]map[string]string) map[string]map[string]string {
	if incoming == nil {
		return existing
	}

	result := make(map[string]map[string]string, len(incoming))
	for name, inCfg := range incoming {
		merged := make(map[string]string, len(inCfg))
		for k, v := range inCfg {
			if isMaskedValue(v) {
				// Preserve existing secret
				if existing != nil && existing[name] != nil {
					merged[k] = existing[name][k]
				}
			} else {
				merged[k] = v
			}
		}
		result[name] = merged
	}
	return result
}

// maskProviderSecrets returns a copy of providers with secret fields masked.
func maskProviderSecrets(providers map[string]store.ProviderConfig) map[string]map[string]string {
	if providers == nil {
		return nil
	}
	masked := make(map[string]map[string]string, len(providers))
	for name, pCfg := range providers {
		mCfg := make(map[string]string, len(pCfg))
		for k, v := range pCfg {
			mCfg[k] = v
		}
		// Mask secret fields
		provType := pCfg["type"]
		for _, secretKey := range providerSecretKeysForType(provType) {
			if val, has := mCfg[secretKey]; has && val != "" {
				mCfg[secretKey] = maskValue(val)
			}
		}
		masked[name] = mCfg
	}
	return masked
}

// providerSecretKeysForType returns fields that should be masked/encrypted for a provider type.
func providerSecretKeysForType(provType string) []string {
	switch provType {
	case "sap_ai_core":
		return []string{"client_id", "client_secret", "auth_url"}
	case "xai_oauth":
		return []string{"access_token", "refresh_token", "expires_at"}
	default:
		return []string{"api_key"}
	}
}

func maskValue(val string) string {
	if len(val) <= 4 {
		return "****"
	}
	return "****" + val[len(val)-4:]
}

// GetEffectiveProvidersHandler handles GET /api/settings/providers/effective.
// Returns the fully merged provider configuration (all 3 layers cascaded, secrets masked).
// Useful for the UI to show the user what providers are actually available.
//
// In addition to the database-backed cascade, any providers configured in the
// local config.yaml (published at daemon bootstrap via SetLocalProviders) are
// surfaced read-only. They are keyed by instance name so identical config.yaml
// across multiple pods appears as a single entry (natural dedupe). Database
// providers take precedence on a name collision. The parallel provider_sources
// map tags each entry as "local" or "platform" and marks config.yaml entries
// read-only so the UI disables edit/delete (they are managed by editing
// config.yaml, not the DB API). config.yaml providers ARE usable at runtime in
// platform mode (merged in mergeResolvedProviders); only their editing surface
// is read-only. Secrets are always masked in this response.
func GetEffectiveProvidersHandler(w http.ResponseWriter, r *http.Request) {
	appCfg := effectiveAppConfig(r)

	// Names configured in config.yaml (published at daemon bootstrap). Used to
	// tag origin: these are managed by editing config.yaml, not the DB API, so
	// they are surfaced read-only even though they ARE runtime-usable.
	localNames := make(map[string]struct{})
	for name := range getLocalProviders() {
		localNames[name] = struct{}{}
	}

	providers := make(map[string]map[string]string, len(appCfg.Providers))
	sources := make(map[string]map[string]any, len(appCfg.Providers))
	maskInto := func(pCfg map[string]string) map[string]string {
		masked := make(map[string]string, len(pCfg))
		for k, v := range pCfg {
			masked[k] = v
		}
		provType := masked["type"]
		for _, secretKey := range providerSecretKeysForType(provType) {
			if val, has := masked[secretKey]; has && val != "" {
				masked[secretKey] = maskValue(val)
			}
		}
		return masked
	}

	// appCfg.Providers is the merged set (config.yaml base + DB overlay, DB wins
	// on name collision). Tag each entry by origin: a name present in localNames
	// but NOT overridden by the DB is config.yaml-sourced (read-only here).
	for name, pCfg := range appCfg.Providers {
		providers[name] = maskInto(pCfg)
		if _, isLocal := localNames[name]; isLocal {
			// config.yaml-sourced (may still be a DB override — but if the name
			// only exists in config.yaml, it is local). Distinguish by checking
			// whether the DB actually holds it: we approximate with localNames,
			// treating collisions as platform-owned below.
			sources[name] = map[string]any{"source": "local", "read_only": true}
		} else {
			sources[name] = map[string]any{"source": "platform", "read_only": false}
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"providers":        providers,
		"provider_sources": sources,
		"default_provider": appCfg.General.DefaultProvider,
		"default_model":    appCfg.General.DefaultModel,
	})
}

// GetTeamProvidersHandler handles GET /api/settings/team/providers.
// Returns raw team-level provider defaults WITHOUT cascade (no platform/org fallback).
// This is used by the UI to distinguish explicitly-set team defaults from inherited ones.
func GetTeamProvidersHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || svc.Settings == nil {
		respondError(w, http.StatusServiceUnavailable, "Team settings not available")
		return
	}

	settings, err := svc.Settings.Get(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load team settings: "+err.Error())
		return
	}

	// Mask secrets for the response
	masked := maskProviderSecrets(settings.Providers)

	respondJSON(w, http.StatusOK, map[string]any{
		"providers":        masked,
		"default_provider": settings.DefaultProvider,
		"default_model":    settings.DefaultModel,
	})
}

// DeleteProviderHandler handles DELETE /api/settings/{level}/providers/{name}.
// Removes a provider from the specified level (platform, org, or team).
func DeleteProviderHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	level := vars["level"]
	providerName := vars["name"]
	if providerName == "" {
		respondError(w, http.StatusBadRequest, "provider name is required")
		return
	}

	// Enforce role checks based on level
	switch level {
	case "platform":
		if RequirePlatformAdmin(w, r) == nil {
			return
		}
	case "org":
		if RequireOrgAdmin(w, r) == nil {
			return
		}
	case "team":
		if !RequireTeamAdmin(w, r) {
			return
		}
	default:
		respondError(w, http.StatusBadRequest, "invalid level: "+level)
		return
	}

	svc := store.FromRequest(r)
	if svc == nil {
		respondError(w, http.StatusServiceUnavailable, "Store not available")
		return
	}

	ctx := r.Context()

	switch level {
	case "platform":
		if svc.PlatformSettings == nil {
			respondError(w, http.StatusServiceUnavailable, "Platform settings not available")
			return
		}
		settings, err := svc.PlatformSettings.Get(ctx)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		delete(settings.Providers, providerName)
		if settings.DefaultProvider == providerName {
			settings.DefaultProvider = ""
		}
		if err := svc.PlatformSettings.Save(ctx, settings); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

	case "org":
		if svc.OrgSettings == nil {
			respondError(w, http.StatusServiceUnavailable, "Org settings not available")
			return
		}
		settings, err := svc.OrgSettings.Get(ctx)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		delete(settings.Providers, providerName)
		if settings.DefaultProvider == providerName {
			settings.DefaultProvider = ""
		}
		if err := svc.OrgSettings.Save(ctx, settings); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

	case "team":
		if svc.Settings == nil {
			respondError(w, http.StatusServiceUnavailable, "Team settings not available")
			return
		}
		settings, err := svc.Settings.Get(ctx)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		delete(settings.Providers, providerName)
		if settings.DefaultProvider == providerName {
			settings.DefaultProvider = ""
		}
		if err := svc.Settings.Save(ctx, settings); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

	default:
		respondError(w, http.StatusBadRequest, "level must be 'platform', 'org', or 'team'")
		return
	}

	GetChatManager().Reset()

	// Invalidate channel LLM pool so channels pick up the new provider config
	if cm := GetChannelManager(); cm != nil {
		cm.InvalidateLLMPool()
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TestProviderHandler handles POST /api/settings/providers/test.
// Tests connectivity to a provider by attempting to list models.
// Accepts raw credentials in the request body (not from stored config).
// Any authenticated user can test — useful for validating before saving.
func TestProviderHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string            `json:"type"`
		Params map[string]string `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Type == "" {
		respondError(w, http.StatusBadRequest, "provider type is required")
		return
	}
	if req.Params == nil {
		req.Params = map[string]string{}
	}

	// Use a 15-second timeout for connectivity tests
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	models, err := provider.TestProviderConnection(ctx, req.Type, req.Params)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"model_count": len(models),
		"models":      models,
	})
}
