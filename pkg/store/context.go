package store

import (
	"context"
	"net/http"
)

type contextKey string

const servicesKey contextKey = "astonish_services"
const credStoreKey contextKey = "astonish_credential_store"
const memoryStoreKey contextKey = "astonish_memory_store"
const memoryScopeKey contextKey = "astonish_memory_scope"
const memorySearcherKey contextKey = "astonish_memory_searcher"
const memoryStoresByScopeKey contextKey = "astonish_memory_stores_by_scope"
const memoryDeleteAuthorizerKey contextKey = "astonish_memory_delete_authorizer"
const flowStoreKey contextKey = "astonish_flow_store"
const teamFlowStoreKey contextKey = "astonish_team_flow_store"
const drillReportStoreKey contextKey = "astonish_drill_report_store"
const sessionIDKey contextKey = "astonish_session_id"
const chatFilesKey contextKey = "astonish_chat_files"
const debugEnabledKey contextKey = "astonish_debug_enabled"
const cacheDiagnosticRecorderKey contextKey = "astonish_cache_diagnostic_recorder"

// WithServices returns a new context containing the Services instance.
func WithServices(ctx context.Context, svc *Services) context.Context {
	return context.WithValue(ctx, servicesKey, svc)
}

// FromContext retrieves the Services instance from the context.
// Returns nil if no Services is present (e.g., in personal mode before
// Services is wired, or in tests).
func FromContext(ctx context.Context) *Services {
	svc, _ := ctx.Value(servicesKey).(*Services)
	return svc
}

// FromRequest retrieves the Services instance from an HTTP request's context.
// This is a convenience wrapper for handler functions.
func FromRequest(r *http.Request) *Services {
	return FromContext(r.Context())
}

