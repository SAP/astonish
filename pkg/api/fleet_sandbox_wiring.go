package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

func resolveFleetPlanCredentials(ctx context.Context, plan *fleet.FleetPlan, cs store.CredentialStore) (map[string]*fleet.ResolvedCredential, error) {
	if plan == nil || len(plan.Credentials) == 0 {
		return nil, nil
	}
	if cs != nil {
		return fleet.ResolveCredentialsPlatform(ctx, plan, cs)
	}
	if fileStore := getAPICredentialStore(); fileStore != nil {
		return fleet.ResolveCredentials(plan, fileStore)
	}
	return nil, fmt.Errorf("credential store is not available")
}

func buildFleetSandboxEnv(ctx context.Context, plan *fleet.FleetPlan, cs store.CredentialStore) (map[string]string, map[string]*fleet.ResolvedCredential, error) {
	resolved, err := resolveFleetPlanCredentials(ctx, plan, cs)
	if err != nil {
		return nil, resolved, err
	}
	injEnv, err := fleet.BuildInjectionEnv(plan, resolved, cs, ctx)
	if err != nil {
		return nil, resolved, err
	}
	env := map[string]string{}
	if injEnv != nil {
		for k, v := range injEnv {
			env[k] = v
		}
	}
	// Backward compat: GH_TOKEN from resolved github when not in injection spec
	if env["GH_TOKEN"] == "" {
		if t := fleet.GitHubToken(resolved); t != "" {
			env["GH_TOKEN"] = t
		}
	}
	if key := os.Getenv("BIFROST_API_KEY"); key != "" {
		env["BIFROST_API_KEY"] = key
	}
	return env, resolved, nil
}

// wireFleetSandbox creates sandbox infrastructure for a fleet session when
// sandbox mode is enabled.
func wireFleetSandbox(fleetSession *fleet.FleetSession, plan *fleet.FleetPlan, credStore store.CredentialStore, mcpStores *store.MCPServerStores, teamTemplate string) error {
	appCfg, err := config.LoadAppConfig()
	if err != nil || appCfg == nil {
		return nil
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return nil
	}

	sandbox.SetSandboxConfig(&appCfg.Sandbox)
	kind := sandbox.BackendKind(appCfg.Sandbox.BackendKind())

	ctx := context.Background()
	boundStore := fleet.NewPlanBoundCredentialStore(credStore, plan)
	if boundStore == nil {
		boundStore = credStore
	}
	env, resolved, envErr := buildFleetSandboxEnv(ctx, plan, boundStore)
	if envErr != nil {
		slog.Warn("fleet sandbox env build failed", "component", "fleet-sandbox", "plan", plan.Key, "error", envErr)
	}

	template := ""
	if plan != nil && plan.Template != "" {
		template = plan.Template
	} else if teamTemplate != "" {
		template = teamTemplate
	}

	if kind == sandbox.BackendKindIncus || kind == "" {
		kind = sandbox.BackendKindDocker
	}
	if mgr := tools.GetSubAgentManager(); mgr != nil && mgr.Redactor != nil {
		fleet.RegisterInjectionWithRedactor(mgr.Redactor, env)
	}
	return wireFleetSandboxBackend(fleetSession, plan, boundStore, resolved, env, mcpStores, template, appCfg, kind)
}

func wireFleetSandboxBackend(
	fleetSession *fleet.FleetSession,
	plan *fleet.FleetPlan,
	credStore store.CredentialStore,
	resolved map[string]*fleet.ResolvedCredential,
	env map[string]string,
	mcpStores *store.MCPServerStores,
	template string,
	appCfg *config.AppConfig,
	kind sandbox.BackendKind,
) error {
	fleetBackend, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if err != nil {
		return fmt.Errorf("fleet sandbox backend unavailable: %w", err)
	}
	limits := sandbox.ToResourceLimits(sandbox.EffectiveLimits(&appCfg.Sandbox))
	pinnedClient := sandbox.NewPinnedBackendClient(fleetBackend, template, env, limits)
	pinnedClient.BindSession(fleetSession.ID)
	if err := pinnedClient.EnsureReady(fleetSession.ID); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return fmt.Errorf("fleet sandbox session not ready: %w", err)
	}

	ctx := context.Background()
	if err := fleet.MaterializeInjectionFiles(ctx, fleetBackend, fleetSession.ID, plan, resolved, credStore); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return fmt.Errorf("fleet credential file injection failed: %w", err)
	}
	if err := materializeFleetBootstrapBackend(ctx, fleetBackend, fleetSession.ID, template, nil); err != nil {
		slog.Warn("fleet bootstrap file injection failed", "component", "fleet-sandbox", "template", template, "error", err)
	}

	var providerBinding *fleet.OpenShellProviderBinding
	if osBackend, ok := fleetBackend.(*openshell.OpenShellBackend); ok {
		if gw := osBackend.Gateway(); gw != nil {
			sandboxName := fmt.Sprintf("astn-sess-%s", truncateID(fleetSession.ID, 8))
			providerBinding, err = fleet.AttachOpenShellProviders(ctx, gw, sandboxName, fleetSession.ID, plan, credStore)
			if err != nil {
				if cleanup != nil {
					cleanup()
				}
				return fmt.Errorf("openshell provider attach failed: %w", err)
			}
			fleet.ApplyOptionalCredentialEgress(ctx, gw, sandboxName, plan)
		}
	}

	subAgentMgr := tools.GetSubAgentManager()
	if subAgentMgr == nil {
		if cleanup != nil {
			cleanup()
		}
		return fmt.Errorf("sub-agent manager not available")
	}

	browserMgr := browser.NewManager(browser.DefaultConfig())
	if osBackend, ok := fleetBackend.(*openshell.OpenShellBackend); ok {
		if gw := osBackend.Gateway(); gw != nil {
			if sessReg := osBackend.Sessions(); sessReg != nil {
				openshell.WireBrowserManager(browserMgr, gw, sessReg, sessReg.TouchActivity)
			}
		}
	}

	toolPool := sandbox.NewSingleClientPool(pinnedClient, fleetBackend)
	wrappedTools := wrapFleetTools(subAgentMgr, nil, toolPool, fleetSession.ID, browserMgr)
	sandboxToolsets := createFleetMCPToolsets(fleetBackend, nil, toolPool, mcpStores)

	fleetSession.SandboxTools = wrappedTools
	fleetSession.SandboxToolsets = sandboxToolsets
	setFleetWorkspaceDir(fleetSession, plan)

	prevCleanup := fleetSession.OnCleanup
	fleetSession.OnCleanup = func() {
		browserMgr.Cleanup()
		if providerBinding != nil {
			providerBinding.DetachAll(context.Background())
		}
		toolPool.Cleanup()
		_ = fleetBackend.DestroySession(context.Background(), fleetSession.ID)
		if cleanup != nil {
			cleanup()
		}
		if prevCleanup != nil {
			prevCleanup()
		}
	}

	slog.Info("sandbox enabled for fleet session", "component", "fleet-sandbox", "session_id", fleetSession.ID, "backend", kind, "template", template, "env_keys", len(env))
	return nil
}

