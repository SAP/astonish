package channels

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/pdfgen"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
)

// ChannelManager owns the lifecycle of all registered channels and routes
// inbound messages to the shared ChatAgent.
type ChannelManager struct {
	channels map[string]Channel
	router   *Router
	agent    *agent.ChatAgent
	sessSvc  session.Service
	commands *CommandRegistry
	mu       sync.RWMutex
	running  atomic.Bool
	logger   *log.Logger

	// Info fields for command context
	providerName string
	modelName    string
	toolCount    int

	// LLM provider pool for per-message provider resolution.
	// When set, each inbound message resolves the effective provider from
	// the 3-tier DB cascade (Platform → Org → Team) and caches LLM instances.
	// When nil, falls back to the ChatAgent's default LLM (startup-time provider).
	llmPool *provider.Pool

	// Credential redaction for outbound messages
	redactor *credentials.Redactor

	// Device authorization for Studio web UI
	authorizeFunc func(code string) (string, bool)

	// Fleet session tracking: maps chat session key -> fleet session ID.
	// When a fleet session is active for a chat, inbound messages are routed
	// to the fleet session instead of the regular chat agent.
	activeFleets map[string]string
	fleetMu      sync.RWMutex

	// Pending session contexts: maps session key -> system context string.
	// Used by /fleet_plan to inject the wizard system prompt on the next
	// regular chat message. Consumed (cleared) after first use.
	pendingContexts   map[string]string
	pendingContextsMu sync.Mutex

	// Pending pinned tool groups: maps session key -> tool group names.
	// Used alongside pendingContexts for wizard sessions that need specific
	// tool groups to stay available across all turns.
	pendingPinnedToolGroups   map[string][]string
	pendingPinnedToolGroupsMu sync.Mutex

	// ReadFileFunc reads a file given a session ID and file path. When sandbox
	// is enabled, the file may live inside a container rather than on the host
	// filesystem. The daemon injects a closure that tries the host first, then
	// falls back to pulling from the session's sandbox container.
	// When nil, os.ReadFile is used directly (no sandbox awareness).
	ReadFileFunc func(sessionID, path string) ([]byte, error)

	// BrowserPDF is an optional browser provider for high-quality Chrome-based
	// PDF generation. When set, fileToDocument uses headless Chrome to render
	// markdown as styled HTML → PDF with full Unicode/emoji support. When nil,
	// falls back to the pure-Go goldmark-pdf renderer (Latin-1 only).
	BrowserPDF pdfgen.BrowserProvider

	// Fleet dependency functions — injected by the daemon to avoid circular imports.
	// These allow fleet commands to access the fleet registries and session management
	// without the channels package importing pkg/api.
	fleetDeps *FleetDeps

	// PlatformResolver resolves an external channel identity to a platform user
	// context. In platform mode, this is used to inject team-scoped stores into
	// the execution context for inbound channel messages. When nil, the channel
	// operates in personal mode (no team context injection).
	platformResolver PlatformResolver
}

// PlatformResolver resolves an external channel identity (e.g., Telegram user ID)
// to a platform user and their preferred team context. The returned context should
// have team-scoped stores injected (credentials, flows, skills, MCP, memories).
type PlatformResolver interface {
	// ResolveChannelUser looks up a channel identity and returns:
	// - ctx with team-scoped stores injected
	// - the platform user ID (for session scoping)
	// - the display name
	// - an error if the user is not linked/provisioned
	ResolveChannelUser(ctx context.Context, channelType, externalID string) (enrichedCtx context.Context, userID, displayName string, err error)

	// ResolveChannelUserWithHint is like ResolveChannelUser but accepts an
	// optional per-message routing hint (from email plus-addressing, etc.).
	// If hint is nil, behaves identically to ResolveChannelUser.
	ResolveChannelUserWithHint(ctx context.Context, channelType, externalID string, hint *RoutingHint) (enrichedCtx context.Context, userID, displayName string, err error)

	// SetRoutingPref stores a persistent routing preference for a channel identity.
	// Used by /org and /team commands to change the user's default routing.
	SetRoutingPref(channelType, externalID, orgSlug, teamSlug string)

	// GetRoutingPref returns the current routing preference for a channel identity,
	// or nil if no preference has been set.
	GetRoutingPref(channelType, externalID string) *RoutingPref

	// ListUserRoutes returns the available orgs and teams for a channel identity.
	// Used by /context to show what the user can route to.
	ListUserRoutes(ctx context.Context, channelType, externalID string) ([]RouteOption, error)
}

// RoutingPref stores a user's persistent org/team routing preference for a channel.
type RoutingPref struct {
	OrgSlug  string
	TeamSlug string
}

// RouteOption describes an available org/team combination for routing.
type RouteOption struct {
	OrgSlug  string
	OrgName  string
	TeamSlug string
	TeamName string
}

// FleetDeps holds fleet-related dependencies injected into the ChannelManager.
// This avoids circular imports between pkg/channels and pkg/api.
type FleetDeps struct {
	// GetSessionRegistry returns the fleet session registry.
	GetSessionRegistry func() FleetSessionRegistry
	// GetPlanRegistry returns the fleet plan registry.
	GetPlanRegistry func() FleetPlanRegistry
	// GetFleetRegistry returns the fleet template registry.
	GetFleetRegistry func() FleetTemplateRegistry
	// StartSessionFromPlan creates and starts a fleet session from a plan key.
	StartSessionFromPlan func(planKey, initialMessage string) (*FleetSessionStartResult, error)
	// StopSession stops a fleet session by ID and unregisters it.
	StopSession func(sessionID string) error
}

// FleetSessionRegistry is the interface for managing active fleet sessions.
type FleetSessionRegistry interface {
	PostHumanMessage(sessionID, text string) error
}

// FleetPlanRegistry is the interface for reading fleet plans.
type FleetPlanRegistry interface {
	ListPlans() []FleetPlanSummary
}

// FleetTemplateRegistry is the interface for reading fleet templates.
type FleetTemplateRegistry interface {
	ListFleets() []FleetTemplateSummary
	GetFleet(key string) (FleetTemplateWithWizard, bool)
}

// FleetPlanSummary is a lightweight view of a fleet plan.
type FleetPlanSummary struct {
	Key         string
	Name        string
	Description string
	ChannelType string
	AgentNames  []string
}

