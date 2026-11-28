package entstore

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func taskTestCandidate(b uuid.UUID) store.BotTaskPrepareRequest {
	id := uuid.New()
	data := []byte("# Task\nInspect state\n")
	v := store.BotTaskRevision{SchemaVersion: store.BotTaskSchemaVersion, ID: uuid.New(), BotID: b, Ref: store.BotTaskRef{TaskID: id, Revision: 1}, Manifest: store.BotTaskManifest{SchemaVersion: store.BotTaskSchemaVersion, BotID: b, TaskID: id, Title: "Inspect state", Assignment: "Inspect state", Assets: []store.BotTaskAsset{{Path: "TASK.md", Kind: store.BotTaskAssetDefinition, Digest: skillSHA(string(data)), SizeBytes: int64(len(data))}}}, Bundle: map[string][]byte{"TASK.md": data}}
	v.Ref.Digest = v.ContentDigest()
	return store.BotTaskPrepareRequest{Task: store.BotTask{ID: id, BotID: b, Key: id.String(), HomePath: "tasks/" + id.String(), Title: "Inspect state", Status: store.BotTaskReady}, Candidate: v}
}
func TestBotTaskStorage(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			q := taskTestCandidate(run.BotID)
			q.Mutation = store.BotTaskMutation{BotID: run.BotID, Lease: lease, IdempotencyKey: "prepare-task", AuthorityConstraintRef: "owner-original-run"}
			before, e := r.Get(owner, run.BotID)
			if e != nil {
				t.Fatal(e)
			}
			p, e := r.PrepareTaskPublication(worker, q)
			if e != nil {
				t.Fatal(e)
			}
			again, e := r.PrepareTaskPublication(worker, q)
			if e != nil || again.ID != p.ID {
				t.Fatalf("prepare replay %v", e)
			}
			changed := q
			changed.Candidate = *q.Candidate.Clone()
			changed.Candidate.Manifest.Title = "different"
			changed.Candidate.Ref.Digest = changed.Candidate.ContentDigest()
			if _, e = r.PrepareTaskPublication(worker, changed); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("edited retry %v", e)
			}
			commit := store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: "opaque"}
			if _, e = r.CommitTaskPublication(worker, commit); !errors.Is(e, store.ErrBotUnsupported) {
				t.Fatalf("no trusted capability %v", e)
			}
			r.taskVerifier = func(_ context.Context, p store.BotTaskPublication, ref string) error {
				if ref != "trusted" {
					return store.ErrBotForbidden
				}
				return p.Candidate.Validate(store.BotTaskReady)
			}
			if _, e = r.CommitTaskPublication(worker, commit); !errors.Is(e, store.ErrBotForbidden) {
				t.Fatalf("forged receipt %v", e)
			}
			commit.WorkspaceReceiptRef = "trusted"
			task, e := r.CommitTaskPublication(worker, commit)
			if e != nil || task.Head == nil || *task.Head != q.Candidate.Ref {
				t.Fatalf("commit %+v %v", task, e)
			}
			after, e := r.Get(owner, run.BotID)
			if e != nil || after.Version != before.Version {
				t.Fatalf("Task changed BotVersion %v", e)
			}
			revision, e := r.GetTaskRevision(owner, run.BotID, q.Candidate.Ref)
			if e != nil || revision.Ref != q.Candidate.Ref {
				t.Fatal(e)
			}
			if _, e = r.GetTaskRevision(owner, uuid.New(), q.Candidate.Ref); e == nil {
				t.Fatal("foreign Bot read")
			}
			page, e := r.ListTasks(owner, run.BotID, store.BotTaskPage{Limit: 1})
			if e != nil || len(page.Tasks) != 1 {
				t.Fatal(e)
			}
			cancelled := commit
			cancelled.Mutation.Lease.Fence++
			if _, e = r.CommitTaskPublication(worker, cancelled); e == nil {
				t.Fatal("late fence accepted")
			}
			life := store.BotTaskLifecycleRequest{Mutation: store.BotTaskMutation{BotID: run.BotID, IdempotencyKey: "archive", ExpectedRevision: task.Revision}, TaskID: task.ID, Status: store.BotTaskArchived, Reason: "owner archive"}
			if _, e = r.ChangeTaskLifecycle(owner, life); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestBotTaskPublicationRecovery(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			q := taskTestCandidate(run.BotID)
			q.Mutation = store.BotTaskMutation{BotID: run.BotID, Lease: lease, IdempotencyKey: "prepare", AuthorityConstraintRef: "original-owner"}
			p, e := r.PrepareTaskPublication(worker, q)
			if e != nil {
				t.Fatal(e)
			}
			pending, e := r.ListPendingTaskPublications(owner, run.BotID, store.BotTaskPage{})
			if e != nil || len(pending) != 1 {
				t.Fatalf("pending %v", e)
			}
			if _, e = r.GetTaskRevision(owner, run.BotID, q.Candidate.Ref); !errors.Is(e, store.ErrBotNotFound) {
				t.Fatal("orphan certified")
			}
			if e = r.HoldTaskPublications(owner, run.BotID, "restore barrier"); e != nil {
				t.Fatal(e)
			}
			r.taskVerifier = func(context.Context, store.BotTaskPublication, string) error { return nil }
			if _, e = r.CommitTaskPublication(worker, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: "receipt"}); !errors.Is(e, store.ErrBotConflict) {
				t.Fatalf("held resumed %v", e)
			}
			q2 := taskTestCandidate(run.BotID)
			q2.Mutation = q.Mutation
			q2.Mutation.IdempotencyKey = "abort"
			p, e = r.PrepareTaskPublication(worker, q2)
			if e != nil {
				t.Fatal(e)
			}
			if e = r.AbortTaskPublication(worker, q2.Mutation, p.ID, "owner discard"); e != nil {
				t.Fatal(e)
			}
			pending, e = r.ListPendingTaskPublications(owner, run.BotID, store.BotTaskPage{})
			if e != nil || len(pending) != 0 {
				t.Fatalf("terminal pending %v", e)
			}
		})
	}
}
func TestBotTaskPage(t *testing.T) {
	for _, p := range []store.BotTaskPage{{Limit: -1}, {Limit: 101}, {After: "bad"}} {
		if _, _, e := taskPage(p); e == nil {
			t.Fatal("invalid page")
		}
	}
	if _, e := (&botRepository{}).PrepareTaskPublication(t.Context(), store.BotTaskPrepareRequest{}); e == nil {
		t.Fatal("invalid prepare")
	}
	if e := InstallBotTaskWorkspaceVerifier(nil, nil); !errors.Is(e, store.ErrBotUnsupported) {
		t.Fatal(e)
	}
}
