package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/docs/slides"
	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"log/slog"
)

// ChatCatalogResources contains executables or explicitly supplied scoped resources.
// Prepared is used by the ordinary factory after its existing host initialization.
// Scoped callers leave Prepared false and must supply a sandbox pool. Browser and
// email transports must be explicitly supplied; neither has a global fallback.
type ChatCatalogResources struct {
	Backend          sandbox.Backend
	BrowserManager   *browser.Manager
	TemplateRegistry *sandbox.TemplateRegistry
	SessionRegistry  *sandbox.SessionRegistry
	LLM              model.LLM

	Prepared                                                             bool
	Pool                                                                 sandbox.ToolNodePool
	CoreTools, CredentialTools, SchedulerTools, SlideTools, DistillTools []tool.Tool
	BrowserTools, EmailTools, SkillTools                                 []tool.Tool
	FilesystemSkills                                                     []skills.Skill
	Groups                                                               map[string]*agent.ToolGroup
	SubAgentManager                                                      *agent.SubAgentManager
}

// ChatCatalog keeps identical executable objects in direct and discovery views.
// It does not own shared stores, sandbox pools or their lifecycle.
type ChatCatalog struct {
	MainThreadTools []tool.Tool
	Groups          map[string]*agent.ToolGroup
	AllTools        []tool.Tool
	SubAgentManager *agent.SubAgentManager
}

func buildChatBase(cfg *ChatFactoryConfig) (*ChatCatalogResources, error) {
	// --- 2. Initialize internal tools ---
	// Tools are organized into groups. The main thread gets only essential tools
	// (read, write, edit, shell, search, memory, delegate). All other tools are
	// available to sub-agents via named groups in delegate_tasks.
	coreTools, err := tools.GetInternalTools()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize internal tools: %w", err)
	}

	// Credential tools → deferred category.
	// Credentials require a platform vault (PG-backed or file-backed store) that
	// is never present in Astonish Code mode, so all credential tools are no-ops
	// there. Suppress them entirely so they are not advertised to the agent.
	var credToolsSlice []tool.Tool
	if !cfg.CodeMode {
		credTools, credErr := tools.GetCredentialTools()
		if credErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create credential tools", "error", credErr)
			}
		} else {
			credToolsSlice = credTools
		}
	}

	// Studio injects Perplexity per request because the shared agent can serve
	// multiple tenants. Only local Code mode owns a factory-scoped instance.
	if shouldRegisterFactoryPerplexity(cfg) {
		perplexityTool, perplexityErr := tools.NewPerplexityWebSearchTool(cfg.AppConfig, provider.GetProvider)
		if perplexityErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create perplexity web search tool", "error", perplexityErr)
			}
		} else {
			coreTools = append(coreTools, perplexityTool)
		}
	}

	// Create memory tools → core category.
	// Memory tools require a PG-backed store injected at request time by
	// TenantMiddleware / ChatRunner.InjectMemoryStores. That injection never
	// happens in Astonish Code mode (there is no platform context), so all
	// three tools are no-ops there and must not be registered — the agent
	// would waste turns calling them only to receive "not available" responses.
	if !cfg.CodeMode {
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
	}

	// --- 2d. Initialize scheduler tools → deferred category ---
	// Scheduler tools require a running scheduler daemon (platform service).
	// They are not available in Astonish Code mode.
	var schedToolsSlice []tool.Tool
	if !cfg.CodeMode {
		schedTools, schedErr := tools.GetSchedulerTools()
		if schedErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create scheduler tools", "error", schedErr)
			}
		} else {
			schedToolsSlice = schedTools
		}
	}

	// --- 2d-i. Initialize slide deck tools → deferred category ---
	// Deck authoring is a Studio/platform capability backed by the personal docs
	// store injected into each request. It is intentionally unavailable in Code mode.
	var slideToolsSlice []tool.Tool
	if !cfg.CodeMode {
		deckTools, deckErr := slides.GetTools()
		if deckErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create slide deck tools", "error", deckErr)
			}
		} else {
			slideToolsSlice = deckTools
		}
	}

	// --- 2d-ii. Initialize distill_flow tool → deferred category ---
	// Flow distillation is a platform service (requires LLM + flow registry).
	// It is not available in Astonish Code mode.
	var distillToolsSlice []tool.Tool
	if !cfg.CodeMode {
		distillTools, distillErr := tools.GetDistillTools()
		if distillErr != nil {
			if cfg.DebugMode {
				slog.Warn("failed to create distill tools", "error", distillErr)
			}
		} else {
			distillToolsSlice = distillTools
		}
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

	// --- 2g. Sub-agent delegation tool → core category ---
	var subAgentMgr *agent.SubAgentManager
	if cfg.AppConfig.SubAgents.IsSubAgentsEnabled() {
		subAgentCfg := agent.SubAgentConfig{
			MaxDepth:      cfg.AppConfig.SubAgents.MaxDepth,
			MaxConcurrent: cfg.AppConfig.SubAgents.MaxConcurrent,
			TaskTimeout:   cfg.AppConfig.SubAgents.TaskTimeout(),
		}
		subAgentMgr = agent.NewSubAgentManager(subAgentCfg)
		delegateTool, delegateErr := tools.NewDelegateTasksToolForManager(subAgentMgr)
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
			// structured plan before starting work. Plan phases are
			// auto-progressed for delegated work and explicitly updated by the
			// model via update_plan for main-thread work.
			announcePlanTool, apErr := tools.NewAnnouncePlanTool()
			if apErr == nil {
				coreTools = append(coreTools, announcePlanTool)
			}
			// update_plan: lets the model mark plan phases running/complete/
			// failed as it works on the main thread, keeping the checklist and
			// the session PLAN.md accurate (and resumable after compaction).
			updatePlanTool, upErr := tools.NewUpdatePlanTool()
			if upErr == nil {
				coreTools = append(coreTools, updatePlanTool)
			}
			if acTool, acErr := tools.NewAnnounceCompletionTool(); acErr == nil {
				coreTools = append(coreTools, acTool)
			}

			// Graph-Optimized Plan mode transition tools (code mode only).
			// They advance the per-session phase state machine that drives the
			// staged runtime tool gate. Always registered; only meaningful when
			// the turn is in Graph-Optimized Plan mode.
			if gpReads, e := tools.NewGraphPlanReadsTool(); e == nil {
				coreTools = append(coreTools, gpReads)
			}
			if gpGaps, e := tools.NewGraphPlanGapsTool(); e == nil {
				coreTools = append(coreTools, gpGaps)
			}
			if gpFinalize, e := tools.NewGraphPlanFinalizeTool(); e == nil {
				coreTools = append(coreTools, gpFinalize)
			}

		}
	}

	return &ChatCatalogResources{CoreTools: coreTools, CredentialTools: credToolsSlice,
		SchedulerTools: schedToolsSlice, SlideTools: slideToolsSlice, DistillTools: distillToolsSlice,
		SubAgentManager: subAgentMgr}, nil
}

