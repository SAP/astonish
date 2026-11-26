package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// ChatRuntimeDependencies is the post-bootstrap composition boundary. It never
// opens a host store, discovers a provider, starts compute, or owns an injected
// pool. Cleanup transfers only the explicitly supplied cleanup ownership.
type ChatRuntimeDependencies struct {
	// PlanInitializer hands transport-owned persistence the guarded verifier.
	// Existing context bindings, including nil, may never be replaced.
	PlanInitializer api.StudioChatPlanInitializer
	Inputs          *chatruntime.Inputs
	Config          *ChatFactoryConfig
	LLM             model.LLM
	Sessions        session.Service
	// SessionID pins non-tool callbacks (distillation dry-runs) to the request's
	// existing sandbox identity. It is not a host directory or a new sandbox.
	SessionID string
	Resources *ChatCatalogResources
	// Catalog is accepted only from the ordinary bootstrap, which already built
	// its host catalog. Scoped callers always build from Resources and Inputs.
	Catalog       *ChatCatalog
	Prompt        *agent.SystemPromptBuilder
	Toolsets      []tool.Toolset
	Redactor      *credentials.Redactor
	Credentials   credentials.CredentialResolver
	ContextWindow int
	FlowRegistry  *agent.FlowRegistry
	FlowRunner    agent.FlowRunnerFunc
	// FlowRunnerAccess and DrillExecution are explicit run_flow/run_drill
	// authority, separate from the distillation callback above. Adapters must
	// use the invocation's guarded request catalog and budget-bound model;
	// do not supply the ambient InteractiveFlowRunner or a host executor.
	// Missing dependencies disable these capabilities in a scoped runtime.
	// UseScopedEngines explicitly enables request-owned flow/drill engines using
	// this runtime's guarded catalog and supplied model. No provider discovery.
	UseScopedEngines bool
	flowContinuation *scopedChatFlowContinuation
	FlowRunnerAccess tools.FlowRunnerAccess
	DrillExecution   *tools.ScopedDrillExecution
	// DrillBudget is the same shared run budget used by tool/model dispatch.
	// Bot composition supplies it explicitly; absent budgets fail closed.
	DrillBudget        execution.RunBudget
	KnowledgeRetrieval agent.KnowledgeRetrievalFunc
	Reflector          *agent.PlatformReflector
	MCPGroupResolver   func(context.Context, string) *agent.ToolGroup
	OnChildSession     func(string, string)
	FleetTools         []tool.Tool
	Cleanup            func()
}

// ChatContextBinding verifies the captured identity on every tool/declaration
// invocation. The runtime intersects its result's lifetime with the caller's, so
// hooks may narrow cancellation/deadlines but cannot detach an invocation.
type ChatContextBinding interface {
	Bind(context.Context) (context.Context, error)
}
type ChatToolAuthorization interface {
	Authorize(context.Context, string, any) error
}
type ChatToolAccounting interface {
	Observe(context.Context, string, any, map[string]any, error)
}
type ChatModelPreparation interface {
	Begin(context.Context) context.Context
	End()
}
type ChatPromptAddition interface {
	AddPrompt(context.Context, *agent.SystemPromptBuilder) error
}

// Hooks augment Chat; they cannot replace ordinary executable tools by name.
// Authorization executes outside the identical executable shared by all views.
// ChatToolDispatch brackets the same executable with request-owned fencing and
// settlement. next retains Chat's scoped plan/flow/drill dispatch.
type ChatToolDispatch interface {
	Dispatch(tool.Context, tool.Tool, any, func(tool.Context, any) (map[string]any, error)) (map[string]any, error)
}

type ChatRuntimeHooks struct {
	Dispatch         ChatToolDispatch
	Context          ChatContextBinding
	Authorization    ChatToolAuthorization
	Accounting       ChatToolAccounting
	ModelPreparation ChatModelPreparation
	Prompt           ChatPromptAddition
	AdditionalTools  []tool.Tool
	// ScopedTools supplies domain-specific memory, scheduling and interaction
	// handlers only. Ordinary execution tools cannot be replaced.
	ScopedTools []tool.Tool
	// ExcludedTools narrows a domain view without installing unavailable stubs.
	ExcludedTools []string
}

// newStudioChatRuntimeFactory captures only borrowed execution infrastructure.
// Every invocation creates a new agent, catalog, delegation manager and model-bound
// callbacks; neither a tenant model nor a mutable agent is installed globally.
func newStudioChatRuntimeFactory(base ChatFactoryConfig, resources ChatCatalogResources, onChild func(string, string)) api.StudioChatRuntimeFactory {
	return func(ctx context.Context, req api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if req.Inputs == nil || req.AppConfig == nil || req.LLM == nil || req.SessionService == nil || req.SessionID == "" || req.UserID == "" {
			return nil, fmt.Errorf("resolved scoped Chat request is required")
		}
		bound, err := req.Inputs.Bind(ctx)
		if err != nil {
			return nil, err
		}
		principal, ok := execution.PrincipalFromContext(bound)
		if !ok || principal.Subject != req.UserID {
			return nil, fmt.Errorf("scoped Chat owner mismatch")
		}
		cfg := base
		cfg.AppConfig, cfg.CodeMode, cfg.PlatformMode = req.AppConfig, false, true
		cfg.DebugMode, cfg.AutoApprove = req.Debug, req.AutoApprove
		cfg.ProviderName, cfg.ModelName = req.ProviderName, req.ModelName
		cfg.SessionService = req.SessionService
		if cfg.WorkspaceDir == "" {
			cfg.WorkspaceDir = "/root"
			if sandbox.BackendKind(cfg.AppConfig.Sandbox.BackendKind()) == sandbox.BackendKindOpenShell {
				cfg.WorkspaceDir = "/sandbox"
			}
		}
		r := resources
		r.Prepared, r.LLM = false, req.LLM
		r.Groups = nil // request MCP/A2A groups come exclusively from Inputs
		// Studio Chat is owner-personal (matching the API's ResourceScopePersonal
		// admission), not the daemon's provider/channel mailbox. Never carry a
		// bootstrap email client or tools into a tenant request.
		r.EmailTools = nil
		if r.Backend != nil && r.Pool != nil {
			r.EmailTools, err = ScopedChatEmailTools(ScopedChatEmailOptions{
				Inputs: req.Inputs, Scope: chatruntime.ResourceScopePersonal,
				Backend: r.Backend, Client: r.Pool.GetOrCreate(req.SessionID), SessionID: req.SessionID,
			})
			if err != nil {
				return nil, err
			}
		}
		d := ChatRuntimeDependencies{
			PlanInitializer: req.PlanInitializer,
			Inputs:          req.Inputs, Config: &cfg, Resources: &r, LLM: req.LLM, Sessions: req.SessionService,
			SessionID:          req.SessionID,
			UseScopedEngines:   true,
			Redactor:           credentials.RedactorFromContext(bound),
			KnowledgeRetrieval: prepareKnowledgeRetrieval,
			Reflector:          &agent.PlatformReflector{LLM: req.LLM, DebugMode: req.Debug},
			OnChildSession:     onChild,
		}
		if cs := store.CredentialStoreFromContext(bound); cs != nil {
			d.Credentials = credentials.NewStoreAdapter(cs)
		}
		if cfg.AppConfig.Sessions.Compaction.IsCompactionEnabled() {
			d.ContextWindow = provider.ResolveContextWindowCached(bound, cfg.ProviderName, cfg.ModelName, cfg.AppConfig)
		}
		appName := req.AppName
		if appName == "" {
			appName = "astonish"
		}
		continuation, publish, err := api.GetChatManager().BeginContinuation(bound, appName, req.SessionID)
		if err != nil {
			return nil, err
		}
		flows, _ := continuation.(*scopedChatFlowContinuation)
		if flows == nil {
			flows = &scopedChatFlowContinuation{}
		}
		d.flowContinuation = flows
		runtime, err := NewScopedChatRuntime(bound, d, ChatRuntimeHooks{})
		if err != nil {
			publish(flows)
			return nil, err
		}
		var once sync.Once
		return &api.StudioChatRuntime{ChatAgent: runtime.ChatAgent, Cleanup: func() {
			once.Do(func() {
				runtime.Cleanup()
				if len(flows.sessions) == 0 {
					publish(nil)
				} else {
					publish(flows)
				}
			})
		}}, nil
	}
}

