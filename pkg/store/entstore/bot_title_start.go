package entstore

import (
	"context"
	"reflect"

	prun "github.com/SAP/astonish/ent/personal/botrun"
	trun "github.com/SAP/astonish/ent/team/botrun"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotTitleStartStore = (*personalBotStore)(nil)
var _ store.BotTitleStartStore = (*teamBotStore)(nil)

func (s *personalBotStore) CreateDraftAndStart(ctx context.Context, q store.BotCreateRequest) (*store.BotTitleStartReceipt, error) {
	return s.repository().CreateDraftAndStart(ctx, q)
}
func (s *teamBotStore) CreateDraftAndStart(ctx context.Context, q store.BotCreateRequest) (*store.BotTitleStartReceipt, error) {
	return s.repository().CreateDraftAndStart(ctx, q)
}

// Already inside the scoped SQL transaction: nested repository operations share
// exactly this transaction, including their CAS, audit and dispatch writes.
type botAdmissionTx struct{ botDraftDB }

func (t botAdmissionTx) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return fn(t.botDraftDB)
}

type titleMandateAdmissionKey struct{}

// Adapter-owned initiation state: never inferred from a Bot name or private turn.
type titleMandateAdmission struct {
	Kind    store.BotInitiationKind
	Subject string
}

type titleAdmission struct {
	store.BotMessageAdmission
	registered []execution.BotRunIdentity
}

func (a *titleAdmission) Register(ctx context.Context, id execution.BotRunIdentity) (execution.BotTurnAuthorization, error) {
	value, err := a.BotMessageAdmission.Register(ctx, id)
	if err == nil {
		a.registered = append(a.registered, id)
	}
	return value, err
}

