package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// backendSlot holds the preserved state of one backend panel in the dual-mode
// TUI. When the user presses Ctrl+Tab, the current slot's state is saved and
// the other slot's state is restored — each mode keeps its own transcript,
// plan mode, and theme independently.
type backendSlot struct {
	backend backend.Backend
	theme   Theme

	// Preserved TUI state (saved on switch-away, restored on switch-to).
	tr            *events.Transcript
	planMode      bool
	graphPlanMode bool
	askMode       bool
	history       []string
	historyIdx    int
	mdCache       map[string]string

	// opened tracks whether Open() has been called on this backend. The alt
	// backend is lazily initialized on first Ctrl+Tab to avoid startup cost
	// when the user never switches.
	opened bool
}

// switchBackend handles Ctrl+Tab: saves the current slot's state, flips to the
// other slot, restores its state (lazy-opening the backend on first visit), and
// swaps the accent theme so the UI color changes instantly.
func (m model) switchBackend() (tea.Model, tea.Cmd) {
	// Cancel any in-flight turn before switching — we don't need the returned
	// model/cmd since switchBackend rebuilds state from the slot anyway.
	//nolint:staticcheck // returned model unused; we overwrite state below.
	_, _, _ = m.cancelInFlightTurn()

	// Save current state into the active slot.
	cur := &m.backends[m.activeBackendIdx]
	cur.tr = m.tr
	cur.planMode = m.planMode
	cur.graphPlanMode = m.graphPlanMode
	cur.askMode = m.askMode
	cur.history = m.history
	cur.historyIdx = m.historyIdx
	cur.mdCache = m.mdCache

	// Flip to the other slot.
	m.activeBackendIdx = 1 - m.activeBackendIdx
	next := &m.backends[m.activeBackendIdx]

	// Lazy-open the backend on first visit.
	if !next.opened {
		if err := next.backend.Open(m.ctx); err != nil {
			// Open failed — revert to original slot and show error.
			m.activeBackendIdx = 1 - m.activeBackendIdx
			m.tr.Apply(events.NewError("Failed to connect to platform: " + err.Error()))
			m.refreshViewport()
			return m, nil
		}
		next.opened = true
		// Create a fresh transcript for the newly-opened backend.
		info := next.backend.Info()
		tr := events.NewTranscript()
		tr.SessionID = info.SessionID
		tr.Provider = info.Provider
		tr.Model = info.Model
		tr.LinearThread = info.Mode == "code"
		if info.Usage != nil {
			tr.LastUsage = &events.Usage{Input: info.Usage.Input, Output: info.Usage.Output, Total: info.Usage.Total}
		}
		if info.ContextTokens > 0 {
			tr.ContextTokens = info.ContextTokens
		}
		next.tr = tr
	}

	// Restore state from the new slot.
	m.backend = next.backend
	m.info = next.backend.Info()
	m.planMode = next.planMode
	m.graphPlanMode = next.graphPlanMode
	m.askMode = next.askMode
	m.history = next.history
	m.historyIdx = next.historyIdx

	// Always reset to the welcome/home screen on switch — create a fresh
	// transcript so the mode banner is shown immediately.
	tr := events.NewTranscript()
	tr.SessionID = m.info.SessionID
	tr.Provider = m.info.Provider
	tr.Model = m.info.Model
	tr.LinearThread = m.info.Mode == "code"
	if m.info.Usage != nil {
		tr.ContextTokens = 0
		tr.LastUsage = &events.Usage{Input: m.info.Usage.Input, Output: m.info.Usage.Output, Total: m.info.Usage.Total}
	}
	m.tr = tr
	next.tr = tr

	// Swap theme — the core of the visual mode change.
	m.theme = next.theme
	m.theme.ApplyTextareaStyles(&m.ta)

	// Clear markdown cache (rendered at old theme/width).
	m.mdCache = nil

	// Update composer placeholder.
	if m.info.Mode == "platform" {
		m.ta.Placeholder = "Message Platform…"
	} else {
		m.ta.Placeholder = "Message Astonish…"
	}

	m.refreshViewport()
	return m, nil
}
