package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// botRepositoryAuth is constructed by the scoped router. check revalidates live
// membership on every operation; a JWT role or an owner UUID alone is not enough.
type botRepositoryAuth struct {
	orgSlug       string
	teamSlug      string
	userID        string
	scope         store.BotScope
	readiness     store.BotActivationReadiness
	checkDelivery func(context.Context, uuid.UUID, store.BotChannelBinding) error
	check         func(context.Context, string) (string, error)
}

func (a botRepositoryAuth) authorize(ctx context.Context, owner uuid.UUID, edit bool) (uuid.UUID, error) {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || p.OrgSlug != a.orgSlug || a.orgSlug == "" || a.check == nil {
		return uuid.Nil, store.ErrBotForbidden
	}
	actor, err := uuid.Parse(p.Subject)
	if err != nil {
		return uuid.Nil, store.ErrBotForbidden
	}
	switch a.scope {
	case store.BotScopePersonal:
		if p.Subject != a.userID || owner != uuid.Nil && actor != owner {
			return uuid.Nil, store.ErrBotForbidden
		}
	case store.BotScopeTeam:
		if a.teamSlug == "" || p.TeamSlug != a.teamSlug {
			return uuid.Nil, store.ErrBotForbidden
		}
	default:
		return uuid.Nil, store.ErrBotForbidden
	}
	role, err := a.check(ctx, p.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("check bot membership: %w", err)
	}
	if role != "owner" && role != "admin" && role != "member" && role != "viewer" {
		return uuid.Nil, store.ErrBotForbidden
	}
	if edit && (role == "viewer" || owner != uuid.Nil && owner != actor && role != "admin" && role != "owner") {
		return uuid.Nil, store.ErrBotForbidden
	}
	return actor, nil
}

type botStoredRecord struct {
	Bot            store.Bot
	CreateKey      string
	CreateHash     string
	LastSequence   int64
	Fence          int64
	ActiveRunID    *uuid.UUID
	LeaseExpiresAt *time.Time
	Metadata       map[string]any
}

type botStoredRun struct {
	Work                       *store.BotWorkState `json:"work,omitempty"`
	ContinuationRequests       map[string]string   `json:"continuation_requests,omitempty"`
	ExternalAdmissionValidated bool
	Run                        store.BotRun
	RequestHash                string
	Instructions               string
	Attachments                []store.ChatFile `json:",omitempty"`
	OccurrenceAt               time.Time
	CommitHash                 string
}