// FleetTemplateSummary is a lightweight view of a fleet template.
type FleetTemplateSummary struct {
	Key         string
	Name        string
	Description string
	AgentNames  []string
}

// FleetTemplateWithWizard provides wizard config from a fleet template.
type FleetTemplateWithWizard struct {
	Name               string
	WizardSystemPrompt string
	PinnedToolGroups   []string
}

// FleetSessionStartResult is the result of starting a fleet session.
type FleetSessionStartResult struct {
	SessionID string
	FleetKey  string
	FleetName string
	// OnMessagePosted allows the caller to compose additional callbacks
	// on the fleet session (e.g., forwarding messages to Telegram).
	SetOnMessagePosted func(fn func(sender, text string))
	// OnSessionDone allows the caller to register a callback for when the session ends.
	SetOnSessionDone func(fn func(sessionID string, err error))
}

// ChannelManagerConfig holds optional configuration for NewChannelManager.
type ChannelManagerConfig struct {
	ProviderName string
	ModelName    string
	ToolCount    int
}

// NewChannelManager creates a new ChannelManager with the given ChatAgent
// and session service. All inbound messages are processed by the shared
// ChatAgent using per-conversation persistent sessions.
func NewChannelManager(chatAgent *agent.ChatAgent, sessSvc session.Service, logger *log.Logger, cfg *ChannelManagerConfig) *ChannelManager {
	if logger == nil {
		logger = log.Default()
	}
	m := &ChannelManager{
		channels:                make(map[string]Channel),
		router:                  NewRouter(),
		agent:                   chatAgent,
		sessSvc:                 sessSvc,
		commands:                DefaultCommands(),
		logger:                  logger,
		activeFleets:            make(map[string]string),
		pendingContexts:         make(map[string]string),
		pendingPinnedToolGroups: make(map[string][]string),
	}
	if cfg != nil {
		m.providerName = cfg.ProviderName
		m.modelName = cfg.ModelName
		m.toolCount = cfg.ToolCount
	}
	return m
}

// SetRedactor sets the credential redactor for outbound message sanitization.
func (m *ChannelManager) SetRedactor(r *credentials.Redactor) {
	m.redactor = r
}

// SetLLMPool sets the LLM provider pool for per-message provider resolution.
// When set, each inbound message resolves the effective provider from the
// 3-tier DB cascade (Platform → Org → Team) instead of using the ChatAgent's
// startup-time provider. This ensures channels use the same provider resolution
// logic as the Studio chat.
func (m *ChannelManager) SetLLMPool(pool *provider.Pool) {
	m.llmPool = pool
}

// InvalidateLLMPool drops all cached LLM instances in the pool, forcing fresh
// provider creation on the next message. Call this when provider settings
// change (API keys, base URLs, default provider/model, etc.).
func (m *ChannelManager) InvalidateLLMPool() {
	if m.llmPool != nil {
		m.llmPool.Invalidate()
	}
}

// SetReadFileFunc sets a sandbox-aware file reader for document attachments.
// When set, handleInbound uses this instead of os.ReadFile to support reading
// files from sandbox containers.
func (m *ChannelManager) SetReadFileFunc(fn func(sessionID, path string) ([]byte, error)) {
	m.ReadFileFunc = fn
}

// readFile reads a file, using the sandbox-aware ReadFileFunc if available,
// otherwise falling back to os.ReadFile.
func (m *ChannelManager) readFile(sessionID, path string) ([]byte, error) {
	if m.ReadFileFunc != nil {
		return m.ReadFileFunc(sessionID, path)
	}
	return os.ReadFile(path)
}

// fileToDocument converts a file artifact into a DocumentAttachment. For
// markdown files (.md), the content is converted to PDF using headless Chrome
// so Telegram users receive a formatted document instead of raw markdown text.
func (m *ChannelManager) fileToDocument(data []byte, filePath string) DocumentAttachment {
	filename := filepath.Base(filePath)
	ext := strings.ToLower(filepath.Ext(filename))

	if ext == ".md" || ext == ".markdown" {
		if m.BrowserPDF == nil {
			m.logger.Printf("[channels] No browser available for PDF generation of %s, sending as markdown", filename)
			return DocumentAttachment{Data: data, Filename: filename}
		}
		pdfData, err := pdfgen.ConvertMarkdownToPDFChrome(data, m.BrowserPDF)
		if err != nil {
			m.logger.Printf("[channels] PDF generation failed for %s, sending as markdown: %v", filename, err)
			return DocumentAttachment{Data: data, Filename: filename}
		}
		pdfName := filename[:len(filename)-len(ext)] + ".pdf"
		return DocumentAttachment{Data: pdfData, Filename: pdfName}
	}

	return DocumentAttachment{Data: data, Filename: filename}
}

// SetAuthorizeFunc sets the device authorization handler for the /authorize command.
func (m *ChannelManager) SetAuthorizeFunc(fn func(code string) (string, bool)) {
	m.authorizeFunc = fn
}

// SetFleetDeps injects fleet-related dependencies for fleet commands.
// Must be called during daemon startup after fleet registries are initialized.
func (m *ChannelManager) SetFleetDeps(deps *FleetDeps) {
	m.fleetDeps = deps
	registerFleetCommands(m)
	// Re-register bot commands with channels that support it (e.g., Telegram)
	// so the new fleet commands appear in the "/" autocomplete menu.
	m.refreshChannelCommands()
}

// SetPlatformResolver sets the platform resolver for inbound channel messages.
// When set, inbound messages will have team-scoped context injected based on
// the sender's linked channel identity.
func (m *ChannelManager) SetPlatformResolver(resolver PlatformResolver) {
	m.platformResolver = resolver
}

// GetPlatformResolver returns the platform resolver, or nil if not set.
// Used by routing commands (/org, /team, /context) to set routing preferences.
func (m *ChannelManager) GetPlatformResolver() PlatformResolver {
	return m.platformResolver
}

// BotUsernameProvider is implemented by channel adapters that expose a bot username.
type BotUsernameProvider interface {
	BotUsername() string
}

// LinkHandlerSetter is implemented by channel adapters that support code-based linking.
type LinkHandlerSetter interface {
	SetLinkHandler(fn func(ctx context.Context, senderID, senderUsername, code string) (bool, string))
}

