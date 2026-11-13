package entstore

import (
	"context"
	"encoding/json"
	"errors"
	adksession "google.golang.org/adk/session"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/ent/team/botapproval"
	"github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/ent/team/scheduledjob"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotSessionMetadataImmutableLinkage(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			var ss store.SessionStore
			switch d := repo.db.(type) {
			case teamBotDB:
				ss = &teamSessionStore{client: d.client}
			case personalBotDB:
				ss = &personalSessionStore{client: d.client}
			}
			for _, id := range []string{b.HomeSessionID.String(), run.SessionID.String()} {
				meta, err := ss.GetSessionMeta(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if meta.BotID != b.ID.String() || meta.Retention != store.SessionRetentionBot || !meta.HasBotLinkage() {
					t.Fatalf("missing linkage: %+v", meta)
				}
				if id == run.SessionID.String() && (meta.BotRunID != run.ID.String() || meta.Purpose != store.SessionPurposeBotRun) {
					t.Fatalf("run linkage: %+v", meta)
				}
				for _, change := range []func(*store.SessionMeta){func(m *store.SessionMeta) { m.BotID = "" }, func(m *store.SessionMeta) { m.Retention = "" }, func(m *store.SessionMeta) { m.UserID = uuid.NewString() }, func(m *store.SessionMeta) { m.ParentID = "other" }} {
					if err := ss.UpdateSessionMeta(ctx, id, change); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("linkage mutation: %v", err)
					}
				}
				if err := ss.UpdateSessionMeta(ctx, id, func(m *store.SessionMeta) { m.Title = "Reviewed title" }); err != nil {
					t.Fatal(err)
				}
			}
			if err := ss.AddSessionMeta(ctx, store.SessionMeta{ID: uuid.NewString(), BotID: b.ID.String()}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("generic attach: %v", err)
			}
			ordinary := uuid.NewString()
			if err := ss.AddSessionMeta(ctx, store.SessionMeta{ID: ordinary, UserID: b.OwnerID.String()}); err != nil {
				t.Fatal(err)
			}
			meta, err := ss.GetSessionMeta(ctx, ordinary)
			if err != nil || meta.HasBotLinkage() || meta.Purpose != "" || meta.Retention != "" {
				t.Fatalf("ordinary metadata changed: %+v %v", meta, err)
			}
		})
	}
}

func TestBotSessionsExcludedFromChatLists(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			var sessions store.SessionStore
			switch d := repo.db.(type) {
			case teamBotDB:
				sessions = &teamSessionStore{client: d.client}
			case personalBotDB:
				sessions = &personalSessionStore{client: d.client}
			}
			ordinary, err := sessions.Create(ctx, &adksession.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			listed, err := sessions.List(ctx, &adksession.ListRequest{AppName: "astonish", UserID: b.OwnerID.String()})
			if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].ID() != ordinary.Session.ID() {
				t.Fatalf("Bot in Chat list: %+v %v", listed, err)
			}
			metas, err := sessions.ListSessionMetas(ctx, "astonish", b.OwnerID.String())
			if err != nil || len(metas) != 1 || metas[0].ID != ordinary.Session.ID() {
				t.Fatalf("Bot in Chat metadata list: %+v %v", metas, err)
			}
			ids, err := sessions.(store.SessionIDEnumerator).AllSessionIDsStrict(ctx)
			if err != nil || !ids[b.HomeSessionID.String()] || !ids[run.SessionID.String()] {
				t.Fatalf("Bot missing from retention inventory: %v %v", ids, err)
			}
		})
	}
}