// MarshalJSON keeps the ceiling in the private stored envelope, never BotRun JSON.
func (s botStoredRun) MarshalJSON() ([]byte, error) {
	type stored botStoredRun
	return json.Marshal(struct {
		stored
		MandateRef    *store.BotMandateRef        `json:"mandate_ref,omitempty"`
		CallerCeiling *execution.BotCallerCeiling `json:"caller_ceiling,omitempty"`
	}{stored: stored(s), MandateRef: s.Run.MandateRef, CallerCeiling: s.Run.CallerCeiling})
}
func (s *botStoredRun) UnmarshalJSON(data []byte) error {
	type stored botStoredRun
	var value struct {
		stored
		MandateRef    *store.BotMandateRef        `json:"mandate_ref,omitempty"`
		CallerCeiling *execution.BotCallerCeiling `json:"caller_ceiling,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = botStoredRun(value.stored)
	s.Run.MandateRef = value.MandateRef
	if value.CallerCeiling != nil {
		ceiling := value.CallerCeiling.Clone()
		s.Run.CallerCeiling = &ceiling
	}
	return nil
}

// botDraftDB is implemented only with a scope's generated Ent client. Each
// transaction returns the same interface bound to the same scope and transaction.
// There is no raw SQL application-data path and no cross-scope transaction.
type botDraftDB interface {
	Transaction(context.Context, func(botDraftDB) error) error
	GetBot(context.Context, uuid.UUID) (*botStoredRecord, error)
	FindCreate(context.Context, string) (*botStoredRecord, error)
	ListBots(context.Context, int, uuid.UUID) ([]botStoredRecord, error)
	InsertBot(context.Context, botStoredRecord) error
	UpdateVersion(context.Context, uuid.UUID, int64, int64, int64, time.Time) error
	ReadVersion(context.Context, uuid.UUID, int64) (*store.BotVersion, error)
	InsertVersion(context.Context, store.BotVersion, string) error
	InsertHomeSession(context.Context, store.Bot) error
	InsertGoal(context.Context, store.BotGoal) error
	ReadGoals(context.Context, uuid.UUID) ([]store.BotGoal, error)
	ReadGoal(context.Context, uuid.UUID, uuid.UUID) (*store.BotGoal, error)
	UpdateGoal(context.Context, store.BotGoal, int64) error
	ReadActivity(context.Context, uuid.UUID, int64, int) ([]store.BotActivity, error)
	AdvanceSequence(context.Context, uuid.UUID, int64) error
	InsertActivity(context.Context, store.BotActivity, string) error
	AppendRunState(context.Context, store.Bot, store.BotRun, int64, time.Time) error
	FindRun(context.Context, uuid.UUID, string) (*botStoredRun, error)
	ReadRun(context.Context, uuid.UUID, uuid.UUID) (*botStoredRun, error)
	ReadRuns(context.Context, uuid.UUID, int, uuid.UUID) ([]store.BotRun, error)
	InsertRun(context.Context, botStoredRun, uuid.UUID) error
	CountRunsSince(context.Context, uuid.UUID, time.Time) (int, error)
	ClaimLease(context.Context, botStoredRecord, botStoredRun, store.BotLease) error
	RenewRunLease(context.Context, store.BotLease, time.Time, time.Time) error
	AdvanceLeaseSequence(context.Context, store.BotLease, int64, time.Time) error
	FindApproval(context.Context, uuid.UUID, uuid.UUID, string) (*store.BotApproval, error)
	ReadApproval(context.Context, uuid.UUID, uuid.UUID) (*store.BotApproval, error)
	ReadApprovals(context.Context, uuid.UUID, int, uuid.UUID) ([]store.BotApproval, error)
	InsertApproval(context.Context, store.BotApproval) error
	UpdateApproval(context.Context, store.BotApproval, store.ApprovalStatus, time.Time) error
	FinishRun(context.Context, botStoredRecord, botStoredRun, store.BotLease, time.Time, bool) error
	RevokeRunApprovals(context.Context, uuid.UUID, uuid.UUID) error
	SetLifecycle(context.Context, botStoredRecord, store.Bot) error
	QuiesceBotRuns(context.Context, uuid.UUID, bool, time.Time) ([]store.BotRun, error)
	DisableBotSchedules(context.Context, uuid.UUID) error
	RevokeBotApprovals(context.Context, uuid.UUID, bool) error
	SaveReadCursor(context.Context, botStoredRecord, store.BotReadCursor) error
	ReadTrackedItems(context.Context, uuid.UUID) ([]store.BotTrackedItem, error)
	WriteTrackedItem(context.Context, store.BotTrackedItem, int64) error
	DisableGoalSchedules(context.Context, uuid.UUID, uuid.UUID) error
	FindActivity(context.Context, uuid.UUID, string) (*store.BotActivity, error)
	AdvanceCorrection(context.Context, botStoredRecord, time.Time) error
	ApplyCorrectionMemory(context.Context, uuid.UUID, uuid.UUID, string, string, bool) error
	ReadCorrectionMemory(context.Context, string) (*store.MemorySearchResult, error)
	CorrectionSessionBot(context.Context, uuid.UUID) (uuid.UUID, error)
	ListPurgeable(context.Context, time.Time, int) ([]botStoredRecord, error)
	PurgeBot(context.Context, botStoredRecord, time.Time) error
}
type botRepository struct {
	auth botRepositoryAuth
	db   botDraftDB
	now  func() time.Time
	// Installed by the runtime composition, never accepted from a request.
	activationCheck func(context.Context, store.Bot, store.BotVersion) error
}

func (r *botRepository) clock() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func (r *botRepository) RequestRun(ctx context.Context, req store.BotRunRequest) (*store.BotRun, error) {
	return r.requestRun(ctx, req, nil)
}

func (r *botRepository) requestRun(ctx context.Context, req store.BotRunRequest, occurrence *botScheduledRequest) (*store.BotRun, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	if req.BotID == uuid.Nil || req.Version < 1 || strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 200 || (occurrence == nil && strings.TrimSpace(req.Instructions) == "") {
		return nil, store.ErrBotInvalid
	}
	// Other trigger adapters are intentionally closed until their signed/owned
	// bindings and scheduling transaction are installed. Never accept a forged cron.
	if occurrence == nil && req.Trigger != store.TriggerManual {
		return nil, store.ErrBotUnsupported
	}
	// Check external readiness without holding a scoped DB transaction. The
	// revision and sequence CAS below reject concurrent lifecycle/configuration edits.
	var reviewed *store.Bot
	if req.Intent != store.BotIntentRegular {
		if occurrence != nil || req.Intent.RequiredStatus() == "" {
			return nil, store.ErrBotInvalid
		}
		reviewed, err = r.Get(ctx, req.BotID)
		if err != nil {
			return nil, err
		}
		if _, err = r.auth.authorize(ctx, reviewed.OwnerID, true); err != nil {
			return nil, err
		}
		// Duplicate consent can still be replayed after a lifecycle transition.
		if old, e := r.db.FindRun(ctx, req.BotID, req.IdempotencyKey); e == nil {
			hash, e := botHash(req)
			if e != nil {
				return nil, e
			}
			if old.RequestHash != hash {
				return nil, store.ErrBotConflict
			}
			return &old.Run, nil
		} else if e != store.ErrBotNotFound {
			return nil, e
		}
		if !req.Intent.AllowsStatus(reviewed.Status) {
			return nil, store.ErrBotForbidden
		}
		if reviewed.Version != req.Version {
			return nil, store.ErrBotConflict
		}
		v, e := r.db.ReadVersion(ctx, req.BotID, req.Version)
		if e != nil {
			return nil, e
		}
		if e = botdomain.ValidateVersion(*v); e != nil {
			return nil, e
		}
		check := r.activationCheck
		if check == nil {
			check = r.auth.readiness
		}
		if check == nil {
			return nil, store.ErrBotUnsupported
		}
		// Single-run consent never grants recurrence authority.
		readinessVersion := *v
		readinessVersion.Triggers = nil
		if e = check(ctx, *reviewed, readinessVersion); e != nil {
			return nil, e
		}
	}
	hash, err := botHash(req)
	if err != nil {
		return nil, err
	}
	var result *store.BotRun
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, req.BotID)
		if err != nil {
			return err
		}
		if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
			return err
		}
		if occurrence != nil {
			if actor != rec.Bot.OwnerID {
				return store.ErrBotForbidden
			}
			// A committed occurrence remains replayable after pause/version rebind.
			// Authentication and ownership above still apply; no live state is mutated.
			if occurrence.external == nil {
				old, e := tx.FindRun(ctx, req.BotID, req.IdempotencyKey)
				if e == nil {
					if old.Run.Version != req.Version || old.Run.TriggerID != req.TriggerID || !old.OccurrenceAt.Equal(req.OccurrenceAt) {
						return store.ErrBotConflict
					}
					occurrence.duplicate = true
					result = &old.Run
					return nil
				}
				if e != store.ErrBotNotFound {
					return e
				}
			}
			if err := r.resolveScheduledRequest(ctx, tx, rec, occurrence, &req); err != nil {
				return err
			}
			hash, err = botHash(req)
			if err != nil {
				return err
			}
		}
		previous, err := tx.FindRun(ctx, req.BotID, req.IdempotencyKey)
		if err == nil {
			if previous.RequestHash != hash {
				return store.ErrBotConflict
			}
			if occurrence != nil {
				occurrence.duplicate = true
			}
			result = &previous.Run
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		if !req.Intent.AllowsStatus(rec.Bot.Status) {
			return store.ErrBotForbidden
		}
		if reviewed != nil {
			if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
				return err
			}
			if rec.Bot.Revision != reviewed.Revision {
				return store.ErrBotConflict
			}
		}
		if rec.Bot.Version != req.Version {
			return store.ErrBotConflict
		}
		v, err := tx.ReadVersion(ctx, req.BotID, req.Version)
		if err != nil {
			return err
		}
		now := r.clock()
		if v.Budget.MaxRunsPerDay != nil {
			loc, err := time.LoadLocation(v.Persona.Timezone)
			if err != nil {
				return store.ErrBotInvalid
			}
			local := now.In(loc)
			midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
			count, err := tx.CountRunsSince(ctx, req.BotID, midnight)
			if err != nil {
				return err
			}
			if int64(count) >= *v.Budget.MaxRunsPerDay {
				return store.ErrBotForbidden
			}
		}
		if occurrence != nil {
			if err := tx.(botScheduledDB).advanceScheduledAdmission(ctx, *rec); err != nil {
				return err
			}
		} else if err := tx.AdvanceSequence(ctx, req.BotID, rec.LastSequence); err != nil {
			return err
		}
		run := store.BotRun{Intent: req.Intent, ID: uuid.New(), BotID: req.BotID, Version: req.Version, SessionID: uuid.New(), Trigger: req.Trigger, TriggerID: req.TriggerID, IdempotencyKey: req.IdempotencyKey, Status: store.RunQueued, CreatedAt: now}
		if occurrence != nil && occurrence.external == nil {
			if err := r.attachScheduledTask(ctx, tx, rec, &run, req); err != nil {
				return err
			}
		}
		if err := tx.InsertRun(ctx, botStoredRun{Run: run, ExternalAdmissionValidated: occurrence != nil && occurrence.external != nil, RequestHash: hash, Instructions: req.Instructions, Attachments: req.Attachments, OccurrenceAt: req.OccurrenceAt}, rec.Bot.OwnerID); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"run_id": run.ID, "status": run.Status, "intent": store.BotOutboxDispatch, "actor_id": actor})
		if err != nil {
			return err
		}
		if err := insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: req.BotID, RunID: &run.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: now}, "dispatch:"+run.ID.String()); err != nil {
			return err
		}
		if err := r.acknowledgeScheduledRequest(ctx, tx, req, occurrence, now); err != nil {
			return fmt.Errorf("acknowledge scheduled request: %w", err)
		}
		result = &run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (r *botRepository) GetRun(ctx context.Context, id store.BotID, runID store.RunID) (*store.BotRun, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	saved, err := r.db.ReadRun(ctx, id, runID)
	if err != nil {
		return nil, err
	}
	return &saved.Run, nil
}
func (r *botRepository) ListRuns(ctx context.Context, id store.BotID, page store.BotPage) ([]store.BotRun, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	limit, after, err := botPage(page)
	if err != nil {
		return nil, err
	}
	return r.db.ReadRuns(ctx, id, limit, after)
}
func (r *botRepository) authorizeWorker(ctx context.Context, id, runID uuid.UUID, version int64, owner uuid.UUID) error {
	actor, err := r.auth.authorize(ctx, owner, false)
	if err != nil {
		return err
	}
	identity, ok := execution.BotRunIdentityFromContext(ctx)
	p, present := execution.PrincipalFromContext(ctx)
	if !ok || !present || identity.Validate() != nil || identity.BotID != id || identity.RunID != runID || identity.Version != version || identity.ActorID != actor.String() || identity.OrgSlug != p.OrgSlug || identity.TeamSlug != p.TeamSlug {
		return store.ErrBotForbidden
	}
	role, err := r.auth.check(ctx, p.Subject)
	if err != nil {
		return err
	}
	if role == "viewer" {
		return store.ErrBotForbidden
	}
	return nil
}
func (r *botRepository) ClaimRun(ctx context.Context, req store.BotClaimRequest) (store.BotLease, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return store.BotLease{}, err
	}
	now := r.clock()
	if req.WorkerID == "" || !req.ExpiresAt.After(now) || req.ExpiresAt.Sub(now) > 5*time.Minute {
		return store.BotLease{}, store.ErrBotInvalid
	}
	var lease store.BotLease
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, req.BotID)
		if err != nil {
			return err
		}
		run, err := tx.ReadRun(ctx, req.BotID, req.RunID)
		if err != nil {
			return err
		}
		if err := r.authorizeWorker(ctx, req.BotID, req.RunID, run.Run.Version, rec.Bot.OwnerID); err != nil {
			return err
		}
		if !interactiveStatus(run.Run, rec.Bot.Status) || rec.Bot.Version != run.Run.Version || rec.ActiveRunID != nil || run.Run.Status != store.RunQueued {
			return store.ErrBotConflict
		}
		if run.Run.Presentation != store.BotRunPresentationHomeInteractive || run.Run.Trigger == store.TriggerCron {
			// Enforce priority inside the claim CAS transaction as well as in
			// worker ordering: another scheduler must not slip past a queued owner.
			after := uuid.Nil
			for {
				runs, err := tx.ReadRuns(ctx, req.BotID, 200, after)
				if err != nil {
					return err
				}
				for _, pending := range runs {
					if pending.Status == store.RunQueued && pending.Presentation == store.BotRunPresentationHomeInteractive && pending.Trigger != store.TriggerCron {
						return store.ErrBotConflict
					}
				}
				if len(runs) < 200 {
					break
				}
				after = runs[len(runs)-1].ID
			}
		}
		// Only first claims initialize work. Legacy in-flight/checkpoint attempts
		// remain legacy; an existing contract is never regenerated on continuation.
		if run.Work == nil && run.Run.StartedAt == nil && len(run.Run.Checkpoint) == 0 {
			version, err := tx.ReadVersion(ctx, req.BotID, run.Run.Version)
			if err != nil {
				return err
			}
			goals, err := tx.ReadGoals(ctx, req.BotID)
			if err != nil {
				return err
			}
			run.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.BotID, RunID: req.RunID, Version: run.Run.Version, AdmissionEventID: run.Run.UserEventID, Instructions: run.Instructions, Charter: version.Charter, Goals: goals, Attachments: store.PinBotWorkAttachments(run.Attachments)})
			if err := run.Work.Validate(); err != nil {
				return err
			}
		}
		lease = store.BotLease{BotID: req.BotID, RunID: req.RunID, WorkerID: req.WorkerID, Fence: rec.Fence + 1, ExpiresAt: req.ExpiresAt.UTC()}
		run.Run.Status = store.RunRunning
		run.Run.StartedAt = &now
		run.Run.Lease = lease
		if err := tx.ClaimLease(ctx, *rec, *run, lease); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"run_id": req.RunID, "status": store.RunRunning, "fence": lease.Fence})
		if err != nil {
			return err
		}
		return insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: req.BotID, RunID: &req.RunID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityRun, Payload: payload, CreatedAt: now}, fmt.Sprintf("claim:%s:%d", req.RunID, lease.Fence))
	})
	if err != nil {
		return store.BotLease{}, err
	}
	return lease, nil
}
func (r *botRepository) checkLease(ctx context.Context, tx botDraftDB, lease store.BotLease, now time.Time) (*botStoredRecord, error) {
	rec, err := tx.GetBot(ctx, lease.BotID)
	if err != nil {
		return nil, err
	}
	run, err := tx.ReadRun(ctx, lease.BotID, lease.RunID)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeWorker(ctx, lease.BotID, lease.RunID, run.Run.Version, rec.Bot.OwnerID); err != nil {
		return nil, err
	}
	if !interactiveStatus(run.Run, rec.Bot.Status) || rec.Bot.Version != run.Run.Version || rec.ActiveRunID == nil || *rec.ActiveRunID != lease.RunID || rec.Fence != lease.Fence || run.Run.Status != store.RunRunning || run.Run.Lease.WorkerID != lease.WorkerID || run.Run.Lease.Fence != lease.Fence {
		return nil, store.ErrBotConflict
	}
	if rec.LeaseExpiresAt == nil || !now.Before(*rec.LeaseExpiresAt) || !now.Before(run.Run.Lease.ExpiresAt) {
		return nil, store.ErrBotExpired
	}
	return rec, nil
}
func (r *botRepository) RenewLease(ctx context.Context, lease store.BotLease, expires time.Time) (store.BotLease, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return store.BotLease{}, err
	}
	now := r.clock()
	if !expires.After(now) || expires.Sub(now) > 5*time.Minute {
		return store.BotLease{}, store.ErrBotInvalid
	}
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		if _, err := r.checkLease(ctx, tx, lease, now); err != nil {
			return err
		}
		return tx.RenewRunLease(ctx, lease, now, expires.UTC())
	})
	if err != nil {
		return store.BotLease{}, err
	}
	lease.ExpiresAt = expires.UTC()
	return lease, nil
}
func (r *botRepository) AppendActivity(ctx context.Context, lease store.BotLease, a store.BotActivity) (store.BotActivity, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return store.BotActivity{}, err
	}
	if a.BotID != lease.BotID || a.RunID == nil || *a.RunID != lease.RunID || a.Kind != store.BotActivityRun || !json.Valid(a.Payload) {
		return store.BotActivity{}, store.ErrBotInvalid
	}
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		now := r.clock()
		rec, err := r.checkLease(ctx, tx, lease, now)
		if err != nil {
			return err
		}
		// CAS root sequence as well as run lease so pause/revocation cannot interleave
		// a checked-but-no-longer-authorized write. Lifecycle also increments sequence.
		if err := tx.AdvanceLeaseSequence(ctx, lease, rec.LastSequence, now); err != nil {
			return err
		}
		a.Sequence = rec.LastSequence + 1
		a.CreatedAt = now
		return tx.InsertActivity(ctx, a, fmt.Sprintf("progress:%s:%d", lease.RunID, a.Sequence))
	})
	if err != nil {
		return store.BotActivity{}, err
	}
	return a, nil
}

func (r *botRepository) ListApprovals(ctx context.Context, id store.BotID, p store.BotPage) ([]store.BotApproval, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	limit, after, err := botPage(p)
	if err != nil {
		return nil, err
	}
	return r.db.ReadApprovals(ctx, id, limit, after)
}
func (r *botRepository) RequestApproval(ctx context.Context, l store.BotLease, a store.BotApproval) (*store.BotApproval, error) {
	// SQLite compares stored timestamps lexically in CAS predicates. Normalize
	// caller clocks so a live approval is not mistaken for expired by its offset.
	a.ExpiresAt = a.ExpiresAt.UTC()
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	digest, err := a.Action.Digest()
	if err != nil {
		return nil, store.ErrBotInvalid
	}
	if a.BotID != l.BotID || a.RunID != l.RunID || a.ActionDigest != digest || a.Version < 1 {
		return nil, store.ErrBotInvalid
	}
	if a.Interaction != nil {
		if a.Interaction.Validate() != nil || a.Action.Tool != "ask_user" || !bytes.Equal(a.Action.Arguments, a.Interaction.Payload) {
			return nil, store.ErrBotInvalid
		}
	}
	if a.Response != nil {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotApproval
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		now := r.clock()
		rec, err := r.checkLease(ctx, tx, l, now)
		if err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, l.BotID, l.RunID)
		if err != nil {
			return err
		}
		if saved.Run.ControlOnly() && !(saved.Run.IsInteractiveChat() && a.Interaction != nil) {
			return store.ErrBotForbidden
		}
		if a.Version != rec.Bot.Version {
			return store.ErrBotConflict
		}
		if !a.ExpiresAt.After(now) {
			return store.ErrBotExpired
		}
		old, err := tx.FindApproval(ctx, l.BotID, l.RunID, digest)
		if err == nil {
			result = old
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		if err := tx.AdvanceLeaseSequence(ctx, l, rec.LastSequence, now); err != nil {
			return err
		}
		a.ID = uuid.New()
		a.CreatedAt = now
		a.Status = store.ApprovalPending
		a.DecidedAt = nil
		a.DecidedBy = nil
		if err := tx.InsertApproval(ctx, a); err != nil {
			return err
		}
		if err := botApprovalActivity(ctx, tx, a, rec.LastSequence+1, now); err != nil {
			return err
		}
		result = &a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func botApprovalActivity(ctx context.Context, tx botDraftDB, a store.BotApproval, seq int64, now time.Time) error {
	payload, err := json.Marshal(map[string]any{"approval_id": a.ID, "status": a.Status, "action_digest": a.ActionDigest})
	if err != nil {
		return err
	}
	if err = tx.InsertActivity(ctx, store.BotActivity{BotID: a.BotID, RunID: &a.RunID, Sequence: seq, Kind: store.BotActivityApproval, Payload: payload, CreatedAt: now}, "approval:"+a.ID.String()+":"+string(a.Status)); err != nil {
		return err
	}
	if a.Status != store.ApprovalPending {
		return nil
	}
	rec, err := tx.GetBot(ctx, a.BotID)
	if err != nil {
		return err
	}
	return appendBotReview(ctx, tx, rec.Bot, a.RunID, a.ID, botReviewApproval, now)
}
func (r *botRepository) DecideApproval(ctx context.Context, q store.BotApprovalDecision) (*store.BotApproval, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	if q.Status != store.ApprovalApproved && q.Status != store.ApprovalDenied {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotApproval
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != q.ActorID {
			return store.ErrBotForbidden
		}
		a, err := tx.ReadApproval(ctx, q.BotID, q.ApprovalID)
		if err != nil {
			return err
		}
		now := r.clock()
		if !now.Before(a.ExpiresAt) {
			return store.ErrBotExpired
		}
		if a.ActionDigest != q.ActionDigest || a.Version != rec.Bot.Version || rec.Bot.Status == store.BotDeleted || rec.Bot.Status == store.BotStopped {
			return store.ErrBotConflict
		}
		if a.Interaction != nil {
			saved, err := tx.ReadRun(ctx, q.BotID, a.RunID)
			if err != nil {
				return err
			}
			if err := r.interactiveAnswerAuthority(ctx, saved.Run); err != nil {
				return err
			}
			if actor != rec.Bot.OwnerID {
				return store.ErrBotForbidden
			}
			if q.Status == store.ApprovalApproved && a.Interaction.ValidateResponse(q.Response) != nil {
				return store.ErrBotInvalid
			}
			if q.Status == store.ApprovalDenied && q.Response != nil {
				return store.ErrBotInvalid
			}
		} else if q.Response != nil {
			return store.ErrBotInvalid
		}
		if a.Status == q.Status && a.DecidedBy != nil && *a.DecidedBy == actor {
			if !reflect.DeepEqual(a.Response, q.Response) {
				return store.ErrBotConflict
			}
			result = a
			return nil
		}
		if a.Status != store.ApprovalPending {
			return store.ErrBotConflict
		}
		if err := tx.AdvanceSequence(ctx, q.BotID, rec.LastSequence); err != nil {
			return err
		}
		a.Status = q.Status
		a.Response = q.Response
		a.DecidedAt = &now
		a.DecidedBy = &actor
		if err := tx.UpdateApproval(ctx, *a, store.ApprovalPending, now); err != nil {
			return err
		}
		if err := botApprovalActivity(ctx, tx, *a, rec.LastSequence+1, now); err != nil {
			return err
		}
		result = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (r *botRepository) ConsumeApproval(ctx context.Context, q store.BotApprovalConsumption) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		now := r.clock()
		rec, err := r.checkLease(ctx, tx, q.Lease, now)
		if err != nil {
			return err
		}
		a, err := tx.ReadApproval(ctx, q.Lease.BotID, q.ApprovalID)
		if err != nil {
			return err
		}
		if !now.Before(a.ExpiresAt) {
			return store.ErrBotExpired
		}
		if a.RunID != q.Lease.RunID || a.Version != q.Version || a.Version != rec.Bot.Version || a.ActionDigest != q.ActionDigest || a.Status != store.ApprovalApproved {
			return store.ErrBotConflict
		}
		if err := tx.AdvanceLeaseSequence(ctx, q.Lease, rec.LastSequence, now); err != nil {
			return err
		}
		a.Status = store.ApprovalConsumed
		if err := tx.UpdateApproval(ctx, *a, store.ApprovalApproved, now); err != nil {
			return err
		}
		return botApprovalActivity(ctx, tx, *a, rec.LastSequence+1, now)
	})
}

// CommitRun publishes one immutable outcome per execution lease. Transport done
// events cannot call this without an explicit status and report. Side-effect
// delivery is deferred: this transaction records publication intent only.
func (r *botRepository) CommitRun(ctx context.Context, q store.BotRunCommit) error {
	// A caller cannot supply publication suppression authority.
	if q.Report.Acknowledgment != nil {
		return store.ErrBotInvalid
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	if !q.Status.Terminal() && q.Status != store.RunWaitingApproval && q.Status != store.RunInterrupted {
		return store.ErrBotInvalid
	}
	if strings.TrimSpace(q.Report.Summary) == "" || q.Usage.ToolCalls < 0 || q.Usage.InputTokens < 0 || q.Usage.OutputTokens < 0 || q.Usage.CostMicros != nil && *q.Usage.CostMicros < 0 {
		return store.ErrBotInvalid
	}
	// Arbitrary activity/outbox payloads cannot smuggle notification or dispatch
	// authority. The service-owned finalizer will materialize these typed intents.
	if len(q.Activity) > 0 || len(q.Outbox) > 0 {
		return store.ErrBotUnsupported
	}
	if err := store.ValidateBotTodos(q.Todos, false); err != nil {
		return err
	}
	if len(q.Checkpoint) > 0 && !json.Valid(q.Checkpoint) {
		return store.ErrBotInvalid
	}
	if q.Status == store.RunWaitingApproval && (len(q.Checkpoint) == 0 || len(q.Report.NeedsApproval) == 0) {
		return store.ErrBotInvalid
	}
	for _, todo := range q.Todos {
		if todo.Status == store.BotTodoInProgress {
			return store.ErrBotInvalid
		}
		if q.Status == store.RunSucceeded && todo.Status != store.BotTodoCompleted && todo.Status != store.BotTodoCancelled {
			return store.ErrBotInvalid
		}
	}
	if q.Status == store.RunSucceeded && (len(q.Report.Unverified) > 0 || len(q.Report.NeedsApproval) > 0) {
		return store.ErrBotInvalid
	}
	hash, err := botHash(q)
	if err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		now := r.clock()
		rec, err := tx.GetBot(ctx, q.Lease.BotID)
		if err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, q.Lease.BotID, q.Lease.RunID)
		if err != nil {
			return err
		}
		if err := r.authorizeWorker(ctx, q.Lease.BotID, q.Lease.RunID, saved.Run.Version, rec.Bot.OwnerID); err != nil {
			return err
		}
		if saved.Run.Status != store.RunRunning {
			if saved.CommitHash == hash && saved.Run.Lease.Fence == q.Lease.Fence && saved.Run.Lease.WorkerID == q.Lease.WorkerID {
				return nil
			}
			return store.ErrBotConflict
		}
		if _, err := r.checkLease(ctx, tx, q.Lease, now); err != nil {
			return err
		}
		for _, id := range q.Report.NeedsApproval {
			a, err := tx.ReadApproval(ctx, q.Lease.BotID, id)
			if err != nil {
				return err
			}
			if a.RunID != q.Lease.RunID || a.Version != saved.Run.Version || a.Status != store.ApprovalPending || !now.Before(a.ExpiresAt) {
				return store.ErrBotConflict
			}
		}
		if len(q.Report.Artifacts) > 0 {
			if rec.Bot.Home.ID == uuid.Nil {
				return store.ErrBotUnsupported
			}
			for _, artifact := range q.Report.Artifacts {
				if artifact.ID == uuid.Nil || artifact.HomeID != rec.Bot.Home.ID || !validBotArtifactPath(artifact.RelativePath) {
					return store.ErrBotInvalid
				}
			}
		}
		if err := validateBotWorkCommit(saved.Work, q); err != nil {
			return err
		}
		q.Report.Acknowledgment, err = scheduleAcknowledgment(ctx, tx, rec, saved.Run, saved.Work, q)
		if err != nil {
			return err
		}
		saved.Run.Status = q.Status
		saved.Run.Report = q.Report
		saved.Run.Usage = q.Usage
		saved.Run.Todos = q.Todos
		saved.Run.Checkpoint = q.Checkpoint
		if q.Status.Terminal() {
			saved.Run.FinishedAt = &now
		}
		saved.CommitHash = hash
		if err := tx.FinishRun(ctx, *rec, *saved, q.Lease, now, false); err != nil {
			return err
		}
		if q.Status.Terminal() {
			if err := tx.RevokeRunApprovals(ctx, q.Lease.BotID, q.Lease.RunID); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(map[string]any{"run_id": q.Lease.RunID, "status": q.Status, "report": q.Report, "usage": q.Usage, "intent": store.BotOutboxPublish})
		if err != nil {
			return err
		}
		return insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: q.Lease.BotID, RunID: &q.Lease.RunID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityReport, Payload: payload, CreatedAt: now}, fmt.Sprintf("result:%s:%d", q.Lease.RunID, q.Lease.Fence))
	})
}

// validateBotWorkCommit binds a successful publication to the exact reviewed
// candidate. Quality never overrides execution failures or pending authority.
func validateBotWorkCommit(work *store.BotWorkState, q store.BotRunCommit) error {
	if work == nil {
		if q.Report.Quality != nil {
			return store.ErrBotInvalid
		}
		return nil
	}
	if err := work.Validate(); err != nil {
		return err
	}
	quality := q.Report.Quality
	if q.Status == store.RunSucceeded && (work.Phase != store.BotWorkAccepted || quality == nil) {
		return store.ErrBotInvalid
	}
	if quality != nil {
		if quality.SchemaVersion != work.SchemaVersion || quality.WorkRevision != work.Revision || quality.RequirementsDigest != work.RequirementsDigest || quality.Outcome != work.Phase || quality.Repairs != work.Repairs || !reflect.DeepEqual(quality.Limitations, work.Unresolved) {
			return store.ErrBotInvalid
		}
		if work.Candidate == nil {
			if quality.CandidateDigest != "" {
				return store.ErrBotInvalid
			}
		} else if quality.CandidateDigest != work.Candidate.Digest {
			return store.ErrBotInvalid
		}
		if work.Review == nil {
			if len(quality.Criteria) != 0 {
				return store.ErrBotInvalid
			}
		} else if !reflect.DeepEqual(quality.Criteria, work.Review.Criteria) {
			return store.ErrBotInvalid
		}
	}
	if q.Status == store.RunSucceeded || (quality != nil && quality.Outcome == store.BotWorkAccepted) {
		if work.Candidate == nil || q.Report.Summary != work.Candidate.Text || !reflect.DeepEqual(q.Report.Artifacts, work.Candidate.Artifacts) {
			return store.ErrBotInvalid
		}
	}
	// Unassessed artifacts stay in the run trace rather than becoming deliverables.
	if len(q.Report.Artifacts) > 0 && (work.Phase != store.BotWorkAccepted || quality == nil || work.Candidate == nil || !reflect.DeepEqual(q.Report.Artifacts, work.Candidate.Artifacts)) {
		return store.ErrBotInvalid
	}
	return nil
}

func validBotArtifactPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// RecoverRun fences an expired attempt and records interruption, never success
// or replay. Callers must later review the checkpoint/effects before resuming.
// The caller-supplied lease cannot substitute for database expiry checks.
func (r *botRepository) RecoverRun(ctx context.Context, lease store.BotLease) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, lease.BotID)
		if err != nil {
			return err
		}
		if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, lease.BotID, lease.RunID)
		if err != nil {
			return err
		}
		if saved.Run.Lease.Fence != lease.Fence || saved.Run.Lease.WorkerID != lease.WorkerID {
			return store.ErrBotConflict
		}
		if saved.Run.Status == store.RunInterrupted && saved.CommitHash == "recovered" {
			return nil
		}
		now := r.clock()
		if saved.Run.Status != store.RunRunning || rec.ActiveRunID == nil || *rec.ActiveRunID != lease.RunID || rec.Fence != lease.Fence {
			return store.ErrBotConflict
		}
		if rec.LeaseExpiresAt == nil || now.Before(*rec.LeaseExpiresAt) || now.Before(saved.Run.Lease.ExpiresAt) {
			return store.ErrBotConflict
		}
		if saved.Work != nil && saved.Work.Phase != store.BotWorkAccepted && saved.Work.Phase != store.BotWorkBlocked && saved.Work.Phase != store.BotWorkInterrupted {
			saved.Work.Phase = store.BotWorkInterrupted
			saved.Work.Revision++
			saved.Work.Unresolved = append(saved.Work.Unresolved, "Run interrupted; verify prior effects before any authorized continuation.")
		}
		saved.Run.Status = store.RunInterrupted
		saved.CommitHash = "recovered"
		saved.Run.Report.Summary = "Run interrupted; effects must be checked before resuming."
		saved.Run.Report.Unverified = append(saved.Run.Report.Unverified, "Work after the last checkpoint has not been verified.")
		for i := range saved.Run.Todos {
			if saved.Run.Todos[i].Status == store.BotTodoInProgress {
				saved.Run.Todos[i].Status = store.BotTodoBlocked
				saved.Run.Todos[i].UnblocksWhen = "Verify prior effects and explicitly authorize resumption"
			}
		}
		if err := tx.FinishRun(ctx, *rec, *saved, lease, now, true); err != nil {
			return err
		}
		if err := tx.RevokeRunApprovals(ctx, lease.BotID, lease.RunID); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"run_id": lease.RunID, "status": store.RunInterrupted, "report": saved.Run.Report, "intent": store.BotOutboxPublish})
		if err != nil {
			return err
		}
		return insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: lease.BotID, RunID: &lease.RunID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityReport, Payload: payload, CreatedAt: now}, fmt.Sprintf("recovery:%s:%d", lease.RunID, lease.Fence))
	})
}

func (r *botRepository) Activate(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.enableBot(ctx, q, botdomain.LifecycleActivate)
}
func (r *botRepository) Resume(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.enableBot(ctx, q, botdomain.LifecycleResume)
}
func (r *botRepository) enableBot(ctx context.Context, q store.BotLifecycleRequest, event botdomain.LifecycleEvent) (*store.Bot, error) {
	b, err := r.Get(ctx, q.BotID)
	if err != nil {
		return nil, err
	}
	actor, err := r.auth.authorize(ctx, b.OwnerID, true)
	if err != nil {
		return nil, err
	}
	if actor != q.ActorID {
		return nil, store.ErrBotForbidden
	}
	if q.ExpectedRevision < 1 || strings.TrimSpace(q.Reason) == "" {
		return nil, store.ErrBotInvalid
	}
	if b.Revision != q.ExpectedRevision {
		return nil, store.ErrBotConflict
	}
	if _, err := botdomain.NextBotStatus(b.Status, event, r.clock(), time.Time{}); err != nil {
		return nil, err
	}
	v, err := r.db.ReadVersion(ctx, b.ID, b.Version)
	if err != nil {
		return nil, err
	}
	if err := botdomain.ValidateVersion(*v); err != nil {
		return nil, err
	}
	check := r.activationCheck
	if check == nil {
		check = r.auth.readiness
	}
	if check == nil {
		return nil, store.ErrBotUnsupported
	}
	if err := validateActivationTriggers(ctx, r.db, *b, *v); err != nil {
		return nil, err
	}
	// Readiness may contact storage/model services, so do not hold a DB transaction.
	// A revision CAS below rejects any edit made during this external check.
	if err := check(ctx, *b, *v); err != nil {
		return nil, err
	}
	var result *store.Bot
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != q.ActorID {
			return store.ErrBotForbidden
		}
		if rec.Bot.Revision != q.ExpectedRevision || rec.Bot.Version != v.Version || rec.ActiveRunID != nil {
			return store.ErrBotConflict
		}
		if err := validateActivationTriggers(ctx, tx, rec.Bot, *v); err != nil {
			return err
		}
		now := r.clock()
		status, err := botdomain.NextBotStatus(rec.Bot.Status, event, now, time.Time{})
		if err != nil {
			return err
		}
		next := rec.Bot
		next.Status = status
		next.Revision++
		next.UpdatedAt = now
		next.Scope = r.auth.scope
		if err := tx.SetLifecycle(ctx, *rec, next); err != nil {
			return err
		}
		if err := r.enableDraftActivationSchedules(ctx, tx, rec.Bot, next, *v); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"event": event, "status": status, "actor_id": actor, "revision": next.Revision, "version": next.Version, "reason": q.Reason})
		if err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: next.ID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityLifecycle, Payload: payload, CreatedAt: now}, fmt.Sprintf("lifecycle:%d", next.Revision)); err != nil {
			return err
		}
		result = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *botRepository) Pause(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.changeLifecycle(ctx, q, botdomain.LifecyclePause)
}
func (r *botRepository) Stop(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.changeLifecycle(ctx, q, botdomain.LifecycleStop)
}
func (r *botRepository) BeginDelete(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.changeLifecycle(ctx, q, botdomain.LifecycleDelete)
}
func (r *botRepository) Restore(ctx context.Context, q store.BotLifecycleRequest) (*store.Bot, error) {
	return r.changeLifecycle(ctx, q, botdomain.LifecycleRestore)
}
func (r *botRepository) changeLifecycle(ctx context.Context, q store.BotLifecycleRequest, event botdomain.LifecycleEvent) (*store.Bot, error) {
	return r.changeLifecycleIntent(ctx, q, event, false)
}
func (r *botRepository) changeLifecycleIntent(ctx context.Context, q store.BotLifecycleRequest, event botdomain.LifecycleEvent, permanent bool) (*store.Bot, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	if q.ExpectedRevision < 1 || strings.TrimSpace(q.Reason) == "" {
		return nil, store.ErrBotInvalid
	}
	var result *store.Bot
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != q.ActorID {
			return store.ErrBotForbidden
		}
		if permanent && actor != rec.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		if rec.Bot.PermanentDelete {
			if permanent && (q.ExpectedRevision == rec.Bot.Revision || q.ExpectedRevision == rec.Bot.Revision-1) {
				result = &rec.Bot
				return nil
			}
			return store.ErrBotConflict
		}
		if rec.Bot.Revision != q.ExpectedRevision {
			return store.ErrBotConflict
		}
		// Reservation is irrevocable, including after clock rollback. Its sequence
		// CAS also defeats a restore transaction that read before reservation.
		if _, claimed := rec.Metadata[botHomeCleanupClaimKey]; claimed {
			return store.ErrBotConflict
		}
		now := r.clock()
		until := time.Time{}
		if rec.Bot.RestoreUntil != nil {
			until = *rec.Bot.RestoreUntil
		}
		status, err := botdomain.NextBotStatus(rec.Bot.Status, event, now, until)
		if permanent && rec.Bot.Status == store.BotDeleted {
			status, err = store.BotDeleted, nil
		}
		if err != nil {
			return err
		}
		next := rec.Bot
		next.PermanentDelete = permanent
		next.Status = status
		next.Revision++
		next.UpdatedAt = now
		next.Scope = r.auth.scope
		if event == botdomain.LifecycleDelete {
			next.PreviousStatus = rec.Bot.Status
			next.DeletedAt = &now
			expiry := now.Add(store.BotRetention)
			if permanent {
				expiry = now
			}
			next.RestoreUntil = &expiry
		}
		if event == botdomain.LifecycleRestore {
			next.DeletedAt = nil
			next.RestoreUntil = nil
		}
		if err := tx.SetLifecycle(ctx, *rec, next); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"status": status, "event": event, "actor_id": actor, "reason": q.Reason, "revision": next.Revision, "restore_until": next.RestoreUntil, "cancel_run_id": rec.ActiveRunID})
		if err != nil {
			return err
		}
		seq := rec.LastSequence + 1
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: seq, Kind: store.BotActivityLifecycle, Payload: payload, CreatedAt: now}, fmt.Sprintf("lifecycle:%d", next.Revision)); err != nil {
			return err
		}
		if event != botdomain.LifecycleRestore {
			if err := tx.DisableBotSchedules(ctx, q.BotID); err != nil {
				return err
			}
			stop := event != botdomain.LifecyclePause
			runs, err := tx.QuiesceBotRuns(ctx, q.BotID, stop, now)
			if err != nil {
				return err
			}
			// Pause preserves undecided requests for owner review; grants are revoked.
			if err := tx.RevokeBotApprovals(ctx, q.BotID, stop); err != nil {
				return err
			}
			for _, run := range runs {
				if err := tx.AdvanceSequence(ctx, q.BotID, seq); err != nil {
					return err
				}
				seq++
				body, err := json.Marshal(map[string]any{"run_id": run.ID, "status": run.Status, "report": run.Report, "intent": store.BotOutboxPublish})
				if err != nil {
					return err
				}
				if err := insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: q.BotID, RunID: &run.ID, Sequence: seq, Kind: store.BotActivityReport, Payload: body, CreatedAt: now}, fmt.Sprintf("lifecycle-result:%d:%s", next.Revision, run.ID)); err != nil {
					return err
				}
			}
		}
		if event == botdomain.LifecycleDelete {
			if err := tx.AdvanceSequence(ctx, q.BotID, seq); err != nil {
				return err
			}
			seq++
			body, err := json.Marshal(map[string]any{"intent": store.BotOutboxCleanup, "not_before": next.RestoreUntil, "revision": next.Revision, "home": next.Home, "cancel_run_id": rec.ActiveRunID})
			if err != nil {
				return err
			}
			if err := tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: seq, Kind: store.BotActivityLifecycle, Payload: body, CreatedAt: now}, fmt.Sprintf("cleanup:%d", next.Revision)); err != nil {
				return err
			}
		}
		if event == botdomain.LifecycleDelete {
			fresh, err := tx.GetBot(ctx, q.BotID)
			if err != nil {
				return err
			}
			if err = r.revokeOperationStateTx(ctx, tx, fresh); err != nil {
				return err
			}
		}
		result = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func quiescedBotRun(saved botStoredRun, stop bool, now time.Time) botStoredRun {
	wasRunning := saved.Run.Status == store.RunRunning
	if wasRunning {
		saved.Run.Status = store.RunInterrupted
		saved.Run.Report.Summary = "Work interrupted by owner; prior effects require verification."
		saved.Run.Report.Unverified = append(saved.Run.Report.Unverified, "The last in-flight action may have taken effect.")
	} else if stop || saved.Run.Status == store.RunQueued {
		saved.Run.Status = store.RunCancelled
		saved.Run.FinishedAt = &now
		saved.Run.Report.Summary = "Pending work cancelled by owner."
	}
	for i := range saved.Run.Todos {
		if saved.Run.Todos[i].Status == store.BotTodoInProgress {
			saved.Run.Todos[i].Status = store.BotTodoBlocked
			saved.Run.Todos[i].UnblocksWhen = "Owner authorizes resumption after prior effects are checked"
		}
	}
	saved.CommitHash = ""
	return saved
}
func botLifecycleMetadata(rec botStoredRecord, next store.Bot) map[string]any {
	data := make(map[string]any, len(rec.Metadata)+1)
	for k, v := range rec.Metadata {
		data[k] = v
	}
	data["previous_status"] = string(next.PreviousStatus)
	data["permanent_delete"] = next.PermanentDelete
	return data
}

// PurgeDue removes only cleanup-confirmed tombstones whose retention has elapsed.
// External cleanup is deliberately not performed inside a database transaction.
func (r *botRepository) PurgeDue(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return 0, err
	}
	now := r.clock()
	if cutoff.IsZero() || cutoff.After(now) || limit < 1 || limit > 200 {
		return 0, store.ErrBotInvalid
	}
	candidates, err := r.db.ListPurgeable(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, candidate := range candidates {
		// Permanent requests use the owner-authorized exact-target path (worker
		// polling or HTTP), never this compatibility batch sweep.
		if candidate.Bot.PermanentDelete {
			continue
		}
		if _, err := r.auth.authorize(ctx, candidate.Bot.OwnerID, true); err != nil {
			return count, err
		}
		err := r.db.Transaction(ctx, func(tx botDraftDB) error {
			rec, err := tx.GetBot(ctx, candidate.Bot.ID)
			if err != nil {
				return err
			}
			if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
				return err
			}
			if rec.Bot.PermanentDelete || rec.Bot.Revision != candidate.Bot.Revision || rec.Bot.Status != store.BotDeleted || rec.ActiveRunID != nil || rec.Bot.RestoreUntil == nil || rec.Bot.RestoreUntil.After(cutoff) {
				return store.ErrBotConflict
			}
			return tx.PurgeBot(ctx, *rec, cutoff)
		})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (r *botRepository) MarkRead(ctx context.Context, cursor store.BotReadCursor) error {
	actor, err := r.auth.authorize(ctx, uuid.Nil, false)
	if err != nil {
		return err
	}
	if actor != cursor.ViewerID {
		return store.ErrBotForbidden
	}
	if cursor.Sequence < 0 {
		return store.ErrBotInvalid
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, cursor.BotID)
		if err != nil {
			return err
		}
		if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
			return err
		}
		if cursor.Sequence > rec.LastSequence {
			return store.ErrBotInvalid
		}
		return tx.SaveReadCursor(ctx, *rec, cursor)
	})
}
func botCursorMetadata(rec botStoredRecord, cursor store.BotReadCursor) (map[string]any, error) {
	data := botLifecycleMetadata(rec, rec.Bot)
	cursors := map[string]string{}
	if existing, ok := rec.Metadata["read_cursors"]; ok {
		raw, err := json.Marshal(existing)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &cursors); err != nil {
			return nil, store.ErrBotInvalid
		}
	}
	key := cursor.ViewerID.String()
	if previous, ok := cursors[key]; ok {
		var old int64
		if err := json.Unmarshal([]byte(previous), &old); err != nil {
			return nil, store.ErrBotInvalid
		}
		if old >= cursor.Sequence {
			return nil, nil
		}
	}
	// Decimal strings prevent loss of precision through generic Ent JSON maps.
	cursors[key] = fmt.Sprintf("%d", cursor.Sequence)
	data["read_cursors"] = cursors
	return data, nil
}

func (r *botRepository) ListTrackedItems(ctx context.Context, id store.BotID, goalID uuid.UUID) ([]store.BotTrackedItem, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	if _, err := r.db.ReadGoal(ctx, id, goalID); err != nil {
		return nil, err
	}
	return r.db.ReadTrackedItems(ctx, goalID)
}

// expected is the parent goal revision. Updating a commitment increments that
// revision so completion cannot race with a newly opened responsibility.
func (r *botRepository) SaveTrackedItem(ctx context.Context, id store.BotID, item store.BotTrackedItem, expected int64) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	if expected < 1 || item.ID == uuid.Nil || item.GoalID == uuid.Nil || strings.TrimSpace(item.Title) == "" {
		return store.ErrBotInvalid
	}
	switch item.Status {
	case store.BotTrackedOpen:
	case store.BotTrackedBlocked, store.BotTrackedCancelled:
		if strings.TrimSpace(item.Reason) == "" {
			return store.ErrBotInvalid
		}
	case store.BotTrackedCompleted:
		if len(item.Evidence) == 0 {
			return store.ErrBotInvalid
		}
	default:
		return store.ErrBotInvalid
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if rec.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		g, err := tx.ReadGoal(ctx, id, item.GoalID)
		if err != nil {
			return err
		}
		if g.Revision != expected || g.Status != store.BotGoalActive {
			return store.ErrBotConflict
		}
		for _, e := range item.Evidence {
			if err := e.ValidateCompletion(); err != nil {
				return err
			}
			if e.Kind == store.BotEvidenceOwnerConfirmation && e.ActorID != actor {
				return store.ErrBotForbidden
			}
			if e.ObservedAt.After(r.clock()) {
				return store.ErrBotInvalid
			}
		}
		if err := tx.AdvanceSequence(ctx, id, rec.LastSequence); err != nil {
			return err
		}
		g.Revision++
		if err := tx.UpdateGoal(ctx, *g, expected); err != nil {
			return err
		}
		if err := tx.WriteTrackedItem(ctx, item, g.Revision); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"goal_id": g.ID, "tracked_item_id": item.ID, "status": item.Status, "revision": g.Revision, "actor_id": actor})
		if err != nil {
			return err
		}
		return tx.InsertActivity(ctx, store.BotActivity{BotID: id, Sequence: rec.LastSequence + 1, Kind: store.BotActivityCorrection, Payload: payload, CreatedAt: r.clock()}, fmt.Sprintf("tracked:%s:%d", item.ID, g.Revision))
	})
}
func (r *botRepository) CompleteGoal(ctx context.Context, q store.BotGoalCompletion) (*store.BotGoal, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	if q.ExpectedRevision < 1 || len(q.Evidence) == 0 {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotGoal
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != q.ActorID || rec.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		now := r.clock()
		for _, e := range q.Evidence {
			if err := e.ValidateCompletion(); err != nil {
				return err
			}
			if e.Kind == store.BotEvidenceOwnerConfirmation && e.ActorID != actor {
				return store.ErrBotForbidden
			}
			if e.ObservedAt.After(now) {
				return store.ErrBotInvalid
			}
		}
		g, err := tx.ReadGoal(ctx, q.BotID, q.GoalID)
		if err != nil {
			return err
		}
		if g.Revision != q.ExpectedRevision || g.Status != store.BotGoalActive {
			return store.ErrBotConflict
		}
		items, err := tx.ReadTrackedItems(ctx, q.GoalID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Status != store.BotTrackedCompleted && item.Status != store.BotTrackedCancelled {
				return store.ErrBotConflict
			}
		}
		if err := tx.AdvanceSequence(ctx, q.BotID, rec.LastSequence); err != nil {
			return err
		}
		g.Revision++
		g.Status = store.BotGoalCompleted
		g.CompletedAt = &now
		g.Evidence = q.Evidence
		if err := tx.UpdateGoal(ctx, *g, q.ExpectedRevision); err != nil {
			return err
		}
		if err := tx.DisableGoalSchedules(ctx, q.BotID, q.GoalID); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"goal_id": g.ID, "status": g.Status, "revision": g.Revision, "actor_id": actor, "evidence": g.Evidence})
		if err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityCorrection, Payload: payload, CreatedAt: now}, fmt.Sprintf("goal:%s:%d", g.ID, g.Revision)); err != nil {
			return err
		}
		result = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Correct records an immutable replacement and reconciliation intent together.
// It does not change a policy/version or claim that another store applied it.
func (r *botRepository) Correct(ctx context.Context, q store.BotCorrection) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	if q.ID == uuid.Nil || q.BotID == uuid.Nil || q.ExpectedRevision < 1 || strings.TrimSpace(q.SupersededSource) == "" || strings.TrimSpace(q.Replacement) == "" {
		return store.ErrBotInvalid
	}
	if err := q.Evidence.ValidateCompletion(); err != nil {
		return err
	}
	seen := make(map[uuid.UUID]bool, len(q.GoalIDs))
	for _, id := range q.GoalIDs {
		if id == uuid.Nil || seen[id] {
			return store.ErrBotInvalid
		}
		seen[id] = true
	}
	hash, err := botHash(q)
	if err != nil {
		return err
	}
	key := "correction:" + q.ID.String()
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if q.Evidence.ActorID != actor || rec.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		now := r.clock()
		if q.Evidence.ObservedAt.After(now) {
			return store.ErrBotInvalid
		}
		prior, err := tx.FindActivity(ctx, q.BotID, key)
		if err == nil {
			var saved struct {
				RequestHash string `json:"request_hash"`
			}
			if err := json.Unmarshal(prior.Payload, &saved); err != nil {
				return err
			}
			if prior.Kind != store.BotActivityCorrection || saved.RequestHash != hash {
				return store.ErrBotConflict
			}
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		if rec.Bot.Revision != q.ExpectedRevision {
			return store.ErrBotConflict
		}
		for _, id := range q.GoalIDs {
			if _, err := tx.ReadGoal(ctx, q.BotID, id); err != nil {
				return err
			}
		}
		if err := tx.AdvanceCorrection(ctx, *rec, now); err != nil {
			return err
		}
		// Stop only explicitly affected follow-ups. Reconciliation must review the
		// replacement before rescheduling; correction never implicitly enables work.
		for _, id := range q.GoalIDs {
			if err := tx.DisableGoalSchedules(ctx, q.BotID, id); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(struct {
			Correction  store.BotCorrection `json:"correction"`
			Intent      store.BotOutboxKind `json:"intent"`
			Revision    int64               `json:"revision"`
			RequestHash string              `json:"request_hash"`
		}{q, store.BotOutboxReconcile, rec.Bot.Revision + 1, hash})
		if err != nil {
			return err
		}
		return tx.InsertActivity(ctx, store.BotActivity{BotID: q.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityCorrection, Payload: payload, CreatedAt: now}, key)
	})
}

// ReconcileCorrection applies only an exact, persisted memory correction. Memory
// and receipt share this scoped Ent transaction (not a cross-store transaction).
// Embeddings are invalidated rather than carrying the old fact into retrieval;
// keyword retrieval remains available until normal re-embedding.
func (r *botRepository) ReconcileCorrection(ctx context.Context, botID store.BotID, correctionID uuid.UUID, memories store.MemoryStore) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	if botID == uuid.Nil || correctionID == uuid.Nil {
		return store.ErrBotInvalid
	}
	// Accept only the actual router store sharing this database client. Metadata,
	// source paths, creator IDs and a caller-supplied store cannot establish scope.
	var sqlite bool
	var invalidate func(string)
	switch db := r.db.(type) {
	case teamBotDB:
		m, ok := memories.(*teamMemoryStore)
		if !ok || m == nil || m.client != db.client || r.auth.scope != store.BotScopeTeam {
			return store.ErrBotForbidden
		}
		sqlite = m.dialect == DialectSQLite
		invalidate = func(id string) {
			if m.vecIndex != nil {
				m.vecIndex.remove(id)
			}
		}
	case personalBotDB:
		m, ok := memories.(*personalMemoryStore)
		if !ok || m == nil || m.client != db.client || r.auth.scope != store.BotScopePersonal {
			return store.ErrBotForbidden
		}
		sqlite = m.dialect == DialectSQLite
		invalidate = func(id string) {
			if m.vecIndex != nil {
				m.vecIndex.remove(id)
			}
		}
	default:
		return store.ErrBotUnsupported
	}
	var memoryID string
	invalidateMemory := false
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, botID)
		if err != nil {
			return err
		}
		if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
			return err
		}
		if rec.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		intent, err := tx.FindActivity(ctx, botID, "correction:"+correctionID.String())
		if err != nil {
			return err
		}
		var saved struct {
			Correction  store.BotCorrection `json:"correction"`
			Intent      store.BotOutboxKind `json:"intent"`
			RequestHash string              `json:"request_hash"`
		}
		if err := json.Unmarshal(intent.Payload, &saved); err != nil {
			return err
		}
		q := saved.Correction
		hash, err := botHash(q)
		if err != nil {
			return err
		}
		if intent.Kind != store.BotActivityCorrection || saved.Intent != store.BotOutboxReconcile || q.BotID != botID || q.ID != correctionID || saved.RequestHash != hash {
			return store.ErrBotInvalid
		}
		if !strings.HasPrefix(q.SupersededSource, "memory:") {
			return store.ErrBotUnsupported
		}
		id, err := uuid.Parse(strings.TrimPrefix(q.SupersededSource, "memory:"))
		if err != nil || id == uuid.Nil {
			return store.ErrBotInvalid
		}
		memoryID = id.String()
		key := "correction-memory:" + correctionID.String()
		if _, err := tx.FindActivity(ctx, botID, key); err == nil {
			return nil
		} else if err != store.ErrBotNotFound {
			return err
		}
		// A delayed older intent must not undo a newer correction of this source.
		// Page the durable log rather than trusting the current memory text.
		for after := intent.Sequence; ; {
			activities, err := tx.ReadActivity(ctx, botID, after, 200)
			if err != nil {
				return err
			}
			for _, activity := range activities {
				after = activity.Sequence
				if activity.Kind != store.BotActivityCorrection {
					continue
				}
				var later struct {
					Correction store.BotCorrection `json:"correction"`
					Intent     store.BotOutboxKind `json:"intent"`
				}
				if err := json.Unmarshal(activity.Payload, &later); err != nil {
					return err
				}
				laterID, parseErr := uuid.Parse(strings.TrimPrefix(later.Correction.SupersededSource, "memory:"))
				if later.Intent == store.BotOutboxReconcile && strings.HasPrefix(later.Correction.SupersededSource, "memory:") && parseErr == nil && laterID == id {
					return store.ErrBotConflict
				}
			}
			if len(activities) < 200 {
				break
			}
		}
		// Read through the existing attributed memory interface. Scope was checked
		// structurally above; the update below also compares content and session.
		row, err := tx.ReadCorrectionMemory(ctx, memoryID)
		if err != nil {
			return err
		}
		if row == nil {
			return store.ErrBotNotFound
		}
		sessionID, err := uuid.Parse(row.SessionID)
		if err != nil || sessionID == uuid.Nil {
			return store.ErrBotForbidden
		}
		linkedBot, err := tx.CorrectionSessionBot(ctx, sessionID)
		if err != nil {
			return err
		}
		if linkedBot != botID {
			return store.ErrBotForbidden
		}
		// Lock/CAS the root first, just as correction recording, deletion and
		// purge do. Memory CAS and receipt commit together or both roll back.
		if err := tx.AdvanceSequence(ctx, botID, rec.LastSequence); err != nil {
			return err
		}
		if err := validateMemoryAttribution(ctx, tx, row.SessionID, row.Attribution); err != nil {
			return err
		}
		if row.Attribution != nil {
			annotator, ok := tx.(interface {
				SupersedeMemoryAttribution(context.Context, uuid.UUID, string) error
			})
			if !ok {
				return store.ErrBotUnsupported
			}
			if err := annotator.SupersedeMemoryAttribution(ctx, id, "correction:"+correctionID.String()); err != nil {
				return err
			}
		}
		invalidateMemory = true
		if err := tx.ApplyCorrectionMemory(ctx, id, sessionID, row.Snippet, q.Replacement, sqlite); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"correction_id": correctionID, "memory_id": id, "memory_applied": true})
		if err != nil {
			return err
		}
		return tx.InsertActivity(ctx, store.BotActivity{BotID: botID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityCorrection, Payload: payload, CreatedAt: r.clock()}, key)
	})
	// Invalidate after an attempted mutation, including ambiguous commit. A
	// receipt-only replay must not evict a newer embedding of the same row.
	if invalidateMemory {
		invalidate(memoryID)
	}
	return err
}

func botJSONMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Ent decodes JSON maps through float64. Keep the typed document as a JSON
	// string envelope so exact action IDs and integer budgets survive a DB read.
	return map[string]any{"bot_document": string(b)}, nil
}
func botDecode(m map[string]any, out any) error {
	if document, exists := m["bot_document"]; exists {
		text, ok := document.(string)
		if !ok {
			return store.ErrBotInvalid
		}
		return json.Unmarshal([]byte(text), out)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func botHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func botPage(page store.BotPage) (int, uuid.UUID, error) {
	limit := page.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return 0, uuid.Nil, store.ErrBotInvalid
	}
	after := uuid.Nil
	if page.After != "" {
		var err error
		after, err = uuid.Parse(page.After)
		if err != nil {
			return 0, uuid.Nil, store.ErrBotInvalid
		}
	}
	return limit, after, nil
}
func (r *botRepository) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	rec, err := r.db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
		return nil, err
	}
	b := rec.Bot
	b.Scope = r.auth.scope
	return &b, nil
}
func (r *botRepository) List(ctx context.Context, page store.BotPage) ([]store.Bot, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	limit, after, err := botPage(page)
	if err != nil {
		return nil, err
	}
	records, err := r.db.ListBots(ctx, limit, after)
	if err != nil {
		return nil, err
	}
	result := make([]store.Bot, 0, len(records))
	for _, rec := range records {
		if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
			return nil, err
		}
		rec.Bot.Scope = r.auth.scope
		result = append(result, rec.Bot)
	}
	return result, nil
}
func (r *botRepository) GetVersion(ctx context.Context, id store.BotID, version int64) (*store.BotVersion, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	if version < 1 {
		return nil, store.ErrBotInvalid
	}
	return r.db.ReadVersion(ctx, id, version)
}

func prepareBotDraft(req store.BotCreateRequest, actor uuid.UUID, scope store.BotScope, now time.Time) (botStoredRecord, store.BotVersion, []store.BotGoal, error) {
	if req.OwnerID != actor || strings.TrimSpace(req.Name) == "" || len(req.Name) > 200 || strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 200 {
		return botStoredRecord{}, store.BotVersion{}, nil, store.ErrBotInvalid
	}
	// Marshal/unmarshal the caller's object before normalizing it: never mutate a
	// caller-owned slice, nested pointer or version while saving a draft.
	bytes, err := json.Marshal(req)
	if err != nil {
		return botStoredRecord{}, store.BotVersion{}, nil, err
	}
	var input store.BotCreateRequest
	if err := json.Unmarshal(bytes, &input); err != nil {
		return botStoredRecord{}, store.BotVersion{}, nil, err
	}
	hash, err := botHash(input)
	if err != nil {
		return botStoredRecord{}, store.BotVersion{}, nil, err
	}
	key, err := botHash([]string{actor.String(), input.IdempotencyKey})
	if err != nil {
		return botStoredRecord{}, store.BotVersion{}, nil, err
	}
	id := uuid.New()
	v := input.Version
	v.BotID = id
	v.Version = 1
	v.CreatedBy = actor
	v.CreatedAt = now
	if err := botdomain.ValidateVersion(v); err != nil {
		return botStoredRecord{}, v, nil, err
	}
	// Drafts can describe future schedules, but can never carry live authority.
	for i := range v.Triggers {
		v.Triggers[i].Enabled = false
		v.Triggers[i].ScheduleID = nil
		v.Triggers[i].HookStatus = store.HookDisabled
	}
	v.Autonomy.Level = execution.AutonomySupervised
	v.Autonomy.CanContactPeople = false
	v.Autonomy.CanPublish = false
	v.Autonomy.MaxSpendPerRunMinor = 0
	v.Autonomy.MaxSpendPerMonthMinor = 0
	v.Delivery.Bindings = nil
	goals := input.Goals
	for i := range goals {
		if strings.TrimSpace(goals[i].Title) == "" || strings.TrimSpace(goals[i].CompletionCriteria) == "" {
			return botStoredRecord{}, v, nil, store.ErrBotInvalid
		}
		goals[i].ID = uuid.New()
		goals[i].BotID = id
		goals[i].Revision = 1
		goals[i].Status = store.BotGoalActive
		goals[i].CompletedAt = nil
		goals[i].Evidence = nil
		goals[i].WorkspacePath = "goals/" + goals[i].ID.String()
	}
	rec := botStoredRecord{Bot: store.Bot{ID: id, OwnerID: actor, Scope: scope, Name: input.Name, Status: store.BotDraft, Revision: 1, Version: 1, HomeSessionID: uuid.New(), CreatedAt: now, UpdatedAt: now}, CreateKey: key, CreateHash: hash, LastSequence: 1}
	return rec, v, goals, nil
}
func (r *botRepository) CreateDraft(ctx context.Context, req store.BotCreateRequest) (*store.Bot, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	rec, v, goals, err := prepareBotDraft(req, actor, r.auth.scope, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	var result *store.Bot
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		old, err := tx.FindCreate(ctx, rec.CreateKey)
		if err == nil {
			if old.CreateHash != rec.CreateHash {
				return store.ErrBotConflict
			}
			old.Bot.Scope = r.auth.scope
			result = &old.Bot
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		if err := tx.InsertBot(ctx, rec); err != nil {
			return err
		}
		hash, err := botHash(v)
		if err != nil {
			return err
		}
		if err := tx.InsertVersion(ctx, v, hash); err != nil {
			return err
		}
		if err := tx.InsertHomeSession(ctx, rec.Bot); err != nil {
			return err
		}
		for _, g := range goals {
			if err := tx.InsertGoal(ctx, g); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(map[string]any{"status": store.BotDraft, "version": 1, "actor_id": actor})
		if err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, Sequence: 1, Kind: store.BotActivityLifecycle, Payload: payload, CreatedAt: rec.Bot.CreatedAt}, "draft-created"); err != nil {
			return err
		}
		result = &rec.Bot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (r *botRepository) ListGoals(ctx context.Context, id store.BotID) ([]store.BotGoal, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	return r.db.ReadGoals(ctx, id)
}
func (r *botRepository) ListActivity(ctx context.Context, id store.BotID, after int64, limit int) ([]store.BotActivity, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, store.ErrBotInvalid
	}
	n, _, err := botPage(store.BotPage{Limit: limit})
	if err != nil {
		return nil, err
	}
	return r.db.ReadActivity(ctx, id, after, n)
}
func (r *botRepository) SaveGoal(ctx context.Context, req store.BotGoalUpdate) (*store.BotGoal, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	g := req.Goal
	if g.BotID != req.BotID || strings.TrimSpace(g.Title) == "" || strings.TrimSpace(g.CompletionCriteria) == "" || req.ExpectedRevision < 0 {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotGoal
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, req.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if rec.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		if req.ExpectedRevision == 0 {
			if g.ID != uuid.Nil {
				return store.ErrBotInvalid
			}
			g.ID = uuid.New()
			g.Revision = 1
			g.Status = store.BotGoalActive
			g.Evidence = nil
			g.CompletedAt = nil
			g.WorkspacePath = "goals/" + g.ID.String()
		} else {
			old, err := tx.ReadGoal(ctx, req.BotID, g.ID)
			if err != nil {
				return err
			}
			if old.Revision != req.ExpectedRevision {
				return store.ErrBotConflict
			}
			if old.Status != store.BotGoalActive || g.Status != store.BotGoalActive || g.CompletedAt != nil || len(g.Evidence) > 0 {
				return store.ErrBotForbidden
			}
			g.Revision = old.Revision + 1
			g.WorkspacePath = old.WorkspacePath
		}
		// A root sequence CAS serializes concurrent goal and configuration changes.
		if err := tx.AdvanceSequence(ctx, req.BotID, rec.LastSequence); err != nil {
			return err
		}
		if req.ExpectedRevision == 0 {
			err = tx.InsertGoal(ctx, g)
		} else {
			err = tx.UpdateGoal(ctx, g, req.ExpectedRevision)
		}
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"goal_id": g.ID, "revision": g.Revision, "actor_id": actor})
		if err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: req.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityCorrection, Payload: payload, CreatedAt: time.Now().UTC()}, fmt.Sprintf("goal:%s:%d", g.ID, g.Revision)); err != nil {
			return err
		}
		result = &g
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ApplyVersion accepts draft edits and inert persona/charter corrections while
// paused or stopped. Active-policy changes require the supervisor's lifecycle
// transaction; an inactive prose edit neither changes policy nor resumes work.
func (r *botRepository) ApplyVersion(ctx context.Context, req store.BotVersionRequest) (*store.Bot, error) {
	if req.ExpectedRevision < 1 {
		return nil, store.ErrBotInvalid
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	var result *store.Bot
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, req.BotID)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		switch rec.Bot.Status {
		case store.BotDraft, store.BotPaused, store.BotStopped:
		default:
			return store.ErrBotUnsupported
		}
		if rec.Bot.Revision != req.ExpectedRevision {
			return store.ErrBotConflict
		}
		v := req.Version
		if v.BotID != req.BotID {
			return store.ErrBotInvalid
		}
		v.Version = rec.Bot.Version + 1
		v.CreatedBy = actor
		v.CreatedAt = time.Now().UTC()
		if err := botdomain.ValidateVersion(v); err != nil {
			return err
		}
		{
			// All ordinary version edits are prose-only, including reviewed drafts.
			// Runtime policy replacement requires ConfigureReviewed and its CAS.
			current, err := tx.ReadVersion(ctx, req.BotID, rec.Bot.Version)
			if err != nil {
				return err
			}
			allowed := *current
			allowed.Persona, allowed.Charter, allowed.ChangeReason = v.Persona, v.Charter, v.ChangeReason
			allowed.Version, allowed.CreatedBy, allowed.CreatedAt = v.Version, v.CreatedBy, v.CreatedAt
			allowedHash, err := botHash(allowed)
			if err != nil {
				return err
			}
			nextHash, err := botHash(v)
			if err != nil {
				return err
			}
			if allowedHash != nextHash {
				return store.ErrBotForbidden
			}
		}
		hash, err := botHash(v)
		if err != nil {
			return err
		}
		if err := tx.UpdateVersion(ctx, req.BotID, req.ExpectedRevision, v.Version, rec.LastSequence+1, v.CreatedAt); err != nil {
			return err
		}
		if err := tx.InsertVersion(ctx, v, hash); err != nil {
			return err
		}
		if rec.Bot.Status != store.BotDraft {
			// Pause retains pending requests, but they reference the old version
			// and must not remain actionable after a configuration correction.
			if err := tx.RevokeBotApprovals(ctx, req.BotID, true); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(map[string]any{"version": v.Version, "actor_id": actor})
		if err != nil {
			return err
		}
		if err := tx.InsertActivity(ctx, store.BotActivity{BotID: req.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityPersona, Payload: payload, CreatedAt: v.CreatedAt}, fmt.Sprintf("version:%d", v.Version)); err != nil {
			return err
		}
		rec.Bot.Version = v.Version
		rec.Bot.Revision++
		rec.Bot.UpdatedAt = v.CreatedAt
		rec.Bot.Scope = r.auth.scope
		result = &rec.Bot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
