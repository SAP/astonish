package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

func TestBotWorkerResumedBudgetExhausted(t *testing.T) {
	cap := int64(4)
	started := time.Now().Add(-time.Second)
	in := store.BotRunInput{Version: store.DefaultBotVersion(), Run: store.BotRun{StartedAt: &started, Lease: store.BotLease{Fence: 1}, Checkpoint: []byte(`{}`), Usage: store.BotUsage{ToolCalls: cap, RuntimeNanos: int64(time.Second)}}}
	in.Version.Budget.MaxToolCalls = &cap
	b, err := botRunBudget(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Check(context.Background()); !errors.Is(err, bot.ErrBudgetExceeded) {
		t.Fatalf("resume reset tool cap: %v", err)
	}
	in.Run.Usage = store.BotUsage{RuntimeNanos: int64(in.Version.Budget.MaxRuntimeSeconds) * int64(time.Second)}
	b, err = botRunBudget(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Check(context.Background()); !errors.Is(err, bot.ErrBudgetExceeded) {
		t.Fatalf("resume reset runtime cap: %v", err)
	}
}

func TestBotWorkerPollRecoversCrashAfterDatabaseReopen(t *testing.T) {
	testBotWorkerRecoveryAfterReopen(t, false)
}

func TestBotWorkerPauseRecoveryAfterDatabaseReopen(t *testing.T) {
	testBotWorkerRecoveryAfterReopen(t, true)
}

// Isolate polling of the crashed writer; admission of the queued conversation
// below uses the real scoped store and fencing transaction.
type interruptedRecoveryPollStore struct {
	store.BotStore
	store.BotPublicationStore
}

func (s interruptedRecoveryPollStore) ListRuns(ctx context.Context, id store.BotID, page store.BotPage) ([]store.BotRun, error) {
	runs, err := s.BotStore.ListRuns(ctx, id, page)
	var interrupted []store.BotRun
	for _, run := range runs {
		if run.Status == store.RunInterrupted {
			interrupted = append(interrupted, run)
		}
	}
	return interrupted, err
}

func (s interruptedRecoveryPollStore) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	return s.BotStore.(store.BotCleanupStore).ConfirmCleanup(ctx, q)
}

func testBotWorkerRecoveryAfterReopen(t *testing.T, paused bool) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
			_, backend, err := entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = backend.Close() }()
			org := &store.Organization{ID: uuid.NewString(), Slug: "recovery", Name: "Recovery", Status: "active"}
			if err := backend.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			data, err := backend.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := data.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := backend.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := backend.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := data.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			w := &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "replacement"}}
			auth, _, err := w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo := data.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = data.ForUser(owner.String()).Bots()
			}
			v := store.DefaultBotVersion()
			v.Charter = "Recover without repeating uncertain work"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Recovery", OwnerID: owner, Version: v, IdempotencyKey: "recovery"})
			if err != nil {
				t.Fatal(err)
			}
			// Controlled readiness is test-only; all lifecycle and recovery writes use
			// the real owner-authorized repository.
			backend.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "home", Revision: 1}
			if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "test"})
			if err != nil {
				t.Fatal(err)
			}
			r, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Do not replay", IdempotencyKey: "crash"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			workerCtx := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: r.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
			lease, err := repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: r.ID, WorkerID: "crashed", Now: now, ExpiresAt: now.Add(500 * time.Millisecond)})
			if err != nil {
				t.Fatal(err)
			}
			var conversation *store.BotRun
			if paused {
				b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "pause during execution"})
				if err != nil {
					t.Fatal(err)
				}
				conversation, err = repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentConversation, Trigger: store.TriggerManual, Instructions: "Reply without tools", IdempotencyKey: "paused-conversation"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			_, backend, err = entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			w = &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "replacement"}}
			if delay := time.Until(lease.ExpiresAt); delay > 0 {
				time.Sleep(delay + 10*time.Millisecond)
			}
			if paused {
				auth, data, err = w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
				if err != nil {
					t.Fatal(err)
				}
				repo = data.ForTeam(team.Slug).Bots()
				if scope == store.BotScopePersonal {
					repo = data.ForUser(owner.String()).Bots()
				}
				physical := &cancellationReceiptBackend{err: errors.New("physical writer still exists")}
				w.config.Sandbox = physical
				conversationCtx := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: conversation.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				claim := store.BotClaimRequest{BotID: b.ID, RunID: conversation.ID, WorkerID: "replacement", ExpiresAt: time.Now().Add(time.Minute)}
				pollRepo := interruptedRecoveryPollStore{BotStore: repo, BotPublicationStore: repo.(store.BotPublicationStore)}
				if err = w.pollStore(auth, pollRepo, nil); err == nil {
					t.Fatal("missing physical evidence not reported")
				}
				if _, err = repo.ClaimRun(conversationCtx, claim); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("claim before physical receipt: %v", err)
				}
				physical.err = nil
				if err = w.pollStore(auth, pollRepo, nil); err != nil {
					t.Fatal(err)
				}
				if physical.session != r.SessionID.String() {
					t.Fatal("restart did not verify exact interrupted physical session")
				}
				newLease, err := repo.ClaimRun(conversationCtx, claim)
				if err != nil {
					t.Fatalf("paused conversation blocked after verified restart recovery: %v", err)
				}
				if newLease.Fence <= lease.Fence {
					t.Fatal("fence not advanced")
				}
				if err = repo.CommitRun(workerCtx, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded}); err == nil {
					t.Fatal("stale worker committed")
				}
				if err = repo.CommitRun(conversationCtx, store.BotRunCommit{Lease: newLease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "No tools needed"}}); err != nil {
					t.Fatal(err)
				}
				final, err := repo.Get(auth, b.ID)
				if err != nil || final.Status != store.BotPaused || final.Home != home {
					t.Fatalf("changed paused home: %+v %v", final, err)
				}
				return
			}
			if err := w.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			auth, data, err = w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo = data.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = data.ForUser(owner.String()).Bots()
			}
			saved, err := repo.GetRun(auth, b.ID, r.ID)
			if err != nil || saved.Status != store.RunInterrupted || len(saved.Report.Unverified) == 0 {
				t.Fatalf("crashed attempt not recovered: %+v %v", saved, err)
			}
			if err := repo.CommitRun(auth, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded}); err == nil {
				t.Fatal("late crashed worker committed")
			}
			if err := w.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err = repo.GetRun(auth, b.ID, r.ID)
			if err != nil || saved.Status != store.RunInterrupted || saved.Lease.Fence != lease.Fence {
				t.Fatalf("recovered run retried: %+v %v", saved, err)
			}
			next, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Explicit reviewed work", IdempotencyKey: "next"})
			if err != nil {
				t.Fatal(err)
			}
			now = time.Now()
			workerCtx = execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: next.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
			if _, err := repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: next.ID, WorkerID: "replacement", Now: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
				t.Fatalf("expired active-run lock retained: %v", err)
			}
		})
	}
}
