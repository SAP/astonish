package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"sort"
	"strings"
	"time"
)

// ReconcileAuthorizedOperations inspects at most limit issuer-owned pending
// entries, never the conversation transcript. Retry timestamps survive restart.
func (s *botAuthorizedOperationRuntime) ReconcileAuthorizedOperations(ctx context.Context, id store.BotID, limit int) error {
	if limit < 1 || limit > 100 {
		return store.ErrBotInvalid
	}
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	actor, err := s.repo.auth.authorize(ctx, b.OwnerID, true)
	if err != nil {
		return err
	}
	if actor != b.OwnerID {
		return store.ErrBotForbidden
	}
	rec, err := s.repo.db.GetBot(ctx, id)
	if err != nil {
		return err
	}
	keys := []string{}
	for key := range rec.Metadata {
		if strings.HasPrefix(key, "authorized-pending:") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var failures []error
	attempted := 0
	for _, key := range keys {
		if attempted >= limit {
			break
		}
		opID, err := uuid.Parse(strings.TrimPrefix(key, "authorized-pending:"))
		if err != nil {
			return err
		}
		var ref store.BotMandateRef
		raw, _ := json.Marshal(rec.Metadata[key])
		if json.Unmarshal(raw, &ref) != nil {
			return store.ErrBotInvalid
		}
		// Personal repositories can be polled from multiple teams; never substitute scope.
		if ref.BotID != id {
			return store.ErrBotForbidden
		}
		principal, ok := execution.PrincipalFromContext(ctx)
		if !ok || principal.Subject != ref.OwnerID || principal.OrgSlug != ref.OrgSlug || principal.TeamSlug != ref.TeamSlug {
			continue
		}
		var retry time.Time
		raw, _ = json.Marshal(rec.Metadata["authorized-retry:"+opID.String()])
		_ = json.Unmarshal(raw, &retry)
		if retry.After(s.repo.clock()) {
			continue
		}
		attempted++
		op, err := s.ReadAuthorizedOperation(ctx, ref, opID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if op.Status == store.BotOperationApplying {
			continue // ambiguous effects need exact reconciliation, never blind cancellation
		}
		if op.Decision != store.BotOperationExecute {
			continue // polling is not consent
		}
		if op.Status == store.BotOperationBlocked {
			if err = s.pendingState(ctx, ref, opID, store.BotOperationPending, "", time.Time{}); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		_, err = s.ApplyAuthorizedOperation(ctx, ref, opID)
		if err == nil {
			continue
		}
		state := store.BotOperationBlocked
		reason := "readiness_retry"
		retry = s.repo.clock().Add(time.Minute)
		switch {
		case errors.Is(err, store.ErrBotBusy):
			reason = "source_or_bot_busy"
		case errors.Is(err, store.ErrBotForbidden), errors.Is(err, store.ErrBotInvalid), errors.Is(err, store.ErrBotNotFound), errors.Is(err, store.ErrBotConflict):
			state = store.BotOperationCancelled
			reason = "authority_or_snapshot_invalid"
			retry = time.Time{}
		}
		if e := s.pendingState(ctx, ref, opID, state, reason, retry); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s *botAuthorizedOperationRuntime) pendingState(ctx context.Context, ref store.BotMandateRef, id uuid.UUID, state store.BotOperationStatus, reason string, retry time.Time) error {
	return s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := s.repo.mandateScope(ctx, tx, ref)
		if err != nil {
			return err
		}
		op, err := readMandateOperation(ctx, tx, rec, ref, id)
		if err != nil {
			return err
		}
		if routineOperation(op.Payload.Operation) {
			err = verifyRoutineGrant(ctx, tx, *op)
		} else {
			err = verifyAuthorizedGrant(ctx, tx, *op)
		}
		if err != nil {
			return err
		}
		if op.Status == store.BotOperationApplied || op.Status == store.BotOperationCancelled {
			return nil
		}
		if !op.Status.CanTransitionTo(state) {
			return store.ErrBotConflict
		}
		op.Status = state
		op.Revision++
		op.Receipt = nil
		if state != store.BotOperationPending {
			op.Receipt = &store.BotOperationReceipt{Status: state, ObservedAt: s.repo.clock(), Reason: reason}
		}
		rec.Metadata["authorized-retry:"+id.String()] = retry
		return saveAuthorizedOperation(ctx, tx, rec, *op, s)
	})
}
