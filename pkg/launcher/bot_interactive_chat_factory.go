package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"strings"
)

// Interactive composition deliberately never enters ordinary factory host bootstrap
// or the autonomous action classifier. Every executable view shares one wrapper.
func newInteractiveBotWiredChatAgent(ctx context.Context, cfg *ChatFactoryConfig, in *botWiredInjection) (*ChatFactoryResult, error) {
	r := in.safe.Catalog
	if r == nil || r.Pool == nil || r.Prepared || in.safe.ChatConfig == nil {
		return nil, store.ErrBotUnsupported
	}
	redactor := credentials.NewRedactor()
	scope := chatruntime.ResourceScopeTeam
	if in.binding.req.Bot.Scope == store.BotScopePersonal {
		scope = chatruntime.ResourceScopePersonal
	}
	inputs, err := chatruntime.Build(ctx, chatruntime.BuildOptions{Principal: in.binding.req.Principal, Services: in.scope.Services, ResourceScope: scope, AppConfig: in.safe.ChatConfig, Pool: r.Pool, LLM: in.llm, Redactor: redactor, FilesystemSkills: r.FilesystemSkills, FallbackPolicy: chatruntime.FallbackForbidden})
	if err != nil {
		return nil, err
	}
	owner, err := inputs.Bind(in.binding.owner)
	if err != nil {
		return nil, err
	}
	owner = store.WithSessionID(owner, in.binding.req.Run.SessionID.String())
	in.binding.owner = credentials.WithRedactor(tools.WithTaskResultStore(owner, tools.NewTaskResultStore()), redactor)
	local := *cfg
	local.AppConfig = in.safe.ChatConfig
	copyResources := *r
	copyResources.LLM = in.llm
	local.CatalogResources = &copyResources
	catalog, err := BuildChatCatalog(in.binding.owner, inputs, &local)
	if err != nil {
		return nil, err
	}
	prompt := &agent.SystemPromptBuilder{WorkspaceDir: in.safe.WorkspaceDir, SandboxEnabled: true, SandboxWorkspaceDir: in.safe.WorkspaceDir}
	chat := agent.NewChatAgent(in.llm, nil, nil, in.scope.Sessions, prompt, false, false)
	wrappers := map[string]tool.Tool{}
	wrap := func(t tool.Tool) (tool.Tool, error) {
		if existing := wrappers[t.Name()]; existing != nil {
			return existing, nil
		}
		run, ok := t.(botRunnableTool)
		if !ok {
			return nil, fmt.Errorf("unsupported interactive executable %s", t.Name())
		}
		w := &interactiveBotTool{botRunnableTool: run, binding: in.binding}
		wrappers[t.Name()] = w
		return w, nil
	}
	var all []tool.Tool
	for _, t := range catalog.AllTools {
		w, e := wrap(t)
		if e != nil {
			return nil, e
		}
		all = append(all, w)
	}
	// Replace global plan callbacks with the existing per-agent, sandbox-backed plan runtime.
	plans, err := newBotPlanTools(chat, all, in.safe.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	for _, p := range plans {
		if wrappers[p.Name()] != nil {
			wrappers[p.Name()] = &interactiveBotTool{botRunnableTool: p.(botRunnableTool), binding: in.binding}
		}
	}
	// run_flow uses the shared Chat engine with already-bound child tools and model.
	flow, err := BotRunFlowTool()
	if err != nil {
		return nil, err
	}
	if wrappers[flow.Name()] != nil {
		wrappers[flow.Name()] = &interactiveBotTool{botRunnableTool: flow.(botRunnableTool), binding: in.binding}
	}
	distill, err := functiontool.New(functiontool.Config{Name: "distill_flow", Description: "Distill this scoped conversation into a reusable flow"}, func(c tool.Context, _ tools.DistillFlowArgs) (map[string]any, error) {
		ds := agent.DistillSession{SessionID: c.SessionID(), AppName: c.AppName(), UserID: c.UserID()}
		if _, e := chat.PreviewDistill(c, ds); e != nil {
			return nil, e
		}
		var output strings.Builder
		if e := chat.ConfirmAndDistill(c, ds, func(text string) { output.WriteString(text) }); e != nil {
			return nil, e
		}
		return map[string]any{"status": "ok", "message": output.String()}, nil
	})
	if err != nil {
		return nil, err
	}
	if wrappers[distill.Name()] != nil {
		wrappers[distill.Name()] = &interactiveBotTool{botRunnableTool: distill.(botRunnableTool), binding: in.binding}
	}
	// Workflow adapters explicitly reject unbound Fleet/global callbacks.
	workflow, err := BotWorkflowTools(in.binding.req, in.scope)
	if err != nil {
		return nil, err
	}
	for _, t := range workflow {
		if wrappers[t.Name()] != nil {
			wrappers[t.Name()] = &interactiveBotTool{botRunnableTool: t.(botRunnableTool), binding: in.binding}
		}
	}
	// Replace ordinary Chat scheduler tools before building every executable and
	// discovery view. Bot schedules must remain Bot-owned reviewed triggers.
	schedules, err := BotScheduleTools(in.binding.req, in.scope)
	if err != nil {
		return nil, err
	}
	for _, t := range schedules {
		if wrappers[t.Name()] != nil {
			wrappers[t.Name()] = &interactiveBotTool{botRunnableTool: t.(botRunnableTool), binding: in.binding}
		}
	}
	// No detached drill transport is composed here; never use Chat's global runner.
	for _, name := range []string{"run_drill", "inject_drill_credentials", "save_sandbox_template"} {
		if t := wrappers[name]; t != nil {
			wrappers[name] = &interactiveBotTool{botRunnableTool: &interactiveUnavailableTool{botRunnableTool: t.(botRunnableTool)}, binding: in.binding}
		}
	}
	all = nil
	for _, t := range catalog.AllTools {
		all = append(all, wrappers[t.Name()])
	}
	flow.(*botFlowTool).configure(in.llm, all, redactor)
	chat.FlowRunner = func(c context.Context, yaml string, params map[string]string) (*agent.DryRunExecResult, error) {
		bound := in.binding.bind(c)
		if err := in.binding.check(bound); err != nil {
			return nil, err
		}
		if err := in.binding.req.Authorization.AuthorizeTool(bound, "run_flow"); err != nil {
			return nil, err
		}
		f := flow.(*botFlowTool)
		f.mu.Lock()
		defer f.mu.Unlock()
		key := in.binding.req.Run.SessionID.String() + ":distill-test"
		defer f.engine.CleanupSession(key)
		result, err := f.executeYAML(bound, key, yaml, params)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(result)
		return &agent.DryRunExecResult{Success: result != nil && result.Status == "completed", Output: string(raw)}, nil
	}
	chat.FlowDistiller = agent.NewFlowDistiller(in.llm, all, nil, api.GetFlowSchema, func(y string, ts []agent.DistillerToolInfo) agent.FlowValidationResult {
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

	for _, g := range catalog.Groups {
		for i, t := range g.Tools {
			g.Tools[i] = wrappers[t.Name()]
		}
		for i, ts := range g.Toolsets {
			g.Toolsets[i] = &interactiveBotToolset{Toolset: ts, binding: in.binding}
		}
	}
	index := agent.NewLexicalToolIndex()
	if err = index.PrimeTools(in.binding.owner, all, agent.SortedGroups(catalog.Groups)); err != nil {
		return nil, err
	}
	bridge, err := botProviderTools(index, in.binding)
	if err != nil {
		return nil, err
	}
	chat.Tools = append(all, bridge...)
	prompt.Tools = all
	chat.ToolIndex = index
	chat.Redactor = redactor
	chat.CredentialStore = credentials.NewStoreAdapter(in.credentials)
	chat.PendingSecrets = credentials.NewPendingVault(redactor)
	chat.Compactor = persistentsession.NewCompactor(in.contextWindow)
	chat.Compactor.LLM = func(c context.Context, p string) (string, error) { return makeLLMFunc(in.llm)(in.binding.bind(c), p) }
	mgr := catalog.SubAgentManager
	if mgr != nil {
		mgr.LLM = in.llm
		mgr.SessionService = in.scope.Sessions
		mgr.AppName = "astonish"
		mgr.UserID = in.binding.req.Bot.OwnerID.String()
		mgr.ToolGroups = catalog.Groups
		mgr.ToolIndex = index
		mgr.Redactor = redactor
		mgr.CredentialStore = chat.CredentialStore
		mgr.PendingSecrets = chat.PendingSecrets
		mgr.SandboxEnabled = true
		mgr.SandboxWorkspaceDir = in.safe.WorkspaceDir
		mgr.EventForwarder = chat.ForwardSubTaskEvent
		mgr.FileArtifactCapture = chat.CaptureFileArtifact
		mgr.Compactor = chat.Compactor.Clone()
		chat.SubAgentManager = mgr
	}
	in.binding.owner = agent.WithRequestMCPGroups(in.binding.owner, catalog.Groups)
	if err = restoreBotPlan(ctx, chat, in.scope, in.binding.req.Run.SessionID.String(), in.binding.req.Bot.OwnerID.String()); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	close(done)
	return &ChatFactoryResult{ChatAgent: chat, LLM: in.llm, ProviderName: cfg.ProviderName, ModelName: cfg.ModelName, ProviderConfigured: true, Compactor: chat.Compactor, InternalTools: all, SessionService: in.scope.Sessions, PromptBuilder: prompt, IndexingDone: done, Cleanup: func() { flow.(*botFlowTool).cleanup(); _ = inputs.Close() }}, nil
}

type interactiveBotTool struct {
	botRunnableTool
	binding *botFactoryBinding
}

func (t *interactiveBotTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil || ctx.SessionID() == "" {
		return nil, store.ErrBotForbidden
	}
	bound := botBoundToolContext{Context: ctx, owner: t.binding.bind(ctx)}
	if err := t.binding.check(bound); err != nil {
		return nil, err
	}
	if err := t.binding.req.Authorization.AuthorizeTool(bound, t.Name()); err != nil {
		return nil, err
	}
	for _, disabled := range store.DisabledToolsFromContext(bound) {
		if disabled == t.Name() {
			return nil, store.ErrBotForbidden
		}
	}
	if err := t.binding.req.Budget.ReserveTool(bound, t.Name()); err != nil {
		return nil, err
	}
	result, err := t.botRunnableTool.Run(bound, args)
	// Only the shared built-in node transport is eligible; an MCP tool with
	// the same name or a forged result cannot attest to effects.
	if _, builtin := t.botRunnableTool.(*sandbox.NodeTool); builtin && err == nil {
		raw, marshalErr := json.Marshal(args)
		var values map[string]any
		if marshalErr == nil && json.Unmarshal(raw, &values) == nil {
			if values, normalizeErr := common.NormalizeFileToolArguments(t.Name(), values); normalizeErr == nil {
				recordBuiltinToolObservation(bound, t.binding.req.Run.SessionID.String(), t.Name(), values, result)
			}
		}
	}
	return result, err
}
func (t *interactiveBotTool) ProcessRequest(ctx tool.Context, r *model.LLMRequest) error {
	if ctx == nil {
		return store.ErrBotForbidden
	}
	bound := botBoundToolContext{Context: ctx, owner: t.binding.bind(ctx)}
	if err := t.binding.check(bound); err != nil {
		return err
	}
	if err := t.botRunnableTool.ProcessRequest(bound, r); err != nil {
		return err
	}
	// Declarations may register their raw executable. Preserve the same bound
	// dispatch used by progressive discovery, lazy toolsets and children.
	if r != nil && r.Tools != nil {
		if _, registered := r.Tools[t.Name()]; registered {
			r.Tools[t.Name()] = t
		}
	}
	return nil
}

type interactiveReadonlyContext struct {
	adkagent.ReadonlyContext
	owner context.Context
}

func (c interactiveReadonlyContext) Value(k any) any { return c.owner.Value(k) }

type interactiveUnavailableTool struct{ botRunnableTool }

func (t *interactiveUnavailableTool) Run(tool.Context, any) (map[string]any, error) {
	return nil, fmt.Errorf("%w: %s requires scoped runtime composition", store.ErrBotUnsupported, t.Name())
}

type interactiveBotToolset struct {
	tool.Toolset
	binding *botFactoryBinding
}

func (s *interactiveBotToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	bound := interactiveReadonlyContext{ReadonlyContext: ctx, owner: s.binding.bind(ctx)}
	if err := s.binding.check(bound); err != nil {
		return nil, err
	}
	ts, err := s.Toolset.Tools(bound)
	if err != nil {
		return nil, err
	}
	out := make([]tool.Tool, 0, len(ts))
	for _, t := range ts {
		r, ok := t.(botRunnableTool)
		if !ok {
			return nil, store.ErrBotUnsupported
		}
		out = append(out, &interactiveBotTool{botRunnableTool: r, binding: s.binding})
	}
	return out, nil
}
