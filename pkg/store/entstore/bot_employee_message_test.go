package entstore

import (
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
	"time"
)

func TestBotEmployeeCommitAndPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			report := store.BotReport{Summary: "private provider secret https://private.invalid"}
			m := store.NewBotEmployeeFallback(store.RunFailed, report, store.BotEmployeeReasonForbidden)
			report.EmployeeMessage = &m
			q := store.BotRunCommit{Lease: lease, Status: store.RunFailed, Report: report}
			m.Text = "I sent the payment successfully."
			if err := repo.CommitRun(worker, q); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("forged message: %v", err)
			}
			m = store.NewBotEmployeeFallback(store.RunFailed, report, store.BotEmployeeReasonForbidden)
			q.Report.Summary += "changed"
			if err := repo.CommitRun(worker, q); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("stale source: %v", err)
			}
			q.Report.Summary = report.Summary
			if err := repo.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, q); err != nil {
				t.Fatalf("retry: %v", err)
			}
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Report.EmployeeMessage == nil || saved.Report.ValidateEmployeeMessageIntegrity(saved.Status) != nil {
				t.Fatalf("saved: %+v %v", saved, err)
			}
			pub, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || pub.Text != m.Text || strings.Contains(pub.Text, "private") {
				t.Fatalf("publication: %+v %v", pub, err)
			}
			again, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID, ReportSequence: pub.ReportSequence})
			if err != nil || again.EventID != pub.EventID || again.Text != pub.Text {
				t.Fatalf("retry publication: %+v %v", again, err)
			}
		})
	}
}

func TestBotEmployeeRecoveryAndQuiescence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, lease, now := botRunFixture(t, scope)
			*now = lease.ExpiresAt.Add(time.Second)
			if err := repo.RecoverRun(ctx, lease); err != nil {
				t.Fatal(err)
			}
			if err := repo.RecoverRun(ctx, lease); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Report.EmployeeMessage == nil || saved.Report.EmployeeMessage.Reason != store.BotEmployeeReasonInterrupted || saved.Report.ValidateEmployeeMessageIntegrity(saved.Status) != nil {
				t.Fatalf("recovery: %+v %v", saved, err)
			}
			pub, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || pub.Text != saved.Report.EmployeeMessage.Text {
				t.Fatalf("recovery publication: %+v %v", pub, err)
			}
		})
	}
	for _, status := range []store.RunStatus{store.RunRunning, store.RunQueued} {
		report := store.BotReport{Summary: "stale success"}
		report.FinalizeEmployeeMessage(store.RunSucceeded, nil)
		saved := quiescedBotRun(botStoredRun{Run: store.BotRun{Status: status, Report: report}}, true, time.Now())
		if saved.Run.Report.ValidateEmployeeMessageIntegrity(saved.Run.Status) != nil || saved.Run.Report.EmployeeMessage.Text == report.EmployeeMessage.Text {
			t.Fatal("quiescence retained stale envelope")
		}
	}
}
