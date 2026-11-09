package entstore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotCompletedGoalDeadlineRejectsFreshAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, staleEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stale-enabled=%t", scope, staleEnabled), func(t *testing.T) {
				repo, ctx, jobs, q := scheduledFixture(t, scope, store.TriggerGoalDeadline, 20)
				b, err := repo.Get(ctx, q.BotID)
				if err != nil {
					t.Fatal(err)
				}
				v, err := repo.GetVersion(ctx, q.BotID, q.Version)
				if err != nil {
					t.Fatal(err)
				}
				_, err = repo.CompleteGoal(ctx, store.BotGoalCompletion{BotID: b.ID, GoalID: *v.Triggers[0].GoalID, ExpectedRevision: 1, ActorID: b.OwnerID, Evidence: []store.BotEvidence{{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Complete before deadline admission", Source: "test", ActorID: b.OwnerID, ObservedAt: time.Now()}}})
				if err != nil {
					t.Fatal(err)
				}
				job := jobs.Get(ctx, q.JobID)
				if job.Enabled || job.NextRun != nil {
					t.Fatalf("completion left schedule active: %+v", job)
				}
				if staleEnabled {
					// Simulate a stale scheduler write: the live goal gate must
					// reject independently of the disabled schedule gate.
					job.Enabled, job.NextRun = true, &q.OccurrenceAt
					if err := jobs.Update(ctx, job); err != nil {
						t.Fatal(err)
					}
				}
				receipt, err := repo.AdmitScheduledRun(ctx, q)
				if err == nil || receipt != nil {
					t.Fatalf("completed goal admitted fresh work: %+v %v", receipt, err)
				}
				if staleEnabled && !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("expected live goal rejection, got %v", err)
				}
				runs, err := repo.ListRuns(ctx, b.ID, store.BotPage{})
				if err != nil || len(runs) != 0 {
					t.Fatalf("completed goal created runs: %+v %v", runs, err)
				}
				activity, err := repo.ListActivity(ctx, b.ID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range activity {
					if event.RunID != nil {
						t.Fatalf("completed goal created dispatch activity: %+v", event)
					}
				}
			})
		}
	}
}

func TestBotGoalDeadlineTriggerConfiguration(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, seed := scheduledFixture(t, scope, store.TriggerCron, 20)
			due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			goal, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: seed.BotID, Goal: store.BotGoal{BotID: seed.BotID, Title: "Deadline", CompletionCriteria: "Observed", DueAt: &due}})
			if err != nil {
				t.Fatal(err)
			}
			tid := uuid.New()
			change := func(action store.BotTriggerAction, def store.TriggerSpec) *store.Bot {
				t.Helper()
				b, err := repo.Get(ctx, seed.BotID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "reviewed deadline", Definition: def})
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			b := change(store.BotTriggerSave, store.TriggerSpec{Kind: store.TriggerGoalDeadline, GoalID: &goal.ID, Instructions: "Review deadline"})
			v, _ := repo.GetVersion(ctx, b.ID, b.Version)
			tr := v.Triggers[len(v.Triggers)-1]
			if tr.ScheduleID == nil {
				t.Fatal("no owned schedule")
			}
			job := jobs.Get(ctx, tr.ScheduleID.String())
			if job == nil || job.Enabled || job.NextRun != nil {
				t.Fatalf("bad disabled schedule %+v", job)
			}
			b = change(store.BotTriggerEnable, store.TriggerSpec{})
			job = jobs.Get(ctx, tr.ScheduleID.String())
			if !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(due) || job.Schedule.Cron != store.BotDeadlineSchedule {
				t.Fatalf("not one-shot due schedule %+v", job)
			}
			identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.Format(time.RFC3339Nano))
			q := store.BotScheduledOccurrence{JobID: job.ID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
			admitted, err := repo.AdmitScheduledRun(ctx, q)
			if err != nil || admitted.Run.Trigger != store.TriggerGoalDeadline {
				t.Fatalf("deadline admission %+v %v", admitted, err)
			}
			_, err = repo.CompleteGoal(ctx, store.BotGoalCompletion{BotID: b.ID, GoalID: goal.ID, ExpectedRevision: goal.Revision, ActorID: b.OwnerID, Evidence: []store.BotEvidence{{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Complete", Source: "test", ActorID: b.OwnerID, ObservedAt: time.Now()}}})
			if err != nil {
				t.Fatal(err)
			}
			job = jobs.Get(ctx, job.ID)
			if job.Enabled || job.NextRun != nil {
				t.Fatalf("completion left followup active %+v", job)
			}
			// A replay is a receipt for the already committed occurrence, not new
			// admission. Completion must not break idempotency or create work.
			before, err := repo.ListActivity(ctx, b.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := repo.AdmitScheduledRun(ctx, q)
			if err != nil || replay == nil || !replay.Duplicate || !replay.Acknowledged || replay.Run.ID != admitted.Run.ID {
				t.Fatalf("completed deadline replay %+v %v", replay, err)
			}
			runs, err := repo.ListRuns(ctx, b.ID, store.BotPage{})
			if err != nil || len(runs) != 1 {
				t.Fatalf("replay created work: %+v %v", runs, err)
			}
			after, err := repo.ListActivity(ctx, b.ID, 0, 100)
			if err != nil || len(after) != len(before) {
				t.Fatalf("replay created activity: before=%d after=%d err=%v", len(before), len(after), err)
			}
			if job := jobs.Get(ctx, job.ID); job.Enabled || job.NextRun != nil {
				t.Fatalf("replay resurrected schedule %+v", job)
			}
			// An unrelated version edit cannot resurrect a completed goal schedule.
			change(store.BotTriggerDisable, store.TriggerSpec{})
			if job := jobs.Get(ctx, job.ID); job.Enabled || job.NextRun != nil {
				t.Fatalf("resurrected %+v", job)
			}
			b, _ = repo.Get(ctx, b.ID)
			missing := uuid.New()
			if _, err := repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: uuid.New(), ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "invalid goal", Definition: store.TriggerSpec{Kind: store.TriggerGoalDeadline, GoalID: &missing, Instructions: "Invalid"}}); err == nil {
				t.Fatal("missing goal accepted")
			}
		})
	}
}
