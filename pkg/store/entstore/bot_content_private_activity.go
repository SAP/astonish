package entstore

import (
	"encoding/json"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// These are explicit SQL authority projections, not hydrated request DTOs. The
// private context bytes and the closed-union proposal payload remain only in the
// immutable activity content. decodeActivity compares these projections before
// any caller can use hydrated bytes for replay or a decision.
type botProgressContextAuthority struct {
	Binding store.BotProgressBinding `json:"binding"`
	Digest  string                   `json:"digest"`
}

type botControlProposalAuthority struct {
	ID                    uuid.UUID                      `json:"id"`
	BotID                 store.BotID                    `json:"bot_id"`
	SourceRunID           store.RunID                    `json:"source_run_id"`
	SourceUserID          uuid.UUID                      `json:"source_user_id"`
	Version               int64                          `json:"version"`
	ExpectedRevision      int64                          `json:"expected_revision"`
	Digest                string                         `json:"digest"`
	RequestHash           string                         `json:"request_hash,omitempty"`
	Operation             store.BotControlOperation      `json:"operation"`
	Status                store.BotControlProposalStatus `json:"status"`
	CreatedAt             time.Time                      `json:"created_at"`
	ExpiresAt             time.Time                      `json:"expires_at"`
	DecidedAt             *time.Time                     `json:"decided_at,omitempty"`
	DecidedBy             *uuid.UUID                     `json:"decided_by,omitempty"`
	ReconciliationPending bool                           `json:"reconciliation_pending"`
}

func activityPrivateAuthority(a store.BotActivity) (*botProgressContextAuthority, *botControlProposalAuthority, error) {
	if a.Kind == progressRecordKind {
		var context botProgressContextRecord
		if err := json.Unmarshal(a.Payload, &context); err != nil {
			return nil, nil, err
		}
		if context.Digest != "" || context.Data != nil {
			if context.Binding.Validate() != nil || context.Binding.BotID != a.BotID || a.RunID == nil || context.Binding.RunID != *a.RunID || len(context.Data) == 0 || len(context.Data) > store.BotProgressMaxBytes || context.Digest != store.BotProgressContextDigest(context.Data) {
				return nil, nil, store.ErrBotInvalid
			}
			return &botProgressContextAuthority{Binding: context.Binding, Digest: context.Digest}, nil, nil
		}
	}
	if a.Kind != store.BotActivityLifecycle {
		return nil, nil, nil
	}
	var e controlProposalEnvelope
	if err := json.Unmarshal(a.Payload, &e); err != nil {
		return nil, nil, err
	}
	if e.Record != controlProposalRecord {
		return nil, nil, nil
	}
	p := e.Proposal
	if p.ID == uuid.Nil || p.BotID != a.BotID || a.RunID == nil || p.SourceRunID != *a.RunID || p.SourceUserID == uuid.Nil || p.Version < 1 || p.ExpectedRevision < 1 || p.CreatedAt.IsZero() || !p.ExpiresAt.After(p.CreatedAt) || p.Payload.Validate() != nil {
		return nil, nil, store.ErrBotInvalid
	}
	switch p.Status {
	case store.BotControlProposed:
		if p.DecidedAt != nil || p.DecidedBy != nil {
			return nil, nil, store.ErrBotInvalid
		}
	case store.BotControlApplied, store.BotControlRejected, store.BotControlExpired, store.BotControlStale:
		if p.DecidedAt == nil || p.DecidedBy == nil || *p.DecidedBy == uuid.Nil {
			return nil, nil, store.ErrBotInvalid
		}
	default:
		return nil, nil, store.ErrBotInvalid
	}
	canonical := p
	canonical.Digest = ""
	canonical.Status = store.BotControlProposed
	canonical.DecidedAt, canonical.DecidedBy = nil, nil
	canonical.ReconciliationPending = false
	digest, err := botHash(canonical)
	if err != nil {
		return nil, nil, err
	}
	if digest != p.Digest {
		return nil, nil, store.ErrBotConflict
	}
	return nil, &botControlProposalAuthority{
		ID: p.ID, BotID: p.BotID, SourceRunID: p.SourceRunID, SourceUserID: p.SourceUserID,
		Version: p.Version, ExpectedRevision: p.ExpectedRevision, Digest: p.Digest, RequestHash: e.RequestHash,
		Operation: p.Payload.Operation, Status: p.Status, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
		DecidedAt: p.DecidedAt, DecidedBy: p.DecidedBy, ReconciliationPending: p.ReconciliationPending,
	}, nil
}
