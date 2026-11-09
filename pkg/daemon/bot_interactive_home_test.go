package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type autoHomeBackend struct {
	sandbox.Backend
	calls  int
	fail   bool
	before func()
}

func (b *autoHomeBackend) EnsureBotHome(_ context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	b.calls++
	if b.before != nil {
		b.before()
	}
	if b.fail {
		return nil, errors.New("storage offline")
	}
	return &sandbox.BotHomeBinding{Scope: s, Backend: "controlled", Ref: "home-" + s.BotID}, nil
}

type autoHomeRuntime struct {
	bot.Runtime
	calls int
}

func (r *autoHomeRuntime) Start(_ context.Context, q bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	r.calls++
	if q.Bot.Home.ID == uuid.Nil {
		return bot.RunResult{}, errors.New("runtime without home")
	}
	return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Home ready"}}, nil
}

func TestBotInteractiveAutoHomeHTTP(t *testing.T) {
	for _, mode := range []string{"title", "message", "failure-retry", "revoked", "cas", "restart", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "auto-home", Name: "Auto", Status: "active"}
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
			owner := uuid.NewString()
			if err = db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			backend := &autoHomeBackend{}
			runtime := &autoHomeRuntime{}
			worker := &BotWorker{config: BotWorkerConfig{Backend: db, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "auto-worker", TurnRegistry: registry}, runtime: runtime}
			svc.BotHomeSetup = worker.provisionHome
			svc.BotInteractiveReady = true
			svc.BotTurnRegistry = registry
			caller, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			caller, err = execution.WithAuthenticationEvidence(caller, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			caller = store.WithTenantContext(caller, &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: owner})
			router := mux.NewRouter()
			api.RegisterRoutes(router, svc, db, entstore.TenantMiddleware(db))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(caller)) }))
			defer server.Close()
			request := func(path, body string) []byte {
				t.Helper()
				resp, e := server.Client().Post(server.URL+path, "application/json", strings.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != 201 && resp.StatusCode != 202 {
					t.Fatalf("HTTP %d: %s", resp.StatusCode, raw)
				}
				return raw
			}
			body := `{"name":"Research title","idempotency_key":"once","start_from_title":true}`
			if mode == "message" {
				body = `{"name":"Research title","idempotency_key":"once"}`
			}
			var created struct {
				store.Bot
				InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
			}
			if err = json.Unmarshal(request("/api/studio/bots?scope=personal", body), &created); err != nil {
				t.Fatal(err)
			}
			if mode == "message" {
				var receipt store.BotMessageReceipt
				if err = json.Unmarshal(request("/api/studio/bots/"+created.ID.String()+"/messages?scope=personal", `{"version":1,"instructions":"Research title","idempotency_key":"message"}`), &receipt); err != nil {
					t.Fatal(err)
				}
				created.InitialTurn = &receipt
			}
			if created.InitialTurn == nil || created.Home.ID != uuid.Nil || backend.calls != 0 {
				t.Fatalf("admission must queue without external setup: %+v calls=%d", created, backend.calls)
			}
			repo := ods.ForUser(owner).Bots()
			ss := ods.ForUser(owner).Sessions()
			runID := created.InitialTurn.Run.ID
			ownerCtx, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner})
			if err != nil {
				t.Fatal(err)
			}
			identity := execution.BotRunIdentity{BotID: created.ID, RunID: runID, Version: 1, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug}
			switch mode {
			case "failure-retry":
				backend.fail = true
			case "revoked":
				backend.before = func() { registry.Cancel(identity) }
			case "cas":
				backend.before = func() {
					backend.before = nil
					v := store.DefaultBotVersion()
					v.BotID = created.ID
					v.Charter = "Changed"
					v.ChangeReason = "Concurrent edit"
					_, e := repo.ApplyVersion(ownerCtx, store.BotVersionRequest{BotID: created.ID, ExpectedRevision: 1, Version: v})
					if e != nil {
						t.Error(e)
					}
				}
			case "restart":
				worker.config.TurnRegistry = execution.NewBotTurnAuthorizationRegistry(32)
			case "cancelled":
				var cancel context.CancelFunc
				ownerCtx, cancel = context.WithCancel(ownerCtx)
				cancel()
			}
			err = worker.execute(ownerCtx, repo, ss, created.ID, runID)
			if mode == "failure-retry" || mode == "revoked" || mode == "cas" || mode == "restart" || mode == "cancelled" {
				if err == nil {
					t.Fatal("unsafe preflight succeeded")
				}
				current, e := repo.Get(caller, created.ID)
				if e != nil {
					t.Fatal(e)
				}
				run, e := repo.GetRun(caller, created.ID, runID)
				if e != nil {
					t.Fatal(e)
				}
				if current.Home.ID != uuid.Nil || run.Status != store.RunQueued || run.Lease.Fence != 0 || runtime.calls != 0 {
					t.Fatalf("failed preflight leaked execution: %+v %+v", current, run)
				}
				if mode == "failure-retry" {
					var prerequisite *BotHomePrerequisiteError
					if !errors.As(err, &prerequisite) {
						t.Fatalf("not typed prerequisite: %v", err)
					}
				}
				if mode != "failure-retry" {
					return
				}
				backend.fail = false
				err = worker.execute(ownerCtx, repo, ss, created.ID, runID)
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := repo.Get(caller, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Home.ID == uuid.Nil || current.Status != store.BotDraft || current.Version != 1 || current.Revision != 2 || runtime.calls != 1 {
				t.Fatalf("not safely provisioned: %+v calls=%d", current, runtime.calls)
			}
			runs, err := repo.ListRuns(caller, created.ID, store.BotPage{Limit: 10})
			if err != nil || len(runs) != 1 || runs[0].Status != store.RunSucceeded {
				t.Fatalf("runs: %+v %v", runs, err)
			}
		})
	}
}
