package entstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotProgressScheduledContinuation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"continue", "expired", "parent-revoked", "child-revoked", "policy-denied", "owner-expired", "historical-exhausted", "uncertain"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				r, s, owner, worker, q := authorizedScheduleFixture(t, scope, true)
				op, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "scheduled")}); err != nil {
					t.Fatal(err)
				}
				applied, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
				if err != nil {
					t.Fatal(err)
				}
				receipt := applied.Receipt.Schedule
				jobs := r.db.(botTriggerDB).triggerScheduler()
				job := jobs.Get(owner, receipt.ScheduleID.String())
				now := r.clock()
				r.now = func() time.Time { return now }
				s.repo.now = r.now
				due := now
				job.NextRun = &due
				if err = jobs.Update(owner, job); err != nil {
					t.Fatal(err)
				}
				identity := fmt.Sprintf("%s/%d/%s/%s", q.MandateRef.BotID, receipt.Version, receipt.TriggerID, due.UTC().Format(time.RFC3339Nano))
				admitted, err := r.AdmitScheduledRun(owner, store.BotScheduledOccurrence{BotID: q.MandateRef.BotID, Version: receipt.Version, TriggerID: receipt.TriggerID, JobID: receipt.ScheduleID.String(), OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))})
				if err != nil {
					t.Fatal(err)
				}
				run := admitted.Run
				ref := *run.MandateRef
				worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: ref.OwnerID, OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
				if err = s.ReserveBotMandateRun(worker, ref, run.ID); err != nil {
					t.Fatal(err)
				}
				tokens := int64(7)
				if scenario == "historical-exhausted" {
					tokens = 10
				}
				if err = s.ConsumeBotMandateQuota(worker, ref, run.ID, 1, tokens, 0); err != nil {
					t.Fatal(err)
				}
				lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "progress", ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				cp := progressCheckpoint(t, r, worker, lease, now)
				cp.State.Phase = store.BotProgressSuspended
				cp.State.Checkpoint.Usage.InputTokens = tokens
				cp.State.Checkpoint.Usage.ToolCalls = 1
				if _, err = r.CheckpointProgress(worker, cp); err != nil {
					t.Fatal(err)
				}
				if scenario == "continue" {
					var live []store.BotRunState
					coalesceCtx := store.WithBotRunStateNotifier(owner, func(_ context.Context, state store.BotRunState) { live = append(live, state) })
					for i := int64(1); i <= 3; i++ {
						due = due.Add(time.Second)
						now = due
						job = jobs.Get(owner, job.ID)
						job.NextRun = &due
						if err = jobs.Update(owner, job); err != nil {
							t.Fatal(err)
						}
						identity := fmt.Sprintf("%s/%d/%s/%s", run.BotID, receipt.Version, receipt.TriggerID, due.UTC().Format(time.RFC3339Nano))
						occurrence := store.BotScheduledOccurrence{BotID: run.BotID, Version: receipt.Version, TriggerID: receipt.TriggerID, JobID: job.ID, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
						coalesced, e := r.AdmitScheduledRun(coalesceCtx, occurrence)
						if e != nil {
							t.Fatal(e)
						}
						stored, e := r.GetRun(owner, run.BotID, run.ID)
						if e != nil {
							t.Fatal(e)
						}
						if len(live) != int(i) || stored.Progress == nil || stored.Progress.CoalescedOccurrences != i || !reflect.DeepEqual(stored.Progress, live[i-1].Progress) || stored.StateSequence != live[i-1].StateSequence {
							t.Fatal("live/persisted coalesced state mismatch")
						}
						if !reflect.DeepEqual(coalesced.Run.Progress, stored.Progress) || coalesced.Run.StateSequence != stored.StateSequence {
							t.Fatal("coalesced admission returned stale state")
						}
						if _, e = r.AdmitScheduledRun(coalesceCtx, occurrence); e != nil || len(live) != int(i) {
							t.Fatal("duplicate coalesced occurrence republished state", e)
						}
					}
				}
				b, err := r.Get(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = r.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "owner pause"})
				if err != nil {
					t.Fatal(err)
				}
				before, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if before.Run.Status != store.RunInterrupted || before.Progress.Phase != store.BotProgressSuspended {
					t.Fatalf("not paused: %+v", before)
				}
				grant, err := s.ReadBotMandate(owner, ref)
				if err != nil {
					t.Fatal(err)
				}
				parent, err := s.ReadBotMandate(owner, q.MandateRef)
				if err != nil {
					t.Fatal(err)
				}
				record, err := r.db.GetBot(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				usageBefore, err := progressQuotaUsage(record)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "policy-denied":
					owner = store.WithBotMandateAdmission(owner, func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
						return store.BotMandatePolicy{}, store.ErrBotForbidden
					})
				case "child-revoked":
					if err = s.RevokeBotMandate(owner, ref); err != nil {
						t.Fatal(err)
					}
				case "expired":
					now = grant.Mandate.ExpiresAt.Add(time.Second)
				case "parent-revoked":
					if err = s.RevokeBotMandate(owner, q.MandateRef); err != nil {
						t.Fatal(err)
					}
				case "uncertain":
					before.ProgressUncertain = true
					if err = saveProgressEnvelope(owner, r.db, before); err != nil {
						t.Fatal(err)
					}
				}
				// Refresh real owner evidence, not grant expiry, when advancing the clock.
				owner, err = execution.WithAuthenticationEvidence(owner, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: now.Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "owner-expired" {
					now = now.Add(2 * time.Hour)
				}
				request := store.BotContinueInteractiveRequest{BotID: run.BotID, RunID: run.ID, ExpectedRevision: b.Revision, Kind: store.BotContinuationProgress, CheckpointRevision: before.Progress.Checkpoint.Revision, IdempotencyKey: "resume"}
				_, err = r.ContinueInteractive(owner, request)
				continuationErr := err
				for _, original := range []*store.BotMandateRecord{grant, parent} {
					currentGrant, e := s.ReadBotMandate(owner, original.Mandate.Ref)
					if e != nil || !reflect.DeepEqual(currentGrant.Mandate, original.Mandate) {
						t.Fatalf("continuation renewed or changed authority: %v", e)
					}
				}
				record, err = r.db.GetBot(owner, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				usageAfter, err := progressQuotaUsage(record)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "continue" || scenario == "historical-exhausted" {
					usageBefore.Active[run.ID.String()] = *before.ProgressQuota
				}
				if !reflect.DeepEqual(usageAfter, usageBefore) {
					t.Fatalf("continuation changed cumulative ledger: before=%+v after=%+v", usageBefore, usageAfter)
				}
				if jobs.Get(owner, job.ID).Enabled {
					t.Fatal("continuation enabled recurrence")
				}
				err = continuationErr
				if scenario != "continue" && scenario != "historical-exhausted" {
					if err == nil {
						t.Fatal("unsafe continuation accepted")
					}
					held, e := r.db.ReadRun(owner, run.BotID, run.ID)
					if e != nil {
						t.Fatal(e)
					}
					if held.Run.Status != store.RunInterrupted || !reflect.DeepEqual(held.Run.Usage, before.Run.Usage) || !reflect.DeepEqual(held.ProgressQuota, before.ProgressQuota) {
						t.Fatal("hold reset state")
					}
					if scenario == "expired" || scenario == "parent-revoked" || scenario == "child-revoked" || scenario == "policy-denied" {
						if held.Progress.Phase != store.BotProgressWaitingAuthorization {
							t.Fatal("missing authority hold")
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.ContinueInteractive(owner, request); err != nil {
					t.Fatal("duplicate", err)
				}
				after, err := r.db.ReadRun(owner, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Run.Status != store.RunQueued || !reflect.DeepEqual(after.Run.Usage, before.Run.Usage) || after.ProgressQuota != nil || *after.Run.MandateRef != ref {
					t.Fatal("resume lost ledger or pin")
				}
				if _, err = s.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err == nil {
					t.Fatal("unscoped paused grant allowed")
				}
				if _, err = s.VerifyBotMandate(worker, ref, execution.BotMandatePurposeTask); err != nil {
					t.Fatal("exact resumed run denied", err)
				}
				if err = s.ReserveBotMandateRun(worker, ref, run.ID); err != nil {
					t.Fatal("preparation reserve", err)
				}
				if err = s.ConsumeBotMandateQuota(worker, ref, run.ID, 0, 4, 0); err != nil {
					t.Fatal("historical token ceiling enforced")
				}
				if jobs.Get(owner, job.ID).Enabled {
					t.Fatal("recurrence enabled")
				}
				current, _ := r.Get(owner, b.ID)
				if current.Status != store.BotPaused {
					t.Fatal("Bot unpaused")
				}
				_, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: uuid.NewString(), ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal("resumed claim", err)
				}
			})
		}
	}
}
