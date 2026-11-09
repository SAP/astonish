package entstore

import (
	"context"

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

func (r *botRepository) CreateDraftAndStart(ctx context.Context, q store.BotCreateRequest) (*store.BotTitleStartReceipt, error) {
	admission, ok := store.BotMessageAdmissionFromContext(ctx)
	if !ok {
		return nil, store.ErrBotForbidden
	}
	tracked := &titleAdmission{BotMessageAdmission: admission}
	ctx = context.WithValue(store.WithBotMessageAdmission(ctx, tracked), titleMandateAdmissionKey{}, true)
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
