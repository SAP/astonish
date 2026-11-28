package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/google/uuid"
)

func (m model) botProgressForReview(run store.BotRun) (store.BotRunState, bool) {
	if m.bots.progressReview != nil && m.bots.confirm != "" {
		return *m.bots.progressReview, true
	}
	if m.bots.snapshot != nil {
		for _, s := range m.overviewRunStates(m.bots.snapshot) {
			if s.RunID == run.ID && s.Progress != nil {
				return s, true
			}
		}
	}
	if run.Progress != nil {
		return store.BotRunState{BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Progress: run.Progress, Status: run.Status}, true
	}
	return store.BotRunState{}, false
}

func (m model) reviewBotProgress(state store.BotRunState) (tea.Model, tea.Cmd) {
	c, ok := m.backend.(backend.BotProgressContinuationBackend)
	if !ok || m.bots.err != "" || m.bots.snapshot == nil || m.bots.snapshot.Bot.ID != state.BotID || state.Progress == nil || state.ValidateProgress() != nil || state.Progress.Phase != store.BotProgressSuspended || state.Progress.Reason != store.BotProgressReasonPaused || state.Status != store.RunInterrupted {
		m.bots.err = "Only an owner-paused, safely saved checkpoint can be continued. Reload to review."
		return m, nil
	}
	if m.bots.confirm == "" {
		state.Progress = state.Progress.Clone()
		m.bots.progressReview, m.bots.progressRevision, m.bots.progressKey = &state, m.bots.snapshot.Bot.Revision, uuid.NewString()
		m.bots.confirm = store.ApprovalApproved
		return m, nil
	}
	m.bots.loading = true
	return m, func() tea.Msg {
		_, err := c.ContinueBotProgress(m.ctx, *m.bots.progressReview, m.bots.progressRevision, m.bots.progressKey)
		return botDecisionMsg{generation: m.botGeneration, requestID: m.bots.requestID, err: err}
	}
}
