package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
)

const perplexityWebSearchToolRef = "perplexity:perplexity_web_search"

// PerplexityProviderOption is a configured provider and the Perplexity/Sonar-like models it exposes.
type PerplexityProviderOption struct {
	Provider string   `json:"provider"`
	Type     string   `json:"type,omitempty"`
	Models   []string `json:"models"`
	Error    string   `json:"error,omitempty"`
}

// PerplexityWebSearchOptionsResponse is returned by GET /api/web-search/perplexity/options.
type PerplexityWebSearchOptionsResponse struct {
	Options []PerplexityProviderOption `json:"options"`
}

// PerplexityWebSearchConfigRequest is the body for PUT /api/web-search/perplexity/config.
type PerplexityWebSearchConfigRequest struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	SearchContextSize string `json:"search_context_size,omitempty"`
	MaxResults        int    `json:"max_results,omitempty"`
}

// GetPerplexityWebSearchOptionsHandler lists configured provider models that look like Perplexity/Sonar models.
func GetPerplexityWebSearchOptionsHandler(w http.ResponseWriter, r *http.Request) {
	appCfg := perplexityOptionsAppConfig(r)
	providerNames := make([]string, 0, len(appCfg.Providers))
	for name := range appCfg.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	options := make([]PerplexityProviderOption, 0)
	for _, name := range providerNames {
		provCfg := appCfg.Providers[name]
		models, err := provider.ListModelsForProvider(r.Context(), name, appCfg)
		opt := PerplexityProviderOption{
			Provider: name,
			Type:     config.GetProviderType(name, provCfg),
		}
		if err != nil {
			opt.Error = err.Error()
		} else {
			opt.Models = filterPerplexityModels(models)
		}
		options = append(options, opt)
	}

	respondJSON(w, http.StatusOK, PerplexityWebSearchOptionsResponse{Options: options})
}

// SavePerplexityWebSearchConfigHandler stores the platform-level model-backed Perplexity web search configuration.
func SavePerplexityWebSearchConfigHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.PlatformSettings == nil {
		respondError(w, http.StatusServiceUnavailable, "Platform settings not available")
		return
	}

	var req PerplexityWebSearchConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	if req.Provider == "" || req.Model == "" {
		respondError(w, http.StatusBadRequest, "provider and model are required")
		return
	}
	if !isPerplexityModel(req.Model) {
		respondError(w, http.StatusBadRequest, "selected model must contain perplexity, sonar, or pplx")
		return
	}

	appCfg := perplexityOptionsAppConfig(r)
	models, err := provider.ListModelsForProvider(r.Context(), req.Provider, appCfg)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Failed to list models for provider: "+err.Error())
		return
	}
	if !containsModel(models, req.Model) {
		respondError(w, http.StatusBadRequest, "selected model is not available for provider")
		return
	}

	settings, _ := svc.PlatformSettings.Get(r.Context())
	if settings == nil {
		settings = &store.PlatformSettings{}
	}
	settings.WebSearchTool = perplexityWebSearchToolRef
	settings.PerplexityWebSearch = &store.PerplexityWebSearchSettings{
		Provider:          req.Provider,
		Model:             req.Model,
		SearchContextSize: normalizeSearchContextSize(req.SearchContextSize),
		MaxResults:        normalizeMaxResults(req.MaxResults),
	}
	if err := svc.PlatformSettings.Save(r.Context(), settings); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save platform settings: "+err.Error())
		return
	}

	GetChatManager().Reset()
	respondJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"webSearchTool": perplexityWebSearchToolRef,
		"config":        settings.PerplexityWebSearch,
	})
}

// ClearPerplexityWebSearchConfigHandler removes platform Perplexity web search configuration.
func ClearPerplexityWebSearchConfigHandler(w http.ResponseWriter, r *http.Request) {
	if RequirePlatformAdmin(w, r) == nil {
		return
	}

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
	if settings == nil {
		settings = &store.PlatformSettings{}
	}
	settings.PerplexityWebSearch = nil
	if settings.WebSearchTool == perplexityWebSearchToolRef {
		settings.WebSearchTool = ""
	}
	if err := svc.PlatformSettings.Save(r.Context(), settings); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save platform settings: "+err.Error())
		return
	}

	GetChatManager().Reset()
	respondJSON(w, http.StatusOK, map[string]any{"status": "cleared"})
}

func perplexityOptionsAppConfig(r *http.Request) *config.AppConfig {
	if r.URL.Query().Get("scope") != "platform" {
		return effectiveAppConfig(r)
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.Mode != store.ModePlatform || svc.PlatformSettings == nil {
		return effectiveAppConfig(r)
	}
	settings, err := svc.PlatformSettings.Get(r.Context())
	if err != nil || settings == nil {
		return effectiveAppConfig(r)
	}
	return provider.ResolveEffectiveConfig(r.Context(), svc.PlatformSettings, nil, nil)
}

func filterPerplexityModels(models []string) []string {
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, model := range models {
		if isPerplexityModel(model) && !seen[model] {
			out = append(out, model)
			seen[model] = true
		}
	}
	sort.Strings(out)
	return out
}

func isPerplexityModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "perplexity") || strings.Contains(m, "sonar") || strings.Contains(m, "pplx")
}

func containsModel(models []string, want string) bool {
	for _, model := range models {
		if model == want {
			return true
		}
	}
	return false
}

func normalizeSearchContextSize(size string) string {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(size))
	default:
		return "medium"
	}
}

func normalizeMaxResults(n int) int {
	if n <= 0 {
		return 5
	}
	if n > 20 {
		return 20
	}
	return n
}
