package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Admission resolves only a committed, scoped head and returns detached bytes.
func TestBotTaskAdmissionPin(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, _ := botRunFixture(t, scope)
			candidate := taskTestCandidate(run.BotID)
			candidate.Mutation = store.BotTaskMutation{BotID: run.BotID, Lease: lease, IdempotencyKey: "pin-publication", AuthorityConstraintRef: "owner-original-run"}
			publication, err := repo.PrepareTaskPublication(worker, candidate)
			if err != nil {
				t.Fatal(err)
			}
			pin := func(botID uuid.UUID, id *uuid.UUID) (*store.BotTaskRevision, error) {
				var result *store.BotTaskRevision
				err := repo.db.Transaction(owner, func(tx botDraftDB) error {
					var e error
					result, e = pinTaskHead(owner, tx, botID, id)
					return e
				})
				return result, err
			}
			if result, err := pin(run.BotID, nil); err != nil || result != nil {
				t.Fatalf("legacy pin: %+v %v", result, err)
			}
			zero := uuid.Nil
			if _, err := pin(run.BotID, &zero); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("nil UUID: %v", err)
			}
			if _, err := pin(run.BotID, &candidate.Task.ID); err == nil {
				t.Fatal("uncommitted publication admitted")
			}
			repo.taskVerifier = func(context.Context, store.BotTaskPublication, string) error { return nil }
			task, err := repo.CommitTaskPublication(worker, store.BotTaskCommitRequest{Mutation: candidate.Mutation, PublicationID: publication.ID, WorkspaceReceiptRef: "trusted-test"})
			if err != nil {
				t.Fatal(err)
			}
			pinned, err := pin(run.BotID, &task.ID)
			if err != nil || pinned.Ref != *task.Head {
				t.Fatalf("committed head: %+v %v", pinned, err)
			}
			pinned.Bundle["TASK.md"][0] = '!'
			again, err := pin(run.BotID, &task.ID)
			if err != nil || string(again.Bundle["TASK.md"]) != string(candidate.Candidate.Bundle["TASK.md"]) {
				t.Fatalf("pin aliases registry bytes: %+v %v", again, err)
			}
			if _, err := pin(uuid.New(), &task.ID); err == nil {
				t.Fatal("foreign Bot task admitted")
			}
			if _, err := repo.ChangeTaskLifecycle(owner, store.BotTaskLifecycleRequest{Mutation: store.BotTaskMutation{BotID: run.BotID, ExpectedRevision: task.Revision, IdempotencyKey: "archive-pin"}, TaskID: task.ID, Status: store.BotTaskArchived, Reason: "owner archive"}); err != nil {
				t.Fatal(err)
			}
			if _, err := pin(run.BotID, &task.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("archived task admitted: %v", err)
			}
		})
	}
}
