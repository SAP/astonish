package entstore

import (
	"context"
	"encoding/json"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	ent "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/ent/team/botskill"
	"github.com/SAP/astonish/ent/team/botskillrevision"
	"github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

var _ store.BotSkillStore = (*teamBotStore)(nil)

func (s *teamBotStore) ListSkills(c context.Context, b store.BotID, p store.BotSkillPage) (*store.BotSkillList, error) {
	return s.repository().ListSkills(c, b, p)
}
func (s *teamBotStore) GetSkill(c context.Context, b store.BotID, id uuid.UUID) (*store.BotSkill, error) {
	return s.repository().GetSkill(c, b, id)
}
func (s *teamBotStore) GetSkillRevision(c context.Context, b store.BotID, id uuid.UUID, v int64) (*store.BotSkillRevision, error) {
	return s.repository().GetSkillRevision(c, b, id, v)
}
func (s *teamBotStore) ListSkillRevisions(c context.Context, b store.BotID, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	return s.repository().ListSkillRevisions(c, b, id, p)
}
func (s *teamBotStore) ListSkillRuns(c context.Context, b store.BotID, id uuid.UUID, p store.BotSkillPage) (*store.BotSkillRunList, error) {
	return s.repository().ListSkillRuns(c, b, id, p)
}
func (s *teamBotStore) OrientSkill(c context.Context, q store.BotSkillOrientation) (*store.BotSkillOrientationResult, error) {
	return s.repository().OrientSkill(c, q)
}
func (s *teamBotStore) StageSkillLearning(c context.Context, q store.BotSkillLearningRequest) (*store.BotWorkState, error) {
	return s.repository().StageSkillLearning(c, q)
}
func (s *teamBotStore) ChangeSkillLifecycle(c context.Context, q store.BotSkillLifecycleRequest) (*store.BotSkill, error) {
	return s.repository().ChangeSkillLifecycle(c, q)
}
func (s *teamBotStore) ConvertLegacySkill(c context.Context, q store.BotSkillLegacyConversion) (*store.BotSkill, error) {
	return s.repository().ConvertLegacySkill(c, q)
}

func (d teamBotDB) lockSkills(ctx context.Context, id uuid.UUID) error {
	n, err := d.client.Bot.Update().Where(bot.IDEQ(id), bot.StatusNEQ(bot.StatusDeleted)).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotNotFound
	}
	return nil
}
func (d teamBotDB) readSkill(ctx context.Context, b, id uuid.UUID) (*botStoredSkill, error) {
	row, err := d.client.BotSkill.Query().Where(botskill.BotIDEQ(b), botskill.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
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
func (d teamBotDB) readSkills(ctx context.Context, b, after uuid.UUID, limit int, archived bool) ([]store.BotSkill, error) {
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
func (d teamBotDB) writeSkill(ctx context.Context, s botStoredSkill, expected int64) error {
	data, err := botJSONMap(s)
	if err != nil {
		return err
	}
	v := s.Skill
	if expected == 0 {
		_, err = d.client.BotSkill.Create().SetID(v.ID).SetBotID(v.BotID).SetKey(v.Key).SetRevision(v.Revision).SetStatus(botskill.Status(v.Status)).SetData(data).SetCreatedAt(v.CreatedAt).SetUpdatedAt(v.UpdatedAt).Save(ctx)
		return teamBotError(err)
	}
	n, err := d.client.BotSkill.Update().Where(botskill.BotIDEQ(v.BotID), botskill.IDEQ(v.ID), botskill.RevisionEQ(expected)).SetRevision(v.Revision).SetStatus(botskill.Status(v.Status)).SetData(data).SetUpdatedAt(v.UpdatedAt).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) readSkillRevision(ctx context.Context, b, id uuid.UUID, v int64) (*store.BotSkillRevision, error) {
	row, err := d.client.BotSkillRevision.Query().Where(botskillrevision.BotIDEQ(b), botskillrevision.SkillIDEQ(id), botskillrevision.RevisionEQ(v)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
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
func (d teamBotDB) readSkillRevisions(ctx context.Context, b, id uuid.UUID, after int64, limit int) ([]store.BotSkillRevision, error) {
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
func (d teamBotDB) insertSkillRevision(ctx context.Context, r store.BotSkillRevision) error {
	data, err := botJSONMap(r)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = d.client.BotSkillRevision.Create().SetID(r.ID).SetBotID(r.BotID).SetSkillID(r.SkillID).SetRevision(r.Revision).SetDigest(r.Digest).SetSizeBytes(int64(len(raw))).SetData(data).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) skillQuota(ctx context.Context, b uuid.UUID) (int, int64, error) {
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
func (d teamBotDB) skillSourceEvent(ctx context.Context, session uuid.UUID, id string) (*adksession.Event, error) {
	rows, err := d.client.SessionEvent.Query().Where(sessionevent.SessionIDEQ(session.String()), func(s *sql.Selector) { s.Where(sqljson.ValueEQ(sessionevent.FieldEventData, id, sqljson.Path("ID"))) }).Limit(2).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, store.ErrBotNotFound
	}
	return eventDataToADKEvent(rows[0].EventData)
}
func (d teamBotDB) purgeSkills(ctx context.Context, b uuid.UUID) error {
	if _, err := d.client.BotSkillRevision.Delete().Where(botskillrevision.BotIDEQ(b)).Exec(ctx); err != nil {
		return err
	}
	_, err := d.client.BotSkill.Delete().Where(botskill.BotIDEQ(b)).Exec(ctx)
	return err
}
