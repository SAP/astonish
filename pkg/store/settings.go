package store

import "context"

// ProviderConfig holds a single provider's configuration fields.
// Keys include: type, base_url, api_key, client_id, client_secret, auth_url,
// resource_group, etc. Secret fields are stored encrypted and scrubbed from
// the JSONB on save; they are re-injected at read time for runtime use.
type ProviderConfig = map[string]string

// PlatformSettings represents platform-wide configuration visible to ALL
// organizations and teams. Stored in the platform_settings table.
type PlatformSettings struct {
	// Providers maps provider instance names to their configuration.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// DefaultProvider is the platform-wide default provider instance name.
	DefaultProvider string `json:"default_provider,omitempty"`

	// DefaultModel is the platform-wide default model ID.
	DefaultModel string `json:"default_model,omitempty"`

	// DefaultBrandTheme is the instance-wide Studio brand pack (nova, aster, …).
	// Used on the login screen and as the fallback when a user has no preference.
	// Empty string means product default (classic).
	DefaultBrandTheme string `json:"default_brand_theme,omitempty"`

	// WebSearchTool is the platform-wide configured web search tool.
	WebSearchTool string `json:"web_search_tool,omitempty"`

	// WebExtractTool is the platform-wide configured web extraction tool.
	WebExtractTool string `json:"web_extract_tool,omitempty"`

	// PerplexityWebSearch configures the model-backed Perplexity/Sonar search tool.
	PerplexityWebSearch *PerplexityWebSearchSettings `json:"perplexity_web_search,omitempty"`

	// Channels holds per-channel-type configuration (non-secret fields).
	// Secrets (tokens, passwords) are stored separately in platform_secrets.
	Channels *PlatformChannelSettings `json:"channels,omitempty"`

	// Auth holds platform-wide authentication policy settings.
	// These override the corresponding YAML config values when set.
	Auth *PlatformAuthSettings `json:"auth,omitempty"`
}

// PlatformAuthSettings holds platform-wide authentication policy that can be
// changed at runtime via the Platform Admin UI. When a field is non-nil it
// overrides the corresponding value from the YAML config file.
// PerplexityWebSearchSettings stores platform-wide configuration for the model-backed
// Perplexity/Sonar web search tool.
type PerplexityWebSearchSettings struct {
	Provider          string `json:"provider,omitempty"`
	Model             string `json:"model,omitempty"`
	SearchContextSize string `json:"search_context_size,omitempty"`
	MaxResults        int    `json:"max_results,omitempty"`
}

type PlatformAuthSettings struct {
	// AllowRegistration controls whether new users can self-register.
	// nil means "use YAML config default" (which itself defaults to true).
	AllowRegistration *bool `json:"allow_registration,omitempty"`

	// RequireEmailVerification controls whether self-registered users must
	// verify their email before their account becomes active.
	// nil means "use YAML config default" (which itself defaults to true).
	RequireEmailVerification *bool `json:"require_email_verification,omitempty"`

	// DevEnvironment marks this instance as a development/staging environment.
	// When true, outbound emails include a warning banner indicating instability.
	// nil means false (production by default).
	DevEnvironment *bool `json:"dev_environment,omitempty"`
}

// PlatformChannelSettings groups configuration for all supported channel adapters.
type PlatformChannelSettings struct {
	Telegram *PlatformTelegramConfig `json:"telegram,omitempty"`
	Email    *PlatformEmailConfig    `json:"email,omitempty"`
	Slack    *PlatformSlackConfig    `json:"slack,omitempty"`
}

// PlatformTelegramConfig holds non-secret Telegram channel settings.
type PlatformTelegramConfig struct {
	Enabled bool `json:"enabled"`
}

// PlatformEmailConfig holds non-secret Email channel settings.
type PlatformEmailConfig struct {
	Enabled      bool   `json:"enabled"`
	Provider     string `json:"provider,omitempty"`       // "imap" (default), "gmail", or "msgraph"
	IMAPServer   string `json:"imap_server"`              // e.g. "imap.gmail.com:993"
	SMTPServer   string `json:"smtp_server"`              // e.g. "smtp.gmail.com:587"
	Address      string `json:"address"`                  // agent's email address
	Username     string `json:"username,omitempty"`       // login username (defaults to address)
	Credential   string `json:"credential,omitempty"`     // credential store name (for msgraph provider)
	PollInterval int    `json:"poll_interval,omitempty"`  // seconds, default 30
	Folder       string `json:"folder,omitempty"`         // default "INBOX"
	MarkRead     *bool  `json:"mark_read,omitempty"`      // default true
	MaxBodyChars int    `json:"max_body_chars,omitempty"` // default 50000
}

// PlatformSlackConfig holds non-secret Slack channel settings.
type PlatformSlackConfig struct {
	Enabled     bool   `json:"enabled"`
	Mode        string `json:"mode,omitempty"`         // "socket" (default) or "events"
	AppID       string `json:"app_id,omitempty"`       // Slack App ID for manifest updates
	ConfigToken string `json:"config_token,omitempty"` // Deprecated plaintext fallback; prefer platform secret
	CommandURL  string `json:"command_url,omitempty"`  // HTTPS endpoint for slash commands
}

