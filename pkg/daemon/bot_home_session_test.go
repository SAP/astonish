package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type sessionRecoveryRepo struct {
	store.BotStore
	bot store.Bot
}

func (r sessionRecoveryRepo) Get(context.Context, store.BotID) (*store.Bot, error) {
	b := r.bot
	return &b, nil
}

type sessionRecoveryBackend struct {
	sandbox.Backend
	starts   int
	prepares int
}

func (*sessionRecoveryBackend) BindBotHomeSession(*sandbox.BotHomeBinding, string) error { return nil }
func (b *sessionRecoveryBackend) StartSession(context.Context, string) error {
	b.starts++
	return nil
}
func (b *sessionRecoveryBackend) PrepareBotHomeSession(context.Context, *sandbox.BotHomeBinding, string) error {
	b.prepares++
	return nil
}

func TestBotHomeSessionRecoveryUsesPreparedRunningFastPath(t *testing.T) {
	backend := &sessionRecoveryBackend{}
	binding := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: uuid.NewString(), BotID: uuid.NewString()}, Backend: sandbox.BackendKind("docker"), Ref: "home"}
	if err := prepareBoundBotHomeSession(t.Context(), backend, binding, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if backend.prepares != 1 || backend.starts != 0 {
		t.Fatalf("running retained session used slow restart path: prepares=%d starts=%d", backend.prepares, backend.starts)
	}
}

func TestBotHomeSessionRecoveryPreflight(t *testing.T) {
	backend := &sessionRecoveryBackend{}
	w := &BotWorker{config: BotWorkerConfig{Sandbox: backend}}
	b := store.Bot{ID: uuid.New(), OwnerID: uuid.New(), HomeSessionID: uuid.New()}
	if err := w.prepareExistingBotHomeSession(context.Background(), sessionRecoveryRepo{bot: b}, b.ID); err != nil {
		t.Fatal(err)
	}
	b.Home.ID = uuid.New()
	if err := w.prepareExistingBotHomeSession(context.Background(), sessionRecoveryRepo{bot: b}, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("missing principal: %v", err)
	}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.prepareExistingBotHomeSession(ctx, sessionRecoveryRepo{bot: b}, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("wrong owner: %v", err)
	}
	if backend.starts != 0 {
		t.Fatal("unauthorized or title-only preflight started compute")
	}
}