// Middleware returns an HTTP middleware that injects the Services instance
// into every request's context. This should be applied early in the
// middleware chain (after auth, before handlers).
func Middleware(svc *Services) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithServices(r.Context(), svc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithCredentialStore returns a new context containing a CredentialStore.
// This is used to propagate the tenant-scoped credential store into the
// ADK runner context so that tool functions can access it without globals.
func WithCredentialStore(ctx context.Context, cs CredentialStore) context.Context {
	return context.WithValue(ctx, credStoreKey, cs)
}

// CredentialStoreFromContext retrieves the CredentialStore from a context.
// Returns nil if no CredentialStore is present (personal mode or tests).
// Tool functions should call this and fall back to the package-level global
// credential store when nil.
func CredentialStoreFromContext(ctx context.Context) CredentialStore {
	cs, _ := ctx.Value(credStoreKey).(CredentialStore)
	return cs
}

// WithMemoryStore returns a new context containing a tenant-scoped MemoryStore.
// Used to propagate the PG team memory store into the ADK runner context.
func WithMemoryStore(ctx context.Context, ms MemoryStore) context.Context {
	return context.WithValue(ctx, memoryStoreKey, ms)
}

// MemoryStoreFromContext retrieves the MemoryStore from a context.
// Returns nil if no MemoryStore is present (personal mode or tests).
func MemoryStoreFromContext(ctx context.Context) MemoryStore {
	ms, _ := ctx.Value(memoryStoreKey).(MemoryStore)
	return ms
}

// WithMemoryScope returns a new context containing the scope of the injected
// MemoryStore. This keeps generated scenario-card frontmatter aligned with the
// actual tier where the row is written.
func WithMemoryScope(ctx context.Context, scope MemoryScope) context.Context {
	return context.WithValue(ctx, memoryScopeKey, string(scope))
}

// MemoryScopeFromContext retrieves the scope of the injected MemoryStore.
func MemoryScopeFromContext(ctx context.Context) MemoryScope {
	if ctx == nil {
		return ""
	}
	scope, _ := ctx.Value(memoryScopeKey).(string)
	return MemoryScope(scope)
}

// WithThreeTierSearcher returns a new context containing a ThreeTierSearcher.
// Used to propagate the cross-tier memory searcher into the ADK runner context.
func WithThreeTierSearcher(ctx context.Context, ts ThreeTierSearcher) context.Context {
	return context.WithValue(ctx, memorySearcherKey, ts)
}

// ThreeTierSearcherFromContext retrieves the ThreeTierSearcher from a context.
// Returns nil if no ThreeTierSearcher is present (personal mode or tests).
func ThreeTierSearcherFromContext(ctx context.Context) ThreeTierSearcher {
	ts, _ := ctx.Value(memorySearcherKey).(ThreeTierSearcher)
	return ts
}

// MemoryStoresByScope groups the writable memory stores available to the
// current tenant context. Tools use this to target a specific memory tier by ID
// without bypassing tenant routing.
type MemoryStoresByScope struct {
	Personal MemoryStore
	Team     MemoryStore
	Org      MemoryStore
}

// WithMemoryStoresByScope returns a new context containing per-scope memory stores.
func WithMemoryStoresByScope(ctx context.Context, stores MemoryStoresByScope) context.Context {
	return context.WithValue(ctx, memoryStoresByScopeKey, stores)
}

// MemoryStoresByScopeFromContext retrieves per-scope memory stores from context.
func MemoryStoresByScopeFromContext(ctx context.Context) (MemoryStoresByScope, bool) {
	if ctx == nil {
		return MemoryStoresByScope{}, false
	}
	stores, ok := ctx.Value(memoryStoresByScopeKey).(MemoryStoresByScope)
	return stores, ok
}

// MemoryDeleteAuthorizer checks whether the current caller may delete a memory
// from the requested scope.
type MemoryDeleteAuthorizer func(ctx context.Context, entry *MemorySearchResult, scope string) error

// WithMemoryDeleteAuthorizer returns a new context containing a memory delete authorization function.
func WithMemoryDeleteAuthorizer(ctx context.Context, fn MemoryDeleteAuthorizer) context.Context {
	return context.WithValue(ctx, memoryDeleteAuthorizerKey, fn)
}

// MemoryDeleteAuthorizerFromContext retrieves the memory delete authorizer from context.
func MemoryDeleteAuthorizerFromContext(ctx context.Context) MemoryDeleteAuthorizer {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(memoryDeleteAuthorizerKey).(MemoryDeleteAuthorizer)
	return fn
}

// WithFlowStore returns a new context containing a tenant-scoped FlowStore.
// Used to propagate the PG flow store into the ADK runner context so that
// drill tools (save_drill, list_drills, etc.) can read/write flows from the
// database rather than the local filesystem in platform mode.
func WithFlowStore(ctx context.Context, fs FlowStore) context.Context {
	return context.WithValue(ctx, flowStoreKey, fs)
}

// FlowStoreFromContext retrieves the FlowStore from a context.
// Returns nil if no FlowStore is present (personal mode or tests).
func FlowStoreFromContext(ctx context.Context) FlowStore {
	fs, _ := ctx.Value(flowStoreKey).(FlowStore)
	return fs
}

// WithTeamFlowStore returns a new context containing the team-scoped FlowStore.
// Used by drill tools which are always team-scoped (drills are team-level
// artifacts, never personal). This is separate from the general FlowStore
// (which may be a composite personal+team store for regular flow tools).
func WithTeamFlowStore(ctx context.Context, fs FlowStore) context.Context {
	return context.WithValue(ctx, teamFlowStoreKey, fs)
}

// TeamFlowStoreFromContext retrieves the team-scoped FlowStore from a context.
// Returns nil if not in platform mode. Drill tools use this to ensure they
// always read/write from the team schema.
func TeamFlowStoreFromContext(ctx context.Context) FlowStore {
	fs, _ := ctx.Value(teamFlowStoreKey).(FlowStore)
	return fs
}

// WithDrillReportStore returns a new context containing a tenant-scoped DrillReportStore.
// Used to propagate the PG drill report store into the ADK runner context so that
// the run_drill tool can persist execution results to the database in platform mode.
func WithDrillReportStore(ctx context.Context, rs DrillReportStore) context.Context {
	return context.WithValue(ctx, drillReportStoreKey, rs)
}

// DrillReportStoreFromContext retrieves the DrillReportStore from a context.
// Returns nil if no DrillReportStore is present (personal mode or tests).
func DrillReportStoreFromContext(ctx context.Context) DrillReportStore {
	rs, _ := ctx.Value(drillReportStoreKey).(DrillReportStore)
	return rs
}

const skillStoresKey contextKey = "astonish_skill_stores"
const schedulerStoreKey contextKey = "astonish_scheduler_store"
const personalSchedulerStoreKey contextKey = "astonish_personal_scheduler_store"
const mcpServerStoresKey contextKey = "astonish_mcp_server_stores"

// SkillStores holds references to platform, org, and team skill stores
// for use in tool context injection.
type SkillStores struct {
	Platform SkillStore // platform-wide skill store (nil in personal mode); cascades into all orgs/teams
	Org      SkillStore // org-level skill store (nil if not in platform mode)
	Team     SkillStore // team-level skill store (nil if not in platform mode)
}

// WithSkillStores returns a new context containing the SkillStores.
// Used to propagate tenant-scoped skill stores into the ADK runner context
// so that the skill_lookup tool can resolve skills dynamically per-request.
func WithSkillStores(ctx context.Context, ss *SkillStores) context.Context {
	return context.WithValue(ctx, skillStoresKey, ss)
}

// SkillStoresFromContext retrieves the SkillStores from a context.
// Returns nil if no SkillStores is present (personal mode or tests).
func SkillStoresFromContext(ctx context.Context) *SkillStores {
	if ctx == nil {
		return nil
	}
	ss, _ := ctx.Value(skillStoresKey).(*SkillStores)
	return ss
}

// WithSchedulerStore returns a new context containing a tenant-scoped SchedulerStore.
// Used to propagate the team's scheduler store into the ADK runner context so that
// the schedule_job and list_scheduled_jobs tools can operate on the correct team's jobs.
func WithSchedulerStore(ctx context.Context, ss SchedulerStore) context.Context {
	return context.WithValue(ctx, schedulerStoreKey, ss)
}

// SchedulerStoreFromContext retrieves the team SchedulerStore from a context.
// Returns nil if no SchedulerStore is present (personal mode or tests).
func SchedulerStoreFromContext(ctx context.Context) SchedulerStore {
	if ctx == nil {
		return nil
	}
	ss, _ := ctx.Value(schedulerStoreKey).(SchedulerStore)
	return ss
}

// WithPersonalSchedulerStore returns a new context containing the user's personal SchedulerStore.
func WithPersonalSchedulerStore(ctx context.Context, ss SchedulerStore) context.Context {
	return context.WithValue(ctx, personalSchedulerStoreKey, ss)
}

// PersonalSchedulerStoreFromContext retrieves the personal SchedulerStore from a context.
// Returns nil if no personal scheduler store is present.
func PersonalSchedulerStoreFromContext(ctx context.Context) SchedulerStore {
	if ctx == nil {
		return nil
	}
	ss, _ := ctx.Value(personalSchedulerStoreKey).(SchedulerStore)
	return ss
}

// MCPServerStores holds references to platform/org/team MCP server stores
// for use in tool context injection.
//
// Cascade contract: at chat-build time the agent must see MCP servers from
// ALL three tiers, with team overriding org overriding platform on name
// collisions (mirrors loadMCPConfigForRequest in pkg/api/request_helpers.go).
// Platform-tier installs (e.g. standard servers like Tavily) MUST be visible
// to chats in every org/team — that is the documented inheritance model
// (see Services.PlatformMCPServers docstring).
type MCPServerStores struct {
	Platform MCPServerStore // platform-wide MCP server store (nil in personal mode); cascades into all orgs/teams
	Org      MCPServerStore // org-level MCP server store (nil if not in platform mode)
	Team     MCPServerStore // team-level MCP server store (nil if not in platform mode)
}

// WithMCPServerStores returns a new context containing the MCPServerStores.
// Used to propagate tenant-scoped MCP server stores into the ADK runner context
// so that the agent can resolve MCP servers dynamically per-request.
func WithMCPServerStores(ctx context.Context, ms *MCPServerStores) context.Context {
	return context.WithValue(ctx, mcpServerStoresKey, ms)
}

// MCPServerStoresFromContext retrieves the MCPServerStores from a context.
// Returns nil if no MCPServerStores is present (personal mode or tests).
func MCPServerStoresFromContext(ctx context.Context) *MCPServerStores {
	if ctx == nil {
		return nil
	}
	ms, _ := ctx.Value(mcpServerStoresKey).(*MCPServerStores)
	return ms
}

const a2aAgentStoresKey contextKey = "astonish_a2a_agent_stores"

// A2AAgentStores holds references to platform/org/team A2A agent stores
// for use in tool context injection.
//
// Cascade contract: at chat-build time the agent must see A2A agents from
// ALL three tiers, with team overriding org overriding platform on name
// collisions (mirrors the MCP server cascade pattern).
type A2AAgentStores struct {
	Platform A2AAgentStore // platform-wide A2A agent store (nil in personal mode); cascades into all orgs/teams
	Org      A2AAgentStore // org-level A2A agent store (nil if not in platform mode)
	Team     A2AAgentStore // team-level A2A agent store (nil if not in platform mode)
}

// WithA2AAgentStores returns a new context containing the A2AAgentStores.
// Used to propagate tenant-scoped A2A agent stores into the ADK runner context
// so that the agent can resolve A2A agents dynamically per-request.
func WithA2AAgentStores(ctx context.Context, as *A2AAgentStores) context.Context {
	return context.WithValue(ctx, a2aAgentStoresKey, as)
}

// A2AAgentStoresFromContext retrieves the A2AAgentStores from a context.
// Returns nil if no A2AAgentStores is present (personal mode or tests).
func A2AAgentStoresFromContext(ctx context.Context) *A2AAgentStores {
	if ctx == nil {
		return nil
	}
	as, _ := ctx.Value(a2aAgentStoresKey).(*A2AAgentStores)
	return as
}

const fleetTemplateStoreKey contextKey = "astonish_fleet_template_store"
const fleetPlanStoreKey contextKey = "astonish_fleet_plan_store"
const fleetSetupProfileStoreKey contextKey = "astonish_fleet_setup_profile_store"
const fleetSetupDraftStoreKey contextKey = "astonish_fleet_setup_draft_store"
const fleetRunStateStoreKey contextKey = "astonish_fleet_run_state_store"
const fleetMailboxStoreKey contextKey = "astonish_fleet_mailbox_store"
const fleetTaskBoardStoreKey contextKey = "astonish_fleet_task_board_store"
const fleetTaskEventHandlerKey contextKey = "astonish_fleet_task_event_handler"

// FleetTaskEventHandler notifies listeners when a task board entry changes.
type FleetTaskEventHandler func(event string, task FleetTask)

// WithFleetTaskEventHandler attaches a task-board event callback to ctx.
func WithFleetTaskEventHandler(ctx context.Context, h FleetTaskEventHandler) context.Context {
	return context.WithValue(ctx, fleetTaskEventHandlerKey, h)
}

// FleetTaskEventHandlerFromContext returns the task event handler, if any.
func FleetTaskEventHandlerFromContext(ctx context.Context) FleetTaskEventHandler {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(fleetTaskEventHandlerKey).(FleetTaskEventHandler)
	return h
}

// WithFleetTemplateStore returns a new context containing a tenant-scoped FleetTemplateStore.
// Used to propagate the PG fleet template store into the ADK runner context so that
// fleet tools can resolve templates from the database in platform mode.
func WithFleetTemplateStore(ctx context.Context, fs FleetTemplateStore) context.Context {
	return context.WithValue(ctx, fleetTemplateStoreKey, fs)
}

// FleetTemplateStoreFromContext retrieves the FleetTemplateStore from a context.
// Returns nil if no FleetTemplateStore is present (personal mode or tests).
func FleetTemplateStoreFromContext(ctx context.Context) FleetTemplateStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetTemplateStoreKey).(FleetTemplateStore)
	return fs
}

