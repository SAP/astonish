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
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/gorilla/mux"
)

// The built SPA talks to production HTTP handlers and real SQLite. Only auth
// discovery/static application configuration is a fixture; no Bot response or
// browser fetch is intercepted.
func TestBotProgressBrowserAcceptance(t *testing.T) {
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
			f, before := progressControlFixture(t, scope)
			target, _ := url.Parse(f.server.URL)
			proxy := httputil.NewSingleHostReverseProxy(target)
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
			routes.NotFoundHandler = proxy
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			personal := org.ForUser(f.b.OwnerID.String())
			svc := &store.Services{TenantRouter: f.db, Bots: org.ForTeam("team").Bots(), Sessions: org.ForTeam("team").Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: f.registry}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, root) {
					routes.ServeHTTP(w, r.WithContext(store.WithServices(f.ctx, svc)))
					return
				}
				var value any
				switch r.URL.Path {
				case "/api/auth/setup-status":
					value = map[string]any{"initialized": true, "auth_mode": "builtin"}
				case "/api/auth/me":
					value = map[string]any{"user": map[string]any{"id": f.b.OwnerID.String(), "email": "progress@example.invalid", "display_name": "Progress acceptance", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": "interactive", "name": "Interactive"}, "team": "team"}
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
			defer server.Close()
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(30 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
				t.Fatalf("browser failure: %v\n%v", v, text)
			})
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			page.MustEval(`() => {window.progressLeaks=[];new MutationObserver(()=>{const text=document.querySelector('[aria-label="Bot workspace"]')?.innerText||'';for(const marker of ['read_file','dispatch_ledger','context_ref','reserved_usage','isolated receipt'])if(text.includes(marker))window.progressLeaks.push(marker)}).observe(document.body,{subtree:true,childList:true,characterData:true})}`)
			// Pausing through production HTTP while subscribed proves live state delivery.
			paused := progressPause(t, f, before)
			page.MustElementR("button", "^Continue saved progress$").MustVisible()
			page.MustReload()
			page.MustElementR("button", "^Continue saved progress$").MustVisible()
			text := page.MustElement(`[aria-label="Bot workspace"]`).MustText()
			for _, marker := range []string{"read_file", "dispatch_ledger", "context_ref", "reserved_usage", "isolated receipt"} {
				if strings.Contains(text, marker) {
					t.Fatalf("private restore telemetry: %s", marker)
				}
			}
			page.MustElementR("button", "^Continue saved progress$").MustClick()
			page.MustWait(`() => ![...document.querySelectorAll('button')].some(e=>e.innerText==='Continue saved progress'&&!e.disabled)`)
			deadline := time.Now().Add(5 * time.Second)
			for progressGetRun(t, f, before.ID).Status != store.RunQueued {
				if time.Now().After(deadline) {
					t.Fatal("browser continuation was not admitted")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := progressFreshWorker(t, f).Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			after := progressGetRun(t, f, before.ID)
			if after.Status != store.RunSucceeded || after.ID != before.ID || after.Usage.ToolCalls != paused.Usage.ToolCalls || f.pool.calls != 1 {
				t.Fatalf("browser continuation replayed work: %+v", after)
			}
			page.MustElementR("p", "The saved evidence confirms isolated receipt.").MustVisible()
			page.MustReload()
			page.MustElementR("p", "The saved evidence confirms isolated receipt.").MustVisible()
			if strings.Contains(page.MustElement(`[aria-label="Bot workspace"]`).MustText(), "read_file") {
				t.Fatal("raw tool trace leaked into restored Home")
			}
			t.Logf("built Studio scope=%s live_pause=visible restore_continue=visible owner_continue=admitted same_run=%s reads=1 final=restored", scope, after.ID)
		})
	}
}
