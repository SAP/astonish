package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Durable state is presentation only; it cannot grant permission or resume work.
func (t *Transcript) applyBotRunState(s *store.BotRunState) {
	if s == nil || s.SchemaVersion != store.BotRunStateSchemaVersion || s.BotID.String() != t.BotID || s.RunID == uuid.Nil || s.SessionID == uuid.Nil || s.StateSequence < 1 {
		return
	}
	if t.SessionID != s.SessionID.String() && (t.SessionID != s.HomeSessionID.String() || s.Presentation != store.BotRunPresentationHomeInteractive) {
		return
	}
	switch s.Status {
	case store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled:
	default:
		return
	}
	if t.BotRunStates == nil {
		t.BotRunStates = map[string]store.BotRunState{}
	}
	if prior := t.BotRunStates[s.RunID.String()]; prior.StateSequence >= s.StateSequence {
		return
	}
	t.BotRunStates[s.RunID.String()] = *s
}
func botRunStatePriority(s store.BotRunState) int {
	if s.Reason == store.BotContinuationRequired {
		return 2
	}
	switch s.Status {
	case store.RunRunning:
		return 5
	case store.RunQueued:
		return 4
	case store.RunWaitingApproval:
		return 3
	case store.RunInterrupted:
		return 2
	default:
		return 1
	}
}

func (t *Transcript) restoreBotRunState() {
	if len(t.BotRunStates) == 0 {
		return
	}
	var latest store.BotRunState
	// Sequence numbers are comparable only within the same run. Prefer live
	// work, with a stable ID tie-break, regardless of snapshot/replay ordering.
	for _, s := range t.BotRunStates {
		if latest.RunID == uuid.Nil || botRunStatePriority(s) > botRunStatePriority(latest) ||
			(botRunStatePriority(s) == botRunStatePriority(latest) && s.RunID.String() > latest.RunID.String()) {
			latest = s
		}
	}
	t.Streaming = false
	switch latest.Status {
	case store.RunQueued:
		t.Status = "Queued…"
		t.Streaming = true
	case store.RunRunning:
		t.Status = "Thinking…"
		t.Streaming = true
	case store.RunWaitingApproval:
		t.Status = "Waiting for answer or review"
	case store.RunInterrupted:
		t.Status = "Interrupted — explicit continuation required"
	case store.RunSucceeded:
		t.Status = "Completed"
	case store.RunPartial:
		t.Status = "Partial"
	case store.RunFailed:
		t.Status = "Failed"
	case store.RunSkipped:
		t.Status = "Skipped"
	case store.RunCancelled:
		t.Status = "Cancelled"
	}
	if latest.Reason == store.BotContinuationRequired {
		t.Status = "Continuation required"
		t.Streaming = false
	}
}
