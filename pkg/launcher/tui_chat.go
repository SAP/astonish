package launcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/tui"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// ChatConfig configures the platform-backed interactive chat TUI.
// Chat always talks to an authenticated Astonish platform (Studio REST/SSE) —
// there is no in-process / "personal" chat path.
type ChatConfig struct {
	AutoApprove bool
	SessionID   string // Resume existing session (empty = new on first message)
	DebugMode   bool
}

// RunChatTUI launches the fullscreen terminal chat app against the logged-in
// Astonish platform (local install or cloud — same client, requires auth).
func RunChatTUI(ctx context.Context, cfg *ChatConfig) error {
	if cfg == nil {
		cfg = &ChatConfig{}
	}
	c, err := client.New()
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	remoteCfg, _ := client.LoadRemoteConfig()
	serverURL, org, team, user := "", "", "", ""
	if remoteCfg != nil {
		serverURL = remoteCfg.URL
		org = remoteCfg.Org
		team = remoteCfg.Team
		user = remoteCfg.UserEmail
	}

	b := &platformBackend{
		client:      c,
		sessionID:   cfg.SessionID,
		autoApprove: cfg.AutoApprove,
		debug:       cfg.DebugMode,
		serverURL:   serverURL,
		org:         org,
		team:        team,
		user:        user,
		resumed:     cfg.SessionID != "",
	}
	// Provide local code mode as an alt backend so Ctrl+\ can switch to it.
	// The code backend is lazily initialized on first switch.
	var altBackend backend.Backend
	if lb := newLazyCodeBackend(); lb != nil {
		altBackend = lb
	}
	return tui.Run(ctx, tui.Config{Backend: b, AltBackend: altBackend})
}

// newPlatformBackend creates a platformBackend for use as the primary or alt
// backend. It requires that the user is already logged in (client.IsRemoteMode).
// Returns nil without error when not in remote mode (graceful degradation).
func newPlatformBackend() backend.Backend {
	if !client.IsRemoteMode() {
		return nil
	}
	c, err := client.New()
	if err != nil {
		return nil
	}
	remoteCfg, _ := client.LoadRemoteConfig()
	serverURL, org, team, user := "", "", "", ""
	if remoteCfg != nil {
		serverURL = remoteCfg.URL
		org = remoteCfg.Org
		team = remoteCfg.Team
		user = remoteCfg.UserEmail
	}
	return &platformBackend{
		client:    c,
		serverURL: serverURL,
		org:       org,
		team:      team,
		user:      user,
	}
}

// newLazyCodeBackend returns a backend that lazily initializes the full local
// code-mode agent on first Open(). Returns nil if code mode cannot be set up
// (e.g. missing config). This allows `astonish chat` to offer Ctrl+\ switching
// to code mode without paying the startup cost unless the user actually switches.
func newLazyCodeBackend() backend.Backend {
	return &lazyCodeBackend{}
}

// lazyCodeBackend wraps localAgentBackend with deferred initialization. All
// backend.Backend methods delegate to the inner backend once Open() is called.
type lazyCodeBackend struct {
	inner backend.Backend
}

func (b *lazyCodeBackend) Info() backend.Info {
	if b.inner != nil {
		return b.inner.Info()
	}
	return backend.Info{Mode: "code"}
}

func (b *lazyCodeBackend) Open(ctx context.Context) error {
	if b.inner != nil {
		return nil // already opened
	}
	// Build the full code-mode backend. This mirrors RunCodeTUI's setup but
	// runs lazily on first switch.
	cfg := &CodeConfig{}
	inner, err := buildCodeBackend(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize code mode: %w", err)
	}
	b.inner = inner
	return b.inner.Open(ctx)
}

func (b *lazyCodeBackend) RecordPlanDecision(ctx context.Context, status events.PlanStatus) error {
	if inner, ok := b.inner.(backend.PlanLifecycleBackend); ok {
		return inner.RecordPlanDecision(ctx, status)
	}
	return nil
}

func (b *lazyCodeBackend) RunTurn(ctx context.Context, msg string, opts backend.TurnOptions) (<-chan events.Event, error) {
	if b.inner == nil {
		return nil, fmt.Errorf("code backend not opened")
	}
	return b.inner.RunTurn(ctx, msg, opts)
}

func (b *lazyCodeBackend) ListSessions(ctx context.Context) ([]backend.SessionSummary, error) {
	if b.inner == nil {
		return nil, nil
	}
	return b.inner.ListSessions(ctx)
}

func (b *lazyCodeBackend) LoadHistory(ctx context.Context) ([]backend.HistoryEntry, error) {
	if b.inner == nil {
		return nil, nil
	}
	return b.inner.LoadHistory(ctx)
}

