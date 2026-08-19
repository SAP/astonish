package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// rollbackState holds the /rollback picker overlay. It mirrors sessionsState:
// a scrollable list of revert targets with a confirmation step, since a
// rollback discards later messages and restores files (a destructive action).
type rollbackState struct {
	open          bool
	loading       bool
	confirmRevert bool
	err           string
	notice        string
	points        []backend.RollbackPoint
	cursor        int
	// prefill is the full text of the message being rolled back to. On a
	// successful rollback it is placed into the input composer so the user can
	// edit and resend it without retyping.
	prefill string
}

type rollbackLoadedMsg struct {
	points []backend.RollbackPoint
	err    error
}

type rolledBackMsg struct {
	entries []backend.HistoryEntry
	err     error
}

// rollbackCap returns the RollbackBackend capability of the active backend, or
// nil when the backend cannot roll back (e.g. platform chat). The /rollback
// command is only offered when this is non-nil.
func (m model) rollbackCap() backend.RollbackBackend {
	if rb, ok := m.backend.(backend.RollbackBackend); ok {
		return rb
	}
	return nil
}

func (m model) openRollbackPicker() (tea.Model, tea.Cmd) {
	if m.rollbackCap() == nil {
		m.tr.Apply(events.NewSystem("Rollback is only available in local code mode."))
		m.refreshViewport()
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		return m, nil
	}
	m.rollback = rollbackState{open: true, loading: true, cursor: 0}
	m.slash = slashCompletion{}
	m.files = fileCompletion{}
	return m, m.loadRollbackPointsCmd()
}

func (m model) loadRollbackPointsCmd() tea.Cmd {
	return func() tea.Msg {
		rb := m.rollbackCap()
		if rb == nil {
			return rollbackLoadedMsg{err: fmt.Errorf("rollback not supported")}
		}
		points, err := rb.ListRollbackPoints(m.ctx)
		return rollbackLoadedMsg{points: points, err: err}
	}
}

func (m model) rollbackToCmd(id string) tea.Cmd {
	return func() tea.Msg {
		rb := m.rollbackCap()
		if rb == nil {
			return rolledBackMsg{err: fmt.Errorf("rollback not supported")}
		}
		entries, err := rb.RollbackTo(m.ctx, id)
		return rolledBackMsg{entries: entries, err: err}
	}
}

func (m model) handleRollbackKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.rollback.loading {
		if key == "esc" || key == "ctrl+c" {
			m.rollback = rollbackState{}
			return m, nil
		}
		return m, nil
	}
	if m.rollback.confirmRevert {
		switch key {
		case "esc", "q", "n", "ctrl+c":
			m.rollback.confirmRevert = false
			m.rollback.notice = ""
			return m, nil
		case "y", "enter":
			if len(m.rollback.points) == 0 {
				m.rollback.confirmRevert = false
				return m, nil
			}
			id := m.rollback.points[m.rollback.cursor].ID
			m.rollback.prefill = m.rollback.points[m.rollback.cursor].MessageText
			m.rollback.loading = true
			m.rollback.confirmRevert = false
			m.rollback.err = ""
			m.rollback.notice = "Reverting…"
			return m, m.rollbackToCmd(id)
		}
		return m, nil
	}
	switch key {
	case "esc", "q", "ctrl+c":
		m.rollback = rollbackState{}
		return m, nil
	case "up", "k":
		if m.rollback.cursor > 0 {
			m.rollback.cursor--
		}
		return m, nil
	case "down", "j":
		if m.rollback.cursor < len(m.rollback.points)-1 {
			m.rollback.cursor++
		}
		return m, nil
	case "enter", " ":
		if len(m.rollback.points) == 0 {
			m.rollback = rollbackState{}
			return m, nil
		}
		m.rollback.confirmRevert = true
		m.rollback.err = ""
		m.rollback.notice = ""
		return m, nil
	}
	return m, nil
}

func (m model) applyRollbackLoaded(msg rollbackLoadedMsg) (tea.Model, tea.Cmd) {
	m.rollback.loading = false
	if msg.err != nil {
		m.rollback.err = msg.err.Error()
		return m, nil
	}
	m.rollback.points = msg.points
	// Default the cursor to the most recent point (bottom of the list).
	if len(msg.points) > 0 {
		m.rollback.cursor = len(msg.points) - 1
	} else {
		m.rollback.cursor = 0
	}
	return m, nil
}

