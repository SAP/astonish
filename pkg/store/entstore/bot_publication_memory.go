package entstore

import (
	"context"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
)

func (s *personalBotStore) ReadMemoryPublication(c context.Context, b store.BotID, r store.RunID, seq int64) (*store.BotMemoryPublication, error) {
	return s.repository().readMemoryPublication(c, b, r, seq)
}
func (s *teamBotStore) ReadMemoryPublication(c context.Context, b store.BotID, r store.RunID, seq int64) (*store.BotMemoryPublication, error) {
	return s.repository().readMemoryPublication(c, b, r, seq)
}
func (r *botRepository) readMemoryPublication(ctx context.Context, id store.BotID, run store.RunID, seq int64) (*store.BotMemoryPublication, error) {
	if seq < 1 {
		return nil, store.ErrBotInvalid
	}
	if _, err := r.publicationOwner(ctx, r.db, id); err != nil {
		return nil, err
	}
	p, err := readPublication(ctx, r.db, id, run, seq)
	if err != nil {
		return nil, err
	}
	saved, _, err := committedPublicationReport(ctx, r.db, id, run, seq)
	if err != nil {
		return nil, err
	}
	original, err := r.db.ReadRun(ctx, id, run)
	if err != nil {
		return nil, err
	}
	return &store.BotMemoryPublication{Publication: *p, Goal: original.Instructions, Status: saved.Status}, nil
}
func (s *personalBotStore) WithBotPublicationFileCommit(c context.Context, h store.BotHomeBinding, r store.RunID, seq int64, f func() error) error {
	return s.repository().publicationFileCommit(c, h, r, seq, f)
}
func (s *teamBotStore) WithBotPublicationFileCommit(c context.Context, h store.BotHomeBinding, r store.RunID, seq int64, f func() error) error {
	return s.repository().publicationFileCommit(c, h, r, seq, f)
}

// Lock the existing Bot root before reading, just as transcript commits do. This
// authorizes only metadata maintenance for an already committed publication; it
// neither reuses an expired run lease nor authorizes any external action.
func (r *botRepository) publicationFileCommit(ctx context.Context, h store.BotHomeBinding, run store.RunID, seq int64, commit func() error) error {
	if commit == nil || seq < 1 || h.Scope != r.auth.scope {
		return store.ErrBotInvalid
	}
	if _, err := r.auth.authorize(ctx, [16]byte{}, true); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		var n int
		var err error
		switch d := tx.(type) {
		case personalBotDB:
			n, err = d.client.Bot.Update().Where(personalbot.IDEQ(h.BotID)).AddRevision(0).Save(ctx)
		case teamBotDB:
			n, err = d.client.Bot.Update().Where(teambot.IDEQ(h.BotID)).AddRevision(0).Save(ctx)
		default:
			return store.ErrBotUnsupported
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return store.ErrBotConflict
		}
		b, err := r.publicationOwner(ctx, tx, h.BotID)
		if err != nil {
			return err
		}
		if b.Bot.Home != h || b.Bot.Status == store.BotDeleted {
			return store.ErrBotConflict
		}
		if _, err := readPublication(ctx, tx, h.BotID, run, seq); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return commit()
	})
}
