package entstore

import (
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBotAgentEmployeeCommitAndPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, disposition := range []store.AgentWorkDisposition{store.AgentWorkAnswered, store.AgentWorkComplete, store.AgentWorkPartial, store.AgentWorkBlocked} {
			t.Run(string(scope)+"/"+string(disposition), func(t *testing.T) {
				repo, owner, worker, run, lease, _ := botRunFixture(t, scope)
				report := finishedAgentWorkReport(t, repo, worker, lease, "Here is the requested result, with its limitations.", disposition)
				work, err := repo.LoadWork(worker, lease)
				if err != nil {
					t.Fatal(err)
				}
				status := store.RunSucceeded
				if disposition == store.AgentWorkPartial || disposition == store.AgentWorkBlocked {
					status = store.RunPartial
				}
				report.FinalizeEmployeeMessage(status, work)
				if report.EmployeeMessage == nil || report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent {
					t.Fatal("answer acquired legacy assessment or fallback")
				}
				q := store.BotRunCommit{Lease: lease, Status: status, Report: report}
				stale := q
				stale.Lease.Fence++
				if err := repo.CommitRun(worker, stale); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("stale lease: %v", err)
				}
				if err := repo.CommitRun(worker, q); err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(worker, q); err != nil {
					t.Fatalf("retry: %v", err)
				}
				bad := q
				bad.Report.Summary += " forged"
				if err := repo.CommitRun(worker, bad); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("tampered retry: %v", err)
				}
				saved, err := repo.GetRun(owner, run.BotID, run.ID)
				if err != nil || !reflect.DeepEqual(saved.Work, report.Work) || saved.Report.Quality != nil || saved.Report.ValidateEmployeeMessageIntegrity(status) != nil {
					t.Fatalf("persisted agent result: %+v %v", saved, err)
				}
				pub, err := repo.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
				if err != nil || pub.Text != report.Summary || pub.Suppressed {
					t.Fatalf("agent publication: %+v %v", pub, err)
				}
				again, err := repo.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID, ReportSequence: pub.ReportSequence})
				if err != nil || again.EventID != pub.EventID || again.Text != pub.Text {
					t.Fatalf("publication retry: %+v %v", again, err)
				}
			})
		}
	}
}

func TestBotEmployeeCommitAndPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			work, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Summary: "private provider secret https://private.invalid", Work: store.BuildBotWorkProjection(work)}
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
