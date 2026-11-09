package bot

import (
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// RoutineCapture attests only the independently read pending operation. It is
// server-only metadata, not an applied receipt or a value decoded from tool JSON.
type RoutineCapture struct {
	OperationID uuid.UUID
	BotID       uuid.UUID
	SourceRunID uuid.UUID
	OwnerID     uuid.UUID
	Operation   store.BotControlOperation
	Status      store.BotOperationStatus
	Revision    int64
	ObservedAt  time.Time
}

func (c RoutineCapture) ValidFor(id execution.BotRunIdentity) bool {
	switch c.Operation {
	case store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget, store.BotControlResumeTasks, store.BotControlTaskOnceOperation:
	default:
		return false
	}
	return c.OperationID != uuid.Nil && c.BotID != uuid.Nil && c.SourceRunID != uuid.Nil && c.BotID == id.BotID && c.SourceRunID == id.RunID && c.OwnerID != uuid.Nil && c.OwnerID.String() == id.ActorID && c.Status == store.BotOperationPending && c.Revision > 0 && !c.ObservedAt.IsZero()
}

func (c RoutineCapture) Acknowledgment() string {
	return fmt.Sprintf("Routine operation %s (%s) is durably queued and pending, not applied. No routine change is verified by this turn. The worker processes it after the turn ends and publishes a separate committed result in this Bot's home chat.", c.OperationID, c.Operation)
}

func (c RoutineCapture) Observation() store.BotEvidence {
	return store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: c.Acknowledgment(), Source: "bot-operation://" + c.BotID.String() + "/" + c.OperationID.String(), ActorID: c.OwnerID, ObservedAt: c.ObservedAt}
}
