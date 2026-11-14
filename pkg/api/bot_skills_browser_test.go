package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
	var writes, skillsPanelReads atomic.Int64
	env.Router.PathPrefix("/api/studio/bots").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		// Schedule retains its separate limit=100 lookup. Only the removed
		// Skills panel's list/history reads must disappear from Details.
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/skills") && !(strings.HasSuffix(r.URL.Path, "/skills") && r.URL.Query().Get("limit") == "100") {
			skillsPanelReads.Add(1)
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
	fixtureDist := buildBotSkillsComponentFixture(t, b.ID.String())
	env.Router.HandleFunc("/skills-component-fixture", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<div id="root"></div><script type="module" src="/skills-component-assets/fixture.js"></script>`)
	})
	env.Router.PathPrefix("/skills-component-assets/").Handler(http.StripPrefix("/skills-component-assets/", http.FileServer(http.Dir(fixtureDist))))
	page := newStreamBrowserPage(t, env, bin, dist)
	page.MustNavigate(env.URL + "/#/chat/bots/personal/" + b.ID.String())
	page.MustElement(`[data-testid="chat-input"]`).MustInput("Preserve unfinished Home work")
	page.MustElementR("button", "^Details$").MustClick()
	assertNoSkills := func() {
		t.Helper()
		if !page.MustEval(`() => !document.querySelector('[aria-label="Bot skills"]') && !Array.from(document.querySelectorAll('[role="dialog"] summary')).some(n => /Skills & history|File browser|Tasks/.test(n.textContent))`).Bool() {
			t.Fatal("Details mounted removed Skills or replacement navigation")
		}
		if skillsPanelReads.Load() != 0 {
			t.Fatalf("Details issued removed Skills panel reads: %d", skillsPanelReads.Load())
		}
	}
	page.MustElementR("summary", "^Schedule & triggers$").MustClick()
	page.MustElementR("p", "Legacy instructions: Read official releases").MustVisible()
	page.MustElement(`[aria-label="Schedule skill"]`).MustVisible()
	page.MustElementR("p", "No active skills").MustVisible()
	assertNoSkills()
	if writes.Load() != 0 {
		t.Fatal("Details inspection wrote data")
	}
	t.Log("SKILL_SURFACE browser/details-absent PASS")
	t.Log("SKILL_SURFACE browser/legacy-schedule PASS")
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	if page.MustElement(`[data-testid="chat-input"]`).MustProperty("value").Str() != "Preserve unfinished Home work" {
		t.Fatal("Details lost Home draft")
	}

	// This isolated test-only bundle mounts the preserved component directly,
	// not through Studio routing or Details. Its requests use the same real
	// scoped handlers and SQLite as the workspace above.
	page.MustNavigate(env.URL + "/skills-component-fixture")
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
	// Return to the actual built workspace with populated skill data, then
	// reload. Inspection still must not mount the standalone component.
	skillsPanelReads.Store(0)
	page.MustNavigate(env.URL + "/#/chat/bots/personal/" + b.ID.String())
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Schedule & triggers$").MustClick()
	page.MustElementR("p", "Legacy instructions: Read official releases").MustVisible()
	assertNoSkills()
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	page.MustReload()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Schedule & triggers$").MustClick()
	page.MustElementR("p", "Legacy instructions: Read official releases").MustVisible()
	assertNoSkills()
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	page.MustElement(`[data-testid="chat-input"]`).MustVisible()
	after, err := repo.Get(ctx, b.ID)
	if err != nil || after.Revision != b.Revision {
		t.Fatalf("inspection mutated root: %v", err)
	}
	if writes.Load() != 1 {
		t.Fatalf("inspection made extra writes: %d", writes.Load())
	}
	t.Log("SKILL_SURFACE browser/home-readonly PASS")
}

// Build only a test entry in a temporary directory. No production route,
// source, or built SPA asset is changed to expose the orphaned component.
func buildBotSkillsComponentFixture(t *testing.T, botID string) string {
	t.Helper()
	web, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	entry := filepath.Join(dir, "fixture.tsx")
	source := fmt.Sprintf(`import React from %q;
import { createRoot } from %q;
import BotSkillsPanel from %q;
import { getBot } from %q;
getBot(%q, 'personal').then(bot => createRoot(document.getElementById('root')!).render(React.createElement(BotSkillsPanel, {bot})));
`, filepath.Join(web, "node_modules/react/index.js"), filepath.Join(web, "node_modules/react-dom/client.js"), filepath.Join(web, "src/components/bots/BotSkillsPanel.tsx"), filepath.Join(web, "src/api/bots.ts"), botID)
	if err := os.WriteFile(entry, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dist")
	// Loading the existing config retains React transform and @ aliases while
	// replacing only the build entry/output and never touching web/dist.
	script := `import {build} from 'vite'; await build({configFile: './vite.config.js', define: {'process.env.NODE_ENV': JSON.stringify('production')}, build: {outDir: process.argv[2], emptyOutDir: true, lib: {entry: process.argv[1], formats: ['es'], fileName: () => 'fixture.js'}}});`
	cmd := exec.Command("node", "--input-type=module", "-e", script, entry, out)
	cmd.Dir = web
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build standalone skill component fixture: %v\n%s", err, output)
	}
	return out
}
