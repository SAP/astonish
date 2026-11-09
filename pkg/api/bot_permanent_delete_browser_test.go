package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	appLauncher "github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
)

// Controlled storage adapter: real disposable Docker volumes, not mocked deletion.
// It refuses any volume that lacks this invocation's exact scope and ownership labels.
type deleteBrowserDocker struct {
	sandbox.Backend
	token string
}

func (d *deleteBrowserDocker) command(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %v: %w: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
func (d *deleteBrowserDocker) EnsureBotHome(ctx context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	name := "astonish-delete-proof-" + d.token + "-" + s.Key()[:16]
	_, err := d.command(ctx, "volume", "create", "--label", "astonish.delete-proof="+d.token, "--label", "astonish.scope="+s.Key(), name)
	return &sandbox.BotHomeBinding{Scope: s, Backend: "delete-proof-docker", Ref: name}, err
}
func (d *deleteBrowserDocker) DeleteBotHome(ctx context.Context, b *sandbox.BotHomeBinding) error {
	expected := "astonish-delete-proof-" + d.token + "-" + b.Scope.Key()[:16]
	if b.Ref != expected {
		return store.ErrBotForbidden
	}
	raw, err := d.command(ctx, "volume", "inspect", b.Ref)
	if err != nil {
		return err
	}
	var v []struct{ Labels map[string]string }
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return err
	}
	if len(v) != 1 || v[0].Labels["astonish.delete-proof"] != d.token || v[0].Labels["astonish.scope"] != b.Scope.Key() {
		return store.ErrBotForbidden
	}
	if _, err = d.command(ctx, "volume", "rm", b.Ref); err != nil {
		return err
	}
	// List is a successful independent absence check, not treating arbitrary inspect errors as absence.
	remaining, err := d.command(ctx, "volume", "ls", "--filter", "name=^"+b.Ref+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if remaining != "" {
		return errors.New("home remains")
	}
	return nil
}

type deleteBrowserPool struct{ sandbox.ToolNodePool }

func (*deleteBrowserPool) SetBotHome(string, *sandbox.BotHomeBinding) error { return nil }

// Opt in after building web. Never connects to the owner's daemon/database and
// never pulls/removes images; alpine:latest must already be locally available.
func TestBotPermanentDeleteBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_DELETE_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_BOT_DELETE_BROWSER_ACCEPTANCE=1 after web build")
	}
	ctx := context.Background()
	dir := t.TempDir()
	backend := &deleteBrowserDocker{token: uuid.NewString()[:8]}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := backend.command(ctx, "image", "inspect", "alpine:latest")
	must(err)
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	must(err)
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "delete-proof", Name: "Delete proof", Status: "active"}
	must(db.Organizations().Create(ctx, org))
	must(db.ProvisionOrg(ctx, org.ID, org.Slug))
	ods, err := db.ForOrg(org.Slug)
	must(err)
	team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
	must(ods.Teams().CreateTeam(ctx, team))
	owner := uuid.NewString()
	must(db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@test.invalid", DisplayName: "Disposable owner", Status: "active"}))
	must(db.Organizations().AddMember(ctx, owner, org.ID, "member"))
	must(ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}))
	principal := func(c context.Context) context.Context {
		p, e := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		must(e)
		return p
	}
	ownerCtx := principal(ctx)
	repo := ods.ForUser(owner).Bots()
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: backend, Pool: &deleteBrowserPool{}, WorkerID: "delete-browser", ContextWindow: 4096, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 512}, Prepare: func(context.Context, bot.RunRequest, *appLauncher.BotChatScope) (*appLauncher.BotChatSafeResources, error) {
		return nil, store.ErrBotUnsupported
	}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return nil, store.ErrBotUnsupported
	}})
	must(err)
	// No provider is invoked: this queued fixture run only exercises cancellation/purge.
	db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
	var posts atomic.Int32
	var accepted atomic.Int32
	router := mux.NewRouter()
	api.RegisterRoutes(router, svc, db, entstore.TenantMiddleware(db))
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile("../../web/package.json")
	must(err)
	must(json.Unmarshal(raw, &pkg))
	dist, err := filepath.Abs("../../web/dist/" + pkg.Version)
	must(err)
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			data = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			data = map[string]any{"user": map[string]any{"id": owner, "email": "delete@test.invalid", "display_name": "Delete proof", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": org.Slug, "name": "Delete proof"}, "team": team.Slug}
		case "/api/orgs":
			data = map[string]any{"orgs": []any{}, "active_org": org.Slug}
		case "/api/teams":
			data = []any{map[string]any{"slug": team.Slug, "name": "General"}}
		case "/api/settings/status":
			data = map[string]any{"setupRequired": false}
		case "/api/version":
			data = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			data = []any{}
		case "/api/settings/config":
			data = map[string]any{}
		case "/api/settings/providers/effective":
			data = map[string]any{"providers": map[string]any{}}
		}
		if data != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(data)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := store.WithTenantContext(principal(r.Context()), &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: owner})
		if strings.HasSuffix(r.URL.Path, "/permanent-delete") {
			posts.Add(1)
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "reason") {
				http.Error(w, "reason forbidden", 400)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r.WithContext(c))
			if rr.Code == 202 {
				accepted.Add(1)
			}
			for k, v := range rr.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rr.Code)
			_, _ = w.Write(rr.Body.Bytes())
			return
		}
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	request := func(method, path, body string, status int) store.Bot {
		t.Helper()
		r, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		must(e)
		r.Header.Set("Content-Type", "application/json")
		resp, e := server.Client().Do(r)
		must(e)
		defer resp.Body.Close()
		data, e := io.ReadAll(resp.Body)
		must(e)
		if resp.StatusCode != status {
			t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, data)
		}
		var b store.Bot
		if status == 201 || status == 200 {
			must(json.Unmarshal(data, &b))
		}
		return b
	}
	create := func(name string) store.Bot {
		return request("POST", "/api/studio/bots?scope=personal", fmt.Sprintf(`{"name":%q,"idempotency_key":%q,"definition":{"charter":"Remain idle"}}`, name, uuid.NewString()), 201)
	}
	target, other := create("Disposable delete target"), create("Untouched other Bot")
	provision := func(b store.Bot) store.Bot {
		s := sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner, BotID: b.ID.String()}
		binding, e := backend.EnsureBotHome(ctx, s)
		must(e)
		t.Cleanup(func() { _ = backend.DeleteBotHome(context.Background(), binding) })
		_, e = backend.command(ctx, "run", "--rm", "--pull=never", "--network=none", "--label", "astonish.delete-proof="+backend.token, "-v", binding.Ref+":/home/astonish", "alpine:latest", "sh", "-c", "printf 'owned candidate proof' > /home/astonish/candidate.txt")
		must(e)
		service, e := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, ClientID: "fixture-registry", OrgSlug: org.Slug, TeamSlug: team.Slug, Scopes: []string{entstore.BotHomeRegistrationScope}, Authenticated: true})
		must(e)
		q := entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: b.Scope, Backend: string(binding.Backend), StorageKey: binding.Ref, Revision: 1}}
		must(entstore.RegisterBotHomeBinding(service, repo, q, func(_ context.Context, got entstore.BotHomeRegistration) error {
			if got != q {
				return store.ErrBotConflict
			}
			_, e := backend.command(ctx, "volume", "inspect", got.Home.StorageKey)
			return e
		}))
		got, e := repo.Get(ownerCtx, b.ID)
		must(e)
		return *got
	}
	target, other = provision(target), provision(other)
	_, err = repo.RequestRun(ownerCtx, store.BotRunRequest{BotID: target.ID, Version: target.Version, Intent: store.BotIntentDraftConversation, Trigger: store.TriggerManual, Instructions: "disposable queued run", IdempotencyKey: "delete-proof-run"})
	must(err)
	proof, err := backend.command(ctx, "run", "--rm", "--pull=never", "--network=none", "--label", "astonish.delete-proof="+backend.token, "-v", target.Home.StorageKey+":/home/astonish:ro", "alpine:latest", "cat", "/home/astonish/candidate.txt")
	must(err)
	if proof != "owned candidate proof" {
		t.Fatal("target candidate missing before deletion")
	}
	sqlDB, err := sql.Open("sqlite", filepath.Join(dir, "orgs", org.Slug, "personal", owner+".db"))
	must(err)
	defer sqlDB.Close()
	execSQL := func(q string, args ...any) { t.Helper(); _, e := sqlDB.ExecContext(ctx, q, args...); must(e) }
	child, grandchild := uuid.NewString(), uuid.NewString()
	execSQL("INSERT INTO sessions(id,parent_id) VALUES (?,?)", child, target.HomeSessionID.String())
	execSQL("INSERT INTO sessions(id,parent_id) VALUES (?,?)", grandchild, child)
	for _, sid := range []string{target.HomeSessionID.String(), child, grandchild, other.HomeSessionID.String()} {
		execSQL("INSERT INTO session_events(session_id,event_data) VALUES (?,?)", sid, `{"content":"owned history"}`)
		execSQL("INSERT INTO memories(id,chunk_text,session_id) VALUES (?,?,?)", uuid.NewString(), "delete-proof-needle", sid)
	}
	cred := uuid.NewString()
	execSQL("INSERT INTO credentials(id,name,cred_type,encrypted) VALUES (?,?,?,?)", cred, "Shared keep", "api_key", []byte("unchanged-secret"))
	bin := os.Getenv("ASTONISH_MEMORY_BROWSER_PATH")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Fatal("set ASTONISH_MEMORY_BROWSER_PATH")
		}
	}
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	must(err)
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage(server.URL + "/#/chat").Timeout(45 * time.Second)
	page = page.WithPanic(func(v any) {
		text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
		t.Fatalf("browser: %v\n%v", v, text)
	})
	trash := `[aria-label="Delete Disposable delete target"]`
	page.MustElement(trash).MustHover()
	page.MustElement(trash + " svg.lucide-trash-2")
	must(os.MkdirAll("../../.tmp/hard-delete-browser-artifacts", 0755))
	shot, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	must(err)
	must(os.WriteFile("../../.tmp/hard-delete-browser-artifacts/sidebar-trash.png", shot, 0600))
	page.MustElement(trash).MustClick()
	dialog := page.MustElement(`[role="dialog"]`)
	if dialog.MustEval(`() => this.querySelectorAll('input,textarea').length`).Int() != 0 {
		t.Fatal("reason field present")
	}
	page.MustElementR(`[role="dialog"] button`, "^Cancel$").MustClick()
	if posts.Load() != 0 {
		t.Fatal("cancel sent deletion")
	}
	_, err = repo.Get(ownerCtx, target.ID)
	must(err)
	page.MustElement(trash).MustClick()
	page.MustElementR(`[role="dialog"] button`, "^Delete$").MustClick()
	page.MustElementR(`[role="status"]`, "Backend cleanup is in progress").MustVisible()
	if accepted.Load() != 1 {
		t.Fatal("missing real HTTP 202")
	}
	// Close observation: the real daemon worker, not a browser retry, must finish.
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	must(worker.Poll(ctx))
	request("GET", "/api/studio/bots/"+target.ID.String()+"?scope=personal", "", 404)
	remaining, err := backend.command(ctx, "volume", "ls", "--filter", "name=^"+target.Home.StorageKey+"$", "--format", "{{.Name}}")
	must(err)
	if remaining != "" {
		t.Fatal("deleted volume remains")
	}
	for table, query := range map[string]string{
		"bots":          "SELECT count(*) FROM bots WHERE id=?",
		"bot_versions":  "SELECT count(*) FROM bot_versions WHERE bot_id=?",
		"bot_runs":      "SELECT count(*) FROM bot_runs WHERE bot_id=?",
		"bot_approvals": "SELECT count(*) FROM bot_approvals WHERE bot_id=?",
		"bot_goals":     "SELECT count(*) FROM bot_goals WHERE bot_id=?",
		"bot_outbox":    "SELECT count(*) FROM bot_outbox WHERE bot_id=?",
		"sessions":      "SELECT count(*) FROM sessions WHERE bot_id=?",
	} {
		var n int
		must(sqlDB.QueryRowContext(ctx, query, target.ID.String()).Scan(&n))
		if n != 0 {
			t.Fatalf("stale %s=%d", table, n)
		}
	}
	for _, sid := range []string{target.HomeSessionID.String(), child, grandchild} {
		for table, query := range map[string]string{
			"sessions":       "SELECT count(*) FROM sessions WHERE id=?",
			"memories":       "SELECT count(*) FROM memories WHERE session_id=?",
			"session_events": "SELECT count(*) FROM session_events WHERE session_id=?",
		} {
			var n int
			must(sqlDB.QueryRowContext(ctx, query, sid).Scan(&n))
			if n != 0 {
				t.Fatalf("stale child %s %s=%d", sid, table, n)
			}
		}
	}
	var retrievalCount int
	must(sqlDB.QueryRowContext(ctx, "SELECT count(*) FROM memories_fts WHERE memories_fts MATCH 'needle'").Scan(&retrievalCount))
	if retrievalCount != 1 {
		t.Fatalf("stale retrieval items: %d", retrievalCount)
	}
	var secret []byte
	must(sqlDB.QueryRowContext(ctx, "SELECT encrypted FROM credentials WHERE id=?", cred).Scan(&secret))
	if string(secret) != "unchanged-secret" {
		t.Fatal("shared credential changed")
	}
	request("GET", "/api/studio/bots/"+other.ID.String()+"?scope=personal", "", 200)
	contents, err := backend.command(ctx, "run", "--rm", "--pull=never", "--network=none", "-v", other.Home.StorageKey+":/home/astonish:ro", "alpine:latest", "cat", "/home/astonish/candidate.txt")
	must(err)
	if contents != "owned candidate proof" {
		t.Fatal("other file changed")
	}
	page.MustReload()
	page.MustElement(`[aria-label="Delete Untouched other Bot"]`).MustVisible()
	if ok, _, e := page.Has(trash); e != nil || ok {
		t.Fatal("stale sidebar target", e)
	}
	draft := create("Never provisioned")
	request("POST", "/api/studio/bots/"+draft.ID.String()+"/permanent-delete?scope=personal", fmt.Sprintf(`{"expected_revision":%d,"confirm":true}`, draft.Revision), 204)
	t.Logf("PASS real sidebar trash/cancel/no reason -> HTTP 202 -> closed observer -> daemon Poll -> Docker volume absent -> HTTP 404; SQLite root/children/history/memory counts zero; unrelated Bot/file/shared credential preserved; reload absent; draft 204. Target=%s volume=%s", target.ID, target.Home.StorageKey)
}
