package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
)

// effectiveUserID returns the user ID for the current request.
//
// In platform mode, this is the authenticated user's ID from the JWT token.
// In personal mode, this falls back to the hardcoded "studio_user" constant.
//
// All handlers that create or query user-scoped data (sessions, apps, etc.)
// should call this instead of using studioChatUserID directly.
func effectiveUserID(r *http.Request) string {
	if pu := GetPlatformUser(r); pu != nil {
		return pu.ID
	}
	return studioChatUserID
}

// effectiveCredentialStore returns the credential store for the current request,
// optionally scoped by the "scope" query parameter.
//
// Scope values:
//   - "personal": returns the user's personal credential store
//   - "team": returns the team-scoped credential store
//   - "" (empty/omitted): returns a merged store (personal-first, team-fallback)
//
// In personal mode (no platform), always returns the file-based singleton.
func effectiveCredentialStore(r *http.Request) store.CredentialStore {
	scope := r.URL.Query().Get("scope")
	return effectiveCredentialStoreScoped(r, scope)
}

// effectiveCredentialStoreScoped returns the credential store for the given scope.
func effectiveCredentialStoreScoped(r *http.Request, scope string) store.CredentialStore {
	svc := store.FromRequest(r)
	if svc == nil {
		return nil
	}
	switch scope {
	case "personal":
		return svc.PersonalCredentials
	case "team":
		return svc.Credentials
	default:
		// Merged: personal-first, team-fallback
		if svc.PersonalCredentials != nil || svc.Credentials != nil {
			return store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
		}
		return svc.Credentials
	}
}

// effectivePersonalCredentialStore returns just the personal credential store.
func effectivePersonalCredentialStore(r *http.Request) store.CredentialStore {
	if svc := store.FromRequest(r); svc != nil && svc.PersonalCredentials != nil {
		return svc.PersonalCredentials
	}
	return nil
}

// effectiveTeamCredentialStore returns just the team credential store.
func effectiveTeamCredentialStore(r *http.Request) store.CredentialStore {
	if svc := store.FromRequest(r); svc != nil && svc.Credentials != nil {
		return svc.Credentials
	}
	return nil
}

// isPlatformMode checks whether the current request is running in platform mode
// by inspecting the Services context. Returns false for personal mode or when
// Services is not available.
// Used by handlers in tasks 4.3-4.8 for platform-mode branching.
func isPlatformMode(r *http.Request) bool {
	svc := store.FromRequest(r)
	return svc != nil && svc.Mode == store.ModePlatform
}

// effectiveTeamSlug returns the team slug for the current request.
// In platform mode, this is read from the TenantContext (set by auth middleware).
// In personal mode, this returns an empty string.
func effectiveTeamSlug(r *http.Request) string {
	if tc := store.TenantContextFrom(r.Context()); tc != nil {
		return tc.TeamSlug
	}
	return ""
}

// effectiveMCPStore returns the MCP server store for the current request based
// on the "scope" query parameter.
//
// Scope values:
//   - "team": returns the team-scoped MCP server store
//   - "platform": returns the platform-wide MCP server store (superadmin only)
//   - "org" or "" (empty): returns the org-level MCP server store
//
// Returns nil if not in platform mode or if the requested store is not available.
func effectiveMCPStore(r *http.Request) store.MCPServerStore {
	svc := store.FromRequest(r)
	if svc == nil || svc.Mode != store.ModePlatform {
		return nil
	}
	scope := r.URL.Query().Get("scope")
	switch scope {
	case "team":
		return svc.TeamMCPServers
	case "platform":
		return svc.PlatformMCPServers
	default:
		return svc.MCPServers
	}
}

// DefaultUserID returns the default user ID used in personal mode and for
// system-initiated operations (scheduled fleet sessions, recovery, etc.).
// External packages that need the default user ID should call this instead of
// hardcoding the string.
func DefaultUserID() string {
	return studioChatUserID
}

