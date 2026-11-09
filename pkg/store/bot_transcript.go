package store

import (
	"context"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

// BotTranscriptStore appends to the existing run transcript atomically with the
// current worker lease fence and owner/session binding. Implementations must not
// compose independent BotStore reads and SessionStore writes. Repeating an event
// ID in the same run session is a no-op, but still requires a current lease.
type BotTranscriptStore interface {
	AppendBotRunEvent(context.Context, BotLease, uuid.UUID, *session.Event) error
}

// BotTranscriptEvent uses the persisted row ID, never an in-memory position.
type BotTranscriptEvent struct {
	Cursor int64
	Event  *session.Event
}

// BotTranscriptReader is an optional SessionStore capability. Callers must
// authorize the Bot/session binding before every read and delivery. Reads are
// strictly after the cursor, ordered and bounded (1..200).
type BotTranscriptReader interface {
	ReadBotTranscript(context.Context, uuid.UUID, int64, int) ([]BotTranscriptEvent, error)
}