func (b *lazyCodeBackend) ResumeSession(ctx context.Context, id string) ([]backend.HistoryEntry, error) {
	if b.inner == nil {
		return nil, fmt.Errorf("code backend not opened")
	}
	return b.inner.ResumeSession(ctx, id)
}

func (b *lazyCodeBackend) DeleteSession(ctx context.Context, id string) error {
	if b.inner == nil {
		return fmt.Errorf("code backend not opened")
	}
	return b.inner.DeleteSession(ctx, id)
}

func (b *lazyCodeBackend) NewSession() {
	if b.inner != nil {
		b.inner.NewSession()
	}
}

func (b *lazyCodeBackend) ListProviders(ctx context.Context) ([]string, error) {
	if b.inner == nil {
		return nil, nil
	}
	return b.inner.ListProviders(ctx)
}

func (b *lazyCodeBackend) ListModels(ctx context.Context, provider string) ([]string, error) {
	if b.inner == nil {
		return nil, nil
	}
	return b.inner.ListModels(ctx, provider)
}

func (b *lazyCodeBackend) SetModelPin(ctx context.Context, provider, model string) (string, string, error) {
	if b.inner == nil {
		return "", "", fmt.Errorf("code backend not opened")
	}
	return b.inner.SetModelPin(ctx, provider, model)
}

func (b *lazyCodeBackend) ReadArtifactContent(ctx context.Context, sessionID, path string) (backend.ArtifactContent, error) {
	if b.inner == nil {
		return backend.ArtifactContent{}, fmt.Errorf("code backend not opened")
	}
	return b.inner.ReadArtifactContent(ctx, sessionID, path)
}

func (b *lazyCodeBackend) Close() error {
	if b.inner != nil {
		return b.inner.Close()
	}
	return nil
}

// RespondSubAgentAuth delegates to the inner backend if initialized.
func (b *lazyCodeBackend) RespondSubAgentAuth(choice string) bool {
	if b.inner != nil {
		return b.inner.RespondSubAgentAuth(choice)
	}
	return false
}

// platformBackend implements backend.Backend over Studio REST/SSE.
type platformBackend struct {
	client      *client.Client
	sessionID   string
	autoApprove bool
	debug       bool
	serverURL   string
	org         string
	team        string
	user        string
	usage       *events.Usage
	provider    string
	model       string
	resumed     bool
	// pendingProvider/Model are applied on the next new-session turn when no
	// session id exists yet (pre-chat model pick).
	pendingProvider string
	pendingModel    string

	mu     sync.Mutex
	closed bool
}

func (b *platformBackend) Info() backend.Info {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backend.Info{
		SessionID:   b.sessionID,
		Provider:    b.provider,
		Model:       b.model,
		Mode:        "platform",
		ServerURL:   b.serverURL,
		Org:         b.org,
		Team:        b.team,
		User:        b.user,
		Usage:       cloneUsage(b.usage),
		IsResumed:   b.resumed,
		AutoApprove: b.autoApprove,
		Notices:     nil,
	}
}

func (b *platformBackend) Open(ctx context.Context) error {
	// Connectivity check — fail fast if the platform is unreachable / auth expired.
	if err := b.client.Ping(); err != nil {
		return fmt.Errorf("platform unreachable (%s): %w\nRun: astonish login <url>", b.serverURL, err)
	}
	b.loadInitialModelStatus(ctx)
	return nil
}

func (b *platformBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

// RespondSubAgentAuth is a no-op for platform backends (sub-agent auth is
// only enforced in code-mode TUI).
func (b *platformBackend) RespondSubAgentAuth(choice string) bool { return false }

func (b *platformBackend) loadInitialModelStatus(ctx context.Context) {
	_ = ctx
	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	if sessionID == "" {
		b.loadCascadeModelStatus()
		return
	}
	b.refreshSessionModelStatus(sessionID)
}

func (b *platformBackend) setModel(provider, modelName string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Always overwrite so session switches replace a previous pin instead of
	// leaving a stale provider/model in the footer.
	b.provider = provider
	b.model = modelName
}

func (b *platformBackend) refreshSessionModelStatus(sessionID string) {
	if sessionID == "" {
		b.loadCascadeModelStatus()
		return
	}
	status, err := b.client.GetSessionModelStatus(sessionID)
	if err != nil || status == nil {
		return
	}
	b.setModel(status.EffectiveProvider, status.EffectiveModel)
}

// loadCascadeModelStatus sets the footer to the platform cascade defaults used
// for a brand-new session (no pin).
func (b *platformBackend) loadCascadeModelStatus() {
	providers, err := b.client.GetEffectiveProviders()
	if err != nil || providers == nil {
		b.setModel("", "")
		return
	}
	b.setModel(providers.DefaultProvider, providers.DefaultModel)
}

func (b *platformBackend) ListSessions(ctx context.Context) ([]backend.SessionSummary, error) {
	_ = ctx
	list, err := b.client.ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]backend.SessionSummary, 0, len(list))
	for _, s := range list {
		// Skip sub-agent / child sessions in the picker.
		if s.ParentID != "" {
			continue
		}
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		out = append(out, backend.SessionSummary{
			ID:           s.ID,
			Title:        title,
			UpdatedAt:    s.UpdatedAt,
			MessageCount: s.MessageCount,
		})
	}
	return out, nil
}

