package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
	"github.com/SAP/astonish/pkg/provider"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/tools/ripgrep"
	"github.com/SAP/astonish/pkg/tui"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	adkagent "google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// codeAppName is the ADK app name used for local code-mode sessions. It is
// distinct from the Studio chat app name so the two never share session state.
const (
	codeAppName             = "astonish_code"
	planLifecycleStateKey   = "astonish_plan_lifecycle"
	planApprovalUserMessage = "I approve this plan. Please start implementing it now, phase by phase."
)

// codeUserID is the base local user for code mode (single-user, no auth).
// Code-mode sessions are scoped per working directory by deriving a
// directory-specific user ID from this base (see codeUserIDForDir), so
// `/sessions` in one project never lists sessions from another.
const codeUserID = "local_user"

// codeUserIDForDir returns a session "user ID" that is unique per working
// directory. Code mode persists sessions to a single on-disk FileStore, but
// scopes them per directory by encoding a stable hash of the absolute working
// directory into the user ID. FileStore.List filters by (appName, userID), so
// this naturally isolates each project's session history. The plain base user
// ID is returned as a fallback if the directory is empty.
func codeUserIDForDir(workingDir string) string {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		return codeUserID
	}
	sum := sha256.Sum256([]byte(dir))
	return codeUserID + "_" + hex.EncodeToString(sum[:8])
}

// CodeConfig configures the local, in-process code-mode TUI.
//
// Code mode is Astonish running as a local coding CLI (like Claude Code /
// opencode / grok): the single binary runs the agent loop in-process and calls
// the tools compiled into the binary directly against the host filesystem in
// the user's working directory. It never contacts a platform — there is no
// daemon, no HTTP, and no login.
type CodeConfig struct {
	// Provider/Model optionally pin the LLM for the session (e.g. "openai" /
	// "gpt-4o"). Empty values fall back to the configured cascade default.
	Provider string
	Model    string
	// WorkingDir is the directory tools operate against. Empty = os.Getwd().
	WorkingDir string
	// AutoApprove bypasses the per-tool approval prompt (Claude Code's --yolo).
	AutoApprove bool
	// DebugMode surfaces extra diagnostics in the TUI.
	DebugMode bool
	// SessionID resumes an existing in-process session (within a run).
	SessionID string
}

// RunCodeTUI launches the fullscreen terminal coding app fully in-process.
//
// Unlike RunChatTUI (which streams Studio SSE over an authenticated platform
// client), this builds the entire agent locally via NewWiredChatAgent and
// drives it with the ADK runner in the same process. The sandbox is forced off
// so the compiled-in tools execute directly on the host filesystem in the
// user's current working directory.
func RunCodeTUI(ctx context.Context, cfg *CodeConfig) error {
	if cfg == nil {
		cfg = &CodeConfig{}
	}

	// The TUI owns the terminal (bubbletea alt-screen). Any writes to the
	// standard logger or slog's default handler would corrupt the display —
	// notably ADK's runner, which uses log.Printf to warn "Event from an
	// unknown agent" for every event whose author differs from the root agent
	// name. Redirect both away from the terminal for the lifetime of the TUI.
	// In debug mode, send them to a log file so diagnostics are preserved.
	restoreLogs := redirectLogsForTUI(cfg.DebugMode)
	defer restoreLogs()

	appConfig, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Force host execution: code mode runs tools directly on the user's
	// machine in the CWD. Disabling the sandbox makes every filesystem/shell
	// tool resolve against the process working directory (Claude-Code
	// semantics). This is the single most important line in code mode.
	// Preserve the original sandbox.enabled value before overriding — it must
	// be restored when saving config to disk so the platform daemon (which
	// reads the same config.yaml) does not see sandbox as disabled.
	originalSandboxEnabled := appConfig.Sandbox.Enabled
	forceHostExecution(appConfig)

	// Ensure ripgrep is available for the code-search tools. ripgrep is far
	// superior to the pure-Go fallback (gitignore-aware, faster, richer
	// filters), so code mode provisions it: prefer a system rg, else download
	// the pinned build once and cache it. Done in the background so startup and
	// the first search are not blocked; ResolvePath memoizes the result.
	go func() {
		if _, rgErr := ripgrep.ResolvePath(); rgErr != nil {
			slog.Debug("ripgrep provisioning failed; grep_search will use the Go fallback", "error", rgErr)
		}
	}()

	// Resolve the LLM. Explicit CLI flags win; otherwise fall back to the
	// configured cascade default (general.default_provider / default_model in
	// ~/.config/astonish/config.yaml, written by `astonish setup` or the
	// in-TUI `/model` picker). Code mode may start with no provider at all —
	// the user can configure one from inside the app via `/model`.
	providerName := strings.TrimSpace(cfg.Provider)
	if providerName == "" {
		providerName = appConfig.General.DefaultProvider
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = appConfig.General.DefaultModel
	}

	workingDir := strings.TrimSpace(cfg.WorkingDir)
	if workingDir == "" {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return fmt.Errorf("failed to resolve working directory: %w", wdErr)
		}
		workingDir = wd
	} else {
		abs, absErr := filepath.Abs(workingDir)
		if absErr != nil {
			return fmt.Errorf("invalid working directory %q: %w", workingDir, absErr)
		}
		workingDir = abs
	}
	if info, statErr := os.Stat(workingDir); statErr != nil || !info.IsDir() {
		return fmt.Errorf("working directory does not exist or is not a directory: %s", workingDir)
	}
	// Change the process CWD so tools that default to os.Getwd() (grep_search,
	// find_files, shell_command) operate in the requested directory.
	if err := os.Chdir(workingDir); err != nil {
		return fmt.Errorf("failed to enter working directory %s: %w", workingDir, err)
	}

	// Create a persistent, on-disk session store for code mode. Sessions
	// survive process restarts and are scoped per working directory (via the
	// derived userID below), so `/sessions` in one project never lists another
	// project's history. This store is code-mode-specific (rooted in a "code"
	// subdirectory) so it never mixes with Studio/chat sessions. We pass it into
	// the factory via SessionService, overriding the factory's in-memory default.
	sessionsDir, err := codeSessionsDir(appConfig)
	if err != nil {
		return fmt.Errorf("failed to resolve code sessions directory: %w", err)
	}
	fileStore, err := persistentsession.NewFileStore(sessionsDir)
	if err != nil {
		return fmt.Errorf("failed to create code session store: %w", err)
	}

	// Checkpoint store: snapshots files before each turn writes/edits them so
	// /rollback can restore the working directory. Rooted next to the session
	// store so code-mode state stays self-contained.
	checkpointStore, err := persistentsession.NewCheckpointStore(sessionsDir)
	if err != nil {
		return fmt.Errorf("failed to create code checkpoint store: %w", err)
	}

	// Per-directory session scope: sessions created in this working directory
	// are stored under this userID and only listed here.
	scopedUserID := codeUserIDForDir(workingDir)

	result, err := NewWiredChatAgent(ctx, &ChatFactoryConfig{
		AppConfig:            appConfig,
		ProviderName:         providerName,
		ModelName:            modelName,
		DebugMode:            cfg.DebugMode,
		AutoApprove:          cfg.AutoApprove,
		WorkspaceDir:         workingDir,
		PlatformMode:         false,
		CodeMode:             true,
		AllowMissingProvider: true,
		LoadProjectContext:   true,
		SessionService:       fileStore,
	})
	if err != nil {
		return err
	}

	b := &localAgentBackend{
		result:                 result,
		sessionSvc:             common.NewAutoInitService(result.SessionService),
		fileStore:              fileStore,
		checkpoints:            checkpointStore,
		userID:                 scopedUserID,
		appConfig:              appConfig,
		originalSandboxEnabled: originalSandboxEnabled,
		sessionID:              cfg.SessionID,
		autoApprove:            cfg.AutoApprove,
		debug:                  cfg.DebugMode,
		workingDir:             workingDir,
		gitBranch:              gitutil.DetectBranch(workingDir),
		provider:               result.ProviderName,
		model:                  result.ModelName,
		configured:             result.ProviderConfigured,
		resumed:                cfg.SessionID != "",
		notices:                result.StartupNotices,
		filesystemSkills:       append([]skills.Skill(nil), result.FilesystemSkills...),
		subAgentAuthReqCh:      make(chan agent.SubAgentAuthRequest, 1),
		subAgentAuthRespCh:     make(chan agent.SubAgentAuthResponse, 1),
	}

	// If resuming an existing session, load the persisted title so the header
	// shows it immediately.
	if cfg.SessionID != "" && fileStore != nil {
		if t, err := fileStore.GetSessionTitle(ctx, cfg.SessionID); err == nil && t != "" {
			b.title = t
		}
	}

	// Attempt to provide the platform backend as an alt panel (Ctrl+Tab). This
	// is best-effort: if the user isn't logged in, altBackend is nil and the TUI
	// operates in single-backend mode with Ctrl+Tab as a no-op.
	var altBackend backend.Backend
	if pb := newPlatformBackend(); pb != nil {
		altBackend = pb
	}

	err = tui.Run(ctx, tui.Config{Backend: b, AltBackend: altBackend})
	if result.Cleanup != nil {
		result.Cleanup()
	}
	return err
}

// codeSessionsDir returns the on-disk directory for code-mode session storage.
// It is a "code" subdirectory of the configured sessions directory so code-mode
// sessions never mix with Studio/chat sessions (which use the parent directory).
func codeSessionsDir(appConfig *config.AppConfig) (string, error) {
	var sessCfg *config.SessionConfig
	if appConfig != nil {
		sessCfg = &appConfig.Sessions
	}
	base, err := config.GetSessionsDir(sessCfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "code"), nil
}

// buildCodeBackend constructs a fully-wired localAgentBackend for code mode.
// This is the core setup shared between RunCodeTUI (primary launch) and the
// lazyCodeBackend (alt backend from chat mode via Ctrl+\).
func buildCodeBackend(ctx context.Context, cfg *CodeConfig) (backend.Backend, error) {
	if cfg == nil {
		cfg = &CodeConfig{}
	}

	appConfig, err := config.LoadAppConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	originalSandboxEnabled := appConfig.Sandbox.Enabled
	forceHostExecution(appConfig)

	// Resolve provider/model.
	providerName := strings.TrimSpace(cfg.Provider)
	if providerName == "" {
		providerName = appConfig.General.DefaultProvider
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = appConfig.General.DefaultModel
	}

	workingDir := strings.TrimSpace(cfg.WorkingDir)
	if workingDir == "" {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", wdErr)
		}
		workingDir = wd
	} else {
		abs, absErr := filepath.Abs(workingDir)
		if absErr != nil {
			return nil, fmt.Errorf("invalid working directory %q: %w", workingDir, absErr)
		}
		workingDir = abs
	}

	// Detect the active git branch for footer display (best-effort; empty if not a repo).
	gitBranch := gitutil.DetectBranch(workingDir)

	sessionsDir, err := codeSessionsDir(appConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve code sessions directory: %w", err)
	}
	fileStore, err := persistentsession.NewFileStore(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create code session store: %w", err)
	}

	checkpointStore, err := persistentsession.NewCheckpointStore(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create code checkpoint store: %w", err)
	}

	scopedUserID := codeUserIDForDir(workingDir)

	result, err := NewWiredChatAgent(ctx, &ChatFactoryConfig{
		AppConfig:            appConfig,
		ProviderName:         providerName,
		ModelName:            modelName,
		DebugMode:            cfg.DebugMode,
		AutoApprove:          cfg.AutoApprove,
		WorkspaceDir:         workingDir,
		PlatformMode:         false,
		CodeMode:             true,
		AllowMissingProvider: true,
		LoadProjectContext:   true,
		SessionService:       fileStore,
	})
	if err != nil {
		return nil, err
	}

	b := &localAgentBackend{
		result:                 result,
		sessionSvc:             common.NewAutoInitService(result.SessionService),
		fileStore:              fileStore,
		checkpoints:            checkpointStore,
		userID:                 scopedUserID,
		appConfig:              appConfig,
		originalSandboxEnabled: originalSandboxEnabled,
		autoApprove:            cfg.AutoApprove,
		debug:                  cfg.DebugMode,
		workingDir:             workingDir,
		gitBranch:              gitBranch,
		provider:               result.ProviderName,
		model:                  result.ModelName,
		configured:             result.ProviderConfigured,
		notices:                result.StartupNotices,
		filesystemSkills:       append([]skills.Skill(nil), result.FilesystemSkills...),
		subAgentAuthReqCh:      make(chan agent.SubAgentAuthRequest, 1),
		subAgentAuthRespCh:     make(chan agent.SubAgentAuthResponse, 1),
	}
	return b, nil
}

// forceHostExecution disables the sandbox on the given config so code mode's
// compiled-in tools execute directly on the host filesystem in the working
// directory. Extracted as a helper so the invariant ("code mode never
// sandboxes") is unit-testable.
func forceHostExecution(appConfig *config.AppConfig) {
	if appConfig == nil {
		return
	}
	disabled := false
	appConfig.Sandbox.Enabled = &disabled
}

