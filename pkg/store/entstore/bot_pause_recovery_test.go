package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPausedConversationTeardownBarrier(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, run, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "old", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: []byte(`{}`)}
			digest, _ := action.Digest()
			approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause interrupted writer"})
			if err != nil {
				t.Fatal(err)
			}
			beforeApprovals, err := repo.ListApprovals(ctx, b.ID, store.BotPage{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			var pausedApprovalStatus store.ApprovalStatus
			for _, a := range beforeApprovals {
				if a.ID == approval.ID {
					pausedApprovalStatus = a.Status
				}
			}
			next, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentConversation, Trigger: store.TriggerManual, Instructions: "Reply only", IdempotencyKey: "paused-next"})
			if err != nil {
				t.Fatal(err)
			}
			rec, err := repo.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: b.ID, RunID: run.ID, RunFence: lease.Fence, Fence: rec.Fence, ExpectedRevision: b.Revision, Home: b.Home}
			verified := 0
			service := cleanupRuntimeContext(t, func(_ context.Context, got store.BotCleanupRequest) error {
				if got != q {
					return store.ErrBotForbidden
				}
				verified++
				return nil
			})
			if err = repo.ConfirmCleanup(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("owner forged receipt", err)
			}
			for _, change := range []func(*store.BotCleanupRequest){
				func(w *store.BotCleanupRequest) { w.RunID = next.ID },
				func(w *store.BotCleanupRequest) { w.RunFence++ },
				func(w *store.BotCleanupRequest) { w.Fence++ },
				func(w *store.BotCleanupRequest) { w.ExpectedRevision++ },
				func(w *store.BotCleanupRequest) { w.Home.ID = uuid.New() },
			} {
				wrong := q
				change(&wrong)
				if err = repo.ConfirmCleanup(service, wrong); err == nil {
					t.Fatal("wrong binding accepted")
				}
			}
			if verified != 0 {
				t.Fatal("wrong binding reached physical verifier")
			}
			if err = repo.ConfirmCleanup(service, q); err != nil {
				t.Fatal("queued conversation blocked teardown", err)
			}
			if verified != 1 {
				t.Fatal("physical receipt missing")
			}
			nextCtx := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: next.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			if _, err = repo.ClaimRun(nextCtx, store.BotClaimRequest{BotID: b.ID, RunID: next.ID, WorkerID: "replacement", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
				t.Fatal("conversation blocked", err)
			}
			if err = repo.ConfirmCleanup(service, q); err == nil {
				t.Fatal("old receipt stole current writer")
			}
			approvals, err := repo.ListApprovals(ctx, b.ID, store.BotPage{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, a := range approvals {
				if a.ID == approval.ID {
					found = true
					if a.Status != pausedApprovalStatus {
						t.Fatal("approval revived")
					}
				}
			}
			if !found {
				t.Fatal("lost approval audit")
			}
		})
	}
}
