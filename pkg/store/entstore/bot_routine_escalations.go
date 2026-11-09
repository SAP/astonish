package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"time"
)

// BotRoutineExceptionDecision binds one immutable operation, not a reusable role.
// Digest and revision must be rendered from ReadAuthorizedOperation by the caller.
type BotRoutineExceptionStore = store.BotRoutineExceptionStore

type BotRoutineExceptionDecision = store.BotRoutineExceptionDecision
type routineException struct {
	Ref       store.BotMandateRef
	ID        uuid.UUID
	Digest    string
	Resource  string
	ExpiresAt time.Time
	Actor     uuid.UUID
}

func (s *botAuthorizedOperationRuntime) DecideRoutineException(ctx context.Context, q BotRoutineExceptionDecision) (*store.BotOperation, error) {
	if _, ok := execution.BotRunIdentityFromContext(ctx); ok {
		return nil, store.ErrBotForbidden
	}
	var result *store.BotOperation
	err := s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		m, rec, err := s.verify(ctx, tx, q.MandateRef, execution.BotMandatePurposeTask)
		if err != nil {
			return err
		}
		actor, err := s.repo.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID || !verifiedMandateOwner(ctx, q.MandateRef.OwnerID, s.repo.clock()) {
			return store.ErrBotForbidden
		}
		o, err := readMandateOperation(ctx, tx, rec, q.MandateRef, q.OperationID)
		if err != nil {
			return err
		}
		if err = verifyRoutineGrant(ctx, tx, *o); err != nil {
			return err
		}
		if o.Provenance.PayloadDigest != q.PayloadDigest || o.Revision != q.ExpectedRevision {
			return store.ErrBotConflict
		}
		if o.Status != store.BotOperationBlocked || o.Decision != store.BotOperationRequireAuthorization {
			return store.ErrBotConflict
		}
		if o.Provenance.ExpectedRevision != rec.Bot.Revision || o.Provenance.Version != rec.Bot.Version {
			return store.ErrBotConflict
		}
		if q.Approve {
			policy, err := s.policy(botPolicyContext(ctx, tx), q.MandateRef)
			if err != nil {
				return err
			}
			if !routineAllowed(store.BotMandate{Constraints: policy.ExceptionConstraints}, o.Payload.Operation) {
				return store.ErrBotForbidden
			}
			if !q.ExpiresAt.After(s.repo.clock()) || q.ExpiresAt.After(s.repo.clock().Add(time.Hour)) || q.ExpiresAt.After(m.Mandate.ExpiresAt) {
				return store.ErrBotExpired
			}
			grant := routineException{Ref: q.MandateRef, ID: o.ID, Digest: q.PayloadDigest, Resource: RoutineControlResource(o.Payload.Operation), ExpiresAt: q.ExpiresAt, Actor: actor}
			if err = appendMandateRecord(ctx, tx, rec, "routine-exception:"+o.ID.String(), grant, s.repo.clock()); err != nil {
				return err
			}
			o.Exception = &store.BotOperationExceptionReference{MandateRef: q.MandateRef, OperationID: o.ID, PayloadDigest: q.PayloadDigest, Resource: grant.Resource, ExpiresAt: q.ExpiresAt}
			o.Status = store.BotOperationPending
			o.Decision = store.BotOperationExecute
		} else {
			policy, err := s.policy(botPolicyContext(ctx, tx), q.MandateRef)
			if err != nil {
				return err
			}
			rec.Metadata[routineSourceSuppressionKey(o.Payload, m.Mandate, policy, o.Provenance.SourceDigest)] = true
			o.Status = store.BotOperationCancelled
			o.Decision = store.BotOperationDeny
			o.Receipt = &store.BotOperationReceipt{Status: o.Status, ObservedAt: s.repo.clock(), Reason: "authorization_denied"}
		}
		o.Revision++
		result = o
		return saveAuthorizedOperation(ctx, tx, rec, *o, s)
	})
	return result, err
}
func routineSuppressionKey(payload store.BotControlPayload, mandate store.BotMandate, policy store.BotMandatePolicy) string {
	payload.Reason = "" // narration and unrelated Bot revisions are not authority
	if payload.Memory != nil {
		m := *payload.Memory
		m.ExpectedRevision = 0
		payload.Memory = &m
	}
	// Keep the action/ceiling fingerprint stable across unrelated Bot revisions,
	// mandate IDs and narration. The source-bound key below adds only verified
	// direct-owner provenance, not model-supplied context or tool-call IDs.
	allowed := routineAllowed(store.BotMandate{Constraints: policy.Constraints}, payload.Operation)
	digest, _ := botHash(struct {
		Payload       store.BotControlPayload
		Revision      int64
		MandateAllows bool
		PolicyAllows  bool
		Limits        store.BotMandateLimits
		PolicyLimits  store.BotMandateLimits
	}{payload, mandate.Ref.Revision, routineAllowed(mandate, payload.Operation), allowed, mandate.Limits, policy.Limits})
	return "routine-denied:" + digest
}

func (s *botAuthorizedOperationRuntime) verifyRoutineException(ctx context.Context, tx botDraftDB, o store.BotOperation) error {
	policy, err := s.policy(botPolicyContext(ctx, tx), o.MandateRef)
	if err != nil {
		return err
	}
	if !routineAllowed(store.BotMandate{Constraints: policy.ExceptionConstraints}, o.Payload.Operation) {
		return store.ErrBotForbidden
	}
	a, err := tx.FindActivity(ctx, o.MandateRef.BotID, "routine-exception:"+o.ID.String())
	if err != nil {
		return store.ErrBotForbidden
	}
	var e routineException
	if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &e) != nil || e.Ref != o.MandateRef || e.ID != o.ID || e.Digest != o.Provenance.PayloadDigest || e.Resource != RoutineControlResource(o.Payload.Operation) || e.Actor.String() != o.MandateRef.OwnerID || !e.ExpiresAt.After(s.repo.clock()) {
		return store.ErrBotForbidden
	}
	return nil
}
