package entstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Uses an on-disk scoped database and the real dispatch/cancellation/cleanup
// transactions. Only the cron version and physical-home binding are fixture seeds.
func scheduledInterruptionFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, context.Context, *store.BotRun, store.BotLease, *time.Time, store.BotScheduledOccurrence, func()) {
	t.Helper()
	var reopen func()
	r, owner, worker, initial, lease, now := progressFixture(t, scope, &reopen)
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: finishedAgentWorkReport(t, r, worker, lease, "setup", store.AgentWorkAnswered)}); err != nil {
		t.Fatal(err)
	}
	root, err := r.db.GetBot(owner, initial.BotID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.db.ReadVersion(owner, initial.BotID, 1)
	if err != nil {
		t.Fatal(err)
	}
	jobID, triggerID := uuid.New(), uuid.New()
	v.Version = 2
	v.Triggers = []store.TriggerSpec{{ID: triggerID, Kind: store.TriggerCron, ScheduleID: &jobID, Instructions: "Read current public research", Cron: "* * * * *", Timezone: "UTC", Enabled: true}}
	if err = r.db.InsertVersion(owner, *v, "scheduled-interruption-v2"); err != nil {
		t.Fatal(err)
	}
	// Cleanup requires the exact persisted physical-home identity.
	home := store.BotHomeBinding{ID: uuid.New(), BotID: initial.BotID, Scope: scope, Revision: 1, Backend: "fixture", StorageKey: "fixture"}
	switch db := r.db.(type) {
	case personalBotDB:
		data, e := db.client.Bot.Get(owner, initial.BotID)
		if e != nil {
			t.Fatal(e)
		}
		binding, e := botJSONMap(home)
		if e != nil {
			t.Fatal(e)
		}
		if e = db.client.Bot.UpdateOneID(initial.BotID).SetVersion(2).SetHomeBinding(binding).SetData(data.Data).Exec(owner); e != nil {
			t.Fatal(e)
		}
	case teamBotDB:
		data, e := db.client.Bot.Get(owner, initial.BotID)
		if e != nil {
			t.Fatal(e)
		}
		binding, e := botJSONMap(home)
		if e != nil {
			t.Fatal(e)
		}
		if e = db.client.Bot.UpdateOneID(initial.BotID).SetVersion(2).SetHomeBinding(binding).SetData(data.Data).Exec(owner); e != nil {
			t.Fatal(e)
		}
	}
	due := *now
	jobs := r.db.(botTriggerDB).triggerScheduler()
	if err = jobs.Add(owner, &store.ScheduledJob{ID: jobID.String(), Name: "research", Mode: "bot_trigger", OwnerID: root.Bot.OwnerID.String(), Enabled: true, NextRun: &due, Schedule: store.JobSchedule{Cron: "* * * * *", Timezone: "UTC"}, Payload: store.JobPayload{BotID: initial.BotID.String(), BotVersion: 2, BotTriggerID: triggerID.String()}}); err != nil {
		t.Fatal(err)
	}
	q := store.BotScheduledOccurrence{BotID: initial.BotID, Version: 2, TriggerID: triggerID, JobID: jobID.String(), OccurrenceAt: due}
	scheduledInterruptionKey(&q)
	admission, err := r.AdmitScheduledRun(owner, q)
	if err != nil {
		t.Fatal(err)
	}
	run := &admission.Run
	worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: root.Bot.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "research", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return r, owner, worker, run, lease, now, q, reopen
}

func scheduledInterruptionKey(q *store.BotScheduledOccurrence) {
	identity := fmt.Sprintf("%s/%d/%s/%s", q.BotID, q.Version, q.TriggerID, q.OccurrenceAt.UTC().Format(time.RFC3339Nano))
	q.IdempotencyKey = fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
}

func scheduledInterruptionNext(t *testing.T, r *botRepository, ctx context.Context, now *time.Time, q store.BotScheduledOccurrence) store.BotScheduledOccurrence {
	t.Helper()
	job := r.db.(botTriggerDB).triggerScheduler().Get(ctx, q.JobID)
	if job.NextRun == nil {
		t.Fatal("missing future tick")
	}
	*now = *job.NextRun
	q.OccurrenceAt = *now
	scheduledInterruptionKey(&q)
	return q
}

