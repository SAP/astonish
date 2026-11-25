package entstore

import (
	"context"
	"encoding/json"
	"reflect"
	"time"
	"unicode/utf8"

	ent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/personal/bottask"
	"github.com/SAP/astonish/ent/personal/bottaskrevision"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botTaskPublicationRecord contains coordination fields only. Candidate is a
// typed disk-reference envelope, never a hydrated revision or replay bundle.
// Keep this separate from taskPublicationEnvelope, which is an in-memory API.
type botTaskPublicationRecord struct {
	Format                 int                   `json:"format"`
	ID                     uuid.UUID             `json:"id"`
	BotID                  store.BotID           `json:"bot_id"`
	TaskID                 uuid.UUID             `json:"task_id"`
	ExpectedHead           *store.BotTaskRef     `json:"expected_head"`
	Candidate              map[string]any        `json:"candidate"`
	AuthorityConstraintRef string                `json:"authority_constraint_ref"`
	Mutation               store.BotTaskMutation `json:"mutation"`
	TaskKey                string                `json:"task_key"`
	TaskTitle              string                `json:"task_title"`
	TaskStatus             store.BotTaskStatus   `json:"task_status"`
	TaskRevision           int64                 `json:"task_revision"`
	TaskHead               *store.BotTaskRef     `json:"task_head"`
	TaskHomePath           string                `json:"task_home_path"`
	TaskCreatedAt          time.Time             `json:"task_created_at"`
	TaskUpdatedAt          time.Time             `json:"task_updated_at"`
}

func encodeTaskPublication(ctx context.Context, codec *botContentCodec, v taskPublicationEnvelope) (map[string]any, error) {
	p, task := v.Publication, v.Task
	if p.ID != p.Candidate.ID || p.BotID != p.Candidate.BotID || p.TaskID != p.Candidate.Ref.TaskID ||
		task.ID != p.TaskID || task.BotID != p.BotID || v.Mutation.BotID != p.BotID ||
		len(task.Title) > 256 || !utf8.ValidString(task.Title) ||
		!reflect.DeepEqual(p.ExpectedHead, p.Candidate.Parent) || p.AuthorityConstraintRef != v.Mutation.AuthorityConstraintRef {
		return nil, store.ErrBotInvalid
	}
	candidate, err := codec.encodeTaskRevision(ctx, p.Candidate)
	if err != nil {
		return nil, err
	}
	record := botTaskPublicationRecord{Format: botDiskRecordFormat, ID: p.ID, BotID: p.BotID, TaskID: p.TaskID,
		ExpectedHead: p.ExpectedHead, Candidate: candidate, AuthorityConstraintRef: p.AuthorityConstraintRef,
		Mutation: v.Mutation, TaskKey: task.Key, TaskTitle: task.Title, TaskStatus: task.Status,
		TaskRevision: task.Revision, TaskHead: task.Head, TaskHomePath: task.HomePath,
		TaskCreatedAt: task.CreatedAt, TaskUpdatedAt: task.UpdatedAt}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(raw)}, nil
}

func decodeTaskPublication(ctx context.Context, codec *botContentCodec, botID store.BotID, data map[string]any) (*taskPublicationEnvelope, error) {
	var record botTaskPublicationRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	if record.Format != botDiskRecordFormat || record.BotID != botID || record.Mutation.BotID != botID ||
		record.ID == uuid.Nil || record.TaskID == uuid.Nil || len(record.TaskTitle) > 256 || !utf8.ValidString(record.TaskTitle) ||
		record.TaskKey != record.TaskID.String() || record.TaskHomePath != "tasks/"+record.TaskKey ||
		record.AuthorityConstraintRef != record.Mutation.AuthorityConstraintRef {
		return nil, store.ErrBotInvalid
	}
	candidate, err := codec.decodeTaskRevision(ctx, botID, record.Candidate)
	if err != nil {
		return nil, err
	}
	if candidate.ID != record.ID || candidate.Ref.TaskID != record.TaskID ||
		!reflect.DeepEqual(candidate.Parent, record.ExpectedHead) || candidate.Validate(record.TaskStatus) != nil {
		return nil, store.ErrBotInvalid
	}
	return &taskPublicationEnvelope{
		Publication: store.BotTaskPublication{ID: record.ID, BotID: botID, TaskID: record.TaskID,
			ExpectedHead: record.ExpectedHead, Candidate: *candidate, AuthorityConstraintRef: record.AuthorityConstraintRef},
		Mutation: record.Mutation,
		Task: store.BotTask{ID: record.TaskID, BotID: botID, Key: record.TaskKey, Title: record.TaskTitle,
			Status: record.TaskStatus, Revision: record.TaskRevision, Head: record.TaskHead,
			HomePath: record.TaskHomePath, CreatedAt: record.TaskCreatedAt, UpdatedAt: record.TaskUpdatedAt},
	}, nil
}

var _ store.BotTaskStore = (*personalBotStore)(nil)
var _ store.BotTaskRecoveryStore = (*personalBotStore)(nil)

