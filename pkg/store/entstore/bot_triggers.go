package entstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

var _ store.BotTriggerStore = (*personalBotStore)(nil)
var _ store.BotTriggerStore = (*teamBotStore)(nil)

func (s *personalBotStore) ChangeTrigger(ctx context.Context, q store.BotTriggerChange) (*store.Bot, error) {
	return s.repository().ChangeTrigger(ctx, q)
}
func (s *teamBotStore) ChangeTrigger(ctx context.Context, q store.BotTriggerChange) (*store.Bot, error) {
	return s.repository().ChangeTrigger(ctx, q)
}
func (s *personalBotStore) AdmitExternalRun(ctx context.Context, q store.BotExternalEvent) (*store.BotRun, error) {
	return s.repository().AdmitExternalRun(ctx, q)
}
func (s *teamBotStore) AdmitExternalRun(ctx context.Context, q store.BotExternalEvent) (*store.BotRun, error) {
	return s.repository().AdmitExternalRun(ctx, q)
}

type botTriggerDB interface {
	saveTriggerVersion(context.Context, botStoredRecord, int64, time.Time) error
	triggerScheduler() store.SchedulerStore
}

func (d personalBotDB) triggerScheduler() store.SchedulerStore {
	return &personalSchedulerStore{client: d.client}
}
func (d teamBotDB) triggerScheduler() store.SchedulerStore {
	return &teamSchedulerStore{client: d.client}
}
func (d personalBotDB) saveTriggerVersion(ctx context.Context, r botStoredRecord, v int64, now time.Time) error {
	n, e := d.client.Bot.Update().Where(personalbot.IDEQ(r.Bot.ID), personalbot.RevisionEQ(r.Bot.Revision), personalbot.LastSeqEQ(r.LastSequence), personalbot.StatusEQ(personalbot.Status(r.Bot.Status)), personalbot.ActiveRunIDIsNil()).AddRevision(1).AddLastSeq(1).SetVersion(v).SetUpdatedAt(now).Save(ctx)
	if e != nil {
		return personalBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) saveTriggerVersion(ctx context.Context, r botStoredRecord, v int64, now time.Time) error {
	n, e := d.client.Bot.Update().Where(teambot.IDEQ(r.Bot.ID), teambot.RevisionEQ(r.Bot.Revision), teambot.LastSeqEQ(r.LastSequence), teambot.StatusEQ(teambot.Status(r.Bot.Status)), teambot.ActiveRunIDIsNil()).AddRevision(1).AddLastSeq(1).SetVersion(v).SetUpdatedAt(now).Save(ctx)
	if e != nil {
		return teamBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}

func goalTriggerSchedules(v *store.BotVersion, goalID uuid.UUID) map[uuid.UUID]bool {
	ids := map[uuid.UUID]bool{}
	for _, tr := range v.Triggers {
		if tr.Kind == store.TriggerGoalDeadline && tr.GoalID != nil && *tr.GoalID == goalID && tr.ScheduleID != nil {
			ids[*tr.ScheduleID] = true
		}
	}
	return ids
}

func triggerFingerprint(t store.TriggerSpec) string {
	t.Enabled = false
	t.HookStatus = ""
	t.Fingerprint = ""
	b, _ := json.Marshal(t)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func triggerNext(t store.TriggerSpec, now time.Time) (time.Time, error) {
	if strings.TrimSpace(t.Cron) == "" || t.Timezone == "" {
		return time.Time{}, store.ErrBotInvalid
	}
	loc, e := time.LoadLocation(t.Timezone)
	if e != nil {
		return time.Time{}, store.ErrBotInvalid
	}
	// Five-field cron only: no embedded TZ/CRON_TZ or descriptors overriding review.
	if len(strings.Fields(t.Cron)) != 5 {
		return time.Time{}, store.ErrBotInvalid
	}
	schedule, e := cron.ParseStandard(t.Cron)
	if e != nil {
		return time.Time{}, store.ErrBotInvalid
	}
	next := schedule.Next(now.In(loc))
	if next.IsZero() {
		return time.Time{}, store.ErrBotInvalid
	}
	return next.UTC(), nil
}

func (r *botRepository) ChangeTrigger(ctx context.Context, q store.BotTriggerChange) (*store.Bot, error) {
	if _, ok := execution.BotRunIdentityFromContext(ctx); ok {
		return nil, store.ErrBotForbidden
	}
	if q.BotID == uuid.Nil || q.TriggerID == uuid.Nil || q.ExpectedRevision < 1 || strings.TrimSpace(q.Reason) == "" {
		return nil, store.ErrBotInvalid
	}
	if _, e := r.auth.authorize(ctx, uuid.Nil, true); e != nil {
		return nil, e
	}
	if q.Action == store.BotTriggerDryRun || q.Action == store.BotTriggerEnable {
		var err error
		ctx, err = r.resolveTriggerCredential(ctx, q.BotID, q.TriggerID)
		if err == nil && q.Action == store.BotTriggerDryRun {
			ctx, q.Proof, err = r.prepareHookDryRun(ctx, q, q.Proof)
		}
		if err != nil {
			return nil, err
		}
	}
	var result *store.Bot
	e := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, e := tx.GetBot(ctx, q.BotID)
		if e != nil {
			return e
		}
		actor, e := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if e != nil {
			return e
		}
		if actor != rec.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		if rec.Bot.Revision != q.ExpectedRevision || rec.ActiveRunID != nil {
			return store.ErrBotConflict
		}
		switch rec.Bot.Status {
		case store.BotDraft, store.BotActive, store.BotPaused, store.BotStopped:
		default:
			return store.ErrBotForbidden
		}
		db, ok := tx.(botTriggerDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		v, e := tx.ReadVersion(ctx, q.BotID, rec.Bot.Version)
		if e != nil {
			return e
		}
		old := append([]store.TriggerSpec(nil), v.Triggers...)
		index := -1
		var tr store.TriggerSpec
		for i, t := range v.Triggers {
			if t.ID == q.TriggerID {
				index = i
				tr = t
				break
			}
		}
		if (q.Action == store.BotTriggerDryRun || q.Action == store.BotTriggerEnable) && (tr.Kind == store.TriggerHook || tr.Kind == store.TriggerWebhook) {
			if e := r.validateTriggerCredential(ctx, rec, tr); e != nil {
				return e
			}
		}
		switch q.Action {
		case store.BotTriggerSave:
			next := q.Definition
			if next.ID != uuid.Nil && next.ID != q.TriggerID {
				return store.ErrBotInvalid
			}
			if next.Enabled || next.HookWatermark != "" || next.HookStatus != "" || next.Fingerprint != "" || next.ScheduleID != nil || (next.GoalID != nil && next.Kind != store.TriggerGoalDeadline) {
				return store.ErrBotForbidden
			}
			next.ID = q.TriggerID
			if strings.TrimSpace(next.Instructions) == "" || len(next.Instructions) > 65536 {
				return store.ErrBotInvalid
			}
			if index >= 0 && tr.Kind != next.Kind {
				return store.ErrBotConflict
			}
			if next.Poll != nil && next.Kind != store.TriggerHook {
				return store.ErrBotInvalid
			}
			switch next.Kind {
			case store.TriggerCron, store.TriggerGoalDeadline:
				if next.Kind == store.TriggerCron {
					if _, e := triggerNext(next, r.clock()); e != nil {
						return e
					}
				} else {
					if next.GoalID == nil || *next.GoalID == uuid.Nil || next.Cron != "" {
						return store.ErrBotInvalid
					}
					goal, e := tx.ReadGoal(ctx, q.BotID, *next.GoalID)
					if e != nil {
						return e
					}
					if goal.Status != store.BotGoalActive || goal.DueAt == nil || goal.DueAt.IsZero() {
						return store.ErrBotConflict
					}
				}
				if index >= 0 {
					next.ScheduleID = tr.ScheduleID
				}
				if next.ScheduleID == nil {
					id := uuid.New()
					next.ScheduleID = &id
				}
			case store.TriggerHook, store.TriggerWebhook:
				if strings.TrimSpace(next.CredentialRef) == "" {
					return store.ErrBotInvalid
				}
				next.HookStatus = store.HookDisabled
				if next.Poll != nil {
					if err := next.Poll.Validate(); err != nil {
						return err
					}
					if _, err := triggerNext(next, r.clock()); err != nil {
						return err
					}
					if index >= 0 {
						next.ScheduleID = tr.ScheduleID
					}
					if next.ScheduleID == nil {
						id := uuid.New()
						next.ScheduleID = &id
					}
				}
			case store.TriggerChannel:
				if next.ChannelBinding == nil || next.ChannelBinding.ID == uuid.Nil || next.ChannelBinding.Channel == "" || next.ChannelBinding.ChatID == "" {
					return store.ErrBotInvalid
				}
			default:
				return store.ErrBotUnsupported
			}
			next.Fingerprint = triggerFingerprint(next)
			tr = next
		case store.BotTriggerEnable:
			if index < 0 {
				return store.ErrBotNotFound
			}
			if rec.Bot.Status != store.BotActive {
				return store.ErrBotConflict
			}
			if tr.Kind == store.TriggerHook || tr.Kind == store.TriggerWebhook {
				if tr.HookStatus != store.HookDryRunPassed || tr.Fingerprint != triggerCredentialFingerprint(ctx, tr) {
					return store.ErrBotConflict
				}
				tr.HookStatus = store.HookEnabled
			}
			tr.Enabled = true
		case store.BotTriggerDisable:
			if index < 0 {
				return store.ErrBotNotFound
			}
			tr.Enabled = false
			if tr.Kind == store.TriggerHook || tr.Kind == store.TriggerWebhook {
				tr.HookStatus = store.HookDisabled
			}
		case store.BotTriggerRemove:
			if index < 0 {
				return store.ErrBotNotFound
			}
		case store.BotTriggerDryRun:
			if index < 0 {
				return store.ErrBotNotFound
			}
			if (tr.Kind != store.TriggerHook && tr.Kind != store.TriggerWebhook) || tr.Enabled || tr.HookStatus != store.HookDisabled || q.Proof == nil {
				return store.ErrBotConflict
			}
			if q.Proof.BotID != q.BotID || q.Proof.TriggerID != q.TriggerID || q.Proof.Version != v.Version {
				return store.ErrBotConflict
			}
			if e := r.verifyTriggerSignature(ctx, tr, *q.Proof, "dry-run"); e != nil {
				return e
			}
			if tr.Poll != nil {
				tr.HookWatermark = fmt.Sprintf("%x", sha256.Sum256(q.Proof.Payload))
			}
			tr.HookStatus = store.HookDryRunPassed
			tr.Fingerprint = triggerCredentialFingerprint(ctx, tr)
		default:
			return store.ErrBotInvalid
		}
		if q.Action == store.BotTriggerRemove {
			v.Triggers = append(v.Triggers[:index], v.Triggers[index+1:]...)
		} else if index < 0 {
			v.Triggers = append(v.Triggers, tr)
		} else {
			v.Triggers[index] = tr
		}
		v.Version++
		v.CreatedBy = actor
		v.CreatedAt = r.clock()
		v.ChangeReason = q.Reason
		if e := db.saveTriggerVersion(ctx, *rec, v.Version, v.CreatedAt); e != nil {
			return e
		}
		hash, e := botHash(v)
		if e != nil {
			return e
		}
		if e := tx.InsertVersion(ctx, *v, hash); e != nil {
			return e
		}
		if e := r.syncTriggerSchedules(ctx, tx, db.triggerScheduler(), rec.Bot, *v, old); e != nil {
			return e
		}
		if e := tx.RevokeBotApprovals(ctx, q.BotID, true); e != nil {
			return e
		}
		payload, _ := json.Marshal(map[string]any{"version": v.Version, "trigger_id": q.TriggerID, "action": q.Action, "actor_id": actor})
		if e := tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityPersona, Payload: payload, CreatedAt: v.CreatedAt}, fmt.Sprintf("trigger-version:%d", v.Version)); e != nil {
			return e
		}
		rec.Bot.Version = v.Version
		rec.Bot.Revision++
		rec.Bot.UpdatedAt = v.CreatedAt
		result = &rec.Bot
		return nil
	})
	return result, e
}

