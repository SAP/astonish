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

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tools/ripgrep"
	"github.com/SAP/astonish/pkg/tui"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// codeAppName is the ADK app name used for local code-mode sessions. It is
// distinct from the Studio chat app name so the two never share session state.
const codeAppName = "astonish_code"

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
		AllowMissingProvider: true,
		LoadProjectContext:   true,
		SessionService:       fileStore,
	})
	if err != nil {
		return err
	}

	b := &localAgentBackend{
		result:      result,
		sessionSvc:  common.NewAutoInitService(result.SessionService),
		fileStore:   fileStore,
		checkpoints: checkpointStore,
		userID:      scopedUserID,
		appConfig:   appConfig,
		sessionID:   cfg.SessionID,
		autoApprove: cfg.AutoApprove,
		debug:       cfg.DebugMode,
		workingDir:  workingDir,
		provider:    result.ProviderName,
		model:       result.ModelName,
		configured:  result.ProviderConfigured,
		resumed:     cfg.SessionID != "",
		notices:     result.StartupNotices,
	}

	err = tui.Run(ctx, tui.Config{Backend: b})
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

	debug      bool
	workingDir string
	notices    []string

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
	resumed       bool
	closed        bool
}

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
		Usage:         cloneUsage(b.usage),
		ContextTokens: b.contextTokens,
		IsResumed:     b.resumed,
		AutoApprove:   b.autoApprove,
		Notices:       notices,
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