// SlackHTTPHandlerProvider is implemented by Slack adapters that expose HTTP
// endpoints for Events API and slash command requests.
type SlackHTTPHandlerProvider interface {
	EventsHTTPHandler() http.Handler
	SlashCommandHTTPHandler() http.Handler
}

// GetTelegramBotUsername returns the connected Telegram bot's username, or empty
// string if Telegram is not configured or not connected.
func (m *ChannelManager) GetTelegramBotUsername() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels["telegram"]
	if !ok {
		return ""
	}
	if provider, ok := ch.(BotUsernameProvider); ok {
		return provider.BotUsername()
	}
	return ""
}

// GetSlackBotUserID returns the connected Slack bot's user ID, or empty
// string if Slack is not configured or not connected.
func (m *ChannelManager) GetSlackBotUserID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels["slack"]
	if !ok {
		return ""
	}
	if provider, ok := ch.(BotUsernameProvider); ok {
		return provider.BotUsername()
	}
	return ""
}

// GetSlackHTTPHandlers returns Slack's external HTTP endpoints when the Slack
// channel is registered.
func (m *ChannelManager) GetSlackHTTPHandlers() (events http.Handler, commands http.Handler, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, exists := m.channels["slack"]
	if !exists {
		return nil, nil, false
	}
	provider, ok := ch.(SlackHTTPHandlerProvider)
	if !ok {
		return nil, nil, false
	}
	return provider.EventsHTTPHandler(), provider.SlashCommandHTTPHandler(), true
}

// GetEmailAddress returns the connected email channel's address, or empty
// string if email is not configured or not connected.
func (m *ChannelManager) GetEmailAddress() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels["email"]
	if !ok {
		return ""
	}
	// The email channel's AccountID from Status() contains the address
	status := ch.Status()
	return status.AccountID
}

// SetTelegramLinkHandler sets the link handler on the Telegram channel adapter.
// The handler is called when a user sends /link <code> to the bot.
func (m *ChannelManager) SetTelegramLinkHandler(fn func(ctx context.Context, senderID, senderUsername, code string) (bool, string)) {
	m.setLinkHandlerForChannel("telegram", fn)
}

// SetSlackLinkHandler sets the link handler on the Slack channel adapter.
// The handler is called when a user sends /link <code> to the bot.
func (m *ChannelManager) SetSlackLinkHandler(fn func(ctx context.Context, senderID, senderUsername, code string) (bool, string)) {
	m.setLinkHandlerForChannel("slack", fn)
}

// setLinkHandlerForChannel sets the link handler on a specific channel adapter.
func (m *ChannelManager) setLinkHandlerForChannel(channelID string, fn func(ctx context.Context, senderID, senderUsername, code string) (bool, string)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[channelID]
	if !ok {
		return
	}
	type linkHandlerChannel interface {
		SetLinkHandler(func(ctx context.Context, senderID, senderUsername, code string) (bool, string))
	}
	if lh, ok := ch.(linkHandlerChannel); ok {
		lh.SetLinkHandler(fn)
	}
}

// refreshChannelCommands tells all running channels to re-register their
// command menus. Only channels that implement CommandRefresher are affected.
func (m *ChannelManager) refreshChannelCommands() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.channels {
		if refresher, ok := ch.(CommandRefresher); ok {
			refresher.RefreshCommands(m.commands)
		}
	}
}

// redactText applies credential redaction if a redactor is configured.
func (m *ChannelManager) redactText(s string) string {
	if m.redactor == nil {
		return s
	}
	return m.redactor.Redact(s)
}

// Register adds a channel to the manager. Must be called before StartAll.
func (m *ChannelManager) Register(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[ch.ID()] = ch
}

// StartAll starts all registered channels. Each channel runs in its own
// goroutine. Returns immediately after launching all channels.
func (m *ChannelManager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.channels) == 0 {
		return nil
	}

	m.running.Store(true)

	for id, ch := range m.channels {
		go func(id string, ch Channel) {
			m.logger.Printf("[channels] Starting %s...", id)
			if err := ch.Start(ctx, m.handleInbound); err != nil {
				m.logger.Printf("[channels] %s stopped with error: %v", id, err)
			}
		}(id, ch)
	}

	return nil
}

