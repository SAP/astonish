package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	adrill "github.com/SAP/astonish/pkg/drill"
	emailpkg "github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/flowstore"
	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// ChatFactoryConfig holds all inputs needed to build a fully-wired ChatAgent.
type ChatFactoryConfig struct {
	AppConfig    *config.AppConfig
	ProviderName string
	ModelName    string
	DebugMode    bool
	AutoApprove  bool
	WorkspaceDir string
	IsDaemon     bool // When true, always run indexing/watchers (we ARE the daemon).

	// PlatformMode indicates multi-tenant platform mode. When true, skills are
	// resolved per-request from context-injected stores (platform → org → team).
	// The filesystem user skill directory is NOT read.
	PlatformMode bool

	// PlatformToolVectorStore is the vector store for tool discovery.
	// When set, it's used for semantic tool matching via vector search.
	// Callers should create this via backend.NewToolVectorStore().
	PlatformToolVectorStore agent.ToolVectorStore

	// PlatformEmbedFunc is the embedding function for platform mode tool discovery.
	// Required when PlatformToolVectorStore is set (used to embed query strings
	// before vector search). Typically the same Hugot-based embedder used for memory.
	PlatformEmbedFunc agent.EmbedFunc

	// AllowMissingProvider lets the agent be constructed even when no provider
	// is configured or provider initialization fails. Instead of returning an
	// error, the factory installs a placeholder LLM (which errors on use) and
	// leaves ProviderName/ModelName empty. Used by code mode so the TUI can
	// open and let the user pick a model via /model. The placeholder is meant
	// to be replaced via result.SwappableLLM.Swap once a model is chosen.
	AllowMissingProvider bool

	// LoadProjectContext enables AGENTS.md discovery: when true, the factory
	// walks upward from WorkspaceDir loading AGENTS.md (fallback CLAUDE.md) plus
	// the global ~/.config/astonish/AGENTS.md, and injects the merged content
	// into the system prompt (see agent.LoadProjectContext). Used by code mode;
	// platform mode uses per-team DB instructions instead.
	LoadProjectContext bool

	// SessionService optionally overrides the session store the factory would
	// otherwise create (in-memory by default). Code mode injects a persistent
	// FileStore here so sessions survive restarts and are listable via
	// `/sessions`. When nil, the factory falls back to an in-memory service.
	SessionService session.Service
}

// ChatFactoryResult holds everything produced by the factory.
// Callers (console, channel manager) unpack what they need.
type ChatFactoryResult struct {
	ChatAgent    *agent.ChatAgent
	LLM          model.LLM
	SwappableLLM *provider.SwappableLLM // hot-swappable LLM wrapper (nil in non-Studio callers)
	ProviderName string
	ModelName    string
	// ProviderConfigured is false when the agent was built without a usable
	// provider (AllowMissingProvider) and is running on the placeholder LLM.
	// Code mode uses this to prompt the user to run /model before a turn.
	ProviderConfigured    bool
	Compactor             *persistentsession.Compactor
	InternalTools         []tool.Tool
	MCPToolsets           []tool.Toolset
	MemorySearchAvailable bool
	IndexingDone          chan struct{}
	SessionService        session.Service
	StartupNotices        []string
	PromptBuilder         *agent.SystemPromptBuilder
	CredentialStore       *credentials.Store // Encrypted credential store (nil if unavailable)

	// Cleanup aggregates all deferred cleanups (embedder, MCP, file watcher).
	// The caller must call this when the ChatAgent is no longer needed.
	Cleanup func()

	// ShutdownSandbox stops sandbox containers without destroying them.
	// Used during graceful daemon shutdown to preserve containers across restarts.
	// Nil when sandbox is not enabled.
	ShutdownSandbox func()

	// SandboxPool is the ToolNodePool used for OpenShell/K8s/Incus tool
	// wrapping (nil when sandbox is disabled). Adaptive scheduler uses
	// SandboxPool.Remove after DestroySandbox so the next tick rebinds.
	SandboxPool sandbox.ToolNodePool
}

// mainThreadToolAllowlist returns the set of tool names the top-level coding
// agent gets directly (as opposed to only through sub-agents / lazy ToolIndex
// surfacing). Everything else stays in named groups reachable via delegate_tasks
// or search_tools.
//
// The tree-sitter structural navigation tools (repo_map, code_definition,
// code_references) are main-thread on purpose: they let the agent locate a
// symbol's definition/references in ONE call instead of falling back to broad
// grep_search plus repeated full-file read_file. Session analysis showed the
// latter pattern (e.g. re-reading the same large files 10-12x) inflates the
// prompt and makes each inference progressively slower. See
// docs/architecture/code-intelligence.md.
func mainThreadToolAllowlist() map[string]bool {
	return map[string]bool{
		"read_file":          true,
		"write_file":         true,
		"edit_file":          true,
		"shell_command":      true,
		"grep_search":        true,
		"find_files":         true,
		"repo_map":           true,
		"code_definition":    true,
		"code_references":    true,
		"memory_save":        true,
		"memory_search":      true,
		"memory_delete":      true,
		"delegate_tasks":     true,
		"announce_plan":      true,
		"resolve_credential": true,
		"skill_lookup":       true,
		// Platform web search must be main-thread always-on. If it only lives in
		// the "web" group, ToolIndex may never inject it and the agent claims
		// "no web search tools" even when General selects Perplexity.
		"perplexity_web_search": true,
	}
}

