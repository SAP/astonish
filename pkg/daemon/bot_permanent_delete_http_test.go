package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// A physical temporary-directory backend, not Docker/Kubernetes acceptance.
type permanentHomeFixture struct {
	sandbox.Backend
	root string
	fail bool
}

func (s *permanentHomeFixture) EnsureBotHome(_ context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	ref := filepath.Join(s.root, scope.BotID)
	if err := os.MkdirAll(ref, 0700); err != nil {
		return nil, err
	}
	return &sandbox.BotHomeBinding{Scope: scope, Backend: "controlled", Ref: ref}, nil
}
func (s *permanentHomeFixture) DeleteBotHome(ctx context.Context, b *sandbox.BotHomeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.fail {
		return errors.New("injected physical deletion failure")
	}
	if b.Ref != filepath.Join(s.root, b.Scope.BotID) {
		return store.ErrBotForbidden
	}
	if err := os.RemoveAll(b.Ref); err != nil {
		return err
	}
	if _, err := os.Stat(b.Ref); !os.IsNotExist(err) {
		return errors.New("physical home remains")
	}
	return nil
}

// Inject a SQL-purge outage only after the real store has persisted physical
// verification. A fresh worker must consume that receipt without the backend.
type permanentPurgeOutage struct {
	store.BotStore
	store.BotHomeCleanupStore
	store.BotCleanupStore
	store.BotPermanentDeleteStore
	verified bool
}

func (s *permanentPurgeOutage) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	if err := s.BotCleanupStore.ConfirmCleanup(ctx, q); err != nil {
		return err
	}
	s.verified = true
	return nil
}
func (s *permanentPurgeOutage) PurgeDeleted(ctx context.Context, id store.BotID, rev int64) error {
	if s.verified {
		return errors.New("injected SQL purge outage")
	}
	return s.BotPermanentDeleteStore.PurgeDeleted(ctx, id, rev)
}

func TestBotPermanentDeleteHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "delete-test", Name: "Delete", Status: "active"}
	if err = db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
	if err = ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, reader := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, reader} {
		if err = db.Users().Create(ctx, &store.User{ID: id, Email: id + "@test.invalid", DisplayName: "Test", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err = db.Organizations().AddMember(ctx, id, org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		role := "member"
		if id == reader {
			role = "viewer"
		}
		if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: team.ID, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	principal := func(c context.Context, id string) context.Context {
		c, e := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
	backend := &permanentHomeFixture{root: filepath.Join(dir, "homes")}
	worker := &BotWorker{config: BotWorkerConfig{Backend: db, Sandbox: backend, WorkerID: "permanent-test"}}
	svc.BotHomeSetup = worker.provisionHome
	router := mux.NewRouter()
	api.RegisterRoutes(router, svc, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Test-Actor")
		c := principal(r.Context(), id)
		c = store.WithTenantContext(c, &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: id})
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	request := func(method, path, actor, body string, status int) store.Bot {
		t.Helper()
		r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		r.Header.Set("X-Test-Actor", actor)
		r.Header.Set("Content-Type", "application/json")
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, status, raw)
		}
		var b store.Bot
		if status == 200 || status == 201 {
			if e = json.Unmarshal(raw, &b); e != nil {
				t.Fatal(e)
			}
		}
		return b
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			create := func(key string) store.Bot {
				return request("POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Disposable","idempotency_key":%q,"definition":{"charter":"Stay idle"}}`, scope+key), 201)
			}
			b := create("target")
			other := create("other")
			other = request("POST", "/api/studio/bots/"+other.ID.String()+"/home"+suffix, owner, `{"expected_revision":1}`, 200)
			otherArtifact := filepath.Join(other.Home.StorageKey, "keep.txt")
			if err = os.WriteFile(otherArtifact, []byte("keep unrelated artifact"), 0600); err != nil {
				t.Fatal(err)
			}
			repo := ods.ForTeam(team.Slug).Bots()
			if scope == "personal" {
				repo = ods.ForUser(owner).Bots()
			}
			ownerCtx := principal(ctx, owner)
			path := "/api/studio/bots/" + b.ID.String()
			request("POST", path+"/permanent-delete"+suffix, owner, `{"expected_revision":1,"confirm":false}`, 400)
			request("POST", path+"/permanent-delete"+suffix, owner, `{"expected_revision":1,"confirm":true,"reason":"not allowed"}`, 400)
			denied := 403
			if scope == "personal" {
				denied = 404
			}
			request("POST", path+"/permanent-delete"+suffix, reader, `{"expected_revision":1,"confirm":true}`, denied)
			b = request("POST", path+"/home"+suffix, owner, `{"expected_revision":1}`, 200)
			artifact := filepath.Join(b.Home.StorageKey, "output.txt")
			if err = os.WriteFile(artifact, []byte("bot-owned artifact"), 0600); err != nil {
				t.Fatal(err)
			}
			source, e := repo.RequestRun(ownerCtx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentDraftConversation, Trigger: store.TriggerManual, Instructions: "temporary run", IdempotencyKey: "delete-source"})
			if e != nil {
				t.Fatal(e)
			}
			body := fmt.Sprintf(`{"expected_revision":%d,"confirm":true}`, b.Revision)
			request("POST", path+"/permanent-delete"+suffix, owner, `{"expected_revision":999,"confirm":true}`, 409)
			request("POST", path+"/permanent-delete"+suffix, owner, body, 202)
			deleted := request("GET", path+suffix, owner, "", 200)
			request("POST", path+"/restore"+suffix, owner, fmt.Sprintf(`{"expected_revision":%d,"reason":"restore"}`, deleted.Revision), 409)
			backend.fail = true
			if e := worker.reconcileRetention(ownerCtx, repo, deleted); e == nil {
				t.Fatal("expected physical failure")
			}
			request("POST", path+"/permanent-delete"+suffix, owner, body, 202)
			if _, e := os.Stat(artifact); e != nil {
				t.Fatal("failed cleanup lost artifact", e)
			}
			backend.fail = false
			outage := &permanentPurgeOutage{BotStore: repo, BotHomeCleanupStore: repo.(store.BotHomeCleanupStore), BotCleanupStore: repo.(store.BotCleanupStore), BotPermanentDeleteStore: repo.(store.BotPermanentDeleteStore)}
			if e := worker.reconcileRetention(ownerCtx, outage, deleted); e == nil || !outage.verified {
				t.Fatal("expected verified purge outage", e)
			}
			if _, e := repo.Get(ownerCtx, b.ID); e != nil {
				t.Fatal("retry root lost", e)
			}
			restarted := &BotWorker{config: worker.config}
			// A verified retry no longer needs the physical backend at all.
			restarted.config.Sandbox = nil
			// No further browser request: normal worker enumeration consumes intent.
			if e := restarted.pollStore(ownerCtx, repo, nil); e != nil {
				t.Fatal(e)
			}
			if _, e := os.Stat(b.Home.StorageKey); !os.IsNotExist(e) {
				t.Fatal("home remains", e)
			}
			request("POST", path+"/permanent-delete"+suffix, owner, body, 404)
			request("GET", path+suffix, owner, "", 404)
			if _, e := repo.GetRun(ownerCtx, b.ID, source.ID); !errors.Is(e, store.ErrBotNotFound) {
				t.Fatal("run retained", e)
			}
			if contents, e := os.ReadFile(otherArtifact); e != nil || string(contents) != "keep unrelated artifact" {
				t.Fatal("unrelated artifact changed", e)
			}
			request("GET", "/api/studio/bots/"+other.ID.String()+suffix, owner, "", 200)
			request("POST", path+"/permanent-delete"+suffix, owner, body, 404)
			// Never-provisioned drafts complete synchronously without allocating storage.
			draft := create("draft")
			request("POST", "/api/studio/bots/"+draft.ID.String()+"/permanent-delete"+suffix, owner, `{"expected_revision":1,"confirm":true}`, 204)
		})
	}
}
