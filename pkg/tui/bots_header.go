package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"time"
)

// Snapshot messages are UI metadata, never core transcript events.
type botSnapshotMsg struct {
	generation uint64
	snapshot   *store.BotSnapshot
	err        error
}

// One request at a time, bounded by timeout and the selected watch lifetime.
func watchBotSnapshots(ctx context.Context, cap backend.BotOverviewBackend, generation uint64) <-chan botSnapshotMsg {
	ch := make(chan botSnapshotMsg)
	go func() {
		defer close(ch)
		for ctx.Err() == nil {
			request, cancel := context.WithTimeout(ctx, 5*time.Second)
			snapshot, err := cap.BotSnapshot(request)
			cancel()
			if ctx.Err() != nil {
				return
			}
			select {
			case ch <- botSnapshotMsg{generation: generation, snapshot: snapshot, err: err}:
			case <-ctx.Done():
				return
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
	}()
	return ch
}
func waitBotSnapshot(ch <-chan botSnapshotMsg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return v
	}
}
func (m model) applyBotSnapshot(v botSnapshotMsg) (tea.Model, tea.Cmd) {
	if v.generation != m.botGeneration || !m.botSelected() {
		return m, nil
	}
	if v.snapshot != nil && v.snapshot.Bot.ID != m.botCap().SelectedBot().ID {
		return m, waitBotSnapshot(m.botSnapshots)
	}
	m.botSnapshot, m.botSnapshotErr = v.snapshot, v.err
	if v.err == nil && v.snapshot != nil {
		m.info = m.backend.Info()
		m.tr.Title = m.botHeaderTitle()
		for _, operation := range v.snapshot.Operations {
			m.tr.Apply(events.Event{Kind: events.KindBotOperation, BotOperation: &operation})
		}
		for _, state := range v.snapshot.RunStates {
			m.tr.Apply(events.Event{Kind: events.KindBotRunState, BotRunState: &state})
		}
		if m.botProposalSeen == nil {
			m.botProposalSeen = make(map[uuid.UUID]bool)
		}
		if m.info.SessionID == v.snapshot.Bot.HomeSessionID.String() {
			for _, p := range v.snapshot.PendingProposals {
				if !m.botProposalSeen[p.ID] {
					m.botProposalSeen[p.ID] = true
					m.tr.Apply(events.NewSystem(fmt.Sprintf("Proposal %s: %s. Review saved change with /bots proposals.", p.ID, p.Payload.Operation)))
				}
			}
			m.refreshViewport()
		}
	}
	return m, waitBotSnapshot(m.botSnapshots)
}
func (m model) botHeaderTitle() string {
	bot := m.botCap().SelectedBot()
	if bot == nil {
		return ""
	}
	if m.botSnapshot != nil {
		bot = &m.botSnapshot.Bot
	}
	status := string(bot.Status)
	if m.botSnapshotErr != nil {
		status = "status unavailable"
	}
	title := fmt.Sprintf("Bot: %s [%s]", bot.Name, status)
	if m.info.SessionID != "" && m.info.SessionID != bot.HomeSessionID.String() {
		title += " — run trace"
	}
	return title
}
func (m model) botHeaderStatus() string {
	// A review is bound to one saved snapshot. Do not let background polling
	// change its companion count while the exact action is being reviewed.
	if m.bots.open && m.bots.snapshot != nil && m.bots.err == "" {
		return fmt.Sprintf("Decisions %d", botPendingCount(m.bots.snapshot))
	}
	if m.botSnapshot == nil || m.botSnapshotErr != nil {
		return "Decisions unavailable"
	}
	return fmt.Sprintf("Decisions %d", botPendingCount(m.botSnapshot))
}

// Admission carries only acknowledgement, not user/model execution echoes.
type botAdmissionMsg struct {
	generation uint64
	event      events.Event
	ch         <-chan events.Event
}

func waitBotAdmission(ch <-chan events.Event, generation uint64) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return botAdmissionMsg{generation: generation, event: e, ch: ch}
	}
}
func (m model) applyBotAdmission(v botAdmissionMsg) (tea.Model, tea.Cmd) {
	if v.generation != m.botGeneration || !m.botSelected() {
		return m, nil
	}
	if v.event.Kind == events.KindSystem || v.event.Kind == events.KindError {
		m.tr.Apply(v.event)
		m.refreshViewport()
	}
	return m, waitBotAdmission(v.ch, v.generation)
}
