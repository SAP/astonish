package store

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// Provenance is issuer-written, not caller supplied. Schedule authority is bounded
// to the exact payload digest; the source run keeps its task-purpose mandate.
type BotOperationProvenance struct {
	SourceRunID      uuid.UUID         `json:"source_run_id"`
	SourceUserID     string            `json:"source_user_id"`
	UserEventID      string            `json:"user_event_id"`
	SourceDigest     string            `json:"source_digest"`
	PayloadDigest    string            `json:"payload_digest"`
	Purpose          BotMandatePurpose `json:"purpose"`
	Version          int64             `json:"version"`
	ExpectedRevision int64             `json:"expected_revision"`
}

type BotOperationScheduleReceipt struct {
	TriggerID    uuid.UUID  `json:"trigger_id"`
	ScheduleID   uuid.UUID  `json:"schedule_id"`
	Version      int64      `json:"version"`
	Enabled      bool       `json:"enabled"`
	Removed      bool       `json:"removed"`
	NextRun      *time.Time `json:"next_run,omitempty"`
	SourceRunID  uuid.UUID  `json:"source_run_id"`
	UserEventID  string     `json:"user_event_id"`
	SourceDigest string     `json:"source_digest"`
}

type BotAuthorizedOperationRequest struct {
	Lease          BotLease
	MandateRef     BotMandateRef
	IdempotencyKey string
	Payload        BotControlPayload
}

// BotScheduleAuthorization is supplied canonical persisted source content, never
// model-produced source claims. Composition must explicitly authorize this exact
// requested control from the owner's instruction (not a blanket allow callback).
type BotScheduleAuthorization struct {
	InitiationKind BotInitiationKind
	OwnerCommand   *BotOwnerScheduleCommand
	Mandate        BotMandate
	Provenance     BotOperationProvenance
	Instructions   string
	Payload        BotControlPayload
}
type BotScheduleAuthorizer func(context.Context, BotScheduleAuthorization) error

// Readiness is evaluated outside the write transaction. Both snapshots are fenced
// again before applying. This callback must not mutate schedule configuration.
type BotScheduleReadiness func(context.Context, Bot, BotVersion) error

type BotAuthorizedOperationStore interface {
	EnqueueAuthorizedOperation(context.Context, BotAuthorizedOperationRequest) (*BotOperation, error)
	ReadAuthorizedOperation(context.Context, BotMandateRef, uuid.UUID) (*BotOperation, error)
	ListAuthorizedOperations(context.Context, BotMandateRef) ([]BotOperation, error)
	ApplyAuthorizedOperation(context.Context, BotMandateRef, uuid.UUID) (*BotOperation, error)
}
