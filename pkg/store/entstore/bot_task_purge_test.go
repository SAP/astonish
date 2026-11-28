package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBotTaskPurge(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			r.taskVerifier = func(context.Context, store.BotTaskPublication, string) error { return nil }
			q := taskTestCandidate(run.BotID)
			q.Mutation = store.BotTaskMutation{BotID: run.BotID, Lease: lease, IdempotencyKey: "purge-task", AuthorityConstraintRef: "owner"}
			p, err := r.PrepareTaskPublication(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.CommitTaskPublication(worker, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: p.ID.String()}); err != nil {
				t.Fatal(err)
			}
			soft, teardown := cleanupDeleteFixture(t, r, owner, run, lease)
			deleted, err := r.BeginPermanentDelete(owner, store.BotPermanentDeleteRequest{BotID: soft.ID, ActorID: soft.OwnerID, ExpectedRevision: soft.Revision, Confirm: true})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.PurgeDeleted(owner, deleted.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal("purge before physical cleanup", err)
			}
			if _, err = r.db.(botTaskDB).readTask(owner, deleted.ID, q.Task.ID); err != nil {
				t.Fatal("unverified purge lost Task", err)
			}
			rec, err := r.db.GetBot(owner, deleted.ID)
			if err != nil {
				t.Fatal(err)
			}
			teardown.ExpectedRevision = deleted.Revision
			teardown.Fence = rec.Fence
			runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
			if err = r.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatal(err)
			}
			claim, err := r.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.ConfirmCleanup(runtime, claim.Request); err != nil {
				t.Fatal(err)
			}
			if err = r.PurgeDeleted(owner, deleted.ID, deleted.Revision); err != nil {
				t.Fatal(err)
			}
			d := r.db.(botTaskDB)
			if _, err = d.readTask(owner, deleted.ID, q.Task.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatal("Task survived verified purge", err)
			}
			if _, err = d.readTaskPublication(owner, deleted.ID, p.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatal("revision survived verified purge", err)
			}
		})
	}
}
