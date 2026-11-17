package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/gorilla/mux"
)

// Actual built Studio + Chromium + production handlers and isolated SQLite.
// Discovery responses supply identity only; Bot responses and DOM are not mocked.
// No worker is polled and no provider generation is requested.
func TestBotGenerationHealthBrowser(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building Studio")
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Fatal("explicit browser executable required")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build Studio first", err)
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
			}
			v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
			if err != nil {
				t.Fatal(err)
			}
			v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			v.Budget.RuntimePolicy.GenerationHealth = nil
			v.Budget.RuntimePolicy.ModelCallTimeoutSeconds = 37
			raw, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Saved elapsed-policy browser fixture", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(f.request("PUT", f.path+"/configuration", string(raw), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			server := generationHealthBrowserServer(t, f, dist)
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(45 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
				t.Fatalf("browser failure: %v\n%v", v, text)
			})
			open := func() {
				page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
				page.MustElementR("button", "^Details$").MustClick()
				page.MustElementR("summary", "^Configuration$").MustClick()
				page.MustElement(`[aria-label="Runtime configuration"] form`).MustVisible()
			}
			field := func(label string) *rod.Element {
				return page.MustElementR("label", "^"+label).MustElement("input,textarea,select")
			}
			assertElapsed := func() {
				if field("Use generation health monitoring").MustProperty("checked").Bool() {
					t.Fatal("saved elapsed policy silently upgraded")
				}
				if got := field("Model operation timeout").MustProperty("value").Str(); got != "37" {
					t.Fatalf("saved timeout = %s", got)
				}
			}
			reviewSave := func(reason string) {
				field("Review reason").MustInput(reason)
				page.MustElementR("button", "^Review configuration$").MustClick()
				page.MustElement(`[aria-label="Review configuration changes"]`).MustVisible()
				if !page.MustElementR("button", "^Save reviewed configuration$").MustProperty("disabled").Bool() {
					t.Fatal("save allowed without explicit review consent")
				}
				field("I reviewed this exact configuration").MustClick()
				field("I explicitly authorize the rights").MustClick()
				page.MustElementR("button", "^Save reviewed configuration$").MustClick()
				page.MustElementR("p", "Configuration saved. Activation and runs are separate actions.").MustVisible()
			}
			open()
			assertElapsed()
			field("Delivery preferences").MustInput("Preserve saved elapsed policy")
			reviewSave("Unrelated delivery edit must preserve the elapsed timeout")
			page.MustReload()
			open()
			assertElapsed()
			field("Use generation health monitoring").MustClick()
			page.MustElementR("p", "Health monitoring has no fixed generation duration").MustVisible()
			if got := field("Model operation timeout").MustProperty("value").Str(); got != "0" {
				t.Fatalf("health model timeout = %s", got)
			}
			if got := field("Generation inactivity threshold").MustProperty("value").Str(); got != "120" {
				t.Fatalf("idle threshold = %s", got)
			}
			if got := field("Generation health check interval").MustProperty("value").Str(); got != "10" {
				t.Fatalf("check interval = %s", got)
			}
			reviewSave("Explicitly authorize generation health monitoring")
			page.MustReload()
			open()
			if !field("Use generation health monitoring").MustProperty("checked").Bool() {
				t.Fatal("health choice not restored")
			}
			if got := field("Model operation timeout").MustProperty("value").Str(); got != "0" {
				t.Fatalf("restored model timeout = %s", got)
			}
			current, err := f.repo.Get(f.ctx, f.b.ID)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := f.repo.GetVersion(f.ctx, current.ID, current.Version)
			if err != nil {
				t.Fatal(err)
			}
			p := saved.Budget.RuntimePolicy
			if p == nil || p.GenerationHealth == nil || p.ModelCallTimeoutSeconds != 0 || p.GenerationHealth.IdleTimeoutSeconds != 120 || p.GenerationHealth.CheckIntervalSeconds != 10 {
				t.Fatalf("incorrect persisted health policy: %+v", p)
			}
			if current.Status != store.BotDraft || f.model.productions != 0 || f.pool.calls != 0 {
				t.Fatal("configuration save activated or ran the Bot")
			}
			t.Logf("scope=%s built Studio: saved 37s elapsed policy preserved; explicit health 120s/10s + model timeout 0 reviewed/saved/restored; no activation or generation", scope)
		})
	}
}

func generationHealthBrowserServer(t *testing.T, f *interactiveHTTPFixture, dist string) *httptest.Server {
	t.Helper()
	target, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	routes := mux.NewRouter()
	root := "/api/studio/bots"
	for _, r := range []struct {
		path, method string
		handler      http.HandlerFunc
	}{
		{root, "GET", api.BotListHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler},
		{root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler},
		{root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler},
		{root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler},
	} {
		routes.HandleFunc(r.path, r.handler).Methods(r.method)
	}
	routes.NotFoundHandler = httputil.NewSingleHostReverseProxy(target)
	org, err := f.db.ForOrg("interactive")
	if err != nil {
		t.Fatal(err)
	}
	personal := org.ForUser(f.b.OwnerID.String())
	svc := &store.Services{TenantRouter: f.db, Bots: org.ForTeam("team").Bots(), Sessions: org.ForTeam("team").Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: f.registry}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, root) {
			ctx, cancel := context.WithCancel(f.ctx)
			stop := context.AfterFunc(r.Context(), cancel)
			defer stop()
			defer cancel()
			routes.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
			return
		}
		var value any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			value = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			value = map[string]any{"user": map[string]any{"id": f.b.OwnerID.String(), "email": "health@example.invalid", "display_name": "Health acceptance", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": "interactive", "name": "Interactive"}, "team": "team"}
		case "/api/orgs":
			value = map[string]any{"orgs": []any{}, "active_org": "interactive"}
		case "/api/teams":
			value = []any{map[string]any{"slug": "team", "name": "Team"}}
		case "/api/settings/status":
			value = map[string]any{"setupRequired": false}
		case "/api/version":
			value = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			value = []any{}
		case "/api/settings/config":
			value = map[string]any{}
		case "/api/settings/providers/effective":
			value = map[string]any{"providers": map[string]any{}}
		}
		if value != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(value)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if st, e := os.Stat(path); e == nil && !st.IsDir() {
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	}))
	t.Cleanup(server.Close)
	return server
}
