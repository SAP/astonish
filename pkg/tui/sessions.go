package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// sessionsState holds the sessions picker overlay.
type sessionsState struct {
	open    bool
	loading bool
	err     string
	items   []backend.SessionSummary
	cursor  int
}

type sessionsLoadedMsg struct {
	items []backend.SessionSummary
	err   error
}

type historyLoadedMsg struct {
	sessionID string
	entries   []backend.HistoryEntry
	err       error
	notice    string
}

func (m model) openSessionsPicker() (tea.Model, tea.Cmd) {
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.sessions = sessionsState{open: true, loading: true, cursor: 0}
	return m, m.loadSessionsCmd()
}

func (m model) loadSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		items, err := m.backend.ListSessions(m.ctx)
		return sessionsLoadedMsg{items: items, err: err}
	}
}

func (m model) resumeSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		entries, err := m.backend.ResumeSession(m.ctx, id)
		return historyLoadedMsg{sessionID: id, entries: entries, err: err, notice: "Session resumed."}
	}
}

func (m model) loadInitialHistoryCmd() tea.Cmd {
	return func() tea.Msg {
		entries, err := m.backend.LoadHistory(context.Background())
		info := m.backend.Info()
		return historyLoadedMsg{
			sessionID: info.SessionID,
			entries:   entries,
			err:       err,
			notice:    "Welcome back — session resumed.",
		}
	}
}

func (m model) handleSessionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessions.loading {
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			m.sessions = sessionsState{}
			return m, nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.sessions = sessionsState{}
		return m, nil
	case "up", "k":
		if m.sessions.cursor > 0 {
			m.sessions.cursor--
		}
		return m, nil
	case "down", "j":
		if m.sessions.cursor < len(m.sessions.items)-1 {
			m.sessions.cursor++
		}
		return m, nil
	case "enter", " ":
		if len(m.sessions.items) == 0 {
			m.sessions = sessionsState{}
			return m, nil
		}
		id := m.sessions.items[m.sessions.cursor].ID
		m.sessions = sessionsState{loading: true, open: true}
		return m, m.resumeSessionCmd(id)
	case "n":
		// New session from picker
		m.sessions = sessionsState{}
		return m.startNewSession()
	}
	return m, nil
}

func (m model) startNewSession() (tea.Model, tea.Cmd) {
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.planMode = false
	m.backend.NewSession()
	m.info = m.backend.Info()
	m.tr.Reset()
	m.tr.Apply(events.NewSystem("New session — send a message to begin."))
	m.refreshViewport()
	return m, nil
}

func (m model) applyHistory(msg historyLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.tr.Apply(events.NewError("Failed to load session: " + msg.err.Error()))
		m.sessions = sessionsState{}
		m.refreshViewport()
		return m, nil
	}
	entries := make([]events.HistoryMsg, 0, len(msg.entries))
	for _, e := range msg.entries {
		entries = append(entries, events.HistoryMsg{
			Kind:     e.Kind,
			Text:     e.Text,
			ToolName: e.ToolName,
			ToolID:   e.ToolID,
			Args:     e.Args,
			Result:   e.Result,
		})
	}
	m.planMode = false
	m.tr.LoadHistory(entries)
	m.info = m.backend.Info()
	if msg.sessionID != "" {
		m.info.SessionID = msg.sessionID
	}
	m.info.IsResumed = true
	if msg.notice != "" {
		m.tr.Apply(events.NewSystem(msg.notice))
	}
	if len(entries) == 0 {
		m.tr.Apply(events.NewSystem("Empty session — send a message to continue."))
	}
	m.sessions = sessionsState{}
	m.refreshViewport()
	if m.ready {
		m.vp.GotoBottom()
	}
	return m, nil
}

func (m model) renderSessionsOverlay() string {
	th := m.theme
	w := m.width
	if w < 40 {
		w = 40
	}
	h := m.height - 4
	if h < 8 {
		h = 8
	}

	var body strings.Builder
	body.WriteString(th.Header.Render("Sessions") + th.Muted.Render("  ↑↓ move  enter open  n new  esc close") + "\n\n")

	if m.sessions.loading {
		body.WriteString(th.Muted.Render("Loading…"))
	} else if m.sessions.err != "" {
		body.WriteString(th.Error.Render(m.sessions.err))
	} else if len(m.sessions.items) == 0 {
		body.WriteString(th.Muted.Render("No sessions yet. Press n for a new one."))
	} else {
		// Show a window around cursor
		maxRows := h - 4
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.sessions.cursor >= maxRows {
			start = m.sessions.cursor - maxRows + 1
		}
		end := start + maxRows
		if end > len(m.sessions.items) {
			end = len(m.sessions.items)
		}
		for i := start; i < end; i++ {
			s := m.sessions.items[i]
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			id := s.ID
			if len(id) > 10 {
				id = id[:10]
			}
			line := fmt.Sprintf("%s  %s  · %d msgs", id, title, s.MessageCount)
			if lipgloss.Width(line) > w-6 {
				// crude truncate
				runes := []rune(line)
				if len(runes) > w-9 {
					line = string(runes[:w-9]) + "…"
				}
			}
			if i == m.sessions.cursor {
				body.WriteString(th.Brand.Render("› " + line))
			} else {
				body.WriteString(th.Text.Render("  " + line))
			}
			body.WriteByte('\n')
		}
	}

	box := th.InputBorderFocus.
		Width(w-2).
		MaxHeight(h).
		Padding(1, 2).
		Render(body.String())
	return box
}