func TestBotTenantRoutingAndLiveMembership(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, s, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "bots-org", Name: "Bots org", Status: "active"}
	if err := s.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := s.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "bots-team", Name: "Bots team", SchemaName: "team_bots-team"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, member := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, member} {
		if err := s.Users().Create(ctx, &store.User{ID: id.String(), Email: id.String() + "@example.test", DisplayName: "Bot tester", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := s.Organizations().AddMember(ctx, id.String(), org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id.String(), TeamID: team.ID, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	principal := func(id uuid.UUID) context.Context {
		c, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ownerCtx, memberCtx := principal(owner), principal(member)
	teamStore := ods.ForTeam(team.Slug)
	personalStore := ods.ForUser(owner.String())
	if teamStore == nil || personalStore == nil {
		t.Fatal("missing scoped stores")
	}
	shared, private := teamStore.Bots(), personalStore.Bots()
	v := store.DefaultBotVersion()
	v.Charter = "Observe only"
	req := store.BotCreateRequest{Name: "Shared", OwnerID: owner, Version: v, IdempotencyKey: "create"}
	b, err := shared.CreateDraft(ownerCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Name = "Private"
	p, err := private.CreateDraft(ownerCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Get(memberCtx, b.ID); err != nil {
		t.Fatalf("team member read: %v", err)
	}
	if _, err := private.Get(memberCtx, p.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("private read leaked: %v", err)
	}
	if _, err := shared.Get(ownerCtx, p.ID); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("cross-scope lookup: %v", err)
	}
	if _, err := shared.BeginDelete(memberCtx, store.BotLifecycleRequest{BotID: b.ID, ActorID: member, ExpectedRevision: b.Revision, Reason: "Not owner"}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("member edit: %v", err)
	}
	// Exercise the real middleware over HTTP with a controlled authenticated
	// principal. This is scoped service wiring, not production Bot API acceptance.
	handler := TenantMiddleware(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc := store.FromRequest(r)
		if r.URL.Path == "/personal" {
			if svc == nil || svc.PersonalBots == nil || svc.Bots != nil {
				http.Error(w, "incorrect personal-only scope", 500)
				return
			}
			if _, err := svc.PersonalBots.Get(r.Context(), p.ID); err != nil {
				http.Error(w, err.Error(), 403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/team-only" {
			if svc == nil || svc.Bots == nil || svc.PersonalBots != nil {
				http.Error(w, "private scope inherited", 500)
				return
			}
			if _, err := svc.Bots.Get(r.Context(), b.ID); !errors.Is(err, store.ErrBotForbidden) {
				http.Error(w, "missing principal accepted", 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if svc == nil || svc.Bots == nil || svc.PersonalBots == nil || svc.Bots == svc.PersonalBots {
			http.Error(w, "missing scoped Bots", 500)
			return
		}
		if _, err := svc.Bots.Get(r.Context(), b.ID); err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		if _, err := svc.PersonalBots.Get(r.Context(), p.ID); err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := store.WithServices(ownerCtx, base)
		tenant := &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: owner.String()}
		if r.URL.Path == "/personal" {
			tenant.TeamSlug = ""
		}
		if r.URL.Path == "/team-only" {
			tenant.UserID = ""
			c = store.WithServices(ctx, base)
		}
		c = store.WithTenantContext(c, tenant)
		handler.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("middleware status %d", resp.StatusCode)
	}
	for _, path := range []string{"/personal", "/team-only"} {
		resp, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatalf("%s middleware status %d", path, resp.StatusCode)
		}
	}
	if base.Bots != nil || base.PersonalBots != nil {
		t.Fatal("middleware mutated base services")
	}
	selected := store.WithServices(ctx, &store.Services{Bots: shared})
	if store.BotStoreFromContext(selected, store.BotScopeTeam) != shared || store.BotStoreFromContext(selected, store.BotScopePersonal) != nil || store.BotStoreFromContext(selected, store.BotScope("unknown")) != nil || store.BotStoreFromContext(context.Background(), store.BotScopeTeam) != nil {
		t.Fatal("Bot scope fallback")
	}
	account, err := s.Users().GetByID(ctx, owner.String())
	if err != nil {
		t.Fatal(err)
	}
	account.Status = "suspended"
	if err := s.Users().Update(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := private.Get(ownerCtx, p.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("suspended account read: %v", err)
	}
	account.Status = "active"
	if err := s.Users().Update(ctx, account); err != nil {
		t.Fatal(err)
	}
	org.Status = "suspended"
	if err := s.Organizations().Update(ctx, org); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Get(ownerCtx, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("suspended org read: %v", err)
	}
	org.Status = "active"
	if err := s.Organizations().Update(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().RemoveMember(ctx, member.String(), team.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Get(memberCtx, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("cached team membership: %v", err)
	}
	if err := s.Organizations().RemoveMember(ctx, owner.String(), org.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Get(ownerCtx, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revoked org shared read: %v", err)
	}
	if _, err := private.Get(ownerCtx, p.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revoked org private read: %v", err)
	}
}

var _ store.BotStore = (*teamBotStore)(nil)
var _ store.BotStore = (*personalBotStore)(nil)

type botPurgeInventoryRace struct {
	botDraftDB
	afterList func()
}

func (d botPurgeInventoryRace) ListPurgeable(ctx context.Context, cutoff time.Time, limit int) ([]botStoredRecord, error) {
	rows, err := d.botDraftDB.ListPurgeable(ctx, cutoff, limit)
	if err == nil {
		d.afterList()
	}
	return rows, err
}
func TestBotRetentionRechecksCleanupAfterInventory(t *testing.T) {
	repo, ctx, _, run, _, now := botRunFixture(t, store.BotScopeTeam)
	db := repo.db.(teamBotDB)
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.BeginDelete(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Delete"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.client.Bot.UpdateOneID(b.ID).SetCleanupComplete(true).ClearActiveRunID().ClearLeaseExpiresAt().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	*now = *deleted.RestoreUntil
	repo.db = botPurgeInventoryRace{botDraftDB: db, afterList: func() {
		if err := db.client.Bot.UpdateOneID(b.ID).SetCleanupComplete(false).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}}
	if n, err := repo.PurgeDue(ctx, *now, 10); !errors.Is(err, store.ErrBotConflict) || n != 0 {
		t.Fatalf("trusted stale cleanup: %d %v", n, err)
	}
	repo.db = db
	if _, err := repo.Get(ctx, b.ID); err != nil {
		t.Fatalf("bot lost: %v", err)
	}
	if _, err := db.client.Session.Get(ctx, b.HomeSessionID.String()); err != nil {
		t.Fatalf("home lost: %v", err)
	}
}

func TestBotRetentionRollbackAndCleanupIntent(t *testing.T) {
	repo, ctx, _, run, _, now := botRunFixture(t, store.BotScopeTeam)
	db := repo.db.(teamBotDB)
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	g, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Keep evidence", CompletionCriteria: "Owner confirms"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveTrackedItem(ctx, b.ID, store.BotTrackedItem{ID: uuid.New(), GoalID: g.ID, Title: "Receipt", Status: store.BotTrackedOpen}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.client.SessionEvent.Create().SetSessionID(b.HomeSessionID.String()).SetEventData(map[string]any{"text": "retain"}).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.client.ChatSessionEvent.Create().SetChatSessionID(b.HomeSessionID.String()).SetSeq(1).SetEventType("message").SetPayload(map[string]any{"text": "retain"}).Save(ctx); err != nil {
		t.Fatal(err)
	}
	ordinary, err := db.client.Session.Create().SetID(uuid.NewString()).SetTitle("Ordinary").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownJob, err := db.client.ScheduledJob.Create().SetName("owned").SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeBotTrigger).SetPayload(map[string]any{"bot_id": b.ID.String()}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherJob, err := db.client.ScheduledJob.Create().SetName("legacy").SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeAdaptive).SetPayload(map[string]any{}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.BeginDelete(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Remove"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListActivity(ctx, b.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var intent struct {
		Intent    store.BotOutboxKind `json:"intent"`
		NotBefore time.Time           `json:"not_before"`
		Revision  int64               `json:"revision"`
	}
	if err := json.Unmarshal(events[len(events)-1].Payload, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Intent != store.BotOutboxCleanup || !intent.NotBefore.Equal(*deleted.RestoreUntil) || intent.Revision != deleted.Revision {
		t.Fatalf("cleanup intent: %+v", intent)
	}
	if err := db.client.Bot.UpdateOneID(b.ID).SetCleanupComplete(true).ClearActiveRunID().ClearLeaseExpiresAt().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	*now = deleted.RestoreUntil.Add(-time.Second)
	if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 0 {
		t.Fatalf("purged within restore window: %d %v", n, err)
	}
	*now = *deleted.RestoreUntil
	// Fail near the end, after sessions, jobs, runs and goals have been removed.
	fail := true
	db.client.BotVersion.Use(func(next teament.Mutator) teament.Mutator {
		return teament.MutateFunc(func(ctx context.Context, m teament.Mutation) (teament.Value, error) {
			if fail {
				return nil, errors.New("version delete failed")
			}
			return next.Mutate(ctx, m)
		})
	})
	if n, err := repo.PurgeDue(ctx, *now, 10); err == nil || n != 0 {
		t.Fatalf("partial purge committed: %d %v", n, err)
	}
	if got, err := repo.Get(ctx, b.ID); err != nil || got.Revision != deleted.Revision {
		t.Fatalf("root rollback: %+v %v", got, err)
	}
	assertRunStateSequence(t, repo, ctx, *run, store.RunQueued, store.RunRunning, store.RunInterrupted)
	if n, err := db.client.SessionEvent.Query().Count(ctx); err != nil || n != 4 {
		t.Fatalf("transcript or three transition envelopes lost: %d %v", n, err)
	}
	if n, err := db.client.ChatSessionEvent.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("stream lost: %d %v", n, err)
	}
	if n, err := db.client.BotTrackedItem.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("commitment lost: %d %v", n, err)
	}
	if _, err := db.client.ScheduledJob.Get(ctx, ownJob.ID); err != nil {
		t.Fatalf("schedule lost: %v", err)
	}
	fail = false
	if n, err := repo.PurgeDue(context.Background(), *now, 10); !errors.Is(err, store.ErrBotForbidden) || n != 0 {
		t.Fatalf("anonymous purge: %d %v", n, err)
	}
	role := repo.auth.check
	repo.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
	if n, err := repo.PurgeDue(ctx, *now, 10); !errors.Is(err, store.ErrBotForbidden) || n != 0 {
		t.Fatalf("viewer purge: %d %v", n, err)
	}
	repo.auth.check = role
	if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 1 {
		t.Fatalf("retry purge: %d %v", n, err)
	}
	if _, err := db.client.Session.Get(ctx, ordinary.ID); err != nil {
		t.Fatalf("ordinary session lost: %v", err)
	}
	if _, err := db.client.ScheduledJob.Get(ctx, otherJob.ID); err != nil {
		t.Fatalf("legacy schedule lost: %v", err)
	}
	if n, err := db.client.SessionEvent.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("transcript not purged: %d %v", n, err)
	}
	if n, err := db.client.ChatSessionEvent.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("stream not purged: %d %v", n, err)
	}
	if n, err := db.client.BotTrackedItem.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("commitment not purged: %d %v", n, err)
	}
}

func TestBotRetentionPurgeRequiresCleanup(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			deleted, err := repo.BeginDelete(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Delete"})
			if err != nil {
				t.Fatal(err)
			}
			if n, err := repo.PurgeDue(ctx, *deleted.RestoreUntil, 10); !errors.Is(err, store.ErrBotInvalid) || n != 0 {
				t.Fatalf("future cutoff: %d %v", n, err)
			}
			*now = deleted.RestoreUntil.Add(time.Second)
			if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 0 {
				t.Fatalf("purged before cleanup: %d %v", n, err)
			}
			// Fixture-only confirmation. Production will require the cleanup adapter to
			// observe shutdown and retained-home removal before writing these fields.
			var sessions store.SessionStore
			switch d := repo.db.(type) {
			case teamBotDB:
				if err := d.client.Bot.UpdateOneID(b.ID).SetCleanupComplete(true).Exec(ctx); err != nil {
					t.Fatal(err)
				}
				sessions = &teamSessionStore{client: d.client}
			case personalBotDB:
				if err := d.client.Bot.UpdateOneID(b.ID).SetCleanupComplete(true).Exec(ctx); err != nil {
					t.Fatal(err)
				}
				sessions = &personalSessionStore{client: d.client}
			}
			if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 0 {
				t.Fatalf("ignored teardown barrier: %d %v", n, err)
			}
			switch d := repo.db.(type) {
			case teamBotDB:
				if err := d.client.Bot.UpdateOneID(b.ID).ClearActiveRunID().ClearLeaseExpiresAt().Exec(ctx); err != nil {
					t.Fatal(err)
				}
			case personalBotDB:
				if err := d.client.Bot.UpdateOneID(b.ID).ClearActiveRunID().ClearLeaseExpiresAt().Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			ordinary, err := sessions.Create(ctx, &adksession.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 1 {
				t.Fatalf("purge: %d %v", n, err)
			}
			if _, err := repo.Get(ctx, b.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("bot remains: %v", err)
			}
			for _, id := range []string{b.HomeSessionID.String(), run.SessionID.String()} {
				if meta, err := sessions.GetSessionMeta(ctx, id); err != nil || meta != nil {
					t.Fatalf("session remains: %+v %v", meta, err)
				}
			}
			if _, err := sessions.Get(ctx, &adksession.GetRequest{SessionID: ordinary.Session.ID(), UserID: b.OwnerID.String()}); err != nil {
				t.Fatalf("ordinary Chat removed: %v", err)
			}
			if n, err := repo.PurgeDue(ctx, *now, 10); err != nil || n != 0 {
				t.Fatalf("purge retry: %d %v", n, err)
			}
		})
	}
}

func TestBotRetentionRejectsGenericSessionDeletion(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			var sessions store.SessionStore
			switch d := repo.db.(type) {
			case teamBotDB:
				sessions = &teamSessionStore{client: d.client}
			case personalBotDB:
				sessions = &personalSessionStore{client: d.client}
			}
			for _, id := range []string{b.HomeSessionID.String(), run.SessionID.String()} {
				if err := sessions.Delete(ctx, &adksession.DeleteRequest{SessionID: id, UserID: b.OwnerID.String()}); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("generic delete: %v", err)
				}
				if err := sessions.RemoveSessionMeta(ctx, id); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("metadata delete: %v", err)
				}
			}
			// Negative age makes all current rows eligible, without modifying immutable
			// timestamps; Bot rows must nevertheless be absent from cleanup candidates.
			ordinary, err := sessions.Create(ctx, &adksession.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			removed := sessions.CleanupExpiredSessions(ctx, -1)
			if len(removed) != 1 || removed[0] != ordinary.Session.ID() {
				t.Fatalf("TTL did not preserve only Bot sessions: %v", removed)
			}
			ids, err := sessions.(store.SessionIDEnumerator).AllSessionIDsStrict(ctx)
			if err != nil || !ids[b.HomeSessionID.String()] || !ids[run.SessionID.String()] {
				t.Fatalf("retained inventory: %v %v", ids, err)
			}
		})
	}
}

func TestBotCorrectionRollbackAndScopedFollowups(t *testing.T) {
	repo, ctx, _, run, _, now := botRunFixture(t, store.BotScopeTeam)
	db := repo.db.(teamBotDB)
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	g, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Delivery", CompletionCriteria: "Received"}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, goalID uuid.UUID) *teament.ScheduledJob {
		j, err := db.client.ScheduledJob.Create().SetName(name).SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeBotTrigger).SetPayload(map[string]any{"bot_id": b.ID.String(), "goal_id": goalID.String()}).SetNextRunAt(now.Add(time.Hour)).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	owned, other := create("corrected goal", g.ID), create("other goal", uuid.New())
	q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:date", Replacement: "Friday", GoalIDs: []uuid.UUID{g.ID}, Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Correct date", Source: "message", ObservedAt: *now, ActorID: b.OwnerID}}
	repo.db = failedGoalScheduleTransaction{db}
	if err := repo.Correct(ctx, q); err == nil {
		t.Fatal("ignored schedule failure")
	}
	repo.db = db
	unchanged, err := repo.Get(ctx, b.ID)
	if err != nil || unchanged.Revision != b.Revision {
		t.Fatalf("partial correction: %+v %v", unchanged, err)
	}
	if _, err := db.FindActivity(ctx, b.ID, "correction:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("partial reconciliation: %v", err)
	}
	// Fail after both the revision and schedule writes to prove full rollback.
	fail := true
	db.client.BotOutbox.Use(func(next teament.Mutator) teament.Mutator {
		return teament.MutateFunc(func(ctx context.Context, m teament.Mutation) (teament.Value, error) {
			if fail {
				return nil, errors.New("outbox unavailable")
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := repo.Correct(ctx, q); err == nil {
		t.Fatal("ignored outbox failure")
	}
	j, err := db.client.ScheduledJob.Get(ctx, owned.ID)
	if err != nil || j.Status != scheduledjob.StatusActive || j.NextRunAt == nil {
		t.Fatalf("partial schedule write: %+v %v", j, err)
	}
	fail = false
	if err := repo.Correct(ctx, q); err != nil {
		t.Fatal(err)
	}
	j, err = db.client.ScheduledJob.Get(ctx, owned.ID)
	if err != nil || j.Status != scheduledjob.StatusPaused || j.NextRunAt != nil {
		t.Fatalf("affected followup active: %+v %v", j, err)
	}
	j, err = db.client.ScheduledJob.Get(ctx, other.ID)
	if err != nil || j.Status != scheduledjob.StatusActive || j.NextRunAt == nil {
		t.Fatalf("unrelated followup changed: %+v %v", j, err)
	}
}

func TestBotCorrectionConcurrentCAS(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotCorrection{BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:date", Replacement: "Friday", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Correct date", Source: "message", ObservedAt: *now, ActorID: b.OwnerID}}
			start := make(chan struct{})
			results := make(chan error, 2)
			for range 2 {
				go func() { request := q; request.ID = uuid.New(); <-start; results <- repo.Correct(ctx, request) }()
			}
			close(start)
			wins := 0
			for range 2 {
				if err := <-results; err == nil {
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("correction winners=%d", wins)
			}
			current, err := repo.Get(ctx, b.ID)
			if err != nil || current.Revision != b.Revision+1 {
				t.Fatalf("revision: %+v %v", current, err)
			}
		})
	}
}

func TestBotActivationRejectsRevokedMembershipAndUnreviewedRecurrence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			v := store.DefaultBotVersion()
			v.Charter = "Observe"
			draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Review", OwnerID: b.OwnerID, Version: v, IdempotencyKey: "review"})
			if err != nil {
				t.Fatal(err)
			}
			role := "member"
			repo.auth.check = func(context.Context, string) (string, error) { return role, nil }
			repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { role = ""; return nil }
			q := store.BotLifecycleRequest{BotID: draft.ID, ExpectedRevision: 1, ActorID: b.OwnerID, Reason: "Activate"}
			if _, err := repo.Activate(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked during readiness: %v", err)
			}
			role = "member"
			saved, err := repo.Get(ctx, draft.ID)
			if err != nil || saved.Revision != 1 || saved.Status != store.BotDraft {
				t.Fatalf("revocation changed draft: %+v %v", saved, err)
			}
			repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error {
				t.Fatal("recurring activation reached readiness without scheduler adapter")
				return nil
			}
			v.BotID = draft.ID
			v.Triggers = []store.TriggerSpec{{ID: uuid.New(), Kind: store.TriggerCron, Cron: "0 9 * * *"}}
			if _, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: draft.ID, ExpectedRevision: 1, Version: v}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("unreviewed recurrence entered through prose edit: %v", err)
			}
			saved, err = repo.Get(ctx, draft.ID)
			if err != nil || saved.Revision != 1 || saved.Version != draft.Version || saved.Status != store.BotDraft {
				t.Fatalf("rejected recurrence changed draft: %+v %v", saved, err)
			}
			// Simulate a legacy/raw persisted version: today's prose API correctly
			// refuses this edit, but activation must still reject old unreviewed data.
			v.Version = draft.Version + 1
			v.CreatedBy, v.CreatedAt = b.OwnerID, repo.clock()
			hash, err := botHash(v)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.db.Transaction(ctx, func(tx botDraftDB) error {
				rec, err := tx.GetBot(ctx, draft.ID)
				if err != nil {
					return err
				}
				if err := tx.UpdateVersion(ctx, draft.ID, draft.Revision, v.Version, rec.LastSequence+1, v.CreatedAt); err != nil {
					return err
				}
				return tx.InsertVersion(ctx, v, hash)
			}); err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = draft.Revision + 1
			if _, err := repo.Activate(ctx, q); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("unreviewed recurrence: %v", err)
			}
		})
	}
}

