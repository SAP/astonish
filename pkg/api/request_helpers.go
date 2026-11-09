package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
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
	if principal, ok := execution.PrincipalFromContext(r.Context()); ok && principal.Subject != "" {
		return principal.Subject
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

// effectiveMergedCredentialStore returns personal-first, team-fallback
// credentials for runtime secret resolution (MCP env placeholders, tool
// execution). This intentionally ignores the ?scope= query parameter: that
// scope selects which *config* store to use (team/org/platform MCP servers),
// not which secrets the user may inject. Chat and apps always resolve this way.
func effectiveMergedCredentialStore(r *http.Request) store.CredentialStore {
	if r == nil {
		return nil
	}
	svc := store.FromRequest(r)
	if svc == nil {
		return nil
	}
	if svc.PersonalCredentials != nil || svc.Credentials != nil {
		return store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
	}
	return svc.Credentials
}

// credentialResolverForRequest returns a CredentialResolver for expanding
// {{CREDENTIAL:name:field}} placeholders (e.g. MCP server env at Test/start).
// Always personal-first → team fallback (same as Chat), regardless of MCP
// admin scope. Falls back to the personal-mode file store, then context.
func credentialResolverForRequest(r *http.Request) credentials.CredentialResolver {
	if r == nil {
		return nil
	}
	if cs := effectiveMergedCredentialStore(r); cs != nil {
		return credentials.NewStoreAdapter(cs)
	}
	if file := getAPICredentialStore(); file != nil {
		return file
	}
	return credentials.ResolverFromContext(r.Context())
}

// withRequestCredentialStore attaches the merged (personal-first) credential
// store to ctx so MCP discovery/Test can resolve env placeholders like Chat.
// No-op when no store is available.
func withRequestCredentialStore(ctx context.Context, r *http.Request) context.Context {
	if r == nil {
		return ctx
	}
	if cs := effectiveMergedCredentialStore(r); cs != nil {
		return store.WithCredentialStore(ctx, cs)
	}
	return ctx
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
	return chatruntime.LoadMCPConfig(r.Context(), store.FromRequest(r), effectiveAppConfig(r), chatruntime.FallbackAllowed)
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
		svc = &store.Services{}
	}

	// Only trusted single-tenant deployments may inherit host providers.
	// SQLite is multi-user too; missing team/platform stores are not permission
	// to inherit the operator's credentials.
	if appCfg.Storage.IsPlatformMode() {
		appCfg.Providers = nil
		appCfg.General.DefaultProvider = ""
		appCfg.General.DefaultModel = ""
	}

	// Resolve only explicitly supplied platform/org/team stores.
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
	mergeResolvedProviders(appCfg, resolved)
	if resolved.General.DefaultProvider != "" {
		appCfg.General.DefaultProvider = resolved.General.DefaultProvider
	}
	if resolved.General.DefaultModel != "" {
		appCfg.General.DefaultModel = resolved.General.DefaultModel
	}
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

// effectiveAppConfig shares background initialization's scoped resolution.
// Host YAML fallback is restricted to trusted single-tenant deployments.
func effectiveAppConfig(r *http.Request) *config.AppConfig {
	svc := store.FromRequest(r)
	return EffectiveAppConfigFromContext(r.Context(), svc != nil && svc.Mode == store.ModePlatform)
}

// mergeResolvedProviders overlays scoped providers on the permitted base.
// EffectiveAppConfigFromContext clears the host base in multi-user mode.
// DB entries win on a name collision in trusted single-tenant mode.
func mergeResolvedProviders(appCfg *config.AppConfig, resolved *config.AppConfig) {
	if len(resolved.Providers) == 0 {
		// Keep whatever config.yaml provided (may be nil).
		return
	}
	if appCfg.Providers == nil {
		appCfg.Providers = make(map[string]config.ProviderConfig, len(resolved.Providers))
	}
	for name, pCfg := range resolved.Providers {
		appCfg.Providers[name] = pCfg
	}
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
