package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// productionBotChatResources borrows the same infrastructure as ordinary Studio
// Chat. Never recover a missing bundle from host defaults or a pool's default
// tenant registry. Request-specific models, tools and authorization stay local.
func productionBotChatResources(result *launcher.ChatFactoryResult) *launcher.ChatCatalogResources {
	if result == nil || result.SharedResources == nil {
		return nil
	}
	r := *result.SharedResources
	if r.Prepared || r.Backend == nil || r.Pool == nil || r.Pool != result.SandboxPool {
		return nil
	}
	return &r
}

func productionBotExecutionObserver(v entstore.BotExecutionTiming) {
	attrs := []any{"component", "bot-trace", "stage", v.Stage.String(), "result", v.Result, "duration", v.Duration.Round(time.Millisecond), "started_at", v.StartedAt, "ended_at", v.EndedAt}
	if v.Stage == entstore.BotStageBookkeeping && v.Duration > 10*time.Second {
		slog.Error("bot execution overhead exceeded", attrs...)
		return
	}
	slog.Info("bot execution trace", attrs...)
}

// newProductionBotWorker borrows Chat's bootstrap infrastructure. The worker owns
// attempts, never the shared pool/backend or retained Home lifecycle.
func newProductionBotWorker(backend store.PlatformBackend, cfg *config.AppConfig, shared *launcher.ChatCatalogResources, registry *execution.BotTurnAuthorizationRegistry) (*BotWorker, func(), error) {
	release := func() {}
	if backend == nil || cfg == nil || shared == nil || shared.Backend == nil || shared.Pool == nil || shared.Prepared {
		return nil, release, store.ErrBotUnsupported
	}
	b, pool := shared.Backend, shared.Pool
	if files, ok := b.(sandbox.BotHomeFilesBackend); ok {
		installer, ok := backend.(interface {
			InstallBotContentHomeBackend(sandbox.BotHomeFilesBackend) error
		})
		if !ok {
			return nil, release, store.ErrBotUnsupported
		}
		if err := installer.InstallBotContentHomeBackend(files); err != nil {
			return nil, release, err
		}
	}
	w, err := NewBotWorker(BotWorkerConfig{ObserveExecution: productionBotExecutionObserver, KnowledgeSafety: botKnowledgeSafety, KnowledgeRedactor: productionKnowledgeRedactor(backend, cfg), MandatePolicy: newProductionBotMandatePolicy(backend, cfg), TurnRegistry: registry, Backend: backend, Sandbox: b, Pool: pool, WorkerID: "bot-" + uuid.NewString(), ModelOptions: provider.BudgetLLMOptions{SupportsOutputLimit: true}, ResolveModel: func(ctx context.Context, p execution.Principal, scope store.BotScope, name, modelName string) (model.LLM, error) {
		org, err := backend.ForOrg(p.OrgSlug)
		if err != nil {
			return nil, err
		}
		// Read every layer strictly; a failed team read must not silently use an
		// inherited provider with different credentials. Clone maps before factory use.
		ps, err := backend.PlatformSettings().Get(ctx)
		if err != nil {
			return nil, err
		}
		os, err := backend.OrgSettings(p.OrgSlug).Get(ctx)
		if err != nil {
			return nil, err
		}
		ts, err := org.ForTeam(p.TeamSlug).Settings().Get(ctx)
		if err != nil {
			return nil, err
		}
		local := &config.AppConfig{Providers: map[string]config.ProviderConfig{}}
		if cfg != nil {
			local.General.ContextLength = cfg.General.ContextLength
		}
		merge := func(values map[string]store.ProviderConfig) {
			for n, v := range values {
				copy := config.ProviderConfig{}
				for k, x := range v {
					copy[k] = x
				}
				local.Providers[n] = copy
			}
		}
		if ps != nil {
			merge(ps.Providers)
		}
		if os != nil {
			merge(os.Providers)
		}
		if ts != nil {
			merge(ts.Providers)
		}
		if _, ok := local.Providers[name]; !ok {
			return nil, store.ErrBotUnsupported
		}
		ctx = provider.WithConfinedProviderSecrets(ctx)
		llm, err := provider.GetProvider(ctx, name, modelName, local)
		if err != nil {
			return nil, err
		}
		return newBotGenerationModel(ctx, llm, name, modelName, local), nil
	}, Prepare: func(ctx context.Context, r bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if err := productionBotPriceReadiness(r.Version); err != nil {
			return nil, err
		}
		repo := scope.Services.Bots
		if r.Bot.Scope == store.BotScopePersonal {
			repo = scope.Services.PersonalBots
		}
		if staging, ok := backend.(interface {
			BotContentStaging(context.Context, store.BotContentScope) (store.BotContentStaging, error)
		}); ok {
			scope.Services.BotContentStagingFactory = staging.BotContentStaging
		}
		files, filesOK := b.(sandbox.BotHomeFilesBackend)
		homes, homesOK := b.(sandbox.BotHomeBackend)
		if !filesOK || !homesOK {
			return nil, store.ErrBotUnsupported
		}
		exact, err := homes.EnsureBotHome(ctx, sandbox.BotHomeScope{OrgID: r.Principal.OrgSlug, TeamID: r.Principal.TeamSlug, UserID: r.Bot.OwnerID.String(), BotID: r.Bot.ID.String()})
		if err != nil {
			return nil, err
		}
		if exact == nil {
			return nil, store.ErrBotConflict
		}
		if err := entstore.SelectBotHomeConfiguration(ctx, repo, *exact, files); err != nil {
			return nil, err
		}
		guard := botLiveGuard{repo: repo, request: r}
		scope.Services.BotFilesFactory = func(c context.Context, services *store.Services, requested store.BotScope) (store.BotFiles, error) {
			if requested != r.Bot.Scope {
				return nil, store.ErrBotForbidden
			}
			manager, err := productionBotFiles(c, b, services, requested)
			if err != nil {
				return nil, err
			}
			committer, ok := repo.(store.BotFileCommitStore)
			if !ok {
				return nil, store.ErrBotUnsupported
			}
			manager.AuthorizeFileCommit = func(live context.Context, home store.BotHomeBinding, commit func() error) error {
				if home != r.Bot.Home {
					return store.ErrBotConflict
				}
				if err := guard.check(live); err != nil {
					return err
				}
				return committer.WithBotFileCommit(live, home, r.Run.Lease, commit)
			}
			return manager, nil
		}
		if err := guard.check(ctx); err != nil {
			return nil, err
		}
		// Pin the retained owner before exposing any lazy node tools. GetOrCreate
		// allocates a client only; the first authorized tool performs readiness.
		id := r.Bot.HomeSessionID.String()
		if r.Bot.HomeSessionID == uuid.Nil {
			return nil, store.ErrBotConflict
		}
		if binder, ok := b.(sandbox.BotHomeSessionBinder); ok {
			if err := binder.BindBotHomeSession(exact, id); err != nil {
				return nil, err
			}
		}
		pool.SetSessionScope(id, r.Principal.OrgSlug, r.Principal.TeamSlug)
		if err := sandbox.BindBotHome(pool, id, exact); err != nil {
			return nil, err
		}
		pool.GetOrCreateWithTemplate(id, r.Version.SandboxTemplate)
		pool.Alias(r.Run.SessionID.String(), id)
		catalog := *shared
		// These are the retained Home mount targets installed by the backends,
		// not the daemon HOME or ephemeral /workspace. Resolve the target from
		// the validated binding rather than Exec: catalog construction must stay
		// lazy and must not create compute merely to discover its working path.
		var workspaceDir string
		switch exact.Backend {
		case sandbox.BackendKindDocker, sandbox.BackendKindK8s:
			workspaceDir = "/root"
		case sandbox.BackendKindOpenShell:
			workspaceDir = "/home/astonish"
		default:
			return nil, fmt.Errorf("%w: retained Bot workspace target for backend %s", store.ErrBotUnsupported, exact.Backend)
		}
		local, err := launcher.BotScopedChatConfig(ctx, scope)
		if err != nil {
			return nil, err
		}
		resources := &launcher.BotChatSafeResources{ChatConfig: local, ProviderFactory: provider.GetProvider, Catalog: &catalog, WorkspaceDir: workspaceDir,
			PolicyRuntime:   productionBotPolicyRuntime(guard),
			VerifyIsolation: guard.check, Cleanup: func() {}}
		if workspaceBackend, ok := b.(sandbox.BotHomeWorkspaceBackend); ok {
			if tasks, ok := repo.(store.BotTaskStore); ok {
				workspace := &launcher.BotTaskWorkspace{Backend: workspaceBackend, Bots: repo, Tasks: tasks}
				if err := workspace.Install(); err != nil {
					return resources, err
				}
				resources.TaskWorkspace = workspace
			}
		}
		publication, receipts, err := launcher.BotPublicationToolsWithReceipts(r, scope)
		if err != nil {
			return resources, err
		}
		resources.FileReceipts = receipts
		resources.Tools = publication
		resources.PolicyRuntime.Classifier = launcher.BotFileActionClassifier{Fallback: resources.PolicyRuntime.Classifier}
		if r.Mandate != nil {
			// Chat owns the configured executable. Only retain the research episode;
			// installing a second bot_research executable conflicts with scoped Chat.
			episode, err := productionBotResearchEpisode(ctx, r)
			if err != nil {
				return resources, err
			}
			resources.Research = episode
			// MCP research retains its confined alias until a shared qualified research
			// hook is available. Perplexity must never register that alias twice.
			if grant := r.Mandate.Authorization.Snapshot().PublicResearch; grant != nil && grant.Route.Engine.IsMCP() {
				mcp, closeMCP, e := launcher.BotMCPTools(ctx, pool, id, scope.Services.MCPServers, scope.Services.TeamMCPServers)
				if closeMCP != nil {
					resources.Cleanup = closeMCP
				}
				if e != nil {
					return resources, e
				}
				search, e := launcher.BotConfiguredResearchTool(ctx, r, scope, provider.GetProvider, mcp, episode)
				if e != nil {
					return resources, e
				}
				groups := make(map[string]*agent.ToolGroup, len(catalog.Groups)+1)
				for name, group := range catalog.Groups {
					groups[name] = group
				}
				groups["bot_research"] = &agent.ToolGroup{Name: "bot_research", Tools: []tool.Tool{search}}
				catalog.Groups = groups
				resources.PolicyRuntime.Classifier = launcher.WithBotMandatedReadClassifier(resources.PolicyRuntime.Classifier, []tool.Tool{search})
			}
		}
		return resources, resources.VerifyIsolation(ctx)
	}})
	if err != nil {
		release()
		return nil, nil, err
	}
	if installer, ok := backend.(store.BotActivationInstaller); ok {
		installer.SetBotActivationReadiness(func(ctx context.Context, b store.Bot, v store.BotVersion) error {
			if err := productionBotPriceReadiness(v); err != nil {
				return err
			}
			return w.activationReady(ctx, b, v)
		})
	}
	return w, release, nil
}