// WithFleetPlanStore returns a new context containing a tenant-scoped FleetPlanStore.
// Used to propagate the PG fleet plan store into the ADK runner context so that
// fleet tools can read/write plans from the database in platform mode.
func WithFleetPlanStore(ctx context.Context, fs FleetPlanStore) context.Context {
	return context.WithValue(ctx, fleetPlanStoreKey, fs)
}

// FleetPlanStoreFromContext retrieves the FleetPlanStore from a context.
// Returns nil if no FleetPlanStore is present (personal mode or tests).
func FleetPlanStoreFromContext(ctx context.Context) FleetPlanStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetPlanStoreKey).(FleetPlanStore)
	return fs
}

// WithFleetSetupProfileStore returns a new context containing a tenant-scoped FleetSetupProfileStore.
// Used to propagate setup profile stores into ADK tool contexts during fleet plan setup.
func WithFleetSetupProfileStore(ctx context.Context, fs FleetSetupProfileStore) context.Context {
	return context.WithValue(ctx, fleetSetupProfileStoreKey, fs)
}

// FleetSetupProfileStoreFromContext retrieves the FleetSetupProfileStore from a context.
// Returns nil if no FleetSetupProfileStore is present.
func FleetSetupProfileStoreFromContext(ctx context.Context) FleetSetupProfileStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetSetupProfileStoreKey).(FleetSetupProfileStore)
	return fs
}

