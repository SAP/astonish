package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"strings"
)

type botAuthorizedOperationRuntime struct {
	*botMandateRuntime
	authorize store.BotScheduleAuthorizer
	readiness store.BotScheduleReadiness
}

func NewBotAuthorizedOperationRuntime(bots store.BotStore, policy store.BotMandatePolicyResolver, authorize store.BotScheduleAuthorizer, readiness store.BotScheduleReadiness) (store.BotAuthorizedOperationStore, error) {
	if authorize == nil || readiness == nil {
		return nil, store.ErrBotForbidden
	}
	rt, err := NewBotMandateRuntime(bots, policy)
	if err != nil {
		return nil, err
	}
	return &botAuthorizedOperationRuntime{rt.(*botMandateRuntime), authorize, readiness}, nil
}
func scheduleOperation(op store.BotControlOperation) bool {
	switch op {
	case store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlSchedulePause, store.BotControlScheduleRemove:
		return true
	}
	return false
}
func authorizedGrantKey(id uuid.UUID) string { return "authorized-schedule-grant:" + id.String() }
func (s *botAuthorizedOperationRuntime) ReadAuthorizedOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	return s.ReadBotOperation(ctx, ref, id)
}
func (s *botAuthorizedOperationRuntime) ListAuthorizedOperations(ctx context.Context, ref store.BotMandateRef) ([]store.BotOperation, error) {
	rec, err := s.repo.mandateScope(ctx, s.repo.db, ref)
	if err != nil {
		return nil, err
	}
	out := []store.BotOperation{}
	for key := range rec.Metadata {
		if !strings.HasPrefix(key, "mandate_operation_head:") {
			continue
		}
		id, err := uuid.Parse(strings.TrimPrefix(key, "mandate_operation_head:"))
		if err != nil {
			return nil, err
		}
		o, err := readMandateOperation(ctx, s.repo.db, rec, ref, id)
		if errors.Is(err, store.ErrBotInvalid) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if o.Provenance != nil {
			out = append(out, *o)
		}
	}
	return out, nil
}
func (s *botAuthorizedOperationRuntime) source(ctx context.Context, tx botDraftDB, ref store.BotMandateRef, payload store.BotControlPayload) (*store.BotMandateRecord, *botStoredRecord, *botStoredRun, store.BotOperationProvenance, error) {
	record, err := readMandate(ctx, tx, ref)
	if err != nil {
		return nil, nil, nil, store.BotOperationProvenance{}, err
	}
	purpose := record.Mandate.Purpose
	if purpose != execution.BotMandatePurposeTask && (purpose != execution.BotMandatePurposeOperation || !routineOperation(payload.Operation) || payload.Operation == store.BotControlTaskOnceOperation) {
		return nil, nil, nil, store.BotOperationProvenance{}, store.ErrBotForbidden
	}
	m, rec, err := s.verify(ctx, tx, ref, purpose)
	if err != nil {
		return nil, nil, nil, store.BotOperationProvenance{}, err
	}
	id, err := uuid.Parse(m.Provenance.RunID)
	if err != nil {
		return nil, nil, nil, store.BotOperationProvenance{}, err
	}
	run, err := tx.ReadRun(ctx, ref.BotID, id)
	if err != nil {
		return nil, nil, nil, store.BotOperationProvenance{}, err
	}
	if m.Provenance.InitiationKind != store.BotInitiationKindDirectOwnerMessage || run.Run.Trigger != store.TriggerManual || strings.TrimSpace(run.Instructions) == "" {
		return nil, nil, nil, store.BotOperationProvenance{}, store.ErrBotForbidden
	}
	digest, err := botHash(payload)
	if err != nil {
		return nil, nil, nil, store.BotOperationProvenance{}, err
	}
	p := store.BotOperationProvenance{SourceRunID: id, SourceUserID: ref.OwnerID, UserEventID: m.Provenance.UserEventID, SourceDigest: m.Provenance.SourceDigest, PayloadDigest: digest, Purpose: execution.BotMandatePurposeSchedule, Version: rec.Bot.Version, ExpectedRevision: rec.Bot.Revision}
	return m, rec, run, p, nil
}
func (s *botAuthorizedOperationRuntime) EnqueueAuthorizedOperation(ctx context.Context, q store.BotAuthorizedOperationRequest) (*store.BotOperation, error) {
	if routineOperation(q.Payload.Operation) {
		return s.enqueueRoutineOperation(ctx, q)
	}
	if q.MandateRef.BotID != q.Lease.BotID || strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 || !scheduleOperation(q.Payload.Operation) {
		return nil, store.ErrBotInvalid
	}
	if err := q.Payload.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.repo.checkLease(ctx, s.repo.db, q.Lease, s.repo.clock()); err != nil {
		return nil, err
	}
	// Authorizer may perform semantic IO, so never call it while SQLite is locked.
	m, before, source, provenance, err := s.source(ctx, s.repo.db, q.MandateRef, q.Payload)
	if err != nil {
		return nil, err
	}
	if source.Run.ID != q.Lease.RunID {
		return nil, store.ErrBotForbidden
	}
	if err = s.authorize(ctx, store.BotScheduleAuthorization{InitiationKind: m.Provenance.InitiationKind, Mandate: m.Mandate.Clone(), Provenance: provenance, Instructions: source.Instructions, Payload: q.Payload}); err != nil {
		return nil, err
	}
	id := uuid.NewSHA1(q.Lease.RunID, []byte("authorized-schedule:"+q.IdempotencyKey))
	var result *store.BotOperation
	err = s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		if _, err := s.repo.checkLease(ctx, tx, q.Lease, s.repo.clock()); err != nil {
			return err
		}
		current, rec, run, p, err := s.source(ctx, tx, q.MandateRef, q.Payload)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(m, current) || !reflect.DeepEqual(before.Bot, rec.Bot) || !reflect.DeepEqual(source, run) || p != provenance {
			return store.ErrBotConflict
		}
		if old, err := readMandateOperation(ctx, tx, rec, q.MandateRef, id); err == nil {
			if old.Provenance == nil || old.Provenance.PayloadDigest != p.PayloadDigest || old.Provenance.SourceDigest != p.SourceDigest {
				return store.ErrBotConflict
			}
			result = old
			return nil
		} else if !errors.Is(err, store.ErrBotNotFound) {
			return err
		}
		if q.Payload.Schedule.ExpectedRevision != rec.Bot.Revision {
			return store.ErrBotConflict
		}
		o := store.BotOperation{ID: id, MandateRef: q.MandateRef, IdempotencyKey: q.IdempotencyKey, Payload: q.Payload, Provenance: &p, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Revision: 1}
		if _, _, err := s.scheduleCandidate(ctx, tx, rec, o, current.Mandate.Limits); err != nil {
			return err
		}
		if err := o.Validate(); err != nil {
			return err
		}
		// Immutable issuer grant is independent of the generic operation API.
		if err := appendMandateRecord(ctx, tx, rec, authorizedGrantKey(id), p, s.repo.clock()); err != nil {
			return err
		}
		if err := saveAuthorizedOperation(ctx, tx, rec, o, s); err != nil {
			return err
		}
		result = &o
		return nil
	})
	return result, err
}
func saveAuthorizedOperation(ctx context.Context, tx botDraftDB, rec *botStoredRecord, o store.BotOperation, s *botAuthorizedOperationRuntime) error {
	if err := appendMandateRecord(ctx, tx, rec, operationKey(o.ID, o.Revision), o, s.repo.clock()); err != nil {
		return err
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	rec.Metadata[operationHeadKey(o.ID)] = o.Revision
	key := "authorized-pending:" + o.ID.String()
	if o.Status == store.BotOperationPending || o.Status == store.BotOperationBlocked || o.Status == store.BotOperationApplying {
		rec.Metadata[key] = o.MandateRef
	} else {
		delete(rec.Metadata, key)
		delete(rec.Metadata, "authorized-retry:"+o.ID.String())
	}
	return saveMandateMetadata(ctx, tx, rec)
}
func verifyAuthorizedGrant(ctx context.Context, tx botDraftDB, o store.BotOperation) error {
	if o.Provenance == nil || !scheduleOperation(o.Payload.Operation) || o.Provenance.Purpose != execution.BotMandatePurposeSchedule {
		return store.ErrBotForbidden
	}
	a, err := tx.FindActivity(ctx, o.MandateRef.BotID, authorizedGrantKey(o.ID))
	if err != nil {
		return err
	}
	var p store.BotOperationProvenance
	digest, err := botHash(o.Payload)
	if err != nil {
		return err
	}
	if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &p) != nil || p != *o.Provenance || digest != p.PayloadDigest {
		return store.ErrBotForbidden
	}
	return nil
}
