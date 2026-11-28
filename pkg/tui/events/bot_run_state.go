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
	if t.SessionID != s.SessionID.String() && (t.SessionID != s.HomeSessionID.String() || (s.Presentation != store.BotRunPresentationHomeInteractive && s.Progress == nil && s.NeutralTerminalLabel() == "")) {
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
	prior := t.BotRunStates[s.RunID.String()]
	if prior.StateSequence >= s.StateSequence || prior.SessionID != uuid.Nil && prior.SessionID != s.SessionID {
		return
	}
	if w := s.Work; w != nil {
		if w.BotID != s.BotID || w.RunID != s.RunID || w.WorkRevision < 1 || prior.Work != nil && w.WorkRevision < prior.Work.WorkRevision {
			return
		}
	}
	next := *s
	if p := s.Progress; p != nil {
		// Unknown optional versions fall back to execution status; malformed or
		// cross-bound known projections cannot advance this run.
		if p.SchemaVersion != store.BotProgressSchemaVersion || !p.Phase.Valid() {
			next.Progress = nil
		} else if s.ValidateProgress() != nil || p.ActiveDurationNanos < 0 || prior.Progress != nil && p.Revision < prior.Progress.Revision {
			return
		} else {
			next.Progress = p.Clone()
		}
	} else {
		next.Progress = prior.Progress.Clone()
	}
	if next.Work == nil {
		next.Work = prior.Work
	}
	t.BotRunStates[s.RunID.String()] = next
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
			(botRunStatePriority(s) == botRunStatePriority(latest) && (s.CommittedAt.After(latest.CommittedAt) || s.CommittedAt.Equal(latest.CommittedAt) && s.RunID.String() > latest.RunID.String())) {
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
	if label := latest.NeutralTerminalLabel(); label != "" {
		t.Status, t.Streaming = label, false
	}
	if latest.Reason == store.BotContinuationRequired {
		t.Status = "Continuation required"
		t.Streaming = false
	}
	if label, busy := BotProgressStatus(latest); label != "" {
		t.Status, t.Streaming = label, busy
	}
}

// BotProgressStatus is shared by compact status and read-only details. Progress
// never supersedes a committed terminal execution status.
func BotProgressStatus(s store.BotRunState) (string, bool) {
	if s.Progress == nil || s.ValidateProgress() != nil {
		return "", false
	}
	switch s.Status {
	case store.RunQueued, store.RunRunning, store.RunInterrupted, store.RunWaitingApproval:
	default:
		return "", false
	}
	switch s.Progress.Phase {
	case store.BotProgressWorking:
		return "Working", true
	case store.BotProgressEvaluating:
		return "Checking progress", true
	case store.BotProgressReplanning:
		return "Adjusting approach", true
	case store.BotProgressCheckpointing:
		return "Saving progress", true
	case store.BotProgressSynthesizing:
		return "Preparing result", true
	case store.BotProgressSuspended:
		return "Paused", false
	case store.BotProgressWaitingUser:
		return "Waiting for your input", false
	case store.BotProgressWaitingBudget:
		return "Waiting for budget", false
	case store.BotProgressWaitingAuthorization:
		return "Authorization required", false
	case store.BotProgressReconciling:
		return "Checking an unconfirmed action", false
	}
	return "", false
}