// WithFleetSetupDraftStore returns a new context containing a tenant-scoped FleetSetupDraftStore.
// Used to propagate setup draft stores into ADK tool contexts during fleet plan setup.
func WithFleetSetupDraftStore(ctx context.Context, fs FleetSetupDraftStore) context.Context {
	return context.WithValue(ctx, fleetSetupDraftStoreKey, fs)
}

// FleetSetupDraftStoreFromContext retrieves the FleetSetupDraftStore from a context.
// Returns nil if no FleetSetupDraftStore is present.
func FleetSetupDraftStoreFromContext(ctx context.Context) FleetSetupDraftStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetSetupDraftStoreKey).(FleetSetupDraftStore)
	return fs
}

// WithFleetRunStateStore returns a new context containing a tenant-scoped FleetRunStateStore.
func WithFleetRunStateStore(ctx context.Context, fs FleetRunStateStore) context.Context {
	return context.WithValue(ctx, fleetRunStateStoreKey, fs)
}

// FleetRunStateStoreFromContext retrieves the FleetRunStateStore from a context.
func FleetRunStateStoreFromContext(ctx context.Context) FleetRunStateStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetRunStateStoreKey).(FleetRunStateStore)
	return fs
}

// WithFleetMailboxStore returns a new context containing a tenant-scoped FleetMailboxStore.
func WithFleetMailboxStore(ctx context.Context, fs FleetMailboxStore) context.Context {
	return context.WithValue(ctx, fleetMailboxStoreKey, fs)
}

