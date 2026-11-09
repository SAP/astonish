package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSchedulerDisabledAndDelivery(t *testing.T) {
	executor := &scheduler.Executor{}
	delivered := 0
	mts := NewMultiTenantScheduler(nil, executor, func(context.Context, *scheduler.Job, string, error) error { delivered++; return nil }, nil)
	mts.SetBotSchedulerCallbacks(BotSchedulerCallbacks{Resolve: func(context.Context, scheduler.BotTriggerRequest) (context.Context, store.BotScheduledRunStore, error) {
		t.Fatal("resolver called without runtime")
		return nil, nil, nil
	}})
	if executor.BotTrigger != nil {
		t.Fatal("runtime-less callback enabled")
	}
	for _, err := range []error{nil, errors.New("admission failed")} {
		if e := mts.deliver(context.Background(), &scheduler.Job{Mode: scheduler.ModeBotTrigger}, "", err); e != nil {
			t.Fatal(e)
		}
	}
	if delivered != 0 {
		t.Fatal("Bot leaked legacy notification")
	}
	if err := mts.deliver(context.Background(), &scheduler.Job{Mode: scheduler.ModeRoutine}, "done", nil); err != nil || delivered != 1 {
		t.Fatal("legacy delivery changed")
	}
}

func TestBotSchedulerAdapterPreservesBinding(t *testing.T) {
	due := time.Now().Add(-time.Minute)
	original := &store.ScheduledJob{ID: uuid.NewString(), Mode: string(scheduler.ModeBotTrigger), Enabled: true, NextRun: &due, Payload: store.JobPayload{BotID: uuid.NewString(), BotVersion: 42, BotTriggerID: uuid.NewString()}}
	round := schedulerJobToStoreJob(storeJobToSchedulerJob(original))
	if round.Payload.BotID != original.Payload.BotID || round.Payload.BotVersion != 42 || round.Payload.BotTriggerID != original.Payload.BotTriggerID || !round.NextRun.Equal(due) {
		t.Fatalf("lost binding: %+v", round)
	}
}

type botSchedulerJobs struct {
	store.SchedulerStore
	job *store.ScheduledJob
}

func (s *botSchedulerJobs) Get(context.Context, string) *store.ScheduledJob { j := *s.job; return &j }
func (s *botSchedulerJobs) List(context.Context) []*store.ScheduledJob {
	return []*store.ScheduledJob{s.Get(context.Background(), s.job.ID)}
}
func (s *botSchedulerJobs) Update(_ context.Context, j *store.ScheduledJob) error {
	s.job = j
	return nil
}

type admissionOnlyRuntime struct{ botdomain.Runtime }
type schedulerAdmissionStore struct {
	admit func(context.Context, store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error)
}

func (s schedulerAdmissionStore) AdmitScheduledRun(ctx context.Context, q store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error) {
	return s.admit(ctx, q)
}

func TestBotSchedulerOwnerResolutionAndTenantLanes(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(map[bool]string{false: "team", true: "personal"}[personal], func(t *testing.T) {
			due := time.Now().Add(-time.Minute)
			jobs := &botSchedulerJobs{job: &store.ScheduledJob{ID: uuid.NewString(), Name: "Bot", Mode: string(scheduler.ModeBotTrigger), Enabled: true, OwnerID: uuid.NewString(), NextRun: &due, Schedule: store.JobSchedule{Cron: "* * * * *", Timezone: "UTC"}, Payload: store.JobPayload{BotID: uuid.NewString(), BotVersion: 1, BotTriggerID: uuid.NewString()}}}
			calls := 0
			executor := &scheduler.Executor{}
			mts := NewMultiTenantScheduler(nil, executor, func(context.Context, *scheduler.Job, string, error) error { t.Fatal("legacy delivery"); return nil }, nil)
			ownerCtx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: jobs.job.OwnerID, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			mts.SetBotSchedulerCallbacks(BotSchedulerCallbacks{Runtime: admissionOnlyRuntime{}, Resolve: func(ctx context.Context, req scheduler.BotTriggerRequest) (context.Context, store.BotScheduledRunStore, error) {
				scope, ok := BotSchedulerScopeFromContext(ctx)
				if !ok || scope.OrgSlug != "org" {
					t.Fatalf("missing scope: %+v", scope)
				}
				if _, ok := execution.PrincipalFromContext(ctx); ok {
					t.Fatal("scheduler minted principal")
				}
				if personal && scope.UserID != jobs.job.OwnerID {
					t.Fatal("personal routing lost")
				}
				return ownerCtx, schedulerAdmissionStore{admit: func(c context.Context, q store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error) {
					calls++
					if c != ownerCtx || q.JobID != req.JobID || !q.OccurrenceAt.Equal(due) {
						t.Fatal("binding changed")
					}
					return &store.BotScheduledAdmission{Run: store.BotRun{ID: uuid.New(), Status: store.RunQueued}}, nil
				}}, nil
			}})
			// Nil credential-bearing stores ensure neither lane loads legacy secrets.
			if personal {
				mts.executePersonalJob(context.Background(), jobs.job, "org", nil, nil, jobs, jobs.job.OwnerID)
			} else {
				mts.executeTeamJob(context.Background(), jobs.job, "org", "team", nil, nil, jobs)
			}
			if calls != 1 {
				t.Fatalf("admissions=%d", calls)
			}
		})
	}
}

func TestBotSchedulerPreservesRetryOccurrence(t *testing.T) {
	due := time.Now().Add(-time.Minute)
	jobs := &botSchedulerJobs{job: &store.ScheduledJob{ID: uuid.NewString(), Mode: string(scheduler.ModeBotTrigger), Enabled: true, NextRun: &due, Schedule: store.JobSchedule{Cron: "* * * * *", Timezone: "UTC"}}}
	mts := NewMultiTenantScheduler(nil, &scheduler.Executor{}, nil, nil)
	if n := mts.refreshStoreNextRuns(context.Background(), jobs); n != 0 || !jobs.job.NextRun.Equal(due) {
		t.Fatal("startup discarded due occurrence")
	}
	mts.updateJobState(context.Background(), jobs, jobs.job, "", errors.New("retry"))
	if !jobs.job.NextRun.Equal(due) {
		t.Fatal("error advanced occurrence")
	}
	mts.updateJobState(context.Background(), jobs, jobs.job, "", nil)
	if !jobs.job.NextRun.After(due) {
		t.Fatal("admission not acknowledged")
	}
}
