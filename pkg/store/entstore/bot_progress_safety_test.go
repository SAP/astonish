package entstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotProgressQuotaSuspension(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, ctx, _, _ := runtimeFixture(t, scope)
			principal, _ := execution.PrincipalFromContext(ctx)
			ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := mandateTestPolicy(c, ref)
				runtime := execution.NewBotProgressRuntimePolicy()
				p.RuntimePolicy = runtime
				return p, e
			}
			ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(2))
			ctx = store.WithBotMandateAdmission(ctx, policy)
			v := store.DefaultBotVersion()
			v.Charter = "Observe"
			v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			created, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Progress", OwnerID: uuid.MustParse(principal.Subject), Version: v, IdempotencyKey: "progress-quota"})
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: created.ID, Version: created.Version, IdempotencyKey: "progress-work", Instructions: "Observe carefully"})
			if err != nil {
				t.Fatal(err)
			}
			first := struct {
				Bot store.Bot
				Run store.BotRun
			}{Bot: *created, Run: admitted.Run}
			quota, err := NewBotMandateRuntime(bots, policy)
			if err != nil {
				t.Fatal(err)
			}
			ref := *first.Run.MandateRef
			if err = quota.ReserveBotMandateRun(ctx, ref, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = quota.ConsumeBotMandateQuota(ctx, ref, first.Run.ID, 1, 7, 0); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			repo.now = func() time.Time { return now }
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: first.Bot.ID, RunID: first.Run.ID, Version: first.Run.Version, ActorID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: first.Bot.ID, RunID: first.Run.ID, WorkerID: "progress", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			cp := progressCheckpoint(t, repo, worker, lease, now)
			cp.State.Phase = store.BotProgressSuspended
			if _, err = repo.CheckpointProgress(worker, cp); err != nil {
				t.Fatal(err)
			}
			root, _ := repo.db.GetBot(ctx, first.Bot.ID)
			before, err := progressQuotaUsage(root)
			if err != nil || len(before.Active) != 0 || before.DailyRuns != 1 || before.Tokens != 7 {
				t.Fatalf("suspension reset usage: %+v %v", before, err)
			}
			if err = quota.ReserveBotMandateRun(ctx, ref, first.Run.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("generic reserve bypass: %v", err)
			}
			now = now.Add(2 * time.Minute)
			lease, err = repo.ClaimRun(worker, store.BotClaimRequest{BotID: first.Bot.ID, RunID: first.Run.ID, WorkerID: "resumed", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			mutation := cp.Mutation
			mutation.Lease, mutation.ExpectedRevision = lease, 1
			continuation := store.BotProgressContinueRequest{Mutation: mutation, CheckpointRevision: 1, Reason: store.BotProgressReasonRecovery}
			if _, err = repo.ContinueProgress(worker, continuation); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("continued without occupancy: %v", err)
			}
			pq := quota.(store.BotMandateProgressQuotaStore)
			req := store.BotMandateProgressQuotaRequest{MandateRef: ref, Mutation: mutation}
			if err = pq.ReacquireBotMandateRun(worker, req); err != nil {
				t.Fatal(err)
			}
			if err = pq.ReacquireBotMandateRun(worker, req); err != nil {
				t.Fatal("idempotent reacquire", err)
			}
			if _, err = repo.ContinueProgress(worker, continuation); err != nil {
				t.Fatal(err)
			}
			root, _ = repo.db.GetBot(ctx, first.Bot.ID)
			after, _ := progressQuotaUsage(root)
			if after.DailyRuns != before.DailyRuns || after.Tokens != before.Tokens || after.Active[first.Run.ID.String()].Tokens != 7 {
				t.Fatalf("reacquire reset usage: %+v", after)
			}
			if err = quota.ConsumeBotMandateQuota(ctx, ref, first.Run.ID, 0, 4, 0); err == nil {
				t.Fatal("resumed per-run limit reset")
			}
			if err = pq.SuspendBotMandateRun(worker, req); err != nil {
				t.Fatal(err)
			}
			if err = quota.RevokeBotMandate(ctx, ref); err != nil {
				t.Fatal(err)
			}
			if err = pq.ReacquireBotMandateRun(worker, req); err == nil {
				t.Fatal("revocation bypass")
			}
		})
	}
}