func (r *botRepository) syncTriggerSchedules(ctx context.Context, tx botDraftDB, jobs store.SchedulerStore, b store.Bot, v store.BotVersion, old []store.TriggerSpec) error {
	retained := map[uuid.UUID]bool{}
	for _, t := range v.Triggers {
		if t.ScheduleID == nil {
			continue
		}
		retained[*t.ScheduleID] = true
		if t.Kind != store.TriggerCron && t.Kind != store.TriggerGoalDeadline && !(t.Kind == store.TriggerHook && t.Poll != nil) {
			continue
		}
		job := jobs.Get(ctx, t.ScheduleID.String())
		exists := job != nil
		if !exists {
			job = &store.ScheduledJob{ID: t.ScheduleID.String(), Name: "bot-" + b.ID.String() + "-" + t.ID.String(), Mode: "bot_trigger", OwnerID: b.OwnerID.String()}
		}
		if job.Mode != "bot_trigger" || job.OwnerID != b.OwnerID.String() {
			return store.ErrBotForbidden
		}
		// Persist the exact authenticated team used when this schedule is bound.
		// Personal stores are org/user-scoped, so the scheduler cannot recover
		// this from its tenant iteration. Never infer a default team or use the
		// ambient store context, which is not authenticated routing evidence.
		// Retain an existing binding when editing from another team; an explicit
		// owner-authorized trigger edit also repairs old schedules missing it.
		if job.TeamSlug == "" {
			principal, ok := execution.PrincipalFromContext(ctx)
			if !ok || principal.OrgSlug != r.auth.orgSlug || principal.TeamSlug == "" {
				return store.ErrBotForbidden
			}
			job.TeamSlug = principal.TeamSlug
		}
		job.Payload = store.JobPayload{BotID: b.ID.String(), BotVersion: v.Version, BotTriggerID: t.ID.String()}
		job.Schedule = store.JobSchedule{Cron: t.Cron, Timezone: t.Timezone}
		job.Enabled = t.Enabled && b.Status == store.BotActive
		if t.Kind == store.TriggerGoalDeadline {
			if t.GoalID == nil {
				return store.ErrBotInvalid
			}
			goal, e := tx.ReadGoal(ctx, b.ID, *t.GoalID)
			if e != nil {
				return e
			}
			// Deadline marker is a one-shot: the scheduler clears NextRun after
			// admission. Goal status is checked again inside admission.
			job.Schedule = store.JobSchedule{Cron: store.BotDeadlineSchedule}
			job.Enabled = job.Enabled && goal.Status == store.BotGoalActive && goal.DueAt != nil
			job.NextRun = nil
			if job.Enabled {
				due := goal.DueAt.UTC()
				job.NextRun = &due
			}
		} else if job.Enabled {
			next, e := triggerNext(t, r.clock())
			if e != nil {
				return e
			}
			job.NextRun = &next
		} else {
			job.NextRun = nil
		}
		if !exists {
			if e := jobs.Add(ctx, job); e != nil {
				return e
			}
		}
		if e := jobs.Update(ctx, job); e != nil {
			return e
		}
	}
	for _, t := range old {
		if t.ScheduleID != nil && !retained[*t.ScheduleID] {
			if e := jobs.Remove(ctx, t.ScheduleID.String()); e != nil {
				return e
			}
		}
	}
	return nil
}

