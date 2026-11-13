package entstore

import (
	"context"
	"time"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/ent/team/botapproval"
	"github.com/SAP/astonish/ent/team/botgoal"
	"github.com/SAP/astonish/ent/team/botoutbox"
	"github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/ent/team/bottrackeditem"
	"github.com/SAP/astonish/ent/team/botversion"
	"github.com/SAP/astonish/ent/team/cachediagnostic"
	"github.com/SAP/astonish/ent/team/chatsessionevent"
	"github.com/SAP/astonish/ent/team/memory"
	"github.com/SAP/astonish/ent/team/sandboxsession"
	"github.com/SAP/astonish/ent/team/scheduledjob"
	"github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/store"
)

type teamBotStore struct {
	credentials store.CredentialStore
	client      *teament.Client
	auth        botRepositoryAuth
}

func (s *teamBotStore) repository() *botRepository {
	return &botRepository{auth: s.auth, db: teamBotDB{client: s.client}}
}
func (s *teamBotStore) CreateDraft(ctx context.Context, r store.BotCreateRequest) (*store.Bot, error) {
	return s.repository().CreateDraft(ctx, r)
}
func (s *teamBotStore) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	return s.repository().Get(ctx, id)
}
func (s *teamBotStore) List(ctx context.Context, p store.BotPage) ([]store.Bot, error) {
	return s.repository().List(ctx, p)
}
func (s *teamBotStore) GetVersion(ctx context.Context, id store.BotID, v int64) (*store.BotVersion, error) {
	return s.repository().GetVersion(ctx, id, v)
}
func (s *teamBotStore) ApplyVersion(ctx context.Context, r store.BotVersionRequest) (*store.Bot, error) {
	return s.repository().ApplyVersion(ctx, r)
}