// NewWiredChatAgent creates a fully-wired ChatAgent ready for use by any caller
// (interactive console, channel manager, daemon, etc.).
//
// It performs the entire initialization sequence: LLM provider, internal tools,
// memory (store, indexer, memory tools), MCP toolsets, session service, system
// prompt, flow registry, flow distiller, compactor, and knowledge search.
//
// The returned ChatFactoryResult contains the ChatAgent and all auxiliary objects
// that callers may need. Callers MUST call result.Cleanup() when done.
func NewWiredChatAgent(ctx context.Context, cfg *ChatFactoryConfig) (*ChatFactoryResult, error) {
	var cleanups []func()
	cleanup := func() {
		// Run cleanups in reverse order (LIFO, like defer)
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// --- 0. Initialize credential store (must happen before LLM — secrets are scrubbed from config after migration) ---
	var credStore *credentials.Store
	configDir, configDirErr := config.GetConfigDir()
	if configDirErr == nil {
		cs, csErr := credentials.Open(configDir)
		if csErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to open credential store", "error", csErr)
			}
		} else {
			credStore = cs
			tools.SetCredentialStore(cs)

			// Wire credential store into config package for standard server lookups.
			// In platform mode, the daemon has already set a DB-backed getter
			// (daemonSecretGetter) that reads from platform_secrets first. Do NOT
			// overwrite it — otherwise IsStandardServerInstalled() will read from
			// the file-based store and miss keys that are only in the DB.
			// (Platform mode is always active; this getter is set by the daemon.)

			// Wire credential store into API handlers
			api.SetAPICredentialStore(cs)

			// Auto-migrate secrets from config.yaml to the encrypted store (one-time)
			migrated, migrateErr := credentials.MigrateFromConfig(cs, cfg.AppConfig, nil)
			if migrateErr != nil {
				if cfg.DebugMode {
					slog.Warn("credential migration error", "error", migrateErr)
				}
			} else if migrated > 0 {
				// Re-save config.yaml with secrets scrubbed
				if saveErr := config.SaveAppConfig(cfg.AppConfig); saveErr != nil {
					if cfg.DebugMode {
						slog.Warn("failed to save scrubbed config", "error", saveErr)
					}
				} else if cfg.DebugMode {
					slog.Debug("migrated secrets to encrypted store", "component", "chat-factory", "count", migrated)
				}
			}

			// Inject secrets back into config map so GetProvider() can read them.
			// This is needed because migration scrubs secrets from the config map,
			// and some providers (e.g. openai_compat) only read from the config map
			// with no env var fallback.
			config.InjectProviderSecretsToConfig(cfg.AppConfig, cs.GetSecret)

			// Setup provider env vars from credential store
			config.SetupAllProviderEnvFromStore(cfg.AppConfig, cs.GetSecret)
		}
	}
	// Fallback: if credential store unavailable, use legacy env setup
	if credStore == nil {
		config.SetupAllProviderEnv(cfg.AppConfig)
	}

	// --- 1. Initialize LLM ---
	if cfg.DebugMode {
		slog.Debug("initializing LLM provider", "component", "chat-factory")
	}
	rawLLM, err := provider.GetProvider(ctx, cfg.ProviderName, cfg.ModelName, cfg.AppConfig)
	providerConfigured := err == nil
	if err != nil {
		if !cfg.AllowMissingProvider {
			return nil, fmt.Errorf("failed to initialize provider '%s' with model '%s': %w",
				cfg.ProviderName, cfg.ModelName, err)
		}
		// Code-mode path: no provider yet. Install a placeholder so the agent
		// (tools, MCP, sandbox, session) can still be built; the user picks a
		// model via /model, which swaps in the real LLM.
		if cfg.DebugMode {
			slog.Debug("no provider configured; using placeholder LLM (code mode)", "component", "chat-factory", "error", err)
		}
		rawLLM = provider.NewPlaceholderLLM()
		cfg.ProviderName = ""
		cfg.ModelName = ""
	}
	if cfg.DebugMode {
		slog.Debug("provider initialized", "component", "chat-factory", "provider", cfg.ProviderName, "model", cfg.ModelName)
	}

	// Wrap in SwappableLLM so the model can be hot-swapped without rebuilding
	// the entire ChatAgent (tools, MCP, sandbox, ToolIndex all survive).
	swappableLLM := provider.NewSwappableLLM(rawLLM)
	var llm model.LLM = swappableLLM

	// --- 2. Initialize internal tools ---
	// Tools are organized into groups. The main thread gets only essential tools
	// (read, write, edit, shell, search, memory, delegate). All other tools are
	// available to sub-agents via named groups in delegate_tasks.
	coreTools, err := tools.GetInternalTools()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize internal tools: %w", err)
	}

	// Credential tools → deferred category
	var credToolsSlice []tool.Tool
	credTools, credErr := tools.GetCredentialTools()
	if credErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create credential tools", "error", credErr)
		}
	} else {
		credToolsSlice = credTools
	}

	// Model-backed Perplexity/Sonar is registered only when General → Web Search
	// Tool selects it (and provider/model are configured).
	if cfg.AppConfig != nil &&
		isSelectedWebSearchServer(cfg.AppConfig, "perplexity") &&
		cfg.AppConfig.PerplexityWebSearch.Provider != "" &&
		cfg.AppConfig.PerplexityWebSearch.Model != "" {
		perplexityTool, perplexityErr := tools.NewPerplexityWebSearchTool(cfg.AppConfig, provider.GetProvider)
		if perplexityErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create perplexity web search tool", "error", perplexityErr)
			}
		} else {
			coreTools = append(coreTools, perplexityTool)
		}
	}

	// --- 2b/2c. Initialize semantic memory ---
	// Memory is fully managed by the DB (per-team vector tables).
	// No file-based vector store needed.
	memorySearchAvailable := true
	indexingDone := make(chan struct{})
	close(indexingDone)

	// Create memory tools → core category (always available)
	// Platform mode: create memory tools with nil backing stores.
	// At runtime, they detect stores from request context (injected by
	// TenantMiddleware / ChatRunner.InjectMemoryStores) and route there.
	memorySaveTool, msErr := tools.NewMemorySaveTool()
	if msErr == nil {
		coreTools = append(coreTools, memorySaveTool)
	}
	searchTool, searchErr := tools.NewMemorySearchTool()
	if searchErr == nil {
		coreTools = append(coreTools, searchTool)
	}
	memoryDeleteTool, mdErr := tools.NewMemoryDeleteTool()
	if mdErr == nil {
		coreTools = append(coreTools, memoryDeleteTool)
	}

	// --- 2d. Initialize scheduler tools → deferred category ---
	var schedToolsSlice []tool.Tool
	schedTools, schedErr := tools.GetSchedulerTools()
	if schedErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create scheduler tools", "error", schedErr)
		}
	} else {
		schedToolsSlice = schedTools
	}

	// --- 2d-ii. Initialize distill_flow tool → deferred category ---
	var distillToolsSlice []tool.Tool
	distillTools, distillErr := tools.GetDistillTools()
	if distillErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create distill tools", "error", distillErr)
		}
	} else {
		distillToolsSlice = distillTools
	}

	// --- 2e. Initialize process management tools → core category ---
	processTools, procErr := tools.GetProcessTools()
	if procErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create process tools", "error", procErr)
		}
	} else {
		coreTools = append(coreTools, processTools...)
	}
	cleanups = append(cleanups, tools.CleanupProcessManager)

	// --- 2f. Initialize browser automation tools → deferred category ---
	browserCfg := browserConfigFromApp(cfg.AppConfig)
	browserMgr := browser.NewManager(browserCfg)
	var browserToolsSlice []tool.Tool
	var browserErr error
	if cfg.AppConfig != nil && sandbox.IsSandboxEnabled(&cfg.AppConfig.Sandbox) {
		browserToolsSlice, browserErr = tools.GetBrowserToolsForSandbox(browserMgr)
	} else {
		browserToolsSlice, browserErr = tools.GetBrowserTools(browserMgr)
	}
	if browserErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create browser tools", "error", browserErr)
		}
		browserToolsSlice = nil
	}
	cleanups = append(cleanups, browserMgr.Cleanup)

	// --- 2f-ii. Initialize email tools → deferred category ---
	// Create the email client here (before GetEmailTools) so tools are available
	// in both console and daemon modes. The daemon's initEmailTools becomes a
	// no-op when the factory has already set the client.
	if cfg.AppConfig != nil {
		emailCfg := cfg.AppConfig.Channels.Email
		emailPassword := emailCfg.Password
		if emailPassword == "" && credStore != nil {
			emailPassword = credStore.GetSecret("channels.email.password")
		}
		if emailPassword != "" && emailCfg.IMAPServer != "" &&
			emailCfg.SMTPServer != "" && emailCfg.Address != "" {
			client, clientErr := emailpkg.NewClient(&emailpkg.Config{
				Provider:     emailCfg.Provider,
				IMAPServer:   emailCfg.IMAPServer,
				SMTPServer:   emailCfg.SMTPServer,
				Address:      emailCfg.Address,
				Username:     emailCfg.Username,
				Password:     emailPassword,
				Folder:       emailCfg.Folder,
				MaxBodyChars: emailCfg.MaxBodyChars,
			})
			if clientErr != nil {
				if cfg.DebugMode {
					slog.Warn("failed to create email client", "error", clientErr)
				}
			} else {
				tools.SetEmailClient(client)
			}
		}
	}
	var emailToolsSlice []tool.Tool
	emailTools, emailErr := tools.GetEmailTools()
	if emailErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create email tools", "error", emailErr)
		}
	} else if len(emailTools) > 0 {
		emailToolsSlice = emailTools
	}

	// --- 2g. Sub-agent delegation tool → core category ---
	var subAgentMgr *agent.SubAgentManager
	var fleetOnlyTools []tool.Tool // fleet-only tools (run_fleet_phase), assigned to SubAgentManager.FleetTools
	if cfg.AppConfig.SubAgents.IsSubAgentsEnabled() {
		delegateTool, delegateErr := tools.GetDelegateTasksTool()
		if delegateErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create delegate_tasks tool", "error", delegateErr)
			}
		} else {
			coreTools = append(coreTools, delegateTool)

			// read_task_result: allows the orchestrator to retrieve full sub-task
			// outputs that were summarized by delegate_tasks to prevent context explosion.
			readResultTool, readResultErr := tools.NewReadTaskResultTool()
			if readResultErr == nil {
				coreTools = append(coreTools, readResultTool)
			}

			// announce_plan: allows the orchestrator to announce a
			// structured plan before starting work. Plan steps are auto-
			// progressed by AfterToolCallback (no update_plan needed).
			announcePlanTool, apErr := tools.NewAnnouncePlanTool()
			if apErr == nil {
				coreTools = append(coreTools, announcePlanTool)
			}

			subAgentCfg := agent.SubAgentConfig{
				MaxDepth:      cfg.AppConfig.SubAgents.MaxDepth,
				MaxConcurrent: cfg.AppConfig.SubAgents.MaxConcurrent,
				TaskTimeout:   cfg.AppConfig.SubAgents.TaskTimeout(),
			}
			subAgentMgr = agent.NewSubAgentManager(subAgentCfg)
		}
	}

	// --- 2h. Initialize skills system → deferred category ---
	var skillIndex string
	var skillToolSlice []tool.Tool
	var skillLookupTool tool.Tool // hoisted for SubAgentManager wiring
	if cfg.AppConfig != nil && cfg.AppConfig.Skills.IsSkillsEnabled() {
		// In platform mode, skills are resolved per-request from context-injected stores
		// (platform → org → team). The static index is empty; the per-request merged index
		// is injected by InjectSkillIndex in chat_handlers.go.
		// The skill_lookup tool is still created (with an empty static index) so it can
		// resolve skills from context stores at runtime.
		skillTool, stErr := tools.NewSkillLookupTool(nil)
		if stErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create skill_lookup tool", "error", stErr)
			}
		} else {
			skillToolSlice = append(skillToolSlice, skillTool)
			skillLookupTool = skillTool // keep reference for sub-agent injection
		}
		if cfg.DebugMode {
			slog.Debug("skills system initialized", "component", "chat-factory", "platform", cfg.PlatformMode)
		}
	}

	// --- 3. Load MCP tools from cache (lazy) ---
	mcpCfg, err := loadMCPConfig(ctx, cfg.PlatformMode)
	if err != nil {
		slog.Warn("failed to load MCP config", "error", err)
	}
	var lazyToolsets []*agent.LazyMCPToolset

	if mcpCfg != nil && len(mcpCfg.MCPServers) > 0 {
		for name, serverCfg := range mcpCfg.MCPServers {
			if !serverCfg.IsEnabled() {
				continue
			}

			// Platform mode: tools are stored in the DB (cached_tools column)
			cachedTools := getPlatformCachedTools(ctx, name)

			// Fallback: if the platform DB has no cached tools yet (e.g., async
			// discovery is still running after a fresh install), try the
			// file-based cache. This avoids the race condition where PreWarm
			// or the first chat request arrives before async discovery completes.
			if len(cachedTools) == 0 {
				if fileTools := cache.GetToolsForServer(name); len(fileTools) > 0 {
					cachedTools = fileTools
					slog.Debug("MCP server: using file cache fallback", "server", name, "tools", len(cachedTools))
				}
			}

			// Filter out excluded tools for standard servers (e.g., tavily_research
			// is expensive and redundant with Astonish's delegation-based approach).
			if excluded := config.GetExcludedTools(name); excluded != nil {
				filtered := make([]cache.ToolEntry, 0, len(cachedTools))
				for _, t := range cachedTools {
					if !excluded[t.Name] {
						filtered = append(filtered, t)
					}
				}
				cachedTools = filtered
			}

			if len(cachedTools) == 0 {
				if cfg.DebugMode {
					slog.Debug("MCP server has no cached tools", "component", "lazy-mcp", "server", name)
				}
				continue
			}
			lt := agent.NewLazyMCPToolset(name, cachedTools, serverCfg, cfg.DebugMode)
			if credStore != nil {
				lt.SetCredentialResolver(credStore)
			}
			lazyToolsets = append(lazyToolsets, lt)
		}

		// Note: MCP toolsets are NOT collected into a separate slice anymore.
		// Each server is registered as its own tool group (mcp:<name>)
		// in section 4b below, wrapped in SanitizedToolset at that point.
	}
	cleanups = append(cleanups, func() {
		for _, lt := range lazyToolsets {
			lt.Cleanup()
		}
	})

	// Track startup notices
	var startupNotices []string

	if mcpCfg != nil && len(mcpCfg.MCPServers) > 0 && len(lazyToolsets) == 0 {
		startupNotices = append(startupNotices,
			"MCP servers are configured but no tools are cached yet. "+
				"Run 'astonish studio' or 'astonish tools refresh' to set them up.")
	}

	// --- 3b. Initialize sandbox (session container isolation) ---
	// When sandbox is enabled, all internal tools are wrapped with NodeTool
	// proxies that route execution to an astonish node inside an Incus
	// container. Container creation is lazy — the first tool call triggers
	// cloning from the template and starting the node process.
	var sandboxNodePool *sandbox.NodeClientPool      // hoisted for save_sandbox_template tool (Incus only)
	var sandboxIncusClient *incus.IncusClient        // hoisted for save_sandbox_template tool (Incus only)
	var sandboxTplRegistry *sandbox.TemplateRegistry // hoisted for save_sandbox_template tool (Incus only)
	var sandboxSessRegistry *sandbox.SessionRegistry // hoisted for save_sandbox_template tool (Incus only)
	var backendSandboxPool sandbox.ToolNodePool      // hoisted for sub-agent alias (K8s/OpenShell)
	var backendSessRegistry *sandbox.SessionRegistry // hoisted for sub-agent alias (K8s/OpenShell)
	if cfg.AppConfig != nil && sandbox.IsSandboxEnabled(&cfg.AppConfig.Sandbox) {
		sandbox.SetSandboxConfig(&cfg.AppConfig.Sandbox)
		kind := sandbox.BackendKind(cfg.AppConfig.Sandbox.BackendKind())

		switch kind {
		case sandbox.BackendKindK8s, sandbox.BackendKindMock, sandbox.BackendKindOpenShell:
			// --- Backend-agnostic chat path (Phase F) ---
			// Minimal feature set:
			//   - tool wrapping via BackendPool + WrapToolsWithPool
			//   - per-session pod created lazily on first tool call
			//   - cleanup destroys pods on shutdown
			//
			// NOT wired on K8s (deferred to Phase G):
			//   - idle watchdog (BackendPool has no equivalent yet)
			//   - prune-on-startup (no analogue for stale pods today)
			//   - async template refresh
			//   - save/use/list_sandbox_template tools (Incus image-clone)
			//   - MCP stdio servers inside sandbox (host fallback OK)

			// In platform mode, use PG-backed session registry for cross-replica
			// consistency. Prefer TenantContext from ctx; fall back to default
			// org slug from AppConfig + "general" team.
			var sessRegistry *sandbox.SessionRegistry
			if cfg.PlatformMode {
				if svc := store.FromContext(ctx); svc != nil && svc.Platform != nil {
					orgSlug := store.OrgSlugFromContext(ctx)
					teamSlug := store.TeamSlugFromContext(ctx)
					if orgSlug == "" {
						orgSlug = cfg.AppConfig.Storage.Auth.GetDefaultOrgSlug()
					}
					if teamSlug == "" {
						teamSlug = "general"
					}
					if provider, ok := svc.Platform.(store.SandboxSessionProvider); ok {
						if sessStore := provider.SandboxSessionsForTeam(ctx, orgSlug, teamSlug); sessStore != nil {
							sessRegistry = sandbox.NewSessionRegistryFromStore(sessStore)
						}
					}
				}
			}

			if sessRegistry == nil {
				var regErr error
				sessRegistry, regErr = sandbox.NewSessionRegistry()
				if regErr != nil {
					return nil, fmt.Errorf("sandbox is enabled but session registry failed: %w", regErr)
				}
			}

			b, backendCleanup, berr := sandbox.BackendFromAppConfigWithSessions(cfg.AppConfig, sessRegistry)
			if berr != nil {
				return nil, fmt.Errorf("sandbox is enabled but the %s backend is not available: %w\n\nTo disable sandbox, set 'sandbox.enabled: false' in ~/.config/astonish/config.yaml", kind, berr)
			}

			limits := sandbox.EffectiveLimits(&cfg.AppConfig.Sandbox)
			pool := sandbox.NewBackendPool(b, sandbox.ToResourceLimits(limits))

			// Wrap all tool category slices with pool-backed proxies.
			// Browser tools are NOT wrapped as Node tools; they call the
			// Manager directly, and the Manager is wired below to launch
			// Chromium/CloakBrowser inside the sandbox when supported.
			coreTools = sandbox.WrapToolsWithPool(coreTools, pool)
			if len(credToolsSlice) > 0 {
				credToolsSlice = sandbox.WrapToolsWithPool(credToolsSlice, pool)
			}
			if len(schedToolsSlice) > 0 {
				schedToolsSlice = sandbox.WrapToolsWithPool(schedToolsSlice, pool)
			}
			if len(distillToolsSlice) > 0 {
				distillToolsSlice = sandbox.WrapToolsWithPool(distillToolsSlice, pool)
			}
			if len(emailToolsSlice) > 0 {
				emailToolsSlice = sandbox.WrapToolsWithPool(emailToolsSlice, pool)
			}
			if len(skillToolSlice) > 0 {
				skillToolSlice = sandbox.WrapToolsWithPool(skillToolSlice, pool)
			}
			// Note: browserToolsSlice NOT wrapped — browser tools call the
			// Manager directly (which connects to Chromium in the sandbox via CDP).

			// Wire browser-in-sandbox for OpenShell. CloakBrowser + KasmVNC
			// are pre-installed in the sandbox image; we just need to launch
			// them and tunnel the CDP connection through the gateway exec API.
			if kind == sandbox.BackendKindOpenShell {
				if osb, ok := b.(*openshell.OpenShellBackend); ok && osb.Gateway() != nil {
					gw := osb.Gateway()
					sessReg := osb.Sessions()
					if WireOpenShellBrowserManager(browserMgr, gw, sessReg, sessReg.TouchActivity) {
						// Register the dial func for the VNC proxy handler so it
						// can tunnel HTTP/WebSocket to KasmVNC inside the sandbox.
						api.SetVNCContainerDialFunc(browserMgr.ContainerDialFunc)

						// Register callbacks for the PDF browser manager so PDF
						// export uses the same sandbox Chrome (OpenShell path).
						// The PDF resolve func ensures a sandbox exists (re-creates
						// it if pruned), mirroring the Incus EnsureSessionContainer
						// behaviour. This is different from the browse resolve (which
						// only does a lookup) because PDF export can be triggered long
						// after the sandbox was pruned by the orphan reconciler.
						pdfResolve := func(sessionID string) (string, string, error) {
							rec, err := sessReg.GetSession(sessionID)
							if err == nil && rec != nil && rec.PodName != "" {
								return rec.PodName, "127.0.0.1", nil
							}

							slog.Info("PDF resolve: sandbox not found, creating",
								"component", "chat-factory", "sessionID", sessionID)
							ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
							defer cancel()

							if _, err := osb.CreateSession(ctx, sandbox.SessionSpec{
								SessionID:  sessionID,
								TemplateID: sandbox.BaseTemplateID,
							}); err != nil {
								return "", "", fmt.Errorf("PDF resolve: create session: %w", err)
							}
							if err := osb.StartSession(ctx, sessionID); err != nil {
								return "", "", fmt.Errorf("PDF resolve: start session: %w", err)
							}
							if err := osb.WaitForSessionReady(ctx, sessionID); err != nil {
								return "", "", fmt.Errorf("PDF resolve: wait for ready: %w", err)
							}

							rec, err = sessReg.GetSession(sessionID)
							if err != nil || rec == nil || rec.PodName == "" {
								return "", "", fmt.Errorf("PDF resolve: session still not found after create")
							}
							return rec.PodName, "127.0.0.1", nil
						}

						api.SetPDFBrowserCallbacksForBackend(
							string(sandbox.BackendKindOpenShell),
							pdfResolve,
							browserMgr.ContainerStartBrowserFunc,
							browserMgr.ContainerDialFunc,
						)

						slog.Info("browser-in-sandbox enabled for OpenShell",
							"component", "chat-factory")
					}
				}
			}

			if kind == sandbox.BackendKindK8s {
				if sandbox.WireBackendBrowserManager(browserMgr, b, sessRegistry, sessRegistry.TouchActivity) {
					api.SetVNCContainerDialFunc(browserMgr.ContainerDialFunc)

					// Register callbacks for the PDF browser manager so PDF export
					// uses the same backend-routed Chrome path as browser tools.
					pdfResolve := newBackendPDFResolveFunc(b, sessRegistry)
					api.SetPDFBrowserCallbacksForBackend(
						string(sandbox.BackendKindK8s),
						pdfResolve,
						browserMgr.ContainerStartBrowserFunc,
						browserMgr.ContainerDialFunc,
					)

					slog.Info("browser-in-sandbox enabled for K8s",
						"component", "chat-factory")
				}
			}

			// Lazy MCP toolsets: wire the pool so stdio MCP servers start
			// inside the session's sandbox (same as Incus path). SSE
			// transports are unaffected (isSSETransport check inside).
			for _, lt := range lazyToolsets {
				lt.SetSandboxPool(pool)
			}
			if len(lazyToolsets) > 0 {
				slog.Info("sandbox MCP routing enabled for backend",
					"component", "chat-factory", "backend", string(kind), "toolsets", len(lazyToolsets))
			}

			// sandboxNodePool, sandboxIncusClient, sandboxTplRegistry,
			// sandboxSessRegistry all remain nil — downstream nil-guards
			// silently skip Incus-only features (template tools, idle
			// watchdog, prune, shutdown).

			// Hoist pool and session registry for sub-agent aliasing
			// (OnChildSession closure wired below, after subAgentMgr is built).
			backendSandboxPool = pool
			backendSessRegistry = sessRegistry

			cleanups = append(cleanups, func() {
				pool.Cleanup()
			})
			if backendCleanup != nil {
				bc := backendCleanup // capture
				cleanups = append(cleanups, func() {
					bc()
				})
			}

			slog.Info("sandbox wired to backend-agnostic pool for chat",
				"component", "chat-factory", "backend", string(kind))

		case sandbox.BackendKindIncus, "":
			// --- Legacy Incus path (unchanged) ---
			sandboxClient, sandboxErr := sandbox.SetupSandboxRuntime()
			if sandboxErr != nil {
				return nil, fmt.Errorf("sandbox is enabled but the runtime is not available: %w\n\nTo disable sandbox, set 'sandbox.enabled: false' in ~/.config/astonish/config.yaml", sandboxErr)
			}

			sessRegistry, regErr := sandbox.NewSessionRegistry()
			if regErr != nil {
				return nil, fmt.Errorf("sandbox is enabled but session registry failed: %w", regErr)
			}

			tplRegistry, tplErr := sandbox.NewTemplateRegistry()
			if tplErr != nil {
				if cfg.DebugMode {
					slog.Warn("failed to create template registry", "error", tplErr)
				}
			}

			// Create a pool that manages per-session LazyNodeClients.
			// Each chat session gets its own container, created lazily
			// on the first tool call for that session.
			limits := sandbox.EffectiveLimits(&cfg.AppConfig.Sandbox)
			nodePool := sandbox.NewNodeClientPool(sandboxClient, sessRegistry, tplRegistry, "", &limits)

			// Wrap all tool category slices with NodeTool proxies (pool-backed).
			// Browser tools are NOT wrapped — they run on the host and need direct
			// access to Chrome. Each category slice is wrapped independently so
			// deferred tools are already sandbox-ready when activated later.
			coreTools = sandbox.WrapToolsWithNode(coreTools, nodePool)
			if len(credToolsSlice) > 0 {
				credToolsSlice = sandbox.WrapToolsWithNode(credToolsSlice, nodePool)
			}
			if len(schedToolsSlice) > 0 {
				schedToolsSlice = sandbox.WrapToolsWithNode(schedToolsSlice, nodePool)
			}
			if len(distillToolsSlice) > 0 {
				distillToolsSlice = sandbox.WrapToolsWithNode(distillToolsSlice, nodePool)
			}
			if len(emailToolsSlice) > 0 {
				emailToolsSlice = sandbox.WrapToolsWithNode(emailToolsSlice, nodePool)
			}
			if len(skillToolSlice) > 0 {
				skillToolSlice = sandbox.WrapToolsWithNode(skillToolSlice, nodePool)
			}
			// Note: browserToolsSlice is intentionally NOT wrapped — Manager
			// drives in-container Chromium via SandboxEnabled callbacks.

			// Hoist references for template tool registration
			sandboxNodePool = nodePool
			sandboxIncusClient = sandboxClient
			sandboxTplRegistry = tplRegistry
			sandboxSessRegistry = sessRegistry

			// Wire browser to run inside the session container when sandbox is
			// available. The browser resolves the session container (already
			// managed by NodeClientPool) and starts Chromium + KasmVNC inside it.
			{
				pool := nodePool // capture for closure
				if WireIncusBrowserManager(browserMgr, sandboxClient, pool.TouchActivity) {
					// browser-in-sandbox enabled
				}
			}

			// Wire sandbox pool to all lazy MCP toolsets so stdio MCP servers
			// start inside the session's container instead of on the host.
			// SSE transport servers are unaffected (isSSETransport check inside).
			for _, lt := range lazyToolsets {
				lt.SetSandboxPool(sandbox.AsNodePool(nodePool))
			}

			// Async refresh: check all templates for stale binaries in the background.
			// Must NOT block startup (was the cause of the 502 bug).
			if tplRegistry != nil {
				go sandbox.RefreshAllIfNeeded(sandboxClient, tplRegistry)
			}

			// Auto-prune stale session containers from previous daemon runs.
			// In platform mode, session IDs live in DB across many team schemas;
			// startup pruning is skipped — use scheduled cleanup instead.

			// Start idle watchdog: stops containers that have been inactive for the
			// configured timeout (default 10 min), preserving them for fast restart.
			idleTimeout := sandbox.EffectiveIdleTimeout(&cfg.AppConfig.Sandbox)
			if idleTimeout > 0 {
				idleCtx, idleCancel := context.WithCancel(context.Background())
				nodePool.StartIdleWatchdog(idleCtx, idleTimeout)
				cleanups = append(cleanups, idleCancel)
				if cfg.DebugMode {
					slog.Debug("sandbox idle watchdog enabled", "component", "chat-factory", "timeout", idleTimeout)
				}
			}

			cleanups = append(cleanups, func() {
				// Cleanup destroys all per-session containers
				nodePool.Cleanup()
			})

		default:
			return nil, fmt.Errorf("sandbox: unsupported backend kind %q", kind)
		}
	}

	// --- 4. Create session service ---
	// Platform mode: sessions are persisted in the DB per-request.
	// Use in-memory as the factory-level default; actual persistence is
	// handled by context-injected stores at the API/channel layer.
	// Callers may inject a session service (e.g. code mode passes a
	// persistent FileStore) to override the in-memory default.
	sessionService := cfg.SessionService
	if sessionService == nil {
		sessionService = session.InMemoryService()
		if cfg.DebugMode {
			slog.Debug("session storage: in-memory (platform mode, DB per-request)", "component", "chat-factory")
		}
	} else if cfg.DebugMode {
		slog.Debug("session storage: caller-provided service", "component", "chat-factory")
	}

	// --- 4b. Build tool groups for sub-agent delegation ---
	// Tools are organized into named groups. The main thread gets only essential
	// tools (file ops, shell, search, memory, delegate). All other tools are
	// available to sub-agents via named groups in the delegate_tasks tool.

	// Split coreTools into main-thread essentials and the full "core" group
	// for sub-agents. See mainThreadToolAllowlist for the membership.
	mainThreadToolNames := mainThreadToolAllowlist()

	var mainThreadTools []tool.Tool
	for _, t := range coreTools {
		if mainThreadToolNames[t.Name()] {
			mainThreadTools = append(mainThreadTools, t)
		}
	}
	// Also pull resolve_credential from credToolsSlice into the main thread.
	// It stays in the credentials group too (for sub-agents); the ToolIndex
	// deduplicates and gives main-thread precedence.
	for _, t := range credToolsSlice {
		if mainThreadToolNames[t.Name()] {
			mainThreadTools = append(mainThreadTools, t)
		}
	}
	// Same for skill_lookup — the system prompt instructs the agent to call
	// it directly, so it must be on the main thread.
	for _, t := range skillToolSlice {
		if mainThreadToolNames[t.Name()] {
			mainThreadTools = append(mainThreadTools, t)
		}
	}

	// Separate web-oriented tools from core into their own group
	webToolNames := map[string]bool{
		"web_fetch":             true,
		"perplexity_web_search": true,
		"read_pdf":              true,
		"http_request":          true,
	}
	processToolNames := map[string]bool{
		"process_read":  true,
		"process_write": true,
		"process_list":  true,
		"process_kill":  true,
	}

	var coreGroupTools []tool.Tool
	var webGroupTools []tool.Tool
	var processGroupTools []tool.Tool
	for _, t := range coreTools {
		name := t.Name()
		if webToolNames[name] {
			webGroupTools = append(webGroupTools, t)
		} else if processToolNames[name] {
			processGroupTools = append(processGroupTools, t)
		} else {
			coreGroupTools = append(coreGroupTools, t)
		}
	}

	// Build tool groups map
	toolGroups := map[string]*agent.ToolGroup{}

	toolGroups["core"] = &agent.ToolGroup{
		Name:        "core",
		Description: "File operations, shell commands, search, memory, git diff, file tree",
		Tools:       coreGroupTools,
	}
	if len(webGroupTools) > 0 {
		toolGroups["web"] = &agent.ToolGroup{
			Name:        "web",
			Description: "Fetch web pages, read PDFs, make HTTP API requests",
			Tools:       webGroupTools,
		}
	}
	if len(processGroupTools) > 0 {
		toolGroups["process"] = &agent.ToolGroup{
			Name:        "process",
			Description: "Read, write, list, and kill background processes",
			Tools:       processGroupTools,
		}
	}
	if len(browserToolsSlice) > 0 {
		toolGroups["browser"] = &agent.ToolGroup{
			Name:        "browser",
			Description: "Browse and research websites, get live/current data, take screenshots, fill forms, interact with web pages",
			Tools:       browserToolsSlice,
		}
	}
	if len(credToolsSlice) > 0 {
		toolGroups["credentials"] = &agent.ToolGroup{
			Name:        "credentials",
			Description: "Save, retrieve, list, test, and resolve credentials",
			Tools:       credToolsSlice,
		}
	}
	if len(schedToolsSlice) > 0 {
		toolGroups["scheduler"] = &agent.ToolGroup{
			Name:        "scheduler",
			Description: "Schedule and manage recurring jobs",
			Tools:       schedToolsSlice,
		}
	}
	if len(emailToolsSlice) > 0 {
		toolGroups["email"] = &agent.ToolGroup{
			Name:        "email",
			Description: "Read, send, search, and wait for email",
			Tools:       emailToolsSlice,
		}
	}
	if len(distillToolsSlice) > 0 {
		toolGroups["distill"] = &agent.ToolGroup{
			Name:        "distill",
			Description: "Distill conversation flows into reusable YAML",
			Tools:       distillToolsSlice,
		}
	}
	if len(skillToolSlice) > 0 {
		toolGroups["skill"] = &agent.ToolGroup{
			Name:        "skill",
			Description: "Look up available CLI tool skills",
			Tools:       skillToolSlice,
		}
	}

	// MCP servers — each is its own group
	for _, lt := range lazyToolsets {
		sanitized := agent.NewSanitizedToolset(lt, cfg.DebugMode)
		groupName := "mcp:" + lt.Name()
		serverDesc := fmt.Sprintf("MCP server: %s (%d tools)", lt.Name(), lt.ToolCount())
		toolGroups[groupName] = &agent.ToolGroup{
			Name:        groupName,
			Description: serverDesc,
			Toolsets:    []tool.Toolset{sanitized},
		}
	}

	if cfg.DebugMode {
		totalTools := 0
		for _, g := range toolGroups {
			totalTools += len(g.Tools)
		}
		slog.Debug("tool groups configured", "component", "chat-factory",
			"groups", len(toolGroups), "totalTools", totalTools, "mainThreadTools", len(mainThreadTools))
	}

	// --- 5. Build system prompt ---
	workspaceDir := cfg.WorkspaceDir
	if workspaceDir == "" {
		if wd, wdErr := os.Getwd(); wdErr != nil {
			slog.Warn("failed to get working directory for system prompt", "error", wdErr)
		} else {
			workspaceDir = wd
		}
	}

	// The prompt builder uses all tools for capability detection.
	// It receives sorted tool groups for the delegation guidance section.
	var allToolsForPrompt []tool.Tool
	var allToolsetsForPrompt []tool.Toolset
	sortedGroups := make([]*agent.ToolGroup, 0, len(toolGroups))
	for _, g := range toolGroups {
		allToolsForPrompt = append(allToolsForPrompt, g.Tools...)
		allToolsetsForPrompt = append(allToolsetsForPrompt, g.Toolsets...)
		sortedGroups = append(sortedGroups, g)
	}
	sort.Slice(sortedGroups, func(i, j int) bool {
		return sortedGroups[i].Name < sortedGroups[j].Name
	})

	promptBuilder := &agent.SystemPromptBuilder{
		Tools:        allToolsForPrompt,
		Toolsets:     allToolsetsForPrompt,
		WorkspaceDir: workspaceDir,
		Catalog:      sortedGroups,
	}

	// Sandbox workspace guidance
	if cfg.AppConfig != nil && sandbox.IsSandboxEnabled(&cfg.AppConfig.Sandbox) {
		promptBuilder.SandboxEnabled = true
		switch sandbox.BackendKind(cfg.AppConfig.Sandbox.BackendKind()) {
		case sandbox.BackendKindOpenShell:
			promptBuilder.SandboxWorkspaceDir = "/sandbox"
		default:
			promptBuilder.SandboxWorkspaceDir = "/root"
		}
	}

	// Advertise the General-selected web search/extract tools to the prompt.
	// Use the factory AppConfig (platform cascade), not LoadAppConfig() alone —
	// file config.yaml is empty for web tools in platform mode.
	namedLazy := make([]namedToolset, 0, len(lazyToolsets))
	for _, lt := range lazyToolsets {
		namedLazy = append(namedLazy, lt)
	}
	if searchName := selectedWebSearchToolName(cfg.AppConfig, namedLazy); searchName != "" {
		promptBuilder.WebSearchAvailable = true
		promptBuilder.WebSearchToolName = searchName
	}
	if extractName := selectedWebExtractToolName(cfg.AppConfig, namedLazy); extractName != "" {
		promptBuilder.WebExtractAvailable = true
		promptBuilder.WebExtractToolName = extractName
	}

	// Check browser availability (native browser tools)
	if browserErr == nil && len(browserToolsSlice) > 0 {
		promptBuilder.BrowserAvailable = true
	}

	// Populate agent identity from config (for web portal interactions)
	if cfg.AppConfig != nil && cfg.AppConfig.AgentIdentity.IsConfigured() {
		id := &cfg.AppConfig.AgentIdentity
		identityEmail := id.Email
		// Fall back to the channel email address if identity email is not set
		if identityEmail == "" && cfg.AppConfig.Channels.Email.Address != "" {
			identityEmail = cfg.AppConfig.Channels.Email.Address
		}
		promptBuilder.Identity = &agent.AgentIdentity{
			Name:     id.Name,
			Username: id.Username,
			Email:    identityEmail,
			Bio:      id.Bio,
			Website:  id.Website,
			Locale:   id.Locale,
			Timezone: id.Timezone,
		}
	}

	// Check memory search availability
	if memorySearchAvailable {
		promptBuilder.MemorySearchAvailable = true
	}

	// Resolve timezone: general.timezone -> agent_identity.timezone -> system default
	if cfg.AppConfig != nil {
		tz := cfg.AppConfig.General.Timezone
		if tz == "" && cfg.AppConfig.AgentIdentity.Timezone != "" {
			tz = cfg.AppConfig.AgentIdentity.Timezone
		}
		if tz != "" {
			promptBuilder.Timezone = tz
		}
	}

	// Set skill index for system prompt
	if skillIndex != "" {
		promptBuilder.SkillIndex = skillIndex
	}

	// Platform mode: custom instructions are stored per-team in the DB
	// and injected via the request context. No filesystem INSTRUCTIONS.md/SELF.md needed.

	// Reconcile flow knowledge docs — disabled: flows are now executed
	// on-demand via run_flow tool, not injected as knowledge.
	flowsDir, _ := flowstore.GetFlowsDir()

	// Load custom prompt from app config
	if cfg.AppConfig != nil && cfg.AppConfig.Chat.SystemPrompt != "" {
		promptBuilder.CustomPrompt = cfg.AppConfig.Chat.SystemPrompt
	}

	// Code mode: load project guidance from AGENTS.md (fallback CLAUDE.md),
	// discovered by walking upward from the workspace to the project root,
	// following the agents.md convention. Platform mode uses per-team DB
	// instructions instead, so this is gated on the caller's request.
	if cfg.LoadProjectContext {
		if ctxContent := agent.LoadProjectContext(workspaceDir); ctxContent != "" {
			promptBuilder.ProjectContext = ctxContent
			if cfg.DebugMode {
				slog.Debug("loaded project guidance", "component", "chat-factory", "bytes", len(ctxContent))
			}
		}
	}

	// --- 5b. Initialize fleet registry ---
	// Fleet data lives in the database (per-team).
	// Register fleet tools (they use context-based stores at runtime)
	// and fleet-only tools for sub-agents.
	if cfg.AppConfig != nil && cfg.AppConfig.SubAgents.IsSubAgentsEnabled() {
		var ftErr error
		fleetOnlyTools, ftErr = tools.GetFleetTools()
		if ftErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create fleet internal tools", "error", ftErr)
			}
		}
	}

	// Fleet plan tools → deferred category (available in both modes)
	var fleetToolsSlice []tool.Tool
	fleetPlanTools, fptErr := tools.GetFleetPlanTools()
	if fptErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create fleet plan tools", "error", fptErr)
		}
	} else {
		fleetToolsSlice = append(fleetToolsSlice, fleetPlanTools...)
	}

	fleetPlanValidateTools, fpvErr := tools.GetFleetPlanValidateTools()
	if fpvErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create fleet plan validate tools", "error", fpvErr)
		}
	} else {
		fleetToolsSlice = append(fleetToolsSlice, fleetPlanValidateTools...)
	}

	fleetSetupTools, fstErr := tools.GetFleetSetupTools()
	if fstErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create fleet setup tools", "error", fstErr)
		}
	} else {
		fleetToolsSlice = append(fleetToolsSlice, fleetSetupTools...)
	}

	if len(fleetToolsSlice) > 0 {
		toolGroups["fleet"] = &agent.ToolGroup{
			Name:        "fleet",
			Description: "Create and validate fleet plans",
			Tools:       fleetToolsSlice,
		}
	}

	// Sandbox template tools → deferred category
	if sandboxNodePool != nil && sandboxIncusClient != nil && sandboxTplRegistry != nil {
		var sandboxTplTools []tool.Tool
		tplTool, tplErr := tools.NewSaveSandboxTemplateTool(sandboxNodePool, sandboxIncusClient, sandboxTplRegistry, sandboxSessRegistry)
		if tplErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create save_sandbox_template tool", "error", tplErr)
			}
		} else {
			sandboxTplTools = append(sandboxTplTools, tplTool)
		}

		listTplTool, listErr := tools.NewListSandboxTemplatesTool(sandboxTplRegistry)
		if listErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create list_sandbox_templates tool", "error", listErr)
			}
		} else {
			sandboxTplTools = append(sandboxTplTools, listTplTool)
		}

		useTplTool, useErr := tools.NewUseSandboxTemplateTool(sandboxNodePool, sandboxTplRegistry)
		if useErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create use_sandbox_template tool", "error", useErr)
			}
		} else {
			sandboxTplTools = append(sandboxTplTools, useTplTool)
		}

		if len(sandboxTplTools) > 0 {
			toolGroups["sandbox_templates"] = &agent.ToolGroup{
				Name:        "sandbox_templates",
				Description: "Save, list, and use sandbox container templates",
				Tools:       sandboxTplTools,
			}
		}
	}

	// Drill tools → deferred category
	var drillToolsSlice []tool.Tool
	drillTools, drillErr := tools.GetDrillTools()
	if drillErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create drill tools", "error", drillErr)
		}
	} else {
		drillToolsSlice = append(drillToolsSlice, drillTools...)
	}

	runDrillTool, runDrillErr := tools.NewRunDrillTool(sandboxNodePool, sandboxTplRegistry, browserMgr, adrill.NewLLMProviderFromModel(llm))
	if runDrillErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create run_drill tool", "error", runDrillErr)
		}
	} else {
		drillToolsSlice = append(drillToolsSlice, runDrillTool)
	}

	injectCredsTool, injectCredsErr := tools.NewInjectDrillCredentialsTool(sandboxNodePool, sandboxTplRegistry, browserMgr)
	if injectCredsErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create inject_drill_credentials tool", "error", injectCredsErr)
		}
	} else {
		drillToolsSlice = append(drillToolsSlice, injectCredsTool)
	}

	if len(drillToolsSlice) > 0 {
		toolGroups["drill"] = &agent.ToolGroup{
			Name:        "drill",
			Description: "Create, validate, and run test drills",
			Tools:       drillToolsSlice,
		}
	}

	// Create the dedicated tool index for retrieval-based tool discovery.
	// One document per tool (name + description) enables accurate semantic
	// search without the chunking problems of the general memory store.
	//
	// Platform mode: uses pgvector backed vector store (shared platform DB).
	var toolIndex *agent.ToolIndex
	var vectorStore agent.ToolVectorStore
	var toolEmbedFunc agent.EmbedFunc

	if cfg.PlatformToolVectorStore != nil && cfg.PlatformEmbedFunc != nil {
		// Use pre-created vector store from the backend
		vectorStore = cfg.PlatformToolVectorStore
		toolEmbedFunc = cfg.PlatformEmbedFunc
	}

	if vectorStore != nil && toolEmbedFunc != nil {
		var tiErr error
		toolIndex, tiErr = agent.NewToolIndex(vectorStore, toolEmbedFunc)
		if tiErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create tool index", "error", tiErr)
			}
		} else {
			sortedGroups := agent.SortedGroups(toolGroups)
			if syncErr := toolIndex.SyncTools(context.Background(), mainThreadTools, sortedGroups); syncErr != nil {
				if cfg.DebugMode {
					slog.Warn("failed to sync tool index", "error", syncErr)
				}
			} else if cfg.DebugMode {
				slog.Debug("tool index ready", "tools_indexed", toolIndex.Count())
			}
		}
	}

	// --- 5d. Create flow tools (search_flows, run_flow) ---
	// Flow discovery and execution via dedicated tools rather than
	// knowledge injection.
	if searchFlowsTool, sfErr := tools.NewSearchFlowsTool(); sfErr == nil {
		mainThreadTools = append(mainThreadTools, searchFlowsTool)
	} else if cfg.DebugMode {
		slog.Warn("failed to create search_flows tool", "error", sfErr)
	}
	if runFlowTool, rfErr := tools.NewRunFlowTool(); rfErr == nil {
		mainThreadTools = append(mainThreadTools, runFlowTool)
	} else if cfg.DebugMode {
		slog.Warn("failed to create run_flow tool", "error", rfErr)
	}

	// Create search_tools and add to main thread tools if tool index is available.
	// The onResults callback uses a forward reference to chatAgent (set after
	// ChatAgent creation) so that search_tools discoveries feed into the
	// dynamic tool injection system.
	var chatAgentRef *agent.ChatAgent
	var searchToolsTool tool.Tool
	if toolIndex != nil {
		var stErr error
		searchToolsTool, stErr = tools.NewSearchToolsTool(toolIndex, func(names []string) {
			if chatAgentRef != nil {
				chatAgentRef.RegisterSearchToolsResults(names)
			}
		})
		if stErr == nil {
			mainThreadTools = append(mainThreadTools, searchToolsTool)
		} else if cfg.DebugMode {
			slog.Warn("failed to create search_tools", "error", stErr)
		}
	}

	// --- 6. Create ChatAgent ---
	// Main thread gets essential tools (file ops, shell, search, memory,
	// delegate). Additional tools are dynamically injected per-turn based
	// on hybrid search relevance and search_tools discoveries.
	chatAgent := agent.NewChatAgent(
		llm, mainThreadTools, nil, sessionService,
		promptBuilder, cfg.DebugMode, cfg.AutoApprove,
	)
	chatAgentRef = chatAgent // wire the forward reference for search_tools callback

	// Wire tool index to ChatAgent for per-turn auto-discovery
	if toolIndex != nil {
		chatAgent.ToolIndex = toolIndex
	}

	// Wire credential redactor to ChatAgent, session service, and sub-agents.
	// The Redactor is a security boundary — it MUST always exist (never nil),
	// even when the file-based credential store fails (platform mode with
	// regenerated .store_key). In platform mode, it gets hydrated per-request
	// from the PG-backed credential store in chat_handlers.go.
	redactor := credentials.NewRedactor()
	if credStore != nil {
		redactor = credStore.Redactor()
		chatAgent.CredentialStore = credStore
	}
	chatAgent.Redactor = redactor
	// Create per-session PendingVault for <<<SECRET_N>>> token resolution.
	// The vault extracts raw secrets from user messages before the LLM sees
	// them, and registers them with the redactor as a safety net.
	chatAgent.PendingSecrets = credentials.NewPendingVault(redactor)
	// Attach proactive secret scanner if enabled (default: true).
	// Scans user messages for untagged secrets using keyword, entropy,
	// and structural analysis before they reach the LLM provider.
	if cfg.AppConfig == nil || cfg.AppConfig.Security.IsSecretScannerEnabled() {
		scanner := credentials.NewSecretScanner()
		if cfg.AppConfig != nil {
			sc := cfg.AppConfig.Security.SecretScanner
			if sc.EntropyThreshold > 0 {
				scanner.EntropyThreshold = sc.EntropyThreshold
			}
			if sc.MinTokenLength > 0 {
				scanner.MinTokenLength = sc.MinTokenLength
			}
		}
		chatAgent.PendingSecrets.Scanner = scanner
	}
	if credStore != nil {
		// Also wire to file-based session store for transcript redaction
		if fs, ok := sessionService.(*persistentsession.FileStore); ok {
			fs.RedactFunc = redactor.Redact
			// Wire retroactive redaction callback so that after save_credential
			// completes, the current session's transcript is scrubbed of any
			// secrets that were persisted before the credential was registered.
			chatAgent.RedactSessionFunc = fs.RedactSession
		}
	}

	// Wire SubAgentManager — deferred until all tools and LLM are known.
	// Sub-agents get tool groups for group-based tool resolution.
	if subAgentMgr != nil {
		subAgentMgr.LLM = llm
		subAgentMgr.ToolGroups = toolGroups
		subAgentMgr.FleetTools = fleetOnlyTools
		subAgentMgr.SessionService = sessionService
		subAgentMgr.Redactor = chatAgent.Redactor
		subAgentMgr.CredentialStore = credStore
		subAgentMgr.PendingSecrets = chatAgent.PendingSecrets
		subAgentMgr.EventForwarder = chatAgent.ForwardSubTaskEvent
		// Wire structured sub-task progress: drive plan step transitions from
		// sub-task lifecycle events, then forward to ChatAgent.SubTaskProgressCallback
		// (which is set dynamically by ChatRunner.Run() for Studio sessions).
		subAgentMgr.SubTaskProgress = func(evt agent.SubTaskProgressEvent) {
			// Drive plan step transitions via explicit plan_step binding.
			// task_start → resolve step name, register task, mark step "running"
			// task_complete → mark task done; if all tasks for step done, mark step "complete"
			if plan := chatAgent.GetActivePlan(); plan != nil {
				switch evt.Type {
				case "task_start":
					// Resolve the plan step: use explicit plan_step if set, else prefix match
					stepName := plan.ResolveStepName(evt.PlanStep, evt.TaskName)
					if stepName != "" {
						if emittedStep := plan.StartStep(stepName, evt.TaskName); emittedStep != "" {
							if chatAgent.SubTaskProgressCallback != nil {
								chatAgent.SubTaskProgressCallback(agent.SubTaskProgressEvent{
									Type:       "plan_step_update",
									StepName:   emittedStep,
									StepStatus: "running",
								})
							}
						}
					}
				case "task_complete":
					stepName := plan.ResolveStepName(evt.PlanStep, evt.TaskName)
					if stepName != "" {
						if completedStep := plan.CompleteTask(stepName, evt.TaskName); completedStep != "" {
							if chatAgent.SubTaskProgressCallback != nil {
								chatAgent.SubTaskProgressCallback(agent.SubTaskProgressEvent{
									Type:       "plan_step_update",
									StepName:   completedStep,
									StepStatus: "complete",
								})
							}
						}
					}
				}
			}
			// Forward the original event to the UI as before.
			if chatAgent.SubTaskProgressCallback != nil {
				chatAgent.SubTaskProgressCallback(evt)
			}
		}
		// Wire file artifact capture so sub-agent write_file/edit_file calls
		// propagate file artifacts to the parent ChatAgent for channel delivery
		// (e.g., as Telegram document attachments).
		subAgentMgr.FileArtifactCapture = chatAgent.CaptureFileArtifact

		// Wire MCP group fallback resolver to handle the race condition where
		// async MCP discovery completes after the chat agent was initialized.
		// When a sub-agent requests "mcp:<server>" but it wasn't in ToolGroups
		// at init time, this resolver attempts to create the group on-the-fly
		// from newly-cached tools in the DB.
		if mcpCfg != nil {
			capturedMCPCfg := mcpCfg
			capturedDebugMode := cfg.DebugMode
			capturedCredStore := credStore
			subAgentMgr.MCPGroupResolver = func(ctx context.Context, serverName string) *agent.ToolGroup {
				serverCfg, exists := capturedMCPCfg.MCPServers[serverName]
				if !exists || !serverCfg.IsEnabled() {
					return nil
				}
				// Check access control
				if !config.IsStandardServerInstalled(serverName) {
					// For non-standard servers, check platform stores
					mcpStores := store.MCPServerStoresFromContext(ctx)
					if mcpStores != nil {
						accessible := false
						if mcpStores.Platform != nil {
							if s, _ := mcpStores.Platform.Get(ctx, serverName); s != nil && s.IsEnabled() {
								accessible = true
							}
						}
						if !accessible && mcpStores.Org != nil {
							if s, _ := mcpStores.Org.Get(ctx, serverName); s != nil && s.IsEnabled() {
								accessible = true
							}
						}
						if !accessible && mcpStores.Team != nil {
							if s, _ := mcpStores.Team.Get(ctx, serverName); s != nil && s.IsEnabled() {
								accessible = true
							}
						}
						if !accessible {
							return nil
						}
					}
				}
				// Try to get cached tools from the DB (they may have been
				// populated by async discovery since our init time).
				cachedTools := getPlatformCachedTools(ctx, serverName)
				if len(cachedTools) == 0 {
					return nil
				}
				// Filter out excluded tools
				if excluded := config.GetExcludedTools(serverName); excluded != nil {
					filtered := make([]cache.ToolEntry, 0, len(cachedTools))
					for _, t := range cachedTools {
						if !excluded[t.Name] {
							filtered = append(filtered, t)
						}
					}
					cachedTools = filtered
				}
				if len(cachedTools) == 0 {
					return nil
				}
				// Build the ToolGroup on-the-fly
				lt := agent.NewLazyMCPToolset(serverName, cachedTools, serverCfg, capturedDebugMode)
				if capturedCredStore != nil {
					lt.SetCredentialResolver(capturedCredStore)
				}
				sanitized := agent.NewSanitizedToolset(lt, capturedDebugMode)
				groupName := "mcp:" + serverName
				return &agent.ToolGroup{
					Name:        groupName,
					Description: fmt.Sprintf("MCP server: %s (%d tools)", serverName, lt.ToolCount()),
					Toolsets:    []tool.Toolset{sanitized},
				}
			}
		}

		subAgentMgr.AppName = "astonish"
		// Use SystemUserID as the fallback for child sessions.
		// The per-request user ID from context (store.UserIDFromContext) takes
		// precedence in sub_agent.go — this is only the factory-time default.
		subAgentMgr.UserID = store.SystemUserID

		// Wire plan tools: announce_plan emits events through
		// the same ChatAgent.SubTaskProgressCallback pipeline.
		tools.SetPlanProgressCallback(func(evt agent.SubTaskProgressEvent) {
			if chatAgent.SubTaskProgressCallback != nil {
				chatAgent.SubTaskProgressCallback(evt)
			}
		})
		// Wire plan state storage so AfterToolCallback can auto-progress steps.
		tools.SetPlanStateCallback(func(goal string, steps []agent.PlanStepInfo) {
			plan := agent.NewPlanState(goal, steps)
			chatAgent.SetActivePlan(plan)
		})
		// Wire tool discovery so sub-agents can auto-discover their tools
		if toolIndex != nil {
			subAgentMgr.ToolIndex = toolIndex
		}
		// Provide a factory that creates child-scoped search_tools instances.
		// Each sub-agent gets its own instance whose onResults callback feeds
		// into the child's dynamic tool injection pipeline (not the parent's).
		if toolIndex != nil {
			idx := toolIndex // capture for closure
			subAgentMgr.SearchToolsFactory = func(onResults func([]string)) (tool.Tool, error) {
				return tools.NewSearchToolsTool(idx, onResults)
			}
		}
		// Wire skill awareness into sub-agents so they can load skill
		// content on demand (e.g., git/github skills for repo operations).
		if skillLookupTool != nil {
			subAgentMgr.SkillLookupTool = skillLookupTool
		}
		if skillIndex != "" {
			subAgentMgr.SkillIndex = skillIndex
		}
		// Wire web search/extract tool names so sub-agents get proper guidance
		// preferring dedicated search over web_fetch.
		if promptBuilder.WebSearchAvailable && promptBuilder.WebSearchToolName != "" {
			// Normalize hyphens → underscores to match actual MCP tool names
			subAgentMgr.WebSearchToolName = strings.ReplaceAll(promptBuilder.WebSearchToolName, "-", "_")
		}
		if promptBuilder.WebExtractAvailable && promptBuilder.WebExtractToolName != "" {
			subAgentMgr.WebExtractToolName = strings.ReplaceAll(promptBuilder.WebExtractToolName, "-", "_")
		}
		// Wire sandbox guidance so sub-agents know about workspace and recovery.
		subAgentMgr.SandboxEnabled = promptBuilder.SandboxEnabled
		subAgentMgr.SandboxWorkspaceDir = promptBuilder.SandboxWorkspaceDir
		// Alias sub-agent sessions to the parent's sandbox container so they
		// share the same container instead of each creating a new one.
		if sandboxNodePool != nil {
			pool := sandboxNodePool
			bmgr := browserMgr // capture for closure
			sessReg := sandboxSessRegistry
			subAgentMgr.OnChildSession = func(parentSessionID, childSessionID string) {
				pool.Alias(childSessionID, parentSessionID)
				bmgr.AliasSession(childSessionID, parentSessionID)
				// Register the child session in the sandbox session registry so
				// Backend.ExecStreaming (used by MCP servers) can resolve it to
				// the same container as the parent.
				if sessReg != nil {
					if entry := sessReg.Get(parentSessionID); entry != nil {
						_ = sessReg.Put(childSessionID, entry.ContainerName, entry.TemplateName)
					}
				}
			}
		} else if backendSandboxPool != nil {
			pool := backendSandboxPool
			bmgr := browserMgr // capture for closure
			sessReg := backendSessRegistry
			subAgentMgr.OnChildSession = func(parentSessionID, childSessionID string) {
				pool.Alias(childSessionID, parentSessionID)
				bmgr.AliasSession(childSessionID, parentSessionID)
				// Register the child session in the backend session registry
				// so Backend.ExecStreaming (MCP servers) and other non-pool
				// paths can resolve the child to the parent's pod/container.
				if sessReg != nil {
					if entry := sessReg.Get(parentSessionID); entry != nil {
						_ = sessReg.Put(childSessionID, entry.ContainerName, entry.TemplateName)
					}
				}
			}
		}
		tools.SetSubAgentManager(subAgentMgr)
		chatAgent.SubAgentManager = subAgentMgr
	}

	// --- 6b. Initialize Flow Registry ---
	registryPath, regErr := agent.DefaultRegistryPath()
	if regErr == nil {
		registry, regLoadErr := agent.NewFlowRegistry(registryPath)
		if regLoadErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to load flow registry", "error", regLoadErr)
			}
		} else {
			chatAgent.FlowRegistry = registry

			// Wire flow registry into search_flows / run_flow tools
			tools.SetFlowRegistry(registry)

			if flowsDir != "" {
				added, syncErr := registry.SyncFromDirectory(flowsDir)
				if syncErr != nil {
					if cfg.DebugMode {
						slog.Warn("flow registry sync failed", "error", syncErr)
					}
				} else if added > 0 && cfg.DebugMode {
					slog.Debug("auto-registered new flows", "count", added, "dir", flowsDir)
				}
			}
		}
	}

	// Wire interactive flow runner for the run_flow tool
	flowRunner := NewInteractiveFlowRunner(cfg.AppConfig, cfg.ProviderName, cfg.ModelName, cfg.DebugMode)
	tools.SetFlowRunnerAccess(flowRunner)

	// --- 6b2. Wire knowledge search callbacks ---
	// These callbacks are context-aware: they check the invocation context
	// for a DB-backed ThreeTierSearcher (injected by ChatRunner.InjectMemoryStores)
	// and use it for cross-tier search.
	if memorySearchAvailable {
		chatAgent.KnowledgeSearch = func(ctx context.Context, query string, bm25Query string, maxResults int, minScore float64) ([]agent.KnowledgeSearchResult, error) {
			searcher := store.ThreeTierSearcherFromContext(ctx)
			if searcher == nil {
				return nil, nil
			}
			// Use bm25Query (conversation-context-enriched) for keyword matching
			// when available; the tsvector OR search benefits from extra terms.
			searchQuery := query
			if bm25Query != "" {
				searchQuery = bm25Query
			}
			// Fetch extra candidates before scenario-card de-duplication and query
			// filtering so duplicate/unrelated cards do not starve the final context.
			pgResults, err := searcher.SearchAllTiers(ctx, searchQuery, maxResults*2, minScore)
			if err != nil {
				return nil, err
			}
			pgResults = memory.FilterPreferredScenarioResultsForQuery(query, pgResults)
			if len(pgResults) > maxResults {
				pgResults = pgResults[:maxResults]
			}
			var knowledgeResults []agent.KnowledgeSearchResult
			for _, r := range pgResults {
				knowledgeResults = append(knowledgeResults, agent.KnowledgeSearchResult{
					ID:        r.ID,
					Path:      r.Path,
					Score:     r.Score,
					Snippet:   r.Snippet,
					Category:  r.Category,
					Scope:     r.Scope,
					CreatedBy: r.CreatedBy,
					CreatedAt: r.CreatedAt,
					SessionID: r.SessionID,
				})
			}
			return knowledgeResults, nil
		}

		chatAgent.KnowledgeSearchByCategory = func(ctx context.Context, query string, bm25Query string, maxResults int, minScore float64, category string) ([]agent.KnowledgeSearchResult, error) {
			searcher := store.ThreeTierSearcherFromContext(ctx)
			if searcher == nil {
				return nil, nil
			}
			searchQuery := query
			if bm25Query != "" {
				searchQuery = bm25Query
			}
			// Fetch extra candidates before scenario-card de-duplication and query
			// filtering so duplicate/unrelated cards do not starve the final context.
			pgResults, err := searcher.SearchAllTiersByCategory(ctx, searchQuery, maxResults*2, minScore, category)
			if err != nil {
				return nil, err
			}
			pgResults = memory.FilterPreferredScenarioResultsForQuery(query, pgResults)
			if len(pgResults) > maxResults {
				pgResults = pgResults[:maxResults]
			}
			var knowledgeResults []agent.KnowledgeSearchResult
			for _, r := range pgResults {
				knowledgeResults = append(knowledgeResults, agent.KnowledgeSearchResult{
					ID:        r.ID,
					Path:      r.Path,
					Score:     r.Score,
					Snippet:   r.Snippet,
					Category:  r.Category,
					Scope:     r.Scope,
					CreatedBy: r.CreatedBy,
					CreatedAt: r.CreatedAt,
					SessionID: r.SessionID,
				})
			}
			return knowledgeResults, nil
		}

		if cfg.DebugMode {
			slog.Debug("auto knowledge retrieval: enabled")
		}
	}

	// --- 6c. Initialize Flow Distiller ---
	validateYAML := func(yamlStr string, distillerTools []agent.DistillerToolInfo) agent.FlowValidationResult {
		apiTools := make([]api.ToolInfo, len(distillerTools))
		for i, t := range distillerTools {
			apiTools[i] = api.ToolInfo{Name: t.Name, Description: t.Description, Source: t.Source}
		}
		result := api.ValidateFlowYAML(yamlStr, apiTools)
		return agent.FlowValidationResult{Valid: result.Valid, Errors: result.Errors}
	}

	dryRunYAML := func(yamlStr string, toolSchemas map[string]json.RawMessage) agent.FlowDryRunResult {
		result := api.DryRunFlowYAML(yamlStr, toolSchemas)
		return agent.FlowDryRunResult{Valid: result.Valid, Warnings: result.Warnings, Errors: result.Errors}
	}

	chatAgent.FlowDistiller = agent.NewFlowDistiller(
		llm, allToolsForPrompt, allToolsetsForPrompt,
		api.GetFlowSchema, validateYAML, dryRunYAML,
	)

	// Wire the FlowRunner closure for dry-run execution of distilled flows.
	// This uses the same InteractiveFlowRunner that powers the run_flow tool.
	chatAgent.FlowRunner = func(ctx context.Context, yamlContent string, params map[string]string) (*agent.DryRunExecResult, error) {
		sessionKey := fmt.Sprintf("__distill_dryrun__%d", time.Now().UnixNano())
		defer flowRunner.CleanupSession(sessionKey)

		result, err := flowRunner.RunFlowFromYAML(ctx, yamlContent, "distill-dry-run", params, "", sessionKey)
		if err != nil {
			return &agent.DryRunExecResult{
				Success: false,
				Error:   err.Error(),
			}, nil
		}

		switch result.Status {
		case "completed":
			return &agent.DryRunExecResult{
				Success:      true,
				Output:       result.Output,
				NodesVisited: result.NodesVisited,
			}, nil
		case "waiting_for_input":
			// Flow paused on input node — for dry-run this means we couldn't
			// supply a required parameter. Report which one.
			return &agent.DryRunExecResult{
				Success:      false,
				Output:       result.Output,
				Error:        fmt.Sprintf("flow paused on input node %q (prompt: %s) — parameter value not available", result.InputNode, result.InputPrompt),
				NodesVisited: result.NodesVisited,
			}, nil
		case "error":
			return &agent.DryRunExecResult{
				Success:      false,
				Output:       result.Output,
				Error:        result.Message,
				NodesVisited: result.NodesVisited,
			}, nil
		default:
			return &agent.DryRunExecResult{
				Success:      false,
				Output:       result.Output,
				Error:        fmt.Sprintf("unexpected flow status: %s — %s", result.Status, result.Message),
				NodesVisited: result.NodesVisited,
			}, nil
		}
	}

	if cfg.AppConfig != nil && cfg.AppConfig.Chat.FlowSaveDir != "" {
		chatAgent.FlowSaveDir = cfg.AppConfig.Chat.FlowSaveDir
	}
	if cfg.AppConfig != nil && cfg.AppConfig.Chat.MaxToolCalls > 0 {
		chatAgent.MaxToolCalls = cfg.AppConfig.Chat.MaxToolCalls
	}

	// --- 6e. Initialize context compaction ---
	var compactor *persistentsession.Compactor
	if cfg.AppConfig == nil || cfg.AppConfig.Sessions.Compaction.IsCompactionEnabled() {
		contextWindow := provider.ResolveContextWindowCached(ctx, cfg.ProviderName, cfg.ModelName, cfg.AppConfig)
		compactor = persistentsession.NewCompactor(contextWindow)
		if cfg.AppConfig != nil {
			compCfg := &cfg.AppConfig.Sessions.Compaction
			compactor.Threshold = compCfg.GetThreshold()
			compactor.PreserveRecent = compCfg.GetPreserveRecent()
		}
		compactor.DebugMode = cfg.DebugMode
		compactor.LLM = makeLLMFunc(llm)
		chatAgent.Compactor = compactor
		if subAgentMgr != nil {
			subAgentMgr.Compactor = compactor
		}
		if cfg.DebugMode {
			slog.Debug("context compaction enabled", "window_tokens", contextWindow, "threshold_pct", compactor.Threshold*100)
		}
	}

	// --- 6d. Wire memory and flow context ---
	// Use PlatformReflector which writes to team memory store via context
	// injection. It also runs extraction/consolidation after each turn
	// to keep session memories well-organized.
	chatAgent.PlatformReflector = &agent.PlatformReflector{
		LLM:       llm,
		DebugMode: cfg.DebugMode,
	}

	// Build ShutdownSandbox callback (nil when sandbox is not enabled)
	var shutdownSandbox func()
	if sandboxNodePool != nil {
		pool := sandboxNodePool
		shutdownSandbox = func() {
			pool.CleanupForShutdown()
		}
	}

	var resultPool sandbox.ToolNodePool
	if backendSandboxPool != nil {
		resultPool = backendSandboxPool
	} else if sandboxNodePool != nil {
		resultPool = sandbox.AsNodePool(sandboxNodePool)
	}

	return &ChatFactoryResult{
		ChatAgent:             chatAgent,
		LLM:                   llm,
		SwappableLLM:          swappableLLM,
		ProviderName:          cfg.ProviderName,
		ModelName:             cfg.ModelName,
		ProviderConfigured:    providerConfigured,
		Compactor:             compactor,
		InternalTools:         allToolsForPrompt,
		MCPToolsets:           allToolsetsForPrompt,
		MemorySearchAvailable: memorySearchAvailable,
		IndexingDone:          indexingDone,
		SessionService:        sessionService,
		StartupNotices:        startupNotices,
		PromptBuilder:         promptBuilder,
		CredentialStore:       credStore,
		Cleanup:               cleanup,
		ShutdownSandbox:       shutdownSandbox,
		SandboxPool:           resultPool,
	}, nil
}

