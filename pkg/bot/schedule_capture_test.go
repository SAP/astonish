package bot

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestScheduleCaptureLedgerSnapshot(t *testing.T) {
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
	for _, operation := range []store.BotControlOperation{store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlSchedulePause, store.BotControlScheduleRemove} {
		c := ScheduleCapture{OperationID: uuid.New(), BotID: id.BotID, SourceRunID: id.RunID, OwnerID: uuid.MustParse(id.ActorID), Operation: operation, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now()}
		if !c.ValidFor(id) || !ValidObservation(c.Observation()) {
			t.Fatal("valid capture rejected")
		}
		ctx, _ := WithToolOutcomes(context.Background())
		RecordToolOutcome(ctx, "call", ToolOutcome{Verified: true, ScheduleCapture: &c})
		c.Status = store.BotOperationApplied
		got, ok := TakeToolOutcome(ctx, "call")
		if !ok || !got.ScheduleCapture.ValidFor(id) {
			t.Fatal("ledger aliased mutable caller receipt")
		}
		c.Operation = store.BotControlOperation("unknown")
		if c.ValidFor(id) {
			t.Fatal("unknown operation accepted")
		}
	}
}
