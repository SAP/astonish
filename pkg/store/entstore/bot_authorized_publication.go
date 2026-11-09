package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Select by reserved durable key and verified operation, not by model-controlled
// report fields. Ordinary source replies continue to use their original path.
func authorizedSchedulePublication(ctx context.Context, tx botDraftDB, rec *botStoredRecord, run uuid.UUID, sequence int64) (bool, error) {
	rows, err := tx.ReadActivity(ctx, rec.Bot.ID, sequence-1, 1)
	if err != nil {
		return false, err
	}
	if len(rows) != 1 || rows[0].Sequence != sequence {
		return false, nil
	}
	var payload struct {
		OperationID uuid.UUID `json:"operation_id"`
	}
	if json.Unmarshal(rows[0].Payload, &payload) != nil || payload.OperationID == uuid.Nil {
		return false, nil
	}
	a, err := tx.FindActivity(ctx, rec.Bot.ID, "authorized-schedule-receipt:"+payload.OperationID.String())
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
	if o.Status != store.BotOperationApplied || o.Receipt == nil || o.Receipt.Schedule == nil || o.Receipt.Schedule.SourceRunID != run {
		return false, store.ErrBotForbidden
	}
	if err = verifyAuthorizedGrant(ctx, tx, *o); err != nil {
		return false, err
	}
	return true, nil
}

// canonicalScheduleOperationEvent suppresses only the exact applied revision
// named by a reserved, verified receipt. Old receipts keep their fallback.
func canonicalScheduleOperationEvent(ctx context.Context, tx botDraftDB, rec *botStoredRecord, run uuid.UUID, sequence int64) (string, error) {
	rows, err := tx.ReadActivity(ctx, rec.Bot.ID, sequence-1, 1)
	if err != nil {
		return "", err
	}
	if len(rows) != 1 {
		return "", nil
	}
	var p struct {
		OperationID uuid.UUID `json:"operation_id"`
		Revision    int64     `json:"operation_revision"`
	}
	if json.Unmarshal(rows[0].Payload, &p) != nil || p.Revision < 1 {
		return "", nil
	}
	var reader store.BotTranscriptReader
	switch d := tx.(type) {
	case personalBotDB:
		reader = &personalSessionStore{client: d.client}
	case teamBotDB:
		reader = &teamSessionStore{client: d.client}
	default:
		return "", store.ErrBotUnsupported
	}
	var cursor int64
	for {
		events, err := reader.ReadBotTranscript(ctx, rec.Bot.HomeSessionID, cursor, 200)
		if err != nil {
			return "", err
		}
		for _, row := range events {
			cursor = row.Cursor
			if row.Event == nil {
				continue
			}
			raw, err := json.Marshal(row.Event.Actions.StateDelta[store.BotOperationKey])
			if err != nil {
				continue
			}
			var v store.BotOperationView
			if json.Unmarshal(raw, &v) != nil || v.Validate() != nil || v.BotID != rec.Bot.ID || v.HomeSessionID != rec.Bot.HomeSessionID || v.OperationID != p.OperationID || v.Revision != p.Revision || v.Status != store.BotOperationApplied || v.SourceRunID != run {
				continue
			}
			canonical, err := store.NewBotOperationEvent(v, row.Event.Timestamp)
			if err == nil && canonical.ID == row.Event.ID {
				return canonical.ID, nil
			}
		}
		if len(events) < 200 {
			return "", nil
		}
	}
}