func persistTitleQuestion(ctx context.Context, tx botDraftDB, saved *botStoredRun) error {
	data, err := encodeBotRunData(ctx, tx, *saved)
	if err != nil {
		return err
	}
	var n int
	switch db := tx.(type) {
	case personalBotDB:
		n, err = db.client.BotRun.Update().Where(prun.IDEQ(saved.Run.ID), prun.StatusEQ(prun.StatusRunning), prun.FenceEQ(saved.Run.Lease.Fence)).SetData(data).Save(ctx)
	case teamBotDB:
		n, err = db.client.BotRun.Update().Where(trun.IDEQ(saved.Run.ID), trun.StatusEQ(trun.StatusRunning), trun.FenceEQ(saved.Run.Lease.Fence)).SetData(data).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

// confirmTitleAssignment is transaction-local: authority and assignment queue together.
func (r *botRepository) confirmTitleAssignment(ctx context.Context, tx botDraftDB, rec *botStoredRecord, saved *botStoredRun, a store.BotApproval) error {
	c := saved.Run.TitleConfirmation
	if c == nil || c.State == store.BotTitleConfirmationConfirmed {
		return nil
	}
	if c.ValidateBinding(saved.Run, rec.Bot.OwnerID) != nil {
		return store.ErrBotForbidden
	}
	next, err := c.WithAnswer(a)
	if err != nil {
		return err
	}
	resolve, enabled := store.BotMandateAdmissionFromContext(ctx)
	if saved.Run.MandateRef != nil {
		if !enabled || !verifiedMandateOwner(ctx, rec.Bot.OwnerID.String(), r.clock()) {
			return store.ErrBotForbidden
		}
		old, err := readMandate(ctx, tx, *saved.Run.MandateRef)
		if err != nil {
			return err
		}
		if old.RevokedAt != nil || !old.Mandate.ExpiresAt.After(r.clock()) {
			return store.ErrBotForbidden
		}
		ref := *saved.Run.MandateRef
		ref.ID = uuid.New()
		ref.Revision = 1
		policy, err := resolve(botPolicyContext(ctx, tx), ref)
		if err != nil {
			return err
		}
		now := r.clock()
		if err := old.Mandate.RuntimePolicy.ValidateWithin(policy.RuntimePolicy); err != nil {
			return err
		}
		m := store.BotMandate{RuntimePolicy: old.Mandate.RuntimePolicy.Clone(), Ref: ref, Purpose: execution.BotMandatePurposeTask, ExpiresAt: old.Mandate.ExpiresAt, Scopes: policy.Scopes, Constraints: policy.Constraints, Limits: old.Mandate.Limits}.Clone()
		if err := m.Limits.ValidateWithinRuntimePolicy(policy.Limits, m.RuntimePolicy, policy.RuntimePolicy); err != nil {
			return err
		}
		// Only the originally admitted title is eligible for public queries;
		// the selected/custom assignment is never a public-search subject.
		if policy.PublicResearch != nil {
			scope := policy.PublicResearch.Clone()
			scope.Subject, scope.SourceEventID = c.OriginalTitle, c.UserEventID
			if scope.Validate() == nil {
				m.PublicResearch = &scope
			}
		}
		provenance := store.BotMandateProvenance{InitiationKind: store.BotInitiationKindTitleConfirmation, Source: store.BotMandateSourceAuthenticatedUser, ActorID: rec.Bot.OwnerID.String(), RequestID: a.ID.String(), RecordedAt: now, UserEventID: c.UserEventID, SourceDigest: next.AssignmentDigest, RunID: saved.Run.ID.String(), TitleConfirmation: &store.BotTitleConfirmationProvenance{BotID: c.BotID.String(), RunID: c.RunID.String(), OwnerID: c.OwnerID.String(), UserEventID: c.UserEventID, ApprovalID: a.ID.String(), QuestionID: c.QuestionID, AssignmentDigest: next.AssignmentDigest}}
		record := store.BotMandateRecord{Mandate: m, Provenance: provenance}
		if err := record.Validate(now); err != nil {
			return err
		}
		if err := appendMandateRecord(ctx, tx, rec, mandateKey(ref), record, now); err != nil {
			return err
		}
		rec.Metadata[mandateHeadKey] = ref
		if err := saveMandateMetadata(ctx, tx, rec); err != nil {
			return err
		}
		saved.Run.MandateRef = &ref
	}
	saved.Run.TitleConfirmation = next
	return nil
}

func titleAnswerReplay(saved *botStoredRun, a *store.BotApproval, response *store.BotInteractionResponse) bool {
	c := saved.Run.TitleConfirmation
	return c != nil && c.State == store.BotTitleConfirmationConfirmed && c.ApprovalID == a.ID && reflect.DeepEqual(a.Response, response)
}

func (r *botRepository) CreateDraftAndStart(ctx context.Context, q store.BotCreateRequest) (*store.BotTitleStartReceipt, error) {
	admission, ok := store.BotMessageAdmissionFromContext(ctx)
	if !ok {
		return nil, store.ErrBotForbidden
	}
	tracked := &titleAdmission{BotMessageAdmission: admission}
	ctx = context.WithValue(store.WithBotMessageAdmission(ctx, tracked), titleMandateAdmissionKey{}, titleMandateAdmission{Kind: store.BotInitiationKindTitle, Subject: q.Name})
	var result *store.BotTitleStartReceipt
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		scoped := *r
		scoped.db = botAdmissionTx{tx}
		actor, err := r.auth.authorize(ctx, uuid.Nil, true)
		if err != nil {
			return err
		}
		prepared, _, _, err := prepareBotDraft(q, actor, r.auth.scope, r.clock())
		if err != nil {
			return err
		}
		if old, lookupErr := tx.FindCreate(ctx, prepared.CreateKey); lookupErr == nil {
			// A legacy CreateDraft with this key is not a title-start replay.
			if _, err := tx.FindRun(ctx, old.Bot.ID, "title-start"); err != nil {
				return store.ErrBotConflict
			}
		} else if lookupErr != store.ErrBotNotFound {
			return lookupErr
		}
		b, err := scoped.CreateDraft(ctx, q)
		if err != nil {
			return err
		}
		receipt, err := scoped.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: 1, Instructions: q.Name, IdempotencyKey: "title-start"})
		if err != nil {
			return err
		}
		result = &store.BotTitleStartReceipt{Bot: *b, Run: receipt.Run, UserEventID: receipt.UserEventID, HistoryCursor: receipt.HistoryCursor}
		return nil
	})
	if err != nil {
		for _, id := range tracked.registered {
			admission.Cancel(id)
		}
		return nil, err
	}
	return result, nil
}