func TestBotCorrectionDurableIntent(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			g, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Delivery", CompletionCriteria: "Received"}})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:delivery-date", Replacement: "Delivery is Friday, not Thursday", GoalIDs: []uuid.UUID{g.ID}, Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Correct delivery date", Source: "owner-message:2", ActorID: b.OwnerID, ObservedAt: *now}}
			before, err := repo.ListActivity(ctx, b.ID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.Correct(ctx, q); err != nil {
				t.Fatal(err)
			}
			if err := repo.Correct(ctx, q); err != nil {
				t.Fatalf("exact retry: %v", err)
			}
			saved, err := repo.Get(ctx, b.ID)
			if err != nil || saved.Revision != b.Revision+1 || saved.Version != b.Version || saved.Status != b.Status {
				t.Fatalf("correction changed authority: %+v %v", saved, err)
			}
			events, err := repo.ListActivity(ctx, b.ID, before[len(before)-1].Sequence, 200)
			if err != nil || len(events) != 1 {
				t.Fatalf("duplicate correction: %+v %v", events, err)
			}
			var payload struct {
				Correction store.BotCorrection `json:"correction"`
				Intent     store.BotOutboxKind `json:"intent"`
				Revision   int64               `json:"revision"`
			}
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Intent != store.BotOutboxReconcile || payload.Correction.Replacement != q.Replacement || payload.Correction.ID != q.ID || payload.Revision != saved.Revision {
				t.Fatalf("missing durable reconciliation: %+v", payload)
			}
			q.Replacement = "Conflicting retry"
			if err := repo.Correct(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("changed retry: %v", err)
			}
			q.ID = uuid.New()
			if err := repo.Correct(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale correction: %v", err)
			}
			q.ExpectedRevision = saved.Revision
			q.GoalIDs = []uuid.UUID{uuid.New()}
			if err := repo.Correct(ctx, q); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("foreign goal: %v", err)
			}
			q.GoalIDs = nil
			q.Evidence.ActorID = uuid.New()
			if err := repo.Correct(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("forged evidence: %v", err)
			}
		})
	}
}

func TestBotTemplatePublishForkIsolation(t *testing.T) {
	c := newTeamClient(t)
	_, private := setupPersonalStore(t)
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	role := "member"
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return role, nil }}
	templates := &teamBotTemplateStore{client: c, auth: auth}
	def := store.BotTemplateDefinition{SchemaVersion: store.BotSchemaVersion, Name: "Fare watch", Charter: "Observe fares, never book", SuggestedGoals: []store.BotGoalDefinition{{Title: "Find fare", CompletionCriteria: "Verified fare below threshold"}}, SuggestedSchedules: []store.BotScheduleSuggestion{{Cron: "0 9 * * *", Timezone: "America/New_York", Instructions: "Check fares"}}}
	if _, err := templates.Publish(ctx, def, 0); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("member published: %v", err)
	}
	role = "admin"
	published, err := templates.Publish(ctx, def, 0)
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != 1 || published.PublishedBy != owner {
		t.Fatalf("publication: %+v", published)
	}
	if _, err := templates.Publish(ctx, def, 0); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("duplicate publication: %v", err)
	}
	if n, err := c.TeamAuditLog.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("publication audit: %d %v", n, err)
	}
	destination := &personalBotStore{client: private, auth: botRepositoryAuth{orgSlug: "org", userID: owner.String(), scope: store.BotScopePersonal, check: func(context.Context, string) (string, error) { return "member", nil }}}
	role = "member"
	req := store.BotTemplateForkRequest{TemplateID: published.ID, TemplateRevision: 1, Name: "My fares", IdempotencyKey: "fork"}
	fork, err := templates.ForkToPersonal(ctx, destination, req)
	if err != nil {
		t.Fatal(err)
	}
	if fork.Status != store.BotDraft || fork.Scope != store.BotScopePersonal {
		t.Fatalf("unsafe fork: %+v", fork)
	}
	repeated, err := templates.ForkToPersonal(ctx, destination, req)
	if err != nil || repeated.ID != fork.ID {
		t.Fatalf("fork retry: %+v %v", repeated, err)
	}
	v, err := destination.GetVersion(ctx, fork.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v.Autonomy.Level != execution.AutonomySupervised || v.Autonomy.CanContactPeople || v.Autonomy.CanPublish || v.Autonomy.MaxSpendPerRunMinor != 0 || len(v.Delivery.Bindings) != 0 || len(v.Triggers) != 1 || v.Triggers[0].Enabled || v.Triggers[0].ScheduleID != nil || v.Triggers[0].CredentialRef != "" {
		t.Fatalf("fork carries authority: %+v", v)
	}
	goals, err := destination.ListGoals(ctx, fork.ID)
	if err != nil || len(goals) != 1 || goals[0].Status != store.BotGoalActive || len(goals[0].Evidence) != 0 {
		t.Fatalf("forked goals: %+v %v", goals, err)
	}
	if n, err := private.ScheduledJob.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("fork scheduled execution: %d %v", n, err)
	}
	role = "admin"
	def.Charter = "Observe only the official source"
	updated, err := templates.Publish(ctx, def, 1)
	if err != nil || updated.ID != published.ID || updated.Revision != 2 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := templates.ForkToPersonal(ctx, destination, req); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale template fork: %v", err)
	}
	original, err := destination.GetVersion(ctx, fork.ID, 1)
	if err != nil || original.Charter != v.Charter {
		t.Fatalf("publication mutated fork: %+v %v", original, err)
	}
	role = ""
	if _, err := templates.Get(ctx, published.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revoked reader: %v", err)
	}
}

func TestBotTemplateValidationAndScope(t *testing.T) {
	c := newTeamClient(t)
	actor := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	s := &teamBotTemplateStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}}
	good := store.BotTemplateDefinition{SchemaVersion: 1, Name: "Watch", Charter: "Observe"}
	invalid := []store.BotTemplateDefinition{{SchemaVersion: 99, Name: "Watch", Charter: "Observe"}, {SchemaVersion: 1, Name: "", Charter: "Observe"}, {SchemaVersion: 1, Name: "Watch", Charter: ""}, {SchemaVersion: 1, Name: "Watch", Charter: "Observe", SuggestedSchedules: []store.BotScheduleSuggestion{{Cron: "bad", Timezone: "UTC", Instructions: "check"}}}, {SchemaVersion: 1, Name: "Watch", Charter: "Observe", SuggestedSchedules: []store.BotScheduleSuggestion{{Cron: "0 9 * * *", Timezone: "Not/AZone", Instructions: "check"}}}}
	for i, def := range invalid {
		if _, err := s.Publish(ctx, def, 0); err == nil {
			t.Errorf("invalid template %d accepted", i)
		}
	}
	if n, err := c.BotTemplate.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("invalid rows persisted: %d %v", n, err)
	}
	if _, err := s.Publish(context.Background(), good, 0); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("anonymous publish: %v", err)
	}
	foreign, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "other", TeamSlug: "team", Authenticated: true})
	if _, err := s.List(foreign, store.BotPage{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("cross-org listing: %v", err)
	}
}

func TestBotTemplateAuditFailureRollsBack(t *testing.T) {
	c := newTeamClient(t)
	actor := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	s := &teamBotTemplateStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}}
	def := store.BotTemplateDefinition{SchemaVersion: 1, Name: "Watch", Charter: "Observe"}
	// Ent mutation hook injects audit failure without changing table permissions.
	c.TeamAuditLog.Use(func(next teament.Mutator) teament.Mutator {
		return teament.MutateFunc(func(ctx context.Context, m teament.Mutation) (teament.Value, error) {
			return nil, errors.New("audit unavailable")
		})
	})
	if _, err := s.Publish(ctx, def, 0); err == nil {
		t.Fatal("publication ignored failed audit")
	}
	if n, err := c.BotTemplate.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("publication partially committed: %d %v", n, err)
	}
}