// FleetMailboxStoreFromContext retrieves the FleetMailboxStore from a context.
func FleetMailboxStoreFromContext(ctx context.Context) FleetMailboxStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetMailboxStoreKey).(FleetMailboxStore)
	return fs
}

// WithFleetTaskBoardStore returns a new context containing a tenant-scoped FleetTaskBoardStore.
func WithFleetTaskBoardStore(ctx context.Context, fs FleetTaskBoardStore) context.Context {
	return context.WithValue(ctx, fleetTaskBoardStoreKey, fs)
}

// FleetTaskBoardStoreFromContext retrieves the FleetTaskBoardStore from a context.
func FleetTaskBoardStoreFromContext(ctx context.Context) FleetTaskBoardStore {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(fleetTaskBoardStoreKey).(FleetTaskBoardStore)
	return fs
}

const sandboxTemplateKey contextKey = "astonish_sandbox_template"
const sandboxLayerChainKey contextKey = "astonish_sandbox_layer_chain"
const sandboxImageKey contextKey = "astonish_sandbox_image"
const sessionServiceKey contextKey = "astonish_session_service"
const userIDKey contextKey = "astonish_user_id"
const teamDataStoreKey contextKey = "astonish_team_data_store"

// WithSandboxTemplate returns a new context containing the team's sandbox
// template name. Used to propagate the team's custom container template into
// the ADK runner context so that NodeTool can create containers with the
// correct template instead of always using @base.
func WithSandboxTemplate(ctx context.Context, tpl string) context.Context {
	return context.WithValue(ctx, sandboxTemplateKey, tpl)
}

// SandboxTemplateFromContext retrieves the sandbox template name from a context.
// Returns "" if no template is present (personal mode, tests, or team has no
// custom template — which causes the sandbox to fall back to @base).
func SandboxTemplateFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	tpl, _ := ctx.Value(sandboxTemplateKey).(string)
	return tpl
}

