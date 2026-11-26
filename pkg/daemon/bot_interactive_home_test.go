package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	*regressionHomeBackend
	calls           int
	fail            bool
	before          func()
	recordStartup   bool
	startupOpens    map[string]int
	startupContexts []context.Context
	startupKeys     []string
	selectionOpens  map[string]int
	classifiedOpens int
	startupHomeCtx  context.Context
}

func (b *autoHomeBackend) OpenBotContent(ctx context.Context, home *sandbox.BotHomeBinding, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	// The immutable-read cache holds its mutex while calling this backend.
	// Inspecting operation state here re-enters that mutex and deadlocks. Record
	// observations only; inspect lifetime at the safe setup/runtime boundaries.
	if b.recordStartup {
		if b.startupOpens == nil {
			b.startupOpens = make(map[string]int)
		}
		b.startupOpens[ref.Key]++
		b.startupKeys = append(b.startupKeys, ref.Key)
		b.startupContexts = append(b.startupContexts, ctx)
	}
	return b.regressionHomeBackend.OpenBotContent(ctx, home, ref, r)
}

func (b *autoHomeBackend) StatBotHomeFile(ctx context.Context, home *sandbox.BotHomeBinding, path string) (*store.BotFileEntry, error) {
	// Mutable document initialization runs after immutable Home selection has
	// returned (and released its cache mutex), while startup is still active.
	// Selection owns a separate read operation; count its closed contexts apart
	// from the surrounding startup operation rather than allowing duplicate opens.
	if b.recordStartup {
		for i := b.classifiedOpens; i < len(b.startupContexts); i++ {
			if !startupReadsActive(b.startupContexts[i]) {
				key := b.startupKeys[i]
				b.startupOpens[key]--
				if b.startupOpens[key] == 0 {
					delete(b.startupOpens, key)
				}
				if b.selectionOpens == nil {
					b.selectionOpens = make(map[string]int)
				}
				b.selectionOpens[key]++
			}
		}
		b.classifiedOpens = len(b.startupContexts)
	}
	return b.regressionHomeBackend.StatBotHomeFile(ctx, home, path)
}

func (b *autoHomeBackend) EnsureBotHome(ctx context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	b.startupHomeCtx = ctx
	b.recordStartup = startupReadsActive(ctx)
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
	t       *testing.T
	repo    store.BotStore
	backend *autoHomeBackend
	calls   int
}

func (*autoHomeRuntime) Cancel(context.Context, store.BotLease) error { return nil }