// makeLLMFunc creates a simple LLM call function suitable for FlowDistiller /
// Compactor. Prefer per-request LLM override (team-specific model in platform
// mode) over the factory-time default.
func makeLLMFunc(llm model.LLM) func(ctx context.Context, prompt string) (string, error) {
	return func(ctx context.Context, prompt string) (string, error) {
		effectiveLLM := agent.LLMFromContext(ctx)
		if effectiveLLM == nil {
			effectiveLLM = llm
		}

		req := &model.LLMRequest{
			Contents: []*genai.Content{
				{
					Parts: []*genai.Part{{Text: prompt}},
					Role:  "user",
				},
			},
		}
		var text string
		for resp, err := range effectiveLLM.GenerateContent(ctx, req, false) {
			if err != nil {
				return text, err
			}
			if resp.Content != nil {
				for _, p := range resp.Content.Parts {
					if p.Text != "" {
						text += p.Text
					}
				}
			}
		}
		if text == "" {
			return "", fmt.Errorf("empty response from LLM")
		}
		return text, nil
	}
}

// MakeLLMFunc creates a simple prompt->response function from an LLM.
// Exported for use by the channel manager and other callers.
func MakeLLMFunc(llm model.LLM) func(ctx context.Context, prompt string) (string, error) {
	return makeLLMFunc(llm)
}

