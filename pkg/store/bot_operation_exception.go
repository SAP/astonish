package store

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// BotOperationExceptionReference describes one exact, expiring authorization.
// It is not itself a grant: stores must verify their immutable issuer record.
// Unknown actions remain unsupported even when a descriptor is supplied.
type BotOperationExceptionReference struct {
	MandateRef    BotMandateRef `json:"mandate_ref"`
	OperationID   uuid.UUID     `json:"operation_id"`
	PayloadDigest string        `json:"payload_digest"`
	Resource      string        `json:"resource"`
	ExpiresAt     time.Time     `json:"expires_at"`
}

type BotRoutineExceptionDecision struct {
	MandateRef       BotMandateRef
	OperationID      uuid.UUID
	PayloadDigest    string
	ExpectedRevision int64
	Approve          bool
	ExpiresAt        time.Time
}

// Optional capability; approval records consent, never application.
type BotRoutineExceptionStore interface {
	DecideRoutineException(context.Context, BotRoutineExceptionDecision) (*BotOperation, error)
}
