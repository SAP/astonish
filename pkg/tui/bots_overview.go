package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

type botOverviewMode uint8

const (
	botOverviewNone botOverviewMode = iota
	botOverviewGoals
	botOverviewSchedule
)

type botOverviewMsg struct {
	generation uint64
	requestID  uuid.UUID
	snapshot   *store.BotSnapshot
	goals      []store.BotGoal
	err        error
}

func (m model) openBotOverview(mode botOverviewMode) (tea.Model, tea.Cmd) {
	cap, ok := m.backend.(backend.BotOverviewBackend)
	if !ok || !m.botSelected() {
		m.tr.Apply(events.NewError("Select a remote Bot with overview support first."))
		m.refreshViewport()
		return m, nil
	}
	var retained *store.BotSnapshot
	var goals []store.BotGoal
	if m.bots.open && m.bots.overview == mode {
		retained, goals = m.bots.snapshot, m.bots.goals
	}
	m.bots = botsState{open: true, loading: true, overview: mode, requestID: uuid.New(), snapshot: retained, goals: goals}
	gen, request := m.botGeneration, m.bots.requestID
	return m, func() tea.Msg {
		v := botOverviewMsg{generation: gen, requestID: request}
		v.snapshot, v.err = cap.BotSnapshot(m.ctx)
		if v.err == nil && mode == botOverviewGoals {
			v.goals, v.err = cap.BotGoals(m.ctx)
		}
		return v
	}
}
func (m model) applyBotOverview(v botOverviewMsg) (tea.Model, tea.Cmd) {
	if v.generation != m.botGeneration || !m.bots.open || v.requestID != m.bots.requestID {
		return m, nil
	}
	m.bots.loading = false
	if v.err != nil {
		m.bots.err = v.err.Error()
	} else {
		m.bots.snapshot = v.snapshot
		m.bots.goals = v.goals
		m.info = m.backend.Info()
	}
	return m, nil
}

// overviewRunStates uses the same reducer for fetched and live work. A slower
// inspector fetch must not replace a newer watch revision or another Bot's work.
func (m model) overviewRunStates(snap *store.BotSnapshot) []store.BotRunState {
	tr := events.NewTranscript()
	tr.BotID, tr.SessionID = snap.Bot.ID.String(), snap.Bot.HomeSessionID.String()
	tr.BotRunStates = make(map[string]store.BotRunState)
	if m.tr != nil {
		tr.BotID, tr.SessionID = m.tr.BotID, m.tr.SessionID
		for id, state := range m.tr.BotRunStates {
			tr.BotRunStates[id] = state
		}
	}
	for _, state := range snap.RunStates {
		tr.Apply(events.Event{Kind: events.KindBotRunState, BotRunState: &state})
	}
	states := make([]store.BotRunState, 0, len(tr.BotRunStates))
	for _, state := range tr.BotRunStates {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].RunID.String() < states[j].RunID.String() })
	return states
}

func (m model) renderBotOverview() string {
	var s strings.Builder
	title := "Bot Schedule"
	if m.bots.overview == botOverviewGoals {
		title = "Bot Goals"
	}
	s.WriteString(title + "\n\n")
	if m.bots.loading {
		s.WriteString("Loading work details…\n")
	}
	if m.bots.err != "" {
		s.WriteString("Work details unavailable. Press r to retry.\n")
	}
	if m.bots.snapshot == nil {
		if !m.bots.loading && m.bots.err == "" {
			s.WriteString("No status available.")
		}
	} else {
		if m.bots.loading || m.bots.err != "" {
			s.WriteString("Last known work details (not refreshed)\n")
		}
		snap := m.bots.snapshot
		fmt.Fprintf(&s, "%s [%s] · version %d · revision %d\nPending decisions: %d · Unread: %d\n", snap.Bot.Name, snap.Bot.Status, snap.Bot.Version, snap.Bot.Revision, botPendingCount(snap), snap.UnreadCount)
		// Snapshot currently has no scheduler projection. A last-run report is not
		// an authoritative next-run time, and must not be presented as one.
		s.WriteString("Next run: unavailable from server snapshot\n")
		if m.bots.overview == botOverviewGoals {
			if len(m.bots.goals) == 0 {
				s.WriteString("\nNo goals.")
			} else {
				for _, g := range m.bots.goals {
					fmt.Fprintf(&s, "\n%s [%s]\n", g.Title, g.Status)
				}
			}
		} else {
			s.WriteString("\nSchedules and lifecycle changes follow the Bot's delegated authority; exceptional decisions appear in Chat.")
		}
		states := m.overviewRunStates(snap)
		if snap.LatestOutcome != nil {
			run := *snap.LatestOutcome
			for _, state := range states {
				if state.RunID == run.ID && state.Work != nil && (run.Work == nil || state.Work.WorkRevision >= run.Work.WorkRevision) {
					run.Work = state.Work
				}
			}
			fmt.Fprintf(&s, "\nLatest run: %s [%s]\n%s", run.ID, run.Status, bot.RenderWorkDetails(run.Work))
			if run.Work == nil && run.Report.Quality != nil {
				fmt.Fprintf(&s, "\nLegacy quality: %s", bot.QualitySummary(run.Report.Quality))
				if run.Report.Quality.SchemaVersion == store.BotWorkSchemaVersion {
					for _, limitation := range run.Report.Quality.Limitations {
						fmt.Fprintf(&s, "\nUnresolved: %s", limitation)
					}
				}
			}
			if receipt := run.Report.Skill; receipt != nil && receipt.SchemaVersion == 1 {
				fmt.Fprintf(&s, "\nLearning: %s · used revision %d · committed revision %d", receipt.Kind, receipt.UsedRevision, receipt.CommittedRevision)
			}
		} else {
			s.WriteString("\nNo work result yet")
		}
		for _, state := range states {
			if state.Work != nil && (snap.LatestOutcome == nil || state.RunID != snap.LatestOutcome.ID) {
				fmt.Fprintf(&s, "\nRun %s [%s]\n%s", state.RunID, state.Status, bot.RenderWorkDetails(state.Work))
			}
		}
	}
	s.WriteString("\n\nEsc close · r refresh · /bots config opens configuration link")
	return lipgloss.NewStyle().Width(max(30, m.width-8)).Border(lipgloss.RoundedBorder()).Padding(1, 2).Render(s.String())
}
func (m model) botExternalAction(command string) (tea.Model, tea.Cmd) {
	bot := m.botCap().SelectedBot()
	if bot == nil {
		m.tr.Apply(events.NewError("Select a Bot first with /bots."))
	} else if command == "/bots test-once" {
		m.tr.Apply(events.NewSystem(fmt.Sprintf("Explicit one-time draft test (does not activate): review the current version in Studio, then run in another terminal:\nastonish bots test-once %s --scope %s --version %d --confirm %s:%s --key %s --instructions 'YOUR REVIEWED INSTRUCTIONS'\nOnly Draft Bots accept tests. Re-review on conflict; never auto-confirm a newer version.", bot.ID, bot.Scope, bot.Version, bot.Scope, bot.ID, uuid.NewString())))
	} else if cap, ok := m.backend.(backend.BotOverviewBackend); ok {
		m.tr.Apply(events.NewSystem("Review Bot configuration, schedules and lifecycle in Studio: " + cap.BotStudioURL()))
	}
	m.refreshViewport()
	return m, nil
}
