package scheduler

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"time"
)

// FailBotSchedule serializes failure-only writes with user edits in the file store.
func (s *Store) FailBotSchedule(ctx context.Context, q store.BotScheduledOccurrence, now time.Time, message string, outcome store.BotScheduleFailureOutcome) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if outcome != store.BotScheduleRetry && outcome != store.BotScheduleTerminal {
		return false, store.ErrBotInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[q.JobID]
	if j == nil || !j.Enabled || j.Mode != ModeBotTrigger || j.NextRun == nil || !j.NextRun.Equal(q.OccurrenceAt) || j.Payload.BotID != q.BotID.String() || j.Payload.BotVersion != q.Version || j.Payload.BotTriggerID != q.TriggerID.String() {
		return false, nil
	}
	cp := *j
	cp.LastRun = &now
	cp.LastStatus = StatusFailed
	cp.LastError = message
	cp.ConsecutiveFailures++
	if outcome == store.BotScheduleTerminal {
		cp.Enabled = false
		cp.NextRun = nil
	}
	s.jobs[q.JobID] = &cp
	if err := s.save(); err != nil {
		s.jobs[q.JobID] = j
		return false, err
	}
	return true, nil
}
