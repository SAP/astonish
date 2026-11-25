package entstore

import (
	"context"
	"testing"

	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	teamrun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// PinBotAssessmentRunFixture seeds only an empty assessment contract on a
// test-owned, pristine queued manual run. It is test setup, not a runtime protocol
// conversion API: production SaveWork deliberately forbids protocol changes.
// Requiring testing.TB keeps normal callers out; tenant/owner checks and canonical
// run decoding/encoding still apply. No raw SQL or disk envelope is reconstructed.
func PinBotAssessmentRunFixture(t testing.TB, ctx context.Context, scoped store.BotStore, botID store.BotID, runID store.RunID) error {
	t.Helper()
	var repo *botRepository
	switch s := scoped.(type) {
	case *personalBotStore:
		repo = s.repository()
	case *teamBotStore:
		repo = s.repository()
	default:
		return store.ErrBotUnsupported
	}
	if _, err := repo.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return err
	}
	return repo.db.Transaction(ctx, func(tx botDraftDB) error {
		b, err := tx.GetBot(ctx, botID)
		if err != nil {
			return err
		}
		actor, err := repo.auth.authorize(ctx, b.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != b.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		run, err := tx.ReadRun(ctx, botID, runID)
		if err != nil {
			return err
		}
		if b.ActiveRunID != nil || run.Run.Status != store.RunQueued || run.Run.Trigger != store.TriggerManual || run.Run.StartedAt != nil || run.Run.TitleConfirmation != nil || len(run.Run.Checkpoint) != 0 || run.Work != nil || run.Progress != nil || run.Task != nil || run.Run.Lease.Fence != 0 {
			return store.ErrBotConflict
		}
		version, err := tx.ReadVersion(ctx, botID, run.Run.Version)
		if err != nil {
			return err
		}
		run.Work = store.NewBotWorkState(store.BotWorkContract{BotID: botID, RunID: runID, Version: run.Run.Version, Instructions: run.Instructions, Charter: version.Charter})
		if err := run.Work.Validate(); err != nil {
			return err
		}
		data, err := encodeBotRunData(ctx, tx, *run)
		if err != nil {
			return err
		}
		var n int
		switch d := tx.(type) {
		case personalBotDB:
			n, err = d.client.BotRun.Update().Where(personalrun.IDEQ(runID), personalrun.BotIDEQ(botID), personalrun.StatusEQ(personalrun.StatusQueued)).SetData(data).Save(ctx)
			err = personalBotError(err)
		case teamBotDB:
			n, err = d.client.BotRun.Update().Where(teamrun.IDEQ(runID), teamrun.BotIDEQ(botID), teamrun.StatusEQ(teamrun.StatusQueued)).SetData(data).Save(ctx)
			err = teamBotError(err)
		default:
			return store.ErrBotUnsupported
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return store.ErrBotConflict
		}
		return nil
	})
}
