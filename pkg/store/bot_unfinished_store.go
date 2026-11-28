package store

import (
	"context"
	"github.com/google/uuid"
)

// BotUnfinishedIntakeProvider is an optional capability on scoped Bot repositories.
// It deliberately cannot create Bots, sessions or jobs.
type BotUnfinishedIntakeProvider interface {
	UnfinishedIntakes() BotUnfinishedIntakeStore
}
type BotUnfinishedIntakeStore interface {
	Get(context.Context, uuid.UUID) (*BotUnfinishedIntake, error)
	Put(context.Context, uuid.UUID, BotUnfinishedPut) (*BotUnfinishedIntake, error)
	Delete(context.Context, uuid.UUID, BotUnfinishedDelete) error
}
