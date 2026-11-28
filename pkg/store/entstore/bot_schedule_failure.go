package entstore

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	personaljob "github.com/SAP/astonish/ent/personal/scheduledjob"
	teamjob "github.com/SAP/astonish/ent/team/scheduledjob"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotScheduleFailureStore = (*personalSchedulerStore)(nil)
var _ store.BotScheduleFailureStore = (*teamSchedulerStore)(nil)

func failureBinding(s *sql.Selector, q store.BotScheduledOccurrence, raw string) {
	s.Where(sql.P(func(b *sql.Builder) { b.WriteString("CAST(").Ident("next_run_at").WriteString(" AS TEXT) = ").Arg(raw) }))
	s.Where(sqljson.ValueEQ("payload", q.BotID.String(), sqljson.Path("bot_id")))
	s.Where(sqljson.ValueEQ("payload", q.Version, sqljson.Path("bot_version")))
	s.Where(sqljson.ValueEQ("payload", q.TriggerID.String(), sqljson.Path("bot_trigger_id")))
}

func (s *personalSchedulerStore) FailBotSchedule(ctx context.Context, q store.BotScheduledOccurrence, now time.Time, message string, outcome store.BotScheduleFailureOutcome) (bool, error) {
	id, err := uuid.Parse(q.JobID)
	if err != nil {
		return false, store.ErrBotInvalid
	}
	if outcome != store.BotScheduleRetry && outcome != store.BotScheduleTerminal {
		return false, store.ErrBotInvalid
	}
	raw, err := s.client.ScheduledJob.Query().Where(personaljob.IDEQ(id), personaljob.NextRunAtNotNil()).Aggregate(func(s *sql.Selector) string { return "CAST(" + s.C(personaljob.FieldNextRunAt) + " AS TEXT)" }).String(ctx)
	if err != nil {
		return false, personalBotError(err)
	}
	job := s.Get(ctx, q.JobID)
	if job == nil || job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) {
		return false, nil
	}
	u := s.client.ScheduledJob.Update().Where(personaljob.IDEQ(id), personaljob.ModeEQ(personaljob.ModeBotTrigger), personaljob.StatusEQ(personaljob.StatusActive), func(s *sql.Selector) { failureBinding(s, q, raw) }).SetLastRunAt(now).SetLastStatus("failed").SetLastError(message).AddConsecutiveFailures(1)
	if outcome == store.BotScheduleTerminal {
		u.SetStatus(personaljob.StatusPaused).ClearNextRunAt()
	}
	n, err := u.Save(ctx)
	return n == 1, err
}

func (s *teamSchedulerStore) FailBotSchedule(ctx context.Context, q store.BotScheduledOccurrence, now time.Time, message string, outcome store.BotScheduleFailureOutcome) (bool, error) {
	id, err := uuid.Parse(q.JobID)
	if err != nil {
		return false, store.ErrBotInvalid
	}
	if outcome != store.BotScheduleRetry && outcome != store.BotScheduleTerminal {
		return false, store.ErrBotInvalid
	}
	raw, err := s.client.ScheduledJob.Query().Where(teamjob.IDEQ(id), teamjob.NextRunAtNotNil()).Aggregate(func(s *sql.Selector) string { return "CAST(" + s.C(teamjob.FieldNextRunAt) + " AS TEXT)" }).String(ctx)
	if err != nil {
		return false, teamBotError(err)
	}
	job := s.Get(ctx, q.JobID)
	if job == nil || job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) {
		return false, nil
	}
	u := s.client.ScheduledJob.Update().Where(teamjob.IDEQ(id), teamjob.ModeEQ(teamjob.ModeBotTrigger), teamjob.StatusEQ(teamjob.StatusActive), func(s *sql.Selector) { failureBinding(s, q, raw) }).SetLastRunAt(now).SetLastStatus("failed").SetLastError(message).AddConsecutiveFailures(1)
	if outcome == store.BotScheduleTerminal {
		u.SetStatus(teamjob.StatusPaused).ClearNextRunAt()
	}
	n, err := u.Save(ctx)
	return n == 1, err
}

func (r *botRepository) rejectScheduledRun(ctx context.Context, q store.BotScheduledOccurrence, cause error) error {
	outcome := store.BotScheduleRetry
	// Permanent validation/binding denials cannot become admissible by retrying
	// the same version and occurrence. Conflicts, budgets and I/O remain retryable.
	if errors.Is(cause, store.ErrBotForbidden) || errors.Is(cause, store.ErrBotInvalid) || errors.Is(cause, store.ErrBotUnsupported) {
		outcome = store.BotScheduleTerminal
	}
	var rejected *store.BotScheduleRejected
	if errors.As(cause, &rejected) {
		outcome = rejected.Outcome
	}
	// Explicit typed outcomes override the sentinel classification; unknown
	// infrastructure failures remain retryable rather than being guessed from text.
	var failures store.BotScheduleFailureStore
	switch db := r.db.(type) {
	case personalBotDB:
		failures = &personalSchedulerStore{client: db.client}
	case teamBotDB:
		failures = &teamSchedulerStore{client: db.client}
	}
	ack := false
	// Failure bookkeeping is not a permission bypass. The scheduled admission
	// path requires the live accountable owner, not merely an organization member.
	rec, readErr := r.db.GetBot(ctx, q.BotID)
	if readErr != nil {
		return &store.BotScheduleRejected{Cause: errors.Join(cause, readErr), Outcome: outcome}
	}
	actor, authErr := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if authErr != nil || actor != rec.Bot.OwnerID {
		return &store.BotScheduleRejected{Cause: cause, Outcome: outcome}
	}
	if failures != nil {
		_, err := failures.FailBotSchedule(ctx, q, r.clock(), cause.Error(), outcome)
		ack = err == nil
		if err != nil {
			cause = errors.Join(cause, err)
		}
	}
	return &store.BotScheduleRejected{Cause: cause, Outcome: outcome, Acknowledged: ack}
}
