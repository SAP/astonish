package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// BotScopedChatConfig resolves the same settings cascade as Chat, reading every
// authorized layer strictly before exposing any configured capability. Bootstrap
// provider configuration and a viewer's personal settings are not fallbacks.
func BotScopedChatConfig(ctx context.Context, scope *BotChatScope) (*config.AppConfig, error) {
	if scope == nil || scope.Services == nil {
		return nil, store.ErrBotUnsupported
	}
	return botKnowledgeSearchConfig(ctx, scope.Services)
}

// newBotScopedChatRuntime is an owner-scope adapter, not an agent recipe. All
// ordinary executables, indexes, children, plans, flows and drills are Chat's.
func newBotScopedChatRuntime(ctx context.Context, cfg *ChatFactoryConfig, in *botChatDependencies) (_ *ChatFactoryResult, err error) {
	b := in.binding
	hooks := ChatRuntimeHooks{Context: botSharedBinding{b}, Dispatch: botSharedDispatch{b}, ModelPreparation: botSharedModelPreparation{b}}
	if b.workerOnly {
		hooks.ExcludedTools = []string{"ask_user", "employee_chat"}
	}
	d := ChatRuntimeDependencies{Config: cfg, LLM: in.llm, Sessions: in.scope.Sessions, SessionID: b.req.Run.SessionID.String(), ContextWindow: in.contextWindow, DrillBudget: b.req.Budget}
	control := b.req.Run.ControlOnly() && !botFactoryInteractive(b.req) && b.req.Mandate == nil
	if control {
		d.Catalog = &ChatCatalog{}
	} else {
		if in.safe.Catalog == nil || in.safe.Catalog.Pool == nil || in.safe.Catalog.Prepared || in.safe.ChatConfig == nil {
			return nil, fmt.Errorf("%w: Bot requires shared scoped Chat resources", store.ErrBotUnsupported)
		}
		local := *cfg
		local.AppConfig = in.safe.ChatConfig
		local.WorkspaceDir = in.safe.WorkspaceDir
		d.Config = &local
		r := *in.safe.Catalog
		r.LLM = in.llm
		d.Resources = &r
		d.UseScopedEngines = true
		redactor := credentials.NewRedactor()
		redactor.HydrateFromStore(in.credentials)
		scope := chatruntime.ResourceScopeTeam
		if b.req.Bot.Scope == store.BotScopePersonal {
			scope = chatruntime.ResourceScopePersonal
		}
		var providerFactory tools.LLMFactory
		if factory := in.safe.ProviderFactory; factory != nil {
			providerFactory = func(ctx context.Context, name, modelName string, app *config.AppConfig) (model.LLM, error) {
				if err := b.check(ctx); err != nil {
					return nil, err
				}
				llm, err := factory(provider.WithConfinedProviderSecrets(ctx), name, modelName, app)
				if err != nil {
					return nil, err
				}
				if llm == nil {
					return nil, store.ErrBotUnsupported
				}
				// Search is an auxiliary model call, not an unmetered tool escape.
				return provider.NewBudgetLLM(llm, b.req.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
			}
		}
		inputs, e := chatruntime.Build(ctx, chatruntime.BuildOptions{ProviderFactory: providerFactory, Principal: b.req.Principal, Services: in.scope.Services, ResourceScope: scope, AppConfig: local.AppConfig, Pool: r.Pool, LLM: in.llm, Redactor: redactor, FilesystemSkills: r.FilesystemSkills, FallbackPolicy: chatruntime.FallbackForbidden})
		if e != nil {
			return nil, e
		}
		d.Inputs = inputs
		d.Cleanup = func() { _ = inputs.Close() }
		defer func() {
			if err != nil {
				_ = inputs.Close()
			}
		}()
		owner, e := inputs.Bind(b.owner)
		if e != nil {
			return nil, e
		}
		b.owner = withoutGenericBotMemory(credentials.WithRedactor(tools.WithTaskResultStore(store.WithSessionID(owner, d.SessionID), tools.NewTaskResultStore()), redactor))
		if b.req.Mandate != nil && b.research != nil {
			grant := b.req.Mandate.Authorization.Snapshot().PublicResearch
			if grant != nil && grant.Route.Engine == execution.BotSearchPerplexity && providerFactory != nil && local.AppConfig.General.WebSearchTool == "perplexity:perplexity_web_search" {
				guard, e := BotConfiguredResearchTool(ctx, b.req, in.scope, nil, nil, b.research)
				if e != nil {
					return nil, e
				}
				b.sharedResearch = guard.(*botConfiguredResearchTool)
				b.sharedResearch.sharedResearchAdapter = true
				in.safe.PolicyRuntime.Classifier = WithBotMandatedReadClassifier(in.safe.PolicyRuntime.Classifier, []tool.Tool{guard})
			}
		}
		// Shared configured search uses the same freshly resolved provider binding in
		// interactive and autonomous runs. Classification records the effect of an
		// already-authorized dispatch; it does not grant search authority.
		in.safe.PolicyRuntime.Classifier = botScopedSearchClassifier{services: in.scope.Services, config: local.AppConfig, fallback: in.safe.PolicyRuntime.Classifier}
		b.owner = agent.WithBotPolicyRuntime(b.owner, in.safe.PolicyRuntime)
		if !botFactoryInteractive(b.req) || b.workerOnly {
			classifier := botSharedMCPClassifier{owner: b.owner, fallback: in.safe.PolicyRuntime.Classifier, targets: map[string]agent.MCPToolBinding{}}
			for _, group := range agent.RequestMCPGroupsFromContext(b.owner) {
				for _, set := range group.Toolsets {
					proxies, err := set.Tools(nil)
					if err != nil {
						return nil, err
					}
					for _, proxy := range proxies {
						if target, ok := agent.LazyMCPToolBinding(proxy); ok {
							classifier.targets[target.Identity()] = target
						}
					}
				}
			}
			in.safe.PolicyRuntime.Classifier = classifier
			b.owner = agent.WithBotPolicyRuntime(b.owner, in.safe.PolicyRuntime)
		}
		// Bootstrap/channel email is never inherited by a Bot. Build the same
		// scoped transport as Studio, retaining Bot's exact authorization adapter.
		r.EmailTools = nil
		if r.Backend != nil {
			r.EmailTools, e = ScopedChatEmailTools(ScopedChatEmailOptions{Inputs: inputs, Scope: scope, Backend: r.Backend, Client: r.Pool.GetOrCreate(d.SessionID), SessionID: d.SessionID})
			if e != nil {
				return nil, e
			}
			if !botFactoryInteractive(b.req) || b.workerOnly {
				r.EmailTools = bindBotScopedEmailTools(r.EmailTools, b.req, in.scope.Services)
				in.safe.PolicyRuntime.Classifier = WithBotEmailClassifier(in.safe.PolicyRuntime.Classifier, r.EmailTools)
				b.owner = agent.WithBotPolicyRuntime(b.owner, in.safe.PolicyRuntime)
			}
		}
		d.Redactor = redactor
		d.Credentials = credentials.NewStoreAdapter(in.credentials)
		memory, e := BotMemoryTools(b.req, in.scope)
		if e != nil {
			return nil, e
		}
		// Missing Bot memory never exposes the owner-wide generic handlers.
		if len(memory) == 0 {
			hooks.ExcludedTools = append(hooks.ExcludedTools, "memory_save", "memory_search", "memory_get", "memory_delete")
		}
		hooks.ScopedTools = append(hooks.ScopedTools, memory...)
		schedules, e := BotScheduleTools(b.req, in.scope)
		if e != nil {
			return nil, e
		}
		hooks.ScopedTools = append(hooks.ScopedTools, schedules...)
		question, e := botWorkflowQuestionTool(b.req, in.scope)
		if e != nil {
			return nil, e
		}
		hooks.ScopedTools = append(hooks.ScopedTools, question)
		work, e := newBotWorkTools(b)
		if e != nil {
			return nil, e
		}
		hooks.AdditionalTools = append(hooks.AdditionalTools, work...)
		skills, e := newBotSkillTools(b, in.scope)
		if e != nil {
			return nil, e
		}
		hooks.AdditionalTools = append(hooks.AdditionalTools, skills...)
		tasks, e := newBotTaskTools(b)
		if e != nil {
			return nil, e
		}
		hooks.AdditionalTools = append(hooks.AdditionalTools, tasks...)
		if b.tasks != nil {
			in.safe.PolicyRuntime.Classifier = WithBotTaskClassifier(in.safe.PolicyRuntime.Classifier, tasks)
			if !botFactoryInteractive(b.req) || b.workerOnly {
				b.owner = agent.WithBotPolicyRuntime(b.owner, in.safe.PolicyRuntime)
			}
		}
		if !botFactoryInteractive(b.req) || b.workerOnly {
			in.safe.PolicyRuntime.Classifier = botCapturedDrillClassifier{owner: b.owner, fallback: in.safe.PolicyRuntime.Classifier}
			b.owner = agent.WithBotPolicyRuntime(b.owner, in.safe.PolicyRuntime)
		}
		// Only genuine Bot extensions belong here. Ordinary tools in an old daemon
		// bundle are deliberately not substituted into the shared catalog.
		for _, t := range in.safe.Tools {
			if t == nil {
				return nil, store.ErrBotUnsupported
			}
			if t.Name() == "publish_file" {
				hooks.AdditionalTools = append(hooks.AdditionalTools, t)
			}
		}
	}
	memory, e := b.currentMemory(ctx)
	if e != nil {
		return nil, e
	}
	workspace, e := b.tasks.guidance(ctx)
	if e != nil {
		return nil, e
	}
	provenance, e := b.priorRetrievalContext(ctx)
	if e != nil {
		return nil, e
	}
	hooks.Prompt = botSharedPrompt(memory + workspace + provenance)
	wired, e := NewScopedChatRuntime(b.owner, d, hooks)
	if e != nil {
		return nil, e
	}
	if !control {
		if entry := wired.ChatAgent.ToolIndex.GetToolEntry("web_fetch"); entry != nil {
			if shared, ok := entry.Tool.(*chatRuntimeTool); ok {
				b.publicFetch, _ = shared.chatRuntimeRunnable.(*sandbox.NodeTool)
			}
		}
		if entry := wired.ChatAgent.ToolIndex.GetToolEntry("perplexity_web_search"); entry != nil {
			if shared, ok := entry.Tool.(*chatRuntimeTool); ok {
				b.taskSearch = shared.chatRuntimeRunnable
				if b.sharedResearch != nil {
					b.sharedResearch.shared = shared.chatRuntimeRunnable.Run
				}
				b.taskSearchConfig = d.Config.AppConfig
			}
		}
	}
	if !control && (b.req.Mandate != nil || botFactoryInteractive(b.req)) {
		// Preserve Chat's bridge executables. Only local metadata is sealed as
		// discovery; the resolved action still runs through the shared guard.
		for i, candidate := range wired.ChatAgent.Tools {
			name := candidate.Name()
			if name != "search_tools" && name != "describe_tools" {
				continue
			}
			wrapped, e := agent.NewBotDiscoveryTool(candidate, func(call tool.Context) (tool.Context, error) {
				if call == nil || call.SessionID() == "" {
					return nil, store.ErrBotForbidden
				}
				bound := botBoundToolContext{Context: call, owner: b.bind(call)}
				if e := b.check(bound); e != nil {
					return nil, e
				}
				for _, disabled := range store.DisabledToolsFromContext(bound) {
					if disabled == name {
						return nil, store.ErrBotForbidden
					}
				}
				if botFactoryInteractive(b.req) {
					if e := b.req.Authorization.AuthorizeTool(bound, name); e != nil {
						return nil, e
					}
				} else if e := (execution.CapabilityAuthorizer{}).Authorize(b.req.Principal, execution.CapabilityToolExecute); e != nil {
					return nil, e
				}
				return bound, nil
			})
			if e != nil {
				wired.Cleanup()
				return nil, e
			}
			wired.ChatAgent.Tools[i] = wrapped
		}
	}
	if mgr := wired.ChatAgent.SubAgentManager; mgr != nil {
		mgr.AppName = "astonish"
		mgr.UserID = b.req.Bot.OwnerID.String()
		mgr.ToolGroups = botChildWorkGroups(mgr.ToolGroups)
		idx, e := newChatToolIndex(b.owner, botChildWorkTools(wired.InternalTools), agent.SortedGroups(mgr.ToolGroups), nil, nil)
		if e != nil {
			wired.Cleanup()
			return nil, e
		}
		mgr.ToolIndex = idx
	}
	if control {
		wired.ChatAgent.Tools = nil
		wired.InternalTools = nil
	}
	proposal, e := newBotProposalTool(b)
	if e != nil {
		wired.Cleanup()
		return nil, e
	}
	if proposal != nil {
		proposal, e = agent.NewBotProposalCaptureTool(proposal)
		if e != nil {
			wired.Cleanup()
			return nil, e
		}
		wired.ChatAgent.Tools = append(wired.ChatAgent.Tools, proposal)
		wired.InternalTools = append(wired.InternalTools, proposal)
	}
	if !b.workerOnly {
		if e = restoreBotPlan(ctx, wired.ChatAgent, in.scope, d.SessionID, b.req.Bot.OwnerID.String()); e != nil {
			wired.Cleanup()
			return nil, e
		}
	}
	if b.workerOnly {
		// Remove question extensions as well as shared catalog tools.
		var workerTools []tool.Tool
		for _, t := range wired.InternalTools {
			if t != proposal && t.Name() != "ask_user" && t.Name() != "employee_chat" {
				workerTools = append(workerTools, t)
			}
		}
		// Keep the configured A2A group's qualified identity; flattening every
		// eager executable into _main makes saved a2a/name approvals unresolvable.
		if group := wired.PromptBuilder.Catalog; group != nil {
			for _, g := range group {
				if g == nil || g.Name != "a2a" {
					continue
				}
				var remaining []tool.Tool
				for _, t := range workerTools {
					configured := false
					for _, candidate := range g.Tools {
						if t == candidate {
							configured = true
							break
						}
					}
					if !configured {
						remaining = append(remaining, t)
					}
				}
				workerTools = remaining
			}
		}
		// Preserve only already-guarded lazy MCP sets from shared construction.
		// Copying only Toolsets avoids reintroducing questions or sealed capture
		// through group.Tools, and retains each original executable wrapper.
		var workerGroups []*agent.ToolGroup
		for _, group := range wired.PromptBuilder.Catalog {
			if group != nil && group.Name == "a2a" {
				copy := *group
				copy.Tools = append([]tool.Tool(nil), group.Tools...)
				workerGroups = append(workerGroups, &copy)
				continue
			}
			if group == nil || !strings.HasPrefix(group.Name, "mcp:") || len(group.Toolsets) == 0 {
				continue
			}
			copy := *group
			copy.Tools = nil
			copy.Toolsets = append([]tool.Toolset(nil), group.Toolsets...)
			workerGroups = append(workerGroups, &copy)
		}
		idx, err := newChatToolIndex(b.owner, workerTools, workerGroups, nil, nil)
		if err != nil {
			wired.Cleanup()
			return nil, err
		}
		wired.ChatAgent.ToolIndex = idx
		// Retain sealed metadata discovery when rebuilding the private catalog;
		// actual targets still pass their original execution authorization.
		wired.ChatAgent.Tools, err = botProviderTools(idx, b)
		if err != nil {
			wired.Cleanup()
			return nil, err
		}
		wired.ChatAgent.Toolsets = nil
		// Sealed capture must remain native. It deliberately is not an external
		// executable and cannot inherit the progressive bridge's policy path.
		if proposal != nil {
			wired.ChatAgent.Tools = append(wired.ChatAgent.Tools, proposal)
		}
		wired.InternalTools = workerTools
	} else if !b.researchOnly && bot.EmployeeChatServiceFromContext(b.owner) != nil {
		var employeeTools []tool.Tool
		// Schedule inspection is scoped local metadata, not a delegation to a
		// tool-enabled worker. Keep it available during a paused conversation.
		inspection, err := newBotScheduleInspectionTool(b, in.scope)
		if err != nil {
			wired.Cleanup()
			return nil, err
		}
		employeeTools = append(employeeTools, inspection)
		for _, t := range wired.InternalTools {
			switch t.Name() {
			case "ask_user", "propose_bot_control", botWorkReadTool, botWorkUpdateTool, botEvidenceReadTool, botCheckWorkTool:
				employeeTools = append(employeeTools, t)
			}
		}
		orchestrate, err := functiontool.New(functiontool.Config{Name: "employee_chat", Description: "Send a request to private tool-enabled Chat. Empty worker_id starts; exact returned worker_id follows up in the same history. Results are private evidence, not owner speech."}, func(ctx tool.Context, request bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
			bound := b.bind(ctx)
			if err := b.check(bound); err != nil {
				return nil, err
			}
			service := bot.EmployeeChatServiceFromContext(bound)
			if service == nil {
				return nil, store.ErrBotUnsupported
			}
			return service.Send(bound, request)
		})
		if err != nil {
			wired.Cleanup()
			return nil, err
		}
		sealed, err := agent.NewBotEmployeeChatTool(orchestrate, func(ctx tool.Context) (tool.Context, error) {
			if b.workerOnly || b.researchOnly || b.req.Run.ControlOnly() || b.req.Run.TitleConfirmation != nil && b.req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion || ctx.SessionID() != b.req.Run.SessionID.String() {
				return nil, store.ErrBotForbidden
			}
			bound := b.bind(ctx)
			if err := b.check(bound); err != nil {
				return nil, err
			}
			return botBoundToolContext{Context: ctx, owner: bound}, nil
		})
		if err != nil {
			wired.Cleanup()
			return nil, err
		}
		employeeTools = append(employeeTools, sealed)
		idx, err := newChatToolIndex(b.owner, employeeTools, nil, nil, nil)
		if err != nil {
			wired.Cleanup()
			return nil, err
		}
		wired.ChatAgent.Tools, wired.ChatAgent.Toolsets, wired.ChatAgent.ToolIndex, wired.ChatAgent.SubAgentManager = employeeTools, nil, idx, nil
		wired.InternalTools = employeeTools
		wired.MCPToolsets = nil
	}
	return wired, nil
}

// newBotScheduleInspectionTool exposes only the genuine scoped schedule reader.
// It cannot mutate schedules, create workers, or dispatch sandbox tools.
func newBotScheduleInspectionTool(b *botFactoryBinding, scope *BotChatScope) (tool.Tool, error) {
	schedules, err := BotScheduleTools(b.req, scope)
	if err != nil {
		return nil, err
	}
	reader := schedules[0].(botRunnableTool)
	return functiontool.New(functiontool.Config{Name: reader.Name(), Description: reader.Declaration().Description}, func(ctx tool.Context, args tools.ListScheduledJobsArgs) (botScheduleListResult, error) {
		if ctx == nil || b.workerOnly || b.researchOnly || ctx.SessionID() != b.req.Run.SessionID.String() {
			return botScheduleListResult{}, store.ErrBotForbidden
		}
		bound := botBoundToolContext{Context: ctx, owner: b.bind(ctx)}
		if err := b.check(bound); err != nil {
			return botScheduleListResult{}, err
		}
		for _, disabled := range store.DisabledToolsFromContext(bound) {
			if disabled == reader.Name() {
				return botScheduleListResult{}, store.ErrBotForbidden
			}
		}
		if b.req.Mandate != nil {
			if err := b.req.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, reader.Name()); err != nil {
				return botScheduleListResult{}, err
			}
		}
		if botFactoryInteractive(b.req) {
			if err := b.req.Authorization.AuthorizeTool(bound, reader.Name()); err != nil {
				return botScheduleListResult{}, err
			}
		}
		if err := b.req.Budget.ReserveTool(bound, reader.Name()); err != nil {
			return botScheduleListResult{}, err
		}
		// Reuse the repository reader and its exact-call observation attestation;
		// never use the generic scheduler or trust model-provided result JSON.
		repo := scope.Services.Bots
		jobs := scope.Services.Scheduler
		if b.req.Bot.Scope == store.BotScopePersonal {
			repo, jobs = scope.Services.PersonalBots, scope.Services.PersonalScheduler
		}
		return (botScheduleTools{request: b.req, repo: repo, services: scope.Services, scheduler: jobs}).list(bound, args)
	})
}

