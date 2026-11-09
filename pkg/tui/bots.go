package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

var botsSlashCommand = slashCommand{Name: "bots", Description: "Browse remote Bots; /bots runs, home, approvals, stop-run, detach"}

type botsState struct {
	exceptions               bool
	continueRun              bool
	answerText               string
	proposals                bool
	proposalItems            []store.BotControlProposal
	stopRun                  bool
	overview                 botOverviewMode
	snapshot                 *store.BotSnapshot
	goals                    []store.BotGoal
	requestID                uuid.UUID
	runs                     bool
	runItems                 []store.BotRun
	answering                bool
	answerCursor             int
	response                 *store.BotInteractionResponse
	open, loading, approvals bool
	err                      string
	items                    []store.Bot
	decisions                []store.BotApproval
	cursor                   int
	confirm                  store.ApprovalStatus
}
type botsLoadedMsg struct {
	proposals  []store.BotControlProposal
	snapshot   *store.BotSnapshot
	requestID  uuid.UUID
	runs       []store.BotRun
	items      []store.Bot
	approvals  []store.BotApproval
	err        error
	generation uint64
}
type botSelectedMsg struct {
	err        error
	generation uint64
}
type botStartMsg struct{}
type botWatchMsg struct {
	event      events.Event
	err        error
	generation uint64
	done       bool
}
type botDecisionMsg struct {
	continuation store.BotContinuationState
	requestID    uuid.UUID
	err          error
	generation   uint64
}

