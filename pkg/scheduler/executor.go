package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
)

// DefaultJobTimeout caps a single scheduled job execution so a hung adaptive
// run cannot pin the in-memory "running" lock forever and block re-fires.
const DefaultJobTimeout = 15 * time.Minute

// jobTimeout is the active execution timeout (overridable in tests).
var jobTimeout = DefaultJobTimeout

// RunHeadlessFunc is a function type for running a flow headlessly.
// It is injected by the daemon to avoid import cycles (scheduler -> launcher -> api).
type RunHeadlessFunc func(ctx context.Context, cfg *HeadlessRunConfig) (string, error)

// FleetPollFunc is a function type for polling a fleet plan's external channel.
// It is injected by the daemon to avoid import cycles (scheduler -> fleet).
// The planKey identifies which fleet plan to poll. Returns a result summary.
type FleetPollFunc func(ctx context.Context, planKey string) (string, error)

// FlowResolverFunc resolves a flow name to its raw YAML content.
// In platform mode this reads from the team/personal FlowStore (PostgreSQL).
// Returns the YAML string and nil, or empty string and error if not found.
type FlowResolverFunc func(name string) (string, error)

// HeadlessRunConfig holds the parameters for a headless flow run.
// This mirrors launcher.HeadlessConfig but lives in the scheduler package
// to avoid the import cycle.
type HeadlessRunConfig struct {
	// FlowPath is the filesystem path to the flow YAML (personal mode).
	FlowPath string
	// FlowYAML is the raw YAML content of the flow (platform mode).
	// When set, FlowPath is ignored and the YAML is parsed directly.
	FlowYAML     string
	AppConfig    *config.AppConfig
	ProviderName string
	ModelName    string
	Parameters   map[string]string
	DebugMode    bool
}

// Executor runs scheduled jobs. It uses an injected headless runner for routine
// mode and the shared ChatAgent for adaptive mode.
type Executor struct {
	// ChatAgent is the shared agent for adaptive mode execution.
	ChatAgent *agent.ChatAgent
	// SessionService provides session persistence.
	SessionService session.Service
	// AppConfig holds provider and general settings.
	AppConfig *config.AppConfig
	// ProviderName is the default provider for execution.
	ProviderName string
	// ModelName is the default model for execution.
	ModelName string
	// DebugMode enables verbose logging.
	DebugMode bool
	// RunHeadless is the function to call for routine (flow) execution.
	// Injected by the daemon to avoid import cycles.
	RunHeadless RunHeadlessFunc
	// FleetPoll is the function to call for fleet_poll mode execution.
	// Injected by the daemon to avoid import cycles.
	FleetPoll FleetPollFunc
	// FlowResolver resolves a flow name to raw YAML from the platform store.
	// When set (platform mode), routine jobs use this instead of filesystem resolution.
	// When nil (personal mode), resolveFlowPath() is used as the fallback.
	FlowResolver FlowResolverFunc
	// GatewayConfig enables OpenShell network-policy pre-seed and PolicyAllow
	// auto-approve during adaptive runs (parity with Studio ChatRunner).
	GatewayConfig *openshell.GRPCClientConfig
	// ReadSessionFile reads a path from the adaptive sandbox (or host). Used
	// when write_file content was not captured from tool args but a report
	// fence references a file. Optional; delivery still works via last-wins
	// text and in-loop write_file content capture.
	ReadSessionFile func(sessionID, path string) ([]byte, error)
	// DestroySandbox tears down the OpenShell/K8s sandbox for a session ID.
	// Adaptive jobs call this at the start and end of each run so sandboxes
	// are ephemeral (create → work → destroy) and never reuse a stale pod.
	// Optional; when nil, adaptive runs keep legacy long-lived sandboxes.
	DestroySandbox func(ctx context.Context, sessionID string) error
	// InvalidateSandboxClient drops the ToolNodePool client for sessionID
	// so the next EnsureReady provisions a fresh bind after DestroySandbox.
	// Optional but required for OpenShell backendPool (bound=true sticks otherwise).
	InvalidateSandboxClient func(sessionID string)
}