// prepareScopedChatBrowser owns only the request's CDP connection and handoff.
// The supplied backend/pool remain borrowed; construction never starts compute.
func prepareScopedChatBrowser(ctx context.Context, cfg *ChatFactoryConfig, sessionID string, r *ChatCatalogResources, inputs *chatruntime.Inputs) (func(), error) {
	if sessionID == "" || r.Pool == nil || r.Backend == nil || inputs == nil {
		return nil, fmt.Errorf("scoped browser requires session, backend and pool")
	}
	principal, ok := execution.PrincipalFromContext(ctx)
	if !ok || principal.Subject == "" {
		return nil, fmt.Errorf("scoped browser requires authenticated owner")
	}
	mgr := browser.NewManager(browserConfigFromApp(cfg.AppConfig))
	mgr.SandboxEnabled, mgr.ScopedHandoff = true, true
	mgr.SetSessionID(sessionID)
	if !sandbox.WireBackendBrowserManager(mgr, r.Backend, r.SessionRegistry, r.Pool, nil) {
		// OpenShell transport is explicitly provided by bootstrap. Copy only
		// transport callbacks, never its mutable manager, artifacts or handoff.
		if r.Backend.Kind() != sandbox.BackendKindOpenShell || r.BrowserManager == nil || !r.BrowserManager.SandboxEnabled {
			return nil, fmt.Errorf("scoped browser transport unavailable")
		}
		source := r.BrowserManager
		mgr.ContainerEnsureReadyFunc = source.ContainerEnsureReadyFunc
		mgr.ContainerResolveFunc = source.ContainerResolveFunc
		mgr.ContainerStartBrowserFunc = source.ContainerStartBrowserFunc
		mgr.ContainerDialFunc = source.ContainerDialFunc
		mgr.ContainerStartRecordingFunc = source.ContainerStartRecordingFunc
	}
	if mgr.ContainerResolveFunc == nil || mgr.ContainerStartBrowserFunc == nil || mgr.ContainerDialFunc == nil {
		return nil, fmt.Errorf("scoped browser transport unavailable")
	}
	if r.SessionRegistry != nil {
		mgr.ActivityTouchFunc = r.SessionRegistry.TouchActivity
	}
	ctx, cancelLifetime := context.WithCancel(ctx)
	ensureReady := mgr.ContainerEnsureReadyFunc
	mgr.ContainerEnsureReadyFunc = func(call context.Context, id string) error {
		if id != sessionID {
			return fmt.Errorf("browser session mismatch")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		bound, err := inputs.Bind(call)
		if err != nil {
			return err
		}
		if ensureReady == nil {
			return fmt.Errorf("scoped browser readiness unavailable")
		}
		bound, release := intersectChatRuntimeContext(ctx, bound)
		defer release()
		return ensureReady(bound, id)
	}
	// Invocation context must carry the current principal. Bind rechecks tenant
	// identity and restores the exact scoped stores, without a host fallback.
	bind := func(call context.Context, id string) (context.Context, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := call.Err(); err != nil {
			return nil, err
		}
		if id != sessionID {
			return nil, fmt.Errorf("browser artifact session mismatch")
		}
		if _, ok := execution.PrincipalFromContext(call); !ok {
			return nil, fmt.Errorf("browser artifact principal required")
		}
		return inputs.Bind(call)
	}
	const artifactLimit = 64 << 20
	mgr.WriteArtifactFunc = func(call context.Context, id, filename string, data []byte) error {
		bound, err := bind(call, id)
		if err != nil {
			return err
		}
		if path.Dir(filename) != "/workspace" || path.Clean(filename) != filename || strings.ContainsAny(filename, "\\\x00") || len(data) == 0 || len(data) > artifactLimit {
			return fmt.Errorf("invalid browser artifact")
		}
		bound, release := intersectChatRuntimeContext(ctx, bound)
		defer release()
		bounded, cancel := context.WithTimeout(bound, 30*time.Second)
		defer cancel()
		return r.Backend.PushFile(bounded, sessionID, filename, bytes.NewReader(data), 0600)
	}
	mgr.StatArtifactFunc = func(call context.Context, id, filename string) (int64, error) {
		bound, err := bind(call, id)
		if err != nil {
			return 0, err
		}
		if path.Dir(filename) != "/tmp/astonish-recordings" || path.Clean(filename) != filename || strings.ContainsAny(filename, "\\\x00") {
			return 0, fmt.Errorf("invalid browser recording path")
		}
		bound, release := intersectChatRuntimeContext(ctx, bound)
		defer release()
		bounded, cancel := context.WithTimeout(bound, 30*time.Second)
		defer cancel()
		// Fixed script, positional path, no content or credentials in argv.
		result, err := r.Backend.Exec(bounded, sessionID, sandbox.ExecSpec{Command: []string{"sh", "-c", `test -f "$1" && test ! -L "$1" && stat -c %s -- "$1"`, "browser-artifact-stat", filename}})
		if err != nil {
			return 0, err
		}
		if result == nil || result.ExitCode != 0 || len(result.Stdout) > 32 {
			return 0, fmt.Errorf("browser recording stat failed")
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(result.Stdout)), 10, 64)
		if err != nil || size <= 0 {
			return 0, fmt.Errorf("invalid browser recording size")
		}
		return size, nil
	}
	register, release, err := api.BindScopedBrowserHandoff(ctx, sessionID, mgr)
	if err != nil {
		cancelLifetime()
		return nil, err
	}
	mgr.RegisterHandoffFunc = register
	browserTools, err := tools.GetBrowserToolsForSandbox(mgr)
	if err != nil {
		cancelLifetime()
		release()
		mgr.Cleanup()
		return nil, err
	}
	r.BrowserManager, r.BrowserTools = mgr, browserTools
	var once sync.Once
	return func() { once.Do(func() { cancelLifetime(); release(); mgr.Cleanup() }) }, nil
}

