package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotScheduleFailureCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, change := range []string{"retry", "terminal", "pause", "edit", "version"} {
			t.Run(string(scope)+"/"+change, func(t *testing.T) {
				_, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 100)
				before := jobs.Get(ctx, q.JobID)
				switch change {
				case "pause":
					before.Enabled = false
				case "edit":
					next := q.OccurrenceAt.Add(time.Hour)
					before.NextRun = &next
					before.Name = "edited"
				case "version":
					before.Payload.BotVersion++
				}
				if err := jobs.Update(ctx, before); err != nil {
					t.Fatal(err)
				}
				outcome := store.BotScheduleTerminal
				if change == "retry" {
					outcome = store.BotScheduleRetry
				}
				applied, err := jobs.(store.BotScheduleFailureStore).FailBotSchedule(ctx, q, time.Now(), "denied", outcome)
				if err != nil {
					t.Fatal(err)
				}
				after := jobs.Get(ctx, q.JobID)
				if change == "terminal" {
					if !applied || after.Enabled || after.NextRun != nil || after.ConsecutiveFailures != 1 {
						t.Fatalf("terminal did not stop: %+v", after)
					}
				} else if change == "retry" {
					if !applied || !after.Enabled || !after.NextRun.Equal(q.OccurrenceAt) || after.ConsecutiveFailures != 1 {
						t.Fatalf("retry changed occurrence: %+v", after)
					}
				} else if applied || after.Enabled != before.Enabled || !after.NextRun.Equal(*before.NextRun) || after.Payload.BotVersion != before.Payload.BotVersion || after.Name != before.Name || after.ConsecutiveFailures != 0 {
					t.Fatalf("stale failure overwrote edit: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func TestBotScheduleFailureClassification(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, tc := range []struct {
			name     string
			cause    error
			terminal bool
		}{
			{"forbidden", store.ErrBotForbidden, true},
			{"invalid", store.ErrBotInvalid, true},
			{"unsupported", store.ErrBotUnsupported, true},
			{"conflict", store.ErrBotConflict, false},
			{"unknown", errors.New("temporary infrastructure failure"), false},
			{"typed-retry", &store.BotScheduleRejected{Cause: store.ErrBotForbidden, Outcome: store.BotScheduleRetry}, false},
		} {
			t.Run(string(scope)+"/"+tc.name, func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 100)
				err := repo.rejectScheduledRun(ctx, q, tc.cause)
				var rejected *store.BotScheduleRejected
				if !errors.As(err, &rejected) || !rejected.Acknowledged || (rejected.Outcome == store.BotScheduleTerminal) != tc.terminal {
					t.Fatalf("classification: %+v", rejected)
				}
				job := jobs.Get(ctx, q.JobID)
				if job.ConsecutiveFailures != 1 || job.Enabled == tc.terminal || (job.NextRun == nil) != tc.terminal {
					t.Fatalf("failure bookkeeping: %+v", job)
				}
			})
		}
	}
}

func TestBotScheduleFailureAdmissionTerminal(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 100)
			// Stored executable payload violates the trusted admission binding.
			job := jobs.Get(ctx, q.JobID)
			job.Payload.Instructions = "untrusted"
			if err := jobs.Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			_, err := repo.AdmitScheduledRun(ctx, q)
			var rejected *store.BotScheduleRejected
			if !errors.As(err, &rejected) || !errors.Is(err, store.ErrBotForbidden) || !rejected.Acknowledged || rejected.Outcome != store.BotScheduleTerminal {
				t.Fatalf("denial receipt: %v", err)
			}
			job = jobs.Get(ctx, q.JobID)
			if job.Enabled || job.NextRun != nil || job.ConsecutiveFailures != 1 {
				t.Fatalf("denial retries: %+v", job)
			}
			// Replaying the rejected request must not touch a halted occurrence.
			_, _ = repo.AdmitScheduledRun(ctx, q)
			if after := jobs.Get(ctx, q.JobID); after.Enabled || after.NextRun != nil || after.ConsecutiveFailures != 1 {
				t.Fatalf("replay changed stopped occurrence: %+v", after)
			}
			runs, err := repo.ListRuns(ctx, q.BotID, store.BotPage{})
			if err != nil || len(runs) != 0 {
				t.Fatalf("denial admitted work: %+v %v", runs, err)
			}
		})
	}
}

