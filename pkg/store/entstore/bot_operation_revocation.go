package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

// Delete revokes historical and scheduled authority without opening content.
// SQL tombstones survive restore. Applying operations retain their exact head,
// pending marker and receipt; deletion is not evidence of an absent effect.
func (r *botRepository) revokeOperationStateTx(ctx context.Context, tx botDB, rec *botStoredRecord) error {
	refs := map[store.BotMandateRef]bool{}
	operations := map[uuid.UUID]botCleanupAuthority{}
	for after := int64(0); after >= 0; {
		rows, next, err := readCleanupAuthorities(ctx, tx, rec.Bot.ID, after)
		if err != nil {
			return err
		}
		after = next
		for _, row := range rows {
			a := row.Authority
			if strings.HasPrefix(row.Key, "mandate:") && (a.Mandate == nil || a.Mandate.Ref.ID == uuid.Nil || a.ID != uuid.Nil) {
				return store.ErrBotInvalid
			}
			if strings.HasPrefix(row.Key, "mandate-operation:") && (a.ID == uuid.Nil || a.Mandate != nil) {
				return store.ErrBotInvalid
			}
			if a.Mandate != nil && a.Mandate.Ref.ID != uuid.Nil {
				ref := a.Mandate.Ref
				if ref.Validate() != nil || ref.BotID != rec.Bot.ID || ref.OwnerID != rec.Bot.OwnerID.String() || ref.OrgSlug != r.auth.orgSlug || r.auth.teamSlug != "" && ref.TeamSlug != r.auth.teamSlug || row.Key != mandateKey(ref) {
					return store.ErrBotInvalid
				}
				refs[ref] = true
			}
			if a.ID != uuid.Nil {
				if a.Revision < 1 || a.MandateRef.Validate() != nil || a.MandateRef.BotID != rec.Bot.ID || a.MandateRef.OwnerID != rec.Bot.OwnerID.String() || row.Key != operationKey(a.ID, a.Revision) {
					return store.ErrBotInvalid
				}
				if old, ok := operations[a.ID]; !ok || old.Revision < a.Revision {
					operations[a.ID] = *a
				}
			}
		}
	}
	for ref := range refs {
		exists, err := cleanupRevokedExists(ctx, tx, ref)
		if err != nil {
			return err
		}
		if !exists {
			if err = appendMandateRecord(ctx, tx, rec, mandateRevokedKey(ref), map[string]any{"ref": ref, "revoked": true}, r.clock()); err != nil {
				return err
			}
		}
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	for id, a := range operations {
		if !refs[a.MandateRef] {
			return store.ErrBotInvalid
		}
		raw, err := json.Marshal(rec.Metadata[operationHeadKey(id)])
		if err != nil {
			return err
		}
		var revision int64
		if json.Unmarshal(raw, &revision) != nil || revision != a.Revision {
			return store.ErrBotInvalid
		}
		switch a.Status {
		case store.BotOperationPending, store.BotOperationBlocked:
			a.Status = store.BotOperationCancelled
			a.Revision++
			rec.Metadata[botOperationCleanupPrefix+id.String()] = a
			delete(rec.Metadata, "authorized-pending:"+id.String())
		case store.BotOperationApplying:
		case store.BotOperationApplied, store.BotOperationFailed, store.BotOperationCancelled:
		default:
			return store.ErrBotInvalid
		}
	}
	for key := range rec.Metadata {
		if strings.HasPrefix(key, "mandate_operation_head:") {
			id, err := uuid.Parse(strings.TrimPrefix(key, "mandate_operation_head:"))
			if err != nil {
				return store.ErrBotInvalid
			}
			if _, ok := operations[id]; !ok {
				return store.ErrBotInvalid
			}
		}
		if strings.HasPrefix(key, "authorized-pending:") {
			id, err := uuid.Parse(strings.TrimPrefix(key, "authorized-pending:"))
			if err != nil {
				return store.ErrBotInvalid
			}
			if _, ok := operations[id]; !ok {
				return store.ErrBotInvalid
			}
		}
	}
	delete(rec.Metadata, mandateHeadKey)
	return saveMandateMetadata(ctx, tx, rec)
}

// Explicit single-mandate revocation still uses validated current content. This
// path is not the legacy deletion boundary.
func (r *botRepository) cancelMandateOperationsTx(ctx context.Context, tx botDB, rec *botStoredRecord, ref store.BotMandateRef) error {
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
