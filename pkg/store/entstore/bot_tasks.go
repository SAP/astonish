package entstore

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botTaskDB interface {
	readTask(context.Context, uuid.UUID, uuid.UUID) (*store.BotTask, error)
	readTasks(context.Context, uuid.UUID, uuid.UUID, int, bool) ([]store.BotTask, error)
	writeTask(context.Context, store.BotTask, int64) error
	readTaskPublication(context.Context, uuid.UUID, uuid.UUID) (*taskPublicationEnvelope, error)
	readTaskPublications(context.Context, uuid.UUID) ([]taskPublicationEnvelope, error)
	insertTaskPublication(context.Context, taskPublicationEnvelope) error
	setTaskPublication(context.Context, uuid.UUID, uuid.UUID, store.BotTaskPublicationState, store.BotTaskPublicationState, string) error
}
type taskPublicationEnvelope struct {
	Publication store.BotTaskPublication
	Mutation    store.BotTaskMutation
	Task        store.BotTask
}

// InstallBotTaskWorkspaceVerifier is trusted composition wiring, not an HTTP
// endpoint. The callback must resolve the opaque receipt against retained bytes.
func InstallBotTaskWorkspaceVerifier(s store.BotStore, verify func(context.Context, store.BotTaskPublication, string) error) error {
	switch v := s.(type) {
	case *personalBotStore:
		v.taskVerifier = verify
	case *teamBotStore:
		v.taskVerifier = verify
	default:
		return store.ErrBotUnsupported
	}
	return nil
}
func taskPage(p store.BotTaskPage) (int, uuid.UUID, error) {
	n := p.Limit
	if n == 0 {
		n = 50
	}
	if n < 1 || n > 100 {
		return 0, uuid.Nil, store.ErrBotInvalid
	}
	id := uuid.Nil
	var e error
	if p.After != "" {
		id, e = uuid.Parse(p.After)
	}
	if e != nil {
		return 0, id, store.ErrBotInvalid
	}
	return n, id, nil
}
func (r *botRepository) taskRead(c context.Context, b uuid.UUID) (botTaskDB, error) {
	if _, e := r.Get(c, b); e != nil {
		return nil, e
	}
	d, ok := r.db.(botTaskDB)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	return d, nil
}
func (r *botRepository) ListTasks(c context.Context, b store.BotID, p store.BotTaskPage) (*store.BotTaskList, error) {
	n, a, e := taskPage(p)
	if e != nil {
		return nil, e
	}
	d, e := r.taskRead(c, b)
	if e != nil {
		return nil, e
	}
	rows, e := d.readTasks(c, b, a, n+1, p.IncludeArchived)
	if e != nil {
		return nil, e
	}
	out := &store.BotTaskList{Tasks: rows}
	if len(rows) > n {
		out.Tasks = rows[:n]
		out.Next = rows[n-1].ID.String()
	}
	return out, nil
}
func (r *botRepository) GetTask(c context.Context, b store.BotID, id uuid.UUID) (*store.BotTask, error) {
	d, e := r.taskRead(c, b)
	if e != nil {
		return nil, e
	}
	return d.readTask(c, b, id)
}
func (r *botRepository) GetTaskRevision(c context.Context, b store.BotID, ref store.BotTaskRef) (*store.BotTaskRevision, error) {
	if ref.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	d, e := r.taskRead(c, b)
	if e != nil {
		return nil, e
	}
	rows, e := d.readTaskPublications(c, b)
	if e != nil {
		return nil, e
	}
	for _, v := range rows {
		p := v.Publication
		if p.TaskID == ref.TaskID && p.Candidate.Ref == ref && p.State == store.BotTaskPublicationCommitted {
			return p.Candidate.Clone(), nil
		}
	}
	return nil, store.ErrBotNotFound
}
func (r *botRepository) taskMutation(c context.Context, tx botDB, m store.BotTaskMutation) (*botStoredRecord, error) {
	rec, e := r.skillMutation(c, tx, store.BotSkillMutation{BotID: m.BotID, Lease: m.Lease, ActorID: m.ActorID, IdempotencyKey: m.IdempotencyKey}, m.Lease.RunID != uuid.Nil)
	if e != nil {
		return nil, e
	}
	if m.Lease.RunID != uuid.Nil {
		if e = tx.AdvanceLeaseSequence(c, m.Lease, rec.LastSequence, r.clock()); e != nil {
			return nil, e
		}
	}
	return rec, nil
}
func (r *botRepository) PrepareTaskPublication(c context.Context, q store.BotTaskPrepareRequest) (*store.BotTaskPublication, error) {
	if q.Mutation.AuthorityConstraintRef == "" || q.Task.ID == uuid.Nil || q.Task.BotID != q.Mutation.BotID || q.Candidate.BotID != q.Task.BotID || q.Candidate.Ref.TaskID != q.Task.ID || q.Task.Key != q.Task.ID.String() || q.Task.HomePath != "tasks/"+q.Task.Key || q.Candidate.Validate(q.Task.Status) != nil {
		return nil, store.ErrBotInvalid
	}
	var out *store.BotTaskPublication
	e := r.db.Transaction(c, func(tx botDB) error {
		rec, e := r.taskMutation(c, tx, q.Mutation)
		if e != nil {
			return e
		}
		d := tx.(botTaskDB)
		rows, e := d.readTaskPublications(c, q.Task.BotID)
		if e != nil {
			return e
		}
		for _, v := range rows {
			if v.Mutation.IdempotencyKey == q.Mutation.IdempotencyKey {
				candidate := *q.Candidate.Clone()
				candidate.Ref.Revision = v.Publication.Candidate.Ref.Revision
				candidate.Ref.Digest = candidate.ContentDigest()
				if !reflect.DeepEqual(v.Publication.Candidate, candidate) ||
					!reflect.DeepEqual(v.Publication.ExpectedHead, q.ExpectedHead) ||
					v.Task.ID != q.Task.ID || v.Task.Title != q.Task.Title || v.Task.Status != q.Task.Status ||
					v.Mutation.ExpectedRevision != q.Mutation.ExpectedRevision ||
					v.Mutation.AuthorityConstraintRef != q.Mutation.AuthorityConstraintRef ||
					v.Mutation.ActorID != q.Mutation.ActorID ||
					v.Mutation.Lease.BotID != q.Mutation.Lease.BotID || v.Mutation.Lease.RunID != q.Mutation.Lease.RunID ||
					v.Mutation.Lease.WorkerID != q.Mutation.Lease.WorkerID || v.Mutation.Lease.Fence != q.Mutation.Lease.Fence {
					return store.ErrBotConflict
				}
				p := v.Publication
				out = &p
				return nil
			}
		}
		if !reflect.DeepEqual(q.Candidate.Parent, q.ExpectedHead) {
			return store.ErrBotConflict
		}
		// Allocate from ALL immutable candidates, not just the committed head.
		// Keep the supplied digest validation above before server normalization.
		nextCandidate := int64(1)
		for _, v := range rows {
			if v.Publication.TaskID == q.Task.ID && v.Publication.Candidate.Ref.Revision >= nextCandidate {
				nextCandidate = v.Publication.Candidate.Ref.Revision + 1
			}
		}
		q.Candidate = *q.Candidate.Clone()
		q.Candidate.Ref.Revision = nextCandidate
		q.Candidate.Ref.Digest = q.Candidate.ContentDigest()
		old, e := d.readTask(c, q.Task.BotID, q.Task.ID)
		expected := int64(0)
		if errors.Is(e, store.ErrBotNotFound) {
			if q.ExpectedHead != nil || q.Mutation.ExpectedRevision != 0 || q.Candidate.Ref.Revision != 1 {
				return store.ErrBotConflict
			}
			q.Task.Revision = 1
			q.Task.Head = nil
			q.Task.CreatedAt = r.clock()
			q.Task.UpdatedAt = r.clock()
			if e = d.writeTask(c, q.Task, 0); e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else {
			expected = old.Revision
			if expected != q.Mutation.ExpectedRevision || !reflect.DeepEqual(old.Head, q.ExpectedHead) {
				return store.ErrBotConflict
			}
			if old.Status == store.BotTaskArchived {
				return store.ErrBotForbidden
			}
		}
		_ = expected
		if g := q.Candidate.Manifest.Goal; g != nil {
			v, e := tx.ReadGoal(c, rec.Bot.ID, g.GoalID)
			if e != nil || v.Revision != g.Revision {
				return store.ErrBotForbidden
			}
		}
		for _, s := range q.Candidate.Manifest.Skills {
			v, e := tx.(botSkillDB).readSkillRevision(c, rec.Bot.ID, s.SkillID, s.Revision)
			if e != nil || v.Digest != s.Digest {
				return store.ErrBotForbidden
			}
		}
		p := store.BotTaskPublication{ID: q.Candidate.ID, BotID: q.Task.BotID, TaskID: q.Task.ID, State: store.BotTaskPublicationPrepared, ExpectedHead: q.ExpectedHead.Clone(), Candidate: *q.Candidate.Clone(), AuthorityConstraintRef: q.Mutation.AuthorityConstraintRef}
		env := taskPublicationEnvelope{Publication: p, Mutation: q.Mutation, Task: q.Task}
		if e = d.insertTaskPublication(c, env); e != nil {
			return e
		}
		out = &p
		return nil
	})
	return out, e
}
func (r *botRepository) CommitTaskPublication(c context.Context, q store.BotTaskCommitRequest) (*store.BotTask, error) {
	if r.taskVerifier == nil {
		return nil, store.ErrBotUnsupported
	}
	d, e := r.taskRead(c, q.Mutation.BotID)
	if e != nil {
		return nil, e
	}
	env, e := d.readTaskPublication(c, q.Mutation.BotID, q.PublicationID)
	if e != nil {
		return nil, e
	}
	if q.WorkspaceReceiptRef == "" {
		return nil, store.ErrBotInvalid
	}
	if e = r.taskVerifier(c, env.Publication, q.WorkspaceReceiptRef); e != nil {
		return nil, e
	}
	var out *store.BotTask
	e = r.db.Transaction(c, func(tx botDB) error {
		if _, e := r.taskMutation(c, tx, q.Mutation); e != nil {
			return e
		}
		d := tx.(botTaskDB)
		env, e := d.readTaskPublication(c, q.Mutation.BotID, q.PublicationID)
		if e != nil {
			return e
		}
		p := env.Publication
		if p.AuthorityConstraintRef != q.Mutation.AuthorityConstraintRef || env.Mutation.ActorID != q.Mutation.ActorID || env.Mutation.Lease.BotID != q.Mutation.Lease.BotID || env.Mutation.Lease.WorkerID != q.Mutation.Lease.WorkerID || env.Mutation.Lease.RunID != q.Mutation.Lease.RunID || env.Mutation.Lease.Fence != q.Mutation.Lease.Fence {
			return store.ErrBotForbidden
		}
		old, e := d.readTask(c, p.BotID, p.TaskID)
		if e != nil {
			return e
		}
		if p.State == store.BotTaskPublicationCommitted {
			if p.WorkspaceReceiptRef != q.WorkspaceReceiptRef {
				return store.ErrBotConflict
			}
			out = old
			return nil
		}
		if p.State != store.BotTaskPublicationPrepared && p.State != store.BotTaskPublicationFilesReady {
			return store.ErrBotConflict
		}
		if !reflect.DeepEqual(old.Head, p.ExpectedHead) || old.Status == store.BotTaskArchived {
			return store.ErrBotConflict
		}
		if p.State == store.BotTaskPublicationPrepared {
			if e = d.setTaskPublication(c, p.BotID, p.ID, p.State, store.BotTaskPublicationFilesReady, q.WorkspaceReceiptRef); e != nil {
				return e
			}
		}
		next := *old
		next.Head = p.Candidate.Ref.Clone()
		next.Revision++
		next.Title = env.Task.Title
		next.Status = env.Task.Status
		next.UpdatedAt = r.clock()
		if e = d.writeTask(c, next, old.Revision); e != nil {
			return e
		}
		if e = d.setTaskPublication(c, p.BotID, p.ID, store.BotTaskPublicationFilesReady, store.BotTaskPublicationCommitted, q.WorkspaceReceiptRef); e != nil {
			return e
		}
		out = &next
		return nil
	})
	return out, e
}
func (r *botRepository) ChangeTaskLifecycle(c context.Context, q store.BotTaskLifecycleRequest) (*store.BotTask, error) {
	var out *store.BotTask
	e := r.db.Transaction(c, func(tx botDB) error {
		if _, e := r.taskMutation(c, tx, q.Mutation); e != nil {
			return e
		}
		d := tx.(botTaskDB)
		v, e := d.readTask(c, q.Mutation.BotID, q.TaskID)
		if e != nil {
			return e
		}
		if v.Revision != q.Mutation.ExpectedRevision || strings.TrimSpace(q.Reason) == "" {
			return store.ErrBotConflict
		}
		if e = store.ValidateBotTaskStatusTransition(v.Status, q.Status); e != nil {
			return e
		}
		if q.Status == store.BotTaskReady && v.Head == nil {
			return store.ErrBotInvalid
		}
		old := v.Revision
		v.Status = q.Status
		v.Revision++
		v.UpdatedAt = r.clock()
		e = d.writeTask(c, *v, old)
		out = v
		return e
	})
	return out, e
}
func (r *botRepository) ListPendingTaskPublications(c context.Context, b store.BotID, p store.BotTaskPage) ([]store.BotTaskPublication, error) {
	n, a, e := taskPage(p)
	if e != nil {
		return nil, e
	}
	d, e := r.taskRead(c, b)
	if e != nil {
		return nil, e
	}
	rows, e := d.readTaskPublications(c, b)
	if e != nil {
		return nil, e
	}
	out := []store.BotTaskPublication{}
	for _, v := range rows {
		p := v.Publication
		if p.ID.String() > a.String() && (p.State == store.BotTaskPublicationPrepared || p.State == store.BotTaskPublicationFilesReady) {
			out = append(out, p)
			if len(out) == n {
				break
			}
		}
	}
	return out, nil
}
func (r *botRepository) HoldTaskPublications(c context.Context, b store.BotID, reason string) error {
	return r.db.Transaction(c, func(tx botDB) error {
		rec, e := tx.GetBot(c, b)
		if e != nil {
			return e
		}
		if _, e = r.auth.authorize(c, rec.Bot.OwnerID, true); e != nil {
			return e
		}
		if strings.TrimSpace(reason) == "" {
			return store.ErrBotInvalid
		}
		if e = tx.(botSkillDB).lockSkills(c, b); e != nil {
			return e
		}
		d := tx.(botTaskDB)
		rows, e := d.readTaskPublications(c, b)
		if e != nil {
			return e
		}
		for _, v := range rows {
			p := v.Publication
			if p.State == store.BotTaskPublicationPrepared || p.State == store.BotTaskPublicationFilesReady {
				if e = d.setTaskPublication(c, b, p.ID, p.State, store.BotTaskPublicationHeld, p.WorkspaceReceiptRef); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func (r *botRepository) AbortTaskPublication(c context.Context, m store.BotTaskMutation, id uuid.UUID, reason string) error {
	return r.db.Transaction(c, func(tx botDB) error {
		if _, e := r.taskMutation(c, tx, m); e != nil {
			return e
		}
		if strings.TrimSpace(reason) == "" {
			return store.ErrBotInvalid
		}
		d := tx.(botTaskDB)
		v, e := d.readTaskPublication(c, m.BotID, id)
		if e != nil {
			return e
		}
		if e = store.ValidateBotTaskPublicationTransition(v.Publication.State, store.BotTaskPublicationAborted); e != nil {
			return e
		}
		return d.setTaskPublication(c, m.BotID, id, v.Publication.State, store.BotTaskPublicationAborted, "")
	})
}