// browserConfigFromApp builds a browser.BrowserConfig by merging user settings
// from AppConfig with sensible defaults from browser.DefaultConfig().
func browserConfigFromApp(appCfg *config.AppConfig) browser.BrowserConfig {
	if appCfg == nil {
		return browser.DefaultConfig()
	}
	b := &appCfg.Browser
	return browser.OverrideConfig(browser.ConfigOverrides{
		Headless:            b.Headless,
		ViewportWidth:       b.ViewportWidth,
		ViewportHeight:      b.ViewportHeight,
		NoSandbox:           b.NoSandbox,
		ChromePath:          b.ChromePath,
		UserDataDir:         b.UserDataDir,
		NavigationTimeout:   b.NavigationTimeout,
		Proxy:               b.Proxy,
		RemoteCDPURL:        b.RemoteCDPURL,
		FingerprintSeed:     b.FingerprintSeed,
		FingerprintPlatform: b.FingerprintPlatform,
		HandoffBindAddress:  b.HandoffBindAddress,
		HandoffPort:         b.HandoffPort,
		KasmVNCPort:         b.KasmVNCPort,
		KasmVNCPassword:     b.KasmVNCPassword,
	})
}

// loadMCPConfig loads MCP server configurations based on deployment mode.
// In personal mode, reads from the local mcp_config.json file.
// In platform mode, reads from the context's platform+org+team MCP server stores
// and builds a config.MCPConfig with the merged (team overrides org overrides
// platform) view. Platform-tier servers are inherited by every org/team — this
// is the documented inheritance model for standard servers like Tavily that are
// installed at scope=platform.
func loadMCPConfig(ctx context.Context, platformMode bool) (*config.MCPConfig, error) {
	if !platformMode {
		return config.LoadMCPConfig()
	}

	// Platform mode: build MCPConfig from context stores.
	// First try the dedicated MCPServerStores context key (set by InjectMCPServerStores).
	mcpStores := store.MCPServerStoresFromContext(ctx)

	// If not set, extract from the Services in context (set by TenantMiddleware).
	if mcpStores == nil {
		svc := store.FromContext(ctx)
		if svc != nil && (svc.PlatformMCPServers != nil || svc.MCPServers != nil || svc.TeamMCPServers != nil) {
			mcpStores = &store.MCPServerStores{
				Platform: svc.PlatformMCPServers,
				Org:      svc.MCPServers,
				Team:     svc.TeamMCPServers,
			}
		}
	}

	if mcpStores == nil {
		// No stores available — return empty config
		return &config.MCPConfig{MCPServers: make(map[string]config.MCPServerConfig)}, nil
	}

	merged := make(map[string]config.MCPServerConfig)

	// 1. Load platform-level servers as base (cascade root).
	if mcpStores.Platform != nil {
		platformServers, err := mcpStores.Platform.List(ctx)
		if err != nil {
			slog.Warn("failed to load platform MCP servers", "error", err)
		} else {
			for _, s := range platformServers {
				merged[s.Name] = storeMCPServerToConfig(&s)
			}
		}
	}

	// 2. Org servers override platform by name.
	if mcpStores.Org != nil {
		orgServers, err := mcpStores.Org.List(ctx)
		if err != nil {
			slog.Warn("failed to load org MCP servers", "error", err)
		} else {
			for _, s := range orgServers {
				merged[s.Name] = storeMCPServerToConfig(&s)
			}
		}
	}

	// 3. Team servers override org+platform by name.
	if mcpStores.Team != nil {
		teamServers, err := mcpStores.Team.List(ctx)
		if err != nil {
			slog.Warn("failed to load team MCP servers", "error", err)
		} else {
			for _, s := range teamServers {
				merged[s.Name] = storeMCPServerToConfig(&s)
			}
		}
	}

	cfg := &config.MCPConfig{MCPServers: merged}

	// 4. Merge standard servers (Tavily, Brave, etc.) that are configured via
	// config.yaml / credential store. These are never stored in the DB but
	// should be available in platform mode just as they are in personal mode.
	// Pass the effective app config so team-level WebSearchTool is honored.
	config.MergeStandardServersWithConfig(cfg, api.EffectiveAppConfigFromContext(ctx, true))

	return cfg, nil
}

