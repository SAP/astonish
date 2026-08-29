// Package store defines the abstract storage interfaces for Astonish.
//
// The store layer provides a database-agnostic abstraction over all persistent
// data operations. Two implementations are planned:
//
//   - filestore: wraps the existing file-based storage (for personal/single-user mode)
//   - pgstore:   PostgreSQL-backed (for multi-tenant platform mode, future)
//
// In personal mode, the PlatformStore and TenantRouter are not used; all
// access goes through a single OrgDataStore backed by the local filesystem.
package store

import (
	"context"
	"database/sql"
	"time"
)

// PlatformStore manages cross-organization data: users, orgs, login sessions.
// Only used in platform (multi-tenant) mode.
type PlatformStore interface {
	Users() UserStore
	Organizations() OrganizationStore
	LoginSessions() LoginSessionStore
	OIDCProviders() OIDCProviderStore
	UserChannels() UserChannelStore
	Close() error
}

// TenantRouter routes requests to the correct organization's data store.
// In PostgreSQL mode, this switches to the org-specific database.
// Only used in platform (multi-tenant) mode.
type TenantRouter interface {
	// ForOrg returns a handle to the org's isolated data store.
	ForOrg(orgID string) (OrgDataStore, error)

	// ProvisionOrg creates a new org data store (database/HDI container) and
	// runs all necessary migrations.
	ProvisionOrg(ctx context.Context, orgID, slug string) error

	// DecommissionOrg removes an org's data store.
	DecommissionOrg(ctx context.Context, orgID string) error
}

// PlatformBackend is the combined interface used by platform-mode components
// (auth, channels, setup) that need access to both platform-level stores and
// tenant routing. Both pgstore.PGStore and sqlitestore.SQLiteStore implement this.
type PlatformBackend interface {
	PlatformStore
	TenantRouter
	// InstanceSuffix returns the instance suffix for database naming.
	// Returns empty string for SQLite mode (directory-based isolation).
	InstanceSuffix() string

	// --- Settings ---
	PlatformSettings() PlatformSettingsStore
	OrgSettings(orgSlug string) OrgSettingsStore
	PlatformMCPServers() MCPServerStore
	PlatformSkills() SkillStore
	PlatformSlideTemplates() SlideTemplateStore

	// --- Embeddings ---
	SetEmbedFunc(fn EmbedFunc)
	GetEmbedFunc() EmbedFunc

	// --- Sandbox ---
	SandboxLayers() LayerStore
	SandboxTemplates() SandboxTemplateStore

	// --- Secrets ---
	// SecretGetter returns a function that resolves secrets from the platform
	// secrets table. Used by daemon and API layers for provider keys, channel
	// tokens, and MCP server credentials.
	SecretGetter() func(string) string

	// --- Lifecycle ---
	// MigrateAll runs pending migrations on all databases (platform + org + team).
	MigrateAll(ctx context.Context) error

	// CleanupExpired removes expired transient records (device sessions, link codes).
	CleanupExpired(ctx context.Context) error

	// PlatformDB returns the raw *sql.DB for the platform database.
	// Used for health checks (ping). May return nil if not applicable.
	PlatformDB() *sql.DB
}

// OrgDataStore is the root of all data access within an organization.
// In personal mode, this is the only store needed (backed by the local filesystem).
type OrgDataStore interface {
	// ForTeam returns a TeamDataStore scoped to the given team.
	// In personal mode, teamSlug is ignored (there's only one implicit team).
	ForTeam(teamSlug string) TeamDataStore

	// ForUser returns a PersonalDataStore for the given user's private data.
	// In personal mode, userID is ignored (there's only one implicit user).
	ForUser(userID string) PersonalDataStore

	// Org-wide shared stores (public schema in PG, shared directory in file mode).
	OrgMemories() MemoryStore
	OrgSkills() SkillStore
	OrgSlideTemplates() SlideTemplateStore
	OrgMCPServers() MCPServerStore
	OrgA2AAgents() A2AAgentStore
	OrgNetworkPolicies() NetworkPolicyStore
	OrgApps() AppStore
	OrgAudit() AuditStore
	Teams() TeamManagementStore

	// Provisioning operations.
	ProvisionTeam(ctx context.Context, slug string) error
	ProvisionPersonalSchema(ctx context.Context, userID string) error
	DropTeamSchema(ctx context.Context, slug string) error

	Close() error
}

