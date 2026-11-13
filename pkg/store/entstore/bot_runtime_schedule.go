package entstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	personaljob "github.com/SAP/astonish/ent/personal/scheduledjob"
	teambot "github.com/SAP/astonish/ent/team/bot"
	teamjob "github.com/SAP/astonish/ent/team/scheduledjob"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

var _ store.BotScheduledRunStore = (*personalBotStore)(nil)
var _ store.BotScheduledRunStore = (*teamBotStore)(nil)

func (s *personalBotStore) AdmitScheduledRun(ctx context.Context, q store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error) {
	return s.repository().AdmitScheduledRun(ctx, q)
}
func (s *teamBotStore) AdmitScheduledRun(ctx context.Context, q store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error) {
	return s.repository().AdmitScheduledRun(ctx, q)
}

type botScheduledRequest struct {
	external   *store.BotExternalEvent
	occurrence store.BotScheduledOccurrence
	duplicate  bool
}

// AdmitScheduledRun resolves all executable input from the saved immutable
// version. It never authenticates a caller from a scheduled row's owner field.
func (r *botRepository) AdmitScheduledRun(ctx context.Context, q store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, &store.BotScheduleRejected{Cause: err, Outcome: store.BotScheduleRetry}
	}
	jobID, err := uuid.Parse(q.JobID)
	if err != nil || jobID == uuid.Nil || q.BotID == uuid.Nil || q.TriggerID == uuid.Nil || q.Version < 1 || q.OccurrenceAt.IsZero() || q.OccurrenceAt.After(r.clock()) {
		return nil, &store.BotScheduleRejected{Cause: store.ErrBotInvalid, Outcome: store.BotScheduleRetry}
	}
	identity := fmt.Sprintf("%s/%d/%s/%s", q.BotID, q.Version, q.TriggerID, q.OccurrenceAt.UTC().Format(time.RFC3339Nano))
	key := fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
	if q.IdempotencyKey != key {
		return nil, &store.BotScheduleRejected{Cause: store.ErrBotInvalid, Outcome: store.BotScheduleRetry}
	}
	trusted := &botScheduledRequest{occurrence: q}
	run, err := r.requestRun(ctx, store.BotRunRequest{BotID: q.BotID, Version: q.Version, TriggerID: q.TriggerID, OccurrenceAt: q.OccurrenceAt.UTC(), IdempotencyKey: key}, trusted)
	if err != nil {
		return nil, r.rejectScheduledRun(ctx, q, err)
	}
	return &store.BotScheduledAdmission{Run: *run, Duplicate: trusted.duplicate, Acknowledged: true}, nil
}

// The optional DB capability reads the existing scheduler row inside admission's
// transaction, on the same scoped Ent client. There is no parallel timer store.
type botScheduledDB interface {
	acknowledgeScheduledOccurrence(context.Context, store.BotScheduledOccurrence, time.Time, *time.Time) error
	advanceScheduledAdmission(context.Context, botStoredRecord) error
	readBotSchedule(context.Context, uuid.UUID) (*store.ScheduledJob, error)
}

func (d personalBotDB) readBotSchedule(ctx context.Context, id uuid.UUID) (*store.ScheduledJob, error) {
	row, err := d.client.ScheduledJob.Get(ctx, id)
	if err != nil {
		return nil, personalBotError(err)
	}
	return personalEntScheduledJobToStore(row), nil
}
func (d teamBotDB) readBotSchedule(ctx context.Context, id uuid.UUID) (*store.ScheduledJob, error) {
	row, err := d.client.ScheduledJob.Get(ctx, id)
	if err != nil {
		return nil, teamBotError(err)
	}
	return entScheduledJobToStore(row), nil
}