func NewScopedChatRuntime(ctx context.Context, d ChatRuntimeDependencies, hooks ChatRuntimeHooks) (_ *ChatFactoryResult, err error) {
	var once sync.Once
	var ownedFlow *scopedChatFlowRunner
	var browserCleanup func()
	var delegates *scopedChatDelegates
	cleanup := func() {
		once.Do(func() {
			if delegates != nil {
				delegates.close()
			}
			if browserCleanup != nil {
				browserCleanup()
			}
			if ownedFlow != nil {
				if d.flowContinuation != nil {
					ownedFlow.detach(d.flowContinuation)
				}
				ownedFlow.close()
			}
			if d.Cleanup != nil {
				d.Cleanup()
			}
		})
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	if d.Config == nil || d.Config.AppConfig == nil || d.LLM == nil || d.Sessions == nil {
		return nil, fmt.Errorf("chat runtime requires resolved configuration, model and session service")
	}
	cfg := *d.Config
	if d.Inputs != nil {
		ctx, err = d.Inputs.Bind(ctx)
		if err != nil {
			return nil, err
		}
		if cfg.CodeMode || d.Catalog != nil {
			return nil, fmt.Errorf("scoped runtime cannot accept a host catalog or Code mode")
		}
		// All consumers below (main model, delegates, flow/drill and distillation)
		// share the same per-generation live authority boundary.
		d.LLM = d.Inputs.GuardModel(d.LLM)
	}
	if hooks.Context != nil {
		ctx, err = hooks.Context.Bind(ctx)
		if err != nil {
			return nil, err
		}
	}
	if d.Inputs != nil && d.Resources != nil && d.Resources.Backend == nil && (d.Resources.BrowserManager != nil || len(d.Resources.BrowserTools) != 0) {
		return nil, fmt.Errorf("scoped browser backend unavailable")
	}
	if d.Inputs != nil && d.Resources != nil && d.Resources.Backend != nil {
		r := *d.Resources
		d.Resources = &r
		browserCleanup, err = prepareScopedChatBrowser(ctx, &cfg, d.SessionID, &r, d.Inputs)
		if err != nil {
			return nil, err
		}
		delegates = &scopedChatDelegates{pool: r.Pool, browser: r.BrowserManager}
		previousChild := d.OnChildSession
		d.OnChildSession = func(parent, child string) {
			delegates.alias(parent, child, previousChild)
		}
	}
	catalog := d.Catalog
	if catalog == nil {
		cfg.CatalogResources = d.Resources
		catalog, err = BuildChatCatalog(ctx, d.Inputs, &cfg)
		if err != nil {
			return nil, err
		}
	}
	// Clone containers, retain executable identity. Neither filtering nor adding
	// capabilities is allowed to mutate a caller's catalog or another runtime.
	groups := make(map[string]*agent.ToolGroup, len(catalog.Groups)+1)
	for name, g := range catalog.Groups {
		if g != nil {
			c := *g
			c.Tools = append([]tool.Tool(nil), g.Tools...)
			c.Toolsets = append([]tool.Toolset(nil), g.Toolsets...)
			groups[name] = &c
		}
	}
	main := append([]tool.Tool(nil), catalog.MainThreadTools...)
	scoped := map[string]tool.Tool{}
	for _, t := range hooks.ScopedTools {
		if t == nil {
			return nil, fmt.Errorf("nil scoped handler")
		}
		switch t.Name() {
		case "memory_save", "memory_search", "memory_get", "memory_delete", "schedule_job", "list_scheduled_jobs", "update_scheduled_job", "remove_scheduled_job", "ask_user":
		default:
			return nil, fmt.Errorf("ordinary tool %s cannot be replaced", t.Name())
		}
		if scoped[t.Name()] != nil {
			return nil, fmt.Errorf("duplicate scoped handler %s", t.Name())
		}
		scoped[t.Name()] = t
	}
	excluded := map[string]bool{}
	for _, name := range hooks.ExcludedTools {
		excluded[name] = true
	}
	used := map[string]bool{}
	applyScoped := func(ts []tool.Tool) []tool.Tool {
		out := make([]tool.Tool, 0, len(ts))
		for _, t := range ts {
			if excluded[t.Name()] {
				continue
			}
			if replacement := scoped[t.Name()]; replacement != nil {
				t = replacement
				used[t.Name()] = true
			}
			out = append(out, t)
		}
		return out
	}
	main = applyScoped(main)
	for _, g := range groups {
		g.Tools = applyScoped(g.Tools)
	}
	for _, t := range hooks.ScopedTools {
		if !used[t.Name()] && !excluded[t.Name()] {
			main = append(main, t)
		}
	}
	all := directCodeTools(main, agent.SortedGroups(groups))
	names := make(map[string]bool, len(all))
	for _, t := range all {
		names[t.Name()] = true
	}
	for _, t := range hooks.AdditionalTools {
		if t == nil || names[t.Name()] {
			return nil, fmt.Errorf("additional runtime tool conflicts with ordinary catalog")
		}
		names[t.Name()] = true
	}
	if len(hooks.AdditionalTools) > 0 {
		groups["runtime_extensions"] = &agent.ToolGroup{Name: "runtime_extensions", Description: "Additional scoped capabilities", Tools: append([]tool.Tool(nil), hooks.AdditionalTools...)}
	}
	binding := &chatRuntimeBinding{inputs: d.Inputs, hooks: hooks, flowRunner: d.FlowRunnerAccess, delegates: delegates}
	if d.DrillExecution != nil {
		// Capture configuration, not the caller's mutable dependency container.
		execution := *d.DrillExecution
		binding.drillExecution = &execution
	}
	directToolsets := append([]tool.Toolset(nil), d.Toolsets...)
	if d.UseScopedEngines || d.Inputs != nil || hooks.Context != nil || hooks.Dispatch != nil || hooks.Authorization != nil || hooks.Accounting != nil || len(hooks.AdditionalTools) > 0 {
		// Validate every concrete executable, not a name-deduplicated view.
		// A shared registry preserves identity across eager and lazy consumers.
		for i, t := range main {
			main[i], err = binding.wrap(t, nil)
			if err != nil {
				return nil, err
			}
		}
		for _, g := range agent.SortedGroups(groups) {
			for i, t := range g.Tools {
				g.Tools[i], err = binding.wrap(t, nil)
				if err != nil {
					return nil, err
				}
			}
			for i, ts := range g.Toolsets {
				g.Toolsets[i], err = binding.wrapToolset(ts)
				if err != nil {
					return nil, err
				}
			}
		}
		for i, ts := range directToolsets {
			directToolsets[i], err = binding.wrapToolset(ts)
			if err != nil {
				return nil, err
			}
		}
	}
	binding.groups = groups
	sorted := agent.SortedGroups(groups)
	all = directCodeTools(main, sorted)
	var toolsets []tool.Toolset
	for _, g := range sorted {
		toolsets = append(toolsets, g.Toolsets...)
	}
	prompt := &agent.SystemPromptBuilder{WorkspaceDir: cfg.WorkspaceDir}
	if d.Prompt != nil {
		copy := *d.Prompt
		prompt = &copy
	}
	prompt.Tools, prompt.Toolsets, prompt.Catalog = all, toolsets, sorted
	if d.Prompt == nil {
		prompt.CustomPrompt = cfg.AppConfig.Chat.SystemPrompt
		prompt.Timezone = cfg.AppConfig.General.Timezone
		if cfg.CodeMode {
			code := agent.NewCodeSystemPromptBuilder(prompt)
			code.PlanFilePersistence = true
			code.EnforceAuthorization = !cfg.AutoApprove
		}
		if d.Resources != nil && d.Resources.Pool != nil {
			prompt.SandboxEnabled = true
			prompt.SandboxWorkspaceDir = cfg.WorkspaceDir
		}
	}
	if hooks.Prompt != nil {
		if err := hooks.Prompt.AddPrompt(ctx, prompt); err != nil {
			return nil, err
		}
	}
	index, err := newChatToolIndex(ctx, main, sorted, cfg.PlatformToolVectorStore, cfg.PlatformEmbedFunc)
	if err != nil {
		return nil, err
	}
	providerTools, err := providerToolsForMode(cfg.CodeMode, index, main, sorted)
	if err != nil {
		return nil, err
	}
	redactor := d.Redactor
	if redactor == nil {
		redactor = credentials.NewRedactor()
	}
	if d.UseScopedEngines {
		if d.FlowRunnerAccess != nil || d.DrillExecution != nil || d.FlowRunner != nil {
			return nil, fmt.Errorf("scoped engines cannot replace explicit execution adapters")
		}
		ownedFlow = newScopedChatFlowRunner(d.LLM, all, append(toolsets, directToolsets...), redactor)
		if d.flowContinuation != nil {
			for key, sess := range d.flowContinuation.sessions {
				ownedFlow.engine.sessions.Store(key, sess)
			}
			d.flowContinuation.sessions = nil
		}
		binding.flowRunner = ownedFlow
		injector := newChatDrillInjector(ctx, binding, d.Resources, d.SessionID, d.DrillBudget, redactor)
		binding.drillExecution = &tools.ScopedDrillExecution{
			InjectCredentials: injector,
			LLM:               adrill.NewLLMProviderFromModel(d.LLM),
			SaveReport: func(invocation tool.Context, report *adrill.SuiteReport) error {
				bound, release, err := binding.bind(invocation)
				defer release()
				if err != nil {
					return err
				}
				svc := store.FromContext(bound)
				if svc == nil || svc.DrillReports == nil {
					return nil // optional persistence, never host disk
				}
				encoded, err := json.Marshal(report)
				if err != nil {
					return err
				}
				return svc.DrillReports.SaveReport(bound, &store.DrillReport{
					Suite: report.Suite, Status: report.Status, Summary: redactor.Redact(report.Summary),
					DurationMs: report.Duration, ReportData: []byte(redactor.Redact(string(encoded))),
					StartedAt: report.StartedAt, FinishedAt: report.FinishedAt, CreatedBy: store.UserIDFromContext(bound),
				})
			},
			NewExecutor: func(invocation tool.Context) (adrill.ToolExecutor, error) {
				ts, err := ownedFlow.childTools(invocation)
				if err != nil {
					return nil, err
				}
				return &scopedChatDrillExecutor{invocation: invocation, tools: ts}, nil
			},
		}
	}
	chat := newSuppliedChatAgent(suppliedChatDependencies{LLM: d.LLM, Sessions: d.Sessions, Tools: providerTools, Toolsets: directToolsets, Prompt: prompt, Redactor: redactor, Credentials: d.Credentials, Debug: cfg.DebugMode, AutoApprove: cfg.AutoApprove})
	chat.ToolIndex = index
	if d.Inputs != nil || d.UseScopedEngines {
		binding.plan = &scopedChatPlan{chat: chat, sessions: d.Sessions, sessionID: d.SessionID, workspace: cfg.WorkspaceDir, binding: binding}
	}
	if d.PlanInitializer != nil {
		if _, bound := agent.PlanRuntimeFromContext(ctx); bound || binding.plan == nil {
			return nil, fmt.Errorf("cannot initialize an already bound or unscoped plan runtime")
		}
		binding.plan.runtime, err = d.PlanInitializer(binding.plan.verify)
		if err != nil {
			return nil, err
		}
		if binding.plan.runtime == nil {
			return nil, fmt.Errorf("transport plan initializer returned nil runtime")
		}
		binding.plan.transportOwned = true
	}
	chat.PreProviderRetrievalTimeout = cfg.AppConfig.Chat.PreProviderRetrievalTimeout()
	chat.FlowRegistry = d.FlowRegistry
	if ownedFlow != nil {
		chat.FlowRunner = func(ctx context.Context, yaml string, params map[string]string) (*agent.DryRunExecResult, error) {
			bound, release, err := binding.bind(ctx)
			defer release()
			if err != nil {
				return nil, err
			}
			sessionID := d.SessionID
			if invocation, ok := ctx.(tool.Context); ok {
				if sessionID != "" && sessionID != invocation.SessionID() {
					return nil, fmt.Errorf("scoped dry-run session mismatch")
				}
				sessionID = invocation.SessionID()
			}
			if sessionID == "" {
				return nil, fmt.Errorf("scoped dry-run requires request session identity")
			}
			return ownedFlow.dryRun(bound, sessionID, yaml, params)
		}
	}
	if d.FlowRunner != nil {
		chat.FlowRunner = func(ctx context.Context, yaml string, params map[string]string) (*agent.DryRunExecResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Unscoped bootstrap callers receive their live request catalog from
			// the API. Only a bound runtime may replace it with its guarded view.
			if d.Inputs == nil && hooks.Context == nil && hooks.Authorization == nil && hooks.Accounting == nil {
				return d.FlowRunner(ctx, yaml, params)
			}
			bound, release, err := binding.bind(ctx)
			defer release()
			if err != nil {
				return nil, err
			}
			return d.FlowRunner(bound, yaml, params)
		}
	}
	// Distillation sees the same guarded lazy capabilities as direct dispatch
	// and scoped flows, including toolsets supplied outside named groups.
	distillerToolsets := append(append([]tool.Toolset(nil), toolsets...), directToolsets...)
	chat.FlowDistiller = buildChatFlowDistiller(d.LLM, all, distillerToolsets)
	chat.FlowSaveDir = cfg.AppConfig.Chat.FlowSaveDir
	chat.KnowledgeRetrieval, chat.PlatformReflector = d.KnowledgeRetrieval, d.Reflector
	if cfg.CodeMode && !cfg.AutoApprove {
		// Code's grant-aware folder gate remains authoritative; do not install
		// tools.SetScopeRoot, which would reject already-approved outside paths.
		chat.SetEnforceAuthorization(true)
		chat.SetWorkingDir(prompt.WorkspaceDir)
	}
	if hooks.ModelPreparation != nil {
		chat.BeginModelPreparation = hooks.ModelPreparation.Begin
		chat.EndModelPreparation = hooks.ModelPreparation.End
	}
	if cfg.AppConfig.Security.IsSecretScannerEnabled() {
		scanner := credentials.NewSecretScanner()
		sc := cfg.AppConfig.Security.SecretScanner
		if sc.EntropyThreshold > 0 {
			scanner.EntropyThreshold = sc.EntropyThreshold
		}
		if sc.MinTokenLength > 0 {
			scanner.MinTokenLength = sc.MinTokenLength
		}
		chat.PendingSecrets.Scanner = scanner
	}
	if fs, ok := d.Sessions.(*persistentsession.FileStore); ok {
		fs.RedactFunc = redactor.Redact
		chat.RedactSessionFunc = fs.RedactSession
	}
	mgr := catalog.SubAgentManager
	wireChatDelegation(chat, mgr, d.LLM, d.Sessions)
	if mgr != nil {
		mgr.Config.CodeMode = cfg.CodeMode
		mgr.ToolGroups, mgr.ToolIndex = groups, index
		mgr.SearchToolsFactory = tools.NewScopedSearchToolsTool
		mgr.AppName, mgr.UserID = "astonish", store.SystemUserID
		mgr.WorkDir = prompt.WorkspaceDir
		mgr.SkillIndex = prompt.SkillIndex
		if prompt.WebSearchAvailable {
			mgr.WebSearchToolName = strings.ReplaceAll(prompt.WebSearchToolName, "-", "_")
		}
		if prompt.WebExtractAvailable {
			mgr.WebExtractToolName = strings.ReplaceAll(prompt.WebExtractToolName, "-", "_")
		}
		mgr.SandboxEnabled, mgr.SandboxWorkspaceDir = prompt.SandboxEnabled, prompt.SandboxWorkspaceDir
		mgr.FleetTools, mgr.MCPGroupResolver, mgr.OnChildSession = d.FleetTools, d.MCPGroupResolver, d.OnChildSession
		mgr.SubTaskProgress = func(evt agent.SubTaskProgressEvent) { forwardPlanDelegateProgress(chat, evt) }
		for _, t := range all {
			if t.Name() == "skill_lookup" {
				mgr.SkillLookupTool = t
			}
		}
		if chat.EnforceAuthorization {
			mgr.GetAuthPolicy = chat.GetOrCreateAuthPolicy
			mgr.AuthorizationGate = func(req agent.SubAgentAuthRequest) agent.SubAgentAuthResponse {
				if chat.SubAgentAuthGate != nil {
					return chat.SubAgentAuthGate(req)
				}
				return agent.SubAgentAuthResponse{Granted: false, Choice: "Deny"}
			}
		}
	}
	if cfg.AppConfig.Sessions.Compaction.IsCompactionEnabled() && d.ContextWindow > 0 {
		auxiliary := makeLLMFunc(d.LLM)
		if d.Inputs != nil || hooks.Context != nil {
			auxiliary = func(call context.Context, prompt string) (string, error) {
				bound, release, err := binding.bind(call)
				defer release()
				if err != nil {
					return "", err
				}
				return makeLLMFunc(d.LLM)(bound, prompt)
			}
		}
		wireChatCompactor(chat, d.ContextWindow, &cfg.AppConfig.Sessions.Compaction, cfg.DebugMode, cfg.CodeMode, auxiliary)
	}
	indexingDone := make(chan struct{})
	close(indexingDone)
	var pool sandbox.ToolNodePool
	if d.Resources != nil {
		pool = d.Resources.Pool
	}
	result := &ChatFactoryResult{ChatAgent: chat, LLM: d.LLM, SessionService: d.Sessions, PromptBuilder: prompt, InternalTools: all, MCPToolsets: toolsets, Compactor: chat.Compactor, ProviderName: cfg.ProviderName, ModelName: cfg.ModelName, ProviderConfigured: true, MemorySearchAvailable: d.KnowledgeRetrieval != nil, IndexingDone: indexingDone, SandboxPool: pool, Cleanup: cleanup}
	result.SwappableLLM, _ = d.LLM.(*provider.SwappableLLM)
	return result, nil
}

// scopedChatFlowContinuation owns only private ADK state and paused metadata.
// Detach clears the runner (and with it all model/tool/context references).
// This is in-memory conversation continuity, not restart durability.
type scopedChatFlowContinuation struct{ sessions map[string]*flowSession }

func (c *scopedChatFlowContinuation) Discard() {
	for _, sess := range c.sessions {
		for _, cleanup := range sess.cleanupFuncs {
			cleanup()
		}
	}
	c.sessions = nil
}
func (f *scopedChatFlowRunner) detach(c *scopedChatFlowContinuation) {
	f.cancel()
	f.mu.Lock()
	defer f.mu.Unlock()
	c.sessions = make(map[string]*flowSession)
	f.engine.sessions.Range(func(key, value any) bool {
		sess := value.(*flowSession)
		sess.mu.Lock()
		sess.runner = nil
		sess.mu.Unlock()
		c.sessions[key.(string)] = sess
		f.engine.sessions.Delete(key)
		return true
	})
}

// scopedChatFlowRunner uses only the host-free portion of InteractiveFlowRunner.
// Execution authority is attempt-local; paused state can be detached to ChatManager.
type scopedChatFlowRunner struct {
	mu       sync.Mutex
	engine   InteractiveFlowRunner
	llm      model.LLM
	tools    []tool.Tool
	toolsets []tool.Toolset
	redactor *credentials.Redactor
	lifetime context.Context
	cancel   context.CancelFunc
}

func newScopedChatFlowRunner(llm model.LLM, ts []tool.Tool, sets []tool.Toolset, redactor *credentials.Redactor) *scopedChatFlowRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return &scopedChatFlowRunner{llm: llm, tools: ts, toolsets: sets, redactor: redactor, lifetime: ctx, cancel: cancel}
}