// TeamDataStore accesses a specific team's data.
type TeamDataStore interface {
	Sessions() SessionStore
	Memories() MemoryStore
	Credentials() CredentialStore
	Docs() DocsStore
	Apps() AppStore
	AppState() AppStateStore
	AppStateSQL() AppStateSQLStore
	Flows() FlowStore
	Skills() SkillStore
	MCPServers() MCPServerStore
	A2AAgents() A2AAgentStore
	NetworkPolicies() NetworkPolicyStore
	ScheduledJobs() SchedulerStore
	FleetTemplates() FleetTemplateStore
	FleetPlans() FleetPlanStore
	FleetSetupProfiles() FleetSetupProfileStore
	FleetSetupDrafts() FleetSetupDraftStore
	FleetRunStates() FleetRunStateStore
	FleetMailbox() FleetMailboxStore
	FleetTaskBoard() FleetTaskBoardStore
	DrillReports() DrillReportStore
	Settings() SettingsStore
	Audit() AuditStore

	SessionPin(ctx context.Context, sessionID string) (*SessionPin, error)
	SetSessionPin(ctx context.Context, sessionID, provider, model string) error
	AppPin(ctx context.Context, appSlug string) (*AppPin, error)
	SetAppPin(ctx context.Context, appSlug, provider, model string) error
}

// PersonalDataStore accesses a user's private data.
type PersonalDataStore interface {
	Memories() MemoryStore
	Apps() AppStore
	Sessions() SessionStore
	AppState() AppStateStore
	Flows() FlowStore
	Credentials() CredentialStore
	Docs() DocsStore
	ScheduledJobs() SchedulerStore

	PersonalSettings() PersonalSettingsStore
	SessionPin(ctx context.Context, sessionID string) (*SessionPin, error)
	SetSessionPin(ctx context.Context, sessionID, provider, model string) error
	AppPin(ctx context.Context, appSlug string) (*AppPin, error)
	SetAppPin(ctx context.Context, appSlug, provider, model string) error
}

// --------------------------------------------------------------------------
// Platform-level stores (multi-tenant only)
// --------------------------------------------------------------------------

// User represents a platform user.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"display_name"`
	PasswordHash string    `json:"-"`
	OIDCSubject  string    `json:"oidc_subject,omitempty"`
	OIDCIssuer   string    `json:"oidc_issuer,omitempty"`
	PlatformRole string    `json:"platform_role,omitempty"` // "superadmin" or "" (regular user)
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	LastLoginAt  time.Time `json:"last_login_at,omitempty"`
}

// Organization represents a tenant organization.
type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	DBName    string    `json:"db_name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// LoginSession represents an active login session.
type LoginSession struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	OrgID     string    `json:"org_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UserAgent string    `json:"user_agent,omitempty"`
	IPAddress string    `json:"ip_address,omitempty"`
}

// UserStore manages user records.
type UserStore interface {
	Create(ctx context.Context, user *User) error
	GetByID(ctx context.Context, id string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByOIDC(ctx context.Context, issuer, subject string) (*User, error)
	Update(ctx context.Context, user *User) error
	Delete(ctx context.Context, id string) error

	// List returns all users, ordered by email.
	List(ctx context.Context) ([]*User, error)

	// ListByOrg returns all users who are members of the given org, with their role.
	ListByOrg(ctx context.Context, orgID string) ([]*UserWithRole, error)

	// SetPlatformRole sets or clears the platform-level role for a user.
	// Pass "superadmin" to promote, or "" to demote.
	SetPlatformRole(ctx context.Context, userID, role string) error

	// CountByPlatformRole returns the number of users with the given platform role.
	CountByPlatformRole(ctx context.Context, role string) (int, error)
}

// OrganizationStore manages organization records.
type OrganizationStore interface {
	Create(ctx context.Context, org *Organization) error
	GetByID(ctx context.Context, id string) (*Organization, error)
	GetBySlug(ctx context.Context, slug string) (*Organization, error)
	List(ctx context.Context) ([]*Organization, error)
	Update(ctx context.Context, org *Organization) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)

	// Org membership management.
	AddMember(ctx context.Context, userID, orgID, role string) error
	SetMemberRole(ctx context.Context, userID, orgID, role string) error
	RemoveMember(ctx context.Context, userID, orgID string) error
	GetUserOrgs(ctx context.Context, userID string) ([]*OrgMembership, error)
	GetMemberRole(ctx context.Context, userID, orgID string) (string, error)

	// ListMembers returns all users who are members of the given org, with their role.
	ListMembers(ctx context.Context, orgID string) ([]*UserWithRole, error)
}

