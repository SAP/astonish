package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestRoutineCaptureLedgerSnapshot(t *testing.T) {
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
	for _, op := range []store.BotControlOperation{store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget, store.BotControlResumeTasks, store.BotControlTaskOnceOperation} {
		c := RoutineCapture{OperationID: uuid.New(), BotID: id.BotID, SourceRunID: id.RunID, OwnerID: uuid.MustParse(id.ActorID), Operation: op, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now()}
		if !c.ValidFor(id) || !ValidObservation(c.Observation()) || !strings.Contains(c.Acknowledgment(), string(op)) {
			t.Fatalf("valid receipt rejected: %+v", c)
		}
		ctx, _ := WithToolOutcomes(context.Background())
		RecordToolOutcome(ctx, "call", ToolOutcome{Verified: true, RoutineCapture: &c})
		c.Status = store.BotOperationApplied
		got, ok := TakeToolOutcome(ctx, "call")
		if !ok || !got.RoutineCapture.ValidFor(id) {
			t.Fatal("receipt aliases caller")
		}
		if _, ok = TakeToolOutcome(ctx, "call"); ok {
			t.Fatal("receipt consumed twice")
		}
		for _, bad := range []string{"owner", "run", "revision", "operation", "applied"} {
			invalid := *got.RoutineCapture
			switch bad {
			case "owner":
				invalid.OwnerID = uuid.New()
			case "run":
				invalid.SourceRunID = uuid.New()
			case "revision":
				invalid.Revision = 0
			case "operation":
				invalid.Operation = store.BotControlActivate
			case "applied":
				invalid.Status = store.BotOperationApplied
			}
			if invalid.ValidFor(id) {
				t.Fatalf("accepted %s", bad)
			}
		}
	}
}