// OrgSettings represents organization-wide configuration visible to all
// teams within the org. Stored in organizations.settings JSONB column.
type OrgSettings struct {
	// Providers maps provider instance names to their configuration.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// DefaultProvider overrides the platform default for this org.
	DefaultProvider string `json:"default_provider,omitempty"`

	// DefaultModel overrides the platform default for this org.
	DefaultModel string `json:"default_model,omitempty"`
}

// TeamSettings represents the team-level subset of application configuration
// that can be independently configured per-team in platform mode.
// These settings are stored in the teams.settings JSONB column.
type TeamSettings struct {
	// Providers maps provider instance names to their configuration.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// DefaultProvider overrides the org/platform default for this team.
	DefaultProvider string `json:"default_provider,omitempty"`

	// DefaultModel overrides the org/platform default for this team.
	DefaultModel string `json:"default_model,omitempty"`

	// WebSearchTool is the configured web search tool.
	WebSearchTool string `json:"web_search_tool,omitempty"`

	// WebExtractTool is the configured web extraction tool.
	WebExtractTool string `json:"web_extract_tool,omitempty"`

	// ContextLength overrides context window size (tokens).
	ContextLength int `json:"context_length,omitempty"`

	// WebServers holds web tool configurations (Tavily, Brave, Firecrawl keys and state).
	WebServers map[string]map[string]string `json:"web_servers,omitempty"`

	// MemoryProvider is the embedding provider for memory indexing.
	MemoryProvider string `json:"memory_provider,omitempty"`

	// MemoryModel is the embedding model for memory indexing.
	MemoryModel string `json:"memory_model,omitempty"`

	// TemplateName is the sandbox container template name for this team.
	// When set, all fleet sessions for this team use this template instead of @base.
	// Format: "team-<slug>" (e.g., "team-general").
	TemplateName string `json:"template_name,omitempty"`

	// DisabledTools is a list of built-in tool names that are disabled for this team.
	// Tools in this list will not be available to the agent when serving this team's requests.
	// Only applies in platform mode. Empty = all tools available (default).
	DisabledTools []string `json:"disabled_tools,omitempty"`
}

// PlatformSettingsStore provides read/write access to platform-level settings.
// These are visible to all organizations and teams.
type PlatformSettingsStore interface {
	// Get returns the current platform settings.
	Get(ctx context.Context) (*PlatformSettings, error)

	// Save persists the platform settings.
	Save(ctx context.Context, settings *PlatformSettings) error
}

// OrgSettingsStore provides read/write access to org-level settings.
// These are visible to all teams within the organization.
type OrgSettingsStore interface {
	// Get returns the current org settings.
	Get(ctx context.Context) (*OrgSettings, error)

	// Save persists the org settings.
	Save(ctx context.Context, settings *OrgSettings) error
}

// SettingsStore provides read/write access to team-level settings.
type SettingsStore interface {
	// Get returns the current team settings.
	Get(ctx context.Context) (*TeamSettings, error)

	// Save persists the team settings.
	Save(ctx context.Context, settings *TeamSettings) error
}

// PersonalSettings represents per-user default configuration in personal scope.
// It is the user-level layer of the cascading provider/model default chain
// (platform → org → team → personal → app pin → session pin).
//
// Storage model: one row per user_id. In personal-mode SQLite where user_id
// is often NULL, uuid.Nil (all-zeros) is used as a sentinel for the single
// local user (see DECISION-1 in .omo/plans/per-chat-app-model-pin.md).
type PersonalSettings struct {
	// DefaultProvider is the user-preferred provider instance name.
	// Empty string means "inherit from the enclosing scope" (team/org/platform).
	DefaultProvider string `json:"default_provider,omitempty"`

	// DefaultModel is the user-preferred model ID.
	// Empty string means "inherit from the enclosing scope".
	DefaultModel string `json:"default_model,omitempty"`

	// BrandTheme is the user's Studio brand pack preference (nova, aster, …).
	// Empty string means inherit platform DefaultBrandTheme (then product default).
	BrandTheme string `json:"brand_theme,omitempty"`
}

// GetDefaultProvider implements provider.UserDefaultSettings.
func (ps *PersonalSettings) GetDefaultProvider() string {
	if ps == nil {
		return ""
	}
	return ps.DefaultProvider
}

// GetDefaultModel implements provider.UserDefaultSettings.
func (ps *PersonalSettings) GetDefaultModel() string {
	if ps == nil {
		return ""
	}
	return ps.DefaultModel
}

// PersonalSettingsStore provides read/write access to per-user personal
// settings. Personal scope only — never exposed at org or team scope.
type PersonalSettingsStore interface {
	// Get returns the current personal settings for the caller.
	// Returns a zero-value *PersonalSettings (not nil, not an error) when no
	// row exists yet — this keeps callers on the inheritance path without
	// forcing them to distinguish "unset" from "empty string".
	Get(ctx context.Context) (*PersonalSettings, error)

	// Save persists the personal settings for the caller (upsert semantics).
	Save(ctx context.Context, settings *PersonalSettings) error
}

// SessionPin holds a per-session provider/model override — the second-to-last
// layer of the cascading provider/model default chain
// (platform → org → team → personal → app pin → session pin).
//
// Empty Provider / Model = "not pinned, inherit from the enclosing cascade".
// The router-side setter treats empty strings as "clear the pin" (NULL in DB).
type SessionPin struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// AppPin holds a per-app provider/model override — sits above session-pin in
// the cascading chain. Empty strings mean "not pinned, inherit".
type AppPin struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}
