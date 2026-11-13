package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Built SPA, native browser, real scoped read handlers and SQLite. Legacy data
// is seeded through the explicit trusted import contract, never through GET.
func TestBotSkillsBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit native browser opt-in required")
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
		t.Fatal(err)
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Fatal("native browser unavailable")
		}
	}
	env := setupMemoryHTTPAcceptance(t, &MockLLM{}, nil)
	router := mux.NewRouter()
	registerBotRoutes(router)
	var unavailable, unsupported atomic.Bool
	var writes atomic.Int64
	env.Router.PathPrefix("/api/studio/bots").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		if unavailable.Load() && strings.HasSuffix(r.URL.Path, "/skills") {
			http.Error(w, "Controlled unavailable", 503)
			return
		}
		if unsupported.Load() && strings.HasSuffix(r.URL.Path, "/revisions") {
			_ = json.NewEncoder(w).Encode(store.BotSkillRevisionList{Revisions: []store.BotSkillRevision{{SchemaVersion: 99, Revision: 99, Responsibility: "UNTRUSTED-SCHEMA-CONTENT"}}})
			return
		}
		entstore.TenantMiddleware(env.Store)(router).ServeHTTP(w, r)
	}))
	org, err := env.Store.ForOrg(env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForUser(env.UserID).Bots()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	version := store.DefaultBotVersion()
	version.Charter = "Read official release notes"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Skill browser", OwnerID: uuid.MustParse(env.UserID), Version: version, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	trigger := store.TriggerSpec{ID: uuid.New(), Kind: store.TriggerCron, Instructions: "Read official releases and cite links", Cron: "0 8 * * *", Timezone: "UTC"}
	b, err = repo.(store.BotTriggerStore).ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: trigger.ID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "Legacy fixture", Definition: trigger})
	if err != nil {
		t.Fatal(err)
	}
	page := newStreamBrowserPage(t, env, bin, dist)
	page.MustNavigate(env.URL + "/#/chat/bots/personal/" + b.ID.String())
	page.MustElement(`[data-testid="chat-input"]`).MustInput("Preserve unfinished Home work")
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Skills & history$").MustClick()
	page.MustElementR("p", "No committed skills yet").MustVisible()
	t.Log("SKILL_SURFACE browser/empty PASS")
	page.MustElementR("button", "^Review legacy import$").MustClick()
	page.MustElementR("button", "^Select legacy procedure ").MustClick()
	page.MustElementR("button", "^Cancel legacy import$").MustClick()
	if writes.Load() != 0 {
		t.Fatal("cancel wrote data")
	}
	t.Log("SKILL_SURFACE browser/import-cancel PASS")
	page.MustElementR("button", "^Review legacy import$").MustClick()
	page.MustElementR("button", "^Select legacy procedure ").MustClick()
	page.MustElementR("button", "^Confirm legacy import$").MustClick()
	page.MustElementR("p", "Imported .* as a draft skill").MustVisible()
	if writes.Load() != 1 {
		t.Fatalf("import writes = %d", writes.Load())
	}
	list, err := repo.(store.BotSkillStore).ListSkills(ctx, b.ID, store.BotSkillPage{})
	if err != nil || len(list.Skills) != 1 {
		t.Fatalf("import missing: %v", err)
	}
	imported := list.Skills[0]
	t.Log("SKILL_SURFACE browser/import-success PASS")
	page.MustElementR("button", "^Cancel legacy import$").MustClick()
	page.MustElementR("button", "^Refresh skills$").MustClick()
	page.MustElementR(`[aria-label="Bot skills"] button`, "^"+imported.Title+"$").MustClick()
	page.MustElementR(`[aria-label="Skill history"] p`, "Read official releases and cite links").MustVisible()
	page.MustElementR("p", "No linked runs").MustVisible()
	t.Log("SKILL_SURFACE browser/history PASS")
	unsupported.Store(true)
	page.MustElementR("button", "^Refresh skill history$").MustClick()
	page.MustElementR("p", "Unsupported skill schema 99").MustVisible()
	if strings.Contains(page.MustElement("body").MustText(), "UNTRUSTED-SCHEMA-CONTENT") {
		t.Fatal("unknown schema interpreted")
	}
	t.Log("SKILL_SURFACE browser/schema PASS")
	unsupported.Store(false)
	unavailable.Store(true)
	page.MustElementR("button", "^Refresh skills$").MustClick()
	page.MustElementR(`[aria-label="Bot skills"] [role="alert"]`, "Stale skills").MustVisible()
	unavailable.Store(false)
	page.MustElementR("button", "^Refresh skills$").MustClick()
	page.MustWait(`() => !document.querySelector('[aria-label="Bot skills"] [role="alert"]')`)
	t.Log("SKILL_SURFACE browser/stale-retry PASS")
	page.MustElementR("summary", "^Schedule & triggers$").MustClick()
	page.MustElementR("p", "Legacy instructions: Read official releases").MustVisible()
	page.MustElement(`[aria-label="Schedule skill"]`).MustVisible()
	page.MustElementR("p", "No active skills").MustVisible()
	t.Log("SKILL_SURFACE browser/legacy-schedule PASS")
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	if page.MustElement(`[data-testid="chat-input"]`).MustProperty("value").Str() != "Preserve unfinished Home work" {
		t.Fatal("Details lost Home draft")
	}
	after, err := repo.Get(ctx, b.ID)
	if err != nil || after.Revision != b.Revision {
		t.Fatalf("inspection mutated root: %v", err)
	}
	if writes.Load() != 1 {
		t.Fatalf("inspection made extra writes: %d", writes.Load())
	}
	t.Log("SKILL_SURFACE browser/home-readonly PASS")
}
