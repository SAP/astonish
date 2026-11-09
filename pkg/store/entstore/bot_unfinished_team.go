package entstore

import (
	"context"
	"encoding/json"
	team "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/botintake"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type teamIntakeDB struct{ client *team.Client }

func (s *teamBotStore) UnfinishedIntakes() store.BotUnfinishedIntakeStore {
	return &intakeRepository{auth: s.auth, db: teamIntakeDB{s.client}}
}
func (d teamIntakeDB) read(ctx context.Context, id, owner uuid.UUID) (*store.BotUnfinishedIntake, error) {
	row, err := d.client.BotIntake.Query().Where(botintake.IDEQ(id), botintake.OwnerIDEQ(owner)).Only(ctx)
	if team.IsNotFound(err) {
		return nil, store.ErrBotNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(row.MutationHash) > 64 {
		return nil, store.ErrBotConflict
	}
	b := &store.BotUnfinishedIntake{ID: row.ID, OwnerID: row.OwnerID, Scope: store.BotScopeTeam, Revision: row.Revision, UpdatedAt: row.UpdatedAt, Discarded: row.Discarded, LastMutationHash: row.MutationHash}
	if len(row.Content) > 0 {
		if err := json.Unmarshal(row.Content, &b.Content); err != nil {
			return nil, err
		}
	}
	return b, nil
}
func (d teamIntakeDB) insert(ctx context.Context, b *store.BotUnfinishedIntake) error {
	content, err := json.Marshal(b.Content)
	if err != nil {
		return err
	}
	_, err = d.client.BotIntake.Create().SetID(b.ID).SetOwnerID(b.OwnerID).SetRevision(b.Revision).SetContent(content).SetDiscarded(b.Discarded).SetMutationHash(b.LastMutationHash).SetUpdatedAt(b.UpdatedAt).Save(ctx)
	if team.IsConstraintError(err) {
		return store.ErrBotConflict
	}
	return err
}
func (d teamIntakeDB) cas(ctx context.Context, b *store.BotUnfinishedIntake, revision int64) error {
	update := d.client.BotIntake.Update().Where(botintake.IDEQ(b.ID), botintake.OwnerIDEQ(b.OwnerID), botintake.RevisionEQ(revision), botintake.DiscardedEQ(false)).SetRevision(b.Revision).SetDiscarded(b.Discarded).SetMutationHash(b.LastMutationHash).SetUpdatedAt(b.UpdatedAt)
	if b.Content == nil {
		update.ClearContent()
	} else {
		content, err := json.Marshal(b.Content)
		if err != nil {
			return err
		}
		update.SetContent(content)
	}
	n, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
