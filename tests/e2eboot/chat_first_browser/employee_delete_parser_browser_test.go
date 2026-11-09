package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Controlled integration, NOT live employee acceptance: real built Studio,
// HTTP handlers, Ent SQLite, title admission/SSE, and Python readonly observer.
// No provider, daemon, Docker, interception, replay, or synthetic DOM is used.
// The peer has a registered controlled storage ref (not a claimed real volume);
// the inert target has no physical Home and is purged by the real delete handler.
func TestEmployeePeerDeleteParserBrowser(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit native browser opt-in required")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repoRoot, err := filepath.Abs("../../..")
	must(err)
	root, err := os.MkdirTemp(filepath.Join(repoRoot, ".tmp"), "astonish-chat-first-parser-")
	must(err)
	t.Cleanup(func() {
		if !t.Failed() {
			_ = os.RemoveAll(root)
		}
	})
	t.Logf("controlled artifact root: %s", root)
	dataDir := filepath.Join(root, "data")
	must(os.MkdirAll(dataDir, 0700))
	ctx := context.Background()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dataDir, "platform.db"), DataDir: dataDir})
	must(err)
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "parser-proof", Name: "Parser proof", Status: "active"}
	must(db.Organizations().Create(ctx, org))
	must(db.ProvisionOrg(ctx, org.ID, org.Slug))
	ods, err := db.ForOrg(org.Slug)
	must(err)
	team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
	must(ods.Teams().CreateTeam(ctx, team))
	owner := uuid.NewString()
	must(db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Parser owner", Status: "active"}))
	must(db.Organizations().AddMember(ctx, owner, org.ID, "member"))
	must(ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}))
	auth := func(c context.Context) context.Context {
		c, e := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		must(e)
		c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
		must(e)
		return store.WithTenantContext(c, &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: owner})
	}
	ownerCtx := auth(ctx)
	personal := ods.ForUser(owner)
	bots := personal.Bots()
	v := store.DefaultBotVersion()
	v.Charter = "Inert controlled deletion target"
	original, err := bots.CreateDraft(ownerCtx, store.BotCreateRequest{Name: "Parser deletion target", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "original"})
	must(err)
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	defer registry.Close()
	svc.BotInteractiveReady = true
	svc.BotTurnRegistry = registry
	routes := mux.NewRouter()
	api.RegisterRoutes(routes, svc, db, entstore.TenantMiddleware(db))
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, "web/package.json"))
	must(err)
	must(json.Unmarshal(raw, &pkg))
	dist := filepath.Join(repoRoot, "web/dist", pkg.Version)
	_, err = os.Stat(filepath.Join(dist, "index.html"))
	must(err)
	var creates, deletes atomic.Int32
	routes.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			value = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			value = map[string]any{"user": map[string]any{"id": owner, "email": "parser@example.test", "display_name": "Parser owner", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": org.Slug, "name": org.Name}, "team": team.Slug}
		case "/api/orgs":
			value = map[string]any{"orgs": []any{}, "active_org": org.Slug}
		case "/api/teams":
			value = []any{map[string]any{"slug": team.Slug, "name": team.Name}}
		case "/api/settings/status":
			value = map[string]any{"setupRequired": false}
		case "/api/version":
			value = map[string]any{"version": "controlled"}
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
		if r.URL.Path == "/" {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/studio/bots" {
			creates.Add(1)
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/permanent-delete") {
			deletes.Add(1)
		}
		routes.ServeHTTP(w, r.WithContext(auth(r.Context())))
	}))
	defer server.Close()
	bin := os.Getenv("ASTONISH_BROWSER_BIN")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Fatal("native test browser unavailable")
		}
	}
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	must(err)
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage(server.URL + "/#/chat/bots/personal/" + original.ID.String()).Timeout(45 * time.Second)
	page = page.WithPanic(func(v any) { t.Fatalf("browser: %v", v) })
	capture := func(label, path string, format employeePeerFormat) employeePeerPayload {
		t.Helper()
		p, e := employeeCapturePeer(page, root, label, path, format)
		must(e)
		return p
	}
	var inventory struct{ Bots []store.Bot }
	must(capture("peer-list-before", "/api/studio/bots?scope=personal", employeePeerJSON).decode(&inventory))
	before := map[uuid.UUID]bool{}
	for _, b := range inventory.Bots {
		before[b.ID] = true
	}
	page.MustElementR("button", "^New Bot$").MustClick()
	page.MustElement(`[aria-label="Bot name"]`).MustInput(employeePeerTitle)
	page.MustElementR(`[role="dialog"] button`, "^Start conversation$").MustClick()
	peerID, err := employeeWaitPeerHome(page, server.URL, original.ID.String())
	must(err)
	peer, err := employeeCreatedPeer(capture("peer-list-created", "/api/studio/bots?scope=personal", employeePeerJSON).Body, peerID, *original, before)
	must(err)
	path := "/api/studio/bots/" + peerID
	var rr struct{ Runs []store.BotRun }
	must(capture("peer-title-runs", path+"/runs?scope=personal", employeePeerJSON).decode(&rr))
	history := capture("peer-title-history", employeePeerHistoryPath(peer), employeePeerSSE)
	admitted, err := history.titleRun(peer, rr.Runs)
	must(err)
	if admitted.ID == uuid.Nil || len(rr.Runs) != 1 || admitted.UserEventID != rr.Runs[0].UserEventID || admitted.HistoryCursor != rr.Runs[0].HistoryCursor {
		t.Fatal("exact title source not admitted")
	}
	// Queued is intentionally NOT a successful terminal run. No provider executes.
	if admitted.Status != store.RunQueued {
		t.Fatal("unexpected controlled title state")
	}
	if err := history.decode(&inventory); err == nil {
		t.Fatal("SSE accepted as JSON")
	}
	snapshot := capture("peer-snapshot-before", path+"/snapshot?scope=personal", employeePeerJSON)
	if _, err := snapshot.titleRun(peer, rr.Runs); err == nil {
		t.Fatal("JSON accepted as SSE")
	}
	// Register using the production storage producer shape, never JSON in StorageKey.
	serviceCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, ClientID: "parser-registry", OrgSlug: org.Slug, TeamSlug: team.Slug, Scopes: []string{entstore.BotHomeRegistrationScope}, Authenticated: true})
	must(err)
	current, err := bots.Get(ownerCtx, peer.ID)
	must(err)
	binding := store.BotHomeBinding{ID: uuid.New(), BotID: peer.ID, Scope: peer.Scope, Backend: "docker", StorageKey: "astonish-bot-home-parser-" + peer.ID.String(), Revision: 1}
	must(entstore.RegisterBotHomeBinding(serviceCtx, bots, entstore.BotHomeRegistration{BotID: peer.ID, ExpectedRevision: current.Revision, Home: binding}, func(context.Context, entstore.BotHomeRegistration) error { return nil }))
	state := map[string]any{"root": root, "bot_id": peerID, "employee_home_session": original.HomeSessionID.String(), "employee_related_sessions": []string{original.HomeSessionID.String()}}
	readonly := func(label string) []byte {
		t.Helper()
		payload, e := json.Marshal(state)
		must(e)
		// This fixture's writer is the in-process Ent SQLite service above, not
		// an owned Docker container. Use the canonical projection's local reader
		// explicitly here, as the Python observer unit fixtures do. Never relax
		// employee_readonly's live ownership checks or add a host fallback.
		cmd := exec.Command("python3", "-B", "-c", "import json,sys; sys.path.insert(0,'tests/e2eboot'); import local_bot_smoke_launcher as l; print(json.dumps(l.employee_readonly_local(json.loads(sys.argv[1]))))", string(payload))
		cmd.Dir = repoRoot
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("readonly: %v %s", e, out)
		}
		must(os.WriteFile(filepath.Join(root, label), out, 0600))
		var observation struct {
			Complete bool     `json:"observation_complete"`
			Related  []string `json:"related_session_ids"`
		}
		must(json.Unmarshal(out, &observation))
		if !observation.Complete {
			t.Fatal("incomplete observer")
		}
		for _, sid := range observation.Related {
			if sid == original.HomeSessionID.String() {
				t.Fatal("peer inherited target session cache")
			}
		}
		volume, e := employeeDiskHomeVolume(out, label, peerID)
		must(e)
		if volume != binding.StorageKey {
			t.Fatal("readonly storage key mapping changed")
		}
		return out
	}
	beforeDisk := readonly("employee-peer-disk-before.json")
	beforeHistory := capture("peer-history-before", employeePeerHistoryPath(peer), employeePeerSSE)
	beforeBot := capture("peer-bot-before", path+"?scope=personal", employeePeerJSON)
	page.MustNavigate(server.URL + "/#/chat/bots/personal/" + original.ID.String())
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("button", "^Delete Bot$").MustClick()
	page.MustElementR(`[role="dialog"] button`, "^Delete$").MustClick()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, e := bots.Get(ownerCtx, original.ID)
		if e == store.ErrBotNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delete did not purge", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if creates.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("duplicate mutation: creates=%d deletes=%d", creates.Load(), deletes.Load())
	}
	if after := capture("peer-history-after", employeePeerHistoryPath(peer), employeePeerSSE); after.Body != beforeHistory.Body {
		t.Fatal("peer persisted history changed")
	}
	if after := capture("peer-bot-after", path+"?scope=personal", employeePeerJSON); after.Body != beforeBot.Body {
		t.Fatal("peer Bot changed")
	}
	if after := readonly("employee-peer-disk-after.json"); string(after) != string(beforeDisk) {
		t.Fatal("peer readonly rows changed")
	}
	afterTitle, err := capture("peer-title-after", employeePeerHistoryPath(peer), employeePeerSSE).titleRun(peer, rr.Runs)
	must(err)
	if afterTitle.ID != admitted.ID || afterTitle.UserEventID != admitted.UserEventID || afterTitle.HistoryCursor != admitted.HistoryCursor {
		t.Fatal("historical source changed")
	}
	report := fmt.Sprintf("controlled=true\ncreate_posts=1\ndelete_posts=1\npeer=%s\nhome=%s\nrun=%s\nuser_event=%s\nhistory_cursor=%d\nstorage_key=%s\nprovider_executed=false\nphysical_volume_verified=false\n", peer.ID, peer.HomeSessionID, admitted.ID, admitted.UserEventID, admitted.HistoryCursor, binding.StorageKey)
	must(os.WriteFile(filepath.Join(root, "parser-mapping.txt"), []byte(report), 0600))
	// Copy only the bounded mapping summary outside the disposable fixture.
	must(os.WriteFile(filepath.Join(repoRoot, ".tmp/employee-delete-parser-browser-mapping.txt"), []byte(report), 0600))
	t.Log(report)
}