// WithSandboxLayerChain returns a new context containing the resolved overlay
// layer chain (ordered oldest-first, e.g. ["@base", "<sha256>"]). This is the
// pre-resolved chain that the K8s backend passes directly to
// SessionSpec.LayerChain, bypassing the template-name-to-SHA indirection.
//
// When present, backends MUST use this chain instead of treating the template
// name as a literal layer ID.
func WithSandboxLayerChain(ctx context.Context, chain []string) context.Context {
	return context.WithValue(ctx, sandboxLayerChainKey, chain)
}

// SandboxLayerChainFromContext retrieves the resolved layer chain from context.
// Returns nil if no chain is present (personal mode, Incus backend, or team
// has no custom template). Callers should fall back to the template name if nil.
func SandboxLayerChainFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	chain, _ := ctx.Value(sandboxLayerChainKey).([]string)
	return chain
}

// WithSandboxImage returns a new context containing the per-template container
// image reference. When present, the OpenShell backend uses this image to create
// sandbox containers instead of the global config default.
func WithSandboxImage(ctx context.Context, image string) context.Context {
	return context.WithValue(ctx, sandboxImageKey, image)
}

// SandboxImageFromContext retrieves the per-template container image from
// context. Returns "" if no image override is present (backends use their
// global config default).
func SandboxImageFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	img, _ := ctx.Value(sandboxImageKey).(string)
	return img
}

// WithSessionService returns a new context containing a tenant-scoped SessionStore.
// Used to propagate the per-request session service (e.g., pgstore PersonalSessions)
// into the ADK runner context so that sub-agents (delegate_tasks) create child
// sessions in the correct store rather than the factory-time default.
func WithSessionService(ctx context.Context, ss SessionStore) context.Context {
	return context.WithValue(ctx, sessionServiceKey, ss)
}

// SessionServiceFromContext retrieves the SessionStore from a context.
// Returns nil if no SessionStore is present (personal mode or tests).
// SubAgentManager checks this to prefer the per-request store over its default.
func SessionServiceFromContext(ctx context.Context) SessionStore {
	if ctx == nil {
		return nil
	}
	ss, _ := ctx.Value(sessionServiceKey).(SessionStore)
	return ss
}

// WithUserID returns a new context containing the effective user ID.
// Used to propagate the per-request platform user ID (UUID) into the ADK runner
// context so that sub-agents create child sessions with the correct user_id
// (required by pgstore where user_id is a UUID column).
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFromContext retrieves the user ID from a context.
// Returns "" if no user ID is present (personal mode, tests, or non-platform contexts).
func UserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(userIDKey).(string)
	return id
}

// SystemUserID is the nil UUID used for system-initiated sessions (fleet plans
// without an owner, scheduler jobs, and other headless execution contexts).
// It represents "the platform acting autonomously" when no human user is
// associated with the action. Universally recognizable as a system identity.
const SystemUserID = "00000000-0000-0000-0000-000000000000"

// WithTeamDataStore returns a new context containing a tenant-scoped
// TeamDataStore. Used by the channel manager (and other non-HTTP entry
// points) to resolve per-session/app pins without coupling to the full
// tenant router. In personal mode this is nil.
func WithTeamDataStore(ctx context.Context, tds TeamDataStore) context.Context {
	return context.WithValue(ctx, teamDataStoreKey, tds)
}

// TeamDataStoreFromContext retrieves the TeamDataStore from a context.
// Returns nil if none was injected (personal mode, tests, or unresolved user).
func TeamDataStoreFromContext(ctx context.Context) TeamDataStore {
	if ctx == nil {
		return nil
	}
	tds, _ := ctx.Value(teamDataStoreKey).(TeamDataStore)
	return tds
}

// --- Disabled Tools (per-team tool restrictions) ---

type disabledToolsKey struct{}

// WithDisabledTools attaches a set of disabled tool names to the context.
// Tools in this set will be filtered from the agent's tool list per-request.
func WithDisabledTools(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, disabledToolsKey{}, names)
}

