package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type routineTaskKey struct{}
type routineTaskBinding struct {
	Ref          store.BotMandateRef
	OperationID  uuid.UUID
	Instructions string
}

// A task reuses its parent's finite ceiling and aggregate quota, never a new
// lifetime. Only this immutable binding can attach a second run to that mandate.
func verifyRoutineTaskRun(ctx context.Context, tx botDraftDB, rec *botStoredRecord, m *store.BotMandateRecord, run *botStoredRun) error {
	if run.Run.ID.String() == m.Provenance.RunID {
		return nil
	}
	a, err := tx.FindActivity(ctx, rec.Bot.ID, "routine-task-run:"+run.Run.ID.String())
	if err != nil {
		return store.ErrBotForbidden
	}
	var b routineTaskBinding
	if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &b) != nil || run.Run.MandateRef == nil || b.Ref != *run.Run.MandateRef || b.Instructions != run.Instructions || run.Run.ID != uuid.NewSHA1(b.OperationID, []byte("run")) {
		return store.ErrBotForbidden
	}
	o, err := readMandateOperation(ctx, tx, rec, b.Ref, b.OperationID)
	if err != nil {
		return err
	}
	if o.Status != store.BotOperationApplied || o.Payload.TaskOnce == nil {
		return store.ErrBotForbidden
	}
	task := o.Payload.TaskOnce
	instructions := task.Instructions
	if task.SkillID != nil {
		instructions = task.Invocation
	}
	if instructions != b.Instructions || !sameSkillID(task.SkillID, run.Run.SkillID) || task.Invocation != run.Run.Invocation {
		return store.ErrBotForbidden
	}
	return verifyRoutineGrant(ctx, tx, *o)
}