func (s *teamBotStore) ListGoals(ctx context.Context, id store.BotID) ([]store.BotGoal, error) {
	return s.repository().ListGoals(ctx, id)
}
func (s *teamBotStore) SaveGoal(ctx context.Context, req store.BotGoalUpdate) (*store.BotGoal, error) {
	return s.repository().SaveGoal(ctx, req)
}
func (s *teamBotStore) ListActivity(ctx context.Context, id store.BotID, after int64, limit int) ([]store.BotActivity, error) {
	return s.repository().ListActivity(ctx, id, after, limit)
}
func (d teamBotDB) ReadGoals(ctx context.Context, id uuid.UUID) ([]store.BotGoal, error) {
	rows, err := d.client.BotGoal.Query().Where(botgoal.BotIDEQ(id)).Order(teament.Asc(botgoal.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.BotGoal, 0, len(rows))
	for _, row := range rows {
		var g store.BotGoal
		if err := botDecode(row.Data, &g); err != nil {
			return nil, err
		}
		if g.ID != row.ID || g.BotID != id {
			return nil, store.ErrBotInvalid
		}
		result = append(result, g)
	}
	return result, nil
}
func (d teamBotDB) ReadGoal(ctx context.Context, id, goalID uuid.UUID) (*store.BotGoal, error) {
	row, err := d.client.BotGoal.Query().Where(botgoal.IDEQ(goalID), botgoal.BotIDEQ(id)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	var g store.BotGoal
	if err := botDecode(row.Data, &g); err != nil {
		return nil, err
	}
	if g.ID != goalID || g.BotID != id || g.Revision != row.Revision {
		return nil, store.ErrBotInvalid
	}
	return &g, nil
}
func (d teamBotDB) UpdateGoal(ctx context.Context, g store.BotGoal, revision int64) error {
	data, err := botJSONMap(g)
	if err != nil {
		return err
	}
	u := d.client.BotGoal.Update().Where(botgoal.IDEQ(g.ID), botgoal.BotIDEQ(g.BotID), botgoal.RevisionEQ(revision)).SetRevision(g.Revision).SetTitle(g.Title).SetStatus(botgoal.Status(g.Status)).SetData(data)
	if g.DueAt == nil {
		u.ClearDueAt()
	} else {
		u.SetDueAt(*g.DueAt)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) AdvanceSequence(ctx context.Context, id uuid.UUID, seq int64) error {
	n, err := d.client.Bot.Update().Where(bot.IDEQ(id), bot.LastSeqEQ(seq)).AddLastSeq(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) ReadActivity(ctx context.Context, id uuid.UUID, after int64, limit int) ([]store.BotActivity, error) {
	rows, err := d.client.BotOutbox.Query().Where(botoutbox.BotIDEQ(id), botoutbox.SequenceGT(after)).Order(teament.Asc(botoutbox.FieldSequence)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.BotActivity, 0, len(rows))
	for _, row := range rows {
		var a store.BotActivity
		if err := botDecode(row.Payload, &a); err != nil {
			return nil, err
		}
		if a.BotID != id || a.Sequence != row.Sequence {
			return nil, store.ErrBotInvalid
		}
		result = append(result, a)
	}
	return result, nil
}

func (s *teamBotStore) RequestRun(ctx context.Context, q store.BotRunRequest) (*store.BotRun, error) {
	return s.repository().RequestRun(ctx, q)
}
func (s *teamBotStore) GetRun(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	return s.repository().GetRun(ctx, id, rid)
}
func (s *teamBotStore) ListRuns(ctx context.Context, id store.BotID, p store.BotPage) ([]store.BotRun, error) {
	return s.repository().ListRuns(ctx, id, p)
}
func (s *teamBotStore) ClaimRun(ctx context.Context, q store.BotClaimRequest) (store.BotLease, error) {
	return s.repository().ClaimRun(ctx, q)
}
func (s *teamBotStore) RenewLease(ctx context.Context, l store.BotLease, expires time.Time) (store.BotLease, error) {
	return s.repository().RenewLease(ctx, l, expires)
}
func (s *teamBotStore) AppendActivity(ctx context.Context, l store.BotLease, a store.BotActivity) (store.BotActivity, error) {
	return s.repository().AppendActivity(ctx, l, a)
}
func decodeTeamBotRun(row *teament.BotRun) (*botStoredRun, error) {
	var saved botStoredRun
	if err := botDecode(row.Data, &saved); err != nil {
		return nil, err
	}
	if !saved.Run.ExecutionMode.Valid() || saved.Run.ID != row.ID || saved.Run.BotID != row.BotID || saved.Run.Version != row.Version || saved.Run.SessionID.String() != row.SessionID {
		return nil, store.ErrBotInvalid
	}
	saved.RequestHash = row.RequestHash
	saved.Run.Status = store.RunStatus(row.Status)
	saved.Run.Lease = store.BotLease{BotID: row.BotID, RunID: row.ID, WorkerID: row.WorkerID, Fence: row.Fence}
	if row.LeaseExpiresAt != nil {
		saved.Run.Lease.ExpiresAt = *row.LeaseExpiresAt
	}
	return &saved, nil
}
func (d teamBotDB) FindRun(ctx context.Context, id uuid.UUID, key string) (*botStoredRun, error) {
	row, err := d.client.BotRun.Query().Where(botrun.BotIDEQ(id), botrun.IdempotencyKeyEQ(key)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	return decodeTeamBotRun(row)
}
func (d teamBotDB) ReadRun(ctx context.Context, id, rid uuid.UUID) (*botStoredRun, error) {
	row, err := d.client.BotRun.Query().Where(botrun.BotIDEQ(id), botrun.IDEQ(rid)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	return decodeTeamBotRun(row)
}
func (d teamBotDB) ReadRuns(ctx context.Context, id uuid.UUID, limit int, after uuid.UUID) ([]store.BotRun, error) {
	q := d.client.BotRun.Query().Where(botrun.BotIDEQ(id)).Order(teament.Asc(botrun.FieldID)).Limit(limit)
	if after != uuid.Nil {
		q = q.Where(botrun.IDGT(after))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotRun, 0, len(rows))
	for _, row := range rows {
		r, err := decodeTeamBotRun(row)
		if err != nil {
			return nil, err
		}
		out = append(out, r.Run)
	}
	return out, nil
}
func (d teamBotDB) CountRunsSince(ctx context.Context, id uuid.UUID, since time.Time) (int, error) {
	return d.client.BotRun.Query().Where(botrun.BotIDEQ(id), botrun.CreatedAtGTE(since)).Count(ctx)
}
func (d teamBotDB) InsertRun(ctx context.Context, saved botStoredRun, owner uuid.UUID) error {
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	r := saved.Run
	_, err = d.client.BotRun.Create().SetID(r.ID).SetBotID(r.BotID).SetVersion(r.Version).SetSessionID(r.SessionID.String()).SetIdempotencyKey(r.IdempotencyKey).SetRequestHash(saved.RequestHash).SetStatus(botrun.StatusQueued).SetData(data).SetCreatedAt(r.CreatedAt).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	_, err = d.client.Session.Create().SetID(r.SessionID.String()).SetUserID(owner).SetBotID(r.BotID).SetBotRunID(r.ID).SetPurpose(session.PurposeBotRun).SetRetention(session.RetentionBot).SetMetadata(map[string]any{"app_name": "astonish"}).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) ClaimLease(ctx context.Context, b botStoredRecord, saved botStoredRun, l store.BotLease) error {
	n, err := d.client.Bot.Update().Where(bot.IDEQ(l.BotID), bot.StatusIn(teamInteractiveRunStatuses(saved.Run)...), bot.VersionEQ(saved.Run.Version), bot.FenceEQ(b.Fence), bot.LastSeqEQ(b.LastSequence), bot.ActiveRunIDIsNil()).SetActiveRunID(l.RunID).SetLeaseExpiresAt(l.ExpiresAt).SetFence(l.Fence).AddLastSeq(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	n, err = d.client.BotRun.Update().Where(botrun.IDEQ(l.RunID), botrun.BotIDEQ(l.BotID), botrun.StatusEQ(botrun.StatusQueued)).SetStatus(botrun.StatusRunning).SetWorkerID(l.WorkerID).SetFence(l.Fence).SetLeaseExpiresAt(l.ExpiresAt).SetData(data).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) RenewRunLease(ctx context.Context, l store.BotLease, now, expires time.Time) error {
	saved, err := d.ReadRun(ctx, l.BotID, l.RunID)
	if err != nil {
		return err
	}
	if saved.Run.Intent.RequiredStatus() == "" {
		return store.ErrBotForbidden
	}

	n, err := d.client.Bot.Update().Where(bot.IDEQ(l.BotID), bot.StatusIn(teamInteractiveRunStatuses(saved.Run)...), bot.ActiveRunIDEQ(l.RunID), bot.FenceEQ(l.Fence), bot.LeaseExpiresAtGT(now), bot.LeaseExpiresAtLTE(expires)).SetLeaseExpiresAt(expires).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	n, err = d.client.BotRun.Update().Where(botrun.IDEQ(l.RunID), botrun.BotIDEQ(l.BotID), botrun.StatusEQ(botrun.StatusRunning), botrun.WorkerIDEQ(l.WorkerID), botrun.FenceEQ(l.Fence), botrun.LeaseExpiresAtGT(now), botrun.LeaseExpiresAtLTE(expires)).SetLeaseExpiresAt(expires).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (d teamBotDB) AdvanceLeaseSequence(ctx context.Context, l store.BotLease, seq int64, now time.Time) error {
	saved, err := d.ReadRun(ctx, l.BotID, l.RunID)
	if err != nil {
		return err
	}
	if saved.Run.Intent.RequiredStatus() == "" {
		return store.ErrBotForbidden
	}

	n, err := d.client.Bot.Update().Where(bot.IDEQ(l.BotID), bot.StatusIn(teamInteractiveRunStatuses(saved.Run)...), bot.ActiveRunIDEQ(l.RunID), bot.FenceEQ(l.Fence), bot.LastSeqEQ(seq), bot.LeaseExpiresAtGT(now)).AddLastSeq(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (s *teamBotStore) RequestApproval(ctx context.Context, l store.BotLease, a store.BotApproval) (*store.BotApproval, error) {
	return s.repository().RequestApproval(ctx, l, a)
}
func (s *teamBotStore) ListApprovals(ctx context.Context, id store.BotID, p store.BotPage) ([]store.BotApproval, error) {
	return s.repository().ListApprovals(ctx, id, p)
}
func (s *teamBotStore) DecideApproval(ctx context.Context, q store.BotApprovalDecision) (*store.BotApproval, error) {
	return s.repository().DecideApproval(ctx, q)
}
func (s *teamBotStore) ConsumeApproval(ctx context.Context, q store.BotApprovalConsumption) error {
	return s.repository().ConsumeApproval(ctx, q)
}
func decodeTeamBotApproval(row *teament.BotApproval) (*store.BotApproval, error) {
	var a store.BotApproval
	if err := botDecode(row.Data, &a); err != nil {
		return nil, err
	}
	digest, err := a.Action.Digest()
	if err != nil || digest != row.ActionDigest || a.ID != row.ID || a.BotID != row.BotID || a.RunID != row.RunID || a.Version != row.Version {
		return nil, store.ErrBotInvalid
	}
	a.Status = store.ApprovalStatus(row.Status)
	a.ExpiresAt = row.ExpiresAt
	return &a, nil
}
func (d teamBotDB) FindApproval(ctx context.Context, id, rid uuid.UUID, digest string) (*store.BotApproval, error) {
	row, err := d.client.BotApproval.Query().Where(botapproval.BotIDEQ(id), botapproval.RunIDEQ(rid), botapproval.ActionDigestEQ(digest)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	return decodeTeamBotApproval(row)
}
func (d teamBotDB) ReadApproval(ctx context.Context, id, aid uuid.UUID) (*store.BotApproval, error) {
	row, err := d.client.BotApproval.Query().Where(botapproval.BotIDEQ(id), botapproval.IDEQ(aid)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	return decodeTeamBotApproval(row)
}
func (d teamBotDB) ReadApprovals(ctx context.Context, id uuid.UUID, limit int, after uuid.UUID) ([]store.BotApproval, error) {
	q := d.client.BotApproval.Query().Where(botapproval.BotIDEQ(id)).Order(teament.Asc(botapproval.FieldID)).Limit(limit)
	if after != uuid.Nil {
		q = q.Where(botapproval.IDGT(after))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotApproval, 0, len(rows))
	for _, row := range rows {
		a, err := decodeTeamBotApproval(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}
func (d teamBotDB) InsertApproval(ctx context.Context, a store.BotApproval) error {
	data, err := botJSONMap(a)
	if err != nil {
		return err
	}
	_, err = d.client.BotApproval.Create().SetID(a.ID).SetBotID(a.BotID).SetRunID(a.RunID).SetVersion(a.Version).SetActionDigest(a.ActionDigest).SetStatus(botapproval.StatusPending).SetData(data).SetExpiresAt(a.ExpiresAt).SetCreatedAt(a.CreatedAt).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) UpdateApproval(ctx context.Context, a store.BotApproval, from store.ApprovalStatus, now time.Time) error {
	data, err := botJSONMap(a)
	if err != nil {
		return err
	}
	n, err := d.client.BotApproval.Update().Where(botapproval.IDEQ(a.ID), botapproval.BotIDEQ(a.BotID), botapproval.RunIDEQ(a.RunID), botapproval.VersionEQ(a.Version), botapproval.ActionDigestEQ(a.ActionDigest), botapproval.StatusEQ(botapproval.Status(from)), botapproval.ExpiresAtGT(now)).SetStatus(botapproval.Status(a.Status)).SetData(data).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (s *teamBotStore) CommitRun(ctx context.Context, q store.BotRunCommit) error {
	return s.repository().CommitRun(ctx, q)
}
func (s *teamBotStore) RecoverRun(ctx context.Context, l store.BotLease) error {
	return s.repository().RecoverRun(ctx, l)
}
func (d teamBotDB) FinishRun(ctx context.Context, b botStoredRecord, saved botStoredRun, l store.BotLease, now time.Time, recoverExpired bool) error {
	root := d.client.Bot.Update().Where(bot.IDEQ(l.BotID), bot.ActiveRunIDEQ(l.RunID), bot.FenceEQ(l.Fence), bot.LastSeqEQ(b.LastSequence), bot.RevisionEQ(b.Bot.Revision), bot.VersionEQ(b.Bot.Version))
	run := d.client.BotRun.Update().Where(botrun.IDEQ(l.RunID), botrun.BotIDEQ(l.BotID), botrun.VersionEQ(saved.Run.Version), botrun.FenceEQ(l.Fence), botrun.WorkerIDEQ(l.WorkerID), botrun.StatusEQ(botrun.StatusRunning))
	if recoverExpired {
		root.Where(bot.LeaseExpiresAtLTE(now))
		run.Where(botrun.LeaseExpiresAtLTE(now))
	} else {
		root.Where(bot.StatusIn(teamInteractiveRunStatuses(saved.Run)...), bot.LeaseExpiresAtGT(now))
		run.Where(botrun.LeaseExpiresAtGT(now))
	}
	n, err := root.ClearActiveRunID().ClearLeaseExpiresAt().AddFence(1).AddLastSeq(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	data, err := botJSONMap(saved)
	if err != nil {
		return err
	}
	n, err = run.SetStatus(botrun.Status(saved.Run.Status)).SetData(data).ClearLeaseExpiresAt().Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) RevokeRunApprovals(ctx context.Context, id, rid uuid.UUID) error {
	_, err := d.client.BotApproval.Update().Where(botapproval.BotIDEQ(id), botapproval.RunIDEQ(rid), botapproval.StatusIn(botapproval.StatusPending, botapproval.StatusApproved)).SetStatus(botapproval.StatusRevoked).Save(ctx)
	return teamBotError(err)
}

func (s *teamBotStore) Pause(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return s.repository().Pause(ctx, q)
}
func (s *teamBotStore) Stop(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return s.repository().Stop(ctx, q)
}
func (s *teamBotStore) BeginDelete(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return s.repository().BeginDelete(ctx, q)
}
func (s *teamBotStore) Restore(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return s.repository().Restore(ctx, q)
}
func (d teamBotDB) SetLifecycle(ctx context.Context, rec botStoredRecord, next store.Bot) error {
	u := d.client.Bot.Update().Where(bot.IDEQ(rec.Bot.ID), bot.RevisionEQ(rec.Bot.Revision), bot.LastSeqEQ(rec.LastSequence), bot.FenceEQ(rec.Fence), bot.StatusEQ(bot.Status(rec.Bot.Status))).SetStatus(bot.Status(next.Status)).SetRevision(next.Revision).AddLastSeq(1).AddFence(1).SetUpdatedAt(next.UpdatedAt).SetData(botLifecycleMetadata(rec, next)).SetCleanupComplete(false)
	// Preserve the active-run pointer as a teardown barrier. A lifecycle change
	// invalidates authority; it does not prove the old container has stopped.
	if next.DeletedAt == nil {
		u.ClearDeletedAt()
	} else {
		u.SetDeletedAt(*next.DeletedAt)
	}
	if next.RestoreUntil == nil {
		u.ClearPurgeAfter()
	} else {
		u.SetPurgeAfter(*next.RestoreUntil)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) QuiesceBotRuns(ctx context.Context, id uuid.UUID, stop bool, now time.Time) ([]store.BotRun, error) {
	states := []botrun.Status{botrun.StatusRunning, botrun.StatusQueued}
	if stop {
		states = append(states, botrun.StatusWaitingApproval)
	}
	rows, err := d.client.BotRun.Query().Where(botrun.BotIDEQ(id), botrun.StatusIn(states...)).Order(teament.Asc(botrun.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotRun, 0, len(rows))
	for _, row := range rows {
		saved, err := decodeTeamBotRun(row)
		if err != nil {
			return nil, err
		}
		next := quiescedBotRun(*saved, stop, now)
		data, err := botJSONMap(next)
		if err != nil {
			return nil, err
		}
		n, err := d.client.BotRun.Update().Where(botrun.IDEQ(row.ID), botrun.BotIDEQ(id), botrun.StatusEQ(row.Status), botrun.FenceEQ(row.Fence)).SetStatus(botrun.Status(next.Run.Status)).SetData(data).ClearLeaseExpiresAt().Save(ctx)
		if err != nil {
			return nil, teamBotError(err)
		}
		if n != 1 {
			return nil, store.ErrBotConflict
		}
		out = append(out, next.Run)
	}
	return out, nil
}
func (d teamBotDB) RevokeBotApprovals(ctx context.Context, id uuid.UUID, all bool) error {
	states := []botapproval.Status{botapproval.StatusApproved}
	if all {
		states = append(states, botapproval.StatusPending)
	}
	_, err := d.client.BotApproval.Update().Where(botapproval.BotIDEQ(id), botapproval.StatusIn(states...)).SetStatus(botapproval.StatusRevoked).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) DisableBotSchedules(ctx context.Context, id uuid.UUID) error {
	rows, err := d.client.ScheduledJob.Query().Where(scheduledjob.ModeEQ(scheduledjob.ModeBotTrigger)).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		owner, ok := row.Payload["bot_id"].(string)
		if !ok || owner != id.String() {
			continue
		}
		if err := d.client.ScheduledJob.UpdateOneID(row.ID).SetStatus(scheduledjob.StatusPaused).ClearNextRunAt().Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *teamBotStore) MarkRead(ctx context.Context, cursor store.BotReadCursor) error {
	return s.repository().MarkRead(ctx, cursor)
}
func (d teamBotDB) SaveReadCursor(ctx context.Context, rec botStoredRecord, cursor store.BotReadCursor) error {
	data, err := botCursorMetadata(rec, cursor)
	if err != nil || data == nil {
		return err
	}
	n, err := d.client.Bot.Update().Where(bot.IDEQ(rec.Bot.ID), bot.RevisionEQ(rec.Bot.Revision), bot.LastSeqEQ(rec.LastSequence)).SetData(data).AddRevision(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (s *teamBotStore) CompleteGoal(ctx context.Context, q store.BotGoalCompletion) (*store.BotGoal, error) {
	return s.repository().CompleteGoal(ctx, q)
}
func (s *teamBotStore) SaveTrackedItem(ctx context.Context, id store.BotID, item store.BotTrackedItem, revision int64) error {
	return s.repository().SaveTrackedItem(ctx, id, item, revision)
}
func (s *teamBotStore) ListTrackedItems(ctx context.Context, id store.BotID, goalID uuid.UUID) ([]store.BotTrackedItem, error) {
	return s.repository().ListTrackedItems(ctx, id, goalID)
}
func (d teamBotDB) ReadTrackedItems(ctx context.Context, goalID uuid.UUID) ([]store.BotTrackedItem, error) {
	rows, err := d.client.BotTrackedItem.Query().Where(bottrackeditem.GoalIDEQ(goalID)).Order(teament.Asc(bottrackeditem.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotTrackedItem, 0, len(rows))
	for _, row := range rows {
		var item store.BotTrackedItem
		if err := botDecode(row.Data, &item); err != nil {
			return nil, err
		}
		if item.ID != row.ID || item.GoalID != goalID || string(item.Status) != string(row.Status) {
			return nil, store.ErrBotInvalid
		}
		out = append(out, item)
	}
	return out, nil
}
func (d teamBotDB) WriteTrackedItem(ctx context.Context, item store.BotTrackedItem, revision int64) error {
	data, err := botJSONMap(item)
	if err != nil {
		return err
	}
	old, err := d.client.BotTrackedItem.Get(ctx, item.ID)
	if teament.IsNotFound(err) {
		_, err = d.client.BotTrackedItem.Create().SetID(item.ID).SetGoalID(item.GoalID).SetRevision(revision).SetStatus(bottrackeditem.Status(item.Status)).SetNillableDueAt(item.DueAt).SetData(data).Save(ctx)
		return teamBotError(err)
	}
	if err != nil {
		return err
	}
	if old.GoalID != item.GoalID {
		return store.ErrBotNotFound
	}
	if old.Status == bottrackeditem.StatusCompleted || old.Status == bottrackeditem.StatusCancelled {
		return store.ErrBotConflict
	}
	u := d.client.BotTrackedItem.Update().Where(bottrackeditem.IDEQ(item.ID), bottrackeditem.GoalIDEQ(item.GoalID), bottrackeditem.RevisionEQ(old.Revision)).SetRevision(revision).SetStatus(bottrackeditem.Status(item.Status)).SetData(data)
	if item.DueAt == nil {
		u.ClearDueAt()
	} else {
		u.SetDueAt(*item.DueAt)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) DisableGoalSchedules(ctx context.Context, id, goalID uuid.UUID) error {
	rows, err := d.client.ScheduledJob.Query().Where(scheduledjob.ModeEQ(scheduledjob.ModeBotTrigger)).All(ctx)
	if err != nil {
		return err
	}
	rec, err := d.GetBot(ctx, id)
	if err != nil {
		return err
	}
	version, err := d.ReadVersion(ctx, id, rec.Bot.Version)
	if err != nil {
		return err
	}
	owned := goalTriggerSchedules(version, goalID)
	for _, row := range rows {
		b, _ := row.Payload["bot_id"].(string)
		g, _ := row.Payload["goal_id"].(string)
		if b != id.String() || (g != goalID.String() && !owned[row.ID]) {
			continue
		}
		if err := d.client.ScheduledJob.UpdateOneID(row.ID).SetStatus(scheduledjob.StatusPaused).ClearNextRunAt().Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *teamBotStore) Activate(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return s.repository().Activate(ctx, q)
}
func (s *teamBotStore) Resume(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return s.repository().Resume(ctx, q)
}
func (s *teamBotStore) Correct(ctx context.Context, q store.BotCorrection) error {
	return s.repository().Correct(ctx, q)
}

var _ store.BotCorrectionReconciler = (*teamBotStore)(nil)

func (s *teamBotStore) ReconcileCorrection(ctx context.Context, id store.BotID, correctionID uuid.UUID, memories store.MemoryStore) error {
	return s.repository().ReconcileCorrection(ctx, id, correctionID, memories)
}
func (d teamBotDB) ReadCorrectionMemory(ctx context.Context, id string) (*store.MemorySearchResult, error) {
	return (&teamMemoryStore{client: d.client}).Get(ctx, id)
}
func (d teamBotDB) CorrectionSessionBot(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	row, err := d.client.Session.Get(ctx, id.String())
	if err != nil {
		return uuid.Nil, teamBotError(err)
	}
	if row.BotID == nil {
		return uuid.Nil, store.ErrBotForbidden
	}
	return *row.BotID, nil
}
func (d teamBotDB) ApplyCorrectionMemory(ctx context.Context, id, sessionID uuid.UUID, previous, replacement string, sqlite bool) error {
	u := d.client.Memory.Update().Where(memory.IDEQ(id), memory.SessionIDEQ(sessionID), memory.ChunkTextEQ(previous)).SetChunkText(replacement).ClearEmbedding()
	if sqlite {
		u.SetTsv(replacement)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (d teamBotDB) FindActivity(ctx context.Context, id uuid.UUID, key string) (*store.BotActivity, error) {
	row, err := d.client.BotOutbox.Query().Where(botoutbox.BotIDEQ(id), botoutbox.DeduplicationKeyEQ(key)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	var a store.BotActivity
	if err := botDecode(row.Payload, &a); err != nil {
		return nil, err
	}
	if a.BotID != id || a.Sequence != row.Sequence || string(a.Kind) != row.Kind {
		return nil, store.ErrBotInvalid
	}
	return &a, nil
}
func (d teamBotDB) AdvanceCorrection(ctx context.Context, rec botStoredRecord, now time.Time) error {
	n, err := d.client.Bot.Update().Where(bot.IDEQ(rec.Bot.ID), bot.RevisionEQ(rec.Bot.Revision), bot.LastSeqEQ(rec.LastSequence), bot.StatusNEQ(bot.StatusDeleted)).AddRevision(1).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func (s *teamBotStore) PurgeDue(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return s.repository().PurgeDue(ctx, cutoff, limit)
}
func (d teamBotDB) ListPurgeable(ctx context.Context, cutoff time.Time, limit int) ([]botStoredRecord, error) {
	rows, err := d.client.Bot.Query().Where(bot.StatusEQ(bot.StatusDeleted), bot.PurgeAfterLTE(cutoff), bot.CleanupCompleteEQ(true), bot.ActiveRunIDIsNil(), bot.LeaseExpiresAtIsNil()).Order(teament.Asc(bot.FieldPurgeAfter), teament.Asc(bot.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]botStoredRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := teamBotRecord(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, nil
}

// PurgeBot must only be called on a transaction-bound client.
func (d teamBotDB) PurgeBot(ctx context.Context, rec botStoredRecord, cutoff time.Time) error {
	id := rec.Bot.ID
	// Acquire the root write lock before touching children; restore and all Bot
	// mutations also CAS this root. Recheck cleanup rather than trust the inventory.
	n, err := d.client.Bot.Update().Where(bot.IDEQ(id), bot.RevisionEQ(rec.Bot.Revision), bot.LastSeqEQ(rec.LastSequence), bot.FenceEQ(rec.Fence), bot.StatusEQ(bot.StatusDeleted), bot.PurgeAfterLTE(cutoff), bot.CleanupCompleteEQ(true), bot.ActiveRunIDIsNil(), bot.LeaseExpiresAtIsNil()).AddRevision(1).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	busy, err := d.client.BotRun.Query().Where(botrun.BotIDEQ(id), botrun.StatusIn(botrun.StatusQueued, botrun.StatusRunning, botrun.StatusWaitingApproval)).Exist(ctx)
	if err != nil {
		return err
	}
	if busy {
		return store.ErrBotConflict
	}
	sessions, err := d.client.Session.Query().Where(session.BotIDEQ(id)).All(ctx)
	if err != nil {
		return err
	}
	// Follow persisted parent edges, including legacy child sessions without
	// Bot attribution. Explicit foreign ownership is never adopted.
	seen := make(map[string]bool)
	var sessionIDs []string
	for _, s := range sessions {
		if s.Retention != session.RetentionBot || (s.Purpose != session.PurposeBotHome && s.Purpose != session.PurposeBotRun) {
			return store.ErrBotConflict
		}
		seen[s.ID] = true
	}
	for i := 0; i < len(sessions); i++ {
		s := sessions[i]
		sessionIDs = append(sessionIDs, s.ID)
		children, err := d.client.Session.Query().Where(session.ParentIDEQ(s.ID)).All(ctx)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.BotID != nil && *child.BotID != id || child.UserID != nil && *child.UserID != rec.Bot.OwnerID {
				return store.ErrBotConflict
			}
			if !seen[child.ID] {
				seen[child.ID] = true
				sessions = append(sessions, child)
			}
		}
	}
	for _, s := range sessions {
		// Physical cleanup has already been attested. Remove only registries
		// attributed to this exact session, never an unrelated sandbox.
		registries, err := d.client.SandboxSession.Query().Where(sandboxsession.ChatSessionIDEQ(s.ID)).All(ctx)
		if err != nil {
			return err
		}
		for _, registry := range registries {
			if registry.State != sandboxsession.StateTerminated {
				return store.ErrBotConflict
			}
		}
		if _, err := d.client.SandboxSession.Delete().Where(sandboxsession.ChatSessionIDEQ(s.ID), sandboxsession.StateEQ(sandboxsession.StateTerminated)).Exec(ctx); err != nil {
			return err
		}
		memorySessionID, err := uuid.Parse(s.ID)
		if err != nil {
			return store.ErrBotConflict
		}
		if _, err := d.client.Memory.Delete().Where(memory.SessionIDEQ(memorySessionID)).Exec(ctx); err != nil {
			return err
		}
		if _, err := d.client.CacheDiagnostic.Delete().Where(cachediagnostic.SessionIDEQ(s.ID)).Exec(ctx); err != nil {
			return err
		}
		if _, err := d.client.ChatSessionEvent.Delete().Where(chatsessionevent.ChatSessionIDEQ(s.ID)).Exec(ctx); err != nil {
			return err
		}
		if _, err := d.client.SessionEvent.Delete().Where(sessionevent.SessionIDEQ(s.ID)).Exec(ctx); err != nil {
			return err
		}
	}
	if _, err := d.client.Session.Delete().Where(session.IDIn(sessionIDs...)).Exec(ctx); err != nil {
		return err
	}
	jobs, err := d.client.ScheduledJob.Query().Where(scheduledjob.ModeEQ(scheduledjob.ModeBotTrigger)).All(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		owner, _ := j.Payload["bot_id"].(string)
		if owner != id.String() {
			continue
		}
		if j.Status != scheduledjob.StatusPaused || j.NextRunAt != nil {
			return store.ErrBotConflict
		}
		if err := d.client.ScheduledJob.DeleteOneID(j.ID).Exec(ctx); err != nil {
			return err
		}
	}
	if _, err := d.client.BotApproval.Delete().Where(botapproval.BotIDEQ(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err := d.client.BotRun.Delete().Where(botrun.BotIDEQ(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err := d.client.BotTrackedItem.Delete().Where(bottrackeditem.HasGoalWith(botgoal.BotIDEQ(id))).Exec(ctx); err != nil {
		return err
	}
	if err := d.purgeSkills(ctx, id); err != nil {
		return err
	}
	if _, err := d.client.BotGoal.Delete().Where(botgoal.BotIDEQ(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err := d.client.BotVersion.Delete().Where(botversion.BotIDEQ(id)).Exec(ctx); err != nil {
		return err
	}
	if _, err := d.client.BotOutbox.Delete().Where(botoutbox.BotIDEQ(id)).Exec(ctx); err != nil {
		return err
	}
	return teamBotError(d.client.Bot.DeleteOneID(id).Exec(ctx))
}

type teamBotDB struct {
	client      *teament.Client
	policyOuter *teament.Client
	afterCommit *[]func()
}

func teamBotError(err error) error {
	if teament.IsNotFound(err) {
		return store.ErrBotNotFound
	}
	if teament.IsConstraintError(err) {
		return store.ErrBotConflict
	}
	return err
}
func (d teamBotDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	tx, err := d.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var callbacks []func()
	if err := fn(teamBotDB{client: tx.Client(), policyOuter: d.client, afterCommit: &callbacks}); err != nil {
		return err
	}
	if err := teamBotError(tx.Commit()); err != nil {
		return err
	}
	for _, callback := range callbacks {
		callback()
	}
	return nil
}
func teamBotRecord(b *teament.Bot) (*botStoredRecord, error) {
	id, err := uuid.Parse(b.HomeSessionID)
	if err != nil {
		return nil, err
	}
	var data struct {
		CreateHash     string          `json:"create_hash"`
		PreviousStatus store.BotStatus `json:"previous_status"`
	}
	if err := botDecode(b.Data, &data); err != nil {
		return nil, err
	}
	result := &botStoredRecord{Bot: store.Bot{ID: b.ID, OwnerID: b.OwnerID, Name: b.Name, Scope: store.BotScopeTeam, Status: store.BotStatus(b.Status), PreviousStatus: data.PreviousStatus, Revision: b.Revision, Version: b.Version, HomeSessionID: id, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt, DeletedAt: b.DeletedAt, RestoreUntil: b.PurgeAfter}, CreateKey: b.CreateKey, CreateHash: data.CreateHash, LastSequence: b.LastSeq, Fence: b.Fence, ActiveRunID: b.ActiveRunID, LeaseExpiresAt: b.LeaseExpiresAt, Metadata: b.Data}
	result.Bot.PermanentDelete, _ = b.Data["permanent_delete"].(bool)
	if b.HomeBinding != nil {
		if err := botDecode(b.HomeBinding, &result.Bot.Home); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (d teamBotDB) GetBot(ctx context.Context, id uuid.UUID) (*botStoredRecord, error) {
	b, err := d.client.Bot.Get(ctx, id)
	if err != nil {
		return nil, teamBotError(err)
	}
	return teamBotRecord(b)
}
func (d teamBotDB) FindCreate(ctx context.Context, key string) (*botStoredRecord, error) {
	b, err := d.client.Bot.Query().Where(bot.CreateKeyEQ(key)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	return teamBotRecord(b)
}
func (d teamBotDB) ListBots(ctx context.Context, limit int, after uuid.UUID) ([]botStoredRecord, error) {
	q := d.client.Bot.Query().Order(teament.Asc(bot.FieldID)).Limit(limit)
	if after != uuid.Nil {
		q = q.Where(bot.IDGT(after))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]botStoredRecord, 0, len(rows))
	for _, row := range rows {
		b, err := teamBotRecord(row)
		if err != nil {
			return nil, err
		}
		result = append(result, *b)
	}
	return result, nil
}
func (d teamBotDB) InsertBot(ctx context.Context, r botStoredRecord) error {
	b := r.Bot
	_, err := d.client.Bot.Create().SetID(b.ID).SetOwnerID(b.OwnerID).SetName(b.Name).SetStatus(bot.Status(b.Status)).SetRevision(b.Revision).SetVersion(b.Version).SetHomeSessionID(b.HomeSessionID.String()).SetCreateKey(r.CreateKey).SetLastSeq(r.LastSequence).SetData(map[string]any{"create_hash": r.CreateHash, botProvisioningTracked: true}).SetCreatedAt(b.CreatedAt).SetUpdatedAt(b.UpdatedAt).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) UpdateVersion(ctx context.Context, id uuid.UUID, revision, version, seq int64, now time.Time) error {
	n, err := d.client.Bot.Update().Where(bot.IDEQ(id), bot.RevisionEQ(revision), bot.LastSeqEQ(seq-1), bot.StatusIn(bot.StatusDraft, bot.StatusPaused, bot.StatusStopped)).AddRevision(1).SetVersion(version).SetLastSeq(seq).SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return teamBotError(err)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) ReadVersion(ctx context.Context, id uuid.UUID, version int64) (*store.BotVersion, error) {
	row, err := d.client.BotVersion.Query().Where(botversion.BotIDEQ(id), botversion.VersionEQ(version)).Only(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	var v store.BotVersion
	if err := botDecode(row.Definition, &v); err != nil {
		return nil, err
	}
	if v.BotID != id || v.Version != version || v.SchemaVersion != store.BotSchemaVersion {
		return nil, store.ErrBotInvalid
	}
	return &v, nil
}
func (d teamBotDB) InsertVersion(ctx context.Context, v store.BotVersion, hash string) error {
	data, err := botJSONMap(v)
	if err != nil {
		return err
	}
	_, err = d.client.BotVersion.Create().SetBotID(v.BotID).SetVersion(v.Version).SetSchemaVersion(v.SchemaVersion).SetDefinition(data).SetContentHash(hash).SetCreatedBy(v.CreatedBy).SetChangeReason(v.ChangeReason).SetCreatedAt(v.CreatedAt).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) InsertHomeSession(ctx context.Context, b store.Bot) error {
	_, err := d.client.Session.Create().SetID(b.HomeSessionID.String()).SetUserID(b.OwnerID).SetBotID(b.ID).SetPurpose(session.PurposeBotHome).SetRetention(session.RetentionBot).SetTitle(b.Name).SetMetadata(map[string]any{"app_name": "astonish"}).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) InsertGoal(ctx context.Context, g store.BotGoal) error {
	data, err := botJSONMap(g)
	if err != nil {
		return err
	}
	_, err = d.client.BotGoal.Create().SetID(g.ID).SetBotID(g.BotID).SetRevision(g.Revision).SetTitle(g.Title).SetStatus(botgoal.Status(g.Status)).SetNillableDueAt(g.DueAt).SetData(data).Save(ctx)
	return teamBotError(err)
}
func (d teamBotDB) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	data, err := botJSONMap(a)
	if err != nil {
		return err
	}
	_, err = d.client.BotOutbox.Create().SetBotID(a.BotID).SetSequence(a.Sequence).SetKind(string(a.Kind)).SetDeduplicationKey(key).SetPayload(data).SetAvailableAt(a.CreatedAt).SetCreatedAt(a.CreatedAt).Save(ctx)
	return teamBotError(err)
}