// dryRun bridges a plain HTTP context through a real ADK invocation rather than
// fabricating a partial tool.Context. Its private bookkeeping session uses the
// parent ID so lazy tools retain the request's sandbox; it never owns that sandbox.
func (f *scopedChatFlowRunner) dryRun(ctx context.Context, sessionID, yaml string, params map[string]string) (*agent.DryRunExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ss := session.InMemoryService()
	created, err := ss.Create(ctx, &session.CreateRequest{AppName: "astonish", UserID: store.UserIDFromContext(ctx), SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	defer ss.Delete(context.Background(), &session.DeleteRequest{AppName: "astonish", UserID: created.Session.UserID(), SessionID: sessionID})
	key := fmt.Sprintf("__distill_dryrun__%d", time.Now().UnixNano())
	defer f.CleanupSession(key)
	var result *tools.FlowRunResult
	var runErr error
	a, err := adkagent.New(adkagent.Config{Name: "chat_dry_run", Run: func(ic adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			invocation := tool.NewToolContext(ic, key, &session.EventActions{}, nil)
			result, runErr = f.RunFlowFromYAML(invocation, yaml, "distill-dry-run", params, "", key)
			if runErr != nil {
				yield(nil, runErr)
			}
		}
	}})
	if err != nil {
		return nil, err
	}
	r, err := runner.New(runner.Config{AppName: "astonish", Agent: a, SessionService: ss})
	if err != nil {
		return nil, err
	}
	for _, err := range r.Run(ctx, created.Session.UserID(), sessionID, nil, adkagent.RunConfig{}) {
		if err != nil {
			return nil, err
		}
	}
	if runErr != nil {
		return nil, runErr
	}
	if result == nil {
		return nil, fmt.Errorf("scoped dry-run produced no result")
	}
	out := &agent.DryRunExecResult{Success: result.Status == "completed", Output: result.Output, NodesVisited: result.NodesVisited}
	if !out.Success {
		out.Error = result.Message
		if result.Status == "waiting_for_input" {
			out.Error = fmt.Sprintf("flow paused on input node %q (prompt: %s) — parameter value not available", result.InputNode, result.InputPrompt)
		} else if out.Error == "" {
			out.Error = fmt.Sprintf("unexpected flow status: %s", result.Status)
		}
	}
	return out, nil
}

func (f *scopedChatFlowRunner) childTools(ctx tool.Context) ([]tool.Tool, error) {
	all := append([]tool.Tool(nil), f.tools...)
	for _, set := range f.toolsets {
		ts, err := set.Tools(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, ts...)
	}
	var result []tool.Tool
	for _, t := range directCodeTools(all, nil) {
		// Composite recursion cannot acquire a fresh failure latch.
		switch t.Name() {
		case "run_flow", "run_drill", "inject_drill_credentials":
			continue
		}
		if _, ok := t.(*chatRuntimeTool); !ok {
			return nil, fmt.Errorf("nested tool %q is not runtime guarded", t.Name())
		}
		result = append(result, t)
	}
	return result, nil
}

func (f *scopedChatFlowRunner) RunFlow(context.Context, string, map[string]string, string, string) (*tools.FlowRunResult, error) {
	return nil, fmt.Errorf("scoped flow filesystem execution is disabled")
}
func (f *scopedChatFlowRunner) GetPausedNode(key string) string { return f.engine.GetPausedNode(key) }
func (f *scopedChatFlowRunner) GetPausedOptions(key string) []string {
	return f.engine.GetPausedOptions(key)
}
func (f *scopedChatFlowRunner) CleanupSession(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.engine.CleanupSession(key)
}
func (f *scopedChatFlowRunner) close() {
	f.cancel()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.engine.sessions.Range(func(key, _ any) bool { f.engine.CleanupSession(key.(string)); return true })
}

// A flow engine may present tool errors as output and proceed. Preserve the
// actual error and cancel its whole turn before any subsequent nested effect.
type scopedChatFlowTurn struct {
	mu     sync.Mutex
	err    error
	cancel context.CancelFunc
}
type scopedChatFlowTurnKey struct{}

func (g *scopedChatFlowTurn) fail(err error) {
	if err != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.err == nil {
			g.err = err
			g.cancel()
		}
	}
}
func (g *scopedChatFlowTurn) failure() error { g.mu.Lock(); defer g.mu.Unlock(); return g.err }

