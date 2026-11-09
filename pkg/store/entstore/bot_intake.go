package entstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	personaljob "github.com/SAP/astonish/ent/personal/scheduledjob"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	teamjob "github.com/SAP/astonish/ent/team/scheduledjob"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotIntakeStore = (*personalBotStore)(nil)
var _ store.BotIntakeStore = (*teamBotStore)(nil)

func (s *personalBotStore) PromoteChat(ctx context.Context, q store.BotPromotionRequest) (*store.Bot, error) {
	return s.repository().PromoteChat(ctx, q)
}
func (s *teamBotStore) PromoteChat(ctx context.Context, q store.BotPromotionRequest) (*store.Bot, error) {
	return s.repository().PromoteChat(ctx, q)
}
func (s *personalBotStore) ConvertAdaptiveJob(ctx context.Context, q store.BotConversionRequest) (*store.Bot, error) {
	return s.repository().ConvertAdaptiveJob(ctx, q)
}
func (s *teamBotStore) ConvertAdaptiveJob(ctx context.Context, q store.BotConversionRequest) (*store.Bot, error) {
	return s.repository().ConvertAdaptiveJob(ctx, q)
}

func (s *personalBotStore) ReviewAdaptiveJob(ctx context.Context, id uuid.UUID) (*store.BotAdaptiveReview, error) {
	return s.repository().ReviewAdaptiveJob(ctx, id)
}
func (s *teamBotStore) ReviewAdaptiveJob(ctx context.Context, id uuid.UUID) (*store.BotAdaptiveReview, error) {
	return s.repository().ReviewAdaptiveJob(ctx, id)
}
func (r *botRepository) ReviewAdaptiveJob(ctx context.Context, id uuid.UUID) (*store.BotAdaptiveReview, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	var out *store.BotAdaptiveReview
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		var updated time.Time
		switch d := tx.(type) {
		case personalBotDB:
			row, err := d.client.ScheduledJob.Get(ctx, id)
			if err != nil {
				return personalBotError(err)
			}
			updated = row.UpdatedAt
		case teamBotDB:
			row, err := d.client.ScheduledJob.Get(ctx, id)
			if err != nil {
				return teamBotError(err)
			}
			updated = row.UpdatedAt
		default:
			return store.ErrBotUnsupported
		}
		job, err := tx.(botIntakeDB).reviewAdaptive(ctx, id, actor, updated)
		if err != nil {
			return err
		}
		out = &store.BotAdaptiveReview{SourceJobID: id, UpdatedAt: updated, Schedule: job.Schedule, Instructions: job.Payload.Instructions}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type botIntakeDB interface {
	reviewChat(context.Context, uuid.UUID, uuid.UUID) error
	insertCarryoverMemory(context.Context, uuid.UUID, uuid.UUID, string, string) error
	reviewAdaptive(context.Context, uuid.UUID, uuid.UUID, time.Time) (*store.ScheduledJob, error)
	pauseAdaptive(context.Context, uuid.UUID, time.Time) error
}

func (d personalBotDB) reviewChat(ctx context.Context, id, actor uuid.UUID) error {
	row, err := d.client.Session.Get(ctx, id.String())
	if err != nil {
		return personalBotError(err)
	}
	if row.UserID == nil || *row.UserID != actor || row.BotID != nil || row.BotRunID != nil || row.Purpose != personalsession.PurposeChat {
		return store.ErrBotForbidden
	}
	return nil
}
func (d teamBotDB) reviewChat(ctx context.Context, id, actor uuid.UUID) error {
	row, err := d.client.Session.Get(ctx, id.String())
	if err != nil {
		return teamBotError(err)
	}
	if row.UserID == nil || *row.UserID != actor || row.BotID != nil || row.BotRunID != nil || row.Purpose != teamsession.PurposeChat {
		return store.ErrBotForbidden
	}
	return nil
}
func (d personalBotDB) insertCarryoverMemory(ctx context.Context, sid, actor uuid.UUID, content, category string) error {
	_, err := d.client.Memory.Create().SetSessionID(sid).SetCreatedBy(actor).SetChunkText(content).SetCategory(category).SetTsv(content).Save(ctx)
	return personalBotError(err)
}
func (d teamBotDB) insertCarryoverMemory(ctx context.Context, sid, actor uuid.UUID, content, category string) error {
	_, err := d.client.Memory.Create().SetSessionID(sid).SetCreatedBy(actor).SetChunkText(content).SetCategory(category).SetTsv(content).Save(ctx)
	return teamBotError(err)
}
func (d personalBotDB) reviewAdaptive(ctx context.Context, id, actor uuid.UUID, expected time.Time) (*store.ScheduledJob, error) {
	row, err := d.client.ScheduledJob.Get(ctx, id)
	if err != nil {
		return nil, personalBotError(err)
	}
	if row.CreatedBy == nil || *row.CreatedBy != actor {
		return nil, store.ErrBotForbidden
	}
	if row.Mode != personaljob.ModeAdaptive || row.Status != personaljob.StatusActive || !row.UpdatedAt.Equal(expected) {
		return nil, store.ErrBotConflict
	}
	return personalEntScheduledJobToStore(row), nil
}
func (d teamBotDB) reviewAdaptive(ctx context.Context, id, actor uuid.UUID, expected time.Time) (*store.ScheduledJob, error) {
	row, err := d.client.ScheduledJob.Get(ctx, id)
	if err != nil {
		return nil, teamBotError(err)
	}
	if row.CreatedBy == nil || *row.CreatedBy != actor {
		return nil, store.ErrBotForbidden
	}
	if row.Mode != teamjob.ModeAdaptive || row.Status != teamjob.StatusActive || !row.UpdatedAt.Equal(expected) {
		return nil, store.ErrBotConflict
	}
	return entScheduledJobToStore(row), nil
}
func (d personalBotDB) pauseAdaptive(ctx context.Context, id uuid.UUID, expected time.Time) error {
	row, err := d.client.ScheduledJob.Get(ctx, id)
	if err != nil {
		return personalBotError(err)
	}
	if !row.UpdatedAt.Equal(expected) {
		return store.ErrBotConflict
	}
	expected = row.UpdatedAt
	n, err := d.client.ScheduledJob.Update().Where(personaljob.IDEQ(id), personaljob.ModeEQ(personaljob.ModeAdaptive), personaljob.StatusEQ(personaljob.StatusActive), personaljob.UpdatedAtGTE(expected.Truncate(time.Microsecond)), personaljob.UpdatedAtLT(expected.Truncate(time.Microsecond).Add(time.Microsecond))).SetStatus(personaljob.StatusPaused).ClearNextRunAt().Save(ctx)
	if err != nil {
		return personalBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) pauseAdaptive(ctx context.Context, id uuid.UUID, expected time.Time) error {
	row, err := d.client.ScheduledJob.Get(ctx, id)
	if err != nil {
		return teamBotError(err)
	}
	if !row.UpdatedAt.Equal(expected) {
		return store.ErrBotConflict
	}
	expected = row.UpdatedAt
	n, err := d.client.ScheduledJob.Update().Where(teamjob.IDEQ(id), teamjob.ModeEQ(teamjob.ModeAdaptive), teamjob.StatusEQ(teamjob.StatusActive), teamjob.UpdatedAtGTE(expected.Truncate(time.Microsecond)), teamjob.UpdatedAtLT(expected.Truncate(time.Microsecond).Add(time.Microsecond))).SetStatus(teamjob.StatusPaused).ClearNextRunAt().Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

// insertIntakeDraft runs wholly in its caller's scoped transaction. Intake hash
// includes the reviewed request, so idempotency cannot silently change carryover.
func insertIntakeDraft(ctx context.Context, tx botDraftDB, rec botStoredRecord, v store.BotVersion, goals []store.BotGoal, carry store.BotCarryover, operation string, source uuid.UUID) (*store.Bot, error) {
	if err := tx.InsertBot(ctx, rec); err != nil {
		return nil, err
	}
	hash, err := botHash(v)
	if err != nil {
		return nil, err
	}
	if err := tx.InsertVersion(ctx, v, hash); err != nil {
		return nil, err
	}
	if err := tx.InsertHomeSession(ctx, rec.Bot); err != nil {
		return nil, err
	}
	for _, g := range goals {
		if err := tx.InsertGoal(ctx, g); err != nil {
			return nil, err
		}
	}
	db, ok := tx.(botIntakeDB)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	for _, m := range carry.Memories {
		if strings.TrimSpace(m.Content) == "" {
			return nil, store.ErrBotInvalid
		}
		if err := db.insertCarryoverMemory(ctx, rec.Bot.HomeSessionID, rec.Bot.OwnerID, m.Content, m.Category); err != nil {
			return nil, err
		}
	}
	if carry.Summary != "" {
		if err := db.insertCarryoverMemory(ctx, rec.Bot.HomeSessionID, rec.Bot.OwnerID, carry.Summary, "promotion_summary"); err != nil {
			return nil, err
		}
	}
	// Text artifact snapshots remain durable, inspectable carryover in activity;
	// never pretend that an unconfined source path is a Bot-home artifact.
	payload, err := json.Marshal(map[string]any{"operation": operation, "source_id": source, "actor_id": rec.Bot.OwnerID, "artifacts": carry.Artifacts})
	if err != nil {
		return nil, err
	}
	if err := tx.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, Sequence: 1, Kind: store.BotActivityLifecycle, Payload: payload, CreatedAt: rec.Bot.CreatedAt}, "draft-created"); err != nil {
		return nil, err
	}
	return &rec.Bot, nil
}
func (r *botRepository) PromoteChat(ctx context.Context, q store.BotPromotionRequest) (*store.Bot, error) {
	if !q.Reviewed || r.auth.scope == store.BotScopeTeam && !q.ReviewedForTeam {
		return nil, store.ErrBotForbidden
	}
	if q.SourceSessionID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	v := store.DefaultBotVersion()
	v.Charter = q.Charter
	goals := make([]store.BotGoal, 0, len(q.Carryover.Goals))
	for _, g := range q.Carryover.Goals {
		goals = append(goals, store.BotGoal{Title: g.Title, CompletionCriteria: g.CompletionCriteria})
	}
	for _, a := range q.Carryover.Artifacts {
		if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Content) == "" {
			return nil, store.ErrBotInvalid
		}
	}
	rec, v, goals, err := prepareBotDraft(store.BotCreateRequest{Name: q.Name, OwnerID: actor, IdempotencyKey: q.IdempotencyKey, Version: v, Goals: goals}, actor, r.auth.scope, r.clock())
	if err != nil {
		return nil, err
	}
	rec.CreateHash, err = botHash([]any{"chat_promotion", q.SourceSessionID, q})
	if err != nil {
		return nil, err
	}
	var result *store.Bot
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		db, ok := tx.(botIntakeDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		if err := db.reviewChat(ctx, q.SourceSessionID, actor); err != nil {
			return err
		}
		old, err := tx.FindCreate(ctx, rec.CreateKey)
		if err == nil {
			if old.CreateHash != rec.CreateHash {
				return store.ErrBotConflict
			}
			result = &old.Bot
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		result, err = insertIntakeDraft(ctx, tx, rec, v, goals, q.Carryover, "chat_promotion", q.SourceSessionID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (r *botRepository) ConvertAdaptiveJob(ctx context.Context, q store.BotConversionRequest) (*store.Bot, error) {
	if !q.Reviewed || r.auth.scope == store.BotScopeTeam && !q.ReviewedForTeam {
		return nil, store.ErrBotForbidden
	}
	if q.SourceJobID == uuid.Nil || q.ExpectedUpdatedAt.IsZero() {
		return nil, store.ErrBotInvalid
	}
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	var result *store.Bot
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		db, ok := tx.(botIntakeDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		// Check an existing committed result before reading the now-paused original.
		key, err := botHash([]string{actor.String(), q.IdempotencyKey})
		if err != nil {
			return err
		}
		hash, err := botHash([]any{"adaptive_conversion", q.SourceJobID, q})
		if err != nil {
			return err
		}
		old, err := tx.FindCreate(ctx, key)
		if err == nil {
			if old.CreateHash != hash {
				return store.ErrBotConflict
			}
			result = &old.Bot
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		job, err := db.reviewAdaptive(ctx, q.SourceJobID, actor, q.ExpectedUpdatedAt)
		if err != nil {
			return err
		}
		v := store.DefaultBotVersion()
		v.Charter = q.Charter
		// Only the reviewed job schedule and instructions cross over; no delivery,
		// credentials, Params, execution history or owner/team bindings are copied.
		if job.Schedule.Cron == "" || strings.TrimSpace(job.Payload.Instructions) == "" {
			return store.ErrBotInvalid
		}
		v.Triggers = []store.TriggerSpec{{ID: uuid.New(), Kind: store.TriggerCron, Cron: job.Schedule.Cron, Timezone: job.Schedule.Timezone, Instructions: job.Payload.Instructions}}
		rec, v, goals, err := prepareBotDraft(store.BotCreateRequest{Name: q.Name, OwnerID: actor, IdempotencyKey: q.IdempotencyKey, Version: v}, actor, r.auth.scope, r.clock())
		if err != nil {
			return err
		}
		rec.CreateHash = hash
		result, err = insertIntakeDraft(ctx, tx, rec, v, goals, store.BotCarryover{}, "adaptive_conversion", q.SourceJobID)
		if err != nil {
			return err
		}
		// Last write: a CAS failure rolls back every replacement row, and any later
		// commit failure rolls back this pause. Activation remains a separate action.
		return db.pauseAdaptive(ctx, q.SourceJobID, q.ExpectedUpdatedAt)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