// StopAll gracefully stops all registered channels.
func (m *ChannelManager) StopAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	m.running.Store(false)

	var firstErr error
	for id, ch := range m.channels {
		m.logger.Printf("[channels] Stopping %s...", id)
		if err := ch.Stop(ctx); err != nil {
			m.logger.Printf("[channels] Error stopping %s: %v", id, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Commands returns the command registry so callers (e.g. console TUI) can
// reuse the same commands.
func (m *ChannelManager) Commands() *CommandRegistry {
	return m.commands
}

// Status returns the status of all registered channels.
func (m *ChannelManager) Status() map[string]ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make(map[string]ChannelStatus, len(m.channels))
	for id, ch := range m.channels {
		statuses[id] = ch.Status()
	}
	return statuses
}

// UpdateAllowlists updates the sender allowlists on running channels that
// implement AllowlistUpdater. The allowlists map is keyed by channel ID
// (e.g. "email", "telegram"). Returns true if any channel was updated.
func (m *ChannelManager) UpdateAllowlists(allowlists map[string][]string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	updated := false
	for id, ch := range m.channels {
		if updater, ok := ch.(AllowlistUpdater); ok {
			if newList, has := allowlists[id]; has {
				updater.UpdateAllowlist(newList)
				updated = true
			}
		}
	}
	return updated
}

// --- Fleet session tracking ---

// SetActiveFleet associates a chat session key with an active fleet session ID.
// While active, inbound messages for this chat are routed to the fleet session.
func (m *ChannelManager) SetActiveFleet(chatKey, sessionID string) {
	m.fleetMu.Lock()
	defer m.fleetMu.Unlock()
	m.activeFleets[chatKey] = sessionID
}

// ClearActiveFleet removes the fleet association for a chat session key.
func (m *ChannelManager) ClearActiveFleet(chatKey string) {
	m.fleetMu.Lock()
	defer m.fleetMu.Unlock()
	delete(m.activeFleets, chatKey)
}

// GetActiveFleet returns the active fleet session ID for a chat, or empty string.
func (m *ChannelManager) GetActiveFleet(chatKey string) string {
	m.fleetMu.RLock()
	defer m.fleetMu.RUnlock()
	return m.activeFleets[chatKey]
}

// SetSessionContext sets a one-shot system context to inject on the next regular
// chat message for the given session key. Used by /fleet_plan to inject the
// wizard system prompt before the first wizard turn.
func (m *ChannelManager) SetSessionContext(sessionKey, ctx string) {
	m.pendingContextsMu.Lock()
	defer m.pendingContextsMu.Unlock()
	m.pendingContexts[sessionKey] = ctx
}

// SetPinnedToolGroups sets one-shot pinned tool groups to inject on the next
// regular chat message for the given session key. Used alongside SetSessionContext
// for wizard sessions that need specific tool groups to remain available.
func (m *ChannelManager) SetPinnedToolGroups(sessionKey string, groups []string) {
	m.pendingPinnedToolGroupsMu.Lock()
	defer m.pendingPinnedToolGroupsMu.Unlock()
	if m.pendingPinnedToolGroups == nil {
		m.pendingPinnedToolGroups = make(map[string][]string)
	}
	m.pendingPinnedToolGroups[sessionKey] = groups
}

// consumeSessionContext retrieves and clears a pending session context.
func (m *ChannelManager) consumeSessionContext(sessionKey string) string {
	m.pendingContextsMu.Lock()
	defer m.pendingContextsMu.Unlock()
	ctx := m.pendingContexts[sessionKey]
	delete(m.pendingContexts, sessionKey)
	return ctx
}

// consumePinnedToolGroups retrieves and clears pending pinned tool groups.
func (m *ChannelManager) consumePinnedToolGroups(sessionKey string) []string {
	m.pendingPinnedToolGroupsMu.Lock()
	defer m.pendingPinnedToolGroupsMu.Unlock()
	if m.pendingPinnedToolGroups == nil {
		return nil
	}
	groups := m.pendingPinnedToolGroups[sessionKey]
	delete(m.pendingPinnedToolGroups, sessionKey)
	return groups
}

// handleInbound processes an inbound message from any channel.
// It routes the message to the appropriate session, runs the ChatAgent,
// collects the response, handles auto-distillation, and sends the reply.
func (m *ChannelManager) handleInbound(ctx context.Context, msg InboundMessage) error {
	// Route to determine session key
	route := m.router.Route(msg)

	m.logger.Printf("[channels] Inbound from %s (chat: %s, sender: %s): %s",
		msg.ChannelID, msg.ChatID, msg.SenderName, m.redactText(truncate(msg.Text, 100)))

	// Intercept slash commands before sending to the agent.
	if m.commands.IsCommand(msg.Text) {
		return m.handleCommand(ctx, msg, route)
	}

	// If a fleet session is active for this chat, route the message there
	// instead of the regular chat agent.
	if fleetSessionID := m.GetActiveFleet(route.SessionKey); fleetSessionID != "" {
		return m.handleFleetMessage(ctx, msg, route, fleetSessionID)
	}

	// Get or create persistent session
	userID := fmt.Sprintf("channel_%s_%s", msg.ChannelID, msg.SenderID)
	appName := "astonish"

	// Platform mode: resolve the channel sender to a platform user and inject
	// team-scoped stores into the context. This gives the agent access to the
	// user's team credentials, flows, skills, and MCP servers.
	if m.platformResolver != nil {
		enrichedCtx, platformUserID, _, resolveErr := m.platformResolver.ResolveChannelUserWithHint(ctx, msg.ChannelID, msg.SenderID, msg.RoutingHint)
		if resolveErr != nil {
			m.logger.Printf("[channels] Platform resolver failed for %s/%s: %v", msg.ChannelID, msg.SenderID, resolveErr)
			// Continue with unenriched context — agent will run without team stores
		} else {
			ctx = enrichedCtx
			userID = platformUserID

			// Hydrate the shared Redactor from the resolved credential store
			// so tool output redaction catches PG-backed credentials.
			if m.redactor != nil {
				if cs := store.CredentialStoreFromContext(ctx); cs != nil {
					m.redactor.HydrateFromStore(cs)
				}
			}
		}
	}

	// Inject Redactor into context so memory_save can Placeholderize()
	if m.redactor != nil {
		ctx = credentials.WithRedactor(ctx, m.redactor)
	}

	// Per-message provider resolution: resolve the effective LLM from the
	// 3-tier DB cascade (Platform → Org → Team) plus the per-session pin
	// (DECISION-3: missing-cred → warn + cascade fallback, do NOT drop).
	if m.llmPool != nil {
		if ps := store.ProviderStoresFromContext(ctx); ps != nil {
			appCfg := resolveEffectiveWithSessionPin(ctx, ps, route.SessionKey, msg.ChannelID, msg.SenderID)

			if appCfg.General.DefaultProvider != "" {
				m.logger.Printf("[channels] Dynamic provider resolution: provider=%q model=%q (channel=%s sender=%s)",
					appCfg.General.DefaultProvider, appCfg.General.DefaultModel, msg.ChannelID, msg.SenderID)
				llm, llmErr := m.llmPool.Get(ctx, appCfg.General.DefaultProvider, appCfg.General.DefaultModel, appCfg)
				if llmErr != nil {
					m.logger.Printf("[channels] Failed to resolve provider %q for message: %v", appCfg.General.DefaultProvider, llmErr)
					// Fall through — ChatAgent.Run will use its default c.LLM
				} else {
					ctx = agent.WithLLM(ctx, llm)
					m.logger.Printf("[channels] LLM override injected into context for provider=%q model=%q",
						appCfg.General.DefaultProvider, appCfg.General.DefaultModel)
				}
			} else {
				m.logger.Printf("[channels] Dynamic resolution returned empty DefaultProvider; falling back to startup LLM (channel=%s sender=%s)",
					msg.ChannelID, msg.SenderID)
			}
		} else {
			m.logger.Printf("[channels] No ProviderStores in context; falling back to startup LLM (channel=%s sender=%s)",
				msg.ChannelID, msg.SenderID)
		}
	} else {
		m.logger.Printf("[channels] No LLM pool configured; using startup LLM (channel=%s sender=%s)",
			msg.ChannelID, msg.SenderID)
	}

	sess, err := m.getOrCreateSession(ctx, appName, userID, route.SessionKey)
	if err != nil {
		return fmt.Errorf("session error: %w", err)
	}

	// Set channel-specific output hints and session context via context overrides
	// (thread-safe). Run() clones the SystemPromptBuilder and applies these.
	overrides := &agent.PromptOverrides{
		ChannelHints: channelHints(msg.ChannelID),
	}
	if sessionCtx := m.consumeSessionContext(route.SessionKey); sessionCtx != "" {
		overrides.SessionContext = agent.EscapeCurlyPlaceholders(sessionCtx)
	}
	if pinnedGroups := m.consumePinnedToolGroups(route.SessionKey); len(pinnedGroups) > 0 {
		overrides.PinnedToolGroups = pinnedGroups
	}
	// Build merged skill index (platform + org + team) so the LLM knows about
	// all available skills — matching parity with the Studio Chat path.
	if ss := store.SkillStoresFromContext(ctx); ss != nil {
		if idx := buildChannelSkillIndex(ctx, ss); idx != "" {
			overrides.SkillIndex = idx
		}
	}
	ctx = agent.WithPromptOverrides(ctx, overrides)

	// Create ADK agent wrapper for this turn
	adkAgent, err := adkagent.New(adkagent.Config{
		Name:        "astonish_channel",
		Description: "Astonish channel agent",
		Run:         m.agent.Run,
	})
	if err != nil {
		return fmt.Errorf("failed to create ADK agent: %w", err)
	}

	// Create runner for this turn.
	// Use the context-injected session service (PG in platform mode) if available,
	// so the runner looks up sessions in the same store where getOrCreateSession put them.
	runnerSessSvc := m.sessSvc
	if ctxSvc := sessionServiceFromContext(ctx); ctxSvc != nil {
		runnerSessSvc = ctxSvc
	}

	r, err := runner.New(runner.Config{
		AppName:        appName,
		Agent:          adkAgent,
		SessionService: runnerSessSvc,
	})
	if err != nil {
		return fmt.Errorf("failed to create runner: %w", err)
	}

	// Run the agent (with absolute timestamp for temporal context;
	// see agent.NewTimestampedUserContent for cache-stability rationale).
	userContent := agent.NewTimestampedUserContent(msg.Text)

	// Start typing indicator — shows "typing..." in the chat while the
	// agent is processing. Telegram typing expires after ~5 seconds, so
	// we refresh every 4 seconds. The goroutine stops when the agent
	// finishes (typingCancel is called).
	ch := m.getChannel(msg.ChannelID)
	if ch == nil {
		return fmt.Errorf("channel %s not found", msg.ChannelID)
	}

	target := Target{
		ChannelID: msg.ChannelID,
		ChatID:    msg.ChatID,
		ThreadID:  msg.ThreadID,
	}

	typingCtx, typingCancel := context.WithCancel(ctx)
	go m.sendTypingLoop(typingCtx, ch, target)

	// Process events as they arrive. For real-time channels (Telegram, etc.),
	// each complete LLM text turn is sent as a separate message immediately,
	// giving the user real-time updates during multi-tool operations.
	// For batch channels (email), only the final text turn is sent — earlier
	// turns (like "Let me look into that...") are dropped because email
	// recipients should get one concise reply, not a stream of intermediate steps.
	// Images from tool results (e.g., browser_take_screenshot) are collected
	// and attached to the next outbound text message.
	var messagesSent int
	var pendingImages []ImageAttachment

	// File artifacts from write_file/edit_file tool calls are collected and
	// sent as document attachments alongside the response message.
	var pendingDocuments []DocumentAttachment
	const maxDocumentSize = 10 * 1024 * 1024 // 10 MB limit for document attachments

	// Email is a batch channel: keep only the last text turn, send once at the end.
	isBatchChannel := msg.ChannelID == "email"
	var batchText string

	sessionID := sess.ID() // captured for sandbox-aware file reads

	for event, err := range r.Run(ctx, userID, sessionID, userContent, adkagent.RunConfig{}) {
		if err != nil {
			m.logger.Printf("[channels] Agent error for %s: %v", route.SessionKey, err)
			if messagesSent == 0 && batchText == "" {
				if err := ch.Send(ctx, target, OutboundMessage{
					Text:    friendlyErrorMessage(err),
					ReplyTo: msg.ID,
					Format:  FormatText,
				}); err != nil {
					slog.Error("failed to send message to channel", "target", target, "error", err)
				}
				messagesSent++
			}
			break
		}

		if event.LLMResponse.Content == nil {
			continue
		}

		// Skip streaming text chunks — wait for the complete aggregated
		// response. We send complete thoughts, not word-by-word fragments.
		if event.LLMResponse.Partial {
			continue
		}

		// Collect images from tool results (stashed by AfterToolCallback) and
		// from model InlineData parts (e.g. Gemini image generation).
		m.agent.EnqueueImagesFromContent(event.LLMResponse.Content)
		for _, img := range m.agent.DrainImages() {
			pendingImages = append(pendingImages, ImageAttachment{
				Data:   img.Data,
				Format: img.Format,
			})
		}

		// Drain file artifacts from write_file/edit_file tool calls.
		// Read each file and attach as a document. Uses sandbox-aware
		// readFile to handle files inside containers. Markdown files
		// are converted to PDF for a better channel experience.
		for _, file := range m.agent.DrainFiles() {
			data, err := m.readFile(sessionID, file.Path)
			if err != nil {
				m.logger.Printf("[channels] Failed to read file artifact %s: %v", file.Path, err)
				continue
			}
			if len(data) > maxDocumentSize {
				m.logger.Printf("[channels] Skipping file artifact %s: size %d exceeds %d limit", file.Path, len(data), maxDocumentSize)
				continue
			}
			pendingDocuments = append(pendingDocuments, m.fileToDocument(data, file.Path))
		}

		// Extract user-facing text only. Skip internal parts: function
		// calls, function responses, and chain-of-thought (Thought).
		var eventText strings.Builder
		for _, part := range event.LLMResponse.Content.Parts {
			if part.Text != "" && !part.Thought && part.FunctionCall == nil && part.FunctionResponse == nil {
				eventText.WriteString(part.Text)
			}
		}

		text := eventText.String()
		if strings.TrimSpace(text) == "" {
			continue
		}

		// Prepare display text — redact secrets
		displayText := m.redactText(text)
		if strings.TrimSpace(displayText) == "" {
			continue
		}

		if isBatchChannel {
			// Last-wins: only the final text turn matters for email.
			// Intermediate narration ("Let me look into that...") is dropped.
			batchText = displayText
			continue
		}

		// Streaming mode: send this turn's text as a message immediately.
		// Attach any pending images and documents from preceding tool calls.
		outMsg := OutboundMessage{
			Text:      displayText,
			Format:    FormatHTML,
			Images:    pendingImages,
			Documents: pendingDocuments,
		}
		pendingImages = nil    // consumed
		pendingDocuments = nil // consumed

		// Only the first message is a reply to the user's message
		if messagesSent == 0 {
			outMsg.ReplyTo = msg.ID
		}

		if err := ch.Send(ctx, target, outMsg); err != nil {
			m.logger.Printf("[channels] Failed to send message to %s: %v", msg.ChannelID, err)
		} else {
			messagesSent++
		}
	}

	// Batch channel: send the final text turn as a single message.
	if isBatchChannel && batchText != "" {
		outMsg := OutboundMessage{
			Text:      batchText,
			Format:    FormatHTML,
			ReplyTo:   msg.ID,
			Images:    pendingImages,
			Documents: pendingDocuments,
		}
		pendingImages = nil
		pendingDocuments = nil
		if err := ch.Send(ctx, target, outMsg); err != nil {
			m.logger.Printf("[channels] Failed to send message to %s: %v", msg.ChannelID, err)
		} else {
			messagesSent++
		}
	}

	// If images were produced but no text followed (e.g., the LLM's final
	// turn was a tool call with no commentary), send them as a standalone message.
	// Also drain any remaining file artifacts that weren't consumed above.
	for _, file := range m.agent.DrainFiles() {
		data, err := m.readFile(sessionID, file.Path)
		if err != nil {
			m.logger.Printf("[channels] Failed to read file artifact %s: %v", file.Path, err)
			continue
		}
		if len(data) > maxDocumentSize {
			m.logger.Printf("[channels] Skipping file artifact %s: size %d exceeds %d limit", file.Path, len(data), maxDocumentSize)
			continue
		}
		pendingDocuments = append(pendingDocuments, m.fileToDocument(data, file.Path))
	}

	if len(pendingImages) > 0 || len(pendingDocuments) > 0 {
		outMsg := OutboundMessage{
			Images:    pendingImages,
			Documents: pendingDocuments,
		}
		if messagesSent == 0 {
			outMsg.ReplyTo = msg.ID
		}
		if err := ch.Send(ctx, target, outMsg); err != nil {
			m.logger.Printf("[channels] Failed to send attachments to %s: %v", msg.ChannelID, err)
		} else {
			messagesSent++
		}
	}

	// Stop typing indicator now that the agent is done
	typingCancel()

	// Fallback if the agent produced no visible text at all.
	// This can happen when context compaction degrades the conversation history
	// or the LLM responds with only tool calls and no summary text.
	if messagesSent == 0 {
		if err := ch.Send(ctx, target, OutboundMessage{
			Text:    "Something went wrong and I couldn't generate a response. Try sending your message again, or use /new to start a fresh session.",
			ReplyTo: msg.ID,
			Format:  FormatText,
		}); err != nil {
			slog.Error("failed to send message to channel", "target", target, "error", err)
		}
	}

	m.logger.Printf("[channels] Response sent to %s (chat: %s, %d messages)",
		msg.ChannelID, msg.ChatID, messagesSent)

	return nil
}

// handleCommand executes a slash command and sends the response back to the channel.
func (m *ChannelManager) handleCommand(ctx context.Context, msg InboundMessage, route RouteResult) error {
	userID := fmt.Sprintf("channel_%s_%s", msg.ChannelID, msg.SenderID)
	appName := "astonish"
	sessSvc := m.sessSvc

	// Platform mode: resolve user and inject team-scoped stores (same as handleInbound).
	// This ensures /new deletes from the correct store (PG) with the correct user ID.
	if m.platformResolver != nil {
		enrichedCtx, platformUserID, _, resolveErr := m.platformResolver.ResolveChannelUserWithHint(ctx, msg.ChannelID, msg.SenderID, msg.RoutingHint)
		if resolveErr != nil {
			m.logger.Printf("[channels] Platform resolver failed for command %s/%s: %v", msg.ChannelID, msg.SenderID, resolveErr)
		} else {
			ctx = enrichedCtx
			userID = platformUserID
			if ctxSvc := sessionServiceFromContext(ctx); ctxSvc != nil {
				sessSvc = ctxSvc
			}
		}
	}

	// Resolve effective provider/model dynamically (cascade + session pin)
	// so that /status reflects the same model the next message will use.
	effectiveProvider := m.providerName
	effectiveModel := m.modelName
	if m.llmPool != nil {
		if ps := store.ProviderStoresFromContext(ctx); ps != nil {
			appCfg := resolveEffectiveWithSessionPin(ctx, ps, route.SessionKey, msg.ChannelID, msg.SenderID)
			if appCfg.General.DefaultProvider != "" {
				effectiveProvider = appCfg.General.DefaultProvider
			}
			if appCfg.General.DefaultModel != "" {
				effectiveModel = appCfg.General.DefaultModel
			}
		}
	}

	ch := m.getChannel(msg.ChannelID)
	var presenter CommandPresenter
	if p, ok := ch.(CommandPresenter); ok {
		presenter = p
	}

	cc := CommandContext{
		ChannelID:      msg.ChannelID,
		ChatID:         msg.ChatID,
		SenderID:       msg.SenderID,
		SenderName:     msg.SenderName,
		SessionKey:     route.SessionKey,
		UserID:         userID,
		AppName:        appName,
		SessionService: sessSvc,
		ProviderName:   effectiveProvider,
		ModelName:      effectiveModel,
		ToolCount:      m.toolCount,
		Distiller:      m.agent,
		AuthorizeFunc:  m.authorizeFunc,
		Manager:        m,
		Presenter:      presenter,
	}

	if ch == nil {
		return fmt.Errorf("channel %s not found", msg.ChannelID)
	}

	target := Target{
		ChannelID: msg.ChannelID,
		ChatID:    msg.ChatID,
		ThreadID:  msg.ThreadID,
	}

	// Start typing indicator — some commands (e.g. /distill) involve LLM
	// calls and can take significant time. Shows the user something is happening.
	typingCtx, typingCancel := context.WithCancel(ctx)
	go m.sendTypingLoop(typingCtx, ch, target)

	response, err := m.commands.Execute(ctx, msg.Text, cc)

	typingCancel()

	if err != nil {
		m.logger.Printf("[channels] Command error: %v", err)
		response = "Sorry, that command failed."
	}

	outMsg := OutboundMessage{
		Text:    response,
		ReplyTo: msg.ID,
		Format:  FormatText, // Command responses are plain text
	}

	return ch.Send(ctx, target, outMsg)
}

// handleFleetMessage routes an inbound message to an active fleet session.
// The message is posted as a "customer" message on the fleet's chat channel.
func (m *ChannelManager) handleFleetMessage(ctx context.Context, msg InboundMessage, route RouteResult, fleetSessionID string) error {
	ch := m.getChannel(msg.ChannelID)
	if ch == nil {
		return fmt.Errorf("channel %s not found", msg.ChannelID)
	}

	target := Target{
		ChannelID: msg.ChannelID,
		ChatID:    msg.ChatID,
		ThreadID:  msg.ThreadID,
	}

	// Post the message to the fleet session
	if m.fleetDeps == nil || m.fleetDeps.GetSessionRegistry == nil {
		m.logger.Printf("[channels] Fleet session registry not available")
		if err := ch.Send(ctx, target, OutboundMessage{
			Text:    "Fleet system is not available. Use /fleet_stop to exit fleet mode.",
			ReplyTo: msg.ID,
			Format:  FormatText,
		}); err != nil {
			slog.Error("failed to send message to channel", "target", target, "error", err)
		}
		return nil
	}

	registry := m.fleetDeps.GetSessionRegistry()
	if registry == nil {
		m.logger.Printf("[channels] Fleet session registry not initialized")
		m.ClearActiveFleet(route.SessionKey)
		if err := ch.Send(ctx, target, OutboundMessage{
			Text:    "Fleet session has ended. Returning to normal chat.",
			ReplyTo: msg.ID,
			Format:  FormatText,
		}); err != nil {
			slog.Error("failed to send message to channel", "target", target, "error", err)
		}
		return nil
	}

	if err := registry.PostHumanMessage(fleetSessionID, msg.Text); err != nil {
		m.logger.Printf("[channels] Failed to post fleet message: %v", err)
		// If the session no longer exists, clear the mapping
		m.ClearActiveFleet(route.SessionKey)
		if err := ch.Send(ctx, target, OutboundMessage{
			Text:    "Fleet session has ended. Returning to normal chat.",
			ReplyTo: msg.ID,
			Format:  FormatText,
		}); err != nil {
			slog.Error("failed to send message to channel", "target", target, "error", err)
		}
		return nil
	}

	m.logger.Printf("[channels] Routed message to fleet session %s", fleetSessionID)
	return nil
}

// resolveEffectiveWithSessionPin resolves the effective AppConfig for a
// channel message: 3-tier cascade + user default + session pin (DECISION-3).
// If the pinned provider has no credential on the current team, emits a
// slog.Warn and returns the cascade-only config (message is NEVER dropped).
func resolveEffectiveWithSessionPin(
	ctx context.Context,
	ps *store.ProviderStores,
	sessionKey, channelID, senderID string,
) *config.AppConfig {
	appCfg := provider.ResolveEffectiveConfig(ctx, ps.Platform, ps.Org, ps.Team)
	appCfg = provider.ApplyUserDefault(appCfg, nil)

	var pinnedProvider, pinnedModel string
	if tds := store.TeamDataStoreFromContext(ctx); tds != nil && sessionKey != "" {
		if pin, pinErr := tds.SessionPin(ctx, sessionKey); pinErr == nil && pin != nil {
			pinnedProvider = pin.Provider
			pinnedModel = pin.Model
		}
	}
	appCfg = provider.ApplyProviderOverride(appCfg, pinnedProvider, pinnedModel)

	if pinnedProvider != "" {
		if _, ok := appCfg.Providers[pinnedProvider]; !ok {
			slog.Warn("[channels] pinned provider has no credential on this team; using cascade fallback",
				"channel", channelID,
				"sender", senderID,
				"session", sessionKey,
				"pinnedProvider", pinnedProvider,
				"pinnedModel", pinnedModel)
			appCfg = provider.ResolveEffectiveConfig(ctx, ps.Platform, ps.Org, ps.Team)
			appCfg = provider.ApplyUserDefault(appCfg, nil)
		}
	}
	return appCfg
}

// getOrCreateSession retrieves an existing session by key or creates a new one.
// Session keys are used as session IDs for deterministic mapping.
func (m *ChannelManager) getOrCreateSession(ctx context.Context, appName, userID, sessionKey string) (session.Session, error) {
	// In platform mode, the context carries a per-tenant session service
	// injected by the platform resolver. Prefer it over the factory default.
	svc := m.sessSvc
	if ctxSvc := sessionServiceFromContext(ctx); ctxSvc != nil {
		svc = ctxSvc
	}

	// Try to get existing session
	getResp, err := svc.Get(ctx, &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionKey,
	})
	if err == nil && getResp.Session != nil {
		return getResp.Session, nil
	}

	// Create new session with the key as ID
	createResp, err := svc.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionKey,
	})
	if err != nil {
		return nil, err
	}
	return createResp.Session, nil
}

