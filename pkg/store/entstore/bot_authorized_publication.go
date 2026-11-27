package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Bind all immutable evidence; definitive failures need no success receipt.
func verifyPresentationOperation(ctx context.Context, tx botDB, rec *botStoredRecord, saved *botStoredRun, op *store.BotOperation) error {
	if saved.Run.MandateRef == nil || op.Validate() != nil || op.MandateRef != *saved.Run.MandateRef || op.Provenance == nil || op.Provenance.SourceRunID != saved.Run.ID || op.Provenance.UserEventID != saved.Run.UserEventID || op.Provenance.SourceUserID != rec.Bot.OwnerID.String() {
		return store.ErrBotForbidden
	}
	switch op.Status {
	case store.BotOperationBlocked, store.BotOperationFailed, store.BotOperationCancelled:
		return nil
	case store.BotOperationApplied:
		if scheduleOperation(op.Payload.Operation) {
			if op.Receipt == nil || op.Receipt.Schedule == nil || op.Receipt.Schedule.SourceRunID != saved.Run.ID {
				return store.ErrBotForbidden
			}
			return verifyAuthorizedGrant(ctx, tx, *op)
		}
		if !op.HasVerifiedMetadataReceipt() {
			return store.ErrBotForbidden
		}
		return verifyRoutineGrant(ctx, tx, *op)
	default:
		return store.ErrBotConflict
	}
}

// Select by reserved durable key and verified operation, not by model-controlled
// report fields. Ordinary source replies continue to use their original path.
func authorizedSchedulePublication(ctx context.Context, tx botDB, rec *botStoredRecord, run uuid.UUID, sequence int64) (bool, error) {
	rows, err := tx.ReadActivity(ctx, rec.Bot.ID, sequence-1, 1)
	if err != nil {
		return false, err
	}
	if len(rows) != 1 || rows[0].Sequence != sequence {
		return false, nil
	}
	var payload struct {
		OperationID       uuid.UUID       `json:"operation_id"`
		OperationRevision int64           `json:"operation_revision"`
		Report            store.BotReport `json:"report"`
		Status            store.RunStatus `json:"status"`
	}
	if json.Unmarshal(rows[0].Payload, &payload) != nil || payload.OperationID == uuid.Nil {
		return false, nil
	}
	a, err := tx.FindActivity(ctx, rec.Bot.ID, "authorized-schedule-receipt:"+payload.OperationID.String())
	routine := false
	if errors.Is(err, store.ErrBotNotFound) {
		a, err = tx.FindActivity(ctx, rec.Bot.ID, "authorized-routine-receipt:"+payload.OperationID.String())
		routine = true
	}
	if errors.Is(err, store.ErrBotNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.Sequence != sequence || a.RunID == nil || *a.RunID != run {
		return false, store.ErrBotForbidden
	}
	source, err := tx.ReadRun(ctx, rec.Bot.ID, run)
	if err != nil {
		return false, err
	}
	if source.Run.MandateRef == nil {
		return false, store.ErrBotForbidden
	}
	o, err := readMandateOperation(ctx, tx, rec, *source.Run.MandateRef, payload.OperationID)
	if err != nil {
		return false, err
	}
	if m := payload.Report.EmployeeMessage; m != nil {
		// Wording is not authority: bind the reserved receipt to the exact
		// immutable operation revision and independently verify its grant below.
		if payload.Report.ValidateEmployeeMessageIntegrity(payload.Status) != nil || m.Origin != store.BotEmployeeMessageOperation || m.OperationID != o.ID || m.OperationRevision != o.Revision || payload.OperationRevision != o.Revision {
			return false, store.ErrBotInvalid
		}
	}
	if routine {
		if !o.HasVerifiedMetadataReceipt() || o.Provenance.SourceRunID != run || o.Provenance.UserEventID != source.Run.UserEventID || o.Provenance.SourceUserID != rec.Bot.OwnerID.String() {
			return false, store.ErrBotForbidden
		}
		if err := verifyRoutineGrant(ctx, tx, *o); err != nil {
			return false, err
		}
		return true, nil
	}
	if o.Status != store.BotOperationApplied || o.Receipt == nil || o.Receipt.Schedule == nil || o.Receipt.Schedule.SourceRunID != run {
		return false, store.ErrBotForbidden
	}
	if err = verifyAuthorizedGrant(ctx, tx, *o); err != nil {
		return false, err
	}
	return true, nil
}