// effectiveUserID returns the per-directory session user ID, falling back to
// the base codeUserID when unset (e.g. tests that construct the backend
// directly). All session service calls route through this.
func (b *localAgentBackend) effectiveUserID() string {
	if b.userID != "" {
		return b.userID
	}
	return codeUserID
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

	// emit converts one (type, data) payload — exactly the shape ChatRunner
	// produces — into TUI events and pushes them onto out. This is the Option B
	// bridge: one shared translator (mapSSEToEvents), zero duplicated mapping.
	emit := func(eventType string, data map[string]any) {
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
			select {
			case out <- ev:
			case <-ctx.Done():
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
	if opts.SystemContext != "" || opts.PlanMode {
		ctx = agent.WithPromptOverrides(ctx, &agent.PromptOverrides{
			SessionContext: agent.EscapeCurlyPlaceholders(opts.SystemContext),
			PlanMode:       opts.PlanMode,
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

	// Determine the checkpoint boundary for this turn: the number of events
	// already in the session. Files modified during this turn are snapshotted
	// under this index, and rolling back to a user message at event position P
	// restores every snapshot with index >= P. Deriving both capture and
	// rollback from event position keeps them consistent regardless of how many
	// approval round-trips a turn takes.
	turnIndex := b.sessionEventCount(ctx, sessionID)
	if b.checkpoints != nil {
		b.checkpoints.BeginTurn(sessionID, turnIndex)
	}

	go func() {
		defer close(out)

		emit("session", map[string]any{"sessionId": sessionID, "isNew": isNew})
		// On a brand-new session, seed the index title from the first user
		// message so `/sessions` shows a meaningful label. Best-effort: title
		// failures never block the turn.
		if isNew && b.fileStore != nil {
			if title := deriveSessionTitle(message); title != "" {
				_ = b.fileStore.SetSessionTitle(ctx, sessionID, title)
			}
		}
		out <- events.NewStatus("Thinking…")

		b.driveTurn(ctx, rnr, chatAgent, sessionID, turnIndex, userMsg, emit)

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
					continue
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
		emit("approval", map[string]any{
			"tool":    toolName,
			"options": options,
		})
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
	// Most-recently-updated first.
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	out := make([]backend.SessionSummary, 0, len(metas))
	for _, m := range metas {
		title := m.Title
		if title == "" {
			title = "(untitled)"
		}
		updated := ""
		if !m.UpdatedAt.IsZero() {
			updated = m.UpdatedAt.Format("2006-01-02 15:04")
		}
		out = append(out, backend.SessionSummary{
			ID:           m.ID,
			Title:        title,
			UpdatedAt:    updated,
			MessageCount: m.MessageCount,
		})
	}
	return out, nil
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
	hist, err := b.loadHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// Estimate the resumed session's context occupancy so the header shows real
	// utilization immediately, instead of "Context 0" until the next turn.
	ctxTokens := b.estimateContextTokens(ctx, sessionID)
	b.mu.Lock()
	b.sessionID = sessionID
	b.resumed = true
	b.contextTokens = ctxTokens
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
	var out []backend.HistoryEntry
	for ev := range resp.Session.Events().All() {
		if ev == nil || ev.LLMResponse.Content == nil {
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
				out = append(out, backend.HistoryEntry{Kind: kind, Text: part.Text})
			case part.FunctionCall != nil:
				out = append(out, backend.HistoryEntry{
					Kind:     "tool_call",
					ToolName: part.FunctionCall.Name,
					Args:     part.FunctionCall.Args,
				})
			case part.FunctionResponse != nil:
				out = append(out, backend.HistoryEntry{
					Kind:     "tool_result",
					ToolName: part.FunctionResponse.Name,
					Result:   part.FunctionResponse.Response,
				})
			}
		}
	}
	return out, nil
}

func (b *localAgentBackend) DeleteSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}
	err := b.sessionSvc.Delete(ctx, &session.DeleteRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: sessionID,
	})
	if b.checkpoints != nil {
		_ = b.checkpoints.DeleteSession(sessionID)
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
		if saveErr := config.SaveAppConfig(b.appConfig); saveErr != nil && b.debug {
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

// ListRollbackPoints returns one revert target per user message in the active
// session, oldest first, annotated with how many files a rollback would
// restore. Returns nil when there is no active session or nothing to revert.
func (b *localAgentBackend) ListRollbackPoints(ctx context.Context) ([]backend.RollbackPoint, error) {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	if sessionID == "" {
		return nil, nil
	}
	resp, err := b.sessionSvc.Get(ctx, &session.GetRequest{
		AppName:   codeAppName,
		UserID:    b.effectiveUserID(),
		SessionID: sessionID,
	})
	if err != nil || resp == nil || resp.Session == nil {
		return nil, nil
	}

	var points []backend.RollbackPoint
	turnNumber := 0
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
			continue // skip tool-response / empty user events
		}
		turnNumber++
		label := deriveSessionTitle(text)
		ts := ""
		if !ev.Timestamp.IsZero() {
			ts = ev.Timestamp.Format("15:04:05")
		}
		fileCount := 0
		if b.checkpoints != nil {
			fileCount = b.checkpoints.FileCountFrom(sessionID, idx)
		}
		points = append(points, backend.RollbackPoint{
			ID:         fmt.Sprintf("%d", idx),
			Label:      label,
			Timestamp:  ts,
			FileCount:  fileCount,
			TurnNumber: turnNumber,
		})
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
// by pointID (the event index of a user message). It truncates the session to
// the events before that message, restores every file snapshot captured at or
// after that point, then returns the rebuilt history for the truncated session.
func (b *localAgentBackend) RollbackTo(ctx context.Context, pointID string) ([]backend.HistoryEntry, error) {
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()
	if sessionID == "" {
		return nil, fmt.Errorf("no active session")
	}
	targetIdx, err := strconv.Atoi(strings.TrimSpace(pointID))
	if err != nil || targetIdx < 0 {
		return nil, fmt.Errorf("invalid rollback point %q", pointID)
	}

	// 1) Revert file changes made at or after the target message.
	if b.checkpoints != nil {
		if _, err := b.checkpoints.RestoreTo(sessionID, targetIdx); err != nil {
			return nil, fmt.Errorf("failed to restore files: %w", err)
		}
	}

	// 2) Truncate the conversation to the events before the target message.
	if b.fileStore != nil {
		if _, err := b.fileStore.TruncateEvents(codeAppName, b.effectiveUserID(), sessionID, targetIdx); err != nil {
			return nil, fmt.Errorf("failed to truncate session: %w", err)
		}
	}

	// 3) Reset accumulated usage — the token count no longer reflects the
	// truncated context. It will be re-estimated on the next turn.
	b.mu.Lock()
	b.usage = nil
	b.mu.Unlock()

	// 4) Return the rebuilt history for the now-shorter session.
	return b.loadHistory(ctx, sessionID)
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
	if b.appConfig == nil {
		return nil, nil
	}
	out := make([]backend.ProviderInstance, 0, len(b.appConfig.Providers))
	for name, inst := range b.appConfig.Providers {
		if name == "" || strings.HasPrefix(name, "__") {
			continue
		}
		out = append(out, backend.ProviderInstance{
			Name: name,
			Type: config.GetProviderType(name, inst),
		})
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
	appCfg := b.appConfig
	b.mu.Unlock()

	if err := config.SaveAppConfig(appCfg); err != nil {
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
	appCfg := b.appConfig
	b.mu.Unlock()

	if err := config.SaveAppConfig(appCfg); err != nil {
		b.mu.Lock()
		b.appConfig.Providers[name] = removed
		b.mu.Unlock()
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

// Verify localAgentBackend implements the optional provider-admin capability.
var _ backend.ProviderAdminBackend = (*localAgentBackend)(nil)