func wrapFleetTools(subAgentMgr *agent.SubAgentManager, lazyNode *sandbox.LazyNodeClient, toolPool sandbox.ToolNodePool, fleetSessionID string, browserMgr *browser.Manager) []tool.Tool {
	var baseTools []tool.Tool
	for _, t := range subAgentMgr.AllTools() {
		if agent.IsExcludedChildTool(t.Name()) {
			continue
		}
		if excludedFleetTools[t.Name()] {
			continue
		}
		baseTools = append(baseTools, t)
	}

	var wrappedTools []tool.Tool
	if lazyNode != nil {
		wrappedTools = sandbox.WrapToolsWithNodeClient(baseTools, lazyNode)
	} else if toolPool != nil {
		wrappedTools = sandbox.WrapToolsWithPool(baseTools, toolPool)
	} else {
		wrappedTools = baseTools
	}

	if subAgentMgr.FleetTools != nil {
		var wrappedFleet []tool.Tool
		if lazyNode != nil {
			wrappedFleet = sandbox.WrapToolsWithNodeClient(subAgentMgr.FleetTools, lazyNode)
		} else if toolPool != nil {
			wrappedFleet = sandbox.WrapToolsWithPool(subAgentMgr.FleetTools, toolPool)
		} else {
			wrappedFleet = subAgentMgr.FleetTools
		}
		wrappedTools = append(wrappedTools, wrappedFleet...)
	}

	if lazyNode != nil {
		runDrillTool, runDrillErr := tools.NewRunDrillToolWithClient(lazyNode, fleetSessionID, browserMgr, nil)
		if runDrillErr == nil {
			wrappedTools = replaceOrAppendTool(wrappedTools, runDrillTool)
		}
		injectCredsTool, injectCredsErr := tools.NewInjectDrillCredentialsToolWithClient(lazyNode, fleetSessionID, browserMgr)
		if injectCredsErr == nil {
			wrappedTools = replaceOrAppendTool(wrappedTools, injectCredsTool)
		}
	} else if toolPool != nil {
		client := toolPool.GetOrCreate(fleetSessionID)
		runDrillTool, runDrillErr := tools.NewRunDrillToolWithToolClient(client, fleetSessionID, browserMgr, nil)
		if runDrillErr == nil {
			wrappedTools = replaceOrAppendTool(wrappedTools, runDrillTool)
		}
		injectCredsTool, injectCredsErr := tools.NewInjectDrillCredentialsToolWithToolClient(client, fleetSessionID, browserMgr)
		if injectCredsErr == nil {
			wrappedTools = replaceOrAppendTool(wrappedTools, injectCredsTool)
		}
	}

	return wrappedTools
}

func replaceOrAppendTool(tools []tool.Tool, replacement tool.Tool) []tool.Tool {
	for i, t := range tools {
		if t.Name() == replacement.Name() {
			tools[i] = replacement
			return tools
		}
	}
	return append(tools, replacement)
}

func setFleetWorkspaceDir(fleetSession *fleet.FleetSession, plan *fleet.FleetPlan) {
	if plan != nil && plan.ContainerWorkspaceDir != "" {
		fleetSession.WorkspaceDir = plan.ContainerWorkspaceDir
	} else {
		fleetSession.WorkspaceDir = "/root"
	}
}

func materializeFleetBootstrapBackend(ctx context.Context, fleetBackend sandbox.Backend, sessionID, template string, tplRegistry *sandbox.TemplateRegistry) error {
	var tplStore store.SandboxTemplateStore
	if backend := getPlatformBackend(); backend != nil {
		tplStore = backend.SandboxTemplates()
	}
	files := sandbox.LookupBootstrapFiles(ctx, tplRegistry, tplStore, template)
	if len(files) == 0 {
		return nil
	}
	return sandbox.MaterializeBootstrapFiles(ctx, fleetBackend, sessionID, files)
}

func truncateID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
