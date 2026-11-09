package entstore

import (
	"context"
	"encoding/json"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

type botReviewKind string

const (
	botReviewProposal botReviewKind = "proposal"
	botReviewApproval botReviewKind = "approval"
)

// A review event is only a durable reference. Snapshot/approval reads and their
// existing decision guards remain the authority. Called inside creation's
// transaction, so retries cannot append a second card or expose unsaved input.
func appendBotReview(ctx context.Context, tx botDraftDB, b store.Bot, runID, id uuid.UUID, kind botReviewKind, now time.Time) error {
	adapter, ok := tx.(botPublicationDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	payload, err := json.Marshal(map[string]any{"kind": kind, "review_id": id.String()})
	if err != nil {
		return err
	}
	// A distinct turn avoids claiming that this reference replaces the model's
	// canonical content events or participates in its runtime sequence counter.
	eventID := uuid.NewSHA1(id, []byte("bot-review:"+string(kind)))
	envelope, err := json.Marshal(botdomain.RuntimeEvent{SessionID: b.HomeSessionID, RunID: runID, TurnID: eventID, Sequence: 1, Type: "bot_review", Payload: payload})
	if err != nil {
		return err
	}
	return adapter.AppendHomeReport(ctx, b, &session.Event{ID: eventID.String(), InvocationID: eventID.String(), Author: "bot_runtime", Timestamp: now, Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": json.RawMessage(envelope)}}})
}