func (b *platformBackend) LoadHistory(ctx context.Context) ([]backend.HistoryEntry, error) {
	b.mu.Lock()
	id := b.sessionID
	b.mu.Unlock()
	if id == "" {
		return nil, nil
	}
	return b.loadHistory(ctx, id)
}

func (b *platformBackend) ResumeSession(ctx context.Context, sessionID string) ([]backend.HistoryEntry, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	hist, err := b.loadHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.sessionID = sessionID
	b.resumed = true
	// Drop any pending pre-chat pin; resumed session has its own pin/cascade.
	b.pendingProvider = ""
	b.pendingModel = ""
	b.mu.Unlock()
	// Always refresh so the footer shows this session's effective model.
	b.refreshSessionModelStatus(sessionID)
	return hist, nil
}

func (b *platformBackend) DeleteSession(ctx context.Context, sessionID string) error {
	_ = ctx
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}
	if err := b.client.DeleteSession(sessionID); err != nil {
		return err
	}
	b.mu.Lock()
	wasActive := b.sessionID == sessionID
	if wasActive {
		b.sessionID = ""
		b.usage = nil
		b.resumed = false
		b.pendingProvider = ""
		b.pendingModel = ""
	}
	b.mu.Unlock()
	if wasActive {
		// Active session deleted → new blank chat uses cascade defaults.
		b.loadCascadeModelStatus()
	}
	return nil
}

func (b *platformBackend) ListProviders(ctx context.Context) ([]string, error) {
	_ = ctx
	resp, err := b.client.GetEffectiveProviders()
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	out := make([]string, 0, len(resp.Providers))
	for name := range resp.Providers {
		if name == "" || strings.HasPrefix(name, "__") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (b *platformBackend) ListModels(ctx context.Context, provider string) ([]string, error) {
	_ = ctx
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, fmt.Errorf("provider required")
	}
	models, err := b.client.ListProviderModels(provider)
	if err != nil {
		return nil, err
	}
	return models, nil
}

func (b *platformBackend) SetModelPin(ctx context.Context, provider, model string) (string, string, error) {
	_ = ctx
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)

	b.mu.Lock()
	sessionID := b.sessionID
	b.mu.Unlock()

	if sessionID == "" {
		// No session yet: keep a pending pin for the first chat turn.
		b.mu.Lock()
		b.pendingProvider = provider
		b.pendingModel = model
		b.mu.Unlock()
		if provider == "" && model == "" {
			b.loadCascadeModelStatus()
			info := b.Info()
			return info.Provider, info.Model, nil
		}
		b.setModel(provider, model)
		return provider, model, nil
	}

	resp, err := b.client.PatchSessionModel(sessionID, provider, model)
	if err != nil {
		return "", "", err
	}
	effP, effM := provider, model
	if resp != nil {
		if resp.EffectiveProvider != "" {
			effP = resp.EffectiveProvider
		}
		if resp.EffectiveModel != "" {
			effM = resp.EffectiveModel
		}
	}
	b.setModel(effP, effM)
	b.mu.Lock()
	b.pendingProvider = ""
	b.pendingModel = ""
	b.mu.Unlock()
	return effP, effM, nil
}

func (b *platformBackend) ReadArtifactContent(ctx context.Context, sessionID, path string) (backend.ArtifactContent, error) {
	_ = ctx
	path = strings.TrimSpace(path)
	if path == "" {
		return backend.ArtifactContent{}, fmt.Errorf("artifact path required")
	}
	if sessionID == "" {
		b.mu.Lock()
		sessionID = b.sessionID
		b.mu.Unlock()
	}
	content, err := b.client.GetArtifactContent(path, sessionID)
	if err != nil {
		return backend.ArtifactContent{}, err
	}
	return backend.ArtifactContent{Path: path, Content: content}, nil
}

