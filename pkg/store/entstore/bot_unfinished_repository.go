package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"time"
)

type intakeDB interface {
	read(context.Context, uuid.UUID, uuid.UUID) (*store.BotUnfinishedIntake, error)
	insert(context.Context, *store.BotUnfinishedIntake) error
	cas(context.Context, *store.BotUnfinishedIntake, int64) error
}
type intakeRepository struct {
	auth botRepositoryAuth
	db   intakeDB
}

func (r *intakeRepository) Get(ctx context.Context, id uuid.UUID) (*store.BotUnfinishedIntake, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, false)
	if err != nil {
		return nil, err
	}
	b, err := r.db.read(ctx, id, actor)
	if err != nil {
		return nil, err
	}
	if b.Discarded {
		return nil, store.ErrBotNotFound
	}
	return b, nil
}
func (r *intakeRepository) Put(ctx context.Context, id uuid.UUID, q store.BotUnfinishedPut) (*store.BotUnfinishedIntake, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	// Unique insert and bounded reread handle concurrent creates and exact retries.
	for attempt := 0; attempt < 3; attempt++ {
		b, err := r.db.read(ctx, id, actor)
		if errors.Is(err, store.ErrBotNotFound) {
			next, e := store.NewBotUnfinishedIntake(id, actor, r.auth.scope, q, time.Now())
			if e != nil {
				return nil, e
			}
			if e = r.db.insert(ctx, next); e == nil {
				return next, nil
			} else if !errors.Is(e, store.ErrBotConflict) {
				return nil, e
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		next, err := b.Replace(actor, r.auth.scope, q, time.Now())
		if err != nil {
			return nil, err
		}
		if next.Revision == b.Revision {
			return next, nil
		}
		if err = r.db.cas(ctx, next, b.Revision); err == nil {
			return next, nil
		} else if !errors.Is(err, store.ErrBotConflict) {
			return nil, err
		}
	}
	return nil, store.ErrBotConflict
}
func (r *intakeRepository) Delete(ctx context.Context, id uuid.UUID, q store.BotUnfinishedDelete) error {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		b, err := r.db.read(ctx, id, actor)
		if err != nil {
			return err
		}
		next, err := b.Discard(actor, r.auth.scope, q, time.Now())
		if err != nil {
			return err
		}
		if next.Revision == b.Revision {
			return nil
		}
		if err = r.db.cas(ctx, next, b.Revision); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrBotConflict) {
			return err
		}
	}
	return store.ErrBotConflict
}
