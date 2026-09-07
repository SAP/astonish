package tools

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/browser"
	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// InjectDrillCredentialsArgs are the arguments for inject_drill_credentials.
type InjectDrillCredentialsArgs struct {
	SuiteName string `json:"suite_name" jsonschema:"Name of the drill suite whose credentials/credential_injection to materialize into the sandbox"`
}

// InjectDrillCredentialsResult is the result of inject_drill_credentials.
type InjectDrillCredentialsResult struct {
	Status  string `json:"status"`  // "ok", "skipped", "error"
	Message string `json:"message"` // Human-readable detail
}

// NewInjectDrillCredentialsTool creates inject_drill_credentials for chat/Studio.
// Call this during Studio prep BEFORE start-services so apps that read secrets
// at boot see them. Safe to call again; run_drill also injects before tests.
func NewInjectDrillCredentialsTool(nodePool *sandbox.NodeClientPool, tplRegistry *sandbox.TemplateRegistry, browserMgr *browser.Manager) (tool.Tool, error) {
	deps := &runDrillDeps{
		nodePool:         nodePool,
		templateRegistry: tplRegistry,
		browserMgr:       browserMgr,
	}
	return newInjectDrillCredentialsToolFromDeps(deps)
}

// NewInjectDrillCredentialsToolWithPool creates inject_drill_credentials for
// chat/Studio sessions backed by a backend-agnostic ToolNodePool.
func NewInjectDrillCredentialsToolWithPool(pool sandbox.ToolNodePool, tplRegistry *sandbox.TemplateRegistry, browserMgr *browser.Manager) (tool.Tool, error) {
	deps := &runDrillDeps{
		toolPool:         pool,
		templateRegistry: tplRegistry,
		browserMgr:       browserMgr,
	}
	return newInjectDrillCredentialsToolFromDeps(deps)
}

// NewInjectDrillCredentialsToolWithClient creates inject_drill_credentials for Incus fleet sessions.
func NewInjectDrillCredentialsToolWithClient(lazyClient *sandbox.LazyNodeClient, sessionID string, browserMgr *browser.Manager) (tool.Tool, error) {
	deps := &runDrillDeps{
		lazyClient: lazyClient,
		sessionID:  sessionID,
		browserMgr: browserMgr,
	}
	return newInjectDrillCredentialsToolFromDeps(deps)
}

// NewInjectDrillCredentialsToolWithToolClient creates inject_drill_credentials for backend-agnostic fleet sessions.
func NewInjectDrillCredentialsToolWithToolClient(client sandbox.ToolNodeClient, sessionID string, browserMgr *browser.Manager) (tool.Tool, error) {
	deps := &runDrillDeps{
		toolClient: client,
		sessionID:  sessionID,
		browserMgr: browserMgr,
	}
	return newInjectDrillCredentialsToolFromDeps(deps)
}

func newInjectDrillCredentialsToolFromDeps(deps *runDrillDeps) (tool.Tool, error) {
	fn := func(ctx tool.Context, args InjectDrillCredentialsArgs) (InjectDrillCredentialsResult, error) {
		return executeInjectDrillCredentials(ctx, deps, args)
	}
	return functiontool.New(functiontool.Config{
		Name: "inject_drill_credentials",
		Description: "Materialize a drill suite's credential_injection (or fleet-plan fallback) " +
			"into the active sandbox. Call this during Studio prep BEFORE start-services when " +
			"the suite declares credentials — apps that read secrets only at process boot must " +
			"see files before the backend starts. Safe to call again; run_drill also injects " +
			"before tests. Do not write secret files with write_file.",
	}, fn)
}

func executeInjectDrillCredentials(ctx tool.Context, deps *runDrillDeps, args InjectDrillCredentialsArgs) (InjectDrillCredentialsResult, error) {
	suiteName := strings.TrimSpace(args.SuiteName)
	if suiteName == "" {
		return InjectDrillCredentialsResult{Status: "error", Message: "suite_name is required"}, nil
	}
	suiteName = strings.TrimSuffix(suiteName, ".yaml")
	suiteName = strings.TrimSuffix(suiteName, ".yml")

	fs := getDrillFlowStore(ctx)
	if fs == nil {
		return InjectDrillCredentialsResult{
			Status:  "error",
			Message: "Drill credential injection requires platform mode (team-scoped store not available)",
		}, nil
	}

	suite, err := adrill.LoadSuiteFromStore(fs, ctx, suiteName)
	if err != nil {
		return InjectDrillCredentialsResult{
			Status:  "error",
			Message: fmt.Sprintf("Suite %q not found: %v", suiteName, err),
		}, nil
	}

	if err := injectDrillCredentials(ctx, deps, suiteName, suite); err != nil {
		return InjectDrillCredentialsResult{
			Status:  "error",
			Message: fmt.Sprintf("credential injection failed: %v", err),
		}, nil
	}

	sc := suite.Config.SuiteConfig
	if !adrill.SuiteDeclaresCredentials(sc) {
		return InjectDrillCredentialsResult{
			Status:  "skipped",
			Message: fmt.Sprintf("Suite %q has no credentials/credential_injection declared (fleet-plan fallback applied if present). Safe to start services.", suiteName),
		}, nil
	}

	return InjectDrillCredentialsResult{
		Status:  "ok",
		Message: fmt.Sprintf("Credentials for suite %q injected into the sandbox. Start services next so processes boot with secrets already on disk.", suiteName),
	}, nil
}