// saveAppConfig persists the in-memory AppConfig to disk while preserving the
// user's original sandbox.enabled preference. Code mode sets sandbox.enabled=false
// in-memory (via forceHostExecution) so tools run on the host, but this override
// must NOT leak to the config file — other processes (the platform daemon) share
// the same config.yaml and would incorrectly see sandbox as disabled.
func (b *localAgentBackend) saveAppConfig() error {
	// Temporarily restore the original sandbox.enabled value for serialization.
	b.appConfig.Sandbox.Enabled = b.originalSandboxEnabled
	err := config.SaveAppConfig(b.appConfig)
	// Re-apply the code-mode override so the in-memory config stays correct.
	disabled := false
	b.appConfig.Sandbox.Enabled = &disabled
	return err
}

// formatCodeTokens renders a token count compactly (e.g. 1523 → "1.5k") for
// user-facing compaction notices.
func formatCodeTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// redirectLogsForTUI points the standard logger and slog's default handler away
// from the terminal so background log lines (e.g. ADK's "unknown agent"
// warnings) cannot corrupt the bubbletea alt-screen. It returns a function that
// restores the previous logging configuration.
//
// When debug is true, logs are written to <configDir>/code-debug.log so they
// remain available for troubleshooting; otherwise they are discarded.
func redirectLogsForTUI(debug bool) func() {
	prevLogOut := log.Writer()
	prevLogFlags := log.Flags()
	prevSlog := slog.Default()

	var sink io.Writer = io.Discard
	var file *os.File
	if debug {
		if dir, err := config.GetConfigDir(); err == nil {
			if f, ferr := os.OpenFile(filepath.Join(dir, "code-debug.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
				file = f
				sink = f
			}
		}
	}

	log.SetOutput(sink)
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, nil)))

	return func() {
		log.SetOutput(prevLogOut)
		log.SetFlags(prevLogFlags)
		slog.SetDefault(prevSlog)
		if file != nil {
			_ = file.Close()
		}
	}
}

// localAgentBackend implements backend.Backend by driving the wired ChatAgent
// in-process with the ADK runner. It reuses the exact (type, data) event
// payloads produced by Studio's ChatRunner and feeds them through the shared
// mapSSEToEvents translator, so the TUI renders identically to platform mode
// with no new mapping code.
type localAgentBackend struct {
	result     *ChatFactoryResult
	sessionSvc session.Service
	// fileStore is the persistent, on-disk session store backing sessionSvc.
	// Held separately so ListSessions can enumerate session metadata (titles,
	// counts, timestamps) via ListSessionMetas, which is not part of the ADK
	// session.Service interface.
	fileStore *persistentsession.FileStore
	// checkpoints snapshots files before each turn modifies them, enabling
	// /rollback to restore the working directory. Nil disables file rollback
	// (chat-only), e.g. in tests that construct the backend directly.
	checkpoints *persistentsession.CheckpointStore
	// userID scopes sessions to the current working directory. All session
	// service calls use this instead of the bare codeUserID so `/sessions`
	// only lists sessions created in this directory.
	userID    string
	appConfig *config.AppConfig
	// originalSandboxEnabled preserves the user's sandbox.enabled preference
	// from config.yaml before forceHostExecution() overrides it to &false for
	// code mode. When saving config to disk, we restore this value so the
	// persisted file does not leak the code-mode override to other processes
	// (e.g., the platform daemon) that share the same config file.
	originalSandboxEnabled *bool

	debug      bool
	workingDir string
	gitBranch  string
	notices    []string
	// filesystemSkills is the exact initialization-time slice wired into the
	// runtime skill lookup. It is not reloaded when /skills is invoked.
	filesystemSkills []skills.Skill

	mu          sync.Mutex
	sessionID   string
	autoApprove bool
	provider    string
	model       string
	configured  bool
	usage       *events.Usage
	// contextTokens is the current context-window occupancy (estimated from the
	// session contents when the provider reports no usage). Set on resume so the
	// header shows real utilization immediately, updated after each turn.
	contextTokens int64
	// lastCtxEstimate throttles the mid-turn context re-estimation so a turn
	// with many tool calls does not reload+re-scan the whole session on every
	// step. See maybeEmitEstimatedContext.
	lastCtxEstimate time.Time
	resumed         bool
	closed          bool
	title           string
	// needsRebuild is set when web search configuration changes. The next
	// NewSession() call will rebuild the agent so new tools are loaded.
	needsRebuild bool

	// Sub-agent authorization gate channels. When a sub-agent needs user
	// authorization, it sends a request on subAgentAuthReqCh and blocks
	// waiting for a response on subAgentAuthRespCh. The TUI surfaces the
	// request as an approval overlay. These are created once at backend init
	// and reused across turns (buffered channels, size 1).
	subAgentAuthReqCh  chan agent.SubAgentAuthRequest
	subAgentAuthRespCh chan agent.SubAgentAuthResponse
	// Sub-agent authorization is serialized end-to-end. Only one child may own
	// the approval overlay and response channel at a time.
	subAgentAuthMu      sync.Mutex
	subAgentAuthPending bool
}

func (b *localAgentBackend) ListLocalSkills(ctx context.Context) ([]backend.SkillSummary, error) {
	_ = ctx
	// Re-scan the filesystem on every call so skills added or removed since
	// startup are immediately visible without restarting the process.
	var liveSkills []skills.Skill
	if b.appConfig != nil && b.appConfig.Skills.IsSkillsEnabled() {
		loaded, err := skills.LoadSkills(
			b.appConfig.Skills.GetUserSkillsDir(),
			b.appConfig.Skills.ExtraDirs,
			b.appConfig.Skills.Allowlist,
		)
		if err == nil {
			liveSkills = loaded
		} else {
			// Fall back to the startup snapshot rather than surfacing an error.
			liveSkills = b.filesystemSkills
		}
	} else {
		liveSkills = b.filesystemSkills
	}

	merged := make(map[string]skills.Skill)
	for _, skill := range skills.BuiltinSkillsForCode() {
		merged[strings.ToLower(skill.Name)] = skill
	}
	for _, skill := range liveSkills {
		merged[strings.ToLower(skill.Name)] = skill
	}

	summaries := make([]backend.SkillSummary, 0, len(merged))
	for _, skill := range merged {
		missing := skill.MissingRequirements()
		summaries = append(summaries, backend.SkillSummary{
			Name:        skill.Name,
			Description: skill.Description,
			Source:      skill.Source,
			Eligible:    len(missing) == 0,
			Missing:     append([]string(nil), missing...),
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		left, right := strings.ToLower(summaries[i].Name), strings.ToLower(summaries[j].Name)
		if left == right {
			return summaries[i].Name < summaries[j].Name
		}
		return left < right
	})
	return summaries, nil
}

var _ backend.LocalSkillsBackend = (*localAgentBackend)(nil)

// contextEstimateInterval is the minimum wall-clock gap between mid-turn
// context-occupancy estimates. It keeps the header's "Context" figure moving
// within a long turn without re-scanning the session on every tool step.
const contextEstimateInterval = 750 * time.Millisecond

func (b *localAgentBackend) Info() backend.Info {
	b.mu.Lock()
	defer b.mu.Unlock()
	notices := append([]string(nil), b.notices...)
	if !b.configured {
		notices = append(notices, "No AI model configured yet. Type /model to choose a provider and model.")
	}
	return backend.Info{
		SessionID:     b.sessionID,
		Provider:      b.provider,
		Model:         b.model,
		Mode:          "code",
		WorkingDir:    b.workingDir,
		GitBranch:     b.gitBranch,
		Usage:         cloneUsage(b.usage),
		ContextTokens: b.contextTokens,
		IsResumed:     b.resumed,
		AutoApprove:   b.autoApprove,
		Notices:       notices,
		Title:         b.title,
	}
}

func (b *localAgentBackend) Open(ctx context.Context) error {
	_ = ctx
	return nil
}

func (b *localAgentBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

// RespondSubAgentAuth sends the user's authorization decision back to a blocked
// sub-agent. Returns true only when a sub-agent goroutine is currently blocked
// waiting for a decision (i.e., the approval overlay was raised by a sub-agent,
// not the main thread). When it returns true the caller must NOT call RunTurn —
// the sub-agent resumes on its own. When it returns false the approval belongs
// to the main thread and the caller drives the decision via RunTurn(choice).
//
// The pending flag (set under mu by SubAgentAuthGate before it blocks) is the
// only reliable discriminator: the response channel is buffered, so a bare
// non-blocking send would spuriously succeed for a main-thread approval and
// freeze the turn (the main thread waits on RunTurn that never comes). Grant
// bookkeeping for "Always Allow" is applied by the sub-agent gate in
// sub_agent.go once the response is received.
func (b *localAgentBackend) RespondSubAgentAuth(choice string) bool {
	b.mu.Lock()
	if !b.subAgentAuthPending {
		b.mu.Unlock()
		return false
	}
	// Claim this request before sending so repeated key events cannot deliver a
	// second decision or be mistaken for another queued child.
	b.subAgentAuthPending = false
	respCh := b.subAgentAuthRespCh
	b.mu.Unlock()

	respCh <- agent.SubAgentAuthResponse{
		Granted: isGrantChoice(choice),
		Choice:  choice,
	}
	return true
}

// isGrantChoice returns true if the user's choice text represents a grant
// (Allow / Always Allow / yes / 1 / 2) rather than a denial.
func isGrantChoice(choice string) bool {
	norm := agent.NormalizeAuthChoice(choice)
	return norm != "deny"
}

// effectiveUserID returns the per-directory session user ID, falling back to
// the base codeUserID when unset (e.g. tests that construct the backend
// directly). All session service calls route through this.
func (b *localAgentBackend) effectiveUserID() string {
	if b.userID != "" {
		return b.userID
	}
	return codeUserID
}

// planFilePath returns the per-session PLAN.md sidecar path, alongside the
// session transcript (<sessionID>.jsonl). Returns "" when the file store or
// session ID is unavailable (e.g. chat-only test backends), which disables
// plan-file persistence.
func (b *localAgentBackend) planFilePath(sessionID string) string {
	if b.fileStore == nil || sessionID == "" {
		return ""
	}
	return filepath.Join(b.fileStore.BaseDir(), codeAppName, b.effectiveUserID(), sessionID+".PLAN.md")
}

// removePlanFile deletes the per-session PLAN.md sidecar for sessionID, if one
// exists. Best-effort: a missing file (or disabled persistence) is not an error.
func (b *localAgentBackend) removePlanFile(sessionID string) {
	path := b.planFilePath(sessionID)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Debug("failed to remove PLAN.md sidecar", "component", "localAgentBackend", "path", path, "error", err)
	}
}

// ActivePlanFilePath implements backend.PlanBackend. It returns the absolute
// path of the active session's PLAN.md sidecar, or "" if no session is active
// or plan-file persistence is not configured.
func (b *localAgentBackend) ActivePlanFilePath() string {
	b.mu.Lock()
	sid := b.sessionID
	b.mu.Unlock()
	return b.planFilePath(sid)
}

// shouldContinueApprovedPlan reports whether a Normal turn should keep executing
// an already-approved PLAN.md (research ceilings, inlined plan, no replacement).
func (b *localAgentBackend) shouldContinueApprovedPlan(ctx context.Context, sessionID, planPath string, chatAgent *agent.ChatAgent) bool {
	if planPath == "" {
		return false
	}
	if _, err := os.Stat(planPath); err != nil {
		return false
	}
	if chatAgent != nil && chatAgent.IsActivePlanApproved() {
		return true
	}
	return b.sessionPlanLifecycle(ctx, sessionID) == events.PlanApproved
}

func (b *localAgentBackend) sessionPlanLifecycle(ctx context.Context, sessionID string) events.PlanStatus {
	if sessionID == "" || b.sessionSvc == nil {
		return ""
	}
	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{AppName: codeAppName, UserID: b.effectiveUserID(), SessionID: sessionID})
	if err != nil || resp == nil || resp.Session == nil {
		return ""
	}
	val, err := resp.Session.State().Get(planLifecycleStateKey)
	if err != nil {
		return ""
	}
	s, _ := val.(string)
	return events.PlanStatus(s)
}

// ensureSession creates a new in-process session if none is active and returns
// its ID. Safe to call under no lock; it locks internally.
func (b *localAgentBackend) ensureSession(ctx context.Context) (string, bool, error) {
	b.mu.Lock()
	id := b.sessionID
	b.mu.Unlock()
	if id != "" {
		return id, false, nil
	}
	resp, err := b.sessionSvc.Create(ctx, &session.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
	})
	if err != nil {
		return "", false, fmt.Errorf("failed to create session: %w", err)
	}
	newID := resp.Session.ID()
	b.mu.Lock()
	b.sessionID = newID
	b.mu.Unlock()
	return newID, true, nil
}

