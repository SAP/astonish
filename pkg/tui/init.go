package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// initCap returns the backend's AGENTS.md-generation capability, or nil when
// the active backend does not support it (e.g. platform chat, or code mode
// without a working directory). The /init and /init-deep commands are only
// offered when this is non-nil.
func (m model) initCap() backend.InitBackend {
	if ib, ok := m.backend.(backend.InitBackend); ok && ib.SupportsInit() {
		return ib
	}
	return nil
}

// runInit dispatches an unrestricted agent turn that generates AGENTS.md
// context file(s). It injects the given system-context prompt (initSystemContext
// for /init, initDeepSystemContext for /init-deep) so the agent knows to explore
// the project via codegraph and write the files. Unlike /plan this is NOT a
// runtime-gated mode — the turn must be free to call write_file — so no
// PlanMode/GraphPlanMode/AskMode flag is set.
func (m model) runInit(systemContext, marker string) (tea.Model, tea.Cmd) {
	if m.initCap() == nil {
		m.tr.Apply(events.NewSystem("AGENTS.md generation is only available in code mode."))
		m.refreshViewport()
		return m, nil
	}
	if m.tr.Streaming && !m.tr.Awaiting {
		m.tr.Apply(events.NewSystem("Wait for the current turn to finish before generating AGENTS.md."))
		m.refreshViewport()
		return m, nil
	}

	// Surface a visible user marker so the transcript shows what was requested,
	// mirroring submit()'s normal-message path.
	m.tr.Apply(events.NewUser(marker))
	m.userScrolledUp = false
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel

	opts := m.turnOptions()
	opts.SystemContext = systemContext
	// Ensure this is an unrestricted turn regardless of the current toggle.
	opts.PlanMode = false
	opts.GraphPlanMode = false
	opts.AskMode = false

	ch, err := m.backend.RunTurn(turnCtx, marker, opts)
	if err != nil {
		cancel()
		m.turnCancel = nil
		m.tr.Apply(events.NewError(err.Error()))
		m.refreshViewport()
		return m, nil
	}
	m.eventCh = ch
	m.timerStart()
	return m, tea.Batch(waitEvent(ch), timerTick())
}