type botSharedPrompt string

func (p botSharedPrompt) AddPrompt(_ context.Context, b *agent.SystemPromptBuilder) error {
	b.BuildOverride = func(clone *agent.SystemPromptBuilder) string {
		plain := *clone
		plain.BuildOverride = nil
		return plain.Build() + string(p)
	}
	return nil
}

type botSharedModelPreparation struct{ b *botFactoryBinding }

func (h botSharedModelPreparation) Begin(ctx context.Context) context.Context {
	if h.b.declarations == nil {
		return h.b.bind(ctx)
	}
	return h.b.declarations.begin(h.b.bind(ctx))
}
func (h botSharedModelPreparation) End() { h.b.declarations.close() }

type botSharedBinding struct{ b *botFactoryBinding }

func (h botSharedBinding) Bind(ctx context.Context) (context.Context, error) {
	bound := h.b.bind(ctx)
	if conversation, ok := ctx.Value(botWorkerConversationKey{}).(botWorkerConversation); ok && conversation.binding == h.b && conversation.id == h.b.conversationID {
		bound = context.WithValue(bound, botWorkerConversationKey{}, conversation)
	}
	// ADK passes the original invocation to declaration processors, not the
	// InstructionProvider's preparation context. Borrow only the active bounded
	// read operation; every SQL/lease/membership check below still runs. Never
	// create a declaration lifetime here: standalone dispatch owns its own reads.
	if reads := h.b.declarations; reads != nil {
		reads.mu.Lock()
		bound = entstore.WithBotContentReadOperationFromContext(bound, reads.owner)
		reads.mu.Unlock()
	}
	if err := h.b.check(bound); err != nil {
		return nil, err
	}
	// The progressive bridge already authorized this exact invocation. Owner
	// rebinding must retain its private one-use token, not ambient caller values.
	return agent.PreserveBotInvocationHandoff(api.PreserveCapturedDrillOccurrence(bound, ctx), ctx), nil
}