func productionBotPolicyRuntime(guard botLiveGuard) agent.BotPolicyRuntime {
	runtime := agent.BotPolicyRuntime{Guard: guard, Classifier: launcher.BotCatalogueClassifier{}}
	// Keep absence as a nil interface: a typed-nil mandate fails closed at dispatch.
	if guard.request.Mandate != nil {
		runtime.Mandate = guard.request.Mandate
	}
	return runtime
}

// productionBotFiles selects only the requested repository. It installs no write
// authority for HTTP readers; workers add their exact run fence separately.
func productionBotFiles(ctx context.Context, backend sandbox.Backend, services *store.Services, scope store.BotScope) (*launcher.BotHomeManager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return nil, store.ErrBotForbidden
	}
	if services == nil {
		return nil, store.ErrBotUnsupported
	}
	homes, ok := backend.(sandbox.BotHomeBackend)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	if _, ok := backend.(sandbox.BotHomeFilesBackend); !ok {
		return nil, store.ErrBotUnsupported
	}
	var repo store.BotStore
	switch scope {
	case store.BotScopePersonal:
		repo = services.PersonalBots
	case store.BotScopeTeam:
		repo = services.Bots
	default:
		return nil, store.ErrBotForbidden
	}
	if repo == nil {
		return nil, store.ErrBotUnsupported
	}
	return &launcher.BotHomeManager{Backend: homes, Bots: repo, Scope: scope}, nil
}