// sessionServiceFromContext extracts a session.Service from context if one
// was injected by the platform resolver. Returns nil in personal mode.
func sessionServiceFromContext(ctx context.Context) session.Service {
	ss := store.SessionServiceFromContext(ctx)
	if ss == nil {
		return nil
	}
	return ss
}

// getChannel returns a registered channel by ID.
func (m *ChannelManager) getChannel(id string) Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.channels[id]
}

// Send delivers an outbound message to a target channel. This is used by
// external callers (scheduler, API) to send messages without going through
// the inbound message flow.
func (m *ChannelManager) Send(ctx context.Context, target Target, msg OutboundMessage) error {
	ch := m.getChannel(target.ChannelID)
	if ch == nil {
		return fmt.Errorf("channel %s not found", target.ChannelID)
	}
	msg.Text = m.redactText(msg.Text)
	return ch.Send(ctx, target, msg)
}

// Broadcast delivers an outbound message to all targets across all registered
// channels. For Telegram, this means sending to every allowed user. Used by
// the scheduler to deliver job results without needing per-job targeting.
func (m *ChannelManager) Broadcast(ctx context.Context, msg OutboundMessage) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msg.Text = m.redactText(msg.Text)

	var firstErr error
	for _, ch := range m.channels {
		for _, target := range ch.BroadcastTargets() {
			if err := ch.Send(ctx, target, msg); err != nil {
				m.logger.Printf("[channels] Broadcast to %s/%s failed: %v", target.ChannelID, target.ChatID, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// CountBroadcastTargets returns how many broadcast recipients are currently registered.
func (m *ChannelManager) CountBroadcastTargets() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, ch := range m.channels {
		n += len(ch.BroadcastTargets())
	}
	return n
}

// channelHints returns LLM output guidance for a given channel.
// These hints are injected into the system prompt so the model produces
// output suited to the channel's formatting capabilities.
func channelHints(channelID string) string {
	switch channelID {
	case "telegram":
		return `You are responding via Telegram chat.
- Keep responses concise (under 300 words when possible)
- NEVER use markdown tables — use plain bullet lists instead
- Use simple formatting only: **bold**, *italic*, ` + "`code`" + `, and fenced code blocks
- Break long responses into short paragraphs
- Be conversational — this is a chat, not a terminal`
	case "slack":
		return `You are responding via Slack.
- Keep responses concise, scannable, and conversational
- Use Slack-compatible Markdown/mrkdwn-friendly formatting: **bold**, ~~strikethrough~~, ` + "`code`" + `, and fenced code blocks
- For structured records, prefer a compact Markdown table when the result set is small or medium and the columns are meaningful
- For long result sets, summarize first and use grouped bullet lists or offer to provide more details instead of producing an oversized table
- Avoid HTML and complex Markdown that Slack may not render well
- Use [text](url) for links (they will be auto-converted to Slack format)`
	case "email":
		return `You are responding via email.
- Produce ONE comprehensive reply — do not narrate intermediate steps
- Use proper email formatting with a greeting and sign-off
- Keep responses clear and well-structured
- You can use longer, more detailed responses than chat (email is async)
- Use markdown for formatting (it will be rendered as HTML)
- Do not include unnecessary pleasantries in every message if the conversation is ongoing
- Thread context: you are replying to an email conversation`
	default:
		return ""
	}
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// friendlyErrorMessage converts an error from the agent/LLM into a user-facing
// message that explains what went wrong and what the user should do.
func friendlyErrorMessage(err error) string {
	if llmerror.IsRateLimited(err) {
		return "I'm being rate limited by the AI provider. Please wait a moment and try again."
	}
	if llmerror.IsAuthError(err) {
		return "Authentication error with the AI provider. Please check your API keys and configuration."
	}
	if llmerror.IsServerError(err) {
		return "The AI provider is experiencing issues. Please try again shortly."
	}
	if code := llmerror.StatusCode(err); code > 0 {
		return fmt.Sprintf("The AI provider returned an error (HTTP %d). Please try again.", code)
	}
	// Unknown error — include a brief summary
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return fmt.Sprintf("Sorry, I encountered an error: %s", msg)
}

// typingInterval is how often we refresh the typing indicator.
// Telegram typing expires after ~5 seconds, so 4s gives a comfortable margin.
const typingInterval = 4 * time.Second

// sendTypingLoop sends periodic typing indicators until ctx is cancelled.
// Best-effort: errors are logged but don't interrupt the agent run.
func (m *ChannelManager) sendTypingLoop(ctx context.Context, ch Channel, target Target) {
	// Send immediately so the user sees "typing..." right away
	if err := ch.SendTyping(ctx, target); err != nil {
		m.logger.Printf("[channels] Typing indicator failed: %v", err)
	}

	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := ch.SendTyping(ctx, target); err != nil {
				// Don't spam logs — just log once and stop
				m.logger.Printf("[channels] Typing indicator failed: %v", err)
				return
			}
		}
	}
}

// buildChannelSkillIndex constructs a merged skill index from all available
// skill stores (platform + org + team) for injection into the system prompt.
// This gives channel agents the same skill awareness as Studio Chat.
func buildChannelSkillIndex(ctx context.Context, ss *store.SkillStores) string {
	var all []skills.Skill

	// Platform skills (base layer, inherited by all orgs/teams)
	if ss.Platform != nil {
		if platformSkills, err := ss.Platform.LoadAll(ctx); err == nil {
			for _, s := range platformSkills {
				all = append(all, skills.Skill{
					Name:        s.Name,
					Description: s.Description,
					OS:          s.OS,
					RequireBins: s.RequireBins,
					RequireEnv:  s.RequireEnv,
					Source:      "platform",
				})
			}
		}
	}

	// Org skills
	if ss.Org != nil {
		if orgSkills, err := ss.Org.LoadAll(ctx); err == nil {
			for _, s := range orgSkills {
				all = append(all, skills.Skill{
					Name:        s.Name,
					Description: s.Description,
					OS:          s.OS,
					RequireBins: s.RequireBins,
					RequireEnv:  s.RequireEnv,
					Source:      "org",
				})
			}
		}
	}

	// Team skills (highest priority, can override org/platform names)
	if ss.Team != nil {
		if teamSkills, err := ss.Team.LoadAll(ctx); err == nil {
			for _, s := range teamSkills {
				all = append(all, skills.Skill{
					Name:        s.Name,
					Description: s.Description,
					OS:          s.OS,
					RequireBins: s.RequireBins,
					RequireEnv:  s.RequireEnv,
					Source:      "team",
				})
			}
		}
	}

	if len(all) == 0 {
		// Even with no platform/org/team skills, BuildSkillIndex will include
		// built-in skills (e.g. generative-ui). Pass empty slice.
		return skills.BuildSkillIndex(nil)
	}

	// Deduplicate preferring later entries (team > org > platform).
	seen := make(map[string]bool, len(all))
	var deduped []skills.Skill
	for i := len(all) - 1; i >= 0; i-- {
		name := strings.ToLower(all[i].Name)
		if !seen[name] {
			seen[name] = true
			deduped = append(deduped, all[i])
		}
	}
	// Reverse to restore platform → org → team display order
	for i, j := 0, len(deduped)-1; i < j; i, j = i+1, j-1 {
		deduped[i], deduped[j] = deduped[j], deduped[i]
	}

	return skills.BuildSkillIndex(deduped)
}