func TestBotProgressRollbackConcurrentAndCommit(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, ctx, run, lease, now := progressFixture(t, scope)
			cp := progressCheckpoint(t, r, ctx, lease, *now)
			if _, err := r.CheckpointProgress(ctx, cp); err != nil {
				t.Fatal(err)
			}
			m := cp.Mutation
			m.ExpectedRevision = 1
			begin := store.BotProgressBeginDispatchRequest{Mutation: m, DispatchID: uuid.New(), Kind: store.BotProgressDispatchModel, OperationDigest: strings.Repeat("a", 64), ReservedUsage: store.BotUsage{InputTokens: 30}}
			original := r.db
			r.db = botProgressFailDB{original}
			if _, err := r.BeginProgressDispatch(ctx, begin); !errors.Is(err, errBotProgressFixture) {
				t.Fatal("injection", err)
			}
			r.db = original
			saved, _ := r.db.ReadRun(ctx, run.BotID, run.ID)
			if saved.ProgressPending != 0 || saved.Run.Usage.InputTokens != 0 {
				t.Fatal("rollback leaked charge")
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := r.BeginProgressDispatch(ctx, begin); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			saved, _ = r.db.ReadRun(ctx, run.BotID, run.ID)
			if saved.ProgressPending != 1 || saved.Run.Usage.InputTokens != 30 {
				t.Fatalf("duplicate reservation: %+v", saved)
			}
			settle := store.BotProgressSettleDispatchRequest{Mutation: m, DispatchID: begin.DispatchID, Outcome: store.BotProgressDispatchSettled, Usage: store.BotUsage{InputTokens: 35}}
			if _, err := r.SettleProgressDispatch(ctx, settle); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(10 * time.Second)
			lease, err := r.RenewLease(ctx, lease, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			report := finishedAgentWorkReport(t, r, ctx, lease, "Observed", store.AgentWorkAnswered)
			commit := store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}
			if err := r.CommitRun(ctx, commit); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("commit erased usage: %v", err)
			}
			commit.Usage = store.BotUsage{InputTokens: 35}
			if err := r.CommitRun(ctx, commit); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("commit erased heartbeat time: %v", err)
			}
			commit.Usage.RuntimeNanos = int64(10 * time.Second)
			*now = now.Add(5 * time.Second)
			if err := r.CommitRun(ctx, commit); err != nil {
				t.Fatal(err)
			}
			if err := r.CommitRun(ctx, commit); err != nil {
				t.Fatal("commit replay", err)
			}
			saved, err = r.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Run.Usage.RuntimeNanos != int64(15*time.Second) || saved.Run.Usage.InputTokens != 35 {
				t.Fatalf("final cumulative accounting: %+v %v", saved, err)
			}
		})
	}
}