// resolvedTriggerCredential is private, request-local evidence. Credential IO must
// finish before a Bot transaction takes SQLite's single connection. Never persist
// this value; only the domain-separated key-bound proof fingerprint is stored.
type resolvedTriggerCredential struct {
	botID, ownerID    uuid.UUID
	version, revision int64
	scope             store.BotScope
	triggerHash       string
	secret            string
}
type triggerCredentialContextKey struct{}

func (r *botRepository) resolveTriggerCredential(ctx context.Context, botID, triggerID uuid.UUID) (context.Context, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return ctx, err
	}
	b, err := r.Get(ctx, botID)
	if err != nil {
		return ctx, err
	}
	if actor != b.OwnerID || b.Scope != r.auth.scope {
		return ctx, store.ErrBotForbidden
	}
	v, err := r.db.ReadVersion(ctx, botID, b.Version)
	if err != nil {
		return ctx, err
	}
	for _, tr := range v.Triggers {
		if tr.ID != triggerID {
			continue
		}
		if tr.Kind != store.TriggerHook && tr.Kind != store.TriggerWebhook {
			return ctx, nil
		}
		svc := store.FromContext(ctx)
		if svc == nil {
			return ctx, store.ErrBotUnsupported
		}
		creds := svc.PersonalCredentials
		if r.auth.scope == store.BotScopeTeam {
			creds = svc.Credentials
		}
		if creds == nil {
			return ctx, store.ErrBotUnsupported
		}
		cred := creds.Get(ctx, tr.CredentialRef)
		if cred == nil || cred.Type != store.CredAPIKey || len(cred.Value) < 32 {
			return ctx, store.ErrBotForbidden
		}
		hash, err := botHash(tr)
		if err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, triggerCredentialContextKey{}, resolvedTriggerCredential{botID: b.ID, ownerID: b.OwnerID, version: b.Version, revision: b.Revision, scope: b.Scope, triggerHash: hash, secret: cred.Value}), nil
	}
	return ctx, store.ErrBotNotFound
}

