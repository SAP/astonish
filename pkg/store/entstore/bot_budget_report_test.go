package entstore

import (
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotBudgetReportPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			cause := &store.BotMandateQuotaError{Dimension: store.BotQuotaTotalTokens, Limit: 91, Used: 30, Requested: 70}
			message, _ := bot.BudgetFailureMessage(cause)
			report := store.BotReport{Summary: message, Failure: &store.BotReportFailure{Kind: store.BotReportFailureBudget, TechnicalDetails: bot.BudgetFailureDetails(cause)}}
			before, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunFailed, Report: report, Usage: store.BotUsage{InputTokens: 30}}); err != nil {
				t.Fatal(err)
			}
			publisher := &bot.Publisher{Store: repo, Now: func() time.Time { return *now }}
			var first *store.BotPublication
			for range 2 {
				publication, err := publisher.Publish(ctx, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if first != nil && (publication.EventID != first.EventID || publication.ReportSequence != first.ReportSequence || publication.Text != first.Text) {
					t.Fatal("publication retry changed canonical receipt")
				}
				first = publication
			}
			after, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Status != after.Status {
				t.Fatal("publication changed bot state")
			}
			rows := reviewTranscript(t, ctx, repo, *after)
			if len(rows) != 1 {
				t.Fatalf("want one canonical row got %d", len(rows))
			}
			e := rows[0].Event
			if e == nil || e.ID != first.EventID || e.Author != "assistant" || e.InvocationID != "bot-report:"+run.ID.String() || e.Content == nil || len(e.Content.Parts) != 1 || e.Content.Parts[0] == nil {
				t.Fatalf("not canonical %+v", e)
			}
			text := e.Content.Parts[0].Text
			if text != store.NewBotEmployeeFallback(store.RunFailed, report, "").Text || text != first.Text {
				t.Fatalf("unexpected employee message: %q", text)
			}
			for _, unwanted := range []string{"Run report", run.ID.String(), "accounted=30", "requested=70", "limit=91", "Technical details", "[Error:"} {
				if strings.Contains(text, unwanted) {
					t.Errorf("diagnostic %q leaked into employee message: %s", unwanted, text)
				}
			}
			restored, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Status != store.RunFailed || restored.Report.Failure == nil || restored.Report.Summary != message || restored.Usage.InputTokens != 30 {
				t.Fatalf("lost persistence %+v", restored)
			}
			if restored.Report.Failure.Kind != store.BotReportFailureBudget || restored.Report.Failure.TechnicalDetails != report.Failure.TechnicalDetails {
				t.Fatalf("lost budget diagnostics: %+v", restored.Report.Failure)
			}
			diagnostic := bot.RenderDiagnosticReport(*restored)
			for _, want := range []string{message, "Run report", run.ID.String(), "accounted=30", "requested=70", "limit=91", "Technical details"} {
				if !strings.Contains(diagnostic, want) {
					t.Errorf("diagnostic report missing %q: %s", want, diagnostic)
				}
			}
		})
	}
}
