package entstore

import (
	"context"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// ReconcileRestoredBotWorkspaceHome replaces only a restore-held Home binding.
// It neither clears the recovery barrier nor resumes runs/schedules/approvals.
func ReconcileRestoredBotWorkspaceHome(ctx context.Context, bots store.BotStore, backend sandbox.BotHomeWorkspaceArchiveBackend, a *BotWorkspaceArchive, target *sandbox.BotHomeBinding) error {
	if a == nil || target == nil || backend == nil {
		return store.ErrBotInvalid
	}
	if err := a.Validate(); err != nil {
		return err
	}
	var r *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		r = s.taskRepository()
	case *teamBotStore:
		r = s.taskRepository()
	default:
		return store.ErrBotUnsupported
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		if err := tx.(botSkillDB).lockSkills(ctx, a.BotID); err != nil {
			return err
		}
		rec, err := tx.GetBot(ctx, a.BotID)
		if err != nil {
			return err
		}
		if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
			return err
		}
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || p.Kind != execution.PrincipalKindUser || p.Validate() != nil || p.Subject != rec.Bot.OwnerID.String() || target.Scope != (sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: a.BotID.String()}) {
			return store.ErrBotForbidden
		}
		held, _ := rec.Metadata["restore_reconciliation_required"].(bool)
		if !held || rec.Bot.Status == store.BotActive || rec.Bot.Home != a.Home || target.Validate() != nil || target.Scope.BotID != a.BotID.String() || target.Scope.UserID != rec.Bot.OwnerID.String() || target.Scope.OrgID != r.auth.orgSlug || r.auth.teamSlug != "" && target.Scope.TeamID != r.auth.teamSlug || target.Ref == a.Home.StorageKey {
			return store.ErrBotConflict
		}
		guard := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(c context.Context) error { return c.Err() })
		if err = backend.RestoreBotHomeWorkspace(guard, target, &a.Snapshot); err != nil {
			return err
		}
		home := a.Home
		home.Backend = string(target.Backend)
		home.StorageKey = target.Ref
		home.Revision++
		data, err := botJSONMap(home)
		if err != nil {
			return err
		}
		switch d := tx.(type) {
		case personalBotDB:
			_, err = d.client.Bot.Update().Where(personalbot.IDEQ(a.BotID), personalbot.RevisionEQ(rec.Bot.Revision)).SetHomeBinding(data).AddRevision(1).Save(ctx)
		case teamBotDB:
			_, err = d.client.Bot.Update().Where(teambot.IDEQ(a.BotID), teambot.RevisionEQ(rec.Bot.Revision)).SetHomeBinding(data).AddRevision(1).Save(ctx)
		default:
			return store.ErrBotUnsupported
		}
		return err
	})
}