func (b *platformBackend) NewSession() {
	b.mu.Lock()
	b.sessionID = ""
	b.usage = nil
	b.resumed = false
	// A brand-new session should not inherit a previous session pin or a
	// pending pin from an earlier picker selection on another conversation.
	b.pendingProvider = ""
	b.pendingModel = ""
	b.mu.Unlock()
	b.loadCascadeModelStatus()
}

func (b *platformBackend) ApproveNetworkGrant(ctx context.Context, sessionID string, denial events.NetworkDenial, broader bool, sandboxName string) error {
	_ = ctx
	if sessionID == "" {
		b.mu.Lock()
		sessionID = b.sessionID
		b.mu.Unlock()
	}
	if sessionID == "" {
		return fmt.Errorf("session id required for network approval")
	}
	if broader || denial.ChunkID == "" {
		host := denial.Host
		if broader && denial.BroaderPattern != "" {
			host = denial.BroaderPattern
		}
		if host == "" {
			return fmt.Errorf("host required for network approval")
		}
		return b.client.ApproveNetworkGrantBroader(sessionID, host, denial.Port, sandboxName)
	}
	return b.client.ApproveNetworkGrant(sessionID, denial.ChunkID, sandboxName)
}

func (b *platformBackend) DenyNetworkGrant(ctx context.Context, sessionID string, denial events.NetworkDenial, sandboxName string) error {
	_ = ctx
	if sessionID == "" {
		b.mu.Lock()
		sessionID = b.sessionID
		b.mu.Unlock()
	}
	if sessionID == "" {
		return fmt.Errorf("session id required for network denial")
	}
	return b.client.DenyNetworkGrant(sessionID, denial.ChunkID, sandboxName, "denied from terminal")
}

func (b *platformBackend) loadHistory(ctx context.Context, id string) ([]backend.HistoryEntry, error) {
	_ = ctx
	detail, err := b.client.GetSessionDetail(id)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	// Keep title available via Info if we stash it — optional.
	_ = detail.Title
	b.usage = usageFromSessionDetail(detail)
	b.mu.Unlock()
	return studioDetailToHistory(detail), nil
}

func usageFromSessionDetail(detail *client.SessionDetail) *events.Usage {
	if detail == nil || detail.TotalUsage == nil {
		return nil
	}
	return &events.Usage{
		Input:  detail.TotalUsage.InputTokens,
		Output: detail.TotalUsage.OutputTokens,
		Total:  detail.TotalUsage.TotalTokens,
	}
}

func networkDenialFromClient(d client.NetworkDenial) events.NetworkDenial {
	return events.NetworkDenial{
		ChunkID:        d.ChunkID,
		Host:           d.Host,
		Port:           d.Port,
		Binary:         d.Binary,
		Rationale:      d.Rationale,
		SecurityNotes:  d.SecurityNotes,
		BroaderPattern: d.BroaderPattern,
	}
}

func cloneUsage(usage *events.Usage) *events.Usage {
	if usage == nil {
		return nil
	}
	return &events.Usage{Input: usage.Input, Output: usage.Output, Total: usage.Total}
}

func addUsage(total, delta *events.Usage) *events.Usage {
	if delta == nil {
		return cloneUsage(total)
	}
	if total == nil {
		total = &events.Usage{}
	} else {
		total = cloneUsage(total)
	}
	total.Input += delta.Input
	total.Output += delta.Output
	total.Total += delta.Total
	return total
}

func studioDetailToHistory(detail *client.SessionDetail) []backend.HistoryEntry {
	if detail == nil {
		return nil
	}
	out := studioMessagesToHistory(detail.Messages)
	for _, a := range detail.Artifacts {
		artifact := artifactFromClient(a)
		if artifact.Path == "" {
			continue
		}
		out = append(out, backend.HistoryEntry{Kind: "artifact", Text: artifact.Path, Artifact: &artifact})
	}
	return out
}

func studioMessagesToHistory(msgs []client.StudioMessage) []backend.HistoryEntry {
	out := make([]backend.HistoryEntry, 0, len(msgs))
	for _, m := range msgs {
		switch m.Type {
		case "user", "agent", "system", "thinking":
			if strings.TrimSpace(m.Content) == "" && m.Type != "thinking" {
				continue
			}
			out = append(out, backend.HistoryEntry{
				Kind: m.Type,
				Text: m.Content,
			})
		case "tool_call":
			args, _ := m.ToolArgs.(map[string]any)
			if args == nil {
				// JSON may decode as map[string]interface{} already; try generic convert.
				if raw, ok := m.ToolArgs.(map[string]interface{}); ok {
					args = raw
				}
			}
			out = append(out, backend.HistoryEntry{
				Kind:     "tool_call",
				ToolName: m.ToolName,
				ToolID:   m.ToolID,
				Args:     args,
			})
		case "tool_result":
			out = append(out, backend.HistoryEntry{
				Kind:     "tool_result",
				ToolName: m.ToolName,
				ToolID:   m.ToolID,
				Result:   m.ToolResult,
			})
		default:
			// Soft-degrade unknown history types.
			if m.Content != "" {
				out = append(out, backend.HistoryEntry{Kind: "system", Text: m.Content})
			}
		}
	}
	return out
}

