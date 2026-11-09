package launcher

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type offlineHomeBackend struct {
	ensures, reads, deletes int
	data                    []byte
	last                    *sandbox.BotHomeBinding
}

func (b *offlineHomeBackend) EnsureBotHome(context.Context, sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	b.ensures++
	return nil, errors.New("must not provision")
}
func (b *offlineHomeBackend) ReadBotHomeArtifact(_ context.Context, home *sandbox.BotHomeBinding, _ string, max int64) ([]byte, error) {
	b.reads++
	b.last = home
	if max != sandbox.MaxBotHomeArtifactBytes {
		panic("unbounded read")
	}
	return b.data, nil
}
func (b *offlineHomeBackend) DeleteBotHome(_ context.Context, home *sandbox.BotHomeBinding) error {
	b.deletes++
	b.last = home
	return nil
}

func TestBotHomeManagerOfflineAuthorizationAndDigest(t *testing.T) {
	owner, id := uuid.New(), uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	home := store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopeTeam, Backend: "docker", StorageKey: "opaque", Revision: 1}
	repo := homeManagerStore{b: store.Bot{ID: id, OwnerID: owner, Scope: home.Scope, Home: home}}
	backend := &offlineHomeBackend{data: []byte("old artifact")}
	manager := &BotHomeManager{Backend: backend, Bots: repo} // deliberately no pool or physical run
	artifact := store.BotArtifact{HomeID: home.ID, RelativePath: "results/report", SHA256: fmt.Sprintf("%x", sha256.Sum256(backend.data))}
	r, err := manager.ReadArtifact(ctx, home, artifact)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	if string(data) != "old artifact" || backend.ensures != 0 || backend.last.Scope.UserID != owner.String() {
		t.Fatal("offline read recreated or mis-scoped home")
	}
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		home     store.BotHomeBinding
		artifact store.BotArtifact
	}{
		{"anonymous", context.Background(), home, artifact},
		{"foreign-home", ctx, func() store.BotHomeBinding { h := home; h.ID = uuid.New(); return h }(), artifact},
		{"foreign-artifact", ctx, home, store.BotArtifact{HomeID: uuid.New(), RelativePath: "results/report"}},
		{"traversal", ctx, home, store.BotArtifact{HomeID: home.ID, RelativePath: "../escape"}},
		{"malformed-hash", ctx, home, store.BotArtifact{HomeID: home.ID, RelativePath: "results/report", SHA256: "bad"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := backend.reads
			if _, err := manager.ReadArtifact(tc.ctx, tc.home, tc.artifact); err == nil {
				t.Fatal("unauthorized read")
			}
			if backend.reads != before {
				t.Fatal("dispatched before authorization")
			}
		})
	}
	artifact.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("changed")))
	if _, err := manager.ReadArtifact(ctx, home, artifact); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("digest mismatch: %v", err)
	}
	if err := manager.Delete(ctx, home); !errors.Is(err, store.ErrBotForbidden) || backend.deletes != 0 {
		t.Fatalf("unauthorized delete: %v", err)
	}
	manager.AuthorizeDelete = func(context.Context, store.BotHomeBinding) (*sandbox.BotHomeBinding, error) {
		return nil, store.ErrBotForbidden
	}
	if err := manager.Delete(ctx, home); !errors.Is(err, store.ErrBotForbidden) || backend.deletes != 0 {
		t.Fatal("authorization failure ignored")
	}
	manager.AuthorizeDelete = func(_ context.Context, h store.BotHomeBinding) (*sandbox.BotHomeBinding, error) {
		if h != home {
			t.Fatal("authorizing other home")
		}
		return &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: owner.String(), BotID: id.String()}, Backend: sandbox.BackendKindDocker, Ref: home.StorageKey}, nil
	}
	if err := manager.Delete(ctx, home); err != nil || backend.deletes != 1 || backend.ensures != 0 {
		t.Fatalf("authorized deletion: %v", err)
	}
}
