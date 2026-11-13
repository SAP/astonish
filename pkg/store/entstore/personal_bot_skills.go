package entstore

import (
	"context"
	"encoding/json"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	ent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/personal/bot"
	"github.com/SAP/astonish/ent/personal/botskill"
	"github.com/SAP/astonish/ent/personal/botskillrevision"
	"github.com/SAP/astonish/ent/personal/sessionevent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

var _ store.BotSkillStore = (*personalBotStore)(nil)

func (s *personalBotStore) ListSkills(c context.Context, b store.BotID, p store.BotSkillPage) (*store.BotSkillList, error) {
	return s.repository().ListSkills(c, b, p)
}
func (s *personalBotStore) GetSkill(c context.Context, b store.BotID, id uuid.UUID) (*store.BotSkill, error) {
	return s.repository().GetSkill(c, b, id)
}
func (s *personalBotStore) GetSkillRevision(c context.Context, b store.BotID, id uuid.UUID, v int64) (*store.BotSkillRevision, error) {
	return s.repository().GetSkillRevision(c, b, id, v)
}
func (s *personalBotStore) ListSkillRevisions(c context.Context, b store.BotID, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	return s.repository().ListSkillRevisions(c, b, id, p)
}
func (s *personalBotStore) ListSkillRuns(c context.Context, b store.BotID, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRunList, error) {
	return s.repository().ListSkillRuns(c, b, id, p)
}
func (s *personalBotStore) OrientSkill(c context.Context, q store.BotSkillOrientation) (*store.BotSkillOrientationResult, error) {
	return s.repository().OrientSkill(c, q)
}
func (s *personalBotStore) StageSkillLearning(c context.Context, q store.BotSkillLearningRequest) (*store.BotWorkState, error) {
	return s.repository().StageSkillLearning(c, q)
}
func (s *personalBotStore) ChangeSkillLifecycle(c context.Context, q store.BotSkillLifecycleRequest) (*store.BotSkill, error) {
	return s.repository().ChangeSkillLifecycle(c, q)
}
func (s *personalBotStore) ConvertLegacySkill(c context.Context, q store.BotSkillLegacyConversion) (*store.BotSkill, error) {
	return s.repository().ConvertLegacySkill(c, q)
}

func (d personalBotDB) lockSkills(ctx context.Context, id uuid.UUID) error {
	n, err := d.client.Bot.Update().Where(bot.IDEQ(id), bot.StatusNEQ(bot.StatusDeleted)).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotNotFound
	}
	return nil
}
func (d personalBotDB) readSkill(ctx context.Context, b, id uuid.UUID) (*botStoredSkill, error) {
	row, err := d.client.BotSkill.Query().Where(botskill.BotIDEQ(b), botskill.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, personalBotError(err)
	}
	var s botStoredSkill
	if err = botDecode(row.Data, &s); err != nil {
		return nil, err
	}
	if s.Skill.ID != row.ID || s.Skill.BotID != b || s.Skill.Key != row.Key || s.Skill.Revision != row.Revision || string(s.Skill.Status) != string(row.Status) {
		return nil, store.ErrBotInvalid
	}
	return &s, nil
}
func (d personalBotDB) readSkills(ctx context.Context, b, after uuid.UUID, limit int, archived bool) ([]store.BotSkill, error) {
	q := d.client.BotSkill.Query().Where(botskill.BotIDEQ(b), botskill.IDGT(after)).Order(ent.Asc(botskill.FieldID)).Limit(limit)
	if !archived {
		q.Where(botskill.StatusNEQ(botskill.StatusArchived))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotSkill, 0, len(rows))
	for _, row := range rows {
		s, err := d.readSkill(ctx, b, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, s.Skill)
	}
	return out, nil
}
func (d personalBotDB) writeSkill(ctx context.Context, s botStoredSkill, expected int64) error {
	data, err := botJSONMap(s)
	if err != nil {
		return err
	}
	v := s.Skill
	if expected == 0 {
		_, err = d.client.BotSkill.Create().SetID(v.ID).SetBotID(v.BotID).SetKey(v.Key).SetRevision(v.Revision).SetStatus(botskill.Status(v.Status)).SetData(data).SetCreatedAt(v.CreatedAt).SetUpdatedAt(v.UpdatedAt).Save(ctx)
		return personalBotError(err)
	}
	n, err := d.client.BotSkill.Update().Where(botskill.BotIDEQ(v.BotID), botskill.IDEQ(v.ID), botskill.RevisionEQ(expected)).SetRevision(v.Revision).SetStatus(botskill.Status(v.Status)).SetData(data).SetUpdatedAt(v.UpdatedAt).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d personalBotDB) readSkillRevision(ctx context.Context, b, id uuid.UUID, v int64) (*store.BotSkillRevision, error) {
	row, err := d.client.BotSkillRevision.Query().Where(botskillrevision.BotIDEQ(b), botskillrevision.SkillIDEQ(id), botskillrevision.RevisionEQ(v)).Only(ctx)
	if err != nil {
		return nil, personalBotError(err)
	}
	var r store.BotSkillRevision
	if err = botDecode(row.Data, &r); err != nil {
		return nil, err
	}
	if r.ID != row.ID || r.BotID != b || r.SkillID != id || r.Revision != v || r.Digest != row.Digest {
		return nil, store.ErrBotInvalid
	}
	if err = r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
func (d personalBotDB) readSkillRevisions(ctx context.Context, b, id uuid.UUID, after int64, limit int) ([]store.BotSkillRevision, error) {
	rows, err := d.client.BotSkillRevision.Query().Where(botskillrevision.BotIDEQ(b), botskillrevision.SkillIDEQ(id), botskillrevision.RevisionGT(after)).Order(ent.Asc(botskillrevision.FieldRevision)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotSkillRevision, 0, len(rows))
	for _, row := range rows {
		r, err := d.readSkillRevision(ctx, b, id, row.Revision)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, nil
}
func (d personalBotDB) insertSkillRevision(ctx context.Context, r store.BotSkillRevision) error {
	data, err := botJSONMap(r)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = d.client.BotSkillRevision.Create().SetID(r.ID).SetBotID(r.BotID).SetSkillID(r.SkillID).SetRevision(r.Revision).SetDigest(r.Digest).SetSizeBytes(int64(len(raw))).SetData(data).Save(ctx)
	return personalBotError(err)
}
func (d personalBotDB) skillQuota(ctx context.Context, b uuid.UUID) (int, int64, error) {
	count, err := d.client.BotSkill.Query().Where(botskill.BotIDEQ(b)).Count(ctx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := d.client.BotSkillRevision.Query().Where(botskillrevision.BotIDEQ(b)).Select(botskillrevision.FieldSizeBytes).All(ctx)
	if err != nil {
		return 0, 0, err
	}
	var size int64
	for _, row := range rows {
		size += row.SizeBytes
	}
	return count, size, nil
}
func (d personalBotDB) skillSourceEvent(ctx context.Context, session uuid.UUID, id string) (*adksession.Event, error) {
	rows, err := d.client.SessionEvent.Query().Where(sessionevent.SessionIDEQ(session.String()), func(s *sql.Selector) { s.Where(sqljson.ValueEQ(sessionevent.FieldEventData, id, sqljson.Path("ID"))) }).Limit(2).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, store.ErrBotNotFound
	}
	return eventDataToADKEvent(rows[0].EventData)
}
func (d personalBotDB) purgeSkills(ctx context.Context, b uuid.UUID) error {
	if _, err := d.client.BotSkillRevision.Delete().Where(botskillrevision.BotIDEQ(b)).Exec(ctx); err != nil {
		return err
	}
	_, err := d.client.BotSkill.Delete().Where(botskill.BotIDEQ(b)).Exec(ctx)
	return err
}