// RunTurn drives one agent turn in-process and streams TUI events.
//
// The heart of code mode: mirror the ChatRunner driver loop from
// pkg/api/chat_runner.go, but instead of buffering SSE for HTTP subscribers,
// translate each (type, data) payload into events.Event via mapSSEToEvents and
// push it onto the returned channel. Approvals use the same state-delta +
// turn-suspension protocol the TUI already implements: when the agent yields
// awaiting_approval it emits an approval event and ends the turn; the TUI
// calls RunTurn again with the user's Yes/No as the next message.
func (b *localAgentBackend) RunTurn(ctx context.Context, message string, opts backend.TurnOptions) (<-chan events.Event, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("backend closed")
	}
	autoApprove := b.autoApprove
	configured := b.configured
	b.mu.Unlock()

	// No provider yet: don't attempt a turn (the placeholder LLM would only
	// error). Guide the user to the /model picker via a one-shot system notice.
	if !configured {
		out := make(chan events.Event, 1)
		out <- events.NewSystem("No AI model is configured. Type /model to choose a provider and model, then try again.")
		close(out)
		return out, nil
	}

	sessionID, isNew, err := b.ensureSession(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan events.Event, 64)
	var outClosed atomic.Bool

	// emit converts one (type, data) payload — exactly the shape ChatRunner
	// produces — into TUI events and pushes them onto out. This is the Option B
	// bridge: one shared translator (mapSSEToEvents), zero duplicated mapping.
	//
	// Safe to call from any goroutine, including after the turn goroutine has
	// closed `out`. Late callers (sub-agent progress callbacks firing after
	// context cancellation) are silently discarded.
	emit := func(eventType string, data map[string]any) {
		if outClosed.Load() {
			return
		}
		raw, mErr := json.Marshal(data)
		if mErr != nil {
			return
		}
		sev := &client.SSEEvent{Type: eventType, Data: string(raw)}
		for _, ev := range mapSSEToEvents(sev, b.debug) {
			if ev.Kind == events.KindUsage && ev.Usage != nil && !ev.Usage.Estimated {
				b.mu.Lock()
				b.usage = addUsage(b.usage, ev.Usage)
				b.mu.Unlock()
			}
			if ev.Kind == events.KindModelChanged {
				b.setModel(ev.Provider, ev.Model)
			}
			// Safe send: recover from the narrow TOCTOU race where
			// outClosed is read as false but close(out) fires before
			// the send executes. This can happen when a sub-agent's
			// deferred progress emission races with context cancellation.
			func() {
				defer func() { recover() }() //nolint:errcheck // intentional panic suppression
				select {
				case out <- ev:
				case <-ctx.Done():
				}
			}()
			if outClosed.Load() {
				return
			}
		}
	}

	agentInst, err := adkagent.New(adkagent.Config{
		Name:        codeAppName,
		Description: "Astonish local coding agent",
		Run:         b.result.ChatAgent.Run,
	})
	if err != nil {
		close(out)
		return nil, fmt.Errorf("failed to create agent: %w", err)
	}
	rnr, err := runner.New(runner.Config{
		AppName:        codeAppName,
		Agent:          agentInst,
		SessionService: b.sessionSvc,
	})
	if err != nil {
		close(out)
		return nil, fmt.Errorf("failed to create runner: %w", err)
	}

	chatAgent := b.result.ChatAgent
	chatAgent.AutoApprove = autoApprove
	// Persist any announced plan to a per-session PLAN.md sidecar so the plan
	// survives context compaction (the model can re-read it after a summary).
	planPath := b.planFilePath(sessionID)
	chatAgent.SetPlanFilePath(planPath)
	if chatAgent.Compactor != nil {
		chatAgent.Compactor.SetPlanFilePath(planPath)
		// Surface compaction to the user: a transcript notice plus an estimated
		// usage reading so the context figure in the header drops to the new
		// (post-compaction) size. Without this, compaction is invisible and the
		// header keeps showing the pre-compaction peak.
		chatAgent.Compactor.SetOnCompaction(func(beforeTokens, afterTokens int) {
			emit("system", map[string]any{
				"content": fmt.Sprintf("Compacted context: %s → %s tokens.", formatCodeTokens(beforeTokens), formatCodeTokens(afterTokens)),
			})
			emit("usage", map[string]any{
				"input_tokens": afterTokens,
				"estimated":    true,
			})
			b.mu.Lock()
			b.contextTokens = int64(afterTokens)
			b.mu.Unlock()
		})
		// Persist mid-turn (and any) BeforeModelCallback compaction into the
		// session store so the next model step rebuilds from the summary — not
		// the full pre-compaction transcript (session ecff74b2: 8k→190k loop).
		if b.fileStore != nil {
			userID := b.effectiveUserID()
			chatAgent.Compactor.SetPersistCompacted(func(pctx context.Context, sid string, compacted []*genai.Content) error {
				evs := contentsToSessionEvents(compacted)
				if len(evs) == 0 {
					return nil
				}
				_, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, userID, sid, evs)
				return err
			})
		}
	}
	// Wire structured sub-task progress so delegation events flow to the TUI.
	// This enables the delegation panel that shows task timers and completion status.
	chatAgent.SubTaskProgressCallback = func(evt agent.SubTaskProgressEvent) {
		switch evt.Type {
		case "plan_announced":
			// Plan content is rendered by the FunctionResponse handler for
			// announce_plan; no text emission needed here to avoid duplication.
		case "delegation_start":
			tasks := make([]any, len(evt.Tasks))
			for i, t := range evt.Tasks {
				tasks[i] = map[string]any{"name": t.Name, "description": t.Description, "plan_step": t.PlanStep}
			}
			emit("delegation", map[string]any{"type": "start", "tasks": tasks})
		case "task_start":
			emit("delegation", map[string]any{"type": "task_start", "task_name": evt.TaskName, "status": evt.Status, "attempt": evt.Attempt})
		case "task_state", "task_retry":
			emit("delegation", map[string]any{
				"type": evt.Type, "task_name": evt.TaskName, "status": evt.Status,
				"duration": evt.Duration, "last_activity": evt.LastActivity,
				"attempt": evt.Attempt, "reason": evt.Reason, "no_activity": evt.NoActivity,
			})
		case "task_complete":
			emit("delegation", map[string]any{"type": "task_complete", "task_name": evt.TaskName, "duration": evt.Duration, "status": evt.Status, "attempt": evt.Attempt})
		case "task_failed":
			emit("delegation", map[string]any{
				"type": "task_failed", "task_name": evt.TaskName, "duration": evt.Duration,
				"error": evt.Error, "status": evt.Status, "attempt": evt.Attempt,
				"reason": evt.Reason, "no_activity": evt.NoActivity,
			})
		case "task_tool_call":
			emit("delegation", map[string]any{
				"type": "task_tool_call", "task_name": evt.TaskName,
				"tool_name": evt.ToolName, "tool_args": evt.ToolArgs,
			})
		case "task_tool_result":
			emit("delegation", map[string]any{
				"type": "task_tool_result", "task_name": evt.TaskName,
				"tool_name": evt.ToolName, "tool_result": evt.ToolResult,
			})
		case "task_text":
			emit("delegation", map[string]any{
				"type": "task_text", "task_name": evt.TaskName,
				"text": evt.Text,
			})
		case "delegation_complete":
			emit("delegation", map[string]any{"type": "done", "status": evt.Status})
		}
	}
	// Wire sub-agent authorization gate for code-mode HITL. When a sub-agent
	// needs tool/folder authorization, it sends a request on the channel; this
	// gate blocks the sub-agent goroutine until the TUI user responds.
	if chatAgent.EnforceAuthorization && !autoApprove {
		chatAgent.SubAgentAuthGate = func(req agent.SubAgentAuthRequest) agent.SubAgentAuthResponse {
			// Serialize the complete prompt/response lifecycle. Without this lock,
			// parallel children overwrite the single overlay and all wait on the
			// same uncorrelated response channel.
			b.subAgentAuthMu.Lock()
			defer b.subAgentAuthMu.Unlock()

			policy := chatAgent.GetOrCreateAuthPolicy(req.ParentSessionID)
			if policy != nil {
				if req.Kind == "folder" && len(policy.OutOfScopePaths(req.Args)) == 0 {
					return agent.SubAgentAuthResponse{Granted: true, Choice: "Always Allow"}
				}
				if req.Kind == "tool" && policy.AllToolsAllowedForSession() {
					return agent.SubAgentAuthResponse{Granted: true, Choice: "Always Allow"}
				}
			}

			// Determine options based on kind (same as main-thread gates).
			var options []any
			if req.Kind == "folder" {
				for _, o := range agent.FolderApprovalOptions() {
					options = append(options, o)
				}
			} else {
				for _, o := range agent.ToolApprovalOptions() {
					options = append(options, o)
				}
			}
			// Mark a sub-agent as blocked BEFORE surfacing the overlay so
			// RespondSubAgentAuth routes the user's decision here (and not to a
			// main-thread RunTurn). Setting it before emit closes the window
			// where a very fast approval could arrive before the flag is set.
			// Cleared as soon as we wake, so a subsequent main-thread approval
			// in the same turn is not misrouted.
			b.mu.Lock()
			b.subAgentAuthPending = true
			b.mu.Unlock()
			// Emit approval event so the TUI shows the overlay.
			payload := map[string]any{
				"tool":      req.ToolName,
				"options":   options,
				"kind":      req.Kind,
				"sub_agent": true,
				"task_name": req.TaskName,
			}
			if len(req.Args) > 0 {
				payload["args"] = req.Args
			}
			if len(req.OutOfScopePaths) > 0 {
				payload["paths"] = req.OutOfScopePaths
			}
			emit("approval", payload)
			// Block until the TUI user responds — or the turn is cancelled
			// (Esc / Ctrl+C). On cancellation we deny and clear the pending
			// flag so no sub-agent goroutine leaks and the next turn starts
			// clean.
			var resp agent.SubAgentAuthResponse
			select {
			case resp = <-b.subAgentAuthRespCh:
			case <-ctx.Done():
				resp = agent.SubAgentAuthResponse{Granted: false, Choice: "Deny"}
				b.mu.Lock()
				b.subAgentAuthPending = false
				b.mu.Unlock()
			}
			return resp
		}
	}
	// Graph-Optimized Plan mode setup.
	planMode := opts.PlanMode
	graphPlan := opts.GraphPlanMode
	askMode := opts.AskMode
	approvedPlanExecution := opts.ApprovedPlanExecution
	// Capture the explicit intent BEFORE the inferred branch below can flip
	// approvedPlanExecution. This is true only when the turn was launched
	// directly from approving the plan; it arms the bounded research clamp.
	// Inferred continuations (an approved PLAN.md still on disk) leave this
	// false so discovery behaves as regular Normal mode.
	approvedPlanExecutionExplicit := opts.ApprovedPlanExecution
	systemContext := opts.SystemContext
	if planMode || graphPlan {
		// Planning / revision turns may replace the plan. Approved execution
		// never reopens that slot — announce_plan is Plan-mode only.
		chatAgent.AllowActivePlanReplacement()
	} else if !askMode {
		if !approvedPlanExecution && b.shouldContinueApprovedPlan(ctx, sessionID, planPath, chatAgent) {
			approvedPlanExecution = true
		}
		if approvedPlanExecution {
			if err := chatAgent.RestoreApprovedPlan(); err != nil {
				if opts.ApprovedPlanExecution {
					return nil, fmt.Errorf("restore approved plan: %w", err)
				}
				slog.Debug("restore approved plan skipped", "component", "localAgentBackend", "error", err)
			}
			if strings.TrimSpace(systemContext) == "" {
				systemContext = agent.BuildPlanExecutionSystemContext(planPath)
			}
		}
	}
	if graphPlan {
		gp := chatAgent.GetOrCreateGraphPlanState(sessionID)
		gp.Reset()
		chatAgent.SetActiveGraphPlan(gp)
	} else {
		chatAgent.SetActiveGraphPlan(nil)
	}

	if systemContext != "" || planMode || graphPlan || askMode || approvedPlanExecution {
		ctx = agent.WithPromptOverrides(ctx, &agent.PromptOverrides{
			SessionContext:                agent.EscapeCurlyPlaceholders(systemContext),
			PlanMode:                      planMode,
			GraphPlanMode:                 graphPlan,
			AskMode:                       askMode,
			ApprovedPlanExecution:         approvedPlanExecution,
			ApprovedPlanExecutionExplicit: approvedPlanExecutionExplicit,
		})
	}

	// Build the user message. Pasted images / file attachments arrive as raw
	// bytes on opts.Attachments; forward them as InlineData parts so multimodal
	// models can see them (mirrors the platform backend, which routes through
	// agent.NewTimestampedUserContentWithAttachments). Without this, code-mode
	// paste would insert the composer placeholder but silently drop the image.
	var userMsg *genai.Content
	if atts := agentAttachmentsFromBackend(opts.Attachments); len(atts) > 0 {
		userMsg = agent.NewTimestampedUserContentWithAttachments(message, atts)
	} else {
		userMsg = &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{{Text: message}},
		}
	}

	go func() {
		defer func() {
			outClosed.Store(true)
			close(out)
		}()

		emit("session", map[string]any{"sessionId": sessionID, "isNew": isNew})
		// On a brand-new session, seed the index with a provisional title from
		// the first user message, then kick off a best-effort LLM title refine
		// in the background. The provisional title appears immediately in
		// `/sessions`; the LLM-refined title replaces it asynchronously.
		if isNew && b.fileStore != nil {
			provisional := deriveSessionTitle(message)
			if provisional != "" {
				_ = b.fileStore.SetSessionTitle(ctx, sessionID, provisional)
				b.mu.Lock()
				b.title = provisional
				b.mu.Unlock()
			}
			// Best-effort LLM title refinement (non-blocking).
			if b.result != nil && b.result.LLM != nil {
				go generateCodeSessionTitle(b.result.LLM, b.fileStore, sessionID, message, provisional, func(title string) {
					b.mu.Lock()
					b.title = title
					b.mu.Unlock()
					emit("session_title", map[string]any{"title": title, "sessionId": sessionID})
				})
			}
		}

		// Ensure the .codegraph/ index exists so the codegraph MCP server can
		// answer codegraph_explore queries. This runs on every turn but is a
		// fast no-op when the index already exists (single os.Stat). On first
		// run it indexes the project and shows progress in the status line.
		if notice := EnsureCodegraph(ctx, b.workingDir, func(msg string) {
			out <- events.NewStatus(msg)
		}); notice != "" {
			emit("system", map[string]any{"content": notice})
		}

		// Turn-boundary compaction: if the active session is over threshold,
		// start a child session whose first events are the summary + recent
		// turns, then drive the turn against that child. The parent transcript
		// is never rewritten, so /rollback can still reach pre-compaction turns.
		effectiveID := b.maybeCompactToChild(ctx, sessionID, emit)

		// File-snapshot boundary for this turn is derived from the *effective*
		// (possibly post-compaction) session so capture/rollback stay aligned.
		turnIndex := b.sessionEventCount(ctx, effectiveID)
		if b.checkpoints != nil {
			b.checkpoints.BeginTurn(effectiveID, turnIndex)
		}

		out <- events.NewStatus("Thinking…")

		b.driveTurn(ctx, rnr, chatAgent, effectiveID, turnIndex, userMsg, emit)

		emit("done", map[string]any{"done": true})
	}()

	return out, nil
}

