package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// WithBotWorkspaceArchiveFence holds the same per-Bot locks used by physical
// writers while a scope's logical rows and file inventories are exported. The
// callback must not mutate the scoped store. New Bots must be prevented by the
// operator's offline backup barrier (or lifecycle quiescence) during discovery.
func WithBotWorkspaceArchiveFence(ctx context.Context, bots store.BotStore, export func() error) error {
	var r *botRepository
	switch b := bots.(type) {
	case *personalBotStore:
		r = b.taskRepository()
	case *teamBotStore:
		r = b.taskRepository()
	default:
		return store.ErrBotUnsupported
	}
	return r.db.Transaction(ctx, func(tx botDB) error {
		after := uuid.Nil
		for {
			rows, err := tx.ListBots(ctx, 100, after)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Bot.Status == store.BotDeleted {
					continue
				}
				if _, err := r.auth.authorize(ctx, row.Bot.OwnerID, true); err != nil {
					return err
				}
				if err := tx.(botSkillDB).lockSkills(ctx, row.Bot.ID); err != nil {
					return err
				}
			}
			if len(rows) < 100 {
				break
			}
			after = rows[len(rows)-1].Bot.ID
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return export()
	})
}