func (m model) applyRolledBack(msg rolledBackMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.rollback = rollbackState{}
		m.tr.Apply(events.NewError("Rollback failed: " + msg.err.Error()))
		m.refreshViewport()
		return m, nil
	}
	entries := make([]events.HistoryMsg, 0, len(msg.entries))
	for _, e := range msg.entries {
		entries = append(entries, events.HistoryMsg{
			Kind:             e.Kind,
			Text:             e.Text,
			ToolName:         e.ToolName,
			ToolID:           e.ToolID,
			Args:             e.Args,
			Result:           e.Result,
			Artifact:         e.Artifact,
			PlanStatus:       e.PlanStatus,
			Options:          e.Options,
			PlanContext:      e.PlanContext,
			PlanWhatNotToDo:  e.PlanWhatNotToDo,
			PlanVerification: e.PlanVerification,
		})
	}
	m.planMode = false
	m.tr.Reset()
	m.tr.LoadHistory(entries)
	m.info = m.backend.Info()
	m.tr.LastUsage = nil
	m.tr.Apply(events.NewSystem("Rolled back. Chat and file changes reverted to before the selected message; its text is in the input for you to edit and resend."))
	prefill := m.rollback.prefill
	m.rollback = rollbackState{}
	// Prefill the composer with the rolled-back message so the user can edit
	// and resend it without retyping. CursorEnd places the caret at the end so
	// they can start typing/editing immediately; recompute the composer height
	// so a multi-line message is fully visible.
	if prefill != "" {
		m.ta.SetValue(prefill)
		m.ta.CursorEnd()
		if m.ready {
			m.ta.SetHeight(m.composerTextHeight())
			m.layout()
		}
		m.syncSlashCompletion()
		m.syncFileCompletion()
	}
	m.refreshViewport()
	if m.ready {
		m.vp.GotoBottom()
	}
	return m, nil
}

func (m model) renderRollbackOverlay() string {
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
	help := "  ↑↓ move  enter select  esc close"
	if m.rollback.confirmRevert {
		help = "  y revert  n/esc cancel"
	}
	body.WriteString(th.Header.Render("Rollback") + th.Muted.Render(help) + "\n\n")

	switch {
	case m.rollback.loading:
		body.WriteString(th.Muted.Render(first(m.rollback.notice, "Loading…")))
	case m.rollback.err != "":
		body.WriteString(th.Error.Render(m.rollback.err))
	case m.rollback.confirmRevert && len(m.rollback.points) > 0:
		p := m.rollback.points[m.rollback.cursor]
		body.WriteString(th.Danger.Render("Revert to this message? ") + th.Text.Render(first(p.Label, "(message)")) + "\n")
		detail := "This discards all later messages"
		if p.FileCount > 0 {
			detail += fmt.Sprintf(" and restores %d file(s)", p.FileCount)
		}
		detail += ". Press y to revert or Esc to cancel."
		body.WriteString(th.Muted.Render(detail))
	case len(m.rollback.points) == 0:
		body.WriteString(th.Muted.Render("Nothing to roll back to yet. Send a message first."))
	default:
		maxRows := h - 4
		if maxRows < 3 {
			maxRows = 3
		}
		start := 0
		if m.rollback.cursor >= maxRows {
			start = m.rollback.cursor - maxRows + 1
		}
		end := start + maxRows
		if end > len(m.rollback.points) {
			end = len(m.rollback.points)
		}
		for i := start; i < end; i++ {
			p := m.rollback.points[i]
			label := p.Label
			if label == "" {
				label = "(message)"
			}
			meta := fmt.Sprintf("#%d", p.TurnNumber)
			if p.Timestamp != "" {
				meta += "  " + p.Timestamp
			}
			if p.FileCount > 0 {
				meta += fmt.Sprintf("  · %d files", p.FileCount)
			}
			line := fmt.Sprintf("%s  %s", meta, label)
			if lipgloss.Width(line) > w-6 {
				runes := []rune(line)
				if len(runes) > w-9 {
					line = string(runes[:w-9]) + "…"
				}
			}
			if i == m.rollback.cursor {
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