// namedToolset is the minimal surface selectedWeb* needs from lazy MCP toolsets.
type namedToolset interface {
	Name() string
}

func isSelectedWebSearchServer(appCfg *config.AppConfig, serverID string) bool {
	if appCfg == nil || appCfg.General.WebSearchTool == "" {
		return false
	}
	parts := strings.SplitN(appCfg.General.WebSearchTool, ":", 2)
	return len(parts) >= 1 && parts[0] == serverID
}

// selectedWebSearchToolName returns the General-selected web search tool name
// when that provider is actually available (installed credentials / Perplexity config / lazy toolset).
func selectedWebSearchToolName(appCfg *config.AppConfig, lazyToolsets []namedToolset) string {
	ok, serverName, toolName := api.IsWebSearchConfiguredWith(appCfg)
	if !ok || toolName == "" {
		return ""
	}
	toolName = strings.ReplaceAll(toolName, "-", "_")

	lazyByName := map[string]bool{}
	for _, lt := range lazyToolsets {
		lazyByName[lt.Name()] = true
	}

	if serverName == "perplexity" {
		if appCfg != nil && appCfg.PerplexityWebSearch.Provider != "" && appCfg.PerplexityWebSearch.Model != "" {
			return toolName
		}
		return ""
	}
	if config.IsStandardServerInstalled(serverName) || lazyByName[serverName] {
		return toolName
	}
	return ""
}