func TestBotTemplateRejectsUnexpectedPersistedFields(t *testing.T) {
	c := newTeamClient(t)
	actor := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	s := &teamBotTemplateStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}}
	row, err := c.BotTemplate.Create().SetName("Unsafe shape").SetPublishedBy(actor).SetDefinition(map[string]any{"schema_version": 1, "name": "Unsafe shape", "charter": "Observe", "credentials": map[string]any{"mail": "private-reference"}}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, row.ID); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("unexpected private fields accepted: %v", err)
	}
}

func TestBotTemplateConcurrentPublicationCAS(t *testing.T) {
	c := newTeamClient(t)
	actor := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	s := &teamBotTemplateStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}}
	def := store.BotTemplateDefinition{SchemaVersion: 1, Name: "Watch", Charter: "Observe"}
	initial, err := s.Publish(ctx, def, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, charter := range []string{"Observe A", "Observe B"} {
		wg.Add(1)
		go func(charter string) {
			defer wg.Done()
			<-start
			d := def
			d.Charter = charter
			_, err := s.Publish(ctx, d, 1)
			results <- err
		}(charter)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("publication winners: %d", wins)
	}
	current, err := s.Get(ctx, initial.ID)
	if err != nil || current.Revision != 2 {
		t.Fatalf("current revision: %+v %v", current, err)
	}
	if n, err := c.TeamAuditLog.Query().Count(ctx); err != nil || n != 2 {
		t.Fatalf("phantom audit: %d %v", n, err)
	}
}

func TestBotGoalCompletionRequiresWholeOutcome(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			g, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Replacement", CompletionCriteria: "Replacement arrived and works"}})
			if err != nil {
				t.Fatal(err)
			}
			evidence := store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "It arrived and works", Source: "owner-message:1", ObservedAt: *now, ActorID: b.OwnerID}
			q := store.BotGoalCompletion{BotID: b.ID, GoalID: g.ID, ActorID: b.OwnerID, ExpectedRevision: 1}
			if _, err := repo.CompleteGoal(ctx, q); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("completed without evidence: %v", err)
			}
			item := store.BotTrackedItem{ID: uuid.New(), GoalID: g.ID, Title: "Confirm replacement works", Status: store.BotTrackedOpen}
			if err := repo.SaveTrackedItem(ctx, b.ID, item, 1); err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = 2
			q.Evidence = []store.BotEvidence{evidence}
			if _, err := repo.CompleteGoal(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("completed with open responsibility: %v", err)
			}
			item.Status = store.BotTrackedCompleted
			if err := repo.SaveTrackedItem(ctx, b.ID, item, 2); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("commitment completed without evidence: %v", err)
			}
			item.Evidence = []store.BotEvidence{evidence}
			if err := repo.SaveTrackedItem(ctx, b.ID, item, 2); err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = 3
			done, err := repo.CompleteGoal(ctx, q)
			if err != nil || done.Status != store.BotGoalCompleted || done.CompletedAt == nil || done.Revision != 4 {
				t.Fatalf("completion: %+v %v", done, err)
			}
			if err := repo.SaveTrackedItem(ctx, b.ID, store.BotTrackedItem{ID: uuid.New(), GoalID: g.ID, Title: "Unrequested follow-up", Status: store.BotTrackedOpen}, 4); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("finished goal reopened: %v", err)
			}
			items, err := repo.ListTrackedItems(ctx, b.ID, g.ID)
			if err != nil || len(items) != 1 || items[0].Status != store.BotTrackedCompleted {
				t.Fatalf("commitments: %+v %v", items, err)
			}
		})
	}
}

type failedGoalScheduleDB struct{ botDraftDB }

func (d failedGoalScheduleDB) DisableGoalSchedules(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("schedule update failed")
}

type failedGoalScheduleTransaction struct{ botDraftDB }

func (d failedGoalScheduleTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failedGoalScheduleDB{tx}) })
}
func TestBotGoalCompletionStopsOnlyOwnedFollowups(t *testing.T) {
	repo, ctx, _, run, _, now := botRunFixture(t, store.BotScopeTeam)
	db := repo.db.(teamBotDB)
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Delivery", CompletionCriteria: "Received"}})
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, goalID uuid.UUID) *teament.ScheduledJob {
		j, err := db.client.ScheduledJob.Create().SetName(name).SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeBotTrigger).SetPayload(map[string]any{"bot_id": b.ID.String(), "goal_id": goalID.String()}).SetNextRunAt(now.Add(time.Hour)).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	owned := create("delivery follow-up", goal.ID)
	other := create("other goal", uuid.New())
	q := store.BotGoalCompletion{BotID: b.ID, GoalID: goal.ID, ExpectedRevision: 1, ActorID: b.OwnerID, Evidence: []store.BotEvidence{{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Received", Source: "message", ObservedAt: *now, ActorID: b.OwnerID}}}
	repo.db = failedGoalScheduleTransaction{db}
	if _, err := repo.CompleteGoal(ctx, q); err == nil {
		t.Fatal("expected failure")
	}
	repo.db = db
	unchanged, err := db.ReadGoal(ctx, b.ID, goal.ID)
	if err != nil || unchanged.Status != store.BotGoalActive || unchanged.Revision != 1 {
		t.Fatalf("partial completion: %+v %v", unchanged, err)
	}
	if _, err := repo.CompleteGoal(ctx, q); err != nil {
		t.Fatal(err)
	}
	j, err := db.client.ScheduledJob.Get(ctx, owned.ID)
	if err != nil || j.Status != scheduledjob.StatusPaused || j.NextRunAt != nil {
		t.Fatalf("followup remains active: %+v %v", j, err)
	}
	j, err = db.client.ScheduledJob.Get(ctx, other.ID)
	if err != nil || j.Status != scheduledjob.StatusActive || j.NextRunAt == nil {
		t.Fatalf("unrelated followup changed: %+v %v", j, err)
	}
}

func TestBotTrackedItemCannotMoveBetweenGoals(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			create := func(title string) *store.BotGoal {
				g, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: run.BotID, Goal: store.BotGoal{BotID: run.BotID, Title: title, CompletionCriteria: "Owner confirms"}})
				if err != nil {
					t.Fatal(err)
				}
				return g
			}
			a, b := create("A"), create("B")
			item := store.BotTrackedItem{ID: uuid.New(), GoalID: a.ID, Title: "Commitment", Status: store.BotTrackedOpen}
			if err := repo.SaveTrackedItem(ctx, run.BotID, item, 1); err != nil {
				t.Fatal(err)
			}
			item.GoalID = b.ID
			if err := repo.SaveTrackedItem(ctx, run.BotID, item, 1); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("moved tracked item: %v", err)
			}
			g, err := repo.db.ReadGoal(ctx, run.BotID, b.ID)
			if err != nil || g.Revision != 1 {
				t.Fatalf("failed move changed goal: %+v %v", g, err)
			}
			items, err := repo.ListTrackedItems(ctx, run.BotID, a.ID)
			if err != nil || len(items) != 1 {
				t.Fatalf("original lost: %+v %v", items, err)
			}
		})
	}
}

func TestBotActivationRequiresReadinessAndExplicitRevision(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			principal, _ := execution.PrincipalFromContext(ctx)
			owner, err := uuid.Parse(principal.Subject)
			if err != nil {
				t.Fatal(err)
			}
			v := store.DefaultBotVersion()
			v.Charter = "Observe only"
			draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Activate", OwnerID: owner, Version: v, IdempotencyKey: "activate"})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotLifecycleRequest{BotID: draft.ID, ExpectedRevision: 1, ActorID: owner, Reason: "Enable manual runs"}
			if _, err := repo.Activate(ctx, q); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("activation without runtime: %v", err)
			}
			unavailable := errors.New("durable home unavailable")
			repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return unavailable }
			if _, err := repo.Activate(ctx, q); !errors.Is(err, unavailable) {
				t.Fatalf("readiness ignored: %v", err)
			}
			saved, err := repo.Get(ctx, draft.ID)
			if err != nil || saved.Status != store.BotDraft || saved.Revision != 1 {
				t.Fatalf("failed activation changed draft: %+v %v", saved, err)
			}
			checks := 0
			repo.activationCheck = func(_ context.Context, b store.Bot, version store.BotVersion) error {
				checks++
				if b.ID != draft.ID || version.BotID != draft.ID || version.Version != 1 {
					t.Fatal("readiness checked wrong snapshot")
				}
				return nil
			}
			active, err := repo.Activate(ctx, q)
			if err != nil || active.Status != store.BotActive || active.Revision != 2 || checks != 1 {
				t.Fatalf("activation: %+v %v checks=%d", active, err, checks)
			}
			if _, err := repo.Activate(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale activation: %v", err)
			}
			q.ExpectedRevision = active.Revision
			paused, err := repo.Pause(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = paused.Revision
			resumed, err := repo.Resume(ctx, q)
			if err != nil || resumed.Status != store.BotActive {
				t.Fatalf("resume: %+v %v", resumed, err)
			}
			runs, err := repo.ListRuns(ctx, draft.ID, store.BotPage{})
			if err != nil || len(runs) != 0 {
				t.Fatalf("activation replayed work: %+v %v", runs, err)
			}
			// The pre-existing run has a live container barrier even after its lease is
			// revoked by pause. Resuming its Bot must not clear that barrier.
			existing, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			p, err := repo.Pause(ctx, store.BotLifecycleRequest{BotID: existing.ID, ExpectedRevision: existing.Revision, ActorID: owner, Reason: "Pause active run"})
			if err != nil {
				t.Fatal(err)
			}
			repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
			if _, err := repo.Resume(ctx, store.BotLifecycleRequest{BotID: p.ID, ExpectedRevision: p.Revision, ActorID: owner, Reason: "Resume"}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("resumed before shutdown: %v", err)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "late"}}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("old worker revived: %v", err)
			}
		})
	}
}

func TestBotActivationRechecksStateAfterReadiness(t *testing.T) {
	repo, ctx, _, _, _, _ := botRunFixture(t, store.BotScopeTeam)
	principal, _ := execution.PrincipalFromContext(ctx)
	owner, _ := uuid.Parse(principal.Subject)
	v := store.DefaultBotVersion()
	v.Charter = "Review version"
	draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Raced", OwnerID: owner, Version: v, IdempotencyKey: "raced"})
	if err != nil {
		t.Fatal(err)
	}
	repo.activationCheck = func(ctx context.Context, b store.Bot, v store.BotVersion) error {
		v.Charter = "New charter"
		_, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: b.Revision, Version: v})
		return err
	}
	if _, err := repo.Activate(ctx, store.BotLifecycleRequest{BotID: draft.ID, ExpectedRevision: 1, ActorID: owner, Reason: "Activate reviewed charter"}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("unreviewed version activated: %v", err)
	}
	saved, err := repo.Get(ctx, draft.ID)
	if err != nil || saved.Status != store.BotDraft || saved.Version != 2 {
		t.Fatalf("race outcome: %+v %v", saved, err)
	}
}