// agentAttachmentsFromBackend converts TUI backend attachments (raw bytes) into
// agent.Attachment values (base64 data) for NewTimestampedUserContentWithAttachments.
// Empty payloads are skipped so a stray placeholder never produces a broken part.
func agentAttachmentsFromBackend(atts []backend.Attachment) []agent.Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]agent.Attachment, 0, len(atts))
	for _, a := range atts {
		if len(a.Data) == 0 {
			continue
		}
		out = append(out, agent.Attachment{
			Filename: a.Filename,
			MimeType: a.MimeType,
			Data:     base64.StdEncoding.EncodeToString(a.Data),
		})
	}
	return out
}

// sessionEventCount returns how many events are currently persisted for the
// session (0 if it cannot be loaded). Used to derive the per-turn checkpoint
// boundary.
func (b *localAgentBackend) sessionEventCount(ctx context.Context, sessionID string) int {
	if sessionID == "" {
		return 0
	}
	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: sessionID,
	})
	if err != nil || resp == nil || resp.Session == nil {
		return 0
	}
	return resp.Session.Events().Len()
}

// compactToChildResult is the outcome of compactToChild.
type compactToChildResult struct {
	SessionID string // active session after the call (parent if no-op)
	Before    int
	After     int
	Did       bool // true when a child was created and switched to
}

// contentsToSessionEvents converts compacted genai contents into ADK events
// suitable for seeding or rewriting a session.
func contentsToSessionEvents(contents []*genai.Content) []*session.Event {
	out := make([]*session.Event, 0, len(contents))
	for i, c := range contents {
		if c == nil {
			continue
		}
		author := "model"
		if c.Role == "user" {
			author = "user"
		}
		out = append(out, &session.Event{
			ID:          fmt.Sprintf("compact-%d", i),
			Author:      author,
			Timestamp:   time.Now(),
			LLMResponse: adkmodel.LLMResponse{Content: c},
		})
	}
	return out
}

// compactToChild persistently compacts the active session so subsequent model
// calls (including mid-turn) no longer rebuild the full history.
//
// Preferred path (FileStore): ArchiveAndReplaceEvents — archives the full
// history under a new parent session id, rewrites the *same* active session id
// to the summary+recent events, and updates every live ADK session pointer.
// That way rnr.Run(sessionID) keeps working and ContentsRequestProcessor sees
// the compact history on the next model step (no 8k→190k thrash).
//
// Fallback (in-memory tests without FileStore): create a child session and
// switch b.sessionID to it (original design).
//
// force=true (from /compact) always attempts compaction when there is enough
// history; force=false only runs when the session exceeds the threshold.
func (b *localAgentBackend) compactToChild(ctx context.Context, sessionID string, force bool) compactToChildResult {
	out := compactToChildResult{SessionID: sessionID}
	if sessionID == "" || b.result == nil || b.result.ChatAgent == nil || b.result.ChatAgent.Compactor == nil {
		return out
	}
	comp := b.result.ChatAgent.Compactor

	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: sessionID,
	})
	if err != nil || resp == nil || resp.Session == nil {
		return out
	}

	var contents []*genai.Content
	for ev := range resp.Session.Events().All() {
		if ev != nil && ev.LLMResponse.Content != nil {
			contents = append(contents, ev.LLMResponse.Content)
		}
	}
	if len(contents) == 0 {
		return out
	}
	before := persistentsession.EstimateTokens(contents)
	out.Before = before
	out.After = before

	if !force && !comp.ShouldCompact(contents) {
		return out
	}
	if force && len(contents) <= comp.PreserveRecent {
		return out
	}

	// Suppress the in-callback OnCompaction hook for this explicit compaction
	// so we emit a single user-facing notice from the caller.
	comp.SetOnCompaction(nil)

	compacted, cErr := comp.CompactContents(ctx, contents)
	if cErr != nil || len(compacted) >= len(contents) {
		return out
	}
	after := persistentsession.EstimateTokens(compacted)
	out.After = after
	newEvents := contentsToSessionEvents(compacted)
	if len(newEvents) == 0 {
		return out
	}

	// Preferred: archive full history + rewrite active session in place.
	if b.fileStore != nil {
		if _, aErr := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), sessionID, newEvents); aErr != nil {
			slog.Warn("persistent compaction archive failed", "session_id", sessionID, "error", aErr)
			return out
		}
		b.mu.Lock()
		b.sessionID = sessionID // unchanged — ADK Run keeps this id
		b.contextTokens = int64(after)
		b.mu.Unlock()
		out.SessionID = sessionID
		out.Did = true
		return out
	}

	// Fallback for pure in-memory session services (unit tests).
	childResp, cErr := b.sessionSvc.Create(ctx, &session.CreateRequest{
		AppName: codeAppName,
		UserID:  b.effectiveUserID(),
		State:   map[string]any{persistentsession.StateKeyParentID: sessionID},
	})
	if cErr != nil || childResp == nil || childResp.Session == nil {
		return out
	}
	childID := childResp.Session.ID()
	childSess := childResp.Session
	for _, ev := range newEvents {
		if aErr := b.sessionSvc.AppendEvent(ctx, childSess, ev); aErr != nil {
			slog.Debug("compaction child seeding failed; staying on parent", "error", aErr)
			return out
		}
	}
	b.mu.Lock()
	b.sessionID = childID
	b.contextTokens = int64(after)
	b.mu.Unlock()
	out.SessionID = childID
	out.Did = true
	return out
}

// maybeCompactToChild runs automatic threshold-based compaction at a turn
// boundary and surfaces a notice on the event stream when it fires.
func (b *localAgentBackend) maybeCompactToChild(ctx context.Context, sessionID string, emit func(string, map[string]any)) string {
	res := b.compactToChild(ctx, sessionID, false)
	// Re-arm the UI hook for any within-turn safety-valve compactons.
	if b.result != nil && b.result.ChatAgent != nil && b.result.ChatAgent.Compactor != nil {
		comp := b.result.ChatAgent.Compactor
		comp.SetOnCompaction(func(beforeTokens, afterTokens int) {
			emit("system", map[string]any{
				"content": fmt.Sprintf("Compacted context: %s → %s tokens.", formatCodeTokens(beforeTokens), formatCodeTokens(afterTokens)),
			})
			emit("usage", map[string]any{
				"input_tokens": afterTokens,
				"estimated":    true,
			})
			b.mu.Lock()
			b.contextTokens = int64(afterTokens)
			b.mu.Unlock()
		})
	}
	if !res.Did {
		return sessionID
	}
	emit("system", map[string]any{
		"content": fmt.Sprintf("Compacted context: %s → %s tokens. Earlier turns are preserved and remain reachable via /rollback.",
			formatCodeTokens(res.Before), formatCodeTokens(res.After)),
	})
	emit("usage", map[string]any{"input_tokens": res.After, "estimated": true})
	emit("session", map[string]any{"sessionId": res.SessionID, "isNew": false})
	return res.SessionID
}

// deriveSessionTitle produces a short, single-line title from the first user
// message of a session. Empty input yields an empty title (caller skips).
func deriveSessionTitle(message string) string {
	title := strings.TrimSpace(message)
	if title == "" {
		return ""
	}
	if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
		title = strings.TrimSpace(title[:idx])
	}
	const maxLen = 60
	if len(title) > maxLen {
		title = strings.TrimSpace(title[:maxLen]) + "…"
	}
	return title
}

// generateCodeSessionTitle performs a best-effort LLM title generation for code
// mode sessions. It mirrors the Studio title pipeline (pkg/api/chat_utils.go's
// generateStudioSessionTitle) but is self-contained for code mode. On success,
// it persists the refined title and invokes onTitle so the TUI can emit an event.
// On any failure the provisional title already in the store is left unchanged.
func generateCodeSessionTitle(llm adkmodel.LLM, store *persistentsession.FileStore, sessionID, userMessage, provisionalTitle string, onTitle func(string)) {
	if llm == nil || store == nil || userMessage == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	prompt := fmt.Sprintf(
		"Generate a concise title (5-7 words max) for a conversation that starts with this message. "+
			"Return ONLY the title text, nothing else. "+
			"Do not include any thinking, reasoning, analysis, or explanation. "+
			"No quotes, no markdown, no punctuation at the end.\n\nUser message: %s", userMessage)

	req := &adkmodel.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText(prompt, genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{
			Temperature:     genai.Ptr(float32(0.3)),
			MaxOutputTokens: 100,
		},
	}

	var raw string
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			slog.Debug("code session title LLM error", "session_id", sessionID, "error", err)
			return // provisional title stays
		}
		if resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			if part.Text != "" && !part.Thought {
				raw += part.Text
			}
		}
	}

	title := cleanCodeSessionTitle(raw)
	if title == "" || title == provisionalTitle {
		return
	}
	setCtx, setCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer setCancel()
	if err := store.SetSessionTitle(setCtx, sessionID, title); err != nil {
		slog.Debug("failed to set refined code session title", "session_id", sessionID, "error", err)
		return
	}
	if onTitle != nil {
		onTitle(title)
	}
}

// cleanCodeSessionTitle strips thinking tags and truncates to a reasonable length.
func cleanCodeSessionTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if title == "" {
		return ""
	}
	// Strip common thinking tag wrappers: <think>...</think> or <thinking>...</thinking>.
	for _, prefix := range []string{"<think>", "<thinking>"} {
		if idx := strings.Index(title, prefix); idx >= 0 {
			after := title[idx+len(prefix):]
			if closeIdx := strings.Index(after, ">"); closeIdx >= 0 {
				// Remove everything from <prefix> through the closing >
				title = title[:idx] + after[closeIdx+1:]
			} else {
				// Unclosed tag: remove from prefix to end.
				title = title[:idx]
			}
		}
	}
	title = strings.TrimSpace(title)
	// Remove surrounding quotes.
	title = strings.Trim(title, "\"'`")
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if len(title) > 80 {
		title = title[:77] + "..."
	}
	return title
}

