package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotOperationStore = (*botMandateRuntime)(nil)

func operationKey(id uuid.UUID, revision int64) string {
	return fmt.Sprintf("mandate-operation:%s:%d", id, revision)
}
func operationHeadKey(id uuid.UUID) string { return "mandate_operation_head:" + id.String() }
func readMandateOperation(ctx context.Context, tx botDraftDB, rec *botStoredRecord, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	raw, ok := rec.Metadata[operationHeadKey(id)]
	if !ok {
		return nil, store.ErrBotNotFound
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var rev int64
	if err = json.Unmarshal(b, &rev); err != nil {
		return nil, err
	}
	a, err := tx.FindActivity(ctx, ref.BotID, operationKey(id, rev))
	if err != nil {
		return nil, err
	}
	var o store.BotOperation
	if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &o) != nil || o.ID != id || o.MandateRef != ref || o.Revision != rev || o.Validate() != nil {
		return nil, store.ErrBotInvalid
	}
	return &o, nil
}
func (s *botMandateRuntime) ReadBotOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	rec, err := s.repo.mandateScope(ctx, s.repo.db, ref)
	if err != nil {
		return nil, err
	}
	return readMandateOperation(ctx, s.repo.db, rec, ref, id)
}
func (s *botMandateRuntime) CreateBotOperation(ctx context.Context, o store.BotOperation) (*store.BotOperation, error) {
	if o.Provenance != nil {
		return nil, store.ErrBotForbidden
	}
	if o.Validate() != nil || o.Status != store.BotOperationPending || o.Revision != 1 || o.Receipt != nil {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotOperation
	err := s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		m, err := readMandate(ctx, tx, o.MandateRef)
		if err != nil {
			return err
		}
		_, rec, err := s.verify(ctx, tx, o.MandateRef, m.Mandate.Purpose)
		if err != nil {
			return err
		}
		// The unique key binds caller idempotency to exact immutable operation content.
		digest, err := botHash(o)
		if err != nil {
			return err
		}
		dedup := uuid.NewSHA1(o.MandateRef.ID, []byte(o.IdempotencyKey))
		key := "mandate-operation-key:" + dedup.String()
		if a, err := tx.FindActivity(ctx, o.MandateRef.BotID, key); err == nil {
			var prior struct {
				ID     uuid.UUID
				Digest string
			}
			if json.Unmarshal(a.Payload, &prior) != nil || prior.Digest != digest {
				return store.ErrBotConflict
			}
			result, err = readMandateOperation(ctx, tx, rec, o.MandateRef, prior.ID)
			return err
		} else if !errors.Is(err, store.ErrBotNotFound) {
			return err
		}
		if _, exists := rec.Metadata[operationHeadKey(o.ID)]; exists {
			return store.ErrBotConflict
		}
		if err = appendMandateRecord(ctx, tx, rec, key, struct {
			ID     uuid.UUID
			Digest string
		}{o.ID, digest}, s.repo.clock()); err != nil {
			return err
		}
		if err = appendMandateRecord(ctx, tx, rec, operationKey(o.ID, o.Revision), o, s.repo.clock()); err != nil {
			return err
		}
		if rec.Metadata == nil {
			rec.Metadata = map[string]any{}
		}
		rec.Metadata[operationHeadKey(o.ID)] = o.Revision
		if err = saveMandateMetadata(ctx, tx, rec); err != nil {
			return err
		}
		copy, err := o.Clone()
		result = &copy
		return err
	})
	return result, err
}
func (s *botMandateRuntime) TransitionBotOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID, revision int64, next store.BotOperationStatus, receipt store.BotOperationReceipt) (*store.BotOperation, error) {
	var result *store.BotOperation
	err := s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		m, err := readMandate(ctx, tx, ref)
		if err != nil {
			return err
		}
		_, rec, err := s.verify(ctx, tx, ref, m.Mandate.Purpose)
		if err != nil {
			return err
		}
		o, err := readMandateOperation(ctx, tx, rec, ref, id)
		if err != nil {
			return err
		}
		if o.Provenance != nil {
			return store.ErrBotForbidden
		}
		if o.Revision != revision || !o.Status.CanTransitionTo(next) {
			return store.ErrBotConflict
		}
		o.Status = next
		o.Revision++
		o.Receipt = &receipt
		if err = o.Validate(); err != nil {
			return err
		}
		if err = appendMandateRecord(ctx, tx, rec, operationKey(id, o.Revision), o, s.repo.clock()); err != nil {
			return err
		}
		rec.Metadata[operationHeadKey(id)] = o.Revision
		if err = saveMandateMetadata(ctx, tx, rec); err != nil {
			return err
		}
		result = o
		return nil
	})
	return result, err
}