// storeMetaToResponse converts a store.SessionMeta to the API response type.
func storeMetaToResponse(m store.SessionMeta) StudioSessionResponse {
	return StudioSessionResponse{
		ID:           m.ID,
		Title:        m.Title,
		CreatedAt:    m.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    m.UpdatedAt.Format(time.RFC3339),
		MessageCount: m.MessageCount,
		FleetKey:     m.FleetKey,
		FleetName:    m.FleetName,
		IssueNumber:  m.IssueNumber,
		Repo:         m.Repo,
	}
}

// loadMCPConfigForRequest returns the merged MCP server config appropriate for
// the request context. In platform mode it reads from the platform+org+team DB
// stores with a three-tier cascade (platform base → org overrides → team overrides);
// in personal mode it reads from the filesystem.
func loadMCPConfigForRequest(r *http.Request) *config.MCPConfig {
	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform {
		merged := make(map[string]config.MCPServerConfig)

		// Tier 1: Platform-level (base, inherited by all orgs/teams)
		if svc.PlatformMCPServers != nil {
			platformServers, err := svc.PlatformMCPServers.List(r.Context())
			if err == nil {
				for _, s := range platformServers {
					merged[s.Name] = config.MCPServerConfig{
						Command:   s.Command,
						Args:      s.Args,
						Env:       s.Env,
						Transport: s.Transport,
						URL:       s.URL,
						Enabled:   s.Enabled,
					}
				}
			}
		}

		// Tier 2: Org-level (overrides platform by name)
		if svc.MCPServers != nil {
			orgServers, err := svc.MCPServers.List(r.Context())
			if err == nil {
				for _, s := range orgServers {
					merged[s.Name] = config.MCPServerConfig{
						Command:   s.Command,
						Args:      s.Args,
						Env:       s.Env,
						Transport: s.Transport,
						URL:       s.URL,
						Enabled:   s.Enabled,
					}
				}
			}
		}

		// Tier 3: Team-level (overrides org+platform by name)
		if svc.TeamMCPServers != nil {
			teamServers, err := svc.TeamMCPServers.List(r.Context())
			if err == nil {
				for _, s := range teamServers {
					merged[s.Name] = config.MCPServerConfig{
						Command:   s.Command,
						Args:      s.Args,
						Env:       s.Env,
						Transport: s.Transport,
						URL:       s.URL,
						Enabled:   s.Enabled,
					}
				}
			}
		}
		cfg := &config.MCPConfig{MCPServers: merged}
		// Merge standard servers (Tavily, Brave, etc.) so they appear alongside
		// user-configured DB servers in platform mode.
		// Pass the request-scoped effective config so team-level WebSearchTool is honored.
		config.MergeStandardServersWithConfig(cfg, effectiveAppConfig(r))
		return cfg
	}

	// No platform context (personal mode) — fall back to local mcp_config.json
	cfg, err := config.LoadMCPConfig()
	if err != nil || cfg == nil {
		return &config.MCPConfig{MCPServers: make(map[string]config.MCPServerConfig)}
	}
	return cfg
}

// EffectiveAppConfigFromContext builds the effective application configuration using
// the 3-tier provider cascade from the context's store.Services.
// Use this when you have a context with store.Services but no *http.Request
// (e.g., chat agent initialization, background tasks).
// In personal mode (isPlatformMode=false), returns config.LoadAppConfig() directly.
func EffectiveAppConfigFromContext(ctx context.Context, isPlatformMode bool) *config.AppConfig {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		slog.Warn("failed to load app config", "error", err)
		appCfg = &config.AppConfig{}
	}

	if !isPlatformMode {
		return appCfg
	}

	svc := store.FromContext(ctx)
	if svc == nil {
		return appCfg
	}

	// Platform mode: providers come exclusively from the database.
	// Delegate to the shared provider resolution function.
	resolved := provider.ResolveEffectiveConfig(ctx, svc.PlatformSettings, svc.OrgSettings, svc.Settings)

	// Layer 4: User default (personal settings override team/org/platform).
	var userDefault provider.UserDefaultSettings
	if svc.PersonalSettings != nil {
		if ps, err := svc.PersonalSettings.Get(ctx); err == nil {
			userDefault = ps
		}
	}
	resolved = provider.ApplyUserDefault(resolved, userDefault)
	resolved = provider.ApplyProviderOverride(resolved, "", "")
	appCfg.Providers = resolved.Providers
	appCfg.General.DefaultProvider = resolved.General.DefaultProvider
	appCfg.General.DefaultModel = resolved.General.DefaultModel
	appCfg.General.WebSearchTool = resolved.General.WebSearchTool
	appCfg.General.WebExtractTool = resolved.General.WebExtractTool
	appCfg.PerplexityWebSearch = resolved.PerplexityWebSearch

	// Apply additional non-provider team settings (web tools, context length, etc.)
	if svc.Settings != nil {
		teamSettings, tsErr := svc.Settings.Get(ctx)
		if tsErr == nil && teamSettings != nil {
			applyTeamNonProviderSettings(appCfg, teamSettings)
		}
	}

	return appCfg
}