// BuildChatCatalog registers the same executable Chat tools for ordinary and
// scoped callers. It never initializes providers, vaults, host skills, MCP
// discovery, browsers, pools or global callbacks. Bind inputs on each invocation
// as well as construction; Bot authorization remains the caller's outer boundary.
func BuildChatCatalog(ctx context.Context, inputs *chatruntime.Inputs, cfg *ChatFactoryConfig) (*ChatCatalog, error) {
	if cfg == nil || cfg.AppConfig == nil {
		return nil, fmt.Errorf("chat catalog configuration required")
	}
	var err error
	if inputs != nil {
		ctx, err = inputs.Bind(ctx)
		if err != nil {
			return nil, err
		}
	}
	r := cfg.CatalogResources
	if r == nil {
		r = &ChatCatalogResources{}
	}
	if inputs != nil && r.Prepared {
		return nil, fmt.Errorf("scoped catalog cannot accept host-prepared resources")
	}
	if !r.Prepared {
		if inputs == nil || r.Pool == nil {
			return nil, fmt.Errorf("scoped catalog requires inputs and sandbox pool")
		}
		// Do not initialize the factory-only Perplexity provider in scoped mode.
		local := *cfg
		local.CodeMode = false
		base, e := buildChatBase(&local)
		if e != nil {
			return nil, e
		}
		base.CoreTools = sandbox.WrapToolsWithPool(base.CoreTools, r.Pool)
		base.CredentialTools = sandbox.WrapToolsWithPool(base.CredentialTools, r.Pool)
		base.SchedulerTools = sandbox.WrapToolsWithPool(base.SchedulerTools, r.Pool)
		base.DistillTools = sandbox.WrapToolsWithPool(base.DistillTools, r.Pool)
		base.BrowserTools, base.EmailTools, base.SkillTools = r.BrowserTools, r.EmailTools, r.SkillTools
		base.Groups = r.Groups
		base.Backend, base.BrowserManager, base.TemplateRegistry, base.SessionRegistry, base.LLM, base.Pool = r.Backend, r.BrowserManager, r.TemplateRegistry, r.SessionRegistry, r.LLM, r.Pool
		if len(base.SkillTools) == 0 && cfg.AppConfig.Skills.IsSkillsEnabled() {
			lookup, e := tools.NewSkillLookupTool(r.FilesystemSkills, skillLookupMode(true, false))
			if e != nil {
				return nil, e
			}
			base.SkillTools = []tool.Tool{lookup}
		}
		r = base
	}
	coreTools, credToolsSlice, schedToolsSlice := r.CoreTools, r.CredentialTools, r.SchedulerTools
	slideToolsSlice, distillToolsSlice := r.SlideTools, r.DistillTools
	browserToolsSlice, emailToolsSlice, skillToolSlice := r.BrowserTools, r.EmailTools, r.SkillTools
	// --- 4b. Build tool groups for sub-agent delegation ---
	// Tools are organized into named groups. The main thread gets only essential
	// tools (file ops, shell, search, memory, delegate). All other tools are
	// available to sub-agents via named groups in the delegate_tasks tool.

	// Split coreTools into main-thread essentials and the full "core" group
	// for sub-agents. See mainThreadToolAllowlist for the membership.
	mainThreadToolNames := mainThreadToolAllowlist(cfg.CodeMode)

	var mainThreadTools []tool.Tool
	for _, t := range coreTools {
		if mainThreadToolNames[t.Name()] {
			mainThreadTools = append(mainThreadTools, t)
		}
	}
	// Also pull resolve_credential from credToolsSlice into the main thread.
	// It stays in the credentials group too (for sub-agents); the ToolIndex
	// deduplicates and gives main-thread precedence.
	// In code mode credToolsSlice is always nil (credentials suppressed), so
	// this loop is a no-op there — guard it explicitly for clarity.
	if !cfg.CodeMode {
		for _, t := range credToolsSlice {
			if mainThreadToolNames[t.Name()] {
				mainThreadTools = append(mainThreadTools, t)
			}
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
	botTools, botToolsErr := tools.GetBotToolsForMode(cfg.CodeMode)
	if botToolsErr != nil {
		return nil, fmt.Errorf("create Bot tools: %w", botToolsErr)
	}
	if len(botTools) > 0 {
		toolGroups[tools.BotToolGroup] = &agent.ToolGroup{Name: tools.BotToolGroup, Description: "Inspect Bots and request controlled lifecycle changes or manual runs", Tools: tools.BindScopedBotControls(botTools)}
	}
	if len(schedToolsSlice) > 0 {
		toolGroups["scheduler"] = &agent.ToolGroup{
			Name:        "scheduler",
			Description: "Schedule and manage recurring jobs",
			Tools:       schedToolsSlice,
		}
	}
	if len(slideToolsSlice) > 0 {
		toolGroups["slides"] = &agent.ToolGroup{
			Name:        "slides",
			Description: "Create, read, write, list, and validate Astonish slide decks",
			Tools:       slideToolsSlice,
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

	if inputs != nil {
		if err := addChatExtendedGroups(cfg, toolGroups, r.Pool, nil, r.Backend, r.TemplateRegistry, r.SessionRegistry, r.BrowserManager, r.LLM); err != nil {
			return nil, err
		}
	}
	for name, g := range r.Groups {
		toolGroups[name] = g
	}
	if inputs != nil {
		for name, g := range inputs.RequestToolGroups() {
			toolGroups[name] = g
		}
	}
	disabled := map[string]bool{}
	for _, name := range store.DisabledToolsFromContext(ctx) {
		disabled[name] = true
	}
	filter := func(ts []tool.Tool) []tool.Tool {
		out := make([]tool.Tool, 0, len(ts))
		for _, t := range ts {
			if t != nil && !disabled[t.Name()] {
				out = append(out, t)
			}
		}
		return out
	}
	mainThreadTools = filter(mainThreadTools)
	for name, g := range toolGroups {
		if g == nil {
			delete(toolGroups, name)
			continue
		}
		copy := *g
		copy.Tools = filter(g.Tools)
		if len(disabled) > 0 {
			copy.Toolsets = make([]tool.Toolset, 0, len(g.Toolsets))
			for _, ts := range g.Toolsets {
				copy.Toolsets = append(copy.Toolsets, &chatCatalogFilteredToolset{Toolset: ts, disabled: disabled})
			}
		}
		toolGroups[name] = &copy
	}
	return &ChatCatalog{MainThreadTools: mainThreadTools, Groups: toolGroups,
		AllTools: directCodeTools(mainThreadTools, agent.SortedGroups(toolGroups)), SubAgentManager: r.SubAgentManager}, nil
}

type chatCatalogFilteredToolset struct {
	tool.Toolset
	disabled map[string]bool
}

func (s *chatCatalogFilteredToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	ts, err := s.Toolset.Tools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, 0, len(ts))
	for _, t := range ts {
		if !s.disabled[t.Name()] {
			out = append(out, t)
		}
	}
	return out, nil
}

// addChatExtendedGroups is shared registration, not host resource initialization.
func addChatExtendedGroups(cfg *ChatFactoryConfig, toolGroups map[string]*agent.ToolGroup,
	backendSandboxPool sandbox.ToolNodePool, sandboxNodePool *sandbox.NodeClientPool,
	backendSandbox sandbox.Backend, sandboxTplRegistry *sandbox.TemplateRegistry,
	backendSessRegistry *sandbox.SessionRegistry, browserMgr *browser.Manager, llm model.LLM) error {
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
	if backendSandbox != nil && backendSandboxPool != nil && sandboxTplRegistry != nil {
		var sandboxTplTools []tool.Tool
		tplTool, tplErr := tools.NewSaveSandboxTemplateToolFromBackend(backendSandbox, sandboxTplRegistry, backendSessRegistry)
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

		useTplTool, useErr := tools.NewUseSandboxTemplateToolFromBackend(backendSandboxPool, backendSandbox, sandboxTplRegistry)
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

	var runDrillTool tool.Tool
	var runDrillErr error
	if backendSandboxPool != nil {
		runDrillTool, runDrillErr = tools.NewRunDrillToolWithPool(backendSandboxPool, sandboxTplRegistry, browserMgr, adrill.NewLLMProviderFromModel(llm))
	} else {
		runDrillTool, runDrillErr = tools.NewRunDrillTool(sandboxNodePool, sandboxTplRegistry, browserMgr, adrill.NewLLMProviderFromModel(llm))
	}
	if runDrillErr != nil {
		if cfg.DebugMode {
			slog.Warn("failed to create run_drill tool", "error", runDrillErr)
		}
	} else {
		drillToolsSlice = append(drillToolsSlice, runDrillTool)
	}

	var injectCredsTool tool.Tool
	var injectCredsErr error
	if backendSandboxPool != nil {
		injectCredsTool, injectCredsErr = tools.NewInjectDrillCredentialsToolWithPool(backendSandboxPool, sandboxTplRegistry, browserMgr)
	} else {
		injectCredsTool, injectCredsErr = tools.NewInjectDrillCredentialsTool(sandboxNodePool, sandboxTplRegistry, browserMgr)
	}
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

	// Flows are deferred catalog tools in Studio; Code mode has no flow registry.
	if !cfg.CodeMode {
		var flowTools []tool.Tool
		if searchFlowsTool, sfErr := tools.NewSearchFlowsTool(); sfErr == nil {
			flowTools = append(flowTools, searchFlowsTool)
		} else if cfg.DebugMode {
			slog.Warn("failed to create search_flows tool", "error", sfErr)
		}
		if runFlowTool, rfErr := tools.NewRunFlowTool(); rfErr == nil {
			flowTools = append(flowTools, runFlowTool)
		} else if cfg.DebugMode {
			slog.Warn("failed to create run_flow tool", "error", rfErr)
		}
		if len(flowTools) > 0 {
			toolGroups["flows"] = &agent.ToolGroup{Name: "flows", Description: "Search and run reusable flows", Tools: flowTools}
		}
	}

	return nil
}