type scopedChatFlowTool struct {
	chatRuntimeRunnable
	sessionID string
	userID    string
}
type scopedChatFlowToolContext struct {
	tool.Context
	sessionID string
	userID    string
}

func (c scopedChatFlowToolContext) UserID() string    { return c.userID }
func (c scopedChatFlowToolContext) SessionID() string { return c.sessionID }
func (t *scopedChatFlowTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	guard, _ := ctx.Value(scopedChatFlowTurnKey{}).(*scopedChatFlowTurn)
	if guard == nil {
		return nil, fmt.Errorf("scoped flow turn is unavailable")
	}
	if err := guard.failure(); err != nil {
		return nil, err
	}
	out, err := t.chatRuntimeRunnable.Run(scopedChatFlowToolContext{Context: ctx, sessionID: t.sessionID, userID: t.userID}, args)
	guard.fail(err)
	return out, err
}
func (t *scopedChatFlowTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if err := t.chatRuntimeRunnable.ProcessRequest(ctx, req); err != nil {
		return err
	}
	if req != nil && req.Tools != nil {
		if _, ok := req.Tools[t.Name()]; ok {
			req.Tools[t.Name()] = t
		}
	}
	return nil
}

func (f *scopedChatFlowRunner) RunFlowFromYAML(ctx context.Context, yaml, _ string, params map[string]string, response, key string) (result *tools.FlowRunResult, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.lifetime.Err(); err != nil {
		return nil, err
	}
	invocation, ok := ctx.(tool.Context)
	if !ok {
		return nil, fmt.Errorf("scoped flow requires a tool invocation")
	}
	runCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(f.lifetime, cancel)
	defer func() { stop(); cancel() }()
	guard := &scopedChatFlowTurn{cancel: cancel}
	runCtx = context.WithValue(runCtx, scopedChatFlowTurnKey{}, guard)
	defer func() {
		if failure := guard.failure(); failure != nil {
			err = failure
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = f.lifetime.Err()
		}
		if err != nil || result != nil && (result.Status == "completed" || result.Status == "error") {
			f.engine.CleanupSession(key)
		}
	}()
	var sess *flowSession
	if response != "" {
		if value, ok := f.engine.sessions.Load(key); ok {
			sess = value.(*flowSession)
		}
		if sess == nil {
			return nil, fmt.Errorf("paused flow unavailable")
		}
		if sess.userID != store.UserIDFromContext(ctx) {
			return nil, fmt.Errorf("flow owner mismatch")
		}
	} else {
		f.engine.CleanupSession(key)
	}
	var cfg *config.AgentConfig
	if sess != nil {
		cfg = sess.agentConfig
	} else {
		cfg, err = config.LoadAgentFromBytes([]byte(yaml))
		if err != nil {
			return nil, err
		}
	}
	if cfg.Type == "drill" || cfg.Type == "drill_suite" {
		return nil, fmt.Errorf("use run_drill for drills")
	}
	ts, err := f.childTools(invocation)
	if err != nil {
		return nil, err
	}
	for i, t := range ts {
		ts[i] = &scopedChatFlowTool{chatRuntimeRunnable: t.(chatRuntimeRunnable), sessionID: invocation.SessionID(), userID: store.UserIDFromContext(ctx)}
	}
	ss := session.InMemoryService()
	if sess != nil {
		ss = sess.sessionService
	}
	a := agent.NewAstonishAgent(cfg, f.llm, ts)
	a.AutoApprove, a.IsWebMode, a.SessionService, a.Redactor = true, true, ss, f.redactor
	ag, err := adkagent.New(adkagent.Config{Name: "chat_flow", Description: cfg.Description, Run: a.Run})
	if err != nil {
		return nil, err
	}
	r, err := runner.New(runner.Config{AppName: "astonish", Agent: ag, SessionService: ss})
	if err != nil {
		return nil, err
	}
	if sess != nil {
		sess.runner = r
		return f.engine.resumeFlow(runCtx, key, response)
	}
	created, err := ss.Create(runCtx, &session.CreateRequest{AppName: "astonish", UserID: store.UserIDFromContext(ctx)})
	if err != nil {
		return nil, err
	}
	sess = &flowSession{runner: r, sessionService: ss, sessionID: created.Session.ID(), userID: created.Session.UserID(), appName: "astonish", agentConfig: cfg}
	sess.cleanupFuncs = []func(){func() {
		_ = ss.Delete(context.Background(), &session.DeleteRequest{AppName: sess.appName, UserID: sess.userID, SessionID: sess.sessionID})
	}}
	f.engine.sessions.Store(key, sess)
	return f.engine.executeFlowTurn(runCtx, sess, nil, params)
}

// newChatDrillInjector captures only explicitly supplied transport and identity.
// Every secret read/write revalidates the current bound principal. It never asks a
// host singleton or fleet plan for missing resources.
func newChatDrillInjector(owner context.Context, binding *chatRuntimeBinding, resources *ChatCatalogResources, sessionID string, budget execution.RunBudget, redactor *credentials.Redactor) func(tool.Context, *adrill.LoadedSuite) error {
	if budget == nil {
		budget = api.BotChatRunBudgetFromContext(owner)
	}
	principal, hasPrincipal := execution.PrincipalFromContext(owner)
	var backend sandbox.Backend
	var pool sandbox.ToolNodePool
	if resources != nil {
		backend, pool = resources.Backend, resources.Pool
	}
	return func(invocation tool.Context, suite *adrill.LoadedSuite) error {
		// Empty suites must not resolve transport or start sandbox work.
		if suite == nil || suite.Config == nil {
			return nil
		}
		spec, err := adrill.SpecFromSuite(suite.Name, suite.Config.SuiteConfig)
		if err != nil {
			return fmt.Errorf("drill injection: %s", redactor.Redact(err.Error()))
		}
		if spec == nil || !spec.HasWork() {
			return nil
		}
		bound, release, err := binding.bind(invocation)
		defer release()
		if err != nil {
			return err
		}
		ctx := chatRuntimeToolContext{Context: invocation, bound: bound}
		check := func(current context.Context) error {
			if err := current.Err(); err != nil {
				return err
			}
			p, ok := execution.PrincipalFromContext(current)
			if !hasPrincipal || !ok || !reflect.DeepEqual(p, principal) || principal.Validate() != nil || sessionID == "" || invocation.SessionID() != sessionID {
				return fmt.Errorf("%w: scoped drill credential principal or session mismatch", store.ErrBotForbidden)
			}
			fresh, done, err := binding.bind(current)
			defer done()
			if err != nil {
				return err
			}
			p, ok = execution.PrincipalFromContext(fresh)
			if !ok || !reflect.DeepEqual(p, principal) {
				return store.ErrBotForbidden
			}
			if budget != nil {
				return budget.Check(fresh)
			}
			return nil
		}
		authorize := func(current context.Context, action map[string]any) error {
			// Nested injection is a separate effect: outer run_drill authorization
			// does not authorize its payload. Hooks may further restrict Bot policy.
			if auth := binding.hooks.Authorization; auth != nil {
				if err := auth.Authorize(current, "inject_drill_credentials", action); err != nil {
					return err
				}
			}
			if _, botRun := execution.BotRunIdentityFromContext(current); botRun || execution.RunPolicyFromContext(current) != nil {
				if err := agent.AuthorizeBotTool(current, "inject_drill_credentials", action); err != nil {
					return err
				}
				if budget == nil {
					return fmt.Errorf("%w: scoped drill injection budget unavailable", store.ErrBotUnsupported)
				}
				if err := budget.ReserveTool(current, "inject_drill_credentials"); err != nil {
					return err
				}
			}
			if err := check(current); err != nil {
				return err
			}
			if pool != nil {
				client := pool.GetOrCreate(sessionID)
				if client == nil {
					return fmt.Errorf("scoped drill credential sandbox client unavailable")
				}
				if err := client.EnsureReady(sessionID); err != nil {
					return fmt.Errorf("scoped drill credential sandbox readiness: %w", err)
				}
			}
			return nil
		}
		if err := check(ctx); err != nil {
			return err
		}
		target := backend
		if target == nil && pool != nil {
			target = pool.GetBackend()
		}
		return injectScopedDrillCredentials(ctx, suite, redactor, target, sessionID, check, authorize)
	}
}

