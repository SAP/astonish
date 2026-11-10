package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestBotWorkQualityOverview(t *testing.T) {
	m := model{theme: DefaultTheme(), width: 110, height: 40, ready: true}
	m.bots.overview = botOverviewGoals
	m.bots.snapshot = &store.BotSnapshot{Bot: store.Bot{Name: "Analyst"}}
	if !strings.Contains(stripANSI(m.renderBotOverview()), "No assessed result yet.") {
		t.Fatal("missing empty state")
	}
	m.bots.snapshot.LatestOutcome = &store.BotRun{Status: store.RunSucceeded}
	if !strings.Contains(stripANSI(m.renderBotOverview()), "Not assessed") {
		t.Fatal("legacy inferred pass")
	}
	m.bots.snapshot.LatestOutcome.Report.Quality = &store.BotQualityReport{SchemaVersion: 1, Outcome: store.BotWorkBlocked, Limitations: []string{"Current source unavailable"}}
	out := stripANSI(m.renderBotOverview())
	for _, text := range []string{"Requirements remain unmet", "Current source unavailable", "Esc close"} {
		if !strings.Contains(out, text) {
			t.Fatal("missing", text)
		}
	}
	m.bots.err = "offline"
	if strings.Contains(stripANSI(m.renderBotOverview()), "Current source unavailable") {
		t.Fatal("stale assessment shown as current after error")
	}
}

func TestBotScheduledReportsRenderDistinctAfterActivity(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%t", replay), func(t *testing.T) {
			tr := events.NewTranscript()
			journal := []events.Event{
				events.NewUser("Schedule recurring weather reports"),
				events.NewToolCall("schedule_job", "schedule-1", nil),
				events.NewToolResult("schedule_job", "schedule-1", "created"),
				events.NewText("Weather schedule created."),
				events.NewDone(),
			}
			for _, ev := range journal {
				tr.Apply(ev)
			}
			m := model{theme: DefaultTheme(), tr: tr, width: 100, height: 40, ready: true}
			for occurrence := 1; occurrence <= 3; occurrence++ {
				body := fmt.Sprintf("Occurrence %d: Orlando is sunny.", occurrence)
				batch := []events.Event{events.NewBotReport(fmt.Sprintf("bot-report:00000000-0000-0000-0000-%012d", occurrence), fmt.Sprintf("report-%d", occurrence), body)}
				journal = append(journal, batch...)
				if replay {
					// Bot history is restored by replaying WatchBot events, not by
					// loading ordinary Chat HistoryMsg entries.
					m.tr = events.NewTranscript()
					for _, ev := range journal {
						m.tr.Apply(ev)
					}
				} else {
					for _, ev := range batch {
						m.tr.Apply(ev)
					}
				}
				out, hits, _ := m.renderTranscript()
				plain := stripANSI(out)
				// Body presence alone misses concatenation into a single bubble.
				// Assert separate rendered ItemAgent regions as well as all text.
				agents := 0
				for _, hit := range hits {
					if hit.kind == events.ItemAgent {
						agents++
					}
				}
				if agents != occurrence+1 {
					t.Errorf("after occurrence %d: rendered agent regions=%d, want %d\n%s", occurrence, agents, occurrence+1, plain)
				}
				for prior := 1; prior <= occurrence; prior++ {
					text := fmt.Sprintf("Occurrence %d: Orlando is sunny.", prior)
					if strings.Count(strings.Join(strings.Fields(plain), " "), text) != 1 {
						t.Errorf("after occurrence %d: report body %q must remain visible exactly once\n%s", occurrence, text, plain)
					}
				}
				for _, item := range m.tr.Items {
					if item.Kind == events.ItemActivity && item.Expanded {
						t.Error("reports required expanding earlier tool activity")
					}
				}
			}
		})
	}
}