// driveTurn runs the ADK runner loop and translates each event via emit. It is
// intentionally a slim version of ChatRunner.Run: it handles the concerns that
// matter for local host execution (text, tool calls/results, approvals via
// state delta, usage, thinking, errors) and omits Studio-only surfaces
// (network-denial prompts, tutorial blueprints, app previews) that do not apply
// when there is no sandbox and no platform.
func (b *localAgentBackend) driveTurn(
	ctx context.Context,
	rnr *runner.Runner,
	chatAgent *agent.ChatAgent,
	sessionID string,
	turnIndex int,
	userMsg *genai.Content,
	emit func(string, map[string]any),
) {
	seenPartialText := false
	sawRealUsage := false
	// Allow the first mid-turn estimate to fire immediately (the throttle only
	// applies to subsequent tool steps within this turn).
	b.mu.Lock()
	b.lastCtxEstimate = time.Time{}
	b.mu.Unlock()
	for event, runErr := range rnr.Run(ctx, b.effectiveUserID(), sessionID, userMsg, adkagent.RunConfig{
		StreamingMode: adkagent.StreamingModeSSE,
	}) {
		if ctx.Err() != nil {
			return
		}
		if runErr != nil {
			emit("error", map[string]any{"error": runErr.Error()})
			return
		}

		// Approval / thinking / retry surfaced through the state delta.
		if event.Actions.StateDelta != nil {
			b.processStateDelta(event.Actions.StateDelta, emit)
		}

		if event.LLMResponse.Content == nil {
			if b.emitUsage(event, emit) {
				sawRealUsage = true
			}
			continue
		}

		for _, part := range event.LLMResponse.Content.Parts {
			if part.Text != "" && !part.Thought {
				if event.LLMResponse.Partial {
					seenPartialText = true
					emit("text", map[string]any{"text": part.Text})
				} else if !seenPartialText {
					emit("text", map[string]any{"text": part.Text})
				} else {
					seenPartialText = false
				}
			}
			if part.FunctionCall != nil {
				if part.FunctionCall.Name == "announce_plan" {
					continue
				}
				// Snapshot files this tool is about to modify BEFORE it runs, so
				// /rollback can restore them. The FunctionCall event is streamed
				// before the tool executes; capture is best-effort and never
				// blocks the turn.
				b.captureToolTargets(sessionID, turnIndex, part.FunctionCall.Name, part.FunctionCall.Args)
				args := part.FunctionCall.Args
				if chatAgent.Redactor != nil && args != nil {
					args = chatAgent.Redactor.RedactMap(args)
				}
				emit("tool_call", map[string]any{
					"name": part.FunctionCall.Name,
					"id":   part.FunctionCall.ID,
					"args": args,
				})
			}
			if part.FunctionResponse != nil {
				if part.FunctionResponse.Name == "announce_plan" {
					status, _ := part.FunctionResponse.Response["status"].(string)
					if status != "ok" {
						// Rejected/no-op announcements are ordinary tool results. In
						// particular, they must not create a second pending approval.
						emit("tool_result", map[string]any{
							"name":   part.FunctionResponse.Name,
							"id":     part.FunctionResponse.ID,
							"result": part.FunctionResponse.Response,
						})
						continue
					}
					// Emit the document and pending approval lifecycle atomically.
					// This prevents a prompt from ever existing without its plan.
					planPayload := map[string]any{"status": string(events.PlanPending)}
					if plan := chatAgent.GetActivePlan(); plan != nil {
						goal, steps := plan.SnapshotInfo()
						doc := plan.SnapshotDoc()
						planPayload["text"] = agent.RenderPlanFromInfoWithDoc(goal, doc, steps)
						planPayload["plan_context"] = doc.Context
						planPayload["plan_what_not_to_do"] = doc.WhatNotToDo
						planPayload["plan_verification"] = doc.Verification
					}
					emit("plan", planPayload)
					emit("done", nil)
					return
				}
				resp := part.FunctionResponse.Response
				if chatAgent.Redactor != nil && resp != nil {
					resp = chatAgent.Redactor.RedactMap(resp)
				}
				emit("tool_result", map[string]any{
					"name":   part.FunctionResponse.Name,
					"id":     part.FunctionResponse.ID,
					"result": resp,
				})
				// A tool step just completed and the context has grown. Refresh
				// the header's context figure mid-turn (throttled) so it moves
				// during long tool loops instead of only at turn end. This is a
				// no-op when a real provider reading already advanced the figure
				// this iteration.
				if !sawRealUsage {
					b.maybeEmitEstimatedContext(ctx, sessionID, emit)
				}
			}
		}

		if b.emitUsage(event, emit) {
			sawRealUsage = true
		}
	}

	// Some providers (notably local OpenAI-compatible proxies) never return
	// usage metadata, which would leave the header stuck at "Context 0". When
	// no real usage was seen this turn, estimate the context fill from the
	// session's accumulated contents (the same heuristic the compactor uses)
	// and emit a synthetic usage event so the header reflects reality.
	if !sawRealUsage {
		b.emitEstimatedContext(ctx, sessionID, emit)
	}
}

// emitUsage emits a usage event from real provider metadata. It returns true
// when metadata was present (so callers can skip the local estimate fallback).
func (b *localAgentBackend) emitUsage(event *session.Event, emit func(string, map[string]any)) bool {
	if event.LLMResponse.UsageMetadata == nil || event.LLMResponse.Partial {
		return false
	}
	um := event.LLMResponse.UsageMetadata
	if um.TotalTokenCount == 0 && um.PromptTokenCount == 0 && um.CandidatesTokenCount == 0 {
		return false
	}
	emit("usage", map[string]any{
		"input_tokens":  um.PromptTokenCount,
		"output_tokens": um.CandidatesTokenCount,
		"total_tokens":  um.TotalTokenCount,
	})
	return true
}

// Compact implements backend.CompactionBackend: it runs compaction **now**
// (child-session summary), updates the active session and context estimate, and
// returns a status line. It does not wait for the next user message.
func (b *localAgentBackend) Compact(ctx context.Context) (string, error) {
	if b.result == nil || b.result.ChatAgent == nil || b.result.ChatAgent.Compactor == nil {
		return "Compaction is disabled.", nil
	}
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	if sessionID == "" {
		return "No active session to compact.", nil
	}

	_, win := b.result.ChatAgent.Compactor.TokenUsage()
	if win == 0 {
		// TokenUsage may still be zero before any model call; fall back to the
		// configured window on the compactor itself.
		win = b.result.ChatAgent.Compactor.ContextWindow
	}
	res := b.compactToChild(ctx, sessionID, true /* force */)
	if !res.Did {
		est := res.Before
		if est == 0 {
			est = int(b.estimateContextTokens(ctx, sessionID))
		}
		pct := 0.0
		if win > 0 {
			pct = float64(est) / float64(win) * 100
		}
		return fmt.Sprintf("Context is ~%s tokens (%.0f%% of %s). Not enough older history to compact further.",
			formatCodeTokens(est), pct, formatCodeTokens(win)), nil
	}
	pctAfter := 0.0
	if win > 0 {
		pctAfter = float64(res.After) / float64(win) * 100
	}
	return fmt.Sprintf("Compacted context: %s → %s tokens (now ~%.0f%% of %s). Earlier turns are preserved and remain reachable via /rollback.",
		formatCodeTokens(res.Before), formatCodeTokens(res.After), pctAfter, formatCodeTokens(win)), nil
}

// estimateContextTokens estimates the current context-window fill from all
// events stored in the session. Mirrors session.EstimateTokens (~3 chars/token)
// so it aligns with the compactor. Returns 0 when the session cannot be read or
// is empty.
func (b *localAgentBackend) estimateContextTokens(ctx context.Context, sessionID string) int64 {
	if sessionID == "" {
		return 0
	}
	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: sessionID,
	})
	if err != nil || resp == nil || resp.Session == nil {
		return 0
	}
	var contents []*genai.Content
	for ev := range resp.Session.Events().All() {
		if ev != nil && ev.LLMResponse.Content != nil {
			contents = append(contents, ev.LLMResponse.Content)
		}
	}
	est := persistentsession.EstimateTokens(contents)
	if est <= 0 {
		return 0
	}
	return int64(est)
}

// emitEstimatedContext estimates the current context-window fill from all
// events stored in the session and emits it as a usage event. Used when the
// provider does not report token usage.
func (b *localAgentBackend) emitEstimatedContext(ctx context.Context, sessionID string, emit func(string, map[string]any)) {
	est := b.estimateContextTokens(ctx, sessionID)
	if est <= 0 {
		return
	}
	b.mu.Lock()
	b.contextTokens = est
	b.lastCtxEstimate = time.Now()
	b.mu.Unlock()
	// Report as input tokens so it drives the header's context figure without
	// inflating cumulative "Usage" output counts. The transcript uses the max
	// input+output as the context occupancy.
	emit("usage", map[string]any{
		"input_tokens":  est,
		"output_tokens": 0,
		"total_tokens":  0,
		"estimated":     true,
	})
}

// maybeEmitEstimatedContext emits an estimated context reading like
// emitEstimatedContext, but only if at least contextEstimateInterval has
// elapsed since the last estimate. It is called between tool steps within a
// turn so the header's "Context" figure advances live during long tool loops,
// while the throttle prevents re-scanning the whole session on every step.
func (b *localAgentBackend) maybeEmitEstimatedContext(ctx context.Context, sessionID string, emit func(string, map[string]any)) {
	b.mu.Lock()
	throttled := !b.lastCtxEstimate.IsZero() && time.Since(b.lastCtxEstimate) < contextEstimateInterval
	b.mu.Unlock()
	if throttled {
		return
	}
	b.emitEstimatedContext(ctx, sessionID, emit)
}

// processStateDelta mirrors ChatRunner.processStateDelta for the local driver,
// emitting approval / auto_approved / retry / error_info / thinking payloads in
// the same shapes so mapSSEToEvents renders them correctly.
func (b *localAgentBackend) processStateDelta(delta map[string]any, emit func(string, map[string]any)) {
	if optsVal, ok := delta["approval_options"]; ok {
		toolName, _ := delta["approval_tool"].(string)
		var options []any
		switch v := optsVal.(type) {
		case []string:
			for _, s := range v {
				options = append(options, s)
			}
		case []any:
			options = v
		}
		payload := map[string]any{
			"tool":    toolName,
			"options": options,
		}
		// Code-mode authorization prompts carry a kind ("tool"/"folder") and,
		// for folder prompts, the requested out-of-project paths.
		if kind, ok := delta["approval_kind"].(string); ok && kind != "" {
			payload["kind"] = kind
		}
		if pathsVal, ok := delta["approval_paths"]; ok {
			switch v := pathsVal.(type) {
			case []string:
				payload["paths"] = v
			case []any:
				var paths []string
				for _, p := range v {
					if s, ok := p.(string); ok {
						paths = append(paths, s)
					}
				}
				payload["paths"] = paths
			}
		}
		if argsVal, ok := delta["approval_args"]; ok {
			if args, ok := argsVal.(map[string]any); ok && len(args) > 0 {
				payload["args"] = args
			}
		}
		emit("approval", payload)
	}
	if autoApproved, ok := delta["auto_approved"].(bool); ok && autoApproved {
		if toolName, ok := delta["approval_tool"].(string); ok {
			emit("auto_approved", map[string]any{"tool": toolName})
		}
	}
	if spinnerText, ok := delta["_spinner_text"].(string); ok {
		emit("thinking", map[string]any{"text": spinnerText})
	}
}

func (b *localAgentBackend) setModel(providerName, modelName string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if providerName != "" {
		b.provider = providerName
	}
	if modelName != "" {
		b.model = modelName
	}
}

func (b *localAgentBackend) ListSessions(ctx context.Context) ([]backend.SessionSummary, error) {
	_ = ctx
	// Enumerate persisted code sessions for the current working directory
	// (scoped by userID). Falls back to showing just the active in-memory
	// session if no persistent store is wired (e.g. in tests).
	if b.fileStore == nil {
		b.mu.Lock()
		id := b.sessionID
		b.mu.Unlock()
		if id == "" {
			return nil, nil
		}
		return []backend.SessionSummary{{ID: id, Title: "(current)"}}, nil
	}

	metas, err := b.fileStore.ListSessionMetas(codeAppName, b.effectiveUserID())
	if err != nil {
		return nil, fmt.Errorf("failed to list code sessions: %w", err)
	}

	// Resolve the effective UpdatedAt for each root session: after compaction,
	// new messages go to the descendant tip whose UpdatedAt advances while the
	// root's stays frozen. Walk the chain to find the most recent timestamp so
	// a resumed-and-continued session sorts to the top.
	type metaWithEffective struct {
		meta       persistentsession.SessionMeta
		effectiveT time.Time
	}
	enriched := make([]metaWithEffective, 0, len(metas))
	for _, m := range metas {
		effective := m.UpdatedAt
		tipID := b.fileStore.LatestDescendant(m.ID)
		if tipID != m.ID {
			if tipMeta, tErr := b.fileStore.GetSessionMeta(tipID); tErr == nil && tipMeta != nil {
				if tipMeta.UpdatedAt.After(effective) {
					effective = tipMeta.UpdatedAt
				}
			}
		}
		enriched = append(enriched, metaWithEffective{meta: m, effectiveT: effective})
	}

	// Most-recently-updated first (using effective timestamp from tip).
	sort.Slice(enriched, func(i, j int) bool {
		return enriched[i].effectiveT.After(enriched[j].effectiveT)
	})
	out := make([]backend.SessionSummary, 0, len(enriched))
	for _, e := range enriched {
		title := e.meta.Title
		if title == "" {
			title = "(untitled)"
		}
		updated := ""
		if !e.effectiveT.IsZero() {
			updated = e.effectiveT.Format("2006-01-02 15:04")
		}
		out = append(out, backend.SessionSummary{
			ID:           e.meta.ID,
			Title:        title,
			UpdatedAt:    updated,
			MessageCount: e.meta.MessageCount,
		})
	}
	return out, nil
}

func (b *localAgentBackend) RecordPlanDecision(ctx context.Context, status events.PlanStatus) error {
	b.mu.Lock()
	id := b.sessionID
	b.mu.Unlock()
	if id == "" || status == "" {
		return nil
	}
	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{AppName: codeAppName, UserID: b.effectiveUserID(), SessionID: id})
	if err != nil {
		return fmt.Errorf("load session for plan decision: %w", err)
	}
	if resp == nil || resp.Session == nil {
		return fmt.Errorf("load session for plan decision: session not found")
	}
	ev := &session.Event{
		ID:        fmt.Sprintf("plan-decision-%d", time.Now().UnixNano()),
		Author:    "system",
		Timestamp: time.Now(),
		Actions: session.EventActions{StateDelta: map[string]any{
			planLifecycleStateKey: string(status),
		}},
	}
	if err := b.sessionSvc.AppendEvent(ctx, resp.Session, ev); err != nil {
		return fmt.Errorf("persist plan decision: %w", err)
	}
	return nil
}

