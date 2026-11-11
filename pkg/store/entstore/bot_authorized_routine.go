package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"time"
)

// Routine controls are deliberately a closed set: configuration, budget, access,
// activation and broad resume are not routine, regardless of charter text.
type routineMutationKey struct{}

func routineOperation(op store.BotControlOperation) bool {
	switch op {
	case store.BotControlPriorityPause, store.BotControlResumeTasks, store.BotControlTaskOnceOperation, store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget:
		return true
	}
	return false
}

// RoutineControlResource is the exact tool-execution resource required in the
// issuer's mandate. A charter/persona change never edits this authority ceiling.
func RoutineControlResource(op store.BotControlOperation) string {
	if !routineOperation(op) {
		return ""
	}
	return "bot_control:" + string(op)
}
func routineAllowed(m store.BotMandate, op store.BotControlOperation) bool {
	for _, c := range m.Constraints {
		if c.Action == execution.CapabilityToolExecute {
			for _, r := range c.Resources {
				if r == RoutineControlResource(op) {
					return true
				}
			}
		}
	}
	return false
}
func routineGrantKey(id uuid.UUID) string { return "authorized-routine-grant:" + id.String() }
func verifyRoutineGrant(ctx context.Context, tx botDraftDB, o store.BotOperation) error {
	if o.Provenance == nil || !routineOperation(o.Payload.Operation) || (o.Provenance.Purpose != execution.BotMandatePurposeTask && o.Provenance.Purpose != execution.BotMandatePurposeOperation) {
		return store.ErrBotForbidden
	}
	a, err := tx.FindActivity(ctx, o.MandateRef.BotID, routineGrantKey(o.ID))
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
func (s *botAuthorizedOperationRuntime) enqueueRoutineOperation(ctx context.Context, q store.BotAuthorizedOperationRequest) (*store.BotOperation, error) {
	if q.Lease.BotID != q.MandateRef.BotID || strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 {
		return nil, store.ErrBotInvalid
	}
	if err := q.Payload.Validate(); err != nil {
		return nil, err
	}
	// Match the apply-time required goal text before persisting an immutable
	// grant. A description is not completion criteria; let the source turn
	// correct missing input rather than cancelling its operation after it ends.
	if q.Payload.Operation == store.BotControlGoalCreate || q.Payload.Operation == store.BotControlGoalEdit {
		if strings.TrimSpace(q.Payload.Goal.Goal.Title) == "" {
			return nil, fmt.Errorf("%w: goal.goal.title must be non-empty", store.ErrBotInvalid)
		}
		if strings.TrimSpace(q.Payload.Goal.Goal.CompletionCriteria) == "" {
			return nil, fmt.Errorf("%w: goal.goal.completion_criteria must be non-empty and reflect the owner's requested outcome", store.ErrBotInvalid)
		}
	}
	if _, err := s.repo.checkLease(ctx, s.repo.db, q.Lease, s.repo.clock()); err != nil {
		return nil, err
	}
	m, _, source, p, err := s.source(ctx, s.repo.db, q.MandateRef, q.Payload)
	if err != nil {
		return nil, err
	}
	if source.Run.ID != q.Lease.RunID {
		return nil, store.ErrBotForbidden
	}
	p.Purpose = m.Mandate.Purpose
	// Canonical source, not caller-produced source claims, reaches the trusted authorizer.
	if err = s.authorize(ctx, store.BotScheduleAuthorization{InitiationKind: m.Provenance.InitiationKind, Mandate: m.Mandate.Clone(), Provenance: p, Instructions: source.Instructions, Payload: q.Payload}); err != nil {
		return nil, err
	}
	id := uuid.NewSHA1(q.Lease.RunID, []byte("authorized-routine:"+q.IdempotencyKey))
	var result *store.BotOperation
	err = s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		if _, err := s.repo.checkLease(ctx, tx, q.Lease, s.repo.clock()); err != nil {
			return err
		}
		current, rec, run, prov, err := s.source(ctx, tx, q.MandateRef, q.Payload)
		if err != nil {
			return err
		}
		prov.Purpose = current.Mandate.Purpose
		if prov != p || run.Run.ID != q.Lease.RunID {
			return store.ErrBotConflict
		}
		if old, e := readMandateOperation(ctx, tx, rec, q.MandateRef, id); e == nil {
			if old.Provenance == nil || old.Provenance.PayloadDigest != p.PayloadDigest {
				return store.ErrBotConflict
			}
			result = old
			return nil
		} else if !errors.Is(e, store.ErrBotNotFound) {
			return e
		}
		if q.Payload.Operation == store.BotControlResumeTasks {
			if err := s.validateResumeCaptureTargets(ctx, tx, rec, q.Payload.ResumeTargets); err != nil {
				return err
			}
		}
		policy, err := s.policy(botPolicyContext(ctx, tx), q.MandateRef)
		if err != nil {
			return err
		}
		denied, err := routineSourceSuppressed(ctx, tx, rec, q.Payload, current.Mandate, policy, prov.SourceDigest)
		if err != nil {
			return err
		}
		if denied {
			return store.ErrBotForbidden
		}
		// Fingerprints must be captured by the tool's read, never silently broadened.
		if q.Payload.Memory != nil {
			if q.Payload.Operation != store.BotControlMemoryRemember && q.Payload.Memory.ExpectedFingerprint == "" {
				return store.ErrBotInvalid
			}
			if err = prepareControlMemory(ctx, tx, rec, &q.Payload); err != nil {
				return err
			}
		}
		decision, status := store.BotOperationExecute, store.BotOperationPending
		if !routineAllowed(current.Mandate, q.Payload.Operation) {
			decision, status = store.BotOperationRequireAuthorization, store.BotOperationBlocked
		}
		o := store.BotOperation{ID: id, MandateRef: q.MandateRef, IdempotencyKey: q.IdempotencyKey, Payload: q.Payload, Provenance: &prov, Status: status, Decision: decision, Revision: 1}
		if status == store.BotOperationBlocked && decision == store.BotOperationRequireAuthorization {
			expiry := s.repo.clock().Add(time.Hour)
			if current.Mandate.ExpiresAt.Before(expiry) {
				expiry = current.Mandate.ExpiresAt
			}
			o.Exception = &store.BotOperationExceptionReference{MandateRef: q.MandateRef, OperationID: id, PayloadDigest: prov.PayloadDigest, ExpiresAt: expiry}
		}
		if err = o.Validate(); err != nil {
			return err
		}
		if err = appendMandateRecord(ctx, tx, rec, routineGrantKey(id), prov, s.repo.clock()); err != nil {
			return err
		}
		if o.Payload.Operation == store.BotControlPriorityPause && o.Decision == store.BotOperationExecute {
			if err = s.pauseRoutineTx(ctx, tx, rec, &o); err != nil {
				return err
			}
		} else if err = saveAuthorizedOperation(ctx, tx, rec, o, s); err != nil {
			return err
		}
		result = &o
		return nil
	})
	return result, err
}
func (s *botAuthorizedOperationRuntime) applyRoutineOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	var result *store.BotOperation
	err := s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := s.repo.mandateScope(ctx, tx, ref)
		if err != nil {
			return err
		}
		o, err := readMandateOperation(ctx, tx, rec, ref, id)
		if err != nil {
			return err
		}
		if err = verifyRoutineGrant(ctx, tx, *o); err != nil {
			return err
		}
		result = o
		if o.Status == store.BotOperationApplied {
			return nil
		}
		if o.Status != store.BotOperationPending || o.Decision != store.BotOperationExecute {
			return store.ErrBotConflict
		}
		m, _, run, p, err := s.source(ctx, tx, ref, o.Payload)
		if err != nil {
			return err
		}
		p.Purpose = m.Mandate.Purpose
		if p != *o.Provenance {
			return store.ErrBotConflict
		}
		if !routineAllowed(m.Mandate, o.Payload.Operation) {
			if err = s.verifyRoutineException(ctx, tx, *o); err != nil {
				return err
			}
		}
		if rec.ActiveRunID != nil || !run.Run.Status.Terminal() {
			return store.ErrBotBusy
		}
		// Reuse mutation primitives, NOT proposal confirmation or a human decision record.
		proposal := store.BotControlProposal{ID: o.ID, BotID: ref.BotID, SourceRunID: p.SourceRunID, SourceUserID: rec.Bot.OwnerID, Version: p.Version, ExpectedRevision: p.ExpectedRevision, Payload: o.Payload}
		mutationCtx := context.WithValue(ctx, routineMutationKey{}, true)
		if o.Payload.Operation == store.BotControlTaskOnceOperation {
			mutationCtx = context.WithValue(mutationCtx, routineTaskKey{}, ref)
		}
		if o.Payload.Operation == store.BotControlResumeTasks {
			err = s.resumeRoutineTx(ctx, tx, rec, *o)
		} else if o.Payload.Operation == store.BotControlPriorityPause {
			return s.pauseRoutineTx(ctx, tx, rec, o)
		} else {
			err = s.repo.applyControlProposal(mutationCtx, tx, rec, proposal, rec.Bot.OwnerID)
		}
		if err != nil {
			return err
		}
		rec, err = tx.GetBot(ctx, ref.BotID)
		if err != nil {
			return err
		}
		o.Status = store.BotOperationApplied
		o.Revision++
		o.Receipt = &store.BotOperationReceipt{Status: o.Status, ObservedAt: s.repo.clock(), Postconditions: []store.BotOperationPostcondition{{Resource: RoutineControlResource(o.Payload.Operation), Revision: rec.Bot.Revision, Verified: true}}}
		if err := saveAuthorizedOperation(ctx, tx, rec, *o, s); err != nil {
			return err
		}
		if !o.HasVerifiedMetadataReceipt() {
			return nil
		}
		report, err := store.BotOperationEmployeeReport(run.Run.Status, *o)
		if err != nil {
			return err
		}
		// Atomic publication intent reuses the already committed canonical operation
		// event; it does not publish a second prose acknowledgment.
		return insertPublicationActivity(ctx, tx, rec, run.Run.ID, store.BotActivityReport, "authorized-routine-receipt:"+id.String(), map[string]any{"run_id": run.Run.ID, "status": run.Run.Status, "report": report, "operation_id": id, "operation_revision": o.Revision, "intent": store.BotOutboxPublish}, s.repo.clock())
	})
	if err != nil {
		// Never return an Applied in-memory receipt for a rolled-back transaction.
		return nil, err
	}
	return result, nil
}
