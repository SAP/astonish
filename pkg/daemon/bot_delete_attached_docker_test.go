package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	_ "github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Physical lifecycle acceptance, not a mocked Docker command test. All writable
// storage and IDs are disposable. Existing template layers are mounted read-only.
func TestBotPermanentDeleteAttachedDockerAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 for physical Docker deletion acceptance")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE must select an existing local image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	image = command("image", "inspect", "--format", "{{.Id}}", image)
	layers := os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if layers == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		layers = filepath.Join(config, "astonish", "sandbox", "layers")
	}
	if _, err := os.Stat(filepath.Join(layers, sandbox.BaseTemplateID, "rootfs", "bin", "sh")); err != nil {
		t.Fatalf("read-only existing base layer required: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
	root := t.TempDir()
	open := func() (sandbox.Backend, *sandbox.SessionRegistry) {
		t.Helper()
		st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
		if err != nil {
			t.Fatal(err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindDocker, Sessions: reg, Docker: sandbox.DockerRuntimeConfig{SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"}})
		if err != nil {
			t.Fatal(err)
		}
		return b, reg
	}
	backend, _ := open()
	org, team, owner := "delete-"+uuid.NewString(), "acceptance", uuid.NewString()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(root, "platform.db"), DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	organization := &store.Organization{ID: uuid.NewString(), Slug: org, Name: "Disposable", Status: "active"}
	if err := db.Organizations().Create(ctx, organization); err != nil {
		t.Fatal(err)
	}
	if err := db.ProvisionOrg(ctx, organization.ID, org); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org)
	if err != nil {
		t.Fatal(err)
	}
	group := &store.Team{ID: uuid.NewString(), Slug: team, Name: "Disposable", SchemaName: "team_acceptance"}
	if err := ods.Teams().CreateTeam(ctx, group); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@test.invalid", DisplayName: "Disposable", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(ctx, owner, organization.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: group.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	ownerCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: owner, OrgSlug: org, TeamSlug: team, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := ods.ForUser(owner).Bots()
	w := &BotWorker{config: BotWorkerConfig{Backend: db, Sandbox: backend, WorkerID: "delete-integrated"}}
	svc.BotHomeSetup = w.provisionHome
	router := mux.NewRouter()
	api.RegisterRoutes(router, svc, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(rw, r.WithContext(store.WithTenantContext(ownerCtx, &store.TenantContext{OrgSlug: org, TeamSlug: team, UserID: owner})))
	}))
	defer server.Close()
	request := func(method, path, body string, status int) store.Bot {
		t.Helper()
		r, e := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		response, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("%s %s: got %d want %d", method, path, response.StatusCode, status)
		}
		var result store.Bot
		if status == 200 || status == 201 {
			if e := json.NewDecoder(response.Body).Decode(&result); e != nil {
				t.Fatal(e)
			}
		}
		return result
	}
	b := request("POST", "/api/studio/bots?scope=personal", `{"name":"Attached disposable","idempotency_key":"attached","definition":{"charter":"Stay idle"}}`, 201)
	path := "/api/studio/bots/" + b.ID.String()
	b = request("POST", path+"/home?scope=personal", `{"expected_revision":1}`, 200)
	scope := sandbox.BotHomeScope{OrgID: org, TeamID: team, UserID: owner, BotID: b.ID.String()}
	home := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKind(b.Home.Backend), Ref: b.Home.StorageKey}
	rows, err := sql.Open("sqlite", filepath.Join(root, "orgs", org, "personal", owner+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, e := rows.ExecContext(ctx, query, args...); e != nil {
			t.Fatal(e)
		}
	}
	job, sentinel, task := uuid.NewString(), uuid.NewString(), uuid.NewString()
	execSQL(`INSERT INTO scheduled_jobs(id,name,schedule,mode,payload,status) VALUES (?,'owned','* * * * *','bot_trigger',?,'active')`, job, fmt.Sprintf(`{"bot_id":%q}`, b.ID))
	execSQL(`INSERT INTO scheduled_jobs(id,name,schedule,mode,payload,status) VALUES (?,'sentinel','* * * * *','routine','{}','active')`, sentinel)
	execSQL(`INSERT INTO bot_tasks(id,bot_id,key,home_path,title) VALUES (?,?,'proof','tasks/proof','Proof')`, task, b.ID.String())
	execSQL(`INSERT INTO bot_task_revisions(id,bot_id,task_id,revision,digest,size_bytes,authority_constraint_ref,data) VALUES (?,?,?,1,'proof',5,'fixture','{}')`, uuid.NewString(), b.ID.String(), task)
	execSQL(`INSERT INTO session_events(session_id,event_data) VALUES (?,?)`, b.HomeSessionID.String(), `{"content":"owned history"}`)
	private := "astonish-bot-workspace-" + scope.Key()
	id := uuid.NewString()
	foreignName := "astonish-delete-foreign-" + uuid.NewString()
	var container, overlay, foreign, network string
	// Only remove names/IDs allocated by this fixture. No pruning or production
	// registry access. Report cleanup failures rather than hiding failed acceptance.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		remove := func(args ...string) {
			out, e := exec.CommandContext(cleanup, "docker", args...).CombinedOutput()
			if e != nil && !strings.Contains(strings.ToLower(string(out)), "no such") {
				t.Errorf("cleanup docker %v: %v: %s", args, e, out)
			}
		}
		if foreign != "" {
			remove("rm", "-f", foreign)
		}
		if container != "" {
			remove("rm", "-f", container)
		}
		for _, volume := range []string{overlay, private, home.Ref} {
			if volume != "" {
				remove("volume", "rm", volume)
			}
		}
		if network != "" {
			remove("network", "rm", network)
		}
	})
	// Use a small explicit benchmark-range subnet: Docker Desktop may have
	// exhausted its default /16 pools. Never prune or reuse an existing network.
	network = command("network", "create", "--internal", "--subnet="+fmt.Sprintf("198.19.%d.0/28", uuid.New()[0]), "--label=astonish.org="+org, "astonish-org-"+org)
	command("volume", "create", "--driver=local", "--label=astonish.bot-home.scope="+scope.Key(), "--label=astonish.bot-home.version=workspace-1", private)
	session, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: scope.UserID, OrgSlug: org, TeamSlug: team, TemplateID: sandbox.BaseTemplateID, BotHome: home})
	if err != nil {
		t.Fatal(err)
	}
	container = session.BackendRef
	overlay = command("inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/var/astonish/overlay"}}{{.Name}}{{end}}{{end}}`, container)
	if err := backend.WaitForSessionReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	result, err := backend.Exec(ctx, id, sandbox.ExecSpec{Command: []string{"/bin/sh", "-c", "printf owned > /root/delete-acceptance; printf overlay > /tmp/delete-acceptance"}})
	if err != nil || result == nil || result.ExitCode != 0 {
		t.Fatalf("write Home/overlay: %+v %v", result, err)
	}
	if command("inspect", "--format", "{{.State.Running}}", container) != "true" {
		t.Fatal("owned container not running")
	}
	if err := backend.(sandbox.BotHomeDeleteBackend).DeleteBotHome(ctx, home); err == nil {
		t.Fatal("raw delete detached a running session")
	}
	// A foreign attachment with a forged scope label is never a deletion grant.
	foreign = command("container", "run", "--detach", "--pull=never", "--network=none", "--name="+foreignName, "--label=astonish.bot-home.scope="+scope.Key(), "--mount=type=volume,source="+home.Ref+",target=/foreign,volume-nocopy", "--entrypoint=/bin/sleep", image, "infinity")
	request("POST", path+"/permanent-delete?scope=personal", fmt.Sprintf(`{"expected_revision":%d,"confirm":true}`, b.Revision), 202)
	request("GET", path+"?scope=personal", "", http.StatusGone)
	request("GET", path+"/permanent-delete?scope=personal", "", http.StatusAccepted)
	deleted, err := repo.Get(ownerCtx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.reconcileRetention(ownerCtx, repo, *deleted); err == nil {
		t.Fatal("foreign attachment accepted")
	}
	if err := repo.(store.BotPermanentDeleteStore).PurgeDeleted(ownerCtx, b.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("failed cleanup became purgeable: %v", err)
	}
	if command("inspect", "--format", "{{.State.Running}}", foreign) != "true" || command("inspect", "--format", "{{.State.Running}}", container) != "true" {
		t.Fatal("foreign preflight altered containers")
	}
	command("rm", "-f", foreign)
	foreign = ""
	// Restart both controller and durable registry; no pool/session cache survives.
	backend, reg := open()
	restarted := &BotWorker{config: w.config}
	restarted.config.Sandbox = backend
	if err := restarted.pollStore(ownerCtx, repo, nil); err != nil {
		t.Fatal(err)
	}
	request("GET", path+"?scope=personal", "", http.StatusNotFound)
	for _, query := range []string{
		"SELECT count(*) FROM bots WHERE id=?",
		"SELECT count(*) FROM bot_tasks WHERE bot_id=?",
		"SELECT count(*) FROM bot_task_revisions WHERE bot_id=?",
		"SELECT count(*) FROM sessions WHERE bot_id=?",
	} {
		var n int
		if e := rows.QueryRowContext(ctx, query, b.ID.String()).Scan(&n); e != nil || n != 0 {
			t.Fatalf("owned rows remain %s: %d %v", query, n, e)
		}
	}
	var n int
	if e := rows.QueryRowContext(ctx, "SELECT count(*) FROM session_events WHERE session_id=?", b.HomeSessionID.String()).Scan(&n); e != nil || n != 0 {
		t.Fatalf("history remains: %d %v", n, e)
	}
	if e := rows.QueryRowContext(ctx, "SELECT count(*) FROM scheduled_jobs WHERE id=?", job).Scan(&n); e != nil || n != 0 {
		t.Fatalf("owned job remains: %d %v", n, e)
	}
	if e := rows.QueryRowContext(ctx, "SELECT count(*) FROM scheduled_jobs WHERE id=? AND status='active' AND payload='{}'", sentinel).Scan(&n); e != nil || n != 1 {
		t.Fatalf("sentinel changed: %d %v", n, e)
	}
	for _, name := range []string{container, overlay, private, home.Ref} {
		kind := "volume"
		if name == container {
			kind = "container"
		}
		out, e := exec.CommandContext(ctx, "docker", kind, "inspect", name).CombinedOutput()
		if e == nil || !strings.Contains(strings.ToLower(string(out)), "no such") {
			t.Fatalf("%s still present/absence unverified: %v %s", name, e, out)
		}
	}
	if rec, e := reg.GetSession(id); e != nil || rec != nil {
		t.Fatalf("registry remains: %+v %v", rec, e)
	}
	if _, e := os.Stat(filepath.Join(root, "uppers", id)); !os.IsNotExist(e) {
		t.Fatalf("private upper remains: %v", e)
	}
	if err := backend.(sandbox.BotHomeSessionCleanupBackend).ReconcileBotHomeSessions(ctx, home, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("retry reconciliation: %v", err)
	}
	if err := backend.(sandbox.BotHomeDeleteBackend).DeleteBotHome(ctx, home); err != nil {
		t.Fatalf("retry deletion: %v", err)
	}
	// Simulate a prior cleanup losing Home first but leaving its private workspace.
	command("volume", "create", "--driver=local", "--label=astonish.bot-home.scope="+scope.Key(), "--label=astonish.bot-home.version=workspace-1", private)
	if err := backend.(sandbox.BotHomeDeleteBackend).DeleteBotHome(ctx, home); err != nil {
		t.Fatalf("missing Home/private workspace retry: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "volume", "inspect", private).CombinedOutput(); err == nil || !strings.Contains(strings.ToLower(string(out)), "no such") {
		t.Fatalf("private workspace remains after missing Home retry: %v %s", err, out)
	}
	t.Logf("physical Docker acceptance: owned running session %s, overlay %s, private workspace and Home absent; forged attachment preserved until fixture teardown; fresh registry and worker completed receipt", id, overlay)
}
