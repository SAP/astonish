package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Exercise the actual conditional Ent updates, not a mocked persistence layer.
func TestBotUnfinishedDatabaseCASAndTombstone(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner := uuid.New()
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			var db intakeDB
			var assertEmpty func()
			var assertCleared func(uuid.UUID)
			if scope == store.BotScopePersonal {
				_, c := setupPersonalStore(t)
				db = personalIntakeDB{c}
				assertEmpty = func() {
					if n, e := c.Bot.Query().Count(ctx); e != nil || n != 0 {
						t.Fatalf("Bot side effect: %d %v", n, e)
					}
					if n, e := c.Session.Query().Count(ctx); e != nil || n != 0 {
						t.Fatalf("session side effect: %d %v", n, e)
					}
					if n, e := c.ScheduledJob.Query().Count(ctx); e != nil || n != 0 {
						t.Fatalf("job side effect: %d %v", n, e)
					}
				}
				assertCleared = func(id uuid.UUID) {
					row, e := c.BotIntake.Get(ctx, id)
					if e != nil {
						t.Fatal(e)
					}
					if len(row.Content) != 0 || !row.Discarded {
						t.Fatalf("content retained: %+v", row)
					}
				}
			} else {
				c := newTeamClient(t)
				db = teamIntakeDB{c}
				assertEmpty = func() {
					if n, e := c.Bot.Query().Count(ctx); e != nil || n != 0 {
						t.Fatalf("Bot side effect: %d %v", n, e)
					}
					if n, e := c.Session.Query().Count(ctx); e != nil || n != 0 {
						t.Fatalf("session side effect: %d %v", n, e)
					}
					if n, e := c.ScheduledJob.Query().Count(ctx); e != nil || n != 0 {
						t.Fatalf("job side effect: %d %v", n, e)
					}
				}
				assertCleared = func(id uuid.UUID) {
					row, e := c.BotIntake.Get(ctx, id)
					if e != nil {
						t.Fatal(e)
					}
					if len(row.Content) != 0 || !row.Discarded {
						t.Fatalf("content retained: %+v", row)
					}
				}
			}
			role := "admin"
			repo := &intakeRepository{auth: botRepositoryAuth{scope: scope, orgSlug: "org", teamSlug: "team", userID: owner.String(), check: func(context.Context, string) (string, error) { return role, nil }}, db: db}
			q := store.BotUnfinishedPut{IdempotencyKey: "create", Content: store.BotUnfinishedContent{Mode: store.BotIntakeBlank, Name: "private"}}
			id := uuid.New()
			// Sequential exact replay complements the concurrent routed HTTP fixture.
			for range 2 {
				if _, err := repo.Put(ctx, id, q); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := db.read(ctx, id, owner)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Revision != 1 {
				t.Fatalf("duplicate insert: %d", saved.Revision)
			}
			if _, err := db.read(ctx, id, uuid.New()); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("crossowner read: %v", err)
			}
			// Start both transitions from precisely the same snapshot and apply SQL CAS.
			q.ExpectedRevision = 1
			q.IdempotencyKey = "save"
			q.Content.Name = "new"
			replacement, err := saved.Replace(owner, scope, q, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			tombstone, err := saved.Discard(owner, scope, store.BotUnfinishedDelete{ExpectedRevision: 1, IdempotencyKey: "discard"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			// Both candidates use the same database revision, regardless of ordering.
			// The HTTP fixture separately exercises truly concurrent requests on
			// production-routed connections (these older unit fixtures have no busy timeout).
			won, lost := 0, 0
			for _, next := range []*store.BotUnfinishedIntake{tombstone, replacement} {
				e := db.cas(ctx, next, 1)
				if e == nil {
					won++
				} else if errors.Is(e, store.ErrBotConflict) {
					lost++
				} else {
					t.Fatal(e)
				}
			}
			if won != 1 || lost != 1 {
				t.Fatalf("CAS winners=%d conflicts=%d", won, lost)
			}
			current, err := db.read(ctx, id, owner)
			if err != nil {
				t.Fatal(err)
			}
			if !current.Discarded {
				if err := repo.Delete(ctx, id, store.BotUnfinishedDelete{ExpectedRevision: 2, IdempotencyKey: "final-discard"}); err != nil {
					t.Fatal(err)
				}
			}
			assertCleared(id)
			if err := db.cas(ctx, replacement, 1); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("late save resurrected: %v", err)
			}
			q.ExpectedRevision = 0
			if _, err := repo.Put(ctx, id, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("recreate tombstone: %v", err)
			}
			assertEmpty()
			// Cached repository must call membership validation again on every operation.
			role = "revoked"
			if _, err := repo.Get(ctx, id); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked read: %v", err)
			}
			if _, err := repo.Put(ctx, uuid.New(), q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked write: %v", err)
			}
			if err := repo.Delete(ctx, id, store.BotUnfinishedDelete{ExpectedRevision: 2, IdempotencyKey: "final-discard"}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked delete: %v", err)
			}
		})
	}
}