type botWorkerConversationKey struct{}

type botWorkerConversation struct {
	binding *botFactoryBinding
	id      string
}

type botWorkerExecutionContext struct {
	tool.Context
	sessionID    string
	conversation botWorkerConversation
}

func (c botWorkerExecutionContext) SessionID() string { return c.sessionID }
func (c botWorkerExecutionContext) Value(key any) any {
	if key == (botWorkerConversationKey{}) {
		return c.conversation
	}
	return c.Context.Value(key)
}

type botSharedDispatch struct{ b *botFactoryBinding }

// BindPreparedPlan admits only the factory-only saved continuation. The entry
// uses the physical run session, while nested dispatch retains private provenance.
func (h botSharedDispatch) BindPreparedPlan(ctx tool.Context) (tool.Context, error) {
	if !h.b.workerOnly {
		return ctx, nil
	}
	prepared := api.BotPreparedPlanSessionFromContext(ctx)
	if ctx == nil || prepared == nil || prepared.Resume == nil || ctx.SessionID() != h.b.req.Run.SessionID.String() || prepared.Request.Run.ID != h.b.req.Run.ID {
		return nil, store.ErrBotForbidden
	}
	if err := h.b.check(ctx); err != nil {
		return nil, err
	}
	return botWorkerExecutionContext{Context: ctx, sessionID: h.b.req.Run.SessionID.String(), conversation: botWorkerConversation{binding: h.b, id: h.b.conversationID}}, nil
}