func (r *botRepository) validateTriggerCredential(ctx context.Context, rec *botStoredRecord, tr store.TriggerSpec) error {
	resolved, ok := ctx.Value(triggerCredentialContextKey{}).(resolvedTriggerCredential)
	hash, err := botHash(tr)
	if err != nil {
		return err
	}
	if !ok || resolved.botID != rec.Bot.ID || resolved.ownerID != rec.Bot.OwnerID || resolved.version != rec.Bot.Version || resolved.revision != rec.Bot.Revision || resolved.scope != rec.Bot.Scope || resolved.scope != r.auth.scope || resolved.triggerHash != hash {
		return store.ErrBotConflict
	}
	return nil
}

func triggerCredentialFingerprint(ctx context.Context, tr store.TriggerSpec) string {
	resolved, ok := ctx.Value(triggerCredentialContextKey{}).(resolvedTriggerCredential)
	if !ok {
		return ""
	}
	h := hmac.New(sha256.New, []byte(resolved.secret))
	fmt.Fprintf(h, "bot-trigger-proof-v1\n%s\n%s\n%s\n%s", resolved.scope, resolved.botID, resolved.ownerID, triggerFingerprint(tr))
	return hex.EncodeToString(h.Sum(nil))
}

func (r *botRepository) verifyTriggerSignature(ctx context.Context, tr store.TriggerSpec, q store.BotExternalEvent, purpose string) error {
	if q.EventID == "" || len(q.EventID) > 128 || strings.ContainsAny(q.EventID, "\r\n") || len(q.Payload) > 65536 {
		return store.ErrBotInvalid
	}
	now := r.clock()
	stamp := time.Unix(q.Timestamp, 0)
	if stamp.Before(now.Add(-store.BotTriggerSignatureWindow)) || stamp.After(now.Add(store.BotTriggerSignatureWindow)) {
		return store.ErrBotExpired
	}
	resolved, ok := ctx.Value(triggerCredentialContextKey{}).(resolvedTriggerCredential)
	hash, err := botHash(tr)
	if err != nil {
		return err
	}
	if !ok || resolved.botID != q.BotID || resolved.version != q.Version || resolved.triggerHash != hash {
		return store.ErrBotConflict
	}
	expected, _ := hex.DecodeString(store.BotTriggerSignature(resolved.secret, q, purpose))
	actual, e := hex.DecodeString(q.Signature)
	if e != nil || !hmac.Equal(expected, actual) {
		return store.ErrBotForbidden
	}
	return nil
}

