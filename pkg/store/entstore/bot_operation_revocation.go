package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

// Delete revokes every issued reference, including historical and schedule refs.
// Pause deliberately does not use this path: lifecycle gates suspend execution
// while the mandate ceiling and cumulative accounting survive owner updates.
func (r *botRepository) revokeOperationStateTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord) error {
	refs := map[store.BotMandateRef]bool{}
	for after := int64(0); ; {
		rows, err := tx.ReadActivity(ctx, rec.Bot.ID, after, 200)
		if err != nil {
			return err
		}
		for _, row := range rows {
			after = row.Sequence
			if row.Kind != mandateRecordKind {
				continue
			}
			var m store.BotMandateRecord
			if json.Unmarshal(row.Payload, &m) == nil && m.Mandate.Ref.Validate() == nil {
				refs[m.Mandate.Ref] = true
			}
		}
		if len(rows) < 200 {
			break
		}
	}
	for ref := range refs {
		m, err := readMandate(ctx, tx, ref)
		if err != nil {
			return err
		}
		if m.RevokedAt == nil {
			if err = appendMandateRecord(ctx, tx, rec, mandateRevokedKey(ref), map[string]any{"ref": ref, "revoked": true}, r.clock()); err != nil {
				return err
			}
		}
		if err = r.cancelMandateOperationsTx(ctx, tx, rec, ref); err != nil {
			return err
		}
	}
	delete(rec.Metadata, mandateHeadKey)
	// Retain accounting until actual purge, including soft-delete/restore.
	return saveMandateMetadata(ctx, tx, rec)
}

func (r *botRepository) cancelMandateOperationsTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, ref store.BotMandateRef) error {
	rt := &botAuthorizedOperationRuntime{botMandateRuntime: &botMandateRuntime{repo: r}}
	for key, value := range rec.Metadata {
		if !strings.HasPrefix(key, "authorized-pending:") {
			continue
		}
		var candidate store.BotMandateRef
		raw, err := json.Marshal(value)
		if err != nil || json.Unmarshal(raw, &candidate) != nil {
			return store.ErrBotInvalid
		}
		if candidate != ref {
			continue
		}
		id, err := uuid.Parse(strings.TrimPrefix(key, "authorized-pending:"))
		if err != nil {
			return err
		}
		o, err := readMandateOperation(ctx, tx, rec, ref, id)
		if err != nil {
			return err
		}
		// Applying effects may already exist. Keep the exact operation/receipt and
		// pending marker for reconciliation, rather than claiming cancellation.
		if o.Status == store.BotOperationApplying {
			continue
		}
		if o.Status == store.BotOperationPending || o.Status == store.BotOperationBlocked {
			o.Status = store.BotOperationCancelled
			o.Revision++
			o.Receipt = nil
			if err = saveAuthorizedOperation(ctx, tx, rec, *o, rt); err != nil {
				return err
			}
		}
	}
	return saveMandateMetadata(ctx, tx, rec)
}