// Execute runs a job based on its mode and returns the result text.
// A DefaultJobTimeout is applied so hung LLM/tool loops cancel cleanly.
func (e *Executor) Execute(ctx context.Context, job *Job) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	var result string
	var err error
	switch job.Mode {
	case ModeRoutine:
		result, err = e.executeRoutine(ctx, job)
	case ModeAdaptive:
		result, err = e.executeAdaptive(ctx, job)
	case ModeFleetPoll:
		result, err = e.executeFleetPoll(ctx, job)
	default:
		return "", fmt.Errorf("unknown job mode: %s", job.Mode)
	}
	if ctx.Err() == context.DeadlineExceeded {
		if err != nil {
			return result, fmt.Errorf("job %q timed out after %s: %w", job.Name, jobTimeout, err)
		}
		return result, fmt.Errorf("job %q timed out after %s", job.Name, jobTimeout)
	}
	return result, err
}

// executeRoutine runs a flow through the headless flow engine.
func (e *Executor) executeRoutine(ctx context.Context, job *Job) (string, error) {
	if job.Payload.Flow == "" {
		return "", fmt.Errorf("routine job %q has no flow specified", job.Name)
	}
	if e.RunHeadless == nil {
		return "", fmt.Errorf("headless runner not configured")
	}

	// Gateway on ctx so NodeTool PreSeeds DB allow rules before first Call
	// (NetworkPolicyStores are injected by MultiTenantScheduler / callers).
	if e.GatewayConfig != nil {
		ctx = netpolicy.WithGatewayConfig(ctx, e.GatewayConfig)
	}

	cfg := &HeadlessRunConfig{
		AppConfig:    e.AppConfig,
		ProviderName: e.ProviderName,
		ModelName:    e.ModelName,
		Parameters:   job.Payload.Params,
		DebugMode:    e.DebugMode,
	}

	// Multi-tenant platform path: FlowStore injected into context by the
	// multi-tenant scheduler tick loop (per-team). This takes priority over
	// the single-team FlowResolver closure.
	if fs := store.FlowStoreFromContext(ctx); fs != nil {
		yamlContent, err := fs.GetFlow(ctx, job.Payload.Flow)
		if err != nil {
			return "", fmt.Errorf("flow %q not found in team store: %w", job.Payload.Flow, err)
		}
		cfg.FlowYAML = yamlContent
		return e.RunHeadless(ctx, cfg)
	}

	// Platform mode (legacy single-team path): resolve flow YAML from the
	// closure captured at executor construction time.
	if e.FlowResolver != nil {
		yamlContent, err := e.FlowResolver(job.Payload.Flow)
		if err != nil {
			return "", fmt.Errorf("flow %q not found in store: %w", job.Payload.Flow, err)
		}
		cfg.FlowYAML = yamlContent
		return e.RunHeadless(ctx, cfg)
	}

	// Personal mode: resolve flow from the filesystem.
	agentPath, err := resolveFlowPath(job.Payload.Flow)
	if err != nil {
		return "", fmt.Errorf("flow %q not found: %w", job.Payload.Flow, err)
	}
	cfg.FlowPath = agentPath

	return e.RunHeadless(ctx, cfg)
}

