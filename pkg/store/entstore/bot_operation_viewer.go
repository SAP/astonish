package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strconv"
)

// ReadOperationForViewer resolves only a reserved operation head under current
// scoped authorization. Authority is returned to server callers, never as JSON.
func (r *botRepository) ReadOperationForViewer(ctx context.Context, botID, operationID uuid.UUID) (*store.BotOperation, *store.BotOperationView, error) {
	var operation *store.BotOperation
	var view *store.BotOperationView
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, botID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		raw, err := json.Marshal(rec.Metadata[operationHeadKey(operationID)])
		if err != nil {
			return err
		}
		var revision int64
		if json.Unmarshal(raw, &revision) != nil || revision < 1 {
			return store.ErrBotNotFound
		}
		a, err := tx.FindActivity(ctx, botID, "mandate-operation:"+operationID.String()+":"+strconv.FormatInt(revision, 10))
		if err != nil {
			return err
		}
		var o store.BotOperation
		if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &o) != nil || o.ID != operationID || o.Revision != revision || o.MandateRef.OwnerID != actor.String() {
			return store.ErrBotInvalid
		}
		v, err := store.ProjectBotOperation(rec.Bot, o, a.Sequence)
		if err != nil {
			return err
		}
		operation, view = &o, &v
		return nil
	})
	return operation, view, err
}

func (s *personalBotStore) ReadOperationForViewer(c context.Context, b, id uuid.UUID) (*store.BotOperation, *store.BotOperationView, error) {
	return s.repository().ReadOperationForViewer(c, b, id)
}
func (s *teamBotStore) ReadOperationForViewer(c context.Context, b, id uuid.UUID) (*store.BotOperation, *store.BotOperationView, error) {
	return s.repository().ReadOperationForViewer(c, b, id)
}
