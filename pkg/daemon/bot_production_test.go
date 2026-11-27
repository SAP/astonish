package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type employeeLiveGuardRepo struct {
	*workerEventRepo
	approval     *store.BotApproval
	loadErr      error
	loadedLease  store.BotLease
	consumptions int
}

func (r *employeeLiveGuardRepo) RequeueApprovedRun(context.Context, store.BotID, store.RunID, uuid.UUID) error {
	return store.ErrBotUnsupported
}
func (r *employeeLiveGuardRepo) LoadResumeApproval(_ context.Context, lease store.BotLease) (*store.BotApproval, error) {
	r.loadedLease = lease
	return r.approval, r.loadErr
}
func (r *employeeLiveGuardRepo) ConsumeApproval(context.Context, store.BotApprovalConsumption) error {
	r.consumptions++
	return nil
}

func TestEmployeeWorkerProductionLiveGuard(t *testing.T) {
	for _, variant := range []string{"verified_read", "unverified_read", "unknown", "unsafe", "approved", "wrong_action", "wrong_approval", "wrong_bot", "wrong_run", "wrong_version", "expired_approval", "pending_approval", "invalid_saved_action", "readback_failure", "spend", "missing_marker", "foreign_marker", "foreign_identity", "foreign_principal", "owner_revoked", "version_revoked", "fence_revoked", "worker_revoked", "session_revoked", "expired_lease", "turn_revoked", "expired_turn", "turn_identity_changed", "turn_tool_revoked", "missing_authority", "cancelled"} {
		t.Run(variant, func(t *testing.T) {
			owner := uuid.New()
			p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
			ctx, err := execution.WithPrincipal(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			expires := time.Now().Add(time.Minute)
			if variant == "expired_turn" {
				expires = time.Now().Add(50 * time.Millisecond)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: expires})
			if err != nil {
				t.Fatal(err)
			}
			id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			registry := execution.NewBotTurnAuthorizationRegistry(1)
			defer registry.Close()
			authority, err := registry.Register(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			current := p
			auth, err := registry.Authorizer(id, authority.CallerCeiling(), func(context.Context, execution.Principal) (execution.Principal, error) { return current, nil })
			if err != nil {
				t.Fatal(err)
			}
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			run := store.BotRun{ID: id.RunID, BotID: id.BotID, Version: 1, SessionID: uuid.New(), Status: store.RunRunning, Intent: store.BotIntentConversation, ExecutionMode: execution.BotExecutionModeChatInteractive, Lease: store.BotLease{BotID: id.BotID, RunID: id.RunID, Fence: 2, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}}
			req := bot.RunRequest{Principal: p, Bot: store.Bot{ID: id.BotID, OwnerID: owner, Status: store.BotPaused, Version: 1}, Version: store.BotVersion{Version: 1}, Run: run, Budget: meter, Authorization: auth}
			repo := &employeeLiveGuardRepo{workerEventRepo: &workerEventRepo{b: req.Bot, r: run}}
			ctx = execution.WithBotRunIdentity(ctx, id)
			if variant != "missing_marker" {
				ctx = agent.WithBotPrivateWorkerExecution(ctx)
			}
			action := execution.ActionDescriptor{Tool: "read_file", Arguments: json.RawMessage(`{"path":"/exact"}`), Effect: execution.EffectRead, ConstraintsVerified: true}
			expect := execution.PolicyDeny
			if variant == "verified_read" {
				expect = execution.PolicyAllow
			}
			if variant == "unverified_read" || variant == "unknown" || variant == "unsafe" {
				expect = execution.PolicyRequestApproval
			}
			switch variant {
			case "unverified_read":
				action.ConstraintsVerified = false
			case "unknown":
				action.Effect = execution.EffectUnknown
			case "unsafe":
				action.Effect = execution.EffectSend
			case "spend":
				action.Effect = execution.EffectSpend
			case "foreign_marker", "foreign_identity":
				other := id
				other.RunID = uuid.New()
				ctx = execution.WithBotRunIdentity(ctx, other)
				if variant == "foreign_identity" {
					ctx = agent.WithBotPrivateWorkerExecution(ctx)
				}
			case "foreign_principal":
				other := p
				other.Subject = uuid.NewString()
				ctx, err = execution.WithPrincipal(ctx, other)
				if err != nil {
					t.Fatal(err)
				}
			case "owner_revoked":
				repo.b.OwnerID = uuid.New()
			case "version_revoked":
				repo.b.Version++
			case "fence_revoked":
				repo.r.Lease.Fence++
			case "worker_revoked":
				repo.r.Lease.WorkerID = "foreign"
			case "session_revoked":
				repo.r.SessionID = uuid.New()
			case "expired_lease":
				repo.r.Lease.ExpiresAt = time.Now().Add(-time.Second)
			case "expired_turn":
				time.Sleep(time.Until(expires) + time.Millisecond)
			case "turn_revoked":
				registry.Cancel(id)
			case "turn_identity_changed":
				current.Subject = uuid.NewString()
			case "turn_tool_revoked":
				current.Scopes = []string{"chat"}
			case "missing_authority":
				req.Authorization = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			approvedCases := map[string]bool{"approved": true, "wrong_action": true, "wrong_approval": true, "wrong_bot": true, "wrong_run": true, "wrong_version": true, "expired_approval": true, "pending_approval": true, "invalid_saved_action": true, "readback_failure": true}
			if approvedCases[variant] {
				action.Effect = execution.EffectSend
				digest, _ := action.Digest()
				approval := store.BotApproval{ID: uuid.New(), BotID: id.BotID, RunID: id.RunID, Version: 1, Action: action, ActionDigest: digest, Status: store.ApprovalApproved, ExpiresAt: time.Now().Add(time.Minute)}
				checkpoint := bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: approval.ID, BotID: id.BotID, RunID: id.RunID, SessionID: run.SessionID, Version: 1, ActionDigest: digest, Fence: 1}
				req.Run.Checkpoint, _ = json.Marshal(checkpoint)
				switch variant {
				case "approved":
					expect = execution.PolicyAllow
				case "wrong_action":
					action.Arguments = json.RawMessage(`{"path":"/other"}`)
				case "wrong_approval":
					approval.ID = uuid.New()
				case "wrong_bot":
					approval.BotID = uuid.New()
				case "wrong_run":
					approval.RunID = uuid.New()
				case "wrong_version":
					approval.Version++
				case "expired_approval":
					approval.ExpiresAt = time.Now().Add(-time.Second)
				case "pending_approval":
					approval.Status = store.ApprovalPending
				case "invalid_saved_action":
					approval.Action.Arguments = json.RawMessage(`{"path":"/other"}`)
				case "readback_failure":
					repo.loadErr = store.ErrBotConflict
				}
				repo.approval = &approval
			}
			decision, err := (botLiveGuard{repo: repo, request: req}).Authorize(ctx, action)
			if expect == execution.PolicyDeny {
				if err == nil || decision.Kind == execution.PolicyAllow {
					t.Fatalf("unsafe allow: %+v %v", decision, err)
				}
			} else {
				digest, _ := action.Digest()
				if err != nil || decision.Kind != expect || decision.ActionDigest != digest {
					t.Fatalf("decision=%+v error=%v want=%s", decision, err, expect)
				}
			}
			if variant == "approved" && (decision.GrantID != repo.approval.ID || repo.loadedLease != req.Run.Lease) {
				t.Fatal("approval not read under exact current lease")
			}
			if repo.consumptions != 0 {
				t.Fatal("live guard consumed resume-owned approval")
			}
		})
	}
}

func TestBotLiveGuardRevocationAndFence(t *testing.T) {
	ctx := context.Background()
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, runID, owner := uuid.New(), uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: id, OwnerID: owner, Status: store.BotActive, Version: 1}, Version: store.BotVersion{Version: 1}, Run: store.BotRun{ID: runID, SessionID: uuid.New(), Status: store.RunRunning, Lease: store.BotLease{Fence: 1, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}}, Budget: meter}
	repo := &workerEventRepo{b: req.Bot, r: req.Run}
	guard := botLiveGuard{repo: repo, request: req}
	if err := guard.check(ctx); err != nil {
		t.Fatal(err)
	}
	repo.b.Status = store.BotPaused
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
	repo.b = req.Bot
	repo.r.Lease.Fence++
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	repo.r = req.Run
	repo.r.Lease.ExpiresAt = time.Now().Add(-time.Second)
	if err := guard.check(ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
}