func (s *personalBotStore) taskRepository() *botRepository {
	r := s.repository()
	r.taskVerifier = s.taskVerifier
	return r
}
func (s *personalBotStore) ListTasks(c context.Context, b store.BotID, p store.BotTaskPage) (*store.BotTaskList, error) {
	return s.taskRepository().ListTasks(c, b, p)
}
func (s *personalBotStore) GetTask(c context.Context, b store.BotID, id uuid.UUID) (*store.BotTask, error) {
	return s.taskRepository().GetTask(c, b, id)
}
func (s *personalBotStore) GetTaskRevision(c context.Context, b store.BotID, p store.BotTaskRef) (*store.BotTaskRevision, error) {
	return s.taskRepository().GetTaskRevision(c, b, p)
}
func (s *personalBotStore) PrepareTaskPublication(c context.Context, q store.BotTaskPrepareRequest) (*store.BotTaskPublication, error) {
	return s.taskRepository().PrepareTaskPublication(c, q)
}
func (s *personalBotStore) CommitTaskPublication(c context.Context, q store.BotTaskCommitRequest) (*store.BotTask, error) {
	return s.taskRepository().CommitTaskPublication(c, q)
}
func (s *personalBotStore) ChangeTaskLifecycle(c context.Context, q store.BotTaskLifecycleRequest) (*store.BotTask, error) {
	return s.taskRepository().ChangeTaskLifecycle(c, q)
}
func (s *personalBotStore) ListPendingTaskPublications(c context.Context, b store.BotID, p store.BotTaskPage) ([]store.BotTaskPublication, error) {
	return s.taskRepository().ListPendingTaskPublications(c, b, p)
}
func (s *personalBotStore) HoldTaskPublications(c context.Context, b store.BotID, p string) error {
	return s.taskRepository().HoldTaskPublications(c, b, p)
}
func (s *personalBotStore) AbortTaskPublication(c context.Context, m store.BotTaskMutation, id uuid.UUID, p string) error {
	return s.taskRepository().AbortTaskPublication(c, m, id, p)
}
func (d personalBotDB) readTask(c context.Context, b, id uuid.UUID) (*store.BotTask, error) {
	v, e := d.client.BotTask.Query().Where(bottask.BotIDEQ(b), bottask.IDEQ(id)).Only(c)
	if e != nil {
		return nil, personalBotError(e)
	}
	r := &store.BotTask{ID: v.ID, BotID: v.BotID, Key: v.Key, Title: v.Title, Revision: v.Revision, Status: store.BotTaskStatus(v.Status), HomePath: v.HomePath, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	if v.HeadRevision > 0 {
		r.Head = &store.BotTaskRef{TaskID: id, Revision: v.HeadRevision, Digest: v.HeadDigest}
	}
	return r, nil
}
func (d personalBotDB) readTasks(c context.Context, b, a uuid.UUID, n int, archived bool) ([]store.BotTask, error) {
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
func (d personalBotDB) writeTask(c context.Context, v store.BotTask, expected int64) error {
	head := int64(0)
	digest := ""
	if v.Head != nil {
		head = v.Head.Revision
		digest = v.Head.Digest
	}
	if expected == 0 {
		_, e := d.client.BotTask.Create().SetID(v.ID).SetBotID(v.BotID).SetKey(v.Key).SetHomePath(v.HomePath).SetTitle(v.Title).SetRevision(v.Revision).SetStatus(bottask.Status(v.Status)).SetHeadRevision(head).SetHeadDigest(digest).SetCreatedAt(v.CreatedAt).SetUpdatedAt(v.UpdatedAt).Save(c)
		return personalBotError(e)
	}
	n, e := d.client.BotTask.Update().Where(bottask.BotIDEQ(v.BotID), bottask.IDEQ(v.ID), bottask.RevisionEQ(expected)).SetRevision(v.Revision).SetTitle(v.Title).SetStatus(bottask.Status(v.Status)).SetHeadRevision(head).SetHeadDigest(digest).SetUpdatedAt(v.UpdatedAt).Save(c)
	if e != nil {
		return personalBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d personalBotDB) readTaskPublication(c context.Context, b, id uuid.UUID) (*taskPublicationEnvelope, error) {
	row, e := d.client.BotTaskRevision.Query().Where(bottaskrevision.BotIDEQ(b), bottaskrevision.IDEQ(id)).Only(c)
	if e != nil {
		return nil, personalBotError(e)
	}
	v, e := decodeTaskPublication(c, d.contentCodec(), b, row.Data)
	if e != nil {
		return nil, e
	}
	p := &v.Publication
	if p.ID != row.ID || p.BotID != b || p.TaskID != row.TaskID || p.Candidate.Ref.Digest != row.Digest || p.Candidate.Ref.Revision != row.Revision || p.Candidate.Validate(v.Task.Status) != nil {
		return nil, store.ErrBotInvalid
	}
	p.State = store.BotTaskPublicationState(row.PublicationState)
	p.WorkspaceReceiptRef = row.WorkspaceReceiptRef
	return v, nil
}
func (d personalBotDB) readTaskPublications(c context.Context, b uuid.UUID) ([]taskPublicationEnvelope, error) {
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
func (d personalBotDB) insertTaskPublication(c context.Context, v taskPublicationEnvelope) error {
	data, e := encodeTaskPublication(c, d.contentCodec(), v)
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
	return personalBotError(e)
}
func (d personalBotDB) setTaskPublication(c context.Context, b, id uuid.UUID, from, to store.BotTaskPublicationState, receipt string) error {
	n, e := d.client.BotTaskRevision.Update().Where(bottaskrevision.BotIDEQ(b), bottaskrevision.IDEQ(id), bottaskrevision.PublicationStateEQ(bottaskrevision.PublicationState(from))).SetPublicationState(bottaskrevision.PublicationState(to)).SetWorkspaceReceiptRef(receipt).Save(c)
	if e != nil {
		return personalBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