// DisabledToolsFromContext retrieves the disabled tool names from the context.
// Returns nil if no restrictions are set (personal mode or unrestricted team).
func DisabledToolsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	names, _ := ctx.Value(disabledToolsKey{}).([]string)
	return names
}

// --- Tenant Identity (org/team slug propagation) ---

type orgSlugKey struct{}
type teamSlugKey struct{}

// WithOrgSlug attaches the organization slug to the context.
// Used to propagate tenant identity into the ADK runner context.
func WithOrgSlug(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, orgSlugKey{}, slug)
}

// OrgSlugFromContext retrieves the organization slug from the context.
// Returns "" if not in platform mode or if the context lacks tenant identity.
func OrgSlugFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(orgSlugKey{}).(string)
	return s
}

// WithTeamSlug attaches the team slug to the context.
// Used to propagate tenant identity into the ADK runner context.
func WithTeamSlug(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, teamSlugKey{}, slug)
}

// TeamSlugFromContext retrieves the team slug from the context.
// Returns "" if not in platform mode or if the context lacks tenant identity.
func TeamSlugFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(teamSlugKey{}).(string)
	return s
}

// --- Run Job Function (for scheduler test execution) ---

// RunJobFunc executes a scheduled job by ID and returns its output.
// This is injected into the tool context so that the schedule_job tool can
// trigger test execution without going through the unauthenticated HTTP bridge.
type RunJobFunc func(ctx context.Context, jobID string) (string, error)

type runJobFuncKey struct{}

// WithRunJobFunc injects a RunJobFunc into the context.
func WithRunJobFunc(ctx context.Context, fn RunJobFunc) context.Context {
	return context.WithValue(ctx, runJobFuncKey{}, fn)
}

// RunJobFuncFromContext retrieves the RunJobFunc from the context.
// Returns nil if not available (personal mode or not injected).
func RunJobFuncFromContext(ctx context.Context) RunJobFunc {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(runJobFuncKey{}).(RunJobFunc)
	return fn
}

// WithSessionID returns a new context containing the active session ID.
// This is used to tag memories created during a session.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext retrieves the active session ID from the context.
// Returns empty string if no session ID is present.
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(sessionIDKey).(string)
	return s
}

// ChatFile is one file the user attached on the current chat turn (decoded
// bytes). Tools such as add_deck_image read these instead of asking the user
// to rehost the file on a public URL.
type ChatFile struct {
	Filename string
	MimeType string
	Data     []byte
}

// WithChatFiles returns a context carrying this turn's chat attachments.
func WithChatFiles(ctx context.Context, files []ChatFile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, chatFilesKey, files)
}

// ChatFilesFromContext returns this turn's chat attachments, or nil.
func ChatFilesFromContext(ctx context.Context) []ChatFile {
	if ctx == nil {
		return nil
	}
	files, _ := ctx.Value(chatFilesKey).([]ChatFile)
	return files
}

// WithDebugEnabled marks an authorized request for diagnostic collection.
func WithDebugEnabled(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, debugEnabledKey, enabled)
}

// DebugEnabledFromContext reports whether diagnostics are authorized for this request.
func DebugEnabledFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(debugEnabledKey).(bool)
	return enabled
}

// CacheDiagnosticRecorder persists one request fingerprint.
type CacheDiagnosticRecorder func(ctx context.Context, diagnostic CacheDiagnostic) error

// WithCacheDiagnosticRecorder adds a request-scoped diagnostics sink.
func WithCacheDiagnosticRecorder(ctx context.Context, recorder CacheDiagnosticRecorder) context.Context {
	return context.WithValue(ctx, cacheDiagnosticRecorderKey, recorder)
}

// CacheDiagnosticRecorderFromContext returns the request-scoped diagnostics sink.
func CacheDiagnosticRecorderFromContext(ctx context.Context) CacheDiagnosticRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(cacheDiagnosticRecorderKey).(CacheDiagnosticRecorder)
	return recorder
}

// --- Memory Merge Function (cross-session dedup) ---

// MemorySaveOrMergeFunc is a function that saves a memory entry as structured
// card memory. If no merge function is in context, callers must fail closed
// rather than inserting raw durable memory.
type MemorySaveOrMergeFunc func(ctx context.Context, memStore MemoryStore, entry MemoryEntry) error