type scopedChatDrillExecutor struct {
	invocation tool.Context
	tools      []tool.Tool
}

func (e *scopedChatDrillExecutor) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	for _, t := range e.tools {
		if t.Name() == name {
			// This is a fresh child invocation, not the outer run_drill leaf.
			// Preserve owner/policy/budget, but never reuse its one-shot approval.
			child := agent.WithoutBotInvocationHandoff(botDrillStepContext{Context: e.invocation, step: ctx})
			child, err := api.CapturedDrillChildContext(child, name, args)
			if err != nil {
				return nil, err
			}
			return t.(chatRuntimeRunnable).Run(chatRuntimeToolContext{Context: e.invocation, bound: child}, args)
		}
	}
	return nil, fmt.Errorf("scoped drill tool %q is unavailable", name)
}

type chatRuntimeBinding struct {
	delegates      *scopedChatDelegates
	registryMu     sync.Mutex
	executables    map[string]*chatRuntimeTool
	toolsets       []*chatRuntimeToolset
	groups         map[string]*agent.ToolGroup
	inputs         *chatruntime.Inputs
	hooks          ChatRuntimeHooks
	flowRunner     tools.FlowRunnerAccess
	drillExecution *tools.ScopedDrillExecution
	plan           *scopedChatPlan
}

// scopedChatPlan owns only one attempt's execution dependencies. Durable state is
// the existing ADK snapshot, not a host PLAN.md or another process-global registry.
// Calls are serialized because PlanRuntime's verifier is synchronous and carries
// the current tool invocation (including cancellation, policy and accounting).
type scopedChatPlan struct {
	mu                                    sync.Mutex
	chat                                  *agent.ChatAgent
	sessions                              session.Service
	sessionID, userID, appName, workspace string
	binding                               *chatRuntimeBinding
	runtime                               *agent.PlanRuntime
	prepared                              *scopedPreparedPlan
	transportOwned                        bool
	invocation                            tool.Context
}

func (p *scopedChatPlan) run(ctx tool.Context, executable chatRuntimeRunnable, args any) (map[string]any, error) {
	// A transport-installed runtime (even nil) is authoritative. In particular,
	// never revive a disabled plan from callbacks or the agent's legacy plan.
	if runtime, bound := agent.PlanRuntimeFromContext(ctx); bound {
		if !p.transportOwned || runtime == nil || runtime != p.runtime {
			return executable.Run(ctx, args)
		}
	} else if p.transportOwned {
		return nil, fmt.Errorf("transport plan runtime is not bound")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.SessionID() == "" || !path.IsAbs(p.workspace) || (p.sessionID != "" && p.sessionID != ctx.SessionID()) || (p.userID != "" && p.userID != ctx.UserID()) {
		return nil, fmt.Errorf("scoped plan session mismatch")
	}
	p.sessionID, p.userID = ctx.SessionID(), ctx.UserID()
	p.appName = ctx.AppName()
	if p.appName == "" {
		p.appName = "astonish"
	}
	p.invocation = ctx
	defer func() { p.invocation = nil }()
	if p.runtime == nil {
		sess, err := p.sessions.Get(ctx, &session.GetRequest{AppName: p.appName, UserID: p.userID, SessionID: p.sessionID})
		if err != nil {
			return nil, err
		}
		options := agent.PlanRuntimeOptions{Workspace: p.workspace, Verify: p.verify, CommitSnapshot: p.commit}
		if prepared := api.BotPreparedPlanSessionFromContext(ctx); prepared != nil {
			p.prepared = &scopedPreparedPlan{session: prepared}
			options.PrepareOperation = p.prepareOperation
		}
		p.runtime = agent.NewPlanRuntime(nil, options)
		if value, err := sess.Session.State().Get(agent.PlanSnapshotStateKey); err == nil && value != nil {
			raw, err := json.Marshal(value)
			var snapshot agent.PlanSnapshot
			if err != nil || json.Unmarshal(raw, &snapshot) != nil || snapshot.PlanID == "" || len(snapshot.Steps) == 0 {
				p.runtime = nil
				return nil, fmt.Errorf("invalid durable plan snapshot")
			}
			p.runtime = agent.NewPlanRuntimeFromSnapshot(snapshot, options)
		}
		if len(p.runtime.Snapshot().Steps) > 0 {
			approvalRuntime := agent.NewPlanRuntimeFromSnapshot(p.runtime.Snapshot(), agent.PlanRuntimeOptions{CommitSnapshot: func(next agent.PlanSnapshot) error {
				return p.sessions.AppendEvent(ctx, sess.Session, &session.Event{ID: fmt.Sprintf("plan-consent-%s-%d", next.PlanID, next.Sequence), Author: "system", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: next}}})
			}})
			if err := api.ApproveBotDefinitionPlan(ctx, approvalRuntime); err != nil {
				return nil, err
			}
			p.runtime = agent.NewPlanRuntimeFromSnapshot(approvalRuntime.Snapshot(), options)
		}
	}
	bound := agent.WithPlanRuntime(ctx, p.runtime)
	bound = tools.WithPlanCallbacks(bound, tools.PlanCallbacks{Progress: func(e agent.SubTaskProgressEvent) {
		e.SessionID = ctx.SessionID()
		p.chat.EmitSubTaskProgress(ctx.SessionID(), e)
	}})
	out, err := executable.Run(chatRuntimeToolContext{Context: ctx, bound: bound}, args)
	// AutoApprove is an explicit trusted composition option, not plan content.
	if err == nil && !p.transportOwned && executable.Name() == "announce_plan" && out["status"] == "ok" && p.chat.AutoApprove {
		if err = p.runtime.SetApproved(); err != nil {
			return nil, err
		}
		out["snapshot"] = p.runtime.Snapshot()
	}
	if err == nil && executable.Name() == "announce_plan" && out["status"] == "ok" && p.prepared != nil {
		if err = p.prepared.session.CaptureDefinition(ctx, p.runtime.Snapshot()); err != nil {
			return nil, err
		}
	}
	return out, err
}

