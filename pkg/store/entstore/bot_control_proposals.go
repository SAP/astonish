package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

const controlProposalRecord = "bot_control_proposal_v1"

type controlProposalEnvelope struct {
	Record      string                   `json:"record"`
	Proposal    store.BotControlProposal `json:"proposal"`
	RequestHash string                   `json:"request_hash,omitempty"`
}

func controlProposalKey(id uuid.UUID) string { return "control-proposal:" + id.String() }
func readControlProposal(ctx context.Context, db botDraftDB, bot, id uuid.UUID) (*store.BotControlProposal, error) {
	a, err := db.FindActivity(ctx, bot, controlProposalKey(id)+":decision")
	if errors.Is(err, store.ErrBotNotFound) {
		a, err = db.FindActivity(ctx, bot, controlProposalKey(id))
	}
	if err != nil {
		return nil, err
	}
	var e controlProposalEnvelope
	if err = json.Unmarshal(a.Payload, &e); err != nil {
		return nil, err
	}
	if e.Record != controlProposalRecord || e.Proposal.ID != id || e.Proposal.BotID != bot {
		return nil, store.ErrBotInvalid
	}
	canonical := e.Proposal
	canonical.Digest = ""
	canonical.Status = store.BotControlProposed
	canonical.DecidedAt, canonical.DecidedBy = nil, nil
	canonical.ReconciliationPending = false
	digest, err := botHash(canonical)
	if err != nil {
		return nil, err
	}
	if digest != e.Proposal.Digest {
		return nil, store.ErrBotConflict
	}
	if err = e.Proposal.Payload.Validate(); err != nil {
		return nil, err
	}
	return &e.Proposal, nil
}
func (r *botRepository) ProposeControl(ctx context.Context, q store.BotControlProposalRequest) (*store.BotControlProposal, error) {
	if q.Lease.BotID == uuid.Nil || q.Lease.RunID == uuid.Nil || strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 {
		return nil, store.ErrBotInvalid
	}
	if err := q.Payload.Validate(); err != nil {
		return nil, err
	}
	q.ExpiresAt = q.ExpiresAt.UTC()
	requestHash, err := botHash(struct {
		Payload   store.BotControlPayload
		ExpiresAt string
	}{q.Payload, q.ExpiresAt.Format("2006-01-02T15:04:05.999999999Z07:00")})
	if err != nil {
		return nil, err
	}
	id := uuid.NewSHA1(q.Lease.RunID, []byte("control:"+q.IdempotencyKey))
	var result *store.BotControlProposal
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := r.checkLease(ctx, tx, q.Lease, r.clock())
		if err != nil {
			return err
		}
		if old, e := tx.FindActivity(ctx, q.Lease.BotID, controlProposalKey(id)); e == nil {
			var v controlProposalEnvelope
			if e = json.Unmarshal(old.Payload, &v); e != nil {
				return e
			}
			if v.RequestHash != requestHash {
				return store.ErrBotConflict
			}
			result, e = readControlProposal(ctx, tx, q.Lease.BotID, id)
			return e
		} else if !errors.Is(e, store.ErrBotNotFound) {
			return e
		}
		if !q.ExpiresAt.After(r.clock()) {
			return store.ErrBotExpired
		}
		source, err := tx.ReadRun(ctx, rec.Bot.ID, q.Lease.RunID)
		if err != nil {
			return err
		}
		if source.Run.Trigger != store.TriggerManual {
			return store.ErrBotForbidden
		}
		admission, err := tx.FindActivity(ctx, rec.Bot.ID, "dispatch:"+q.Lease.RunID.String())
		if err != nil {
			return err
		}
		var admitted struct {
			ActorID uuid.UUID `json:"actor_id"`
			RunID   uuid.UUID `json:"run_id"`
		}
		if err = json.Unmarshal(admission.Payload, &admitted); err != nil {
			return err
		}
		if admitted.ActorID == uuid.Nil || admitted.RunID != q.Lease.RunID || admission.RunID == nil || *admission.RunID != q.Lease.RunID {
			return store.ErrBotForbidden
		}
		if err = prepareControlMemory(ctx, tx, rec, &q.Payload); err != nil {
			return err
		}
		result = &store.BotControlProposal{ID: id, BotID: rec.Bot.ID, SourceRunID: q.Lease.RunID, SourceUserID: admitted.ActorID, Version: source.Run.Version, ExpectedRevision: rec.Bot.Revision, Payload: q.Payload, Status: store.BotControlProposed, CreatedAt: r.clock(), ExpiresAt: q.ExpiresAt}
		digest, err := botHash(*result)
		if err != nil {
			return err
		}
		result.Digest = digest
		data, err := json.Marshal(controlProposalEnvelope{Record: controlProposalRecord, Proposal: *result, RequestHash: requestHash})
		if err != nil {
			return err
		}
		if err = tx.AdvanceLeaseSequence(ctx, q.Lease, rec.LastSequence, r.clock()); err != nil {
			return err
		}
		if err = tx.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, RunID: &q.Lease.RunID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: data, CreatedAt: result.CreatedAt}, controlProposalKey(id)); err != nil {
			return err
		}
		return appendBotReview(ctx, tx, rec.Bot, q.Lease.RunID, id, botReviewProposal, result.CreatedAt)
	})
	return result, err
}
func (r *botRepository) ReadControlProposal(ctx context.Context, bot store.BotID, id uuid.UUID) (*store.BotControlProposal, error) {
	if _, _, err := r.viewerRecord(ctx, r.db, bot); err != nil {
		return nil, err
	}
	return readControlProposal(ctx, r.db, bot, id)
}
func listControlProposals(ctx context.Context, db botDraftDB, bot store.BotID) ([]store.BotControlProposal, error) {
	result := []store.BotControlProposal{}
	indices := map[uuid.UUID]int{}
	for after := int64(0); ; {
		page, err := db.ReadActivity(ctx, bot, after, 200)
		if err != nil {
			return nil, err
		}
		for _, a := range page {
			after = a.Sequence
			if a.Kind != store.BotActivityLifecycle {
				continue
			}
			var e controlProposalEnvelope
			if err = json.Unmarshal(a.Payload, &e); err != nil {
				return nil, err
			}
			if e.Record != controlProposalRecord {
				continue
			}
			if i, ok := indices[e.Proposal.ID]; ok {
				result[i] = e.Proposal
			} else {
				indices[e.Proposal.ID] = len(result)
				result = append(result, e.Proposal)
			}
		}
		if len(page) < 200 {
			break
		}
	}
	return result, nil
}
func (r *botRepository) ListControlProposals(ctx context.Context, bot store.BotID) ([]store.BotControlProposal, error) {
	if _, _, err := r.viewerRecord(ctx, r.db, bot); err != nil {
		return nil, err
	}
	return listControlProposals(ctx, r.db, bot)
}
func (r *botRepository) ConfirmControlProposal(ctx context.Context, q store.BotControlProposalDecision) (*store.BotControlProposal, error) {
	return r.decideControlProposal(ctx, q, true)
}
func (r *botRepository) RejectControlProposal(ctx context.Context, q store.BotControlProposalDecision) (*store.BotControlProposal, error) {
	return r.decideControlProposal(ctx, q, false)
}
func (r *botRepository) decideControlProposal(ctx context.Context, q store.BotControlProposalDecision, confirm bool) (*store.BotControlProposal, error) {
	// Worker contexts carry a Bot execution identity; a model cannot turn its own proposal into consent.
	if _, ok := execution.BotRunIdentityFromContext(ctx); ok {
		return nil, store.ErrBotForbidden
	}
	if q.BotID == uuid.Nil || q.ProposalID == uuid.Nil || q.Digest == "" {
		return nil, store.ErrBotInvalid
	}
	var reviewed *store.Bot
	var err error
	if confirm {
		reviewed, err = r.prepareControlExecution(ctx, q)
		if err != nil {
			return nil, err
		}
	}
	var result *store.BotControlProposal
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		p, err := readControlProposal(ctx, tx, q.BotID, q.ProposalID)
		if err != nil {
			return err
		}
		if p.Digest != q.Digest {
			return store.ErrBotConflict
		}
		result = p
		if p.Status != store.BotControlProposed {
			return nil
		}
		now := r.clock()
		switch {
		case !now.Before(p.ExpiresAt):
			p.Status = store.BotControlExpired
		case !confirm:
			p.Status = store.BotControlRejected
		default:
			source, err := tx.ReadRun(ctx, p.BotID, p.SourceRunID)
			if err != nil {
				return err
			}
			if !source.Run.Status.Terminal() || rec.ActiveRunID != nil {
				return store.ErrBotBusy
			}
			expectedRevision := p.ExpectedRevision
			// Preflight may consume exactly one revision for trusted home setup.
			// The saved proposal/digest stay immutable; full Bot equality below
			// still fences every concurrent mutation after that setup.
			if reviewed != nil {
				expectedRevision = reviewed.Revision
			}
			if rec.Bot.Version != p.Version || rec.Bot.Revision != expectedRevision {
				p.Status = store.BotControlStale
				break
			}
			if reviewed != nil {
				before, e := botHash(*reviewed)
				if e != nil {
					return e
				}
				after, e := botHash(rec.Bot)
				if e != nil {
					return e
				}
				if before != after {
					return store.ErrBotConflict
				}
			}
			if err = r.applyControlProposal(ctx, tx, rec, *p, actor); err != nil {
				return err
			}
			p.Status = store.BotControlApplied
			rec, err = tx.GetBot(ctx, q.BotID)
			if err != nil {
				return err
			}
		}
		// Recheck live membership immediately before the durable decision receipt.
		if currentActor, e := r.auth.authorize(ctx, rec.Bot.OwnerID, true); e != nil {
			return e
		} else if currentActor != actor {
			return store.ErrBotForbidden
		}
		p.DecidedAt = &now
		p.DecidedBy = &actor
		return insertPublicationActivity(ctx, tx, rec, p.SourceRunID, store.BotActivityLifecycle, controlProposalKey(p.ID)+":decision", controlProposalEnvelope{Record: controlProposalRecord, Proposal: *p}, now)
	})
	return result, err
}
func (r *botRepository) applyControlProposal(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	switch p.Payload.Operation {
	case store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget:
		return r.applyControlMemoryTx(ctx, tx, rec, p, actor)
	case store.BotControlTaskOnceOperation:
		return r.applyControlTaskTx(ctx, tx, rec, p, actor)
	case store.BotControlActivate, store.BotControlResume:
		return r.applyControlLifecycleTx(ctx, tx, rec, p, actor)
	case store.BotControlConfigurationOperation:
		return r.applyControlConfigurationTx(ctx, tx, rec, p, actor)
	case store.BotControlResponsibility, store.BotControlPersona:
		if rec.Bot.Status == store.BotDeleted {
			return store.ErrBotConflict
		}
		current, err := tx.ReadVersion(ctx, p.BotID, p.Version)
		if err != nil {
			return err
		}
		v := *current
		if p.Payload.Operation == store.BotControlResponsibility {
			v.Charter = *p.Payload.Responsibility
		} else {
			v.Persona = *p.Payload.Persona
		}
		return r.saveControlVersionTx(ctx, tx, rec, p, v, actor)
	case store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlSchedulePause, store.BotControlScheduleRemove:
		return r.applyControlScheduleTx(ctx, tx, rec, p, actor)
	case store.BotControlGoalCreate, store.BotControlGoalEdit:
		return r.applyControlGoalTx(ctx, tx, rec, p, actor)
	default:
		return store.ErrBotUnsupported
	}
}