func (h botSharedDispatch) Dispatch(ctx tool.Context, t tool.Tool, args any, next func(tool.Context, any) (map[string]any, error)) (result map[string]any, cause error) {
	if h.b.workerOnly && (t.Name() == "ask_user" || t.Name() == "employee_chat") {
		return nil, store.ErrBotForbidden
	}
	if h.b.workerOnly {
		if ctx == nil || h.b.conversationID == "" {
			return nil, store.ErrBotForbidden
		}
		conversation, nested := ctx.Value(botWorkerConversationKey{}).(botWorkerConversation)
		if nested {
			if conversation.binding != h.b || conversation.id != h.b.conversationID || ctx.SessionID() != h.b.req.Run.SessionID.String() {
				return nil, store.ErrBotForbidden
			}
		} else if ctx.SessionID() != h.b.conversationID {
			return nil, store.ErrBotForbidden
		}
		// Retain private conversation provenance separately from the physical run
		// session used by scoped engines. Nested plan mirrors and verifiers must
		// redispatch through the same guards, not impersonate a public run call.
		ctx = botWorkerExecutionContext{Context: ctx, sessionID: h.b.req.Run.SessionID.String(), conversation: botWorkerConversation{binding: h.b, id: h.b.conversationID}}
	}
	if c := h.b.req.Run; c.ControlOnly() && !botFactoryInteractive(h.b.req) && h.b.req.Mandate == nil {
		return nil, store.ErrBotForbidden
	}
	if h.b.researchOnly && t.Name() != "web_fetch" && t.Name() != "perplexity_web_search" {
		return nil, store.ErrBotForbidden
	}
	target, ok := t.(botRunnableTool)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	defer func() {
		if captureErr := h.b.captureSharedTaskSearch(ctx, target, args, result, cause); captureErr != nil {
			cause = errors.Join(cause, captureErr)
		}
	}()
	// ctx has already passed the shared owner binding. Retain its scoped engine
	// capabilities while the existing policy wrapper rebinds invocation lifetime.
	binding := *h.b
	binding.owner = ctx
	if binding.publicFetch != nil && target == binding.publicFetch {
		next = func(c tool.Context, a any) (map[string]any, error) {
			var readExecuted bool
			return binding.publicFetch.RunWithTransport(c, a, func(client sandbox.ToolNodeClient) sandbox.ToolNodeClient {
				return &botPublicFetchClient{ToolNodeClient: client, executed: &readExecuted}
			}, func(values, result map[string]any, err error) {
				recordBuiltinToolObservationWithRead(c, c.SessionID(), target.Name(), values, result, readExecuted && err == nil, a)
			})
		}
	}
	if guard := binding.sharedResearch; guard != nil && target == binding.taskSearch {
		next = func(c tool.Context, args any) (map[string]any, error) {
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			if _, _, err := guard.prepare(c, raw); err != nil {
				return nil, err
			}
			var a botPublicResearchArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			result, err := guard.search(c, a)
			body, _ := json.Marshal(result)
			var values map[string]any
			_ = json.Unmarshal(body, &values)
			return values, err
		}
	}
	if ctx.SessionID() != binding.req.Run.SessionID.String() {
		if len(botChildWorkTools([]tool.Tool{target})) == 0 {
			return nil, store.ErrBotForbidden
		}
	}
	// Validate inside the authorized continuation so progress accounting sees
	// the uncertain write before closing its journal entry. This is Bot policy,
	// not a change to ordinary Chat's NodeTool transport contract.
	if _, builtin := target.(*sandbox.NodeTool); builtin && target.Name() == "write_file" {
		run := next
		if run == nil {
			run = target.Run
		}
		next = func(c tool.Context, a any) (map[string]any, error) {
			result, err := run(c, a)
			if err == nil && result["error"] == nil {
				err = validateBotSharedWriteReceipt(a, result)
			}
			return result, err
		}
	}
	if target.Name() == "run_drill" {
		run := next
		if run == nil {
			run = target.Run
		}
		next = func(c tool.Context, a any) (map[string]any, error) {
			snapshot, err := api.CapturedDrillForInvocation(c)
			if err != nil {
				return nil, err
			}
			if snapshot != nil {
				deps := tools.DrillExecutionFromContext(c)
				if deps == nil {
					return nil, store.ErrBotUnsupported
				}
				copy := *deps
				copy.CapturedSuite = snapshot.Suite
				c = chatRuntimeToolContext{Context: c, bound: tools.WithDrillExecutionContext(c, &copy)}
			}
			return run(c, a)
		}
	}
	// Private workers use the exact-action boundary even on interactive runs.
	// The public interactive wrapper uses bare Chat names and cannot consume a
	// qualified MCP/A2A dispatch handoff or prepare its lazy terminal adapter.
	if botFactoryInteractive(binding.req) && !binding.workerOnly {
		return (&interactiveBotTool{botRunnableTool: target, binding: &binding, next: next}).Run(ctx, args)
	}
	identity := target.Name()
	for groupName, group := range agent.RequestMCPGroupsFromContext(ctx) {
		for _, candidate := range group.Tools {
			if shared, ok := candidate.(*chatRuntimeTool); ok && shared.chatRuntimeRunnable == t {
				identity = agent.BotToolIdentity(groupName, target.Name())
			}
		}
	}
	return (&botBoundTool{identity: identity, botRunnableTool: target, binding: &binding, next: next}).Run(ctx, args)
}