// effectiveAppConfig returns the application configuration appropriate for the request.
// In platform mode, providers come exclusively from the database (3-tier cascade):
//  1. Platform settings (visible to all orgs/teams)
//  2. Org settings (visible to all teams in the org)
//  3. Team settings (specific to this team)
//
// config.yaml providers are NOT used in platform mode.
// In personal mode, it simply returns config.LoadAppConfig().
func effectiveAppConfig(r *http.Request) *config.AppConfig {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		slog.Warn("failed to load app config", "error", err)
		appCfg = &config.AppConfig{}
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.Mode != store.ModePlatform {
		return appCfg
	}

	ctx := r.Context()

	// Platform mode: providers come exclusively from the database.
	// Delegate to the shared provider resolution function.
	resolved := provider.ResolveEffectiveConfig(ctx, svc.PlatformSettings, svc.OrgSettings, svc.Settings)

	// Layer 4: User default (personal settings override team/org/platform).
	var userDefault provider.UserDefaultSettings
	if svc.PersonalSettings != nil {
		if ps, err := svc.PersonalSettings.Get(ctx); err == nil {
			userDefault = ps
		}
	}
	resolved = provider.ApplyUserDefault(resolved, userDefault)
	resolved = provider.ApplyProviderOverride(resolved, "", "")
	appCfg.Providers = resolved.Providers
	appCfg.General.DefaultProvider = resolved.General.DefaultProvider
	appCfg.General.DefaultModel = resolved.General.DefaultModel
	appCfg.General.WebSearchTool = resolved.General.WebSearchTool
	appCfg.General.WebExtractTool = resolved.General.WebExtractTool
	appCfg.PerplexityWebSearch = resolved.PerplexityWebSearch

	// Apply additional non-provider team settings (web tools, context length, etc.)
	if svc.Settings != nil {
		teamSettings, tsErr := svc.Settings.Get(ctx)
		if tsErr == nil && teamSettings != nil {
			applyTeamNonProviderSettings(appCfg, teamSettings)
		}
	}

	return appCfg
}

// applyTeamNonProviderSettings overlays team-level non-provider settings from the DB
// onto the host AppConfig. Provider settings are handled by provider.ResolveEffectiveConfig.
// Only non-zero/non-empty team values override the host defaults.
func applyTeamNonProviderSettings(appCfg *config.AppConfig, ts *store.TeamSettings) {
	// Team-specific non-provider settings.
	if ts.WebSearchTool != "" {
		appCfg.General.WebSearchTool = ts.WebSearchTool
	}
	if ts.WebExtractTool != "" {
		appCfg.General.WebExtractTool = ts.WebExtractTool
	}
	if ts.ContextLength > 0 {
		appCfg.General.ContextLength = ts.ContextLength
	}

	// Overlay web servers: team web server configs replace host configs
	if len(ts.WebServers) > 0 {
		if appCfg.WebServers == nil {
			appCfg.WebServers = make(map[string]config.WebServerConfig)
		}
		for name, ws := range ts.WebServers {
			appCfg.WebServers[name] = config.WebServerConfig{
				APIKey:    ws["api_key"],
				Installed: ws["installed"] == "true",
			}
		}
	}

	// Overlay memory settings
	if ts.MemoryProvider != "" {
		appCfg.Memory.Embedding.Provider = ts.MemoryProvider
	}
	if ts.MemoryModel != "" {
		appCfg.Memory.Embedding.Model = ts.MemoryModel
	}
}
