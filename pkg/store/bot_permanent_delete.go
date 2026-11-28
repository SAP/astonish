package store

import (
	"context"
	"github.com/google/uuid"
)

// BotPermanentDeleteRequest is explicit irreversible intent, never a cleanup attestation.
// There is deliberately no user-supplied reason or deadline.
type BotPermanentDeleteRequest struct {
	BotID            BotID
	ActorID          uuid.UUID
	ExpectedRevision int64
	Confirm          bool
}

// BotPermanentDeleteStore retains the root until verified cleanup and the exact
// target purge commit. PurgeDeleted never selects other eligible Bots.
type BotPermanentDeleteStore interface {
	BeginPermanentDelete(context.Context, BotPermanentDeleteRequest) (*Bot, error)
	PurgeDeleted(context.Context, BotID, int64) error
}