// selectedWebExtractToolName returns the General-selected web extract tool name when available.
func selectedWebExtractToolName(appCfg *config.AppConfig, lazyToolsets []namedToolset) string {
	ok, serverName, toolName := api.IsWebExtractConfiguredWith(appCfg)
	if !ok || toolName == "" {
		return ""
	}
	toolName = strings.ReplaceAll(toolName, "-", "_")

	lazyByName := map[string]bool{}
	for _, lt := range lazyToolsets {
		lazyByName[lt.Name()] = true
	}
	if config.IsStandardServerInstalled(serverName) || lazyByName[serverName] {
		return toolName
	}
	return ""
}

// storeMCPServerToConfig converts a store.MCPServer to config.MCPServerConfig.
func storeMCPServerToConfig(s *store.MCPServer) config.MCPServerConfig {
	return config.MCPServerConfig{
		Command:   s.Command,
		Args:      s.Args,
		Env:       s.Env,
		Transport: s.Transport,
		URL:       s.URL,
		Enabled:   s.Enabled,
	}
}

// getPlatformCachedTools extracts cached tool declarations from the MCP server
// stores in the context. Walks team → org → platform in that order, returning
// the first tier that has cached_tools for the given server name (matches the
// override semantics of loadMCPConfig: team beats org beats platform).
func getPlatformCachedTools(ctx context.Context, serverName string) []cache.ToolEntry {
	mcpStores := store.MCPServerStoresFromContext(ctx)

	// If not set via dedicated key, try Services from context
	if mcpStores == nil {
		svc := store.FromContext(ctx)
		if svc != nil && (svc.PlatformMCPServers != nil || svc.MCPServers != nil || svc.TeamMCPServers != nil) {
			mcpStores = &store.MCPServerStores{
				Platform: svc.PlatformMCPServers,
				Org:      svc.MCPServers,
				Team:     svc.TeamMCPServers,
			}
		}
	}

	if mcpStores == nil {
		return nil
	}

	// Try team first (highest precedence).
	if mcpStores.Team != nil {
		srv, err := mcpStores.Team.Get(ctx, serverName)
		if err == nil && srv != nil && len(srv.CachedTools) > 0 {
			return parseCachedToolsJSON(srv.CachedTools)
		}
	}

	// Fall back to org.
	if mcpStores.Org != nil {
		srv, err := mcpStores.Org.Get(ctx, serverName)
		if err == nil && srv != nil && len(srv.CachedTools) > 0 {
			return parseCachedToolsJSON(srv.CachedTools)
		}
	}

	// Fall back to platform (cascade root for standard servers like Tavily).
	if mcpStores.Platform != nil {
		srv, err := mcpStores.Platform.Get(ctx, serverName)
		if err == nil && srv != nil && len(srv.CachedTools) > 0 {
			return parseCachedToolsJSON(srv.CachedTools)
		}
	}

	return nil
}

// parseCachedToolsJSON parses the cached_tools JSONB column into cache.ToolEntry slice.
func parseCachedToolsJSON(data []byte) []cache.ToolEntry {
	// The cached_tools column stores an array of tool declarations
	// compatible with the MCPDiscoveredTool struct from the API:
	// [{"name": "...", "description": "...", "inputSchema": {...}}, ...]
	type discoveredTool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}

	var tools []discoveredTool
	if err := json.Unmarshal(data, &tools); err != nil {
		slog.Warn("failed to parse cached MCP tools", "error", err)
		return nil
	}

	entries := make([]cache.ToolEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, cache.ToolEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return entries
}
