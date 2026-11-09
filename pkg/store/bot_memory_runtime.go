package store

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

// BotRuntimeMemoryMutationStore performs memory mutations under the live worker
// lease, in the same transaction as the Bot/session/content checks.
type BotRuntimeMemoryMutationStore interface {
	MutateBotRunMemory(context.Context, MemoryStore, BotLease, BotMemoryMutation) error
}

type botRunMemoryRepository struct {
	BotStore
	lease BotLease
}

func (r botRunMemoryRepository) AuthorizeBotMemoryWrite(ctx context.Context, id BotID) (uuid.UUID, error) {
	identity, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok || identity.BotID != id || id != r.lease.BotID || identity.RunID != r.lease.RunID {
		return uuid.Nil, ErrBotForbidden
	}
	b, err := r.Get(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	if b == nil || b.OwnerID.String() != identity.ActorID {
		return uuid.Nil, ErrBotForbidden
	}
	return b.OwnerID, nil
}
func (r botRunMemoryRepository) MutateBotMemory(ctx context.Context, m MemoryStore, id BotID, q BotMemoryMutation) error {
	if id != r.lease.BotID {
		return ErrBotForbidden
	}
	backend, ok := r.BotStore.(BotRuntimeMemoryMutationStore)
	if !ok {
		return ErrBotUnsupported
	}
	return backend.MutateBotRunMemory(ctx, m, r.lease, q)
}

// NewBotRunMemoryService uses the same Bot-prefiltered reads as interactive
// memory, but writes must use an explicitly fenced runtime backend.
func NewBotRunMemoryService(bots BotStore, memory MemoryStore, lease BotLease, scope BotScope) (*BotMemoryService, error) {
	if bots == nil {
		return nil, ErrBotUnsupported
	}
	return NewBotMemoryService(botRunMemoryRepository{BotStore: bots, lease: lease}, memory, lease.BotID, scope)
}
