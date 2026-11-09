package entstore

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotHomeBindingPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner := uuid.New()
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			callerActive := true
			auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) {
				if !callerActive {
					return "", store.ErrBotForbidden
				}
				return "member", nil
			}}
			path := filepath.Join(t.TempDir(), "binding.db")
			if scope == store.BotScopePersonal {
				auth.teamSlug = ""
			} // same construction as tenant router
			var closeDB func() error
			var repo *botRepository
			var public store.BotStore
			open := func() {
				db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
				if err != nil {
					t.Fatal(err)
				}
				drv := entsql.OpenDB(dialect.SQLite, db)
				if scope == store.BotScopeTeam {
					c := teament.NewClient(teament.Driver(drv))
					if e := c.Schema.Create(ctx); e != nil {
						t.Fatal(e)
					}
					s := &teamBotStore{client: c, auth: auth}
					public = s
					repo = s.repository()
					closeDB = c.Close
				} else {
					c := personalent.NewClient(personalent.Driver(drv))
					if e := c.Schema.Create(ctx); e != nil {
						t.Fatal(e)
					}
					s := &personalBotStore{client: c, auth: auth}
					public = s
					repo = s.repository()
					closeDB = c.Close
				}
			}
			open()
			defer func() { _ = closeDB() }()
			v := store.DefaultBotVersion()
			v.Charter = "Observe only"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Registry", OwnerID: owner, Version: v, IdempotencyKey: "binding"})
			if err != nil {
				t.Fatal(err)
			}
			q := BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "fixture", StorageKey: "opaque-home", Revision: 1}}
			internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceCode, ClientID: "registry", OrgSlug: "org", TeamSlug: "team", Scopes: []string{BotHomeRegistrationScope}, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			verify := func(_ context.Context, got BotHomeRegistration) error {
				calls++
				if got != q {
					t.Fatalf("inexact attestation: %+v", got)
				}
				return nil
			}
			if err := RegisterBotHomeBinding(ctx, public, q, verify); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("user forged binding: %v", err)
			}
			if err := RegisterBotHomeBinding(internal, public, q, nil); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("missing verifier: %v", err)
			}
			bad := q
			bad.Home.Scope = store.BotScopeTeam
			if scope == store.BotScopeTeam {
				bad.Home.Scope = store.BotScopePersonal
			}
			if err := RegisterBotHomeBinding(internal, public, bad, verify); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("cross scope: %v", err)
			}
			wrongPrincipal, _ := execution.PrincipalFromContext(internal)
			wrongPrincipal.OrgSlug = "other"
			wrongCtx, err := execution.WithPrincipal(context.Background(), wrongPrincipal)
			if err != nil {
				t.Fatal(err)
			}
			if err := RegisterBotHomeBinding(wrongCtx, public, q, verify); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("cross org: %v", err)
			}
			bad = q
			bad.Home.BotID = uuid.New()
			if err := RegisterBotHomeBinding(internal, public, bad, verify); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("foreign binding: %v", err)
			}
			bad = q
			bad.ExpectedRevision++
			if err := RegisterBotHomeBinding(internal, public, bad, verify); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale revision: %v", err)
			}
			if calls != 0 {
				t.Fatal("invalid request reached callback")
			}
			sentinel := errors.New("not registered")
			if err := RegisterBotHomeBinding(internal, public, q, func(context.Context, BotHomeRegistration) error { return sentinel }); !errors.Is(err, sentinel) {
				t.Fatalf("attestation failure: %v", err)
			}
			// Revocation after preflight/attestation must fail inside registration.
			for _, revoked := range []string{"caller", "owner", "service-requester", "missing-validator"} {
				t.Run("transaction-"+revoked, func(t *testing.T) {
					ownerActive := true
					a := BotHomeRegistrationAuthorization{RequestContext: ctx, Validate: func(c context.Context, got store.Bot) error {
						p, ok := execution.PrincipalFromContext(c)
						if !ok || p.Subject != owner.String() || got.OwnerID != owner {
							t.Fatal("lost original identity")
						}
						if !ownerActive {
							return store.ErrBotForbidden
						}
						return nil
					}}
					if revoked == "service-requester" {
						a.RequestContext = internal
					}
					if revoked == "missing-validator" {
						a.Validate = nil
					}
					err := RegisterBotHomeBinding(internal, public, q, func(context.Context, BotHomeRegistration) error {
						if revoked == "caller" {
							callerActive = false
						}
						if revoked == "owner" {
							ownerActive = false
						}
						return nil
					}, a)
					callerActive = true
					if !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("revocation accepted: %v", err)
					}
					got, err := public.Get(ctx, b.ID)
					if err != nil || got.Home.ID != uuid.Nil || got.Revision != b.Revision {
						t.Fatalf("rejected registration mutated bot: %+v %v", got, err)
					}
				})
			}
			// A callback racing a revision change must never commit its old binding.
			raceBot, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Race", OwnerID: owner, Version: v, IdempotencyKey: "race"})
			if err != nil {
				t.Fatal(err)
			}
			raceQ := q
			raceQ.BotID = raceBot.ID
			raceQ.ExpectedRevision = raceBot.Revision
			raceQ.Home.BotID = raceBot.ID
			raceQ.Home.ID = uuid.New()
			err = RegisterBotHomeBinding(internal, public, raceQ, func(context.Context, BotHomeRegistration) error {
				next, err := public.GetVersion(ctx, raceBot.ID, raceBot.Version)
				if err != nil {
					return err
				}
				next.ChangeReason = "reviewed race"
				_, err = public.ApplyVersion(ctx, store.BotVersionRequest{BotID: raceBot.ID, ExpectedRevision: raceBot.Revision, Version: *next})
				return err
			})
			if !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("revision race accepted: %v", err)
			}
			raceSaved, err := public.Get(ctx, raceBot.ID)
			if err != nil || raceSaved.Home.ID != uuid.Nil {
				t.Fatalf("race left binding: %+v %v", raceSaved, err)
			}
			if err := RegisterBotHomeBinding(internal, public, q, verify); err != nil {
				t.Fatal(err)
			}
			if err := RegisterBotHomeBinding(internal, public, q, verify); err != nil {
				t.Fatalf("retry: %v", err)
			}
			// Replay must not bypass live authorization merely because storage exists.
			err = RegisterBotHomeBinding(internal, public, q, verify, BotHomeRegistrationAuthorization{RequestContext: ctx, Validate: func(context.Context, store.Bot) error { return store.ErrBotForbidden }})
			if !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked replay accepted: %v", err)
			}
			bad = q
			bad.Home.ID = uuid.New()
			if err := RegisterBotHomeBinding(internal, public, bad, verify); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("replacement: %v", err)
			}
			if scope == store.BotScopeTeam {
				d := repo.db.(teamBotDB)
				ss := &teamSandboxSessionStore{client: d.client}
				ordinary := &store.SandboxSession{SessionID: uuid.NewString(), ChatSessionID: uuid.NewString(), Backend: "fixture", State: store.SandboxSessionStateEvicted}
				if err := ss.Put(ctx, ordinary); err != nil {
					t.Fatal(err)
				}
				before, _ := ss.Get(ctx, ordinary.SessionID)
				row := &store.SandboxSession{SessionID: uuid.NewString(), ChatSessionID: b.HomeSessionID.String(), Backend: "fixture", State: store.SandboxSessionStateEvicted, BotHomeBinding: &q.Home}
				if err := ss.Put(ctx, row); err != nil {
					t.Fatal(err)
				}
				if err := ss.Put(ctx, row); err != nil {
					t.Fatal(err)
				}
				if err := closeDB(); err != nil {
					t.Fatal(err)
				}
				open()
				ss = &teamSandboxSessionStore{client: repo.db.(teamBotDB).client}
				got, err := ss.Get(ctx, row.SessionID)
				if err != nil || got.BotHomeBinding == nil || *got.BotHomeBinding != q.Home {
					t.Fatalf("registry reopen: %+v %v", got, err)
				}
				after, _ := ss.Get(ctx, ordinary.SessionID)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("ordinary registry changed")
				}
			} else {
				if err := closeDB(); err != nil {
					t.Fatal(err)
				}
				open()
			}
			got, err := public.Get(ctx, b.ID)
			if err != nil || got.Home != q.Home || got.Revision != b.Revision+1 || got.Status != store.BotDraft {
				t.Fatalf("binding reopen: %+v %v", got, err)
			}
			runs, err := public.ListRuns(ctx, b.ID, store.BotPage{})
			if err != nil || len(runs) != 0 {
				t.Fatalf("registration started run: %v %v", runs, err)
			}
		})
	}
}