func (b *localAgentBackend) LoadHistory(ctx context.Context) ([]backend.HistoryEntry, error) {
	b.mu.Lock()
	id := b.sessionID
	b.mu.Unlock()
	return b.loadHistory(ctx, id)
}

func (b *localAgentBackend) ResumeSession(ctx context.Context, sessionID string) ([]backend.HistoryEntry, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	// Compaction creates a child session linked via ParentID. The session list
	// shows roots (children are hidden); resume must open the tip of the chain
	// so the model replays [latest summary]+tail rather than the raw parent.
	activeID := sessionID
	if b.fileStore != nil {
		activeID = b.fileStore.LatestDescendant(sessionID)
	}
	hist, err := b.loadHistory(ctx, activeID)
	if err != nil {
		return nil, err
	}
	// Estimate the resumed session's context occupancy so the header shows real
	// utilization immediately, instead of "Context 0" until the next turn.
	// Use the active (tip) session — that is the model-facing context size.
	ctxTokens := b.estimateContextTokens(ctx, activeID)

	// Load persisted title for the header (use root session's title since
	// compacted children inherit the same title).
	var title string
	if b.fileStore != nil {
		if t, tErr := b.fileStore.GetSessionTitle(ctx, sessionID); tErr == nil {
			title = t
		}
	}

	b.mu.Lock()
	b.sessionID = activeID
	b.resumed = true
	b.contextTokens = ctxTokens
	b.title = title
	b.mu.Unlock()
	return hist, nil
}

// loadHistory reads prior events from the session service and maps them to TUI
// history entries.
func (b *localAgentBackend) loadHistory(ctx context.Context, id string) ([]backend.HistoryEntry, error) {
	if id == "" {
		return nil, nil
	}
	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: id,
	})
	if err != nil || resp == nil || resp.Session == nil {
		return nil, nil
	}

	// Pre-scan: identify FunctionCall IDs that were superseded by the approval
	// flow. These have no FunctionResponse because the approval protocol
	// re-issues the call with a new ID after the user approves. Emitting them
	// in history would show phantom "extra" tool executions.
	answeredIDs := make(map[string]bool)
	approvalSuperseded := make(map[string]bool)
	var allEvents []*session.Event
	for ev := range resp.Session.Events().All() {
		allEvents = append(allEvents, ev)
		if ev == nil {
			continue
		}
		// Collect FunctionResponse IDs from BOTH Content and LLMResponse.Content
		// to match the main rendering loop which reads from LLMResponse.Content.
		for _, part := range eventAllParts(ev) {
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				answeredIDs[part.FunctionResponse.ID] = true
			}
		}
	}
	for i, ev := range allEvents {
		if ev == nil {
			continue
		}
		// Scan for FunctionCalls in both Content and LLMResponse.Content fields.
		for _, part := range eventAllParts(ev) {
			if part.FunctionCall == nil || part.FunctionCall.ID == "" {
				continue
			}
			if answeredIDs[part.FunctionCall.ID] {
				continue
			}
			// Look ahead for awaiting_approval=true
			for j := i + 1; j < len(allEvents); j++ {
				if allEvents[j] == nil {
					continue
				}
				if allEvents[j].Actions.StateDelta != nil {
					if awaiting, ok := allEvents[j].Actions.StateDelta["awaiting_approval"]; ok {
						if ab, isBool := awaiting.(bool); isBool && ab {
							approvalSuperseded[part.FunctionCall.ID] = true
							break
						}
					}
				}
				if eventHasUserRole(allEvents[j]) {
					break
				}
			}
		}
	}

	// Track which event indices carry awaiting_approval=true or auto_approved=true
	// so we can: (1) detect and reclassify the immediately-following user approval
	// response as a system message instead of a user bubble, and (2) suppress the
	// pre-rendered ANSI tool box text that these events carry (it was shown as a
	// transient overlay during live execution, not as inline agent text).
	approvalPromptIndices := make(map[int]bool)
	for i, ev := range allEvents {
		if ev == nil {
			continue
		}
		if ev.Actions.StateDelta != nil {
			if awaiting, ok := ev.Actions.StateDelta["awaiting_approval"]; ok {
				if ab, isBool := awaiting.(bool); isBool && ab {
					approvalPromptIndices[i] = true
				}
			}
			if autoApproved, ok := ev.Actions.StateDelta["auto_approved"]; ok {
				if ab, isBool := autoApproved.(bool); isBool && ab {
					approvalPromptIndices[i] = true
				}
			}
		}
	}

	// Track announce_plan call args by call ID so we can reconstruct the plan
	// document when the corresponding FunctionResponse is seen. During a live
	// session the plan is rendered as synthetic agent text (not stored in the
	// session); on resume we must recreate that text from the stored call args.
	announcePlanArgs := make(map[string]map[string]any)
	latestPlanOutIdx := -1

	var out []backend.HistoryEntry
	for i, ev := range allEvents {
		if ev == nil {
			continue
		}
		if lifecycle, ok := ev.Actions.StateDelta[planLifecycleStateKey].(string); ok && latestPlanOutIdx >= 0 {
			out[latestPlanOutIdx].PlanStatus = events.PlanStatus(lifecycle)
		}
		if ev.LLMResponse.Content == nil {
			continue
		}
		role := ev.LLMResponse.Content.Role
		for _, part := range ev.LLMResponse.Content.Parts {
			switch {
			case part.Text != "" && !part.Thought:
				kind := "agent"
				if role == "user" {
					kind = "user"
				}
				// Skip the approval prompt event text entirely. During live
				// execution this was rendered as a transient overlay (not inline
				// agent text). Re-emitting the pre-rendered ANSI tool box from
				// RenderToolBox would show a purple bordered card with tool args
				// instead of the compact activity fold.
				if approvalPromptIndices[i] {
					continue
				}
				// Reclassify the user's approval response as a system message
				// so it matches the live rendering ("Approval: Always Allow")
				// instead of appearing as an orphaned user bubble.
				if kind == "user" && isApprovalResponseEvent(i, allEvents, approvalPromptIndices, part.Text) {
					out = append(out, backend.HistoryEntry{Kind: "system", Text: "Approval: " + extractApprovalChoice(part.Text)})
					continue
				}
				if kind == "user" && strings.TrimSpace(part.Text) == planApprovalUserMessage && latestPlanOutIdx >= 0 {
					out[latestPlanOutIdx].PlanStatus = events.PlanApproved
				}
				out = append(out, backend.HistoryEntry{Kind: kind, Text: part.Text})
			case part.FunctionCall != nil:
				if approvalSuperseded[part.FunctionCall.ID] {
					continue
				}
				// announce_plan is never shown in the activity fold during live
				// execution — it is converted to a rendered plan text event.
				// Store the args so we can reconstruct that text when the
				// FunctionResponse arrives; do not emit a tool_call entry.
				if part.FunctionCall.Name == "announce_plan" {
					announcePlanArgs[part.FunctionCall.ID] = part.FunctionCall.Args
					continue
				}
				out = append(out, backend.HistoryEntry{
					Kind:     "tool_call",
					ToolName: part.FunctionCall.Name,
					ToolID:   part.FunctionCall.ID,
					Args:     part.FunctionCall.Args,
				})
			case part.FunctionResponse != nil:
				// Reconstruct the plan document text from the stored call args,
				// mirroring the synthetic text event emitted during live execution.
				if part.FunctionResponse.Name == "announce_plan" {
					args := announcePlanArgs[part.FunctionResponse.ID]
					if rendered := renderPlanFromArgs(args); rendered != "" {
						out = append(out, backend.HistoryEntry{
							Kind:       "plan",
							Text:       rendered,
							PlanStatus: events.PlanPending,
							Options:    []string{"Approve & implement", "Request changes", "Decline"},
						})
						latestPlanOutIdx = len(out) - 1
					}
					continue
				}
				out = append(out, backend.HistoryEntry{
					Kind:     "tool_result",
					ToolName: part.FunctionResponse.Name,
					ToolID:   part.FunctionResponse.ID,
					Result:   part.FunctionResponse.Response,
				})
			}
		}
	}
	return out, nil
}

// eventAllParts returns all genai.Parts from an event's Content (accessible
// as both ev.Content and ev.LLMResponse.Content due to Go struct embedding —
// they are the same field). This helper exists for clarity and nil-safety.
func eventAllParts(ev *session.Event) []*genai.Part {
	if ev.Content == nil {
		return nil
	}
	return ev.Content.Parts
}

// eventHasUserRole returns true if the event carries a user-role message.
// (ev.Content and ev.LLMResponse.Content are the same field via Go embedding.)
func eventHasUserRole(ev *session.Event) bool {
	return ev.Content != nil && ev.Content.Role == "user"
}

// isApprovalResponseEvent returns true when the user-role event at index i is
// an approval response that should be reclassified as a system message. It
// checks whether the preceding event (skipping nil/empty) carried an
// awaiting_approval=true state delta, or whether the text itself matches
// known approval response patterns.
func isApprovalResponseEvent(i int, allEvents []*session.Event, approvalPromptIndices map[int]bool, text string) bool {
	// Check if a preceding event is an approval prompt.
	for prev := i - 1; prev >= 0; prev-- {
		if allEvents[prev] == nil {
			continue
		}
		if approvalPromptIndices[prev] {
			return true
		}
		// Stop once we hit a non-nil event that isn't an approval prompt.
		break
	}
	// Fallback: match known approval response patterns by content.
	return isApprovalResponseText(text)
}

// isApprovalResponseText returns true if text looks like an approval decision
// (raw short choice or rewritten authorization instruction).
func isApprovalResponseText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch lower {
	case "allow", "always allow", "deny", "yes", "no", "y", "n":
		return true
	}
	if strings.HasPrefix(text, "The user authorized") || strings.HasPrefix(text, "The user denied") {
		return true
	}
	return false
}

// extractApprovalChoice extracts a short human-readable choice label from the
// raw or rewritten approval response text.
func extractApprovalChoice(text string) string {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	// Short canonical choices — use as-is.
	switch lower {
	case "allow", "always allow", "deny", "yes", "no", "y", "n":
		return trimmed
	}
	// Rewritten authorization text: extract the choice from the prefix.
	if strings.HasPrefix(text, "The user authorized") {
		return "Allow"
	}
	if strings.HasPrefix(text, "The user denied") {
		return "Deny"
	}
	// Fallback.
	if len(trimmed) > 30 {
		return trimmed[:30] + "…"
	}
	return trimmed
}

// renderPlanFromArgs reconstructs the rendered plan document from the stored
// announce_plan FunctionCall args. This mirrors the synthetic text event that
// is emitted during live execution but is never persisted to the session store.
func renderPlanFromArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	goal, _ := args["goal"].(string)

	var steps []agent.PlanStepInfo
	if raw, ok := args["steps"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			step := agent.PlanStepInfo{
				Name:          stringField(m, "name"),
				Description:   stringField(m, "description"),
				Details:       stringField(m, "details"),
				Verify:        stringField(m, "verify"),
				ParallelGroup: stringField(m, "parallel_group"),
			}
			if filesRaw, ok := m["files"].([]any); ok {
				for _, f := range filesRaw {
					fm, ok := f.(map[string]any)
					if !ok {
						continue
					}
					step.Files = append(step.Files, agent.PlanFileChange{
						Path: stringField(fm, "path"),
						Kind: stringField(fm, "kind"),
					})
				}
			}
			steps = append(steps, step)
		}
	}

	doc := agent.PlanDocumentInfo{
		Context:      stringField(args, "context"),
		WhatNotToDo:  stringField(args, "what_not_to_do"),
		Verification: stringField(args, "verification"),
	}
	return agent.RenderPlanFromInfoWithDoc(goal, doc, steps)
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func (b *localAgentBackend) DeleteSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}
	// Collect child session IDs before deletion: sessionSvc.Delete cascades and
	// removes their index entries, so the per-session PLAN.md sidecars must be
	// resolved up front to be cleaned up alongside the parent's.
	var childIDs []string
	if b.fileStore != nil {
		if children, cerr := b.fileStore.ListChildren(sessionID); cerr == nil {
			for _, child := range children {
				childIDs = append(childIDs, child.ID)
			}
		}
	}
	err := b.sessionSvc.Delete(ctx, &session.DeleteRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: sessionID,
	})
	if b.checkpoints != nil {
		_ = b.checkpoints.DeleteSession(sessionID)
	}
	// Remove the per-session PLAN.md sidecar(s) written in code mode (see
	// planFilePath). Best-effort: a missing file is not an error.
	b.removePlanFile(sessionID)
	for _, childID := range childIDs {
		b.removePlanFile(childID)
	}
	b.mu.Lock()
	if b.sessionID == sessionID {
		b.sessionID = ""
		b.usage = nil
		b.resumed = false
	}
	b.mu.Unlock()
	return err
}

