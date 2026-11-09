package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type homeManagerStore struct {
	store.BotStore
	b store.Bot
	r store.BotRun
}

func (s homeManagerStore) Get(context.Context, store.BotID) (*store.Bot, error) { return &s.b, nil }
func (s homeManagerStore) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	return &s.r, nil
}

type homeManagerBackend struct{ binding sandbox.BotHomeBinding }

func (b homeManagerBackend) EnsureBotHome(context.Context, sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return &b.binding, nil
}

type homeManagerPool struct {
	sandbox.ToolNodePool
	got *sandbox.BotHomeBinding
}

func (p *homeManagerPool) SetBotHome(_ string, b *sandbox.BotHomeBinding) error {
	p.got = b
	return nil
}
func TestBotHomeManagerExactBindingAndFence(t *testing.T) {
	owner, id, runID := uuid.New(), uuid.New(), uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	home := store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopeTeam, Backend: "docker", StorageKey: "opaque", Revision: 1}
	lease := store.BotLease{BotID: id, RunID: runID, Fence: 2, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}
	repo := homeManagerStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, Home: home}, r: store.BotRun{ID: runID, BotID: id, SessionID: uuid.New(), Status: store.RunRunning, Lease: lease}}
	pool := &homeManagerPool{}
	backend := homeManagerBackend{binding: sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: owner.String(), BotID: id.String()}, Backend: sandbox.BackendKindDocker, Ref: "opaque"}}
	manager := &BotHomeManager{Backend: backend, Pool: pool, Bots: repo}
	actual, err := manager.Ensure(ctx, bot.HomeRequest{BotID: id, Scope: store.BotScopeTeam})
	if err != nil || actual != home {
		t.Fatalf("ensure %v %v", actual, err)
	}
	binding := bot.HomeRunBinding{Home: home, PhysicalSessionID: repo.r.SessionID, Lease: lease}
	if err := manager.BindRun(ctx, binding); err != nil || pool.got == nil {
		t.Fatalf("bind %v", err)
	}
	binding.Lease.Fence--
	if err := manager.BindRun(ctx, binding); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale %v", err)
	}
	backend.binding.Ref = "different"
	manager.Backend = backend
	if _, err := manager.Ensure(ctx, bot.HomeRequest{BotID: id, Scope: store.BotScopeTeam}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("home changed %v", err)
	}
	if _, err := manager.ReadArtifact(ctx, home, store.BotArtifact{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, home); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
}