func TestBotScheduledOpenAssignmentStillCoalesces(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, suspended := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/suspended=%t", scope, suspended), func(t *testing.T) {
				r, owner, worker, run, lease, now, q, reopen := scheduledInterruptionFixture(t, scope)
				if suspended {
					cp := progressCheckpoint(t, r, worker, lease, *now)
					cp.State.Phase = store.BotProgressSuspended
					if _, err := r.CheckpointProgress(worker, cp); err != nil {
						t.Fatal(err)
					}
				}
				reopen()
				q = scheduledInterruptionNext(t, r, owner, now, q)
				got, err := r.AdmitScheduledRun(owner, q)
				if err != nil || !got.Coalesced || got.Run.ID != run.ID {
					t.Fatalf("open assignment did not coalesce: %+v %v", got, err)
				}
			})
		}
	}
}

func TestBotScheduledInterruptedDispatchClassification(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"pending-model", "settled-read-pending-model", "pending-tool", "uncertain-tool", "tool-without-receipt", "no-teardown", "missing-ledger", "recovered-no-teardown", "unknown-ledger"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				r, owner, worker, run, lease, now, q, reopen := scheduledInterruptionFixture(t, scope)
				mutation := progressCheckpoint(t, r, worker, lease, *now).Mutation
				if scenario == "settled-read-pending-model" {
					read := store.BotProgressBeginDispatchRequest{Mutation: mutation, DispatchID: uuid.New(), Kind: store.BotProgressDispatchTool, OperationDigest: strings.Repeat("a", 64), ReservedUsage: store.BotUsage{ToolCalls: 1}}
					if _, err := r.BeginProgressDispatch(worker, read); err != nil {
						t.Fatal(err)
					}
					if _, err := r.SettleProgressDispatch(worker, store.BotProgressSettleDispatchRequest{Mutation: mutation, DispatchID: read.DispatchID, Outcome: store.BotProgressDispatchSettled, EvidenceRef: "trusted-read:exact-call", Usage: read.ReservedUsage}); err != nil {
						t.Fatal(err)
					}
				}
				kind := store.BotProgressDispatchModel
				if scenario == "pending-tool" || scenario == "uncertain-tool" || scenario == "tool-without-receipt" {
					kind = store.BotProgressDispatchTool
				}
				begin := store.BotProgressBeginDispatchRequest{Mutation: mutation, DispatchID: uuid.New(), Kind: kind, OperationDigest: strings.Repeat("b", 64), ReservedUsage: store.BotUsage{InputTokens: 37}}
				if _, err := r.BeginProgressDispatch(worker, begin); err != nil {
					t.Fatal(err)
				}
				if scenario == "uncertain-tool" || scenario == "tool-without-receipt" {
					outcome, evidence := store.BotProgressDispatchSettled, ""
					if scenario == "uncertain-tool" {
						outcome, evidence = store.BotProgressDispatchUncertain, "write-outcome-unknown"
					}
					if _, err := r.SettleProgressDispatch(worker, store.BotProgressSettleDispatchRequest{Mutation: mutation, DispatchID: begin.DispatchID, Outcome: outcome, EvidenceRef: evidence, Usage: begin.ReservedUsage}); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "recovered-no-teardown" {
					*now = now.Add(2 * time.Minute)
					if err := r.RecoverRun(owner, lease); err != nil {
						t.Fatal(err)
					}
				} else if _, err := r.CancelRun(owner, run.BotID, run.ID); err != nil {
					t.Fatal(err)
				}
				if scenario == "pending-model" || scenario == "settled-read-pending-model" {
					q = scheduledInterruptionNext(t, r, owner, now, q)
					held, e := r.AdmitScheduledRun(owner, q)
					if e != nil || !held.Coalesced || held.Run.ID != run.ID {
						t.Fatalf("missing teardown released run: %+v %v", held, e)
					}
				}
				root, err := r.db.GetBot(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "no-teardown" && scenario != "recovered-no-teardown" {
					cleanup := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: run.BotID, RunID: run.ID, RunFence: lease.Fence, Fence: root.Fence, ExpectedRevision: root.Bot.Revision, Home: root.Bot.Home}
					if err = r.ConfirmCleanup(cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil }), cleanup); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "missing-ledger" || scenario == "unknown-ledger" {
					// A private cursor without the corresponding ledger cannot prove safety.
					saved, e := r.db.ReadRun(owner, run.BotID, run.ID)
					if e != nil {
						t.Fatal(e)
					}
					saved.ProgressCursor++
					if scenario == "unknown-ledger" {
						saved.ProgressCursor, saved.ProgressPending = 0, 0
						saved.ProgressUncertain = false
					}
					if e = saveProgressEnvelope(owner, r.db, saved); e != nil {
						t.Fatal(e)
					}
				}
				reopen()
				before, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if before.Progress != nil {
					t.Fatal("fixture must reproduce missing checkpoint")
				}
				rootBefore, err := r.db.GetBot(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				q = scheduledInterruptionNext(t, r, owner, now, q)
				got, err := r.AdmitScheduledRun(owner, q)
				if err != nil {
					t.Fatal(err)
				}
				safe := scenario == "pending-model" || scenario == "settled-read-pending-model"
				if safe && (got.Coalesced || got.Run.ID == run.ID || got.Run.SessionID == run.SessionID || got.Run.Status != store.RunQueued) {
					t.Fatalf("future tick did not create distinct run: %+v", got)
				}
				if !safe && (!got.Coalesced || got.Run.ID != run.ID) {
					t.Fatalf("unsafe prior work released: %+v", got)
				}
				replay, err := r.AdmitScheduledRun(owner, q)
				if err != nil || !replay.Duplicate || replay.Run.ID != got.Run.ID {
					t.Fatalf("duplicate tick: %+v %v", replay, err)
				}
				after, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Run.Status != store.RunInterrupted || !reflect.DeepEqual(after.Run.Usage, before.Run.Usage) || after.ProgressPending != before.ProgressPending || after.ProgressUncertain != before.ProgressUncertain || !reflect.DeepEqual(after.ProgressQuota, before.ProgressQuota) || !reflect.DeepEqual(after.Run.Report, before.Run.Report) {
					t.Fatal("classification rewrote prior failure or billing")
				}
				rootAfter, err := r.db.GetBot(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(rootBefore.Metadata, rootAfter.Metadata) {
					t.Fatal("classification changed quota/authority metadata")
				}
				if safe {
					// A held tick is a durable receipt, not backlog to replay after teardown.
					prior := q
					prior.OccurrenceAt = q.OccurrenceAt.Add(-time.Minute)
					scheduledInterruptionKey(&prior)
					heldAgain, e := r.AdmitScheduledRun(owner, prior)
					if e != nil || !heldAgain.Duplicate || !heldAgain.Coalesced || heldAgain.Run.ID != run.ID {
						t.Fatalf("held tick replayed after teardown: %+v %v", heldAgain, e)
					}
					nextInput, e := r.LoadRunInput(owner, run.BotID, got.Run.ID)
					if e != nil {
						t.Fatal(e)
					}
					if nextInput.Progress != nil || len(nextInput.Run.Checkpoint) != 0 || nextInput.Work != nil || nextInput.Run.StartedAt != nil {
						t.Fatal("new occurrence resumed old work")
					}
					// The newly queued occurrence still coalesces subsequent ticks.
					q = scheduledInterruptionNext(t, r, owner, now, q)
					coalesced, e := r.AdmitScheduledRun(owner, q)
					if e != nil || !coalesced.Coalesced || coalesced.Run.ID != got.Run.ID {
						t.Fatalf("queued coalescing changed: %+v %v", coalesced, e)
					}
				}
			})
		}
	}
}