func (b *localAgentBackend) NewSession() {
	b.mu.Lock()
	b.sessionID = ""
	b.usage = nil
	b.resumed = false
	rebuild := b.needsRebuild
	b.needsRebuild = false
	b.mu.Unlock()

	if rebuild {
		b.rebuildAgent()
	}
}

// rebuildAgent re-runs NewWiredChatAgent with the current in-memory appConfig
// so that tools configured after process start (e.g. web search via /websearch)
// are loaded into the agent. Called from NewSession() when needsRebuild is set.
func (b *localAgentBackend) rebuildAgent() {
	ctx := context.Background()

	b.mu.Lock()
	appCfg := b.appConfig
	providerName := b.provider
	modelName := b.model
	b.mu.Unlock()

	result, err := NewWiredChatAgent(ctx, &ChatFactoryConfig{
		AppConfig:            appCfg,
		ProviderName:         providerName,
		ModelName:            modelName,
		DebugMode:            b.debug,
		AutoApprove:          b.autoApprove,
		WorkspaceDir:         b.workingDir,
		PlatformMode:         false,
		CodeMode:             true,
		AllowMissingProvider: true,
		LoadProjectContext:   true,
		SessionService:       b.fileStore,
	})
	if err != nil {
		slog.Warn("failed to rebuild agent after config change", "error", err)
		return
	}

	// Clean up old agent resources.
	if b.result.Cleanup != nil {
		b.result.Cleanup()
	}

	b.mu.Lock()
	b.result = result
	b.sessionSvc = common.NewAutoInitService(result.SessionService)
	b.provider = result.ProviderName
	b.model = result.ModelName
	b.configured = result.ProviderConfigured
	b.mu.Unlock()
}

func (b *localAgentBackend) ListProviders(ctx context.Context) ([]string, error) {
	_ = ctx
	if b.appConfig == nil {
		return nil, nil
	}
	out := make([]string, 0, len(b.appConfig.Providers))
	for name := range b.appConfig.Providers {
		if name == "" || strings.HasPrefix(name, "__") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (b *localAgentBackend) ListModels(ctx context.Context, providerName string) ([]string, error) {
	_ = ctx
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil, fmt.Errorf("provider required")
	}
	return provider.ListModelsForProvider(ctx, providerName, b.appConfig)
}

func (b *localAgentBackend) SetModelPin(ctx context.Context, providerName, modelName string) (string, string, error) {
	providerName = strings.TrimSpace(providerName)
	modelName = strings.TrimSpace(modelName)

	if b.result.SwappableLLM == nil {
		return "", "", fmt.Errorf("model switching is not available in this session")
	}
	if providerName == "" || modelName == "" {
		// Clearing the pin: keep the current effective model (there is no
		// separate cascade in code mode — the configured default is the
		// effective model).
		info := b.Info()
		return info.Provider, info.Model, nil
	}
	newLLM, err := provider.GetProvider(ctx, providerName, modelName, b.appConfig)
	if err != nil {
		return "", "", fmt.Errorf("failed to switch to %s/%s: %w", providerName, modelName, err)
	}
	b.result.SwappableLLM.Swap(newLLM)

	b.mu.Lock()
	b.provider = providerName
	b.model = modelName
	b.configured = true
	b.mu.Unlock()

	// Persist the choice as the Astonish default so it survives across runs
	// (general.default_provider / default_model in ~/.config/astonish/config.yaml).
	// A save failure is non-fatal — the in-memory swap already took effect.
	if b.appConfig != nil {
		b.appConfig.General.DefaultProvider = providerName
		b.appConfig.General.DefaultModel = modelName
		if saveErr := b.saveAppConfig(); saveErr != nil && b.debug {
			slog.Warn("failed to persist model selection to config", "component", "code-mode", "error", saveErr)
		}
	}

	return providerName, modelName, nil
}

func (b *localAgentBackend) ReadArtifactContent(ctx context.Context, sessionID, path string) (backend.ArtifactContent, error) {
	_ = ctx
	_ = sessionID
	path = strings.TrimSpace(path)
	if path == "" {
		return backend.ArtifactContent{}, fmt.Errorf("artifact path required")
	}
	// Host execution: artifacts are real files on disk relative to the working
	// directory.
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(b.workingDir, resolved)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return backend.ArtifactContent{}, err
	}
	return backend.ArtifactContent{Path: path, Content: string(data)}, nil
}

// --- RollbackBackend (code-mode /rollback) ---
//
// These methods revert both the conversation and the working-directory file
// changes to an earlier user message. Chat revert truncates the session's
// events (FileStore.TruncateEvents); file revert restores per-turn snapshots
// captured before each tool wrote to disk (CheckpointStore). Code mode is the
// only backend that implements this — the platform backend has no host
// filesystem to snapshot.

// mutatingFileTools are the tool names whose call args name a file the tool is
// about to write. Kept minimal and generic: any tool that takes a path/file_path
// and writes to it. This intentionally mirrors the transcript's file-diff
// detection (write_file / edit_file) rather than special-casing any domain.
var mutatingFileTools = map[string]bool{
	"write_file": true,
	"edit_file":  true,
}

// captureToolTargets snapshots the file a mutating tool is about to modify.
// Best-effort: snapshot failures are ignored so a turn is never blocked.
func (b *localAgentBackend) captureToolTargets(sessionID string, turnIndex int, toolName string, args map[string]any) {
	if b.checkpoints == nil || sessionID == "" {
		return
	}
	if !mutatingFileTools[strings.ToLower(strings.TrimSpace(toolName))] {
		return
	}
	path := pathFromToolArgs(args)
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.workingDir, path)
	}
	_ = b.checkpoints.Capture(sessionID, turnIndex, path)
}

// pathFromToolArgs extracts a file path from a tool call's args, checking the
// conventional "path" and "file_path" keys.
func pathFromToolArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, k := range []string{"path", "file_path"} {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ListRollbackPoints returns one revert target per user message across the
// compaction chain (root ancestor → active tip), oldest first. Point IDs are
// "sessionID:eventIndex" so RollbackTo can re-activate the owning session even
// after a reload. Pre-compaction turns remain reachable because parent
// transcripts are never rewritten by compaction.
func (b *localAgentBackend) ListRollbackPoints(ctx context.Context) ([]backend.RollbackPoint, error) {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	if sessionID == "" {
		return nil, nil
	}

	// Walk root → … → tip so the picker shows the full history across restarts.
	chain := []string{sessionID}
	if b.fileStore != nil {
		chain = b.fileStore.AncestorChain(sessionID)
		if len(chain) == 0 {
			chain = []string{sessionID}
		}
	}

	var points []backend.RollbackPoint
	turnNumber := 0
	// Later sessions in the chain (descendants) contribute their full file
	// snapshot counts to earlier points' "files that would be restored".
	descendantFileTotals := make([]int, len(chain))
	if b.checkpoints != nil {
		for i := len(chain) - 1; i >= 0; i-- {
			n := b.checkpoints.FileCountFrom(chain[i], 0)
			if i+1 < len(chain) {
				n += descendantFileTotals[i+1]
			}
			descendantFileTotals[i] = n
		}
	}

	for ci, sid := range chain {
		resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{
			AppName:   codeAppName,
			UserID:    b.effectiveUserID(),
			SessionID: sid,
		})
		if err != nil || resp == nil || resp.Session == nil {
			continue
		}
		idx := -1
		for ev := range resp.Session.Events().All() {
			idx++
			if ev == nil || ev.LLMResponse.Content == nil {
				continue
			}
			if ev.LLMResponse.Content.Role != "user" {
				continue
			}
			text := firstUserText(ev.LLMResponse.Content.Parts)
			if text == "" {
				continue // skip tool-response / empty user events / summary shells
			}
			// Skip the synthetic compaction-summary seed at the start of a child
			// (authored as user/model with the "[Context Summary" marker).
			if strings.Contains(text, "[Context Summary") {
				continue
			}
			turnNumber++
			label := deriveSessionTitle(text)
			ts := ""
			if !ev.Timestamp.IsZero() {
				ts = ev.Timestamp.Format("15:04:05")
			}
			fileCount := 0
			if b.checkpoints != nil {
				fileCount = b.checkpoints.FileCountFrom(sid, idx)
				// Plus every snapshot from later sessions in the chain.
				if ci+1 < len(chain) {
					fileCount += descendantFileTotals[ci+1]
				}
			}
			points = append(points, backend.RollbackPoint{
				ID:          fmt.Sprintf("%s:%d", sid, idx),
				Label:       label,
				MessageText: text,
				Timestamp:   ts,
				FileCount:   fileCount,
				TurnNumber:  turnNumber,
			})
		}
	}
	return points, nil
}

// firstUserText returns the first non-empty, non-thought text part authored by
// the user (skips function responses, which have no Text).
func firstUserText(parts []*genai.Part) string {
	for _, p := range parts {
		if p == nil {
			continue
		}
		if p.Text != "" && !p.Thought {
			return strings.TrimSpace(p.Text)
		}
	}
	return ""
}

// RollbackTo reverts the conversation and file changes to the point identified
// by pointID. Point IDs are "sessionID:eventIndex" (from ListRollbackPoints);
// a bare integer is still accepted for same-session rollbacks.
//
// Option A (compaction chain): if the point lives in an ancestor session,
// re-activate that ancestor (truncate it to the target), restore files for that
// session and every later child, then delete the later child sessions. Parent
// transcripts are never rewritten by compaction, so this works after reload.
func (b *localAgentBackend) RollbackTo(ctx context.Context, pointID string) ([]backend.HistoryEntry, error) {
	b.mu.Lock()
	activeID := b.sessionID
	b.mu.Unlock()
	if activeID == "" {
		return nil, fmt.Errorf("no active session")
	}

	ownerID, targetIdx, err := parseRollbackPointID(pointID, activeID)
	if err != nil {
		return nil, err
	}

	// Sessions that must be fully undone (file restore + delete): every
	// descendant of ownerID in the current chain after ownerID itself.
	toDrop := []string{}
	if b.fileStore != nil {
		chain := b.fileStore.AncestorChain(activeID)
		// chain is root → tip; drop everything after ownerID.
		found := false
		for _, sid := range chain {
			if found {
				toDrop = append(toDrop, sid)
			}
			if sid == ownerID {
				found = true
			}
		}
		// If active is a descendant of owner, also walk LatestDescendant from
		// owner and drop the tip side if AncestorChain from active missed any
		// (defensive: linear chain is the normal case).
		if !found && ownerID != activeID {
			// owner is not on the active's ancestor chain — treat as invalid.
			return nil, fmt.Errorf("rollback point session %q is not on the active chain", ownerID)
		}
	}

	// 1) Revert file changes: full restore of every later child, then owner from targetIdx.
	if b.checkpoints != nil {
		for _, sid := range toDrop {
			if _, err := b.checkpoints.RestoreTo(sid, 0); err != nil {
				return nil, fmt.Errorf("failed to restore files for compacted session %s: %w", sid, err)
			}
		}
		if _, err := b.checkpoints.RestoreTo(ownerID, targetIdx); err != nil {
			return nil, fmt.Errorf("failed to restore files: %w", err)
		}
	}

	// 2) Truncate the owning session to the events before the target message.
	if b.fileStore != nil {
		if _, err := b.fileStore.TruncateEvents(codeAppName, b.effectiveUserID(), ownerID, targetIdx); err != nil {
			return nil, fmt.Errorf("failed to truncate session: %w", err)
		}
		// 3) Delete later children in the compaction chain (option A).
		for _, sid := range toDrop {
			_ = b.sessionSvc.Delete(ctx, &session.DeleteRequest{
				AppName:   codeAppName,
				UserID:    b.effectiveUserID(),
				SessionID: sid,
			})
			if b.checkpoints != nil {
				_ = b.checkpoints.DeleteSession(sid)
			}
		}
	}

	// 4) Re-activate the owning session and reset usage.
	ctxTokens := b.estimateContextTokens(ctx, ownerID)
	b.mu.Lock()
	b.sessionID = ownerID
	b.usage = nil
	b.contextTokens = ctxTokens
	b.mu.Unlock()

	// 5) Return the rebuilt history for the re-activated session.
	return b.loadHistory(ctx, ownerID)
}

// parseRollbackPointID parses "sessionID:eventIndex" or a bare event index
// (legacy / same-session form). When bare, ownerID defaults to activeID.
func parseRollbackPointID(pointID, activeID string) (ownerID string, targetIdx int, err error) {
	pointID = strings.TrimSpace(pointID)
	if pointID == "" {
		return "", 0, fmt.Errorf("invalid rollback point %q", pointID)
	}
	if i := strings.LastIndex(pointID, ":"); i >= 0 {
		ownerID = pointID[:i]
		idxStr := pointID[i+1:]
		targetIdx, err = strconv.Atoi(idxStr)
		if err != nil || targetIdx < 0 || ownerID == "" {
			return "", 0, fmt.Errorf("invalid rollback point %q", pointID)
		}
		return ownerID, targetIdx, nil
	}
	targetIdx, err = strconv.Atoi(pointID)
	if err != nil || targetIdx < 0 {
		return "", 0, fmt.Errorf("invalid rollback point %q", pointID)
	}
	return activeID, targetIdx, nil
}

// Verify localAgentBackend implements the optional rollback capability.
var _ backend.RollbackBackend = (*localAgentBackend)(nil)