func (w *BotWorker) filesForRequest(ctx context.Context, services *store.Services, scope store.BotScope) (store.BotFiles, error) {
	if w == nil {
		return nil, store.ErrBotUnsupported
	}
	return productionBotFiles(ctx, w.config.Sandbox, services, scope)
}

// productionBotWorkspaceDir probes the already home-bound physical sandbox.
// /workspace is ephemeral on Docker; advertising it as the Bot workspace loses
// files on cleanup. Never use the daemon HOME or rewrite exact approved paths.
func productionBotWorkspaceDir(ctx context.Context, b sandbox.Backend, id string) (string, error) {
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := b.Exec(probe, id, sandbox.ExecSpec{Command: []string{"/bin/sh", "-c", `test -n "$HOME" && test -d "$HOME" && printf '%s' "$HOME"`}})
	if err != nil {
		return "", err
	}
	if result == nil || result.ExitCode != 0 {
		return "", fmt.Errorf("%w: Bot sandbox home unavailable", store.ErrBotUnsupported)
	}
	home := string(result.Stdout)
	if !path.IsAbs(home) || home == "/" || path.Clean(home) != home || strings.ContainsAny(home, "\x00\r\n") {
		return "", fmt.Errorf("%w: invalid Bot sandbox home", store.ErrBotUnsupported)
	}
	return home, nil
}