func (m model) botCap() backend.BotBackend { b, _ := m.backend.(backend.BotBackend); return b }
func (m model) botSelected() bool          { b := m.botCap(); return b != nil && b.SelectedBot() != nil }
func (m *model) detachBotWatch() {
	if m.botCancel != nil {
		m.botCancel()
		m.botCancel = nil
	}
	m.botGeneration++
	m.botEvents = nil
	m.botSnapshots = nil
	m.botSnapshot = nil
	m.botSnapshotErr = nil
	m.botProposalSeen = nil
}
func (m model) botCommand(text string) (tea.Model, tea.Cmd) {
	cap := m.botCap()
	if cap == nil {
		m.tr.Apply(events.NewSystem("/bots requires remote platform chat; local code is unchanged."))
		m.refreshViewport()
		return m, nil
	}
	switch text {
	case "/bots exceptions":
		return m.openBotExceptions()
	case "/bots goals":
		return m.openBotOverview(botOverviewGoals)
	case "/bots schedule", "/bots status":
		return m.openBotOverview(botOverviewSchedule)
	case "/bots config", "/bots test-once":
		return m.botExternalAction(text)
	case "/bots proposals":
		return m.openBotProposals()
	case "/bots decisions":
		return m.openBotDecisions()
	case "/bots detach":
		m.detachBotWatch()
		cap.DetachBot()
		m.bots = botsState{}
		m.info = m.backend.Info()
		m.tr.Reset()
		m.tr.Apply(events.NewSystem("Detached. Bot work continues on the daemon."))
		m.refreshViewport()
		return m, nil
	case "/bots home":
		c, ok := m.backend.(backend.BotSessionBackend)
		if !ok || !m.botSelected() {
			m.tr.Apply(events.NewError("Select a remote Bot first."))
			m.refreshViewport()
			return m, nil
		}
		m.detachBotWatch()
		gen := m.botGeneration
		m.bots = botsState{open: true, loading: true}
		return m, func() tea.Msg { return botSelectedMsg{err: c.SelectBotSession(m.ctx, uuid.Nil), generation: gen} }
	case "/bots", "/bots approvals", "/bots runs", "/bots stop-run", "/bots continue":
		if !m.botSelected() && m.tr.Streaming {
			m.tr.Apply(events.NewSystem("Finish or cancel the chat turn before selecting a Bot."))
			m.refreshViewport()
			return m, nil
		}
		approvals := text == "/bots approvals"
		runs := text == "/bots runs" || text == "/bots stop-run" || text == "/bots continue"
		if (approvals || runs) && !m.botSelected() {
			m.tr.Apply(events.NewError("Select a Bot first with /bots."))
			m.refreshViewport()
			return m, nil
		}
		m.bots = botsState{continueRun: text == "/bots continue", stopRun: text == "/bots stop-run", open: true, loading: true, approvals: approvals, runs: runs, requestID: uuid.New()}
		m.slash = slashCompletion{}
		gen := m.botGeneration
		return m, func() tea.Msg {
			if runs {
				c, ok := m.backend.(backend.BotSessionBackend)
				if !ok {
					return botsLoadedMsg{requestID: m.bots.requestID, err: fmt.Errorf("run selection unavailable"), generation: gen}
				}
				r, err := c.BotRuns(m.ctx)
				return botsLoadedMsg{requestID: m.bots.requestID, runs: r, err: err, generation: gen}
			}
			if approvals {
				if overview, ok := m.backend.(backend.BotOverviewBackend); ok {
					snap, err := overview.BotSnapshot(m.ctx)
					result := botsLoadedMsg{requestID: m.bots.requestID, snapshot: snap, err: err, generation: gen}
					if err == nil && snap != nil {
						result.approvals = snap.PendingApprovals
					}
					return result
				}
				a, err := cap.BotApprovals(m.ctx)
				return botsLoadedMsg{requestID: m.bots.requestID, approvals: a, err: err, generation: gen}
			}
			v, err := cap.ListBots(m.ctx)
			return botsLoadedMsg{requestID: m.bots.requestID, items: v, err: err, generation: gen}
		}
	default:
		m.tr.Apply(events.NewSystem("Use /bots, /bots runs, /bots home, /bots approvals, or /bots detach."))
		m.refreshViewport()
		return m, nil
	}
}
func (m model) startBotWatch() (tea.Model, tea.Cmd) {
	if !m.botSelected() {
		return m, nil
	}
	m.detachBotWatch()
	m.info = m.backend.Info()
	m.tr.Reset()
	m.tr.Title = m.info.Title
	m.tr.SessionID = m.info.SessionID
	m.tr.BotID = m.botCap().SelectedBot().ID.String()
	guidance := "Send a message here. Draft and paused conversations do not activate work."
	if bot := m.botCap().SelectedBot(); m.info.SessionID != "" && m.info.SessionID != bot.HomeSessionID.String() {
		guidance = "This run trace is read-only; /bots home returns to the conversation."
	}
	m.tr.Apply(events.NewSystem("Watching " + m.info.Title + ". " + guidance + " /bots decisions reviews saved proposals and approvals; /bots detach leaves work running."))
	m.refreshViewport()
	ctx, cancel := context.WithCancel(m.ctx)
	m.botCancel = cancel
	gen := m.botGeneration
	ch := make(chan botWatchMsg)
	m.botEvents = ch
	cap := m.botCap()
	go func() {
		defer close(ch)
		err := cap.WatchBot(ctx, func(e events.Event) error {
			select {
			case ch <- botWatchMsg{event: e, generation: gen}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if ctx.Err() == nil {
			select {
			case ch <- botWatchMsg{err: err, generation: gen, done: true}:
			case <-ctx.Done():
			}
		}
	}()
	if overview, ok := m.backend.(backend.BotOverviewBackend); ok {
		m.botSnapshots = watchBotSnapshots(ctx, overview, gen)
		return m, tea.Batch(waitBotEvent(ch), waitBotSnapshot(m.botSnapshots))
	}
	return m, waitBotEvent(ch)
}
func waitBotEvent(ch <-chan botWatchMsg) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-ch
		if !ok {
			return nil
		}
		return v
	}
}
func (m model) applyBotWatch(v botWatchMsg) (tea.Model, tea.Cmd) {
	if v.generation != m.botGeneration || !m.botSelected() {
		return m, nil
	}
	if v.err != nil {
		m.tr.Apply(events.NewError("Bot watch disconnected: " + v.err.Error() + ". Re-select to reconnect."))
	} else if !v.done {
		m.tr.Apply(v.event)
	}
	m.refreshViewport()
	if v.done {
		return m, nil
	}
	return m, waitBotEvent(m.botEvents)
}
func (m model) handleBotsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" || key == "ctrl+c" {
		m.bots = botsState{}
		return m, nil
	}
	if m.bots.loading {
		return m, nil
	}
	if m.bots.overview != botOverviewNone {
		return m, nil
	}
	if m.bots.exceptions {
		return m.handleBotExceptionKey(key)
	}
	if m.bots.proposals {
		return m.handleBotProposalKey(key)
	}
	if m.bots.answering {
		return m.handleBotAnswerKey(key)
	}
	n := len(m.bots.items)
	if m.bots.runs {
		n = len(m.bots.runItems)
	}
	if m.bots.approvals {
		n = len(m.bots.decisions)
	}
	switch key {
	case "up", "k":
		if m.bots.cursor > 0 {
			m.bots.cursor--
		}
		m.bots.confirm = ""
	case "down", "j":
		if m.bots.cursor+1 < n {
			m.bots.cursor++
		}
		m.bots.confirm = ""
	case "y", "n":
		if m.bots.approvals && n > 0 {
			a := m.bots.decisions[m.bots.cursor]
			if a.Status != store.ApprovalPending || !a.ExpiresAt.After(time.Now()) {
				m.bots.err = "Approval is no longer pending or has expired. Reload to review."
				return m, nil
			}
			if a.Interaction != nil && key == "y" {
				if _, ok := m.backend.(backend.BotInteractionBackend); !ok || a.Interaction.Validate() != nil {
					m.bots.err = "Invalid or unsupported question."
					return m, nil
				}
				m.bots.answering = true
				m.bots.answerCursor = 0
				m.bots.confirm = ""
				m.bots.response = nil
				return m, nil
			}
			m.bots.confirm = store.ApprovalDenied
			if key == "y" {
				m.bots.confirm = store.ApprovalApproved
			}
		}
	case "enter":
		if n == 0 {
			return m, nil
		}
		cap := m.botCap()
		gen := m.botGeneration
		if m.bots.approvals {
			if m.bots.confirm == "" {
				return m, nil
			}
			a := m.bots.decisions[m.bots.cursor]
			if a.Status != store.ApprovalPending || !a.ExpiresAt.After(time.Now()) || m.bots.err != "" {
				m.bots.confirm = ""
				m.bots.err = "Approval changed or expired; close and reload to review."
				return m, nil
			}
			status := m.bots.confirm
			m.bots.loading = true
			return m, func() tea.Msg {
				if status == store.ApprovalApproved {
					return m.approveBotContinuation(a, nil)
				}
				return botDecisionMsg{requestID: m.bots.requestID, err: cap.DecideBotApproval(m.ctx, a, status), generation: gen}
			}
		}
		if m.bots.runs {
			if m.bots.continueRun {
				run := m.bots.runItems[m.bots.cursor]
				c, ok := m.backend.(backend.BotInteractiveContinuationBackend)
				if !ok || !run.IsInteractiveChat() || m.bots.err != "" {
					m.bots.err = "Select an interactive run eligible for explicit continuation."
					return m, nil
				}
				if m.bots.confirm == "" {
					m.bots.confirm = store.ApprovalApproved
					return m, nil
				}
				m.bots.loading = true
				return m, func() tea.Msg {
					_, err := c.ContinueBotInteractive(m.ctx, run, nil)
					return botDecisionMsg{generation: gen, requestID: m.bots.requestID, err: err}
				}
			}
			if m.bots.stopRun {
				if m.bots.err != "" {
					return m, nil
				}
				m.tr.Apply(events.NewSystem(m.botStopRunRoute(m.bots.runItems[m.bots.cursor])))
				m.bots.open = false
				m.refreshViewport()
				return m, nil
			}
			sid := m.bots.runItems[m.bots.cursor].SessionID
			if sid == uuid.Nil {
				m.bots.err = "Run has no transcript yet."
				return m, nil
			}
			c, ok := m.backend.(backend.BotSessionBackend)
			if !ok {
				return m, nil
			}
			m.detachBotWatch()
			gen = m.botGeneration
			m.bots.loading = true
			return m, func() tea.Msg { return botSelectedMsg{err: c.SelectBotSession(m.ctx, sid), generation: gen} }
		}
		id := m.bots.items[m.bots.cursor].ID.String()
		m.detachBotWatch()
		gen = m.botGeneration
		m.bots.loading = true
		return m, func() tea.Msg { _, err := cap.SelectBot(m.ctx, id); return botSelectedMsg{err: err, generation: gen} }
	}
	return m, nil
}
func (m model) renderBotsOverlay() string {
	if m.bots.exceptions {
		return m.renderBotExceptions()
	}
	if m.bots.proposals {
		return m.renderBotProposals()
	}
	if m.bots.overview != botOverviewNone {
		return m.renderBotOverview()
	}
	var s strings.Builder
	title := "Bots — separate from chat sessions"
	if m.bots.approvals {
		title = "Bot approvals — exact durable decisions"
	}
	if m.bots.runs {
		title = "Bot runs — read-only traces"
	}
	if m.bots.stopRun {
		title = "Stop run — review exact CLI action"
	}
	s.WriteString(title + "\n\n")
	if m.bots.snapshot != nil {
		fmt.Fprintf(&s, "Pending decisions: %d\n", botPendingCount(m.bots.snapshot))
	}
	switch {
	case m.bots.loading:
		s.WriteString("Loading…")
	case m.bots.err != "":
		s.WriteString("Error: " + m.bots.err + "\n")
	case m.bots.approvals && len(m.bots.decisions) == 0:
		s.WriteString("No approvals.")
	case m.bots.runs && len(m.bots.runItems) == 0:
		s.WriteString("No runs.")
	case !m.bots.runs && !m.bots.approvals && len(m.bots.items) == 0:
		s.WriteString("No Bots. Create a draft with astonish bots create draft.")
	}
	start := max(0, m.bots.cursor-3)
	if m.bots.runs {
		for i := start; i < len(m.bots.runItems) && i < start+8; i++ {
			r := m.bots.runItems[i]
			mark := "  "
			if i == m.bots.cursor {
				mark = "> "
			}
			fmt.Fprintf(&s, "%s%s [%s]\n  Session: %s\n", mark, r.ID, r.Status, r.SessionID)
		}
		if m.bots.continueRun {
			if m.bots.confirm == "" {
				s.WriteString("\nExplicit continuation: Enter reviews selected run · Esc cancels")
			} else {
				s.WriteString("\nContinue this exact interactive run? Enter confirms · Esc cancels. No activation or schedule changes.")
			}
		} else if m.bots.stopRun {
			s.WriteString("\nStop run: ↑↓ select · Enter shows exact CLI confirmation · Esc cancels\nNo cancellation until you execute the reviewed command.")
		} else {
			s.WriteString("\n↑↓ select · Enter watch · Esc close · /bots home returns home · /bots stop-run cancels a run")
		}
	} else if m.bots.approvals {
		for i := start; i < len(m.bots.decisions) && i < start+6; i++ {
			a := m.bots.decisions[i]
			mark := "  "
			if i == m.bots.cursor {
				mark = "> "
			}
			fmt.Fprintf(&s, "%s%s [%s]\n", mark, a.ID, a.Status)
		}
		if len(m.bots.decisions) > 0 {
			a := m.bots.decisions[m.bots.cursor]
			raw, _ := json.MarshalIndent(a.Action, "", "  ")
			if a.Interaction != nil {
				m.renderBotQuestion(&s, a)
			}
			fmt.Fprintf(&s, "\nRun: %s  Version: %d\nDigest: %s\nExpires: %s\n%s\n", a.RunID, a.Version, a.ActionDigest, a.ExpiresAt.Format(time.RFC3339), raw)
		}
		if m.bots.confirm != "" {
			fmt.Fprintf(&s, "\nConfirm %s for this exact action: Enter (Esc cancels)", m.bots.confirm)
		} else {
			s.WriteString("\n[Cancel] · y approve and continue · n deny · then Enter confirms · Esc closes")
		}
	} else {
		for i := start; i < len(m.bots.items) && i < start+8; i++ {
			b := m.bots.items[i]
			mark := "  "
			if i == m.bots.cursor {
				mark = "> "
			}
			fmt.Fprintf(&s, "%s%s [%s]  %s\n", mark, b.Name, b.Status, b.ID)
		}
		s.WriteString("\n↑↓ select · Enter watch · Esc close")
	}
	return lipgloss.NewStyle().Width(max(30, m.width-8)).Border(lipgloss.RoundedBorder()).Padding(1, 2).Render(s.String())
}
