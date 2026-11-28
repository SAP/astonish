package entstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	personalbotrun "github.com/SAP/astonish/ent/personal/botrun"
	teambotrun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotScheduleDailyQuotaRetry(t *testing.T) {
	testDailyQuotaRecovery(t, false)
}

func TestBotScheduleDailyQuotaExecutorRecovery(t *testing.T) {
	testDailyQuotaRecovery(t, true)
}

func testDailyQuotaRecovery(t *testing.T, throughExecutor bool) {
	t.Helper()
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []store.TriggerKind{store.TriggerCron, store.TriggerGoalDeadline} {
			t.Run(string(scope)+"/"+string(kind), func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, kind, 1)
				v, err := repo.db.ReadVersion(ctx, q.BotID, q.Version)
				if err != nil {
					t.Fatal(err)
				}
				// Pin a non-UTC persona in a new immutable fixture version.
				v.Version++
				v.Persona.Timezone = "Etc/GMT-7"
				binding := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "quota"}
				channelID := uuid.New()
				v.Triggers = append(v.Triggers, store.TriggerSpec{ID: channelID, Kind: store.TriggerChannel, Instructions: "Saved channel input", Enabled: true, ChannelBinding: &binding})
				if err := repo.db.InsertVersion(ctx, *v, "daily-quota-v3"); err != nil {
					t.Fatal(err)
				}
				rec, err := repo.db.GetBot(ctx, q.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.db.UpdateVersion(ctx, q.BotID, rec.Bot.Revision, v.Version, rec.LastSequence+1, time.Now()); err != nil {
					t.Fatal(err)
				}
				q.Version = v.Version
				job := jobs.Get(ctx, q.JobID)
				job.Payload.BotVersion = q.Version
				if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				identity := fmt.Sprintf("%s/%d/%s/%s", q.BotID, q.Version, q.TriggerID, q.OccurrenceAt.UTC().Format(time.RFC3339Nano))
				q.IdempotencyKey = fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
				loc, err := time.LoadLocation(v.Persona.Timezone)
				if err != nil {
					t.Fatal(err)
				}
				local := q.OccurrenceAt.In(loc)
				now := time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 0, 0, loc).UTC()
				repo.now = func() time.Time { return now }
				created := now.In(loc).Add(-time.Hour)
				finished := created.Add(time.Minute)
				seed := store.BotRun{ID: uuid.New(), BotID: q.BotID, Version: q.Version, SessionID: uuid.New(), Trigger: store.TriggerManual, IdempotencyKey: "counted-terminal", Status: store.RunSucceeded, CreatedAt: created, FinishedAt: &finished}
				if err := repo.db.InsertRun(ctx, botStoredRun{Run: seed, RequestHash: fmt.Sprintf("%x", sha256.Sum256([]byte("counted-terminal"))), Instructions: "Completed counted run"}, rec.Bot.OwnerID); err != nil {
					t.Fatal(err)
				}
				// InsertRun intentionally initializes SQL control as queued; finish
				// this controlled historical fixture before counting its terminal run.
				switch db := repo.db.(type) {
				case personalBotDB:
					err = db.client.BotRun.UpdateOneID(seed.ID).SetStatus(personalbotrun.StatusSucceeded).Exec(ctx)
				case teamBotDB:
					err = db.client.BotRun.UpdateOneID(seed.ID).SetStatus(teambotrun.StatusSucceeded).Exec(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				count, err := repo.db.CountRunsSince(ctx, q.BotID, time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc))
				if err != nil || count != 1 {
					t.Fatalf("terminal seed not counted: %d %v", count, err)
				}
				// Manual and genuinely authenticated channel requests keep exact sentinel identity.
				_, err = repo.RequestRun(ctx, store.BotRunRequest{BotID: q.BotID, Version: q.Version, Trigger: store.TriggerManual, Instructions: "manual", IdempotencyKey: "quota-manual"})
				if !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("manual quota identity: %v", err)
				}
				var manualRejected *store.BotScheduleRejected
				if errors.As(err, &manualRejected) {
					t.Fatalf("manual quota acquired schedule semantics: %v", err)
				}
				p, _ := execution.PrincipalFromContext(ctx)
				p.Authentication, p.Surface = execution.AuthMethodChannel, execution.SurfaceChannel
				channelCtx, err := execution.WithPrincipal(ctx, p)
				if err != nil {
					t.Fatal(err)
				}
				_, err = repo.AdmitExternalRun(channelCtx, store.BotExternalEvent{BotID: q.BotID, Version: q.Version, TriggerID: channelID, EventID: "quota-external", Binding: &binding})
				if !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("external quota identity: %v", err)
				}
				var externalRejected *store.BotScheduleRejected
				if errors.As(err, &externalRejected) {
					t.Fatalf("external quota acquired schedule semantics: %v", err)
				}
				if job := jobs.Get(ctx, q.JobID); !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) || job.ConsecutiveFailures != 0 {
					t.Fatalf("non-scheduled quota changed schedule: %+v", job)
				}

				var admitted *store.BotScheduledAdmission
				calls := 0
				executor := &scheduler.Executor{BotTrigger: func(c context.Context, req scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
					calls++
					if req.JobID != q.JobID || req.BotID != q.BotID || req.Version != q.Version || req.TriggerID != q.TriggerID || !req.OccurrenceAt.Equal(q.OccurrenceAt) || req.IdempotencyKey != q.IdempotencyKey {
						t.Fatalf("executor changed retained occurrence: %+v", req)
					}
					out, err := repo.AdmitScheduledRun(c, store.BotScheduledOccurrence{JobID: req.JobID, BotID: req.BotID, Version: req.Version, TriggerID: req.TriggerID, OccurrenceAt: req.OccurrenceAt, IdempotencyKey: req.IdempotencyKey})
					if err != nil {
						return scheduler.BotAdmission{}, err
					}
					admitted = out
					status := scheduler.BotAdmissionAdmitted
					if out.Duplicate {
						status = scheduler.BotAdmissionDuplicate
					}
					return scheduler.BotAdmission{Status: status, RunID: out.Run.ID, Acknowledgement: scheduler.BotScheduleAcknowledged}, nil
				}}
				retained := tickJob(jobs.Get(ctx, q.JobID))
				admit := func() error {
					if !throughExecutor {
						admitted, err = repo.AdmitScheduledRun(ctx, q)
						return err
					}
					c, ack := scheduler.WithBotAcknowledgement(ctx)
					result, e := executor.Execute(c, retained)
					if result != "" || *ack != scheduler.BotScheduleAcknowledged {
						t.Fatalf("managed executor receipt: %q %v %v", result, *ack, e)
					}
					return e
				}
				err = admit()
				var rejected *store.BotScheduleRejected
				if !errors.Is(err, store.ErrBotForbidden) || !errors.As(err, &rejected) || rejected.Outcome != store.BotScheduleRetry || !rejected.Acknowledged {
					t.Fatalf("daily quota must be managed retry, not terminal: %v", err)
				}
				job = jobs.Get(ctx, q.JobID)
				if !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) || job.ConsecutiveFailures != 1 || job.LastRun == nil || !job.LastRun.Equal(now) || job.LastError == "" || job.Payload.BotVersion != q.Version || job.Payload.BotTriggerID != q.TriggerID.String() {
					t.Fatalf("quota did not retain exact enabled occurrence: %+v", job)
				}
				runs, err := repo.ListRuns(ctx, q.BotID, store.BotPage{})
				if err != nil || len(runs) != 1 || runs[0].Status != store.RunSucceeded {
					t.Fatalf("quota rejection admitted work: %+v %v", runs, err)
				}
				// UTC remains on the same day; only persona-local midnight resets allowance.
				now = now.Add(time.Minute)
				if err := admit(); err != nil || admitted == nil || admitted.Duplicate || !admitted.Acknowledged || admitted.Run.Trigger != kind || admitted.Run.IdempotencyKey != q.IdempotencyKey {
					t.Fatalf("midnight admission: %+v %v", admitted, err)
				}
				runID := admitted.Run.ID
				job = jobs.Get(ctx, q.JobID)
				if job.ConsecutiveFailures != 0 || job.LastError != "" || job.LastRun == nil || !job.LastRun.Equal(now) {
					t.Fatalf("admission did not reset failures: %+v", job)
				}
				if kind == store.TriggerCron {
					if !job.Enabled || job.NextRun == nil || !job.NextRun.After(now) {
						t.Fatalf("cron did not advance: %+v", job)
					}
				} else if !job.Enabled || job.NextRun != nil {
					t.Fatalf("deadline did not finish occurrence: %+v", job)
				}
				if err := admit(); err != nil || !admitted.Duplicate || admitted.Run.ID != runID {
					t.Fatalf("occurrence replay: %+v %v", admitted, err)
				}
				if throughExecutor && calls != 3 {
					t.Fatalf("executor callbacks=%d", calls)
				}
				after := jobs.Get(ctx, q.JobID)
				if after.Enabled != job.Enabled || (after.NextRun == nil) != (job.NextRun == nil) || (job.NextRun != nil && !after.NextRun.Equal(*job.NextRun)) || !after.LastRun.Equal(*job.LastRun) || after.ConsecutiveFailures != 0 {
					t.Fatalf("replay acknowledged twice: before=%+v after=%+v", job, after)
				}
				runs, err = repo.ListRuns(ctx, q.BotID, store.BotPage{})
				if err != nil || len(runs) != 2 {
					t.Fatalf("recovery must admit once: %+v %v", runs, err)
				}
				activity, err := repo.ListActivity(ctx, q.BotID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				dispatches := 0
				for _, a := range activity {
					if a.RunID != nil && *a.RunID == runID {
						dispatches++
					}
				}
				if dispatches != 1 {
					t.Fatalf("occurrence dispatches=%d", dispatches)
				}
			})
		}
	}
}

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
