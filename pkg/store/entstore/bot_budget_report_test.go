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
			for range 2 {
				if _, err = publisher.Publish(ctx, run.BotID, run.ID); err != nil {
					t.Fatal(err)
				}
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
			if e.InvocationID != "bot-report:"+run.ID.String() || e.Content == nil {
				t.Fatalf("not canonical %+v", e)
			}
			text := e.Content.Parts[0].Text
			for _, want := range []string{message, "accounted=30", "requested=70", "limit=91", "Technical details"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q: %s", want, text)
				}
			}
			if strings.Contains(text, "[Error:") {
				t.Fatal(text)
			}
			restored, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Status != store.RunFailed || restored.Report.Failure == nil || restored.Report.Summary != message || restored.Usage.InputTokens != 30 {
				t.Fatalf("lost persistence %+v", restored)
			}
		})
	}
}