func artifactFromClient(a client.ArtifactInfo) events.Artifact {
	return events.Artifact{
		Path:        a.Path,
		FileName:    a.FileName,
		FileType:    a.FileType,
		ToolName:    a.ToolName,
		IsReport:    a.IsReport,
		ReportTitle: a.ReportTitle,
	}
}

func (b *platformBackend) RunTurn(ctx context.Context, message string, opts backend.TurnOptions) (<-chan events.Event, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("backend closed")
	}
	sessionID := b.sessionID
	autoApprove := b.autoApprove
	debug := b.debug
	c := b.client
	pendingProvider := b.pendingProvider
	pendingModel := b.pendingModel
	b.mu.Unlock()

	req := &client.ChatRequest{
		SessionID:     sessionID,
		Message:       message,
		AutoApprove:   autoApprove,
		Debug:         debug,
		SystemContext: opts.SystemContext,
		PlanMode:      opts.PlanMode,
		Attachments:   chatAttachmentsFromBackend(opts.Attachments),
	}
	// Apply pre-session model pin on the first turn of a new session.
	if sessionID == "" && (pendingProvider != "" || pendingModel != "") {
		req.Provider = pendingProvider
		req.Model = pendingModel
	}

	stream, err := c.SendChatMessage(req)
	if err != nil {
		return nil, err
	}

	out := make(chan events.Event, 64)
	go func() {
		defer close(out)
		defer func() { _ = stream.Close() }()

		out <- events.NewStatus("Thinking…")

		for {
			if ctx.Err() != nil {
				_ = stream.Close()
				out <- events.NewSystem("Turn cancelled.")
				out <- events.NewDone()
				return
			}

			sev, err := stream.Next()
			if err != nil {
				if err != io.EOF && sessionID != "" {
					if reconnected, ok := b.tryReconnectStream(ctx, out, sessionID, debug, err); ok {
						_ = stream.Close()
						stream = reconnected
						continue
					}
				}
				if err != io.EOF {
					out <- events.NewError(err.Error())
				}
				out <- events.NewDone()
				return
			}

			for _, ev := range mapSSEToEvents(sev, debug) {
				if ev.Kind == events.KindNetworkDenial && len(ev.NetworkDenials) == 0 {
					ev = b.hydrateNetworkDenialEvent(ev, sessionID)
				}
				// Keep session id in sync for subsequent turns.
				if ev.Kind == events.KindSession && ev.SessionID != "" {
					b.mu.Lock()
					b.sessionID = ev.SessionID
					// Pending pin was sent on this first turn; clear local pending state.
					b.pendingProvider = ""
					b.pendingModel = ""
					b.mu.Unlock()
					sessionID = ev.SessionID
					b.refreshSessionModelStatus(sessionID)
				}
				if ev.Kind == events.KindModelChanged {
					b.setModel(ev.Provider, ev.Model)
				}
				if ev.Kind == events.KindUsage && ev.Usage != nil {
					b.mu.Lock()
					b.usage = addUsage(b.usage, ev.Usage)
					b.mu.Unlock()
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					_ = stream.Close()
					out <- events.NewDone()
					return
				}
			}
		}
	}()

	return out, nil
}

func (b *platformBackend) hydrateNetworkDenialEvent(ev events.Event, fallbackSessionID string) events.Event {
	sessionID := ev.SessionID
	if sessionID == "" {
		sessionID = fallbackSessionID
	}
	if sessionID == "" {
		return ev
	}
	resp, err := b.client.GetNetworkDenials(sessionID)
	if err != nil || resp == nil || len(resp.Denials) == 0 {
		return ev
	}
	denials := make([]events.NetworkDenial, 0, len(resp.Denials))
	for _, d := range resp.Denials {
		denials = append(denials, networkDenialFromClient(d))
	}
	ev.SessionID = sessionID
	if ev.SandboxName == "" {
		ev.SandboxName = resp.SandboxName
	}
	ev.NetworkDenials = denials
	return ev
}

