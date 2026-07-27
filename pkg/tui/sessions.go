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
	open          bool
	loading       bool
	confirmDelete bool
	err           string
	notice        string
	items         []backend.SessionSummary
	cursor        int
}

type sessionsLoadedMsg struct {
	items []backend.SessionSummary
	err   error
}

type sessionDeletedMsg struct {
	id  string
	err error
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

func (m model) deleteSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		return sessionDeletedMsg{id: id, err: m.backend.DeleteSession(m.ctx, id)}
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
	key := msg.String()
	if m.sessions.loading {
		if key == "esc" || key == "ctrl+c" {
			m.sessions = sessionsState{}
			return m, nil
		}
		return m, nil
	}
	if m.sessions.confirmDelete {
		switch key {
		case "esc", "q", "n", "ctrl+c":
			m.sessions.confirmDelete = false
			m.sessions.notice = ""
			return m, nil
		case "y", "enter":
			if len(m.sessions.items) == 0 {
				m.sessions.confirmDelete = false
				return m, nil
			}
			id := m.sessions.items[m.sessions.cursor].ID
			m.sessions.loading = true
			m.sessions.confirmDelete = false
			m.sessions.err = ""
			m.sessions.notice = "Deleting session…"
			return m, m.deleteSessionCmd(id)
		}
		return m, nil
	}
	switch key {
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
	case "d", "backspace", "delete":
		if len(m.sessions.items) == 0 {
			return m, nil
		}
		m.sessions.confirmDelete = true
		m.sessions.err = ""
		m.sessions.notice = ""
		return m, nil
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
	m.refreshViewport()
	return m, nil
}

func (m model) applySessionDeleted(msg sessionDeletedMsg) (tea.Model, tea.Cmd) {
	m.sessions.loading = false
	if msg.err != nil {
		m.sessions.err = "Failed to delete session: " + msg.err.Error()
		m.sessions.notice = ""
		return m, nil
	}
	m.sessions.notice = "Session deleted."
	m.sessions.err = ""
	for i, s := range m.sessions.items {
		if s.ID == msg.id {
			m.sessions.items = append(m.sessions.items[:i], m.sessions.items[i+1:]...)
			break
		}
	}
	if m.sessions.cursor >= len(m.sessions.items) && m.sessions.cursor > 0 {
		m.sessions.cursor--
	}
	if msg.id == m.info.SessionID || msg.id == m.tr.SessionID {
		m.planMode = false
		m.backend.NewSession()
		m.info = m.backend.Info()
		m.tr.Reset()
		m.refreshViewport()
	}
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
	if m.info.Usage != nil {
		m.tr.LastUsage = &events.Usage{Input: m.info.Usage.Input, Output: m.info.Usage.Output, Total: m.info.Usage.Total}
	}
	if msg.sessionID != "" {
		m.info.SessionID = msg.sessionID
	}
	m.info.IsResumed = true
	if msg.notice != "" && len(entries) > 0 {
		m.tr.Apply(events.NewSystem(msg.notice))
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
	help := "  ↑↓ move  enter open  d delete  n new  esc close"
	if m.sessions.confirmDelete {
		help = "  y delete  n/esc cancel"
	}
	body.WriteString(th.Header.Render("Sessions") + th.Muted.Render(help) + "\n\n")

	if m.sessions.loading {
		body.WriteString(th.Muted.Render(first(m.sessions.notice, "Loading…")))
	} else if m.sessions.err != "" {
		body.WriteString(th.Error.Render(m.sessions.err))
	} else if m.sessions.confirmDelete && len(m.sessions.items) > 0 {
		s := m.sessions.items[m.sessions.cursor]
		body.WriteString(th.Danger.Render("Delete this session? ") + th.Text.Render(first(s.Title, "(untitled)")) + "\n")
		body.WriteString(th.Muted.Render("This removes the saved transcript. Press y to delete or Esc to cancel."))
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
	return m.paintCompletionPopup(box, w-2)
}
