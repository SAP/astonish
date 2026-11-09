package bot

import (
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// ScheduleCapture is a server-only read-back receipt. It is not an applied
// BotOperationReceipt and must never be reconstructed from tool response JSON.
type ScheduleCapture struct {
	OperationID uuid.UUID
	BotID       uuid.UUID
	SourceRunID uuid.UUID
	OwnerID     uuid.UUID
	Operation   store.BotControlOperation
	Status      store.BotOperationStatus
	Revision    int64
	ObservedAt  time.Time
}

func (c ScheduleCapture) ValidFor(id execution.BotRunIdentity) bool {
	switch c.Operation {
	case store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlSchedulePause, store.BotControlScheduleRemove:
	default:
		return false
	}
	return c.OperationID != uuid.Nil && c.BotID != uuid.Nil && c.SourceRunID != uuid.Nil && c.BotID == id.BotID && c.SourceRunID == id.RunID && c.OwnerID != uuid.Nil && c.OwnerID.String() == id.ActorID && c.Status == store.BotOperationPending && c.Revision > 0 && !c.ObservedAt.IsZero()
}

func (c ScheduleCapture) Acknowledgment() string {
	return fmt.Sprintf("Schedule operation %s (%s) is durably queued and pending. No schedule change is verified by this turn. The worker applies it after the turn ends and publishes a separate committed result in this Bot's home chat.", c.OperationID, c.Operation)
}

func (c ScheduleCapture) Observation() store.BotEvidence {
	return store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: c.Acknowledgment(), Source: "bot-operation://" + c.BotID.String() + "/" + c.OperationID.String(), ActorID: c.OwnerID, ObservedAt: c.ObservedAt}
}
