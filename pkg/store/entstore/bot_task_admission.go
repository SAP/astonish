package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// pinTaskHead runs inside admission's transaction. Detached snapshots are recovery
// evidence only; the runtime subsequently verifies this exact bundle against Home.
func pinTaskHead(ctx context.Context, tx botDraftDB, botID uuid.UUID, taskID *uuid.UUID) (*store.BotTaskRevision, error) {
	if taskID == nil {
		return nil, nil
	}
	if *taskID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	db, ok := tx.(botTaskDB)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	task, err := db.readTask(ctx, botID, *taskID)
	if err != nil {
		return nil, err
	}
	if task.BotID != botID || task.Status == store.BotTaskArchived || task.Head == nil {
		return nil, store.ErrBotConflict
	}
	publications, err := db.readTaskPublications(ctx, botID)
	if err != nil {
		return nil, err
	}
	for _, p := range publications {
		if p.Publication.State == store.BotTaskPublicationCommitted && p.Publication.Candidate.Ref == *task.Head {
			revision := p.Publication.Candidate.Clone()
			if err := revision.Validate(task.Status); err != nil {
				return nil, err
			}
			return revision, nil
		}
	}
	return nil, store.ErrBotConflict
}
