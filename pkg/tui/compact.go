package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// compactionCap returns the backend's compaction capability, or nil when the
// active backend does not support on-demand compaction (e.g. platform chat).
func (m model) compactionCap() backend.CompactionBackend {
	if cb, ok := m.backend.(backend.CompactionBackend); ok {
		return cb
	}
	return nil
}

// compactDoneMsg carries the result of an async /compact request.
type compactDoneMsg struct {
	status string
	err    error
}

// runCompact runs on-demand context compaction immediately (not on the next
// message). Shows "Compacting…" in the status line right away so the user sees
// work in progress while the (sometimes multi-second) summarizer runs.
func (m model) runCompact() (tea.Model, tea.Cmd) {
	cap := m.compactionCap()
	if cap == nil {
		m.tr.Apply(events.NewSystem("Compaction is not available in this mode."))
		m.refreshViewport()
		return m, nil
	}
	// Guard against double /compact while the first is still summarizing.
	if m.compacting {
		m.tr.Apply(events.NewSystem("Compaction already in progress…"))
		m.refreshViewport()
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		m.tr.Apply(events.NewSystem("Wait for the current turn to finish before compacting."))
		m.refreshViewport()
		return m, nil
	}

	m.compacting = true
	m.tr.Status = "Compacting…"
	m.refreshViewport()

	ctx := m.ctx
	return m, tea.Batch(
		m.spin.Tick,
		func() tea.Msg {
			status, err := cap.Compact(ctx)
			return compactDoneMsg{status: status, err: err}
		},
	)
}

// applyCompactDone renders the outcome of a /compact request, clears the
// in-progress status, and syncs the header context occupancy from the backend.
func (m model) applyCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	m.compacting = false
	m.tr.Status = ""
	if msg.err != nil {
		m.tr.Apply(events.NewError("Compaction failed: " + msg.err.Error()))
	} else if msg.status != "" {
		m.tr.Apply(events.NewSystem(msg.status))
	}
	// Refresh header metadata (session id + context tokens after compaction).
	m.info = m.backend.Info()
	if m.info.ContextTokens > 0 {
		m.tr.ContextTokens = m.info.ContextTokens
	}
	m.refreshViewport()
	return m, nil
}
