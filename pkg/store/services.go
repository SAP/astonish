package store

// Services is the central dependency container for Astonish.
//
// It replaces the package-level Set*/Get* globals that were previously used
// to wire dependencies between the daemon, launchers, API handlers, and tools.
//
// In personal mode, all stores are backed by the local filesystem.
// In platform mode, stores are backed by PostgreSQL with per-org/team scoping.
//
// Services is created once at startup (in daemon.Run or launcher entry points)
// and passed to all consumers via function parameters or request context.
type Services struct {
	// Mode indicates the deployment mode.
	Mode DeploymentMode

	// Sessions provides access to the team-scoped session store.
	// In platform mode this holds fleet sub-sessions (team-shared).
	// Regular chat sessions use PersonalSessions instead.
	Sessions SessionStore

	// PersonalSessions provides access to the user's private session store.
	// Regular chat sessions are always created here. Fleet sub-sessions
	// remain in the team-scoped Sessions store.
	PersonalSessions SessionStore

	// Memory provides access to the vector + BM25 memory search system.
	Memory MemoryStore

	// MemoryMgr provides higher-level memory operations (load/append MEMORY.md).
	MemoryMgr MemoryManager

	// Credentials provides access to the team-scoped encrypted credential store.
	// In platform mode, these are credentials shared across the team (app-to-app auth).
	Credentials CredentialStore

	// PersonalCredentials provides access to the user's private credential store.
	// Credentials saved from chat go here by default (user identity credentials).
	// Users can explicitly publish credentials to team when sharing is needed.
	PersonalCredentials CredentialStore

	// Docs provides access to team-scoped slide decks.
	Docs DocsStore

	// PersonalDocs provides access to the user's private slide decks.
	PersonalDocs DocsStore

	// Apps provides access to team-shared generative UI app definitions.
	// In platform mode, these are apps that have been published to the team.
	Apps AppStore

	// PersonalApps provides access to the user's private app definitions.
	// Apps are created here by default and explicitly published to team when ready.
	PersonalApps AppStore

	// AppState provides access to per-app persistent state.
	AppState AppStateStore

	// AppStateSQL provides raw SQL execution against per-app PostgreSQL schemas.
	// Only populated in platform mode; nil in personal mode (SQLite used directly).
	AppStateSQL AppStateSQLStore

	// Flows provides access to team-shared flow/agent definitions.
	// In platform mode, these are flows that have been published to the team.
	Flows FlowStore

	// PersonalFlows provides access to the user's private flow/agent definitions.
	// Flows are created here by default and explicitly published to team when ready.
	PersonalFlows FlowStore

	// PersonalSettings provides access to per-user default configuration
	// (default provider / default model). Personal scope only — there is no
	// team/org equivalent (DECISION-1 in .omo/plans/per-chat-app-model-pin.md).
	// Nil in personal mode when no user context has been established.
	PersonalSettings PersonalSettingsStore

	// Scheduler provides access to team-scoped scheduled job persistence.
	// Shared team automation; runs with team credentials only.
	Scheduler SchedulerStore

	// PersonalScheduler provides access to the user's private scheduled jobs.
	// Personal automation; runs with MergedCredentialStore(personal, team).
	// Nil when no user context is established.
	PersonalScheduler SchedulerStore

	// FleetTemplates provides access to fleet template definitions.
	FleetTemplates FleetTemplateStore

	// FleetPlans provides access to fleet plan definitions.
	FleetPlans FleetPlanStore

	// FleetSetupProfiles provides reusable plan setup profile definitions.
	FleetSetupProfiles FleetSetupProfileStore

	// FleetSetupDrafts stores in-progress plan setup state.
	FleetSetupDrafts FleetSetupDraftStore

	// FleetRunStates persists fleet session runtime state for recovery.
	FleetRunStates FleetRunStateStore

	// FleetMailbox persists per-recipient fleet handoff messages.
	FleetMailbox FleetMailboxStore

	// FleetTaskBoard persists fleet session task-board entries.
	FleetTaskBoard FleetTaskBoardStore

	// Skills provides access to org-level skill definitions.
	// In platform mode, these are skills shared across all teams in the org.
	Skills SkillStore

	// TeamSkills provides access to team-scoped skill definitions.
	// Team skills override org skills of the same name. Team admins manage these.
	TeamSkills SkillStore

	// MCPServers provides access to org-level MCP server configurations.
	// In platform mode, these are MCP servers shared across all teams in the org.
	MCPServers MCPServerStore

	// TeamMCPServers provides access to team-scoped MCP server configurations.
	// Team MCP servers override org MCP servers of the same name. Team admins manage these.
	TeamMCPServers MCPServerStore

	// PlatformMCPServers provides access to platform-wide MCP server configurations.
	// Platform MCP servers are inherited by all organizations and teams.
	// Only superadmins can manage these. Nil in personal mode.
	PlatformMCPServers MCPServerStore

	// A2AAgents provides access to org-level A2A agent connection configurations.
	// In platform mode, these are remote A2A agents shared across all teams in the org.
	A2AAgents A2AAgentStore

	// TeamA2AAgents provides access to team-scoped A2A agent connection configurations.
	// Team A2A agents override org A2A agents of the same name. Team admins manage these.
	TeamA2AAgents A2AAgentStore

	// PlatformA2AAgents provides access to platform-wide A2A agent connection configurations.
	// Platform A2A agents are inherited by all organizations and teams.
	// Only superadmins can manage these. Nil in personal mode.
	PlatformA2AAgents A2AAgentStore

	// PlatformSkills provides access to platform-wide skill definitions.
	// Platform skills are inherited by all organizations and teams.
	// Only superadmins can manage these. Nil in personal mode.
	PlatformSkills SkillStore

	// PlatformSlideTemplates are PPTX-imported slide templates visible to every org.
	// Only superadmins can write these. Nil in personal mode.
	PlatformSlideTemplates SlideTemplateStore

	// OrgSlideTemplates are PPTX-imported slide templates visible to every team
	// in the org. Org admins write these. Nil in personal mode.
	OrgSlideTemplates SlideTemplateStore

	// NetworkPolicies provides access to org-level network policy rules.
	// In platform mode, these rules apply to all teams in the org.
	NetworkPolicies NetworkPolicyStore

	// TeamNetworkPolicies provides access to team-scoped network policy rules.
	// Team rules can add allows but cannot override platform/org denies.
	TeamNetworkPolicies NetworkPolicyStore

	// PlatformNetworkPolicies provides access to platform-wide network policy rules.
	// Platform denies cannot be overridden by any lower tier.
	// Only superadmins can manage these. Nil in personal mode.
	PlatformNetworkPolicies NetworkPolicyStore

	// DrillReports provides access to drill test report persistence.
	DrillReports DrillReportStore

	// Settings provides access to team-level configuration overrides.
	// In platform mode, this reads/writes the teams.settings JSONB column.
	// Nil in personal mode (host config is used directly).
	Settings SettingsStore

	// OrgSettings provides access to org-level configuration (providers, defaults).
	// Visible to all teams within the organization. Nil in personal mode.
	OrgSettings OrgSettingsStore

	// PlatformSettings provides access to platform-wide configuration (providers, defaults).
	// Visible to all organizations and teams. Nil in personal mode.
	PlatformSettings PlatformSettingsStore

	// Audit provides audit logging.
	Audit AuditStore

	// MemorySearcher provides cross-tier memory search in platform mode.
	// In personal mode this is nil (use Memory directly).
	MemorySearcher ThreeTierSearcher

	// Platform-only fields (nil in personal mode).
	Platform     PlatformStore
	TenantRouter TenantRouter
}

// DeploymentMode indicates the operational mode of Astonish.
type DeploymentMode string

const (
	// ModePlatform is the multi-tenant mode with PostgreSQL/SQLite storage.
	ModePlatform DeploymentMode = "platform"
)