type memorySaveOrMergeKey struct{}

// WithMemorySaveOrMerge injects a MemorySaveOrMergeFunc into the context.
// This is set by the launcher when wiring the ChatRunner in platform mode,
// allowing the memory_save tool to perform cross-session dedup without
// needing direct access to the LLM or agent.MemoryMerger.
func WithMemorySaveOrMerge(ctx context.Context, fn MemorySaveOrMergeFunc) context.Context {
	return context.WithValue(ctx, memorySaveOrMergeKey{}, fn)
}

// MemorySaveOrMergeFromContext retrieves the MemorySaveOrMergeFunc from context.
// Returns nil if not available (personal mode or not injected).
func MemorySaveOrMergeFromContext(ctx context.Context) MemorySaveOrMergeFunc {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(memorySaveOrMergeKey{}).(MemorySaveOrMergeFunc)
	return fn
}

// --- Tenant Context (HTTP middleware identity resolution) ---

// TenantContext holds the resolved tenant identity for a request.
// This is populated by auth middleware and consumed by TenantMiddleware.
type TenantContext struct {
	OrgSlug  string
	TeamSlug string
	UserID   string
}

type tenantCtxKey struct{}

// WithTenantContext stores the tenant identity in the request context.
// This should be called by the auth middleware after resolving the user's
// organization and team membership.
func WithTenantContext(ctx context.Context, tc *TenantContext) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tc)
}

// TenantContextFrom retrieves the tenant identity from the context.
func TenantContextFrom(ctx context.Context) *TenantContext {
	tc, _ := ctx.Value(tenantCtxKey{}).(*TenantContext)
	return tc
}

// --- Provider settings stores (for per-message provider resolution) ---

const providerStoresKey contextKey = "astonish_provider_stores"

// ProviderStores holds references to the 3-tier settings stores needed for
// provider resolution. Used by the channel manager to resolve the effective
// LLM provider per-message without coupling to the HTTP API layer.
type ProviderStores struct {
	Platform PlatformSettingsStore // platform-wide settings (nil in personal mode)
	Org      OrgSettingsStore      // org-level settings (nil if not resolved)
	Team     SettingsStore         // team-level settings (nil if not resolved)
}

// WithProviderStores returns a new context containing the ProviderStores.
func WithProviderStores(ctx context.Context, ps *ProviderStores) context.Context {
	return context.WithValue(ctx, providerStoresKey, ps)
}

// ProviderStoresFromContext retrieves the ProviderStores from a context.
// Returns nil if no ProviderStores is present (personal mode or tests).
func ProviderStoresFromContext(ctx context.Context) *ProviderStores {
	if ctx == nil {
		return nil
	}
	ps, _ := ctx.Value(providerStoresKey).(*ProviderStores)
	return ps
}

// --- Network policy stores (for multi-tier allow/deny rules) ---

const networkPolicyStoresKey contextKey = "astonish_network_policy_stores"

// NetworkPolicyStores holds references to the 3-tier network policy stores.
// Used to resolve effective allow/deny rules for sandbox network access.
//
// Merge semantics: deny-wins-from-above. A platform deny cannot be overridden
// by an org or team allow. Within the same tier, later saves override earlier.
type NetworkPolicyStores struct {
	Platform NetworkPolicyStore // platform-wide rules (nil in personal mode)
	Org      NetworkPolicyStore // org-level rules (nil if not in platform mode)
	Team     NetworkPolicyStore // team-level rules (nil if not in platform mode)
}

// WithNetworkPolicyStores returns a new context containing the NetworkPolicyStores.
func WithNetworkPolicyStores(ctx context.Context, nps *NetworkPolicyStores) context.Context {
	return context.WithValue(ctx, networkPolicyStoresKey, nps)
}

// NetworkPolicyStoresFromContext retrieves the NetworkPolicyStores from a context.
// Returns nil if no stores are present (personal mode or tests).
func NetworkPolicyStoresFromContext(ctx context.Context) *NetworkPolicyStores {
	if ctx == nil {
		return nil
	}
	nps, _ := ctx.Value(networkPolicyStoresKey).(*NetworkPolicyStores)
	return nps
}