func (p *scopedChatPlan) call(name string, args map[string]any) (map[string]any, error) {
	p.binding.registryMu.Lock()
	executable := p.binding.executables[name]
	p.binding.registryMu.Unlock()
	if executable == nil || p.invocation == nil {
		return nil, fmt.Errorf("scoped plan %s unavailable", name)
	}
	// Persistence and verification are distinct actions, not replays of the
	// already-consumed announce/update handoff. Keep policy and all live guards.
	bound := agent.WithoutBotInvocationHandoff(p.invocation)
	if p.prepared != nil {
		if _, interactive := execution.BotTurnAuthorizerFromContext(p.invocation); !interactive {
			bound = context.WithValue(bound, preparedPlanLeafKey{}, p)
		}
	}
	invocation := chatRuntimeToolContext{Context: p.invocation, bound: bound}
	return executable.Run(invocation, args)
}
func (p *scopedChatPlan) verify(command string) (int, string, error) {
	if p.prepared != nil {
		if _, interactive := execution.BotTurnAuthorizerFromContext(p.invocation); interactive {
			if err := p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanDispatchStarted, DispatchID: uuid.New()}); err != nil {
				return -1, "", err
			}
		}
	}
	out, err := p.call("shell_command", map[string]any{"command": command, "working_dir": p.workspace, "timeout": 600, "background": false})
	if err != nil {
		return -1, "", err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return -1, "", err
	}
	var result struct {
		ExitCode *int   `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Error    string `json:"error"`
		Waiting  bool   `json:"waiting_for_input"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return -1, "", err
	}
	if result.ExitCode == nil || result.Waiting || result.Error != "" {
		return -1, result.Stdout, fmt.Errorf("verification did not finish: %s", result.Error)
	}
	return *result.ExitCode, result.Stdout, nil
}
func (p *scopedChatPlan) commit(snapshot agent.PlanSnapshot) error {
	ctx := p.invocation
	if ctx == nil {
		return fmt.Errorf("scoped plan requires live invocation")
	}
	if p.prepared != nil {
		return p.commitPrepared(snapshot)
	}
	markdown := agent.NewPlanState(snapshot.Goal, snapshot.Document, snapshot.Steps).RenderMarkdown()
	out, err := p.call("write_file", map[string]any{"path": path.Join(p.workspace, "PLAN.md"), "content": markdown})
	if err != nil {
		return err
	}
	if message, _ := out["error"].(string); message != "" {
		return fmt.Errorf("plan mirror: %s", message)
	}
	current, err := p.sessions.Get(ctx, &session.GetRequest{AppName: p.appName, UserID: p.userID, SessionID: p.sessionID})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return p.sessions.AppendEvent(ctx, current.Session, &session.Event{ID: fmt.Sprintf("plan-%s-%d", snapshot.PlanID, snapshot.Sequence), Author: "system", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}})
}

// sameChatRuntimeObject compares identity without panicking on non-comparable
// third-party implementations. Unknown identity is never deduplicated by name.
func sameChatRuntimeObject(a, b any) bool {
	return a != nil && b != nil && reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.ValueOf(a).Comparable() && a == b
}

func (b *chatRuntimeBinding) wrapToolset(ts tool.Toolset) (tool.Toolset, error) {
	if ts == nil {
		return nil, fmt.Errorf("scoped toolset is nil")
	}
	for _, old := range b.toolsets {
		if sameChatRuntimeObject(old.Toolset, ts) {
			return old, nil
		}
	}
	wrapped := &chatRuntimeToolset{Toolset: ts, binding: b}
	b.toolsets = append(b.toolsets, wrapped)
	return wrapped, nil
}

func (b *chatRuntimeBinding) wrap(t tool.Tool, source *chatRuntimeToolset) (tool.Tool, error) {
	if t == nil {
		return nil, fmt.Errorf("scoped tool is nil")
	}
	r, ok := t.(chatRuntimeRunnable)
	if !ok {
		return nil, fmt.Errorf("scoped tool %q is not executable", t.Name())
	}
	b.registryMu.Lock()
	defer b.registryMu.Unlock()
	if b.executables == nil {
		b.executables = make(map[string]*chatRuntimeTool)
	}
	if old := b.executables[t.Name()]; old != nil {
		if sameChatRuntimeObject(old.chatRuntimeRunnable, r) || (source != nil && old.source == source) {
			// Lazy MCP returns fresh proxies on every metadata read. Pin the
			// first executable for this exact toolset, never a same-name peer.
			return old, nil
		}
		return nil, fmt.Errorf("scoped tool %q conflicts with another executable", t.Name())
	}
	wrapped := &chatRuntimeTool{chatRuntimeRunnable: r, binding: b, source: source}
	b.executables[t.Name()] = wrapped
	return wrapped, nil
}

// chatRuntimeIntersectionContext fences synchronous dispatch even when an
// AfterFunc notification has not been scheduled yet. Keep cancellation latched in
// the standard context so Err remains stable, Done closes before Err is exposed,
// and release cannot cancel either borrowed source lifetime.
type chatRuntimeIntersectionKey struct{}

type chatRuntimeIntersectionContext struct {
	context.Context
	parent      context.Context
	bound       context.Context
	parentFence *chatRuntimeIntersectionContext
	boundFence  *chatRuntimeIntersectionContext
	cancel      context.CancelFunc
}

// Derived contexts such as a drill's WithCancel have their own Err method. Carry
// the fence as a private value so the next shared binding can still check the
// original lifetimes synchronously instead of relying on queued propagation.
func (c *chatRuntimeIntersectionContext) Value(key any) any {
	if _, ok := key.(chatRuntimeIntersectionKey); ok {
		return c
	}
	return c.Context.Value(key)
}

func (c *chatRuntimeIntersectionContext) refreshCancellation() {
	if (c.parentFence != nil && c.parentFence.Err() != nil) ||
		(c.boundFence != nil && c.boundFence != c.parentFence && c.boundFence.Err() != nil) ||
		c.parent.Err() != nil || c.bound.Err() != nil {
		c.cancel()
	}
}

func (c *chatRuntimeIntersectionContext) Err() error {
	c.refreshCancellation()
	return c.Context.Err()
}

func (c *chatRuntimeIntersectionContext) Done() <-chan struct{} {
	c.refreshCancellation()
	return c.Context.Done()
}

// intersectChatRuntimeContext keeps hook values, but neither lifetime may extend
// the other. Callers release the cancellation link after synchronous dispatch.
func intersectChatRuntimeContext(parent, bound context.Context) (context.Context, func()) {
	parentFence, _ := parent.Value(chatRuntimeIntersectionKey{}).(*chatRuntimeIntersectionContext)
	boundFence, _ := bound.Value(chatRuntimeIntersectionKey{}).(*chatRuntimeIntersectionContext)
	if parentFence == nil && boundFence == nil && parent.Done() == nil {
		return bound, func() {}
	}
	var merged context.Context
	var cancel context.CancelFunc
	deadline, hasDeadline := parent.Deadline()
	boundDeadline, boundHasDeadline := bound.Deadline()
	if hasDeadline && (!boundHasDeadline || deadline.Before(boundDeadline)) {
		merged, cancel = context.WithDeadline(bound, deadline)
	} else {
		merged, cancel = context.WithCancel(bound)
	}
	stop := context.AfterFunc(parent, cancel)
	if parent.Err() != nil {
		cancel()
	}
	return &chatRuntimeIntersectionContext{Context: merged, parent: parent, bound: bound, parentFence: parentFence, boundFence: boundFence, cancel: cancel}, func() { stop(); cancel() }
}

func (b *chatRuntimeBinding) bind(ctx context.Context) (context.Context, func(), error) {
	parent := ctx
	planRuntime, planBound := agent.PlanRuntimeFromContext(ctx)
	noop := func() {}
	if err := ctx.Err(); err != nil {
		return nil, noop, err
	}
	var err error
	if b.inputs != nil {
		ctx, err = b.inputs.Bind(ctx)
		if err != nil {
			return nil, noop, err
		}
	}
	if b.hooks.Context != nil {
		ctx, err = b.hooks.Context.Bind(ctx)
		if err != nil {
			return nil, noop, err
		}
	}
	ctx, release := intersectChatRuntimeContext(parent, ctx)
	if b.inputs != nil {
		ctx = tools.WithScopedSchedulerContext(ctx)
		// Inputs intentionally masks inherited store keys. Recover only the live
		// ADK session identity, not ambient stores or callbacks, after rebinding.
		if invocation, ok := parent.(tool.Context); ok {
			ctx = store.WithSessionID(ctx, invocation.SessionID())
		}
	}
	// Inputs' transport-level groups are raw capabilities. A nested dispatch
	// must see this runtime's guarded objects, never reintroduce the raw view.
	if b.groups != nil {
		ctx = agent.WithRequestMCPGroups(ctx, b.groups)
	}
	// Presence is authoritative, including nil. Bind after Inputs and hooks so
	// inherited authority cannot escape this runtime's explicit configuration.
	// The handlers receive the final guarded catalog on the current invocation.
	ctx = tools.WithFlowRunnerContext(ctx, b.flowRunner)
	ctx = tools.WithDrillExecutionContext(ctx, b.drillExecution)
	if snapshot, err := api.CapturedDrillForInvocation(ctx); err != nil {
		release()
		return nil, noop, err
	} else if snapshot != nil && b.drillExecution != nil {
		deps := *b.drillExecution
		deps.CapturedSuite = snapshot.Suite
		ctx = tools.WithDrillExecutionContext(ctx, &deps)
	}
	if planBound {
		ctx = agent.WithPlanRuntime(ctx, planRuntime)
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, noop, err
	}
	return ctx, release, nil
}

type chatRuntimeToolContext struct {
	tool.Context
	bound context.Context
}

func (c chatRuntimeToolContext) Value(key any) any           { return c.bound.Value(key) }
func (c chatRuntimeToolContext) Deadline() (time.Time, bool) { return c.bound.Deadline() }
func (c chatRuntimeToolContext) Done() <-chan struct{}       { return c.bound.Done() }
func (c chatRuntimeToolContext) Err() error                  { return c.bound.Err() }

type chatRuntimeReadonlyContext struct {
	adkagent.ReadonlyContext
	bound context.Context
}

