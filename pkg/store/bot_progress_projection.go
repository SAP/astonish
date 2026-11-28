package store

// BuildBotRunState is the canonical live and restored public projection. Sequence
// and committed time belong to this run, never to the latest unrelated activity.
func BuildBotRunState(b Bot, run BotRun) BotRunState {
	return BotRunState{TerminalReason: run.TerminalReason, Progress: run.Progress.Clone(), Work: run.Work, SchemaVersion: BotRunStateSchemaVersion, BotID: b.ID, RunID: run.ID, SessionID: run.SessionID, HomeSessionID: b.HomeSessionID, Presentation: run.Presentation, Status: run.Status, StateSequence: run.StateSequence, CommittedAt: run.StateCommittedAt, Reason: run.ContinuationReason, UserEventID: run.UserEventID}
}

// BuildBotProgressProjection copies only validated employee-facing facts. Private
// checkpoint bindings, context addresses and dispatch receipts never cross here.
func BuildBotProgressProjection(s *BotProgressState, run BotRun) *BotProgressProjection {
	if s == nil || s.Validate() != nil {
		return nil
	}
	cp := s.Checkpoint
	if cp.Binding.BotID != run.BotID || cp.Binding.RunID != run.ID || cp.Binding.SessionID != run.SessionID {
		return nil
	}
	p := &BotProgressProjection{SchemaVersion: BotProgressSchemaVersion, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Revision: cp.Revision, Phase: s.Phase, Reason: s.Reason, ActiveDurationNanos: run.Usage.RuntimeNanos}
	if cp.LastEvaluation != nil {
		at := cp.LastEvaluation.At
		p.EvaluatedAt = &at
		p.Summary = cp.LastEvaluation.Summary
	}
	if run.Status == RunRunning {
		p.ActiveSince = run.StartedAt
	}
	if s.Phase == BotProgressSuspended && s.Reason != BotProgressReasonPaused {
		at := cp.NextEligibleAt
		p.NextEligibleAt = &at
	}
	return p
}