func TestBotRepositoryDraftAtomicityAndScope(t *testing.T) {
	c := newTeamClient(t)
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := &teamBotStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}}
	v := store.DefaultBotVersion()
	v.Charter = "Observe fares without booking"
	req := store.BotCreateRequest{Name: "Fare watch", OwnerID: owner, Version: v, IdempotencyKey: "create-1"}
	b, err := repo.CreateDraft(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotDraft || b.Scope != store.BotScopeTeam {
		t.Fatalf("bad draft: %+v", b)
	}
	again, err := repo.CreateDraft(ctx, req)
	if err != nil || again.ID != b.ID {
		t.Fatalf("idempotency: %+v %v", again, err)
	}
	changed := req
	changed.Name = "different"
	if _, err := repo.CreateDraft(ctx, changed); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("different request reused key: %v", err)
	}
	sess, err := c.Session.Get(ctx, b.HomeSessionID.String())
	if err != nil || sess.BotID == nil || *sess.BotID != b.ID || sess.Purpose != teamsession.PurposeBotHome {
		t.Fatalf("home linkage: %+v %v", sess, err)
	}
	version, err := repo.GetVersion(ctx, b.ID, 1)
	if err != nil || version.Charter != v.Charter {
		t.Fatalf("version: %+v %v", version, err)
	}
	if n, err := c.BotOutbox.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("draft activity: %d %v", n, err)
	}
	if _, err := repo.Get(context.Background(), b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("unauthenticated read: %v", err)
	}
	other, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "other", TeamSlug: "team", Authenticated: true})
	if _, err := repo.Get(other, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("cross-org read: %v", err)
	}
	invalid := req
	invalid.IdempotencyKey = "bad-goal"
	invalid.Goals = []store.BotGoal{{Title: ""}}
	if _, err := repo.CreateDraft(ctx, invalid); err == nil {
		t.Fatal("invalid goal accepted")
	}
	if n, err := c.Bot.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("partial draft persisted: %d %v", n, err)
	}
}

func TestBotRepositoryPersonalVersionIsolation(t *testing.T) {
	_, c := setupPersonalStore(t)
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	role := "member"
	repo := &personalBotStore{client: c, auth: botRepositoryAuth{orgSlug: "org", userID: owner.String(), scope: store.BotScopePersonal, check: func(context.Context, string) (string, error) { return role, nil }}}
	v := store.DefaultBotVersion()
	v.Charter = "Keep a private goal"
	v.Autonomy.Level = execution.AutonomyScoped
	v.Autonomy.CanPublish = true
	v.Triggers = []store.TriggerSpec{{ID: uuid.New(), Kind: store.TriggerCron, Enabled: true, Cron: "0 9 * * *"}}
	req := store.BotCreateRequest{Name: "Private", OwnerID: owner, Version: v, IdempotencyKey: "private-create"}
	b, err := repo.CreateDraft(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !req.Version.Triggers[0].Enabled || req.Version.Autonomy.Level != execution.AutonomyScoped {
		t.Fatal("mutated caller-owned draft")
	}
	saved, err := repo.GetVersion(ctx, b.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Triggers[0].Enabled || saved.Autonomy.Level != execution.AutonomySupervised || saved.Autonomy.CanPublish {
		t.Fatal("draft retained live authority")
	}
	update := *saved
	update.Charter = "Corrected goal"
	updated, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: 1, Version: update})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Version != 2 {
		t.Fatalf("version not advanced: %+v", updated)
	}
	if _, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: 1, Version: update}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	old, err := repo.GetVersion(ctx, b.ID, 1)
	if err != nil || old.Charter != "Keep a private goal" {
		t.Fatalf("history mutated: %+v %v", old, err)
	}
	update.Autonomy.CanPublish = true
	if _, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: 2, Version: update}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("draft elevated: %v", err)
	}
	stranger, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if _, err := repo.List(stranger, store.BotPage{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("private list leaked: %v", err)
	}
	role = ""
	if _, err := repo.Get(ctx, b.ID); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("revoked member allowed: %v", err)
	}
}

func TestBotRepositoryGoalOwnershipAndOrderedActivity(t *testing.T) {
	c := newTeamClient(t)
	owner := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	repo := &teamBotStore{client: c, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}}
	v := store.DefaultBotVersion()
	v.Charter = "Track a delivery"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Delivery", OwnerID: owner, Version: v, IdempotencyKey: "one"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Other", OwnerID: owner, Version: v, IdempotencyKey: "two"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Receive package", CompletionCriteria: "Owner confirms receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	g.Title = "Receive correct package"
	changed, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: *g, ExpectedRevision: 1})
	if err != nil || changed.Revision != 2 {
		t.Fatalf("goal update: %+v %v", changed, err)
	}
	if _, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: *g, ExpectedRevision: 1}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale goal write: %v", err)
	}
	forged := *changed
	forged.BotID = other.ID
	if _, err := repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: other.ID, Goal: forged, ExpectedRevision: 2}); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("cross-bot goal write: %v", err)
	}
	events, err := repo.ListActivity(ctx, b.ID, 0, 50)
	if err != nil || len(events) != 3 {
		t.Fatalf("activity: %+v %v", events, err)
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) || event.BotID != b.ID {
			t.Fatalf("unordered event: %+v", event)
		}
	}
	replay, err := repo.ListActivity(ctx, b.ID, 2, 50)
	if err != nil || len(replay) != 1 || replay[0].Sequence != 3 {
		t.Fatalf("cursor replay: %+v %v", replay, err)
	}
	goals, err := repo.ListGoals(ctx, other.ID)
	if err != nil || len(goals) != 0 {
		t.Fatalf("other bot goals: %+v %v", goals, err)
	}
}

type failingBotDraftDB struct{ botDraftDB }

func (d failingBotDraftDB) InsertHomeSession(context.Context, store.Bot) error {
	return errors.New("injected home-session failure")
}

type failingBotDraftTransaction struct{ botDraftDB }

func (d failingBotDraftTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failingBotDraftDB{tx}) })
}
func TestBotRepositoryDraftRollsBackOnSessionFailure(t *testing.T) {
	c := newTeamClient(t)
	owner := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	repo := &botRepository{auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}, db: failingBotDraftTransaction{teamBotDB{client: c}}}
	v := store.DefaultBotVersion()
	v.Charter = "Observe only"
	if _, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Atomic", OwnerID: owner, Version: v, IdempotencyKey: "rollback"}); err == nil {
		t.Fatal("expected injected error")
	}
	if n, err := c.Bot.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("partial bot: %d %v", n, err)
	}
	if n, err := c.BotVersion.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("partial version: %d %v", n, err)
	}
	if n, err := c.BotOutbox.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("partial publication: %d %v", n, err)
	}
}

func TestBotRepositoryStaleVersionCannotOverwriteActivitySequence(t *testing.T) {
	c := newTeamClient(t)
	ctx := context.Background()
	b := createBotSchemaFixture(t, c)
	d := teamBotDB{client: c}
	if err := d.AdvanceSequence(ctx, b.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateVersion(ctx, b.ID, 1, 2, 1, time.Now()); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale activity sequence accepted: %v", err)
	}
	saved, err := c.Bot.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.LastSeq != 1 || saved.Version != 1 || saved.Revision != 1 {
		t.Fatalf("stale writer modified root: %+v", saved)
	}
}

func TestBotRunAdmissionAndLeaseFencing(t *testing.T) {
	c := newTeamClient(t)
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	repo := &botRepository{db: teamBotDB{client: c}, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}, now: func() time.Time { return now }}
	v := store.DefaultBotVersion()
	v.Charter = "Check once"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Lease test", OwnerID: owner, Version: v, IdempotencyKey: "lease"})
	if err != nil {
		t.Fatal(err)
	}
	req := store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "run-1", Instructions: "Check once"}
	if _, err := repo.RequestRun(ctx, req); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("draft ran: %v", err)
	}
	// Seed activation only: production activation remains a separate reviewed path.
	if err := c.Bot.UpdateOneID(b.ID).SetStatus(bot.StatusActive).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.RequestRun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := repo.RequestRun(ctx, req)
	if err != nil || again.ID != run.ID {
		t.Fatalf("duplicate admission: %+v %v", again, err)
	}
	changed := req
	changed.Instructions = "Different task"
	if _, err := repo.RequestRun(ctx, changed); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("idempotency scope: %v", err)
	}
	session, err := c.Session.Get(ctx, run.SessionID.String())
	if err != nil || session.BotRunID == nil || *session.BotRunID != run.ID || session.Purpose != teamsession.PurposeBotRun {
		t.Fatalf("run session: %+v %v", session, err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	claim := store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker-a", ExpiresAt: now.Add(time.Minute)}
	if _, err := repo.ClaimRun(ctx, claim); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("unbound worker: %v", err)
	}
	lease, err := repo.ClaimRun(worker, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimRun(worker, claim); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("duplicate claim: %v", err)
	}
	updated, err := repo.RenewLease(worker, lease, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Fence != lease.Fence {
		t.Fatal("renewal changed fence")
	}
	bad := updated
	bad.WorkerID = "worker-b"
	if _, err := repo.RenewLease(worker, bad, now.Add(3*time.Minute)); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("foreign renewal: %v", err)
	}
	a := store.BotActivity{BotID: b.ID, RunID: &run.ID, Kind: store.BotActivityRun, Payload: []byte(`{"progress":"checked"}`)}
	if _, err := repo.AppendActivity(worker, updated, a); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	if _, err := repo.AppendActivity(worker, updated, a); !errors.Is(err, store.ErrBotExpired) {
		t.Fatalf("expired writer: %v", err)
	}
	if _, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker-b", ExpiresAt: now.Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("blind restart: %v", err)
	}
}

func TestBotConcurrentClaimsAndPersonalQuota(t *testing.T) {
	_, c := setupPersonalStore(t)
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := &botRepository{db: personalBotDB{client: c}, auth: botRepositoryAuth{orgSlug: "org", userID: owner.String(), scope: store.BotScopePersonal, check: func(context.Context, string) (string, error) { return "member", nil }}, now: func() time.Time { return now }}
	v := store.DefaultBotVersion()
	v.Charter = "Observe"
	cap := int64(1)
	v.Budget.MaxRunsPerDay = &cap
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Quota", OwnerID: owner, Version: v, IdempotencyKey: "quota"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	req := store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "first", Instructions: "Check"}
	run, err := repo.RequestRun(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "second"
	if _, err := repo.RequestRun(ctx, req); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("quota ignored: %v", err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			_, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: name, ExpiresAt: now.Add(time.Minute)})
			results <- err
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful concurrent claims=%d", wins)
	}
	read, err := repo.GetRun(ctx, b.ID, run.ID)
	if err != nil || read.Status != store.RunRunning || read.Lease.Fence != 1 {
		t.Fatalf("claimed run: %+v %v", read, err)
	}
	if err := c.Bot.UpdateOneID(b.ID).SetStatus("paused").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RenewLease(worker, read.Lease, now.Add(2*time.Minute)); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("paused renewal: %v", err)
	}
	if _, err := repo.AppendActivity(worker, read.Lease, store.BotActivity{BotID: b.ID, RunID: &run.ID, Kind: store.BotActivityRun, Payload: []byte(`{}`)}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("paused write: %v", err)
	}
}