type botSharedMCPClassifier struct {
	owner    context.Context
	fallback agent.BotActionClassifier
	targets  map[string]agent.MCPToolBinding
}

func (c botSharedMCPClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if target, ok := c.targets[name]; ok {
		// The outer ChatRunner context carries policy, not the scoped resource
		// bindings created by Chat construction. Resolve target metadata from the
		// captured owner scope, never a later viewer, preserving call cancellation.
		if c.owner != nil {
			ctx = botBoundContext{Context: ctx, owner: c.owner}
		}
		return target.DescribeAction(ctx, name, args)
	}
	if c.fallback != nil {
		return c.fallback.DescribeAction(ctx, name, args)
	}
	return execution.ActionDescriptor{}, store.ErrBotForbidden
}

// Ordinary sandbox file writes no longer have the confined Bot adapter's
// compare-and-swap guarantees. Never inherit its name-only write grant.
type botSharedFileClassifier struct{ fallback agent.BotActionClassifier }

func (c botSharedFileClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	switch name {
	case "write_file", "edit_file":
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown}, nil
	case "read_file", "find_files", "file_tree", "web_fetch":
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	case "publish_file":
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectReversibleWrite, ConstraintsVerified: true}, nil
	}
	if c.fallback != nil {
		return c.fallback.DescribeAction(ctx, name, args)
	}
	return BotCatalogueClassifier{}.DescribeAction(ctx, name, args)
}