func (b *platformBackend) tryReconnectStream(ctx context.Context, out chan<- events.Event, sessionID string, debug bool, cause error) (*client.SSEStream, bool) {
	select {
	case <-ctx.Done():
		return nil, false
	default:
	}

	running, statusErr := b.client.GetSessionStatus(sessionID)
	if statusErr != nil || !running {
		return nil, false
	}
	out <- events.NewStatus("Connection dropped; reconnecting…")
	stream, err := b.client.ReconnectSession(sessionID)
	if err != nil {
		if debug {
			out <- events.NewSystem(fmt.Sprintf("Reconnect failed after stream error %q: %v", cause, err))
		}
		return nil, false
	}
	out <- events.NewStatus("Reconnected…")
	return stream, true
}

// mapSSEToEvents converts one Studio SSE frame into zero or more TUI events.
// Exported for tests via mapSSEToEvents (same package tests).
func mapSSEToEvents(sev *client.SSEEvent, debug bool) []events.Event {
	if sev == nil {
		return nil
	}
	data := []byte(sev.Data)
	switch sev.Type {
	case "session":
		var payload struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.SessionID != "" {
			return []events.Event{{Kind: events.KindSession, SessionID: payload.SessionID}}
		}
	case "session_title":
		var payload struct {
			Title     string `json:"title"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &payload) == nil {
			return []events.Event{{
				Kind:      events.KindSessionTitle,
				Title:     payload.Title,
				SessionID: payload.SessionID,
			}}
		}
	case "text":
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Text != "" {
			// Skip legacy tool-box ANSI frames — activity fold replaces them.
			if containsToolBoxFrame(payload.Text) {
				return nil
			}
			return []events.Event{events.NewText(payload.Text)}
		}
	case "tool_call":
		var payload struct {
			Name string         `json:"name"`
			ID   string         `json:"id"`
			Args map[string]any `json:"args"`
		}
		if json.Unmarshal(data, &payload) == nil {
			name := payload.Name
			return []events.Event{
				events.NewToolCall(name, payload.ID, payload.Args),
				events.NewStatus(fmt.Sprintf("Running %s…", firstNonEmpty(name, "tool"))),
			}
		}
	case "tool_result":
		var payload struct {
			Name   string `json:"name"`
			ID     string `json:"id"`
			Result any    `json:"result"`
		}
		if json.Unmarshal(data, &payload) == nil {
			return []events.Event{events.NewToolResult(payload.Name, payload.ID, payload.Result)}
		}
	case "approval":
		var payload struct {
			Tool    string         `json:"name"`
			Name    string         `json:"tool"`
			Args    map[string]any `json:"args"`
			Options []string       `json:"options"`
			Kind    string         `json:"kind"`
			Paths   []string       `json:"paths"`
		}
		if json.Unmarshal(data, &payload) == nil {
			tool := payload.Tool
			if tool == "" {
				tool = payload.Name
			}
			if payload.Kind != "" {
				return []events.Event{events.NewAuthorizationApproval(tool, payload.Args, payload.Options, payload.Kind, payload.Paths)}
			}
			return []events.Event{events.NewApproval(tool, payload.Args, payload.Options)}
		}
	case "auto_approved":
		var payload struct {
			Name string `json:"name"`
			Tool string `json:"tool"`
		}
		if json.Unmarshal(data, &payload) == nil {
			tool := payload.Name
			if tool == "" {
				tool = payload.Tool
			}
			return []events.Event{{Kind: events.KindAutoApproved, ToolName: tool}}
		}
	case "network_denial_hint":
		var payload struct {
			SessionID   string                 `json:"session_id"`
			SessionID2  string                 `json:"sessionId"`
			SandboxName string                 `json:"sandbox_name"`
			Denials     []client.NetworkDenial `json:"denials"`
		}
		if json.Unmarshal(data, &payload) == nil {
			sessionID := payload.SessionID
			if sessionID == "" {
				sessionID = payload.SessionID2
			}
			denials := make([]events.NetworkDenial, 0, len(payload.Denials))
			for _, d := range payload.Denials {
				denials = append(denials, networkDenialFromClient(d))
			}
			if len(denials) > 0 {
				return []events.Event{events.NewNetworkDenial(sessionID, payload.SandboxName, denials)}
			}
			if sessionID != "" {
				return []events.Event{events.NewNetworkDenial(sessionID, payload.SandboxName, nil)}
			}
		}
	case "thinking":
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &payload) == nil {
			text := payload.Text
			if text == "" {
				text = "Thinking…"
			}
			return []events.Event{events.NewStatus(text)}
		}
	case "subtask_progress":
		var payload struct {
			EventType string `json:"event_type"`
			TaskName  string `json:"task_name"`
		}
		if json.Unmarshal(data, &payload) == nil {
			if payload.EventType == "task_start" && payload.TaskName != "" {
				return []events.Event{
					{Kind: events.KindSubagent, ToolName: payload.TaskName, Text: payload.TaskName},
					events.NewStatus(fmt.Sprintf("Working: %s…", payload.TaskName)),
				}
			}
		}
	case "delegation":
		var payload struct {
			Type     string `json:"type"`
			TaskName string `json:"task_name"`
			Duration string `json:"duration"`
			Error    string `json:"error"`
			Tasks    []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				PlanStep    string `json:"plan_step"`
			} `json:"tasks"`
			ToolName   string         `json:"tool_name"`
			ToolArgs   map[string]any `json:"tool_args"`
			ToolResult any            `json:"tool_result"`
			Text       string         `json:"text"`
		}
		if json.Unmarshal(data, &payload) == nil {
			switch payload.Type {
			case "start":
				tasks := make([]events.DelegationTask, len(payload.Tasks))
				for i, t := range payload.Tasks {
					tasks[i] = events.DelegationTask{Name: t.Name, Description: t.Description, PlanStep: t.PlanStep}
				}
				return []events.Event{events.NewDelegationStart(tasks)}
			case "task_start":
				return []events.Event{events.NewDelegationTaskUpdate("task_start", payload.TaskName, "", "")}
			case "task_complete":
				return []events.Event{events.NewDelegationTaskUpdate("task_complete", payload.TaskName, payload.Duration, "")}
			case "task_failed":
				return []events.Event{events.NewDelegationTaskUpdate("task_failed", payload.TaskName, payload.Duration, payload.Error)}
			case "task_tool_call":
				return []events.Event{events.NewDelegationTaskActivity("task_tool_call", payload.TaskName, payload.ToolName, payload.ToolArgs, nil, "")}
			case "task_tool_result":
				return []events.Event{events.NewDelegationTaskActivity("task_tool_result", payload.TaskName, payload.ToolName, nil, payload.ToolResult, "")}
			case "task_text":
				return []events.Event{events.NewDelegationTaskActivity("task_text", payload.TaskName, "", nil, nil, payload.Text)}
			case "done":
				return []events.Event{events.NewDelegation("done")}
			}
		}
	case "report_marker":
		var payload struct {
			Path  string `json:"path"`
			Title string `json:"title"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Path != "" {
			artifact := events.Artifact{Path: payload.Path, IsReport: true, ReportTitle: payload.Title}
			return []events.Event{{
				Kind:     events.KindReportMarker,
				Text:     payload.Path,
				Artifact: &artifact,
				Meta:     map[string]any{"path": payload.Path, "title": payload.Title},
			}}
		}
	case "artifact":
		var payload struct {
			Path        string `json:"path"`
			ToolName    string `json:"tool_name"`
			ToolName2   string `json:"toolName"`
			FileName    string `json:"fileName"`
			FileType    string `json:"fileType"`
			IsReport    bool   `json:"isReport"`
			ReportTitle string `json:"reportTitle"`
		}
		if json.Unmarshal(data, &payload) == nil {
			path := payload.Path
			if path == "" {
				path = payload.FileName
			}
			toolName := payload.ToolName
			if toolName == "" {
				toolName = payload.ToolName2
			}
			artifact := events.Artifact{
				Path:        path,
				FileName:    payload.FileName,
				FileType:    payload.FileType,
				ToolName:    toolName,
				IsReport:    payload.IsReport,
				ReportTitle: payload.ReportTitle,
			}
			return []events.Event{{
				Kind:     events.KindArtifact,
				Text:     path,
				Artifact: &artifact,
				Meta:     map[string]any{"path": path, "tool": toolName},
			}}
		}
	case "error":
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &payload) == nil {
			msg := payload.Error
			if msg == "" {
				msg = string(data)
			}
			return []events.Event{events.NewError(msg)}
		}
	case "error_info":
		var payload struct {
			Title      string `json:"title"`
			Reason     string `json:"reason"`
			Suggestion string `json:"suggestion"`
			Error      string `json:"originalError"`
		}
		if json.Unmarshal(data, &payload) == nil {
			return []events.Event{{
				Kind:            events.KindErrorInfo,
				ErrorTitle:      payload.Title,
				ErrorReason:     payload.Reason,
				ErrorSuggestion: payload.Suggestion,
				Text:            payload.Error,
			}}
		}
	case "usage":
		var payload struct {
			Input        int64 `json:"input"`
			Output       int64 `json:"output"`
			Total        int64 `json:"total"`
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
			Estimated    bool  `json:"estimated"`
		}
		if json.Unmarshal(data, &payload) == nil {
			input := firstNonZero(payload.Input, payload.InputTokens)
			output := firstNonZero(payload.Output, payload.OutputTokens)
			total := firstNonZero(payload.Total, payload.TotalTokens)
			if total == 0 && (input != 0 || output != 0) {
				total = input + output
			}
			return []events.Event{{
				Kind: events.KindUsage,
				Usage: &events.Usage{
					Input:     input,
					Output:    output,
					Total:     total,
					Estimated: payload.Estimated,
				},
			}}
		}
	case "model_changed":
		var payload struct {
			EffectiveProvider string `json:"effectiveProvider"`
			EffectiveModel    string `json:"effectiveModel"`
			PinnedProvider    string `json:"pinnedProvider"`
			PinnedModel       string `json:"pinnedModel"`
		}
		if json.Unmarshal(data, &payload) == nil {
			p := payload.EffectiveProvider
			m := payload.EffectiveModel
			if p == "" {
				p = payload.PinnedProvider
			}
			if m == "" {
				m = payload.PinnedModel
			}
			return []events.Event{{Kind: events.KindModelChanged, Provider: p, Model: m}}
		}
	case "system":
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Text != "" {
			return []events.Event{events.NewSystem(payload.Text)}
		}
	case "plan":
		var payload struct {
			Text             string            `json:"text"`
			Status           events.PlanStatus `json:"status"`
			PlanContext      string            `json:"plan_context"`
			PlanWhatNotToDo  string            `json:"plan_what_not_to_do"`
			PlanVerification string            `json:"plan_verification"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Text != "" {
			ev := events.NewPlan(payload.Text, payload.Status)
			ev.PlanContext = payload.PlanContext
			ev.PlanWhatNotToDo = payload.PlanWhatNotToDo
			ev.PlanVerification = payload.PlanVerification
			return []events.Event{ev}
		}
	case "plan_approval":
		var payload struct {
			PlanContext      string `json:"plan_context"`
			PlanWhatNotToDo  string `json:"plan_what_not_to_do"`
			PlanVerification string `json:"plan_verification"`
		}
		_ = json.Unmarshal(data, &payload)
		return []events.Event{{
			Kind:             events.KindApproval,
			ToolName:         "announce_plan",
			Options:          []string{"Approve & implement", "Request changes", "Decline"},
			ApprovalKind:     "plan",
			PlanContext:      payload.PlanContext,
			PlanWhatNotToDo:  payload.PlanWhatNotToDo,
			PlanVerification: payload.PlanVerification,
		}}
	case "done":
		return []events.Event{events.NewDone()}
	case "debug":
		// Prefer structured init → model footer even when not in --debug.
		var payload struct {
			Type     string `json:"type"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Type == "init" {
			var out []events.Event
			if payload.Provider != "" || payload.Model != "" {
				out = append(out, events.Event{
					Kind:     events.KindModelChanged,
					Provider: payload.Provider,
					Model:    payload.Model,
				})
			}
			if debug {
				out = append(out, events.NewSystem(fmt.Sprintf("[debug] init provider=%s model=%s", payload.Provider, payload.Model)))
			}
			return out
		}
		if !debug {
			return nil
		}
		return []events.Event{events.NewSystem("[debug] " + sev.Data)}
	case "app_preview", "app_done", "app_saved", "browser_handoff",
		"distill_preview", "distill_saved", "fleet_progress", "fleet_redirect",
		"fleet_plan_redirect", "drill_redirect", "drill_add_redirect",
		"tutorial_scene_slideshow", "flow_output", "image", "new_session":
		// Soft-degrade Studio-only surfaces.
		return []events.Event{events.NewSystem(fmt.Sprintf("(%s — open Studio for full UI)", sev.Type))}
	default:
		if sev.Type != "" && sev.Type != "message" {
			return []events.Event{events.NewSystem(fmt.Sprintf("(unhandled event: %s)", sev.Type))}
		}
	}
	return nil
}

func containsToolBoxFrame(s string) bool {
	return len(s) > 0 && (containsRune(s, '╭') || containsRune(s, '╰') || containsRune(s, '│'))
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func chatAttachmentsFromBackend(atts []backend.Attachment) []client.ChatAttachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]client.ChatAttachment, 0, len(atts))
	for _, a := range atts {
		if len(a.Data) == 0 {
			continue
		}
		filename := a.Filename
		if filename == "" {
			ext := "bin"
			switch a.MimeType {
			case "image/png":
				ext = "png"
			case "image/jpeg":
				ext = "jpg"
			case "image/gif":
				ext = "gif"
			case "image/webp":
				ext = "webp"
			}
			filename = path.Base("attachment." + ext)
		}
		out = append(out, client.ChatAttachment{
			Filename: filename,
			MimeType: a.MimeType,
			Data:     base64.StdEncoding.EncodeToString(a.Data),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
