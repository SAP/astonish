package tui

import (
	"fmt"
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
	m.bots = botsState{open: true, loading: true, overview: mode, requestID: uuid.New()}
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
func (m model) renderBotOverview() string {
	var s strings.Builder
	title := "Bot Schedule"
	if m.bots.overview == botOverviewGoals {
		title = "Bot Goals"
	}
	s.WriteString(title + "\n\n")
	switch {
	case m.bots.loading:
		s.WriteString("Loading…")
	case m.bots.err != "":
		s.WriteString("Error: " + m.bots.err)
	case m.bots.snapshot == nil:
		s.WriteString("No status available.")
	default:
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
		if snap.LatestOutcome != nil {
			fmt.Fprintf(&s, "\nLatest run: %s [%s]\nQuality: %s", snap.LatestOutcome.ID, snap.LatestOutcome.Status, bot.QualitySummary(snap.LatestOutcome.Report.Quality))
			if q := snap.LatestOutcome.Report.Quality; q != nil && q.SchemaVersion == store.BotWorkSchemaVersion {
				for _, limitation := range q.Limitations {
					fmt.Fprintf(&s, "\nUnresolved: %s", limitation)
				}
			}
		} else {
			s.WriteString("\nNo assessed result yet.")
		}
	}
	s.WriteString("\n\nEsc close · /bots config opens configuration link")
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