type workerDraftRepo struct {
	store.BotStore
	bots []store.Bot
}

func (workerDraftRepo) ListActivity(context.Context, store.BotID, int64, int) ([]store.BotActivity, error) {
	return nil, nil
}
func (r workerDraftRepo) List(context.Context, store.BotPage) ([]store.Bot, error) {
	return r.bots, nil
}
func (workerDraftRepo) ListRuns(_ context.Context, id store.BotID, _ store.BotPage) ([]store.BotRun, error) {
	// Paused Bots allow tool-free conversation only; a queued regular run
	// must never resume or execute merely because it is queued.
	return []store.BotRun{{ID: uuid.New(), BotID: id, Status: store.RunQueued, Intent: store.BotIntentRegular}}, nil
}
func TestBotWorkerPollRejectsPausedRegularRun(t *testing.T) {
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	w := &BotWorker{}
	if err := w.pollStore(ctx, workerDraftRepo{bots: []store.Bot{{ID: uuid.New(), OwnerID: owner, Status: store.BotPaused}}}, nil); err != nil && !errors.Is(err, store.ErrBotUnsupported) {
		// The deliberately minimal repository has no publication/retention
		// adapter. Execution would panic on its missing runtime methods.
		t.Fatal(err)
	}
}

type meteredFailureRuntime struct{ bot.Runtime }

func (meteredFailureRuntime) Start(c context.Context, q bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	_ = q.Budget.ReserveTool(c, "controlled")
	return bot.RunResult{}, store.ErrBotUnsupported
}
func (r meteredFailureRuntime) Resume(c context.Context, q bot.RunRequest, s bot.EventSink) (bot.RunResult, error) {
	return r.Start(c, q, s)
}
func TestBotMeteredRuntimeRetainsUsageOnFailure(t *testing.T) {
	for _, resume := range []bool{false, true} {
		meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		runtime := botMeteredRuntime{Runtime: meteredFailureRuntime{}}
		call := runtime.Start
		if resume {
			call = runtime.Resume
		}
		result, err := call(context.Background(), bot.RunRequest{Budget: meter}, nil)
		if !errors.Is(err, store.ErrBotUnsupported) || result.Usage.ToolCalls != 1 {
			t.Fatalf("resume=%v result=%+v err=%v", resume, result, err)
		}
	}
}