func (c chatRuntimeReadonlyContext) Value(key any) any           { return c.bound.Value(key) }
func (c chatRuntimeReadonlyContext) Deadline() (time.Time, bool) { return c.bound.Deadline() }
func (c chatRuntimeReadonlyContext) Done() <-chan struct{}       { return c.bound.Done() }
func (c chatRuntimeReadonlyContext) Err() error                  { return c.bound.Err() }

type chatRuntimeRunnable interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(tool.Context, any) (map[string]any, error)
	ProcessRequest(tool.Context, *model.LLMRequest) error
}

type chatRuntimeTool struct {
	chatRuntimeRunnable
	binding *chatRuntimeBinding
	source  *chatRuntimeToolset
}

func (t *chatRuntimeTool) Run(ctx tool.Context, args any) (result map[string]any, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("scoped tool requires invocation context")
	}
	if t.Name() == "delegate_tasks" && t.binding.delegates != nil {
		lifetime, done, err := t.binding.delegates.begin(ctx)
		if err != nil {
			return nil, err
		}
		defer done()
		ctx = chatRuntimeToolContext{Context: ctx, bound: lifetime}
	}
	bound, release, err := t.binding.bind(ctx)
	defer release()
	if err != nil {
		return nil, err
	}
	if marker := ctx.Value(preparedPlanLeafKey{}); marker != nil {
		bound = context.WithValue(bound, preparedPlanLeafKey{}, marker)
	}
	c := chatRuntimeToolContext{Context: ctx, bound: bound}
	if t.binding.plan != nil && ctx.Value(preparedPlanLeafKey{}) == nil {
		if prepared := api.BotPreparedPlanSessionFromContext(c); prepared != nil && prepared.Resume != nil {
			record, err := prepared.ClaimResume(t.Name(), args)
			if err != nil {
				return nil, err
			}
			return t.binding.plan.run(c, preparedResumeExecutable{chatRuntimeRunnable: t.chatRuntimeRunnable, p: t.binding.plan, record: record}, args)
		}
	}
	if auth := t.binding.hooks.Authorization; auth != nil {
		if err = auth.Authorize(c, t.Name(), args); err != nil {
			return nil, err
		}
	}
	if accounting := t.binding.hooks.Accounting; accounting != nil {
		defer func() { accounting.Observe(c, t.Name(), args, result, err) }()
	}
	next := func(invocation tool.Context, values any) (map[string]any, error) {
		if t.binding.plan != nil && agent.IsPlanManagementTool(t.Name()) {
			return t.binding.plan.run(invocation, t.chatRuntimeRunnable, values)
		}
		return t.chatRuntimeRunnable.Run(invocation, values)
	}
	if dispatch := t.binding.hooks.Dispatch; dispatch != nil {
		return dispatch.Dispatch(c, t.chatRuntimeRunnable, args, next)
	}
	return next(c, args)
}
func (t *chatRuntimeTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if ctx == nil {
		return fmt.Errorf("scoped tool requires declaration context")
	}
	bound, release, err := t.binding.bind(ctx)
	defer release()
	if err != nil {
		return err
	}
	if err = t.chatRuntimeRunnable.ProcessRequest(chatRuntimeToolContext{Context: ctx, bound: bound}, req); err != nil {
		return err
	}
	if req != nil && req.Tools != nil {
		if _, ok := req.Tools[t.Name()]; ok {
			req.Tools[t.Name()] = t
		}
	}
	return nil
}

type chatRuntimeToolset struct {
	tool.Toolset
	binding *chatRuntimeBinding
}

func (s *chatRuntimeToolset) BotMetadataOnly() bool { return agent.IsBotMetadataOnlyToolset(s.Toolset) }

func (s *chatRuntimeToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	if ctx == nil {
		return nil, fmt.Errorf("scoped toolset requires discovery context")
	}
	bound, release, err := s.binding.bind(ctx)
	defer release()
	if err != nil {
		return nil, err
	}
	ts, err := s.Toolset.Tools(chatRuntimeReadonlyContext{ReadonlyContext: ctx, bound: bound})
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, len(ts))
	seen := make(map[string]bool, len(ts))
	for i, t := range ts {
		if t == nil {
			return nil, fmt.Errorf("scoped toolset returned nil tool")
		}
		if seen[t.Name()] {
			return nil, fmt.Errorf("scoped toolset has duplicate tool %q", t.Name())
		}
		seen[t.Name()] = true
		out[i], err = s.binding.wrap(t, s)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// suppliedChatDependencies is the construction boundary after resource resolution.
// It deliberately has no config loader, host store, pool or fallback resolver.
// Ordinary Chat and Bot recipes supply only the capabilities they own.
type suppliedChatDependencies struct {
	LLM                model.LLM
	Sessions           session.Service
	Prompt             *agent.SystemPromptBuilder
	Tools              []tool.Tool
	Toolsets           []tool.Toolset
	Redactor           *credentials.Redactor
	Credentials        credentials.CredentialResolver
	Debug, AutoApprove bool
}

func newSuppliedChatAgent(d suppliedChatDependencies) *agent.ChatAgent {
	chat := agent.NewChatAgent(d.LLM, d.Tools, d.Toolsets, d.Sessions, d.Prompt, d.Debug, d.AutoApprove)
	chat.Redactor, chat.CredentialStore = d.Redactor, d.Credentials
	chat.PendingSecrets = credentials.NewPendingVault(d.Redactor)
	return chat
}

// wireChatDelegation shares the capability-neutral wiring. Recipes retain their
// own guarded tool groups, child index, authorization and identity configuration.
func wireChatDelegation(chat *agent.ChatAgent, mgr *agent.SubAgentManager, llm model.LLM, sessions session.Service) {
	if mgr == nil {
		return
	}
	mgr.LLM, mgr.SessionService = llm, sessions
	mgr.Redactor, mgr.CredentialStore = chat.Redactor, chat.CredentialStore
	mgr.PendingSecrets = chat.PendingSecrets
	mgr.EventForwarder = chat.ForwardSubTaskEvent
	mgr.FileArtifactCapture = chat.CaptureFileArtifact
	chat.SubAgentManager = mgr
}

func wireChatCompactor(chat *agent.ChatAgent, window int, cfg *config.CompactionConfig, debug, codeMode bool, llm func(context.Context, string) (string, error)) *persistentsession.Compactor {
	c := persistentsession.NewCompactor(window)
	if cfg != nil {
		c.Threshold, c.PreserveRecent = cfg.GetThreshold(), cfg.GetPreserveRecent()
	}
	c.DebugMode, c.LLM = debug, llm
	if codeMode {
		c.SetStrategy(&persistentsession.CodeStrategy{})
	}
	chat.Compactor = c
	if chat.SubAgentManager != nil {
		chat.SubAgentManager.Compactor = c.Clone()
		if codeMode {
			chat.SubAgentManager.Compactor.SetStrategy(&persistentsession.CodeStrategy{})
		}
	}
	return c
}

// buildChatFlowDistiller shares validation only; callers retain model, catalog,
// and execution authority. It performs no config or fallback resource lookup.
func buildChatFlowDistiller(llm model.LLM, tools []tool.Tool, toolsets []tool.Toolset) *agent.FlowDistiller {
	return agent.NewFlowDistiller(llm, tools, toolsets, api.GetFlowSchema, func(y string, ts []agent.DistillerToolInfo) agent.FlowValidationResult {
		infos := make([]api.ToolInfo, len(ts))
		for i, t := range ts {
			infos[i] = api.ToolInfo{Name: t.Name, Description: t.Description, Source: t.Source}
		}
		r := api.ValidateFlowYAML(y, infos)
		return agent.FlowValidationResult{Valid: r.Valid, Errors: r.Errors}
	}, func(y string, schemas map[string]json.RawMessage) agent.FlowDryRunResult {
		r := api.DryRunFlowYAML(y, schemas)
		return agent.FlowDryRunResult{Valid: r.Valid, Warnings: r.Warnings, Errors: r.Errors}
	})
}

// newChatToolIndex is shared by all recipes; callers supply already guarded
// catalog entries. Semantic dependency failures never fall back to lexical.
func newChatToolIndex(ctx context.Context, tools []tool.Tool, groups []*agent.ToolGroup, vectors agent.ToolVectorStore, embed agent.EmbedFunc) (*agent.ToolIndex, error) {
	if (vectors == nil) != (embed == nil) {
		return nil, fmt.Errorf("semantic tool retrieval requires both vector store and embedding function")
	}
	if vectors != nil {
		index, err := agent.NewToolIndex(vectors, embed)
		if err != nil {
			return nil, fmt.Errorf("create semantic tool index: %w", err)
		}
		if err := index.SyncTools(ctx, tools, groups); err != nil {
			return nil, fmt.Errorf("initialize semantic tool index: %w", err)
		}
		return index, nil
	}
	index := agent.NewLexicalToolIndex()
	if err := index.PrimeTools(ctx, tools, groups); err != nil {
		return nil, fmt.Errorf("initialize tool catalog: %w", err)
	}
	return index, nil
}
