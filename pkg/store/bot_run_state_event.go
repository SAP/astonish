package store

import (
	"fmt"
	"github.com/google/uuid"
	"time"

	"google.golang.org/adk/session"
)

// NewBotRunStateEvent uses the transaction's activity sequence. Callers must
// persist this only inside the successful transition transaction, never on reads.
func NewBotRunStateEvent(b Bot, run BotRun, sequence int64, at time.Time) (*session.Event, error) {
	if b.ID == uuid.Nil || b.ID != run.BotID || sequence < 1 || run.ID == uuid.Nil || run.SessionID == uuid.Nil || b.HomeSessionID == uuid.Nil {
		return nil, ErrBotInvalid
	}
	switch run.Status {
	case RunQueued, RunRunning, RunWaitingApproval, RunInterrupted, RunSucceeded, RunPartial, RunFailed, RunSkipped, RunCancelled:
	default:
		return nil, ErrBotInvalid
	}
	if run.Work != nil && (run.Work.Validate() != nil || run.Work.BotID != b.ID || run.Work.RunID != run.ID) {
		return nil, ErrBotInvalid
	}
	state := BotRunState{Work: run.Work, SchemaVersion: BotRunStateSchemaVersion, BotID: b.ID, RunID: run.ID, SessionID: run.SessionID, HomeSessionID: b.HomeSessionID, Presentation: run.Presentation, Status: run.Status, StateSequence: sequence, Reason: run.ContinuationReason, UserEventID: run.UserEventID}
	return &session.Event{ID: fmt.Sprintf("bot-run-state:%s:%d", run.ID, sequence), Author: "bot_runtime", Timestamp: at, Actions: session.EventActions{StateDelta: map[string]any{BotRunStateKey: state}}}, nil
}