type failedBotRunDB struct{ botDraftDB }

func (d failedBotRunDB) InsertRun(context.Context, botStoredRun, uuid.UUID) error {
	return errors.New("injected run insert failure")
}

type failedBotRunTransaction struct{ botDraftDB }

func (d failedBotRunTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failedBotRunDB{tx}) })
}
func TestBotRunAdmissionRollback(t *testing.T) {
	c := newTeamClient(t)
	owner := uuid.New()
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	repo := &botRepository{db: teamBotDB{client: c}, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}}
	v := store.DefaultBotVersion()
	v.Charter = "Observe"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Rollback", OwnerID: owner, Version: v, IdempotencyKey: "rollback-run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Bot.UpdateOneID(b.ID).SetStatus(bot.StatusActive).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	repo.db = failedBotRunTransaction{repo.db}
	if _, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, Instructions: "Observe", IdempotencyKey: "run"}); err == nil {
		t.Fatal("expected failure")
	}
	root, err := c.Bot.Get(ctx, b.ID)
	if err != nil || root.LastSeq != 1 {
		t.Fatalf("sequence partially committed: %+v %v", root, err)
	}
	if n, err := c.BotRun.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("partial run: %d %v", n, err)
	}
	if n, err := c.Session.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("partial session: %d %v", n, err)
	}
}

func TestBotDurableApprovalSingleConsumption(t *testing.T) {
	c := newTeamClient(t)
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	repo := &botRepository{db: teamBotDB{client: c}, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "member", nil }}, now: func() time.Time { return now }}
	v := store.DefaultBotVersion()
	v.Charter = "Prepare message; ask before sending"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Approval", OwnerID: owner, Version: v, IdempotencyKey: "approval"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Bot.UpdateOneID(b.ID).SetStatus(bot.StatusActive).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "Prepare message"})
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "w", ExpiresAt: now.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{"body":"hello","message_id":9007199254740993}`)}
	digest, err := action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := store.BotApproval{BotID: b.ID, RunID: run.ID, Version: 1, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Minute)}
	approval, err := repo.RequestApproval(worker, lease, request)
	if err != nil {
		t.Fatal(err)
	}
	same, err := repo.RequestApproval(worker, lease, request)
	if err != nil || same.ID != approval.ID {
		t.Fatalf("duplicate approval: %+v %v", same, err)
	}
	bad := store.BotApprovalDecision{BotID: b.ID, ApprovalID: approval.ID, ActorID: owner, ActionDigest: "changed", Status: store.ApprovalApproved}
	if _, err := repo.DecideApproval(ctx, bad); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("changed action approved: %v", err)
	}
	bad.ActionDigest = digest
	decided, err := repo.DecideApproval(ctx, bad)
	if err != nil || decided.Status != store.ApprovalApproved {
		t.Fatalf("decision: %+v %v", decided, err)
	}
	consume := store.BotApprovalConsumption{Lease: lease, ApprovalID: approval.ID, Version: 1, ActionDigest: digest}
	if err := repo.ConsumeApproval(worker, consume); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeApproval(worker, consume); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("consumed twice: %v", err)
	}
	action.Recipient = "bob"
	digest, _ = action.Digest()
	request.Action = action
	request.ActionDigest = digest
	pending, err := repo.RequestApproval(worker, lease, request)
	if err != nil || pending.ID == approval.ID {
		t.Fatalf("new recipient reused approval: %+v %v", pending, err)
	}
	now = now.Add(2 * time.Minute)
	// Caller-supplied historical timestamps cannot revive an expired request.
	_, err = repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: pending.ID, ActorID: owner, ActionDigest: digest, Status: store.ApprovalApproved, Now: now.Add(-2 * time.Minute)})
	if !errors.Is(err, store.ErrBotExpired) {
		t.Fatalf("expired approved: %v", err)
	}
	approvals, err := repo.ListApprovals(ctx, b.ID, store.BotPage{})
	if err != nil || len(approvals) != 2 {
		t.Fatalf("approval history: %+v %v", approvals, err)
	}
}

func TestBotCommitRunAtomicAndIdempotent(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			q := store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, worker, lease, "Checked the source; unchanged"), Usage: store.BotUsage{ToolCalls: 2, InputTokens: 12, OutputTokens: 8}}
			if err := repo.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, q); err != nil {
				t.Fatalf("exact retry: %v", err)
			}
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Status != store.RunSucceeded || saved.FinishedAt == nil || saved.Report.Summary != q.Report.Summary || saved.Usage.ToolCalls != 2 {
				t.Fatalf("saved result: %+v %v", saved, err)
			}
			events, err := repo.ListActivity(ctx, run.BotID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			reports := 0
			for _, event := range events {
				if event.Kind == store.BotActivityReport {
					reports++
				}
			}
			if reports != 1 {
				t.Fatalf("report count=%d", reports)
			}
			changed := q
			changed.Report.Summary = "different outcome"
			if err := repo.CommitRun(worker, changed); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("changed retry: %v", err)
			}
			forged := q
			forged.Lease.WorkerID = "other"
			if err := repo.CommitRun(worker, forged); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("other worker retry: %v", err)
			}
			if _, err := repo.AppendActivity(worker, lease, store.BotActivity{BotID: run.BotID, RunID: &run.ID, Kind: store.BotActivityRun, Payload: []byte(`{}`)}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("completed worker wrote: %v", err)
			}
			next, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: run.BotID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "next", Instructions: "Next check"})
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := execution.BotRunIdentityFromContext(worker)
			identity.RunID = next.ID
			nextLease, err := repo.ClaimRun(execution.WithBotRunIdentity(ctx, identity), store.BotClaimRequest{BotID: run.BotID, RunID: next.ID, WorkerID: "next-worker", ExpiresAt: lease.ExpiresAt})
			if err != nil || nextLease.Fence <= lease.Fence {
				t.Fatalf("next lease: %+v %v", nextLease, err)
			}
		})
	}
}

func botRunFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, context.Context, *store.BotRun, store.BotLease, *time.Time) {
	t.Helper()
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	var db botDraftDB
	var activate func(uuid.UUID) error
	if scope == store.BotScopeTeam {
		c := newTeamClient(t)
		db = teamBotDB{client: c}
		activate = func(id uuid.UUID) error { return c.Bot.UpdateOneID(id).SetStatus(bot.StatusActive).Exec(ctx) }
	} else {
		_, c := setupPersonalStore(t)
		db = personalBotDB{client: c}
		activate = func(id uuid.UUID) error { return c.Bot.UpdateOneID(id).SetStatus("active").Exec(ctx) }
	}
	repo := &botRepository{auth: auth, db: db, now: func() time.Time { return now }}
	v := store.DefaultBotVersion()
	v.Charter = "Observe source and report"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Completion", OwnerID: owner, Version: v, IdempotencyKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := activate(b.ID); err != nil {
		t.Fatal(err)
	}
	run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "initial", Instructions: "Observe"})
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "worker", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return repo, ctx, worker, run, lease, &now
}

type failBotResultDB struct{ botDraftDB }

func (d failBotResultDB) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	if a.Kind == store.BotActivityReport {
		return errors.New("injected result publication failure")
	}
	return d.botDraftDB.InsertActivity(ctx, a, key)
}

type failBotResultTransaction struct{ botDraftDB }

func (d failBotResultTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failBotResultDB{tx}) })
}

func TestBotCommitRollbackAndExpiredRecovery(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{"body":"review"}`)}
			digest, err := action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: 1, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Waiting for confirmation", Unverified: []string{"send outcome"}}, Checkpoint: []byte(`{"last_verified_step":1}`)}
			original := repo.db
			repo.db = failBotResultTransaction{original}
			if err := repo.CommitRun(worker, q); err == nil {
				t.Fatal("missing injected failure")
			}
			repo.db = original
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Status != store.RunRunning || saved.FinishedAt != nil {
				t.Fatalf("partial commit: %+v %v", saved, err)
			}
			root, err := repo.db.GetBot(ctx, run.BotID)
			if err != nil || root.ActiveRunID == nil || root.Fence != lease.Fence {
				t.Fatalf("lease released on rollback: %+v %v", root, err)
			}
			approvals, err := repo.ListApprovals(ctx, run.BotID, store.BotPage{})
			if err != nil || approvals[0].Status != store.ApprovalPending {
				t.Fatalf("approval revoked on rollback: %+v %v", approvals, err)
			}
			if err := repo.RecoverRun(ctx, lease); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("live run recovered: %v", err)
			}
			*now = now.Add(2 * time.Minute)
			if err := repo.CommitRun(worker, q); !errors.Is(err, store.ErrBotExpired) {
				t.Fatalf("expired worker committed: %v", err)
			}
			repo.db = failBotResultTransaction{original}
			if err := repo.RecoverRun(ctx, lease); err == nil {
				t.Fatal("recovery publication failure missing")
			}
			repo.db = original
			saved, err = repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Status != store.RunRunning {
				t.Fatalf("partial recovery: %+v %v", saved, err)
			}
			if err := repo.RecoverRun(ctx, lease); err != nil {
				t.Fatal(err)
			}
			if err := repo.RecoverRun(ctx, lease); err != nil {
				t.Fatalf("repeat recovery: %v", err)
			}
			saved, err = repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Status != store.RunInterrupted || saved.FinishedAt != nil || len(saved.Report.Unverified) == 0 {
				t.Fatalf("recovery outcome: %+v %v", saved, err)
			}
			a, err := repo.db.ReadApproval(ctx, run.BotID, approval.ID)
			if err != nil || a.Status != store.ApprovalRevoked {
				t.Fatalf("recovery grant not revoked: %+v %v", a, err)
			}
			if err := repo.CommitRun(worker, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale commit after recovery: %v", err)
			}
			if _, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "replacement", ExpiresAt: now.Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("unsafe auto resume: %v", err)
			}
			events, err := repo.ListActivity(ctx, run.BotID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			reports := 0
			for _, e := range events {
				if e.Kind == store.BotActivityReport {
					reports++
				}
			}
			if reports != 1 {
				t.Fatalf("recovery report count=%d", reports)
			}
		})
	}
}

