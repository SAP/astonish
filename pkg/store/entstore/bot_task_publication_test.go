package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotTaskPublicationFreshAfterTerminal(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, terminal := range []store.BotTaskPublicationState{store.BotTaskPublicationHeld, store.BotTaskPublicationAborted} {
			for _, first := range []bool{true, false} {
				name := string(scope) + "/" + string(terminal)
				if first {
					name += "/first"
				} else {
					name += "/replacement"
				}
				t.Run(name, func(t *testing.T) {
					r, owner, worker, run, lease, _ := botRunFixture(t, scope)
					r.taskVerifier = func(context.Context, store.BotTaskPublication, string) error { return nil }
					q := taskTestCandidate(run.BotID)
					q.Mutation = store.BotTaskMutation{BotID: run.BotID, Lease: lease, IdempotencyKey: "initial", AuthorityConstraintRef: "original-owner"}
					commit := func(q store.BotTaskPrepareRequest, p *store.BotTaskPublication) *store.BotTask {
						t.Helper()
						task, err := r.CommitTaskPublication(worker, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: p.ID, WorkspaceReceiptRef: "trusted"})
						if err != nil {
							t.Fatal(err)
						}
						return task
					}
					if !first {
						p, err := r.PrepareTaskPublication(worker, q)
						if err != nil {
							t.Fatal(err)
						}
						task := commit(q, p)
						q.ExpectedHead = task.Head.Clone()
						q.Mutation.ExpectedRevision = task.Revision
						q.Candidate.Parent = task.Head.Clone()
						q.Candidate.Ref.Revision++
						q.Candidate.ID = uuid.New()
						q.Candidate.Ref.Digest = q.Candidate.ContentDigest()
						q.Mutation.IdempotencyKey = "abandoned"
					}
					abandoned, err := r.PrepareTaskPublication(worker, q)
					if err != nil {
						t.Fatal(err)
					}
					if terminal == store.BotTaskPublicationHeld {
						err = r.HoldTaskPublications(owner, run.BotID, "restore")
					} else {
						err = r.AbortTaskPublication(worker, q.Mutation, abandoned.ID, "discard")
					}
					if err != nil {
						t.Fatal(err)
					}
					current, err := r.GetTask(owner, run.BotID, q.Task.ID)
					if err != nil {
						t.Fatal(err)
					}
					fresh := q
					fresh.Candidate = *q.Candidate.Clone()
					fresh.Candidate.ID = uuid.New()
					fresh.Candidate.Manifest.Assignment = "Fresh authorized procedure"
					fresh.Candidate.Ref.Digest = fresh.Candidate.ContentDigest()
					fresh.Mutation.IdempotencyKey = "fresh"
					fresh.Mutation.ExpectedRevision = current.Revision
					p, err := r.PrepareTaskPublication(worker, fresh)
					if err != nil {
						t.Fatal(err)
					}
					if p.Candidate.Ref.Revision != abandoned.Candidate.Ref.Revision+1 || !reflect.DeepEqual(p.ExpectedHead, current.Head) || !reflect.DeepEqual(p.Candidate.Parent, current.Head) {
						t.Fatalf("allocation lost parent: %+v", p)
					}
					replay, err := r.PrepareTaskPublication(worker, fresh)
					if err != nil || replay.Candidate.Ref != p.Candidate.Ref {
						t.Fatalf("normalized replay: %v", err)
					}
					for _, mutate := range []func(*store.BotTaskPrepareRequest){
						func(v *store.BotTaskPrepareRequest) {
							v.ExpectedHead = (&store.BotTaskRef{TaskID: q.Task.ID, Revision: 99, Digest: p.Candidate.Ref.Digest}).Clone()
						},
						func(v *store.BotTaskPrepareRequest) { v.Task.Title = "changed registry title" },
						func(v *store.BotTaskPrepareRequest) { v.Task.Status = store.BotTaskDraft },
						func(v *store.BotTaskPrepareRequest) { v.Mutation.ExpectedRevision++ },
					} {
						edited := fresh
						mutate(&edited)
						if _, err := r.PrepareTaskPublication(worker, edited); !errors.Is(err, store.ErrBotConflict) {
							t.Fatalf("edited normalized replay accepted: %v", err)
						}
					}
					task := commit(fresh, p)
					if task.Head == nil || *task.Head != p.Candidate.Ref {
						t.Fatal("head not normalized")
					}
					got, err := r.GetTaskRevision(owner, run.BotID, *task.Head)
					if err != nil || got.Ref != p.Candidate.Ref {
						t.Fatalf("head unresolvable: %v", err)
					}
					d, err := r.taskRead(owner, run.BotID)
					if err != nil {
						t.Fatal(err)
					}
					evidence, err := d.readTaskPublication(owner, run.BotID, abandoned.ID)
					if err != nil || evidence.Publication.State != terminal || !reflect.DeepEqual(evidence.Publication.Candidate, abandoned.Candidate) {
						t.Fatalf("terminal evidence lost: %v", err)
					}
					if _, err = r.CommitTaskPublication(worker, store.BotTaskCommitRequest{Mutation: q.Mutation, PublicationID: abandoned.ID, WorkspaceReceiptRef: "trusted"}); !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("terminal replay: %v", err)
					}
				})
			}
		}
	}
}

func TestBotTaskWorkspaceCommitFence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			m := store.BotTaskMutation{BotID: run.BotID, Lease: lease, IdempotencyKey: "write"}
			commits := 0
			commit := func() error { commits++; return nil }
			if err := r.withTaskWorkspaceCommit(worker, m, commit); err != nil || commits != 1 {
				t.Fatalf("live commit: %v", err)
			}
			if err := r.withTaskWorkspaceCommitGuard(worker, m, func(check func(context.Context) error) error {
				if err := check(worker); err != nil {
					return err
				}
				live, stop := context.WithCancel(worker)
				stop()
				if !errors.Is(check(live), context.Canceled) {
					t.Fatal("final live cancellation bypassed")
				}
				return nil
			}); err != nil {
				t.Fatalf("transaction-local final check: %v", err)
			}
			omitted := m
			omitted.Lease = store.BotLease{}
			if err := r.withTaskWorkspaceCommit(worker, omitted, commit); !errors.Is(err, store.ErrBotForbidden) || commits != 1 {
				t.Fatalf("omitted lease: %v", err)
			}
			wrong := m
			wrong.Lease.WorkerID = "foreign"
			if err := r.withTaskWorkspaceCommit(worker, wrong, commit); err == nil || commits != 1 {
				t.Fatalf("foreign worker: %v", err)
			}
			cancelled, cancel := context.WithCancel(worker)
			cancel()
			if err := r.withTaskWorkspaceCommit(cancelled, m, commit); err == nil || commits != 1 {
				t.Fatalf("context cancellation: %v", err)
			}
			if _, err := r.CancelRun(owner, run.BotID, run.ID); err != nil {
				t.Fatal(err)
			}
			if err := r.withTaskWorkspaceCommit(worker, m, commit); err == nil || commits != 1 {
				t.Fatalf("persisted cancellation: %v", err)
			}
		})
	}
}