func (r *botRepository) AdmitExternalRun(ctx context.Context, q store.BotExternalEvent) (*store.BotRun, error) {
	if _, ok := execution.BotRunIdentityFromContext(ctx); ok {
		return nil, store.ErrBotForbidden
	}
	if q.BotID == uuid.Nil || q.TriggerID == uuid.Nil || q.Version < 1 || q.EventID == "" || len(q.EventID) > 128 {
		return nil, store.ErrBotInvalid
	}
	ctx, err := r.resolveTriggerCredential(ctx, q.BotID, q.TriggerID)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("bot-event-v1:%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s", q.BotID, q.TriggerID, q.EventID))))
	trusted := &botScheduledRequest{external: &q}
	return r.requestRun(ctx, store.BotRunRequest{BotID: q.BotID, Version: q.Version, TriggerID: q.TriggerID, IdempotencyKey: key}, trusted)
}
func (r *botRepository) resolveExternalRequest(ctx context.Context, tx botDraftDB, rec *botStoredRecord, q store.BotExternalEvent, req *store.BotRunRequest) error {
	if rec.Bot.Status != store.BotActive || rec.Bot.Version != q.Version || rec.Bot.Scope != r.auth.scope {
		return store.ErrBotConflict
	}
	v, e := tx.ReadVersion(ctx, q.BotID, q.Version)
	if e != nil {
		return e
	}
	for _, tr := range v.Triggers {
		if tr.ID != q.TriggerID {
			continue
		}
		if !tr.Enabled {
			return store.ErrBotForbidden
		}
		switch tr.Kind {
		case store.TriggerHook, store.TriggerWebhook:
			if e := r.validateTriggerCredential(ctx, rec, tr); e != nil {
				return e
			}
			if tr.HookStatus != store.HookEnabled || tr.Fingerprint != triggerCredentialFingerprint(ctx, tr) {
				return store.ErrBotConflict
			}
			if tr.Poll != nil {
				occurrence, ok := ctx.Value(hookOccurrenceKey{}).(store.BotScheduledOccurrence)
				if !ok {
					return store.ErrBotForbidden
				}
				if err := r.validateHookOccurrence(ctx, tx, rec, tr, occurrence); err != nil {
					return err
				}
			}
			if e := r.verifyTriggerSignature(ctx, tr, q, "run"); e != nil {
				return e
			}
		case store.TriggerChannel:
			p, ok := execution.PrincipalFromContext(ctx)
			if !ok || p.Kind != execution.PrincipalKindUser || p.Authentication != execution.AuthMethodChannel || p.Surface != execution.SurfaceChannel || !p.Authenticated || q.Binding == nil || tr.ChannelBinding == nil || *q.Binding != *tr.ChannelBinding {
				return store.ErrBotForbidden
			}
		default:
			return store.ErrBotUnsupported
		}
		// Unique event IDs are replay protection, not caller-chosen run keys.
		if _, e := tx.FindRun(ctx, q.BotID, req.IdempotencyKey); e == nil {
			return store.ErrBotConflict
		} else if e != store.ErrBotNotFound {
			return e
		}
		req.Trigger = tr.Kind
		req.Instructions = tr.Instructions
		return nil
	}
	return store.ErrBotNotFound
}