// productionBotResearchEpisode is also used by shared Chat preparation: no second
// executable or MCP connection is needed to retain the title guard.
func productionBotResearchEpisode(ctx context.Context, r bot.RunRequest) (*launcher.BotResearchEpisode, error) {
	grant := r.Mandate.Authorization.Snapshot().PublicResearch
	initialTitle := false
	if grant == nil {
		record, err := r.Mandate.Runtime.ReadBotMandate(ctx, r.Mandate.Authorization.Ref())
		if err != nil {
			return nil, err
		}
		initialTitle = record.Provenance.InitiationKind == store.BotInitiationKindTitle
	}
	confirmedTitle := r.Run.TitleConfirmation != nil && r.Run.TitleConfirmation.State == store.BotTitleConfirmationConfirmed
	if grant != nil || initialTitle || confirmedTitle {
		return launcher.NewBotResearchEpisode(r.Run.ID, r.Run.SessionID.String(), launcher.BotResearchOpenWeb, nil)
	}
	return nil, nil
}

// productionBotMandatedTools retains the confined adapter for acceptance harnesses.
// Production shared Chat uses productionBotResearchEpisode instead; installing a
// question adapter does not grant its use.
func productionBotMandatedTools(ctx context.Context, r bot.RunRequest, scope *launcher.BotChatScope, mcp []tool.Tool, factory tools.LLMFactory) ([]tool.Tool, *launcher.BotResearchEpisode, error) {
	reads, err := launcher.BotMandatedReadTools(r.Mandate, launcher.BotRoutineReadPolicy(scope.Services))
	if err != nil {
		return nil, nil, err
	}
	if r.Mandate.Authorization.Authorize(execution.CapabilityToolExecute, "ask_user") == nil {
		workflow, err := launcher.BotWorkflowTools(r, scope)
		if err != nil {
			return nil, nil, err
		}
		for _, candidate := range workflow {
			if candidate.Name() == "ask_user" {
				reads = append(reads, candidate)
			}
		}
	}
	episode, err := productionBotResearchEpisode(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	grant := r.Mandate.Authorization.Snapshot().PublicResearch
	if grant != nil {
		search, err := launcher.BotConfiguredResearchTool(ctx, r, scope, factory, mcp, episode)
		if err != nil {
			return nil, episode, err
		}
		reads = append(reads, search)
	}
	return reads, episode, nil
}

// productionBotWorkflowTools installs only trusted tenant storage. It deliberately
// does not initialize global email, Fleet activators or disk template registries.
func productionBotWorkflowTools(ctx context.Context, backend store.PlatformBackend, r bot.RunRequest, scope *launcher.BotChatScope) ([]tool.Tool, error) {
	if backend == nil || scope == nil || scope.Services == nil {
		return nil, store.ErrBotUnsupported
	}
	org, err := backend.ForOrg(r.Principal.OrgSlug)
	if err != nil {
		return nil, err
	}
	if org == nil {
		return nil, store.ErrBotUnsupported
	}
	team := org.ForTeam(r.Principal.TeamSlug)
	if team == nil {
		return nil, store.ErrBotUnsupported
	}
	local := *scope
	svc := *scope.Services
	svc.DrillReports = team.DrillReports()
	svc.FleetSetupProfiles = team.FleetSetupProfiles()
	svc.OrgSlideTemplates, svc.PlatformSlideTemplates = org.OrgSlideTemplates(), backend.PlatformSlideTemplates()
	local.Services = &svc
	return launcher.BotWorkflowTools(r, &local)
}

// Production has no conservative model-price oracle yet. Unknown price is not
// zero cost: reject capped activation before a worker or sandbox is admitted.
func productionBotPriceReadiness(v store.BotVersion) error {
	if v.Budget.MaxModelCostMicros != nil {
		return fmt.Errorf("%w: capped model budget requires a conservative price oracle; production model price is unknown", store.ErrBotUnsupported)
	}
	return nil
}

type botUnknownClassifier struct{}

func (botUnknownClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown}, nil
}

