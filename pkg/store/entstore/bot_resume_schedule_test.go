package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotResumeDisabledScheduleApproval(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, occurrence := scheduledFixture(t, scope, store.TriggerCron, 20)
			repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
			receipt, err := repo.AdmitScheduledRun(ctx, occurrence)
			if err != nil {
				t.Fatal(err)
			}
			run := receipt.Run
			owner, _ := execution.PrincipalFromContext(ctx)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner.Subject, OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			action := execution.ActionDescriptor{Tool: "read_file", Effect: execution.EffectUnknown, Arguments: json.RawMessage(`{"path":"/proof.txt"}`)}
			digest, _ := action.Digest()
			a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
				t.Fatal(err)
			}
			b, _ := repo.Get(ctx, run.BotID)
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause recurrence"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			if err := repo.RequeueApprovedRun(ctx, b.ID, run.ID, a.ID); err == nil {
				t.Fatal("paused run requeued")
			}
			b, err = repo.Resume(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "continue exact approval only"})
			if err != nil {
				t.Fatal(err)
			}
			if b.Version != run.Version {
				t.Fatal("resume changed immutable version")
			}
			if err := repo.RequeueApprovedRun(ctx, b.ID, run.ID, a.ID); err != nil {
				t.Fatal(err)
			}
			lease, err = repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "resumed", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: run.Version, ActionDigest: digest}); err != nil {
				t.Fatal(err)
			}
			job := jobs.Get(ctx, occurrence.JobID)
			if job.Enabled || job.NextRun != nil {
				t.Fatal("recurrence resumed")
			}
			if rows, err := repo.ListRuns(ctx, b.ID, store.BotPage{}); err != nil || len(rows) != 1 || rows[0].ID != run.ID {
				t.Fatalf("unexpected catchup: %+v %v", rows, err)
			}
		})
	}
}

func TestBotResumeDisabledScheduleRejectsCorruption(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"owner", "version", "bot", "trigger", "cron", "timezone", "payload", "next-run", "missing", "active"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				repo, ctx, jobs, occurrence := scheduledFixture(t, scope, store.TriggerCron, 20)
				b, _ := repo.Get(ctx, occurrence.BotID)
				b, err := repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause"})
				if err != nil {
					t.Fatal(err)
				}
				v, _ := repo.GetVersion(ctx, b.ID, b.Version)
				job := jobs.Get(ctx, occurrence.JobID)
				switch scenario {
				case "owner":
					b.OwnerID = uuid.New()
				case "version":
					job.Payload.BotVersion++
				case "bot":
					job.Payload.BotID = uuid.NewString()
				case "trigger":
					job.Payload.BotTriggerID = uuid.NewString()
				case "cron":
					job.Schedule.Cron = "*/2 * * * *"
				case "timezone":
					job.Schedule.Timezone = "Europe/Berlin"
				case "payload":
					job.Payload.Instructions = "injected"
				case "next-run":
					now := time.Now()
					job.NextRun = &now
				case "active":
					// Only inert lifecycle states may carry disabled projections.
					b.Status = store.BotActive
				}
				if scenario == "missing" {
					if err := jobs.Remove(ctx, job.ID); err != nil {
						t.Fatal(err)
					}
				} else if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				want := store.ErrBotConflict
				if scenario == "missing" {
					want = store.ErrBotNotFound
				}
				if err := validateActivationTriggers(ctx, repo.db, *b, *v); !errors.Is(err, want) {
					t.Fatalf("corruption accepted: %v", err)
				}
			})
		}
	}
}
