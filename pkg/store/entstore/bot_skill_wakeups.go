package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Admission checks ownership and availability only. The immutable method is
// resolved once at orientation, so a queued wake uses the latest learned head.
func sameSkillID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validateRunSkillSelection(ctx context.Context, tx botDraftDB, botID uuid.UUID, id *uuid.UUID, intent string) error {
	if err := store.ValidateSkillSelection(id, intent); err != nil {
		return err
	}
	if id == nil {
		return nil
	}
	db, ok := tx.(botSkillDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	s, err := db.readSkill(ctx, botID, *id)
	if err != nil {
		return err
	}
	if s.Skill.Status == store.BotSkillArchived {
		return store.ErrBotConflict
	}
	return nil
}