func TestBotScheduleFailureRequiresOwner(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, actor := range []string{"wrong-owner", "removed", "unauthenticated"} {
			t.Run(string(scope)+"/"+actor, func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 100)
				switch actor {
				case "wrong-owner":
					p, _ := execution.PrincipalFromContext(ctx)
					p.Subject = uuid.New().String()
					ctx, _ = execution.WithPrincipal(ctx, p)
				case "removed":
					repo.auth.check = func(context.Context, string) (string, error) { return "", nil }
				case "unauthenticated":
					ctx = context.Background()
				}
				err := repo.rejectScheduledRun(ctx, q, store.ErrBotForbidden)
				var rejected *store.BotScheduleRejected
				if !errors.As(err, &rejected) || rejected.Acknowledged {
					t.Fatalf("unauthorized failure acknowledged: %v", err)
				}
				job := jobs.Get(ctx, q.JobID)
				if !job.Enabled || !job.NextRun.Equal(q.OccurrenceAt) || job.LastRun != nil || job.ConsecutiveFailures != 0 {
					t.Fatalf("unauthorized mutation: %+v", job)
				}
			})
		}
	}
}

// Exercise the same callback receipt consumed by both daemon lanes, with real
// scoped stores. The callback is released only after a concurrent user edit.
func TestBotScheduleFailureManagedCallbackRace(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, change := range []string{"pause", "edit", "version"} {
			t.Run(string(scope)+"/"+change, func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 100)
				entered, release := make(chan struct{}), make(chan struct{})
				executor := &scheduler.Executor{BotTrigger: func(ctx context.Context, _ scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
					close(entered)
					<-release
					return scheduler.BotAdmission{}, repo.rejectScheduledRun(ctx, q, store.ErrBotForbidden)
				}}
				job := &scheduler.Job{ID: q.JobID, Enabled: true, Mode: scheduler.ModeBotTrigger, NextRun: &q.OccurrenceAt, Payload: scheduler.JobPayload{BotID: q.BotID.String(), BotVersion: q.Version, BotTriggerID: q.TriggerID.String()}}
				ctx, ack := scheduler.WithBotAcknowledgement(ctx)
				done := make(chan error, 1)
				go func() { _, err := executor.Execute(ctx, job); done <- err }()
				<-entered
				before := jobs.Get(ctx, q.JobID)
				switch change {
				case "pause":
					before.Enabled = false
				case "edit":
					next := q.OccurrenceAt.Add(time.Hour)
					before.NextRun = &next
				case "version":
					before.Payload.BotVersion++
				}
				if err := jobs.Update(ctx, before); err != nil {
					close(release)
					<-done
					t.Fatal(err)
				}
				close(release)
				if err := <-done; !errors.Is(err, store.ErrBotForbidden) || *ack != scheduler.BotScheduleAcknowledged {
					t.Fatalf("receipt: %v %v", err, *ack)
				}
				after := jobs.Get(ctx, q.JobID)
				if after.Enabled != before.Enabled || after.NextRun == nil || !after.NextRun.Equal(*before.NextRun) || after.Payload.BotVersion != before.Payload.BotVersion || after.ConsecutiveFailures != 0 {
					t.Fatalf("rejection overwrote concurrent edit: %+v", after)
				}
			})
		}
	}
}

func TestBotScheduleManagedRejection(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerCron, 100)
			denied := &store.BotScheduleRejected{Cause: store.ErrBotForbidden, Outcome: store.BotScheduleTerminal}
			err := repo.rejectScheduledRun(ctx, q, denied)
			var receipt *store.BotScheduleRejected
			if !errors.As(err, &receipt) || !receipt.Acknowledged || receipt.Outcome != store.BotScheduleTerminal || !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("receipt: %v", err)
			}
			if j := jobs.Get(ctx, q.JobID); j.Enabled || j.NextRun != nil {
				t.Fatalf("terminal spins: %+v", j)
			}
		})
	}
}
