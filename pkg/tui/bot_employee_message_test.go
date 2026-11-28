package tui

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotEmployeeMessageTerminalRendering(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), Status: store.RunSucceeded, Report: store.BotReport{Summary: "Here are the US market highlights. Would daily updates be useful?", Unchanged: []store.BotEvidence{{Summary: "HTTP request internal"}}, Quality: &store.BotQualityReport{SchemaVersion: 1, Outcome: store.BotWorkAccepted}}}
	run.Report.EmployeeMessage = &store.BotEmployeeMessage{Origin: store.BotEmployeeMessageAssessed, Text: run.Report.Summary, CandidateDigest: "reviewed-candidate", SourceDigest: run.Report.EmployeeMessageSourceDigest(run.Status)}
	tr := events.NewTranscript()
	tr.Apply(events.NewBotReport("bot-report:"+run.ID.String(), "message-1", bot.RenderReport(run)))
	m := model{theme: DefaultTheme(), tr: tr, width: 110, height: 40, ready: true}
	out, _, _ := m.renderTranscript()
	plain := strings.Join(strings.Fields(stripANSI(out)), " ")
	if !strings.Contains(plain, run.Report.Summary) {
		t.Fatal(plain)
	}
	for _, unwanted := range []string{"Run report", "Quality assessment", "HTTP request internal", run.ID.String()} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("conversation leaked %q: %s", unwanted, plain)
		}
	}
}