func (d personalBotDB) advanceScheduledAdmission(ctx context.Context, rec botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.LastSeqEQ(rec.LastSequence), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.VersionEQ(rec.Bot.Version), personalbot.StatusEQ(personalbot.StatusActive)).AddLastSeq(1).Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) advanceScheduledAdmission(ctx context.Context, rec botStoredRecord) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.LastSeqEQ(rec.LastSequence), teambot.RevisionEQ(rec.Bot.Revision), teambot.VersionEQ(rec.Bot.Version), teambot.StatusEQ(teambot.StatusActive)).AddLastSeq(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

// Acknowledgement changes only runtime fields and compares the exact persisted
// occurrence. The Bot revision CAS in the same transaction fences edits/pause.
// Compare the database's own timestamp text, not a round-tripped Go time: SQLite
// drivers can normalize offsets/precision when reading legacy scheduler writes.
// The second typed read validates that text represents the requested occurrence;
// the final predicate fences concurrent changes. Ent's Builder binds arguments
// using the appropriate SQLite/PostgreSQL placeholders.
func (d personalBotDB) acknowledgeScheduledOccurrence(ctx context.Context, q store.BotScheduledOccurrence, now time.Time, next *time.Time) error {
	raw, err := d.client.ScheduledJob.Query().Where(personaljob.IDEQ(uuid.MustParse(q.JobID))).Aggregate(func(s *sql.Selector) string { return "CAST(" + s.C(personaljob.FieldNextRunAt) + " AS TEXT)" }).String(ctx)
	if err != nil {
		return personalBotError(err)
	}
	job, err := d.readBotSchedule(ctx, uuid.MustParse(q.JobID))
	if err != nil {
		return err
	}
	if job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) {
		return store.ErrBotConflict
	}
	u := d.client.ScheduledJob.Update().Where(personaljob.IDEQ(uuid.MustParse(q.JobID)), personaljob.ModeEQ(personaljob.ModeBotTrigger), personaljob.StatusEQ(personaljob.StatusActive), func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.WriteString("CAST(").Ident(personaljob.FieldNextRunAt).WriteString(" AS TEXT) = ").Arg(raw)
		}))
	}).SetLastRunAt(now).SetLastStatus("success").SetLastError("").SetConsecutiveFailures(0)
	if next != nil {
		u.SetNextRunAt(*next)
	} else {
		u.ClearNextRunAt()
	}
	n, err := u.Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) acknowledgeScheduledOccurrence(ctx context.Context, q store.BotScheduledOccurrence, now time.Time, next *time.Time) error {
	raw, err := d.client.ScheduledJob.Query().Where(teamjob.IDEQ(uuid.MustParse(q.JobID))).Aggregate(func(s *sql.Selector) string { return "CAST(" + s.C(teamjob.FieldNextRunAt) + " AS TEXT)" }).String(ctx)
	if err != nil {
		return teamBotError(err)
	}
	job, err := d.readBotSchedule(ctx, uuid.MustParse(q.JobID))
	if err != nil {
		return err
	}
	if job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) {
		return store.ErrBotConflict
	}
	u := d.client.ScheduledJob.Update().Where(teamjob.IDEQ(uuid.MustParse(q.JobID)), teamjob.ModeEQ(teamjob.ModeBotTrigger), teamjob.StatusEQ(teamjob.StatusActive), func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.WriteString("CAST(").Ident(teamjob.FieldNextRunAt).WriteString(" AS TEXT) = ").Arg(raw)
		}))
	}).SetLastRunAt(now).SetLastStatus("success").SetLastError("").SetConsecutiveFailures(0)
	if next != nil {
		u.SetNextRunAt(*next)
	} else {
		u.ClearNextRunAt()
	}
	n, err := u.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (r *botRepository) acknowledgeScheduledRequest(ctx context.Context, tx botDraftDB, req store.BotRunRequest, occurrence *botScheduledRequest, now time.Time) error {
	if occurrence == nil || occurrence.external != nil {
		return nil
	}
	var next *time.Time
	if req.Trigger == store.TriggerCron {
		job, err := tx.(botScheduledDB).readBotSchedule(ctx, uuid.MustParse(occurrence.occurrence.JobID))
		if err != nil {
			return err
		}
		schedule, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(job.Schedule.Cron)
		if err != nil {
			return store.ErrBotInvalid
		}
		loc := time.UTC
		if job.Schedule.Timezone != "" {
			loc, err = time.LoadLocation(job.Schedule.Timezone)
			if err != nil {
				return store.ErrBotInvalid
			}
		}
		n := schedule.Next(now.In(loc)).UTC()
		next = &n
	}
	q := occurrence.occurrence
	// Preserve the driver's timestamp representation for SQLite equality (the
	// identity is UTC, but legacy scheduler writes may carry a local offset).
	job, err := tx.(botScheduledDB).readBotSchedule(ctx, uuid.MustParse(q.JobID))
	if err != nil {
		return err
	}
	if job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) {
		return store.ErrBotConflict
	}
	q.OccurrenceAt = *job.NextRun
	return tx.(botScheduledDB).acknowledgeScheduledOccurrence(ctx, q, now, next)
}