// executeAdaptive sends stored instructions as a chat message through the ChatAgent.
func (e *Executor) executeAdaptive(ctx context.Context, job *Job) (string, error) {
	if job.Payload.Instructions == "" {
		return "", fmt.Errorf("adaptive job %q has no instructions", job.Name)
	}

	if e.ChatAgent == nil {
		return "", fmt.Errorf("no ChatAgent available for adaptive execution (enable channels to use adaptive mode)")
	}

	// Inject scheduler-specific output constraints via context (thread-safe).
	// Run() clones the SystemPromptBuilder and applies these overrides on the clone.
	ctx = agent.WithPromptOverrides(ctx, &agent.PromptOverrides{
		SchedulerHints: `You are executing a SCHEDULED TASK automatically. Your output will be delivered directly as a notification.
CRITICAL RULES:
- Output ONLY the requested data in the format specified by the instructions
- Do NOT add preamble, greetings, or explain what you are doing
- Do NOT mention saved workflows, flows, or execution plans you found
- Do NOT add conversational filler like "Here's what I found" or "Let me check"
- Do NOT add follow-up questions or offers to help
- Just execute the task and return the formatted result`,
	})

	// Create a per-job session key for isolation.
	// Use dashes (not colons) — session IDs flow into Kubernetes label values
	// which only allow [a-z0-9A-Z._-].
	sessionKey := fmt.Sprintf("scheduler-adaptive-%s", job.ID)
	// Personal jobs run as the owning user so personal sessions/credentials apply.
	// Team/headless jobs use SystemUserID.
	userID := store.SystemUserID
	if uid := store.UserIDFromContext(ctx); uid != "" {
		userID = uid
	} else if job.Scope == "personal" && job.OwnerID != "" {
		userID = job.OwnerID
	}
	appName := "astonish"

	// Get or create ADK session (stable per job for transcript continuity).
	sess, err := getOrCreateSession(ctx, e.SessionService, appName, userID, sessionKey)
	if err != nil {
		return "", fmt.Errorf("session error: %w", err)
	}

	// Ephemeral OpenShell sandbox: clear any stale pod/registry row, then
	// destroy again when the run finishes so the next tick starts clean.
	cleanupSandbox := e.ensureEphemeralAdaptiveSandbox(ctx, sess.ID())
	defer cleanupSandbox()

	// Create ADK agent wrapper for this execution
	adkAgent, err := adkagent.New(adkagent.Config{
		Name:        "astonish_scheduler",
		Description: "Astonish scheduled adaptive task",
		Run:         e.ChatAgent.Run,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create ADK agent: %w", err)
	}

	// Inject Redactor into context so memory_save can Placeholderize()
	if e.ChatAgent.Redactor != nil {
		ctx = credentials.WithRedactor(ctx, e.ChatAgent.Redactor)
	}
	// Gateway on ctx so NodeTool PreSeeds DB allow rules before first Call.
	if e.GatewayConfig != nil {
		ctx = netpolicy.WithGatewayConfig(ctx, e.GatewayConfig)
	}

	// Create runner
	r, err := runner.New(runner.Config{
		AppName:        appName,
		Agent:          adkAgent,
		SessionService: e.SessionService,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create runner: %w", err)
	}

	// Send instructions as user message (with absolute timestamp for temporal
	// context; see agent.NewTimestampedUserContent for cache-stability rationale).
	userContent := agent.NewTimestampedUserContent(job.Payload.Instructions)

	// Headless network-policy bridge: auto-approve PolicyAllow denials.
	// Primary PreSeed runs in NodeTool before the first Call.
	netBridge := &netpolicy.SessionBridge{
		GatewayCfg: e.GatewayConfig,
		SessionID:  sess.ID(),
		Stores:     store.NetworkPolicyStoresFromContext(ctx),
	}
	pendingToolURLs := map[string]string{}
	writtenFiles := map[string]string{}
	var lastWins string

	for event, err := range r.Run(ctx, userID, sess.ID(), userContent, adkagent.RunConfig{}) {
		if err != nil {
			return finalizeAdaptiveResult(e, sess.ID(), lastWins, writtenFiles), fmt.Errorf("agent error: %w", err)
		}

		if agent.IsTurnContextEvent(event) || event.LLMResponse.Content == nil {
			continue
		}
		// Skip streaming partials — wait for complete turns (email batch semantics).
		if event.LLMResponse.Partial {
			continue
		}

		for _, part := range event.LLMResponse.Content.Parts {
			if part.FunctionCall != nil {
				if part.FunctionCall.Name == "write_file" {
					captureWriteFileContent(writtenFiles, part.FunctionCall.Args)
				}
				if urlArg, ok := part.FunctionCall.Args["url"].(string); ok && urlArg != "" {
					if part.FunctionCall.ID != "" {
						pendingToolURLs[part.FunctionCall.ID] = urlArg
					}
					pendingToolURLs[part.FunctionCall.Name] = urlArg
				}
			}
			if part.FunctionResponse != nil {
				fallbackURL := ""
				if part.FunctionResponse.ID != "" {
					fallbackURL = pendingToolURLs[part.FunctionResponse.ID]
				}
				if fallbackURL == "" {
					fallbackURL = pendingToolURLs[part.FunctionResponse.Name]
				}
				netBridge.OnToolResult(ctx, part.FunctionResponse.Name, part.FunctionResponse.Response, fallbackURL)
			}
		}

		turnText := extractUserFacingText(event.LLMResponse.Content.Parts)
		lastWins = applyLastWinsTurn(lastWins, turnText)
	}

	return finalizeAdaptiveResult(e, sess.ID(), lastWins, writtenFiles), nil
}

// finalizeAdaptiveResult picks report file body over mid-run narration / bare fences.
func finalizeAdaptiveResult(e *Executor, sessionID, lastWins string, written map[string]string) string {
	var drained []agent.FileArtifact
	if e != nil && e.ChatAgent != nil {
		drained = e.ChatAgent.DrainFiles()
	}
	var readFile func(path string) ([]byte, error)
	if e != nil && e.ReadSessionFile != nil && sessionID != "" {
		readFile = func(path string) ([]byte, error) {
			return e.ReadSessionFile(sessionID, path)
		}
	}
	return preferDeliveryBody(lastWins, written, drained, readFile)
}

// getOrCreateSession retrieves or creates a session by key.
func getOrCreateSession(ctx context.Context, sessSvc session.Service, appName, userID, sessionKey string) (session.Session, error) {
	getResp, err := sessSvc.Get(ctx, &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionKey,
	})
	if err == nil && getResp.Session != nil {
		return getResp.Session, nil
	}

	createResp, err := sessSvc.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionKey,
	})
	if err != nil {
		return nil, err
	}
	return createResp.Session, nil
}

