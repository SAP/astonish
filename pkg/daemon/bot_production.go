package daemon

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// newProductionBotWorker initializes independently of channel configuration.
// Production tools use scoped catalogue adapters, never host/global fallbacks.
func newProductionBotWorker(backend store.PlatformBackend, cfg *config.AppConfig, registries ...*execution.BotTurnAuthorizationRegistry) (*BotWorker, func(), error) {
	var registry *execution.BotTurnAuthorizationRegistry
	if len(registries) > 0 {
		registry = registries[0]
	}
	b, cleanup, err := sandbox.BackendFromAppConfig(cfg)
	if err != nil {
		return nil, cleanup, err
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{})
	release := func() { pool.Cleanup(); cleanup() }
	w, err := NewBotWorker(BotWorkerConfig{MandatePolicy: newProductionBotMandatePolicy(backend, cfg), TurnRegistry: registry, Backend: backend, Sandbox: b, Pool: pool, WorkerID: "bot-" + uuid.NewString(), ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true}, ResolveModel: func(ctx context.Context, p execution.Principal, scope store.BotScope, name, modelName string) (model.LLM, error) {
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
		return provider.GetProvider(ctx, name, modelName, local)
	}, Prepare: func(ctx context.Context, r bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if err := productionBotPriceReadiness(r.Version); err != nil {
			return nil, err
		}
		repo := scope.Services.Bots
		if r.Bot.Scope == store.BotScopePersonal {
			repo = scope.Services.PersonalBots
		}
		guard := botLiveGuard{repo: repo, request: r}
		if err := guard.check(ctx); err != nil {
			return nil, err
		}
		id := r.Run.SessionID.String()
		pool.SetSessionScope(id, r.Principal.OrgSlug, r.Principal.TeamSlug)
		client := pool.GetOrCreateWithTemplate(id, r.Version.SandboxTemplate)
		resources := &launcher.BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: guard, Classifier: launcher.BotCatalogueClassifier{}}, Cleanup: func() {
			pool.Remove(id)
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = b.DestroySession(c, id)
		}}
		if r.Mandate != nil {
			resources.PolicyRuntime.Mandate = r.Mandate
		}
		if err := sandbox.EnsureToolClientReady(ctx, client, id); err != nil {
			return resources, err
		}
		workspace, err := productionBotWorkspaceDir(ctx, b, id)
		if err != nil {
			return resources, err
		}
		resources.WorkspaceDir = workspace
		if r.Mandate == nil && r.Run.IsInteractiveChat() {
			resources.ChatConfig = &config.AppConfig{}
			resources.Catalog = &launcher.ChatCatalogResources{Backend: b, Pool: pool}
			resources.PolicyRuntime = agent.BotPolicyRuntime{}
			resources.VerifyIsolation = func(c context.Context) error {
				if err := guard.check(c); err != nil {
					return err
				}
				state, err := b.SessionState(c, id)
				if err != nil {
					return err
				}
				if state != sandbox.SessionStateRunning {
					return store.ErrBotUnsupported
				}
				return nil
			}
			return resources, resources.VerifyIsolation(ctx)
		}
		if r.Mandate != nil {
			reads, err := launcher.BotMandatedReadTools(r.Mandate, launcher.BotRoutineReadPolicy(scope.Services))
			if err != nil {
				return resources, err
			}
			resources.Tools = reads
			resources.PolicyRuntime.Classifier = launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)
			resources.VerifyIsolation = func(c context.Context) error {
				if err := guard.check(c); err != nil {
					return err
				}
				state, err := b.SessionState(c, id)
				if err != nil {
					return err
				}
				if state != sandbox.SessionStateRunning {
					return store.ErrBotUnsupported
				}
				return nil
			}
			return resources, resources.VerifyIsolation(ctx)
		}
		var scopedTools []tool.Tool
		memoryTools, err := launcher.BotMemoryTools(r, scope)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, memoryTools...)
		browserTools, browserCleanup, browserErr := launcher.BotBrowserTools(b, id)
		if browserCleanup != nil {
			previousCleanup := resources.Cleanup
			resources.Cleanup = func() { browserCleanup(); previousCleanup() }
		}
		if browserErr == nil {
			scopedTools = append(scopedTools, browserTools...)
		}
		// Unsupported backend browser wiring remains an explicit catalogue error;
		// it must never trigger the ordinary Chat host-browser fallback.
		flowSearch, err := launcher.BotSearchFlowsTool()
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, flowSearch)
		flowRun, err := launcher.BotRunFlowTool()
		if err != nil {
			return resources, err
		}
		httpTool, err := launcher.BotHTTPTool(b, client, id)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, flowRun, httpTool)
		mcpTools, mcpCleanup, err := launcher.BotMCPTools(ctx, pool, id, scope.Services.MCPServers, scope.Services.TeamMCPServers)
		if mcpCleanup != nil {
			previousCleanup := resources.Cleanup
			resources.Cleanup = func() { mcpCleanup(); previousCleanup() }
		}
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, mcpTools...)
		scheduleTools, err := launcher.BotScheduleTools(r, scope)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, scheduleTools...)
		knowledgeTools, err := productionBotKnowledgeTools(ctx, backend, r, scope)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, knowledgeTools...)
		workflowTools, err := productionBotWorkflowTools(ctx, backend, r, scope)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, workflowTools...)
		drillTools, err := launcher.BotDrillTools(b, id)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, drillTools...)
		emailTools, err := launcher.BotEmailTools(r, scope, b, client)
		if err != nil {
			return resources, err
		}
		scopedTools = append(scopedTools, emailTools...)
		catalogue, err := launcher.BotCatalogueTools(client, id, scopedTools...)
		if err != nil {
			return resources, err
		}
		resources.Tools = catalogue
		resources.VerifyIsolation = func(c context.Context) error {
			if err := guard.check(c); err != nil {
				return err
			}
			state, err := b.SessionState(c, id)
			if err != nil {
				return err
			}
			if state != sandbox.SessionStateRunning {
				return store.ErrBotUnsupported
			}
			return nil
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

// productionBotKnowledgeTools loads inheritance from the run's authorized org,
// never from daemon config or a viewing member's personal settings.
func productionBotKnowledgeTools(ctx context.Context, backend store.PlatformBackend, r bot.RunRequest, scope *launcher.BotChatScope) ([]tool.Tool, error) {
	org, err := backend.ForOrg(r.Principal.OrgSlug)
	if err != nil {
		return nil, err
	}
	local := *scope
	svc := *scope.Services
	svc.PlatformSkills, svc.Skills = backend.PlatformSkills(), org.OrgSkills()
	svc.PlatformSettings, svc.OrgSettings = backend.PlatformSettings(), backend.OrgSettings(r.Principal.OrgSlug)
	local.Services = &svc
	return launcher.BotKnowledgeTools(r, &local, provider.GetProvider)
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