type botLiveGuard struct {
	repo    store.BotStore
	request bot.RunRequest
}

func (g botLiveGuard) check(ctx context.Context) error {
	if g.request.Mandate != nil {
		if err := g.request.Mandate.Check(ctx); err != nil {
			return err
		}
	}
	if g.request.Mandate == nil && g.request.Run.IsInteractiveChat() {
		if g.request.Authorization == nil {
			return store.ErrBotForbidden
		}
		if err := g.request.Authorization.Authorize(ctx, execution.CapabilityChat); err != nil {
			return err
		}
	}
	if err := g.request.Budget.Check(ctx); err != nil {
		return err
	}
	b, err := g.repo.Get(ctx, g.request.Bot.ID)
	if err != nil {
		return err
	}
	if b == nil || !g.request.Run.Intent.AllowsStatus(b.Status) || b.Version != g.request.Version.Version || b.OwnerID != g.request.Bot.OwnerID {
		return store.ErrBotForbidden
	}
	r, err := g.repo.GetRun(ctx, b.ID, g.request.Run.ID)
	if err != nil {
		return err
	}
	lease := g.request.Run.Lease
	if r == nil || r.Status != store.RunRunning || r.ExecutionMode != g.request.Run.ExecutionMode || r.Intent != g.request.Run.Intent || r.SessionID != g.request.Run.SessionID || r.Lease.Fence != lease.Fence || r.Lease.WorkerID != lease.WorkerID || !time.Now().Before(r.Lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	return nil
}
func (g botLiveGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if g.request.Run.ControlOnly() && g.request.Mandate == nil {
		return execution.PolicyDecision{Kind: execution.PolicyDeny, Reason: "conversation cannot execute tools"}, store.ErrBotForbidden
	}
	if err := g.check(ctx); err != nil {
		return execution.PolicyDecision{}, err
	}
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if a.Effect == execution.EffectSpend || a.SpendMinor != 0 {
		return d, fmt.Errorf("%w: no production purchase adapter can enforce saved currency and spend caps", store.ErrBotUnsupported)
	}
	if d.Kind == execution.PolicyConsumeGrant {
		if g.request.Mandate != nil && d.GrantID == g.request.Mandate.Authorization.Ref().ID {
			grant, err := g.request.Mandate.AuthorizeAction(ctx, a)
			if err != nil || grant.ActionDigest != d.ActionDigest {
				return d, store.ErrBotForbidden
			}
			d.Kind = execution.PolicyAllow
			return d, nil
		}
		// The resume adapter owns atomic consumption. This guard must first
		// re-read the saved approval, rather than trusting the policy snapshot.
		rs, ok := g.repo.(store.BotApprovalResumeStore)
		if !ok {
			return d, store.ErrBotUnsupported
		}
		approval, err := rs.LoadResumeApproval(ctx, g.request.Run.Lease)
		if err != nil {
			return d, err
		}
		if approval == nil || approval.ID != d.GrantID || approval.ActionDigest != d.ActionDigest || approval.Status != store.ApprovalApproved || !time.Now().Before(approval.ExpiresAt) {
			return d, store.ErrBotForbidden
		}
		d.Kind = execution.PolicyAllow
		return d, nil
	}
	if d.Kind != execution.PolicyAllow {
		return d, fmt.Errorf("%w: action requires durable approval", store.ErrBotForbidden)
	}
	return d, nil
}