// validateBotSharedWriteReceipt is called only for the pinned built-in node
// after dispatch. A write may already have happened: failure must not synthesize
// an unchanged receipt or retry the transport.
func validateBotSharedWriteReceipt(args any, result map[string]any) error {
	invalid := fmt.Errorf("%w: built-in write receipt missing or invalid; effect is uncertain", store.ErrBotConflict)
	raw, err := json.Marshal(args)
	if err != nil {
		return invalid
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return invalid
	}
	values, err = common.NormalizeFileToolArguments("write_file", values)
	if err != nil {
		return invalid
	}
	if _, _, verified := tools.VerifiedBuiltinFileReceipt("write_file", values, result); !verified {
		return invalid
	}
	return nil
}

func recordBotSharedNodeObservation(ctx tool.Context, t tool.Tool, args any, result map[string]any, err error) {
	if _, ok := t.(*sandbox.NodeTool); !ok {
		return
	}
	raw, e := json.Marshal(args)
	if e != nil {
		return
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return
	}
	values, e = common.NormalizeFileToolArguments(t.Name(), values)
	if e != nil {
		return
	}
	if err != nil {
		result = map[string]any{"error": err.Error()}
	}
	recordBuiltinToolObservation(ctx, ctx.SessionID(), t.Name(), values, result)
}

type botSharedNext struct {
	botRunnableTool
	next func(tool.Context, any) (map[string]any, error)
}

func (t botSharedNext) Run(c tool.Context, a any) (map[string]any, error) { return t.next(c, a) }
func botDispatchTarget(t botRunnableTool, next func(tool.Context, any) (map[string]any, error)) botRunnableTool {
	if next == nil {
		return t
	}
	return botSharedNext{t, next}
}