func TestBotProgressActiveTimeAcrossRecovery(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			var reopen func()
			r, owner, ctx, run, lease, now := progressFixture(t, scope, &reopen)
			*now = now.Add(10 * time.Second)
			cp := progressCheckpoint(t, r, ctx, lease, *now)
			cp.State.Phase = store.BotProgressSuspended
			state, err := r.CheckpointProgress(ctx, cp)
			if err != nil || state.Checkpoint.Usage.RuntimeNanos != int64(10*time.Second) {
				t.Fatalf("initial active interval: %+v %v", state, err)
			}
			*now = now.Add(6 * time.Hour)
			lease, err = r.ClaimRun(ctx, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "resumed", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			cp.Mutation.Lease, cp.Mutation.ExpectedRevision = lease, 1
			resume := store.BotProgressContinueRequest{Mutation: cp.Mutation, CheckpointRevision: 1, Reason: store.BotProgressReasonRecovery}
			*now = now.Add(10 * time.Second)
			if _, err = r.ContinueProgress(ctx, resume); err != nil {
				t.Fatal(err)
			}
			saved, err := r.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Run.Usage.RuntimeNanos != int64(20*time.Second) {
				t.Fatalf("continuation erased active interval or charged queue: %+v %v", saved, err)
			}
			// An owned but unaccounted interval after a crash is conservatively
			// charged. Reopening a settled checkpoint must requeue the same run.
			reopen()
			*now = now.Add(2 * time.Minute)
			if err = r.RecoverRun(owner, lease); err != nil {
				t.Fatal(err)
			}
			saved, err = r.db.ReadRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Run.Status != store.RunQueued || saved.Progress.Phase != store.BotProgressSuspended || saved.Run.Usage.RuntimeNanos != int64(140*time.Second) || saved.Progress.Checkpoint.Usage.RuntimeNanos != saved.Run.Usage.RuntimeNanos {
				t.Fatalf("safe recovery lost accounting: %+v %v", saved, err)
			}
		})
	}
}

func TestBotProgressFencesAndAccounting(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, ctx, run, lease, now := progressFixture(t, scope)
			cp := progressCheckpoint(t, r, ctx, lease, *now)
			if _, err := r.CheckpointProgress(ctx, cp); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(10 * time.Second)
			renewed, err := r.RenewLease(ctx, lease, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := r.db.ReadRun(ctx, run.BotID, run.ID)
			if saved.Run.Usage.RuntimeNanos != int64(10*time.Second) || !saved.Run.RuntimeAccountingRecorded {
				t.Fatalf("heartbeat usage %+v", saved.Run.Usage)
			}
			mutation := cp.Mutation
			mutation.ExpectedRevision, mutation.Lease = 1, renewed
			begin := store.BotProgressBeginDispatchRequest{Mutation: mutation, DispatchID: uuid.New(), Kind: store.BotProgressDispatchTool, OperationDigest: strings.Repeat("c", 64), ReservedUsage: store.BotUsage{ToolCalls: 1}}
			stale := begin
			stale.Mutation.Lease.Fence++
			if _, err = r.BeginProgressDispatch(ctx, stale); err == nil {
				t.Fatal("stale fence accepted")
			}
			if _, err = r.BeginProgressDispatch(owner, begin); err == nil {
				t.Fatal("owner without worker identity accepted")
			}
			if _, err = r.BeginProgressDispatch(ctx, begin); err != nil {
				t.Fatal(err)
			}
			if _, err = r.SettleProgressDispatch(ctx, store.BotProgressSettleDispatchRequest{Mutation: mutation, DispatchID: begin.DispatchID, Outcome: store.BotProgressDispatchSettled, Usage: store.BotUsage{ToolCalls: 1}}); err != nil {
				t.Fatal(err)
			}
			saved, _ = r.db.ReadRun(ctx, run.BotID, run.ID)
			if !saved.ProgressUncertain {
				t.Fatal("tool success without trusted receipt was replay-safe")
			}
			next := progressCheckpoint(t, r, ctx, renewed, *now)
			next.State.Checkpoint.TurnID = cp.State.Checkpoint.TurnID
			if _, err = r.CheckpointProgress(ctx, next); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("uncertain checkpoint %v", err)
			}
			if err = r.CommitRun(ctx, store.BotRunCommit{Lease: renewed, Status: store.RunQueued}); err == nil {
				t.Fatal("generic running to queued allowed")
			}
		})
	}
	zero := int64(0)
	saved := &botStoredRun{Run: store.BotRun{Usage: store.BotUsage{CostMicros: &zero}}}
	progressCharge(saved, store.BotUsage{InputTokens: 1})
	if saved.Run.Usage.CostMicros != nil {
		t.Fatal("unknown dispatch pricing became known zero")
	}
}