// ensureEphemeralAdaptiveSandbox destroys any prior sandbox for sessionID and
// returns a cleanup that destroys again after the adaptive run. Best-effort:
// destroy errors are ignored so a missing sandbox does not block the run.
// Also invalidates the ToolNodePool client so the next EnsureReady rebinds.
func (e *Executor) ensureEphemeralAdaptiveSandbox(ctx context.Context, sessionID string) (cleanup func()) {
	noop := func() {}
	if e == nil || sessionID == "" {
		return noop
	}
	if e.DestroySandbox == nil && e.InvalidateSandboxClient == nil {
		return noop
	}
	e.destroyAndInvalidateAdaptiveSandbox(ctx, sessionID)
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		e.destroyAndInvalidateAdaptiveSandbox(cleanupCtx, sessionID)
	}
}

func (e *Executor) destroyAndInvalidateAdaptiveSandbox(ctx context.Context, sessionID string) {
	if e.DestroySandbox != nil {
		_ = e.DestroySandbox(ctx, sessionID)
	}
	if e.InvalidateSandboxClient != nil {
		e.InvalidateSandboxClient(sessionID)
	}
}

// resolveFlowPath resolves a flow name to its filesystem path using the same
// resolution chain as the CLI (agents dir, flows dir, etc.).
func resolveFlowPath(name string) (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}

	// Try direct path
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}

	// Try with .yaml extension
	withYAML := name + ".yaml"
	if _, err := os.Stat(withYAML); err == nil {
		return withYAML, nil
	}

	// Try agents directory
	agentsPath := filepath.Join(configDir, "agents", name+".yaml")
	if _, err := os.Stat(agentsPath); err == nil {
		return agentsPath, nil
	}

	// Try flows directory
	flowsPath := filepath.Join(configDir, "flows", name+".yaml")
	if _, err := os.Stat(flowsPath); err == nil {
		return flowsPath, nil
	}

	return "", fmt.Errorf("flow %q not found in any search path", name)
}

// executeFleetPoll delegates to the injected FleetPollFunc.
// The job's Payload.Flow field holds the fleet plan key to poll.
func (e *Executor) executeFleetPoll(ctx context.Context, job *Job) (string, error) {
	planKey := job.Payload.Flow
	if planKey == "" {
		return "", fmt.Errorf("fleet_poll job %q has no plan key (Payload.Flow)", job.Name)
	}
	if e.FleetPoll == nil {
		return "", fmt.Errorf("fleet poll function not configured")
	}
	return e.FleetPoll(ctx, planKey)
}