func TestBotCheckpointWaitPreservesApprovals(t *testing.T) {
	repo, ctx, worker, run, lease, now := botRunFixture(t, store.BotScopePersonal)
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{}`)}
	digest, _ := action.Digest()
	a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: 1, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	work, err := repo.LoadWork(worker, lease)
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Report: store.BotReport{Summary: "Please review", Work: store.BuildBotWorkProjection(work), NeedsApproval: []uuid.UUID{a.ID}}, Checkpoint: []byte(`{"step":"send"}`)}
	if err := repo.CommitRun(worker, q); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetRun(ctx, run.BotID, run.ID)
	if err != nil || saved.Status != store.RunWaitingApproval || len(saved.Checkpoint) == 0 || saved.FinishedAt != nil {
		t.Fatalf("checkpoint: %+v %v", saved, err)
	}
	identity, _ := execution.BotRunIdentityFromContext(worker)
	actor, err := uuid.Parse(identity.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: a.ID, ActorID: actor, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: 1, ActionDigest: digest}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("checkpointed worker consumed: %v", err)
	}
}

func TestBotCommitRejectsFalseSuccessAndMalformedResult(t *testing.T) {
	repo, _, worker, _, lease, _ := botRunFixture(t, store.BotScopeTeam)
	cases := []store.BotRunCommit{
		{Lease: lease, Status: store.RunQueued, Report: store.BotReport{Summary: "queued"}},
		{Lease: lease, Status: store.RunSucceeded},
		// Caller-supplied accepted metadata is not persisted assessment evidence.
		{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Forged accepted answer", Quality: &store.BotQualityReport{Outcome: store.BotWorkAccepted}}},
		{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Done", Unverified: []string{"not checked"}}},
		{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Partial"}, Usage: store.BotUsage{InputTokens: -1}},
		{Lease: lease, Status: store.RunWaitingApproval, Report: store.BotReport{Summary: "Wait"}},
		{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Partial"}, Checkpoint: []byte(`broken`)},
	}
	for i, q := range cases {
		if err := repo.CommitRun(worker, q); !errors.Is(err, store.ErrBotInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

func TestBotConcurrentCompletionKeepsOneOutcome(t *testing.T) {
	repo, ctx, worker, run, lease, _ := botRunFixture(t, store.BotScopePersonal)
	report := acceptedWorkReport(t, repo, worker, lease, "reviewed outcome")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	// Both contenders use the exact reviewed candidate, but their accounting
	// differs. They are valid competing commits, not an unreviewed draft race.
	for _, inputTokens := range []int64{11, 12} {
		wg.Add(1)
		go func(inputTokens int64) {
			defer wg.Done()
			<-start
			results <- repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report, Usage: store.BotUsage{InputTokens: inputTokens}})
		}(inputTokens)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, store.ErrBotConflict) {
			t.Errorf("unexpected competing commit error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("conflicting completions succeeded: %d", wins)
	}
	saved, err := repo.GetRun(ctx, run.BotID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListActivity(ctx, run.BotID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind != store.BotActivityReport {
			continue
		}
		count++
		var payload struct {
			Report store.BotReport `json:"report"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Report.Summary != saved.Report.Summary {
			t.Fatalf("report and outcome diverge: %q / %q", payload.Report.Summary, saved.Report.Summary)
		}
	}
	if count != 1 {
		t.Fatalf("report count: %d", count)
	}
}

func TestBotLifecycleRetentionAndWorkerRevocation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Pause monitoring"}
			paused, err := repo.Pause(ctx, q)
			if err != nil || paused.Status != store.BotPaused {
				t.Fatalf("pause: %+v %v", paused, err)
			}
			if _, err := repo.Pause(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale pause: %v", err)
			}
			if _, err := repo.RenewLease(worker, lease, now.Add(2*time.Minute)); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("paused worker renewed: %v", err)
			}
			saved, err := repo.GetRun(ctx, b.ID, run.ID)
			if err != nil || saved.Status != store.RunInterrupted || saved.FinishedAt != nil {
				t.Fatalf("paused run: %+v %v", saved, err)
			}
			if _, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, Instructions: "again", IdempotencyKey: "again"}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("paused admission: %v", err)
			}
			q.ExpectedRevision = paused.Revision
			q.Reason = "Delete bot"
			deleted, err := repo.BeginDelete(ctx, q)
			if err != nil || deleted.Status != store.BotDeleted || deleted.RestoreUntil == nil || deleted.RestoreUntil.Sub(*now) != store.BotRetention {
				t.Fatalf("delete: %+v %v", deleted, err)
			}
			if deleted.PreviousStatus != store.BotPaused || deleted.HomeSessionID != b.HomeSessionID {
				t.Fatal("delete lost identity/history")
			}
			q.ExpectedRevision = deleted.Revision
			q.Now = now.Add(365 * 24 * time.Hour)
			restored, err := repo.Restore(ctx, q)
			if err != nil || restored.Status != store.BotPaused || restored.DeletedAt != nil || restored.RestoreUntil != nil {
				t.Fatalf("restore: %+v %v", restored, err)
			}
			root, err := repo.db.GetBot(ctx, b.ID)
			if err != nil || root.ActiveRunID == nil {
				t.Fatalf("unconfirmed compute teardown lost barrier: %+v %v", root, err)
			}
			q.ExpectedRevision = restored.Revision
			stopped, err := repo.Stop(ctx, q)
			if err != nil || stopped.Status != store.BotStopped {
				t.Fatalf("stop: %+v %v", stopped, err)
			}
			q.ExpectedRevision = stopped.Revision
			deleted, err = repo.BeginDelete(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			*now = *deleted.RestoreUntil
			q.ExpectedRevision = deleted.Revision
			q.Now = now.Add(-time.Hour)
			if _, err := repo.Restore(ctx, q); !errors.Is(err, store.ErrBotExpired) {
				t.Fatalf("expired restore: %v", err)
			}
		})
	}
}

type failBotLifecycleDB struct{ botDraftDB }

func (d failBotLifecycleDB) DisableBotSchedules(context.Context, uuid.UUID) error {
	return errors.New("injected scheduler failure")
}

type failBotLifecycleTransaction struct{ botDraftDB }

func (d failBotLifecycleTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failBotLifecycleDB{tx}) })
}

func TestBotLifecycleScheduleScopeAndRollback(t *testing.T) {
	repo, ctx, worker, run, lease, now := botRunFixture(t, store.BotScopeTeam)
	db := repo.db.(teamBotDB)
	c := db.client
	next := now.Add(time.Hour)
	owned, err := c.ScheduledJob.Create().SetName("owned").SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeBotTrigger).SetPayload(map[string]any{"bot_id": run.BotID.String()}).SetNextRunAt(next).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := c.ScheduledJob.Create().SetName("other bot").SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeBotTrigger).SetPayload(map[string]any{"bot_id": uuid.NewString()}).SetNextRunAt(next).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := c.ScheduledJob.Create().SetName("legacy adaptive").SetSchedule("0 9 * * *").SetMode(scheduledjob.ModeAdaptive).SetNextRunAt(next).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Pause"}
	repo.db = failBotLifecycleTransaction{db}
	if _, err := repo.Pause(ctx, q); err == nil {
		t.Fatal("expected scheduler failure")
	}
	repo.db = db
	root, err := repo.db.GetBot(ctx, b.ID)
	if err != nil || root.Bot.Status != store.BotActive || root.Fence != lease.Fence || root.Bot.Revision != b.Revision {
		t.Fatalf("partial pause: %+v %v", root, err)
	}
	if _, err := repo.RenewLease(worker, lease, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("rollback invalidated worker: %v", err)
	}
	paused, err := repo.Pause(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.ScheduledJob.Get(ctx, owned.ID)
	if err != nil || job.Status != scheduledjob.StatusPaused || job.NextRunAt != nil {
		t.Fatalf("owned schedule still active: %+v %v", job, err)
	}
	for _, id := range []uuid.UUID{foreign.ID, legacy.ID} {
		j, err := c.ScheduledJob.Get(ctx, id)
		if err != nil || j.Status != scheduledjob.StatusActive || j.NextRunAt == nil {
			t.Fatalf("unrelated schedule changed: %+v %v", j, err)
		}
	}
	q.ExpectedRevision = paused.Revision
	deleted, err := repo.BeginDelete(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	q.ExpectedRevision = deleted.Revision
	if _, err := repo.Restore(ctx, q); err != nil {
		t.Fatal(err)
	}
	job, err = c.ScheduledJob.Get(ctx, owned.ID)
	if err != nil || job.Status != scheduledjob.StatusPaused || job.NextRunAt != nil {
		t.Fatalf("restore enabled recurrence: %+v %v", job, err)
	}
}

func TestBotLifecyclePendingDecisionsAndOwnership(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{}`)}
			digest, _ := action.Digest()
			a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: 1, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			queued, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, Instructions: "Later", IdempotencyKey: "later"})
			if err != nil {
				t.Fatal(err)
			}
			strangerID := uuid.New()
			stranger, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: strangerID.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: strangerID, Reason: "unauthorized"}
			if _, err := repo.BeginDelete(stranger, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("non-owner deleted: %v", err)
			}
			if _, err := repo.Pause(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("spoofed actor: %v", err)
			}
			q.ActorID = b.OwnerID
			q.Reason = "Review work"
			paused, err := repo.Pause(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			cancelled, err := repo.GetRun(ctx, b.ID, queued.ID)
			if err != nil || cancelled.Status != store.RunCancelled {
				t.Fatalf("pending run not cancelled: %+v %v", cancelled, err)
			}
			pending, err := repo.db.ReadApproval(ctx, b.ID, a.ID)
			if err != nil || pending.Status != store.ApprovalPending {
				t.Fatalf("pause lost decision: %+v %v", pending, err)
			}
			if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: 1, ActionDigest: digest}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("paused approval executed: %v", err)
			}
			q.ExpectedRevision = paused.Revision
			if _, err := repo.Stop(ctx, q); err != nil {
				t.Fatal(err)
			}
			revoked, err := repo.db.ReadApproval(ctx, b.ID, a.ID)
			if err != nil || revoked.Status != store.ApprovalRevoked {
				t.Fatalf("stop retained grant: %+v %v", revoked, err)
			}
		})
	}
}

func TestBotLifecycleReportFailureRollsBackEverything(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{}`)}
			digest, _ := action.Digest()
			a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: 1, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.ListActivity(ctx, b.ID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			original := repo.db
			repo.db = failBotResultTransaction{original}
			if _, err := repo.BeginDelete(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Remove"}); err == nil {
				t.Fatal("missing result failure")
			}
			repo.db = original
			unchanged, err := repo.Get(ctx, b.ID)
			if err != nil || unchanged.Status != store.BotActive || unchanged.DeletedAt != nil || unchanged.Revision != b.Revision {
				t.Fatalf("delete partially committed: %+v %v", unchanged, err)
			}
			saved, err := repo.GetRun(ctx, b.ID, run.ID)
			if err != nil || saved.Status != store.RunRunning {
				t.Fatalf("run partially changed: %+v %v", saved, err)
			}
			pending, err := repo.db.ReadApproval(ctx, b.ID, a.ID)
			if err != nil || pending.Status != store.ApprovalPending {
				t.Fatalf("approval partially revoked: %+v %v", pending, err)
			}
			after, err := repo.ListActivity(ctx, b.ID, 0, 50)
			if err != nil || len(after) != len(before) {
				t.Fatalf("partial activity: %d / %d %v", len(after), len(before), err)
			}
			if _, err := repo.RenewLease(worker, lease, now.Add(2*time.Minute)); err != nil {
				t.Fatalf("lease lost on rollback: %v", err)
			}
		})
	}
}