// OrgMembership represents a user's membership in an organization.
type OrgMembership struct {
	UserID    string    `json:"user_id"`
	OrgID     string    `json:"org_id"`
	OrgSlug   string    `json:"org_slug"`
	OrgName   string    `json:"org_name"`
	OrgStatus string    `json:"org_status"` // active, suspended, decommissioned
	Role      string    `json:"role"`       // owner, admin, member
	JoinedAt  time.Time `json:"joined_at"`
}

// UserWithRole is a User with their role within an organization.
type UserWithRole struct {
	User
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// LoginSessionStore manages login sessions.
type LoginSessionStore interface {
	Create(ctx context.Context, session *LoginSession) error
	Validate(ctx context.Context, tokenHash string) (*LoginSession, error)
	Delete(ctx context.Context, tokenHash string) error
	DeleteExpired(ctx context.Context) error
}

// --------------------------------------------------------------------------
// OIDC Provider store (platform-level)
// --------------------------------------------------------------------------

// OIDCProvider represents a configured OIDC identity provider.
type OIDCProvider struct {
	ID           string    `json:"id"`
	OrgID        string    `json:"org_id,omitempty"` // Empty = platform-wide
	Name         string    `json:"name"`
	IssuerURL    string    `json:"issuer_url"`
	DiscoveryURL string    `json:"discovery_url,omitempty"` // Base URL for .well-known discovery (if different from issuer)
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"` // Omitted in list responses
	Scopes       []string  `json:"scopes"`
	TeamClaim    string    `json:"team_claim,omitempty"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// OIDCProviderStore manages OIDC provider configuration records.
type OIDCProviderStore interface {
	Create(ctx context.Context, provider *OIDCProvider) error
	GetByID(ctx context.Context, id string) (*OIDCProvider, error)
	Update(ctx context.Context, provider *OIDCProvider) error
	Delete(ctx context.Context, id string) error

	// List returns all OIDC providers, optionally filtered by org.
	// Pass empty orgID to list platform-wide providers only.
	// Pass "*" to list all providers across all orgs.
	List(ctx context.Context, orgID string) ([]*OIDCProvider, error)

	// ListEnabled returns all enabled providers (platform-wide + for a specific org).
	ListEnabled(ctx context.Context, orgID string) ([]*OIDCProvider, error)

	// GetByIssuer returns the provider matching the given issuer URL (enabled only).
	GetByIssuer(ctx context.Context, issuerURL string) (*OIDCProvider, error)
}

// --------------------------------------------------------------------------
// User channel linking (external messaging accounts)
// --------------------------------------------------------------------------

// UserChannel represents a link between a platform user and an external
// messaging channel (e.g., Telegram user ID, email address).
type UserChannel struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	ChannelType string     `json:"channel_type"` // "telegram", "email"
	ExternalID  string     `json:"external_id"`  // TG user ID or email address
	DisplayName string     `json:"display_name"` // @username or label
	Enabled     bool       `json:"enabled"`
	Verified    bool       `json:"verified"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// UserChannelStore manages user-channel links in the platform database.
type UserChannelStore interface {
	// Link creates a new channel link for a user.
	Link(ctx context.Context, ch *UserChannel) error

	// Unlink removes a channel link by ID.
	Unlink(ctx context.Context, id string) error

	// GetByID returns a specific channel link.
	GetByID(ctx context.Context, id string) (*UserChannel, error)

	// GetByExternalID looks up a channel link by its external identifier.
	// Returns nil, nil if not found.
	GetByExternalID(ctx context.Context, channelType, externalID string) (*UserChannel, error)

	// ListByUser returns all channel links for a given user.
	ListByUser(ctx context.Context, userID string) ([]*UserChannel, error)

	// ListByChannelType returns all verified+enabled links for a channel type.
	// Used to build the dynamic allowlist for Telegram.
	ListByChannelType(ctx context.Context, channelType string) ([]*UserChannel, error)

	// ListByUsers returns all channel links for a set of user IDs and channel type.
	// Used for delivery resolution (find all Telegram targets for a list of team members).
	ListByUsers(ctx context.Context, userIDs []string, channelType string) ([]*UserChannel, error)

	// Update updates mutable fields (display_name, enabled).
	Update(ctx context.Context, ch *UserChannel) error

	// Verify marks a channel link as verified.
	Verify(ctx context.Context, id string) error
}

// Team represents a team within an organization.
type Team struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Slug       string    `json:"slug"`
	SchemaName string    `json:"schema_name"`
	CreatedAt  time.Time `json:"created_at"`
}

// TeamMembership represents a user's membership in a team.
type TeamMembership struct {
	UserID      string    `json:"user_id"`
	TeamID      string    `json:"team_id"`
	Role        string    `json:"role"` // admin, member, viewer
	JoinedAt    time.Time `json:"joined_at"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
}

// TeamManagementStore manages teams and memberships within an org.
type TeamManagementStore interface {
	CreateTeam(ctx context.Context, team *Team) error
	GetTeam(ctx context.Context, id string) (*Team, error)
	GetTeamBySlug(ctx context.Context, slug string) (*Team, error)
	ListTeams(ctx context.Context) ([]*Team, error)
	ListTeamsForUser(ctx context.Context, userID string) ([]*Team, error)
	DeleteTeam(ctx context.Context, id string) error
	RenameTeam(ctx context.Context, id string, name string) error
	CountTeams(ctx context.Context) (int, error)

	AddMember(ctx context.Context, membership *TeamMembership) error
	RemoveMember(ctx context.Context, userID, teamID string) error
	SetRole(ctx context.Context, userID, teamID, role string) error
	ListMembers(ctx context.Context, teamID string) ([]*TeamMembership, error)
	CountMembers(ctx context.Context, teamID string) (int, error)
	GetUserTeams(ctx context.Context, userID string) ([]*TeamMembership, error)
	IsTeamMember(ctx context.Context, userID, teamSlug string) (bool, error)
	GetMemberRole(ctx context.Context, userID, teamID string) (string, error)
}

// --------------------------------------------------------------------------
// Audit store
// --------------------------------------------------------------------------

// AuditEntry represents an immutable audit log entry.
type AuditEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	UserID    string    `json:"user_id"`
	TeamID    string    `json:"team_id,omitempty"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	Detail    any       `json:"detail,omitempty"`
	IPAddress string    `json:"ip_address,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
}

// AuditStore provides append-only audit logging.
type AuditStore interface {
	// Log appends an audit entry. This is the only write operation.
	Log(ctx context.Context, entry *AuditEntry) error

	// Query retrieves audit entries matching the filter.
	Query(ctx context.Context, filter AuditFilter) ([]*AuditEntry, error)
}

// AuditFilter specifies criteria for querying audit entries.
type AuditFilter struct {
	UserID   string
	Action   string
	Resource string
	Since    time.Time
	Until    time.Time
	Limit    int
	Offset   int
}

// --------------------------------------------------------------------------
// Link code store (channel linking)
// --------------------------------------------------------------------------

// LinkCode represents a pending channel-linking verification code.
type LinkCode struct {
	Code      string    `json:"code"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Channel   string    `json:"channel"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LinkCodeStore manages pending link codes for channel verification flows.
// Both pgstore and sqlitestore implement this interface.
type LinkCodeStore interface {
	Generate(ctx context.Context, code, userID, email, channel string) error
	GenerateWithTTL(ctx context.Context, code, userID, email, channel string, ttl time.Duration) error
	Consume(ctx context.Context, code string) (*LinkCode, error)
	Cleanup(ctx context.Context) error
}