func (r *autoHomeRuntime) Start(ctx context.Context, q bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	r.calls++
	r.backend.recordStartup = false
	if startupReadsActive(ctx) || startupReadsActive(r.backend.startupHomeCtx) {
		r.t.Fatal("startup operation crossed runtime boundary")
	}
	for _, retained := range r.backend.startupContexts {
		if startupReadsActive(retained) {
			r.t.Fatal("startup backend read operation crossed runtime boundary")
		}
	}
	if q.Bot.Home.ID == uuid.Nil {
		return bot.RunResult{}, errors.New("runtime without home")
	}
	return bot.RunResult{Status: store.RunSucceeded, Report: reviewedRecoveryReply(r.t, r.repo, ctx, q.Run.Lease, "Home ready")}, nil
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
			backend := &autoHomeBackend{regressionHomeBackend: newRegressionHomeBackend(t)}
			if err := db.InstallBotContentHomeBackend(backend); err != nil {
				t.Fatal(err)
			}
			runtime := &autoHomeRuntime{t: t, repo: ods.ForUser(owner).Bots(), backend: backend}
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
			if mode != "title" {
				body = `{"name":"Research title","idempotency_key":"once"}`
			}
			var created struct {
				store.Bot
				InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
			}
			if err = json.Unmarshal(request("/api/studio/bots?scope=personal", body), &created); err != nil {
				t.Fatal(err)
			}
			if mode != "title" {
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
			// Viewing an admitted turn must neither provision Home nor claim/run it.
			for _, path := range []string{"/api/studio/bots?scope=personal", "/api/studio/bots/" + created.ID.String() + "?scope=personal"} {
				resp, err := server.Client().Get(server.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				raw, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("view HTTP %d: %s (%v)", resp.StatusCode, raw, readErr)
				}
			}
			viewed, err := repo.Get(caller, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			viewedRuns, err := repo.ListRuns(caller, created.ID, store.BotPage{Limit: 10})
			if err != nil || len(viewedRuns) != 1 || viewedRuns[0].Status != store.RunQueued || viewedRuns[0].Lease.Fence != 0 || viewed.Home.ID != uuid.Nil || viewed.HomeSessionID != created.HomeSessionID || backend.calls != 0 || runtime.calls != 0 {
				t.Fatalf("view changed lifecycle: bot=%+v runs=%+v err=%v", viewed, viewedRuns, err)
			}
			runID := created.InitialTurn.Run.ID
			ownerCtx, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "title" {
				// Clarification needs live authorization, but must not prepare a
				// physical Home. The factory journey exercises its actual question.
				if err := worker.prepareInteractiveHome(ownerCtx, repo, created.ID, runID); err != nil {
					t.Fatal(err)
				}
				run, err := repo.GetRun(caller, created.ID, runID)
				if err != nil {
					t.Fatal(err)
				}
				current, err := repo.Get(caller, created.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.Home.ID != uuid.Nil || backend.calls != 0 || runtime.calls != 0 || run.Status != store.RunQueued || run.Lease.Fence != 0 || run.TitleConfirmation == nil || run.TitleConfirmation.State != store.BotTitleConfirmationNeedsQuestion {
					t.Fatalf("pending title provisioned Home: bot=%+v run=%+v calls=%d", current, run, backend.calls)
				}
				registry.Cancel(execution.BotRunIdentity{BotID: created.ID, RunID: runID, Version: 1, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
				if err := worker.prepareInteractiveHome(ownerCtx, repo, created.ID, runID); err == nil {
					t.Fatal("pending title bypassed revoked authority")
				}
				return
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
			if mode == "message" {
				if len(backend.startupOpens) == 0 {
					t.Fatal("startup never carried immutable-read scope to Home")
				}
				for key, count := range backend.selectionOpens {
					if count != 1 {
						t.Fatalf("Home selection reopened immutable %s %d times", key, count)
					}
				}
				if len(backend.selectionOpens) == 0 {
					t.Fatal("Home selection did not verify retained content")
				}
				for key, count := range backend.startupOpens {
					if count != 1 {
						t.Fatalf("startup reopened immutable %s %d times", key, count)
					}
				}
				for _, retained := range backend.startupContexts {
					if startupReadsActive(retained) {
						t.Fatal("completed startup retained read operation")
					}
				}
				t.Logf("startup immutable reads: %d distinct objects, exactly one open each", len(backend.startupOpens))
			}
			current, err := repo.Get(caller, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Home.ID == uuid.Nil || current.HomeSessionID != created.HomeSessionID || current.Status != store.BotActive || current.Version != 1 || current.Revision != 2 || runtime.calls != 1 {
				t.Fatalf("not safely provisioned: %+v calls=%d", current, runtime.calls)
			}
			retained := backend.documentsRoot
			for _, name := range []string{"SOUL.md", "IDENTITY.md", "USER.md", "CHARTER.md", "MEMORY.md"} {
				if _, err := os.Stat(filepath.Join(retained, created.ID.String(), name)); err != nil {
					t.Fatal(err)
				}
			}
			// A selected version cannot silently fall back to the preserved stage.
			backend.documentsRoot = t.TempDir()
			if _, err := repo.GetVersion(caller, created.ID, 1); err == nil {
				t.Fatal("version not selected into retained Home")
			}
			backend.documentsRoot = retained
			runs, err := repo.ListRuns(caller, created.ID, store.BotPage{Limit: 10})
			if err != nil || len(runs) != 1 || runs[0].Status != store.RunSucceeded {
				t.Fatalf("runs: %+v %v", runs, err)
			}
		})
	}
}
