package entstore

import (
	"context"
	"encoding/json"

	ent "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/bottask"
	"github.com/SAP/astonish/ent/team/bottaskrevision"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotTaskStore = (*teamBotStore)(nil)
var _ store.BotTaskRecoveryStore = (*teamBotStore)(nil)

func (s *teamBotStore) taskRepository() *botRepository {
	r := s.repository()
	r.taskVerifier = s.taskVerifier
	return r
}
func (s *teamBotStore) ListTasks(c context.Context, b store.BotID, p store.BotTaskPage) (*store.BotTaskList, error) {
	return s.taskRepository().ListTasks(c, b, p)
}
func (s *teamBotStore) GetTask(c context.Context, b store.BotID, id uuid.UUID) (*store.BotTask, error) {
	return s.taskRepository().GetTask(c, b, id)
}
func (s *teamBotStore) GetTaskRevision(c context.Context, b store.BotID, p store.BotTaskRef) (*store.BotTaskRevision, error) {
	return s.taskRepository().GetTaskRevision(c, b, p)
}
func (s *teamBotStore) PrepareTaskPublication(c context.Context, q store.BotTaskPrepareRequest) (*store.BotTaskPublication, error) {
	return s.taskRepository().PrepareTaskPublication(c, q)
}
func (s *teamBotStore) CommitTaskPublication(c context.Context, q store.BotTaskCommitRequest) (*store.BotTask, error) {
	return s.taskRepository().CommitTaskPublication(c, q)
}
func (s *teamBotStore) ChangeTaskLifecycle(c context.Context, q store.BotTaskLifecycleRequest) (*store.BotTask, error) {
	return s.taskRepository().ChangeTaskLifecycle(c, q)
}
func (s *teamBotStore) ListPendingTaskPublications(c context.Context, b store.BotID, p store.BotTaskPage) ([]store.BotTaskPublication, error) {
	return s.taskRepository().ListPendingTaskPublications(c, b, p)
}
func (s *teamBotStore) HoldTaskPublications(c context.Context, b store.BotID, p string) error {
	return s.taskRepository().HoldTaskPublications(c, b, p)
}
func (s *teamBotStore) AbortTaskPublication(c context.Context, m store.BotTaskMutation, id uuid.UUID, p string) error {
	return s.taskRepository().AbortTaskPublication(c, m, id, p)
}
func (d teamBotDB) readTask(c context.Context, b, id uuid.UUID) (*store.BotTask, error) {
	v, e := d.client.BotTask.Query().Where(bottask.BotIDEQ(b), bottask.IDEQ(id)).Only(c)
	if e != nil {
		return nil, teamBotError(e)
	}
	r := &store.BotTask{ID: v.ID, BotID: v.BotID, Key: v.Key, Title: v.Title, Revision: v.Revision, Status: store.BotTaskStatus(v.Status), HomePath: v.HomePath, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	if v.HeadRevision > 0 {
		r.Head = &store.BotTaskRef{TaskID: id, Revision: v.HeadRevision, Digest: v.HeadDigest}
	}
	return r, nil
}
func (d teamBotDB) readTasks(c context.Context, b, a uuid.UUID, n int, archived bool) ([]store.BotTask, error) {
	q := d.client.BotTask.Query().Where(bottask.BotIDEQ(b), bottask.IDGT(a)).Order(ent.Asc(bottask.FieldID)).Limit(n)
	if !archived {
		q.Where(bottask.StatusNEQ(bottask.StatusArchived))
	}
	rows, e := q.All(c)
	if e != nil {
		return nil, e
	}
	out := []store.BotTask{}
	for _, v := range rows {
		r, e := d.readTask(c, b, v.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, *r)
	}
	return out, nil
}
func (d teamBotDB) writeTask(c context.Context, v store.BotTask, expected int64) error {
	head := int64(0)
	digest := ""
	if v.Head != nil {
		head = v.Head.Revision
		digest = v.Head.Digest
	}
	if expected == 0 {
		_, e := d.client.BotTask.Create().SetID(v.ID).SetBotID(v.BotID).SetKey(v.Key).SetHomePath(v.HomePath).SetTitle(v.Title).SetRevision(v.Revision).SetStatus(bottask.Status(v.Status)).SetHeadRevision(head).SetHeadDigest(digest).SetCreatedAt(v.CreatedAt).SetUpdatedAt(v.UpdatedAt).Save(c)
		return teamBotError(e)
	}
	n, e := d.client.BotTask.Update().Where(bottask.BotIDEQ(v.BotID), bottask.IDEQ(v.ID), bottask.RevisionEQ(expected)).SetRevision(v.Revision).SetTitle(v.Title).SetStatus(bottask.Status(v.Status)).SetHeadRevision(head).SetHeadDigest(digest).SetUpdatedAt(v.UpdatedAt).Save(c)
	if e != nil {
		return teamBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) readTaskPublication(c context.Context, b, id uuid.UUID) (*taskPublicationEnvelope, error) {
	row, e := d.client.BotTaskRevision.Query().Where(bottaskrevision.BotIDEQ(b), bottaskrevision.IDEQ(id)).Only(c)
	if e != nil {
		return nil, teamBotError(e)
	}
	var v taskPublicationEnvelope
	if e = botDecode(row.Data, &v); e != nil {
		return nil, e
	}
	p := &v.Publication
	if p.ID != row.ID || p.BotID != b || p.TaskID != row.TaskID || p.Candidate.Ref.Digest != row.Digest || p.Candidate.Ref.Revision != row.Revision || p.Candidate.Validate(v.Task.Status) != nil {
		return nil, store.ErrBotInvalid
	}
	p.State = store.BotTaskPublicationState(row.PublicationState)
	p.WorkspaceReceiptRef = row.WorkspaceReceiptRef
	return &v, nil
}
func (d teamBotDB) readTaskPublications(c context.Context, b uuid.UUID) ([]taskPublicationEnvelope, error) {
	rows, e := d.client.BotTaskRevision.Query().Where(bottaskrevision.BotIDEQ(b)).Order(ent.Asc(bottaskrevision.FieldID)).Limit(1001).All(c)
	if e != nil {
		return nil, e
	}
	if len(rows) > 1000 {
		return nil, store.ErrBotInvalid
	}
	out := []taskPublicationEnvelope{}
	for _, row := range rows {
		v, e := d.readTaskPublication(c, b, row.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, *v)
	}
	return out, nil
}
func (d teamBotDB) insertTaskPublication(c context.Context, v taskPublicationEnvelope) error {
	data, e := botJSONMap(v)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	p := v.Publication
	parent := int64(0)
	digest := ""
	if p.ExpectedHead != nil {
		parent = p.ExpectedHead.Revision
		digest = p.ExpectedHead.Digest
	}
	_, e = d.client.BotTaskRevision.Create().SetID(p.ID).SetBotID(p.BotID).SetTaskID(p.TaskID).SetRevision(p.Candidate.Ref.Revision).SetDigest(p.Candidate.Ref.Digest).SetSizeBytes(int64(len(raw))).SetParentRevision(parent).SetParentDigest(digest).SetPublicationState(bottaskrevision.PublicationState(p.State)).SetAuthorityConstraintRef(p.AuthorityConstraintRef).SetData(data).Save(c)
	return teamBotError(e)
}
func (d teamBotDB) setTaskPublication(c context.Context, b, id uuid.UUID, from, to store.BotTaskPublicationState, receipt string) error {
	n, e := d.client.BotTaskRevision.Update().Where(bottaskrevision.BotIDEQ(b), bottaskrevision.IDEQ(id), bottaskrevision.PublicationStateEQ(bottaskrevision.PublicationState(from))).SetPublicationState(bottaskrevision.PublicationState(to)).SetWorkspaceReceiptRef(receipt).Save(c)
	if e != nil {
		return teamBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