func TestBotConcurrentLifecycleOnlyOneMutation(t *testing.T) {
	repo, ctx, _, run, _, _ := botRunFixture(t, store.BotScopePersonal)
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	req := store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Pause or delete"}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, remove := range []bool{false, true} {
		wg.Add(1)
		go func(remove bool) {
			defer wg.Done()
			<-start
			var err error
			if remove {
				_, err = repo.BeginDelete(ctx, req)
			} else {
				_, err = repo.Pause(ctx, req)
			}
			results <- err
		}(remove)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("lifecycle winners: %d", wins)
	}
	saved, err := repo.Get(ctx, b.ID)
	if err != nil || saved.Revision != b.Revision+1 {
		t.Fatalf("inconsistent revision: %+v %v", saved, err)
	}
	events, err := repo.ListActivity(ctx, b.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for _, e := range events {
		if e.Kind == store.BotActivityReport {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("duplicate interruption reports: %d", reports)
	}
}

func TestBotReadCursorMonotonicAndOwnerBound(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.ListActivity(ctx, b.ID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.MarkRead(ctx, store.BotReadCursor{BotID: b.ID, ViewerID: uuid.New(), Sequence: 1}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("forged viewer: %v", err)
			}
			if err := repo.MarkRead(ctx, store.BotReadCursor{BotID: b.ID, ViewerID: b.OwnerID, Sequence: 999}); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("future cursor: %v", err)
			}
			for _, seq := range []int64{2, 1, 2} {
				if err := repo.MarkRead(ctx, store.BotReadCursor{BotID: b.ID, ViewerID: b.OwnerID, Sequence: seq}); err != nil {
					t.Fatal(err)
				}
			}
			rec, err := repo.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(rec.Metadata["read_cursors"])
			if err != nil {
				t.Fatal(err)
			}
			var cursors map[string]string
			if err := json.Unmarshal(raw, &cursors); err != nil {
				t.Fatal(err)
			}
			if cursors[b.OwnerID.String()] != "2" {
				t.Fatalf("cursor regressed: %v", cursors)
			}
			if rec.Bot.Version != b.Version || rec.CreateHash == "" {
				t.Fatal("read changed version or erased metadata")
			}
			after, err := repo.ListActivity(ctx, b.ID, 0, 50)
			if err != nil || len(after) != len(before) {
				t.Fatalf("reading created activity: %d/%d %v", len(after), len(before), err)
			}
			q := store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: rec.Bot.Revision, ActorID: b.OwnerID, Reason: "Pause"}
			if _, err := repo.Pause(ctx, q); err != nil {
				t.Fatal(err)
			}
			rec, err = repo.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Metadata["read_cursors"] == nil {
				t.Fatal("lifecycle erased read cursor")
			}
		})
	}
}

func createBotSchemaFixture(t *testing.T, client *teament.Client) *teament.Bot {
	t.Helper()
	b, err := client.Bot.Create().SetOwnerID(uuid.New()).SetName("Fare watch").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBotSchemaDefaultsAndForeignKeys(t *testing.T) {
	c := newTeamClient(t)
	ctx := context.Background()
	b := createBotSchemaFixture(t, c)
	if b.Status != bot.StatusDraft || b.Revision != 1 || b.Version != 1 || b.Fence != 0 || b.ActiveRunID != nil || b.CleanupComplete {
		t.Fatalf("unsafe initial state: %+v", b)
	}
	createVersion := func(id uuid.UUID) error {
		_, err := c.BotVersion.Create().SetBotID(id).SetVersion(1).SetDefinition(map[string]any{"charter": "Observe only"}).SetContentHash("fixture").SetCreatedBy(b.OwnerID).Save(ctx)
		return err
	}
	if err := createVersion(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := createVersion(b.ID); err == nil {
		t.Fatal("duplicate bot version accepted")
	}
	if err := createVersion(uuid.New()); err == nil {
		t.Fatal("orphan version accepted")
	}
	if err := c.Bot.DeleteOneID(b.ID).Exec(ctx); err == nil {
		t.Fatal("bot with retained history hard-deleted")
	}
}

func TestBotSchemaRevisionAndLeaseCAS(t *testing.T) {
	c := newTeamClient(t)
	ctx := context.Background()
	b := createBotSchemaFixture(t, c)
	changed, err := c.Bot.Update().Where(bot.IDEQ(b.ID), bot.RevisionEQ(1)).SetStatus(bot.StatusActive).AddRevision(1).Save(ctx)
	if err != nil || changed != 1 {
		t.Fatalf("first revision: %d %v", changed, err)
	}
	changed, err = c.Bot.Update().Where(bot.IDEQ(b.ID), bot.RevisionEQ(1)).SetName("stale edit").AddRevision(1).Save(ctx)
	if err != nil || changed != 0 {
		t.Fatalf("stale edit: %d %v", changed, err)
	}
	now := time.Now().UTC()
	run := uuid.New()
	claim := func(id uuid.UUID) (int, error) {
		return c.Bot.Update().Where(bot.IDEQ(b.ID), bot.StatusEQ(bot.StatusActive), bot.ActiveRunIDIsNil(), bot.FenceEQ(0)).SetActiveRunID(id).SetLeaseExpiresAt(now.Add(time.Minute)).AddFence(1).Save(ctx)
	}
	if n, err := claim(run); err != nil || n != 1 {
		t.Fatalf("first lease: %d %v", n, err)
	}
	if n, err := claim(uuid.New()); err != nil || n != 0 {
		t.Fatalf("duplicate lease: %d %v", n, err)
	}
	// A stale worker must not satisfy the persisted fencing predicate.
	if n, err := c.Bot.Update().Where(bot.IDEQ(b.ID), bot.FenceEQ(0)).ClearActiveRunID().Save(ctx); err != nil || n != 0 {
		t.Fatalf("stale fence: %d %v", n, err)
	}
}

func TestBotSchemaAtomicDraftAndOutbox(t *testing.T) {
	c := newTeamClient(t)
	ctx := context.Background()
	id := uuid.New()
	tx, err := c.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tx.Bot.Create().SetID(id).SetOwnerID(uuid.New()).SetName("Draft").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, err = tx.BotOutbox.Create().SetBotID(id).SetSequence(1).SetKind("lifecycle").SetDeduplicationKey("create").SetPayload(map[string]any{"status": "draft"}).SetAvailableAt(time.Now()).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if exists, err := c.Bot.Query().Where(bot.IDEQ(b.ID)).Exist(ctx); err != nil || exists {
		t.Fatalf("rolled back bot visible: %v %v", exists, err)
	}
	if n, err := c.BotOutbox.Query().Count(ctx); err != nil || n != 0 {
		t.Fatalf("rolled back intent visible: %d %v", n, err)
	}
}

func TestBotSchemaRunAndApprovalUniqueness(t *testing.T) {
	c := newTeamClient(t)
	ctx := context.Background()
	b := createBotSchemaFixture(t, c)
	createRun := func() (*teament.BotRun, error) {
		return c.BotRun.Create().SetBotID(b.ID).SetVersion(1).SetSessionID(uuid.NewString()).SetIdempotencyKey("cron:occurrence").SetRequestHash("request").SetData(map[string]any{}).Save(ctx)
	}
	r, err := createRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createRun(); err == nil {
		t.Fatal("duplicate occurrence accepted")
	}
	if r.Status != botrun.StatusQueued {
		t.Fatalf("run starts %s", r.Status)
	}
	createApproval := func() (*teament.BotApproval, error) {
		return c.BotApproval.Create().SetBotID(b.ID).SetRunID(r.ID).SetVersion(1).SetActionDigest("exact-action").SetData(map[string]any{}).SetExpiresAt(time.Now().Add(time.Hour)).Save(ctx)
	}
	a, err := createApproval()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createApproval(); err == nil {
		t.Fatal("duplicate exact-action request accepted")
	}
	if a.Status != botapproval.StatusPending {
		t.Fatalf("approval starts %s", a.Status)
	}
	n, err := c.BotApproval.Update().Where(botapproval.IDEQ(a.ID), botapproval.StatusEQ(botapproval.StatusPending)).SetStatus(botapproval.StatusApproved).Save(ctx)
	if err != nil || n != 1 {
		t.Fatalf("decision: %d %v", n, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		n, err = c.BotApproval.Update().Where(botapproval.IDEQ(a.ID), botapproval.StatusEQ(botapproval.StatusApproved)).SetStatus(botapproval.StatusConsumed).Save(ctx)
		if err != nil || n != 1-attempt {
			t.Fatalf("consume %d: %d %v", attempt, n, err)
		}
	}
}

func TestBotSchemaSessionAndSchedulerCompatibility(t *testing.T) {
	c := newTeamClient(t)
	ctx := context.Background()
	s, err := c.Session.Create().SetID(uuid.NewString()).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Purpose != teamsession.PurposeChat || s.Retention != teamsession.RetentionStandard || s.BotID != nil || s.BotRunID != nil {
		t.Fatalf("ordinary session defaults changed: %+v", s)
	}
	for _, mode := range []scheduledjob.Mode{scheduledjob.ModeRoutine, scheduledjob.ModeAdaptive, scheduledjob.ModeFleetPoll, scheduledjob.ModeBotTrigger} {
		j, err := c.ScheduledJob.Create().SetName(string(mode)).SetSchedule("0 9 * * *").SetMode(mode).Save(ctx)
		if err != nil || j.Mode != mode {
			t.Fatalf("schedule %s: %v", mode, err)
		}
	}
}

func TestBotSchemaPersonalScope(t *testing.T) {
	_, c := setupPersonalStore(t)
	ctx := context.Background()
	b, err := c.Bot.Create().SetOwnerID(uuid.New()).SetName("Private").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.BotVersion.Create().SetBotID(b.ID).SetVersion(1).SetDefinition(map[string]any{}).SetContentHash("version").SetCreatedBy(b.OwnerID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BotGoal.Create().SetBotID(uuid.New()).SetTitle("Orphan").SetData(map[string]any{}).Save(ctx); err == nil {
		t.Fatal("orphan private goal accepted")
	}
}