// --- ProviderAdminBackend (code-mode local provider management) ---
// These methods let the /provider TUI overlay manage provider instances and
// persist them to the local config file (~/.config/astonish/config.yaml). They
// never touch a platform database — code mode is file-only by design.

// codeProviderTypes is the catalog of provider types offerable via /provider in
// code mode, with the fields each one needs. API keys are stored directly in
// config.yaml (plaintext) per the local-mode configuration model.
func codeProviderTypes() []backend.ProviderTypeInfo {
	apiKey := backend.ProviderField{Key: "api_key", Label: "API Key", Secret: true}
	return []backend.ProviderTypeInfo{
		{ID: "openai", DisplayName: provider.GetProviderDisplayName("openai"), Fields: []backend.ProviderField{apiKey}},
		{ID: "anthropic", DisplayName: provider.GetProviderDisplayName("anthropic"), Fields: []backend.ProviderField{apiKey}},
		{ID: "gemini", DisplayName: provider.GetProviderDisplayName("gemini"), Fields: []backend.ProviderField{apiKey}},
		{ID: "groq", DisplayName: provider.GetProviderDisplayName("groq"), Fields: []backend.ProviderField{apiKey}},
		{ID: "xai", DisplayName: provider.GetProviderDisplayName("xai"), Fields: []backend.ProviderField{apiKey}},
		{ID: "openrouter", DisplayName: provider.GetProviderDisplayName("openrouter"), Fields: []backend.ProviderField{apiKey}},
		{ID: "poe", DisplayName: provider.GetProviderDisplayName("poe"), Fields: []backend.ProviderField{apiKey}},
		{
			ID:          "sap_ai_core",
			DisplayName: provider.GetProviderDisplayName("sap_ai_core"),
			Fields: []backend.ProviderField{
				{Key: "client_id", Label: "Client ID"},
				{Key: "client_secret", Label: "Client Secret", Secret: true},
				{Key: "auth_url", Label: "Auth URL"},
				{Key: "base_url", Label: "Base URL"},
				{Key: "resource_group", Label: "Resource Group", Default: "default", Optional: true},
			},
		},
		{
			ID:          "litellm",
			DisplayName: provider.GetProviderDisplayName("litellm"),
			Fields: []backend.ProviderField{
				{Key: "base_url", Label: "Base URL", Default: "http://localhost:4000/v1"},
				apiKey,
			},
		},
		{
			ID:          "openai_compat",
			DisplayName: provider.GetProviderDisplayName("openai_compat"),
			Fields: []backend.ProviderField{
				{Key: "base_url", Label: "Base URL", Default: "https://api.openai.com/v1"},
				apiKey,
			},
		},
		{
			ID:          "ollama",
			DisplayName: provider.GetProviderDisplayName("ollama"),
			Fields: []backend.ProviderField{
				{Key: "base_url", Label: "Base URL", Default: "http://localhost:11434", Optional: true},
			},
		},
		{
			ID:          "lm_studio",
			DisplayName: provider.GetProviderDisplayName("lm_studio"),
			Fields: []backend.ProviderField{
				{Key: "base_url", Label: "Base URL", Default: "http://localhost:1234/v1", Optional: true},
			},
		},
	}
}

func (b *localAgentBackend) ProviderTypes() []backend.ProviderTypeInfo {
	return codeProviderTypes()
}

func (b *localAgentBackend) ListProviderInstances(ctx context.Context) ([]backend.ProviderInstance, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	// Code mode manages ONLY the local config.yaml providers. Platform
	// providers are intentionally never surfaced here: their credentials live
	// on the platform and must never transit to the local surface. Platform
	// runtime resolution stays entirely on the platform.
	out := make([]backend.ProviderInstance, 0, len(b.appConfig.Providers))
	if b.appConfig != nil {
		for name, inst := range b.appConfig.Providers {
			if name == "" || strings.HasPrefix(name, "__") {
				continue
			}
			out = append(out, backend.ProviderInstance{
				Name: name,
				Type: config.GetProviderType(name, inst),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (b *localAgentBackend) AddProvider(ctx context.Context, name, typeID string, fields map[string]string) error {
	_ = ctx
	name = strings.TrimSpace(name)
	typeID = strings.TrimSpace(typeID)
	if name == "" {
		return fmt.Errorf("provider instance name is required")
	}
	if typeID == "" {
		return fmt.Errorf("provider type is required")
	}

	// Validate the type against the catalog and enforce required fields.
	var typeInfo *backend.ProviderTypeInfo
	for i := range codeProviderTypes() {
		if t := codeProviderTypes()[i]; t.ID == typeID {
			typeInfo = &t
			break
		}
	}
	if typeInfo == nil {
		return fmt.Errorf("unknown provider type %q", typeID)
	}

	inst := config.ProviderConfig{"type": typeID}
	for _, f := range typeInfo.Fields {
		val := strings.TrimSpace(fields[f.Key])
		if val == "" {
			if f.Optional {
				continue
			}
			return fmt.Errorf("%s is required for %s", f.Label, typeInfo.DisplayName)
		}
		inst[f.Key] = val
	}

	b.mu.Lock()
	if b.appConfig.Providers == nil {
		b.appConfig.Providers = make(map[string]config.ProviderConfig)
	}
	b.appConfig.Providers[name] = inst
	b.mu.Unlock()

	if err := b.saveAppConfig(); err != nil {
		// Roll back the in-memory change so state matches disk.
		b.mu.Lock()
		delete(b.appConfig.Providers, name)
		b.mu.Unlock()
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

func (b *localAgentBackend) RemoveProvider(ctx context.Context, name string) error {
	_ = ctx
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("provider instance name is required")
	}

	b.mu.Lock()
	if b.appConfig == nil || b.appConfig.Providers[name] == nil {
		b.mu.Unlock()
		return fmt.Errorf("provider %q is not configured", name)
	}
	removed := b.appConfig.Providers[name]
	delete(b.appConfig.Providers, name)
	// If this instance was the configured default, clear the default so we
	// don't point at a provider that no longer exists.
	if b.appConfig.General.DefaultProvider == name {
		b.appConfig.General.DefaultProvider = ""
		b.appConfig.General.DefaultModel = ""
	}
	b.mu.Unlock()

	if err := b.saveAppConfig(); err != nil {
		b.mu.Lock()
		b.appConfig.Providers[name] = removed
		b.mu.Unlock()
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

// Verify localAgentBackend implements the optional provider-admin capability.
var _ backend.ProviderAdminBackend = (*localAgentBackend)(nil)

// --- WebSearchAdminBackend (code-mode local web search configuration) ---
// These methods let the /websearch TUI overlay configure web search providers
// and persist them to the local config file (~/.config/astonish/config.yaml).
// After configuration, the user starts a /new session for the tools to load.

// Verify localAgentBackend implements the optional web-search-admin capability.
var _ backend.WebSearchAdminBackend = (*localAgentBackend)(nil)

func (b *localAgentBackend) ListWebSearchProviders(ctx context.Context) ([]backend.WebSearchProvider, error) {
	_ = ctx
	b.mu.Lock()
	activeRef := ""
	if b.appConfig != nil {
		activeRef = b.appConfig.General.WebSearchTool
	}
	perplexityConfigured := b.appConfig != nil &&
		b.appConfig.PerplexityWebSearch.Provider != "" &&
		b.appConfig.PerplexityWebSearch.Model != ""
	b.mu.Unlock()

	servers := config.GetStandardServers()
	out := make([]backend.WebSearchProvider, 0, len(servers))
	for _, srv := range servers {
		if srv.Category != "web" {
			continue
		}
		installed := false
		if srv.Kind == "model" && srv.ID == "perplexity" {
			installed = perplexityConfigured
		} else {
			installed = config.IsStandardServerInstalled(srv.ID)
		}
		active := activeRef != "" && strings.HasPrefix(activeRef, srv.ID+":")
		out = append(out, backend.WebSearchProvider{
			ID:          srv.ID,
			DisplayName: srv.DisplayName,
			Description: srv.Description,
			Kind:        srv.Kind,
			Installed:   installed,
			Active:      active,
			RequiresKey: len(srv.EnvVars) > 0,
		})
	}
	return out, nil
}

func (b *localAgentBackend) GetActiveWebSearch(ctx context.Context) (string, error) {
	_ = ctx
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.appConfig == nil {
		return "", nil
	}
	return b.appConfig.General.WebSearchTool, nil
}

func (b *localAgentBackend) InstallWebSearch(ctx context.Context, serverID, apiKey string) error {
	_ = ctx
	serverID = strings.TrimSpace(serverID)
	apiKey = strings.TrimSpace(apiKey)
	if serverID == "" {
		return fmt.Errorf("server ID is required")
	}

	srv := config.GetStandardServer(serverID)
	if srv == nil {
		return fmt.Errorf("unknown web search server: %s", serverID)
	}
	if srv.Kind == "model" {
		return fmt.Errorf("use ConfigurePerplexityWebSearch for model-backed providers")
	}

	// Build env values from the API key. Standard servers have a single key env var.
	envValues := make(map[string]string)
	if len(srv.EnvVars) > 0 {
		ev := srv.EnvVars[0]
		if ev.Required && apiKey == "" {
			return fmt.Errorf("%s is required", ev.Name)
		}
		envValues[ev.Name] = apiKey
	}

	// Try credential store first, fall back to config.yaml.
	storeKeyInConfig := true
	if b.result.CredentialStore != nil && apiKey != "" {
		storeKey := "web_servers." + serverID + ".api_key"
		if err := b.result.CredentialStore.SetSecret(storeKey, apiKey); err == nil {
			storeKeyInConfig = false
		}
	}

	if err := config.InstallStandardServer(serverID, envValues, storeKeyInConfig); err != nil {
		return fmt.Errorf("failed to install %s: %w", srv.DisplayName, err)
	}

	// Refresh in-memory config to reflect the change.
	b.mu.Lock()
	if b.appConfig.WebServers == nil {
		b.appConfig.WebServers = make(map[string]config.WebServerConfig)
	}
	ws := config.WebServerConfig{}
	if storeKeyInConfig {
		ws.APIKey = apiKey
	}
	b.appConfig.WebServers[serverID] = ws
	if srv.WebSearchTool != "" {
		b.appConfig.General.WebSearchTool = srv.WebSearchTool
	}
	if srv.WebExtractTool != "" {
		b.appConfig.General.WebExtractTool = srv.WebExtractTool
	}
	b.needsRebuild = true
	b.mu.Unlock()

	return nil
}

func (b *localAgentBackend) ConfigurePerplexityWebSearch(ctx context.Context, providerName, modelName string) error {
	_ = ctx
	providerName = strings.TrimSpace(providerName)
	modelName = strings.TrimSpace(modelName)
	if providerName == "" || modelName == "" {
		return fmt.Errorf("provider and model are required")
	}

	// Validate the model looks like a Perplexity/Sonar model.
	m := strings.ToLower(modelName)
	if !strings.Contains(m, "perplexity") && !strings.Contains(m, "sonar") && !strings.Contains(m, "pplx") {
		return fmt.Errorf("selected model must contain perplexity, sonar, or pplx")
	}

	b.mu.Lock()
	b.appConfig.PerplexityWebSearch = config.PerplexityWebSearchConfig{
		Provider:          providerName,
		Model:             modelName,
		SearchContextSize: "medium",
		MaxResults:        5,
	}
	b.appConfig.General.WebSearchTool = "perplexity:perplexity_web_search"
	b.needsRebuild = true
	b.mu.Unlock()

	if err := b.saveAppConfig(); err != nil {
		// Roll back in-memory.
		b.mu.Lock()
		b.appConfig.PerplexityWebSearch = config.PerplexityWebSearchConfig{}
		b.appConfig.General.WebSearchTool = ""
		b.mu.Unlock()
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

func (b *localAgentBackend) ListPerplexityOptions(ctx context.Context) ([]backend.PerplexityOption, error) {
	b.mu.Lock()
	appCfg := b.appConfig
	b.mu.Unlock()

	if appCfg == nil || len(appCfg.Providers) == 0 {
		return nil, nil
	}

	var opts []backend.PerplexityOption
	for name := range appCfg.Providers {
		models, err := provider.ListModelsForProvider(ctx, name, appCfg)
		if err != nil {
			continue
		}
		var filtered []string
		for _, model := range models {
			ml := strings.ToLower(model)
			if strings.Contains(ml, "perplexity") || strings.Contains(ml, "sonar") || strings.Contains(ml, "pplx") {
				filtered = append(filtered, model)
			}
		}
		if len(filtered) > 0 {
			sort.Strings(filtered)
			opts = append(opts, backend.PerplexityOption{
				Provider: name,
				Models:   filtered,
			})
		}
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].Provider < opts[j].Provider })
	return opts, nil
}

func (b *localAgentBackend) ClearWebSearch(ctx context.Context) error {
	_ = ctx
	b.mu.Lock()
	b.appConfig.General.WebSearchTool = ""
	b.appConfig.General.WebExtractTool = ""
	b.appConfig.PerplexityWebSearch = config.PerplexityWebSearchConfig{}
	b.needsRebuild = true
	b.mu.Unlock()

	if err := b.saveAppConfig(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}
