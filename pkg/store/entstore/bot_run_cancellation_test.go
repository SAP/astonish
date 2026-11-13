package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotExactRunCancellationWorkerBarrier(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, scoped, ctx, a, home := runtimeFixture(t, scope)
			b, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: a.BotID, Version: a.Version, Trigger: store.TriggerManual, IdempotencyKey: "next", Instructions: "next"})
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan store.BotLease, 1)
			adapter := &runtimeTestAdapter{start: func(c context.Context, q botdomain.RunRequest, _ botdomain.EventSink) (botdomain.RunResult, error) {
				started <- q.Run.Lease
				<-c.Done()
				return botdomain.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "late success must not commit"}}, nil
			}}
			supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := supervisor.Execute(ctx, a.BotID, a.ID); done <- e }()
			var lease store.BotLease
			select {
			case lease = <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not start")
			}
			before, _ := repo.db.GetBot(ctx, a.BotID)
			cancelled, err := scoped.(store.BotRunCancellationStore).CancelRun(ctx, a.BotID, a.ID)
			if err != nil || cancelled.Status != store.RunInterrupted {
				t.Fatalf("cancel %+v %v", cancelled, err)
			}
			after, _ := repo.db.GetBot(ctx, a.BotID)
			if after.Bot.Status != store.BotActive || after.Bot.Revision != before.Bot.Revision || after.Fence != lease.Fence+1 || after.ActiveRunID == nil || *after.ActiveRunID != a.ID {
				t.Fatalf("lost exact barrier %+v", after)
			}
			if _, err = repo.CancelRun(ctx, a.BotID, a.ID); err != nil {
				t.Fatal(err)
			}
			again, _ := repo.db.GetBot(ctx, a.BotID)
			if again.LastSequence != after.LastSequence || again.Fence != after.Fence {
				t.Fatal("duplicate changed fence or sequence")
			}
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("late success committed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("fenced worker did not cancel")
			}
			if adapter.cancels.Load() != 1 {
				t.Fatal("runtime cancellation not invoked")
			}
			workerB := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.BotID, RunID: b.ID, Version: b.Version, ActorID: after.Bot.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			claim := store.BotClaimRequest{BotID: b.BotID, RunID: b.ID, WorkerID: "next", ExpiresAt: time.Now().Add(time.Minute)}
			if _, err = repo.ClaimRun(workerB, claim); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("B started before receipt: %v", err)
			}
			q := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: a.BotID, RunID: a.ID, RunFence: lease.Fence, Fence: after.Fence, ExpectedRevision: after.Bot.Revision, Home: home.home}
			refused := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return errors.New("destruction still pending") })
			if err = repo.ConfirmCleanup(refused, q); err == nil {
				t.Fatal("unconfirmed destruction accepted")
			}
			if _, err = repo.ClaimRun(workerB, claim); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("failed verification released barrier")
			}
			verified := cleanupRuntimeContext(t, func(_ context.Context, got store.BotCleanupRequest) error {
				if got != q {
					return store.ErrBotForbidden
				}
				return nil
			})
			wrong := q
			wrong.RunID = b.ID
			if err = repo.ConfirmCleanup(verified, wrong); err == nil {
				t.Fatal("wrong run receipt accepted")
			}
			if err = repo.ConfirmCleanup(verified, q); err != nil {
				t.Fatal(err)
			}
			if err = repo.ConfirmCleanup(verified, q); err != nil {
				t.Fatal("duplicate receipt", err)
			}
			if _, err = repo.ClaimRun(workerB, claim); err != nil {
				t.Fatalf("B blocked after receipt: %v", err)
			}
			final, _ := repo.Get(ctx, a.BotID)
			if final.Status != store.BotActive {
				t.Fatal("Bot stopped")
			}
			if _, err = repo.CancelRun(ctx, uuid.New(), a.ID); err == nil {
				t.Fatal("other bot accepted")
			}
			if _, err = repo.CancelRun(ctx, a.BotID, uuid.New()); err == nil {
				t.Fatal("other run accepted")
			}
			if _, err = repo.CancelRun(context.Background(), a.BotID, b.ID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("anonymous stop", err)
			}
		})
	}
}

func TestBotExactRunCancellationQueuedAndWaiting(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, a, lease, _ := botRunFixture(t, scope)
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: []byte(`{}`)}
			digest, _ := action.Digest()
			approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: a.BotID, RunID: a.ID, Version: a.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			work, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: []byte(`{"step":1}`), Report: store.BotReport{Summary: "waiting", Work: store.BuildBotWorkProjection(work), NeedsApproval: []store.BotID{approval.ID}}}); err != nil {
				t.Fatal(err)
			}
			b, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: a.BotID, Version: a.Version, Trigger: store.TriggerManual, IdempotencyKey: "queued", Instructions: "queued"})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := repo.db.GetBot(ctx, a.BotID)
			for _, run := range []*store.BotRun{a, b} {
				got, err := repo.CancelRun(ctx, a.BotID, run.ID)
				if err != nil || got.Status != store.RunCancelled {
					t.Fatalf("cancel %+v %v", got, err)
				}
			}
			after, _ := repo.db.GetBot(ctx, a.BotID)
			if after.Fence != before.Fence || after.Bot.Status != store.BotActive {
				t.Fatal("inactive cancellation fenced Bot")
			}
			approvals, err := repo.ListApprovals(ctx, a.BotID, store.BotPage{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range approvals {
				if got.ID == approval.ID && got.Status != store.ApprovalRevoked {
					t.Fatal("approval remains live")
				}
			}
		})
	}
}
