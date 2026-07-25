package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	return tui.Run(ctx, tui.Config{Backend: b})
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
	resumed     bool

	mu     sync.Mutex
	closed bool
}

func (b *platformBackend) Info() backend.Info {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backend.Info{
		SessionID:   b.sessionID,
		Mode:        "platform",
		ServerURL:   b.serverURL,
		Org:         b.org,
		Team:        b.team,
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
	return nil
}

func (b *platformBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
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
	b.mu.Unlock()
	return hist, nil
}

func (b *platformBackend) NewSession() {
	b.mu.Lock()
	b.sessionID = ""
	b.resumed = false
	b.mu.Unlock()
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
	b.mu.Unlock()
	return studioMessagesToHistory(detail.Messages), nil
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
				Args:     args,
			})
		case "tool_result":
			out = append(out, backend.HistoryEntry{
				Kind:     "tool_result",
				ToolName: m.ToolName,
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

func (b *platformBackend) RunTurn(ctx context.Context, message string) (<-chan events.Event, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("backend closed")
	}
	sessionID := b.sessionID
	autoApprove := b.autoApprove
	debug := b.debug
	c := b.client
	b.mu.Unlock()

	req := &client.ChatRequest{
		SessionID:   sessionID,
		Message:     message,
		AutoApprove: autoApprove,
		Debug:       debug,
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
				// Keep session id in sync for subsequent turns.
				if ev.Kind == events.KindSession && ev.SessionID != "" {
					b.mu.Lock()
					b.sessionID = ev.SessionID
					b.mu.Unlock()
					sessionID = ev.SessionID
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
		}
		if json.Unmarshal(data, &payload) == nil {
			tool := payload.Tool
			if tool == "" {
				tool = payload.Name
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
	case "artifact":
		var payload struct {
			Path     string `json:"path"`
			ToolName string `json:"tool_name"`
			FileName string `json:"fileName"`
		}
		if json.Unmarshal(data, &payload) == nil {
			path := payload.Path
			if path == "" {
				path = payload.FileName
			}
			return []events.Event{{
				Kind: events.KindArtifact,
				Text: path,
				Meta: map[string]any{"path": path, "tool": payload.ToolName},
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
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
			Total  int64 `json:"total"`
		}
		if json.Unmarshal(data, &payload) == nil {
			return []events.Event{{
				Kind: events.KindUsage,
				Usage: &events.Usage{
					Input:  payload.Input,
					Output: payload.Output,
					Total:  payload.Total,
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