func (r *botRepository) resolveScheduledRequest(ctx context.Context, tx botDraftDB, rec *botStoredRecord, trusted *botScheduledRequest, req *store.BotRunRequest) error {
	if trusted.external != nil {
		return r.resolveExternalRequest(ctx, tx, rec, *trusted.external, req)
	}
	q := trusted.occurrence
	if rec.Bot.Status != store.BotActive || rec.Bot.Version != q.Version || rec.Bot.Scope != r.auth.scope {
		return store.ErrBotConflict
	}
	db, ok := tx.(botScheduledDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	job, err := db.readBotSchedule(ctx, uuid.MustParse(q.JobID))
	if err != nil {
		return err
	}
	if !job.Enabled || job.Mode != "bot_trigger" || job.OwnerID != rec.Bot.OwnerID.String() || job.Payload.BotID != q.BotID.String() || job.Payload.BotVersion != q.Version || job.Payload.BotTriggerID != q.TriggerID.String() || job.Payload.Flow != "" || job.Payload.Instructions != "" || len(job.Payload.Params) != 0 {
		return store.ErrBotForbidden
	}
	if job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) {
		previous, err := tx.FindRun(ctx, q.BotID, q.IdempotencyKey)
		if err != nil || !previous.OccurrenceAt.Equal(q.OccurrenceAt) {
			return store.ErrBotConflict
		}
	}
	v, err := tx.ReadVersion(ctx, q.BotID, q.Version)
	if err != nil {
		return err
	}
	for _, trigger := range v.Triggers {
		if trigger.ID != q.TriggerID {
			continue
		}
		if !trigger.Enabled || trigger.ScheduleID == nil || trigger.ScheduleID.String() != q.JobID || strings.TrimSpace(trigger.Instructions) == "" {
			return store.ErrBotForbidden
		}
		switch trigger.Kind {
		case store.TriggerCron:
			if trigger.Cron == "" || trigger.Cron != job.Schedule.Cron || trigger.Timezone != job.Schedule.Timezone {
				return store.ErrBotConflict
			}
		case store.TriggerGoalDeadline:
			if trigger.GoalID == nil {
				return store.ErrBotInvalid
			}
			goal, err := tx.ReadGoal(ctx, q.BotID, *trigger.GoalID)
			if err != nil {
				return err
			}
			if goal.Status != store.BotGoalActive || goal.DueAt == nil || !goal.DueAt.Equal(q.OccurrenceAt) {
				return store.ErrBotConflict
			}
		default:
			return store.ErrBotUnsupported
		}
		req.Trigger = trigger.Kind
		req.Instructions = trigger.Instructions
		req.SkillID, req.Invocation = trigger.SkillID, trigger.Invocation
		if trigger.SkillID != nil {
			req.Instructions = trigger.Invocation
		}
		return nil
	}
	return store.ErrBotForbidden
}
