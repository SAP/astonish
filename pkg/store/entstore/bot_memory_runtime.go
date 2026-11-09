package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type botMemoryLeaseKey struct{}

func (s *teamBotStore) MutateBotRunMemory(ctx context.Context, m store.MemoryStore, lease store.BotLease, q store.BotMemoryMutation) error {
	return s.MutateBotMemory(context.WithValue(ctx, botMemoryLeaseKey{}, lease), m, lease.BotID, q)
}
func (s *personalBotStore) MutateBotRunMemory(ctx context.Context, m store.MemoryStore, lease store.BotLease, q store.BotMemoryMutation) error {
	return s.MutateBotMemory(context.WithValue(ctx, botMemoryLeaseKey{}, lease), m, lease.BotID, q)
}
func guardBotRunMemory(ctx context.Context, r *botRepository, tx botDraftDB, id store.BotID, q store.BotMemoryMutation, lease store.BotLease) (uuid.UUID, uuid.UUID, error) {
	if lease.BotID != id {
		return uuid.Nil, uuid.Nil, store.ErrBotForbidden
	}
	rec, err := r.checkLease(ctx, tx, lease, r.clock())
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	run, err := tx.ReadRun(ctx, id, lease.RunID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	sid := q.Entry.SessionID
	if q.Kind != store.BotMemoryAdd {
		sid = q.Previous.SessionID
	}
	// Runtime mutation is limited to the producing run. Prior evidence cannot be
	// erased or rewritten by an autonomous worker.
	if sid != run.Run.SessionID.String() || (q.Entry.CreatedBy != "" && q.Entry.CreatedBy != rec.Bot.OwnerID.String()) {
		return uuid.Nil, uuid.Nil, store.ErrBotForbidden
	}
	if err := guardMemoryAttribution(ctx, tx, id, q); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if q.Kind == store.BotMemoryUpdate && q.Previous.Attribution != nil && q.Entry.Content != q.Previous.Snippet {
		return uuid.Nil, uuid.Nil, store.ErrBotConflict
	}
	if err := tx.AdvanceLeaseSequence(ctx, lease, rec.LastSequence, r.clock()); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return rec.Bot.OwnerID, run.Run.SessionID, nil
}
