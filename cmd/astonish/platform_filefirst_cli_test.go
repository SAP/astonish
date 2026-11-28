package astonish

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Real public binary, fresh config/data roots, physical Docker ordinary/private
// volumes, and reopened production HTTP handlers. No daemon or model is used.
func TestBotFileFirstBackupCLI(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { testBotFileFirstBackupCLI(t, false, scope) })
	}
}

func TestBotFileFirstStoppedBackupCLI(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { testBotFileFirstBackupCLI(t, true, scope) })
	}
}

func testBotFileFirstBackupCLI(t *testing.T, stoppedSource bool, botScope store.BotScope) {
	t.Helper()
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	dir := t.TempDir()
	layers := os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if stoppedSource {
		if !filepath.IsAbs(layers) {
			t.Fatal("absolute ASTONISH_BOT_HOME_TEST_LAYERS required for stopped-source Chat compute")
		}
		var err error
		dir, err = os.MkdirTemp(filepath.Dir(layers), "backup-stopped-acceptance-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	makeConfig := func(root string) string {
		t.Helper()
		cfgdir := filepath.Join(root, "config", "astonish")
		if err := os.MkdirAll(cfgdir, 0700); err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("storage:\n  backend: sqlite\n  sqlite:\n    data_dir: %s\nsandbox:\n  backend: docker\n  docker:\n    sandbox_image: %s\n", filepath.Join(root, "data"), image)
		if err := os.WriteFile(filepath.Join(cfgdir, "config.yaml"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "data"), 0700); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(root, "data")
	}
	data, targetData := makeConfig(source), makeConfig(target)
	// Resolve the explicitly selected Docker context before isolating HOME; do not
	// inherit application configuration or fall back to a different daemon.
	if os.Getenv("DOCKER_HOST") == "" {
		endpoint, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format={{.Endpoints.docker.Host}}").Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("DOCKER_HOST", strings.TrimSpace(string(endpoint)))
	}
	// Keep the Go toolchain cache while isolating application configuration.
	goenv, err := exec.CommandContext(ctx, "go", "env", "GOPATH", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	cache := strings.Fields(string(goenv))
	if len(cache) != 2 {
		t.Fatal("missing Go cache paths")
	}
	t.Setenv("GOPATH", cache[0])
	t.Setenv("GOCACHE", cache[1])
	t.Setenv("HOME", source)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(source, "config"))
	cfg, err := config.LoadAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	backend, cleanup, err := sandbox.BackendFromAppConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	files, ok := backend.(entstore.BotRestoreBackend)
	if !ok {
		t.Fatal("Docker file restore capability unavailable")
	}
	_, s, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(data, "platform.db"), DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	owner, id, session := uuid.New(), uuid.New(), uuid.New()
	org := &store.Organization{ID: uuid.NewString(), Slug: "backup-cli", Name: "Backup", Status: "active"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@example.test", DisplayName: "Backup", Status: "active"}))
	must(s.Organizations().Create(ctx, org))
	must(s.Organizations().AddMember(ctx, owner.String(), org.ID, "owner"))
	must(s.ProvisionOrg(ctx, org.ID, org.Slug))
	ods, err := s.ForOrg(org.Slug)
	must(err)
	team := &store.Team{ID: uuid.NewString(), Name: "Backup", Slug: "backup", SchemaName: "backup"}
	must(ods.Teams().CreateTeam(ctx, team))
	must(ods.ProvisionTeam(ctx, team.Slug))
	must(ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}))
	must(ods.ProvisionPersonalSchema(ctx, owner.String()))
	scope := sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner.String(), BotID: id.String()}
	home, err := backend.(sandbox.BotHomeBackend).EnsureBotHome(ctx, scope)
	must(err)
	owned := []*sandbox.BotHomeBinding{home}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		// Even an assertion failure before reopening the Bot must clean test-owned
		// destinations. The journal is under this test's fresh private data root.
		if raw, err := os.ReadFile(filepath.Join(targetData, ".filefirst-restore", "journal.json")); err == nil {
			var journal struct {
				Homes []struct {
					Target sandbox.BotHomeBinding `json:"target"`
				} `json:"homes"`
			}
			if err = json.Unmarshal(raw, &journal); err != nil {
				t.Error(err)
			} else {
				for _, h := range journal.Homes {
					owned = append(owned, &h.Target)
				}
			}
		}
		for _, h := range owned {
			if err := backend.(sandbox.BotHomeDeleteBackend).DeleteBotHome(c, h); err != nil {
				t.Error(err)
			}
		}
	}()
	body := bytes.Repeat([]byte{0, 255, 1, 62, 11, 33, 9}, (17<<20)/7+1)
	_, err = files.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "reports/large.bin", SizeBytes: int64(len(body)), Body: bytes.NewReader(body), CreateOnly: true, BeforeCommit: func(op func() error) error { return op() }})
	must(err)
	artifact, err := files.PublishBotArtifactSnapshot(ctx, home, "reports/large.bin", "large.bin", func(op func() error) error { return op() })
	must(err)
	versionBody := []byte(`{"charter":"CLI-disposable-file-first-charter","soul":"","identity":"","user":"","avatar":"","preferences":"","change_reason":"","triggers":null}`)
	versionRef, err := files.PutBotContent(ctx, home, store.BotContentDocument, int64(len(versionBody)), bytes.NewReader(versionBody), 1, func(op func() error) error { return op() })
	must(err)
	version := store.DefaultBotVersion()
	record := map[string]any{"format": 2, "bot_id": id, "version": 1, "schema_version": 1, "autonomy": version.Autonomy, "budget": version.Budget, "notify": version.Delivery.Notify, "bindings": version.Delivery.Bindings, "sandbox_template": version.SandboxTemplate, "language": version.Persona.Language, "timezone": version.Persona.Timezone, "created_by": owner, "created_at": time.Now().UTC(), "triggers": nil, "content": versionRef}
	rawRecord, _ := json.Marshal(record)
	definition, _ := json.Marshal(map[string]any{"bot_record_v2": string(rawRecord)})
	binding := store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: botScope, Backend: "docker", StorageKey: home.Ref, Revision: 1}
	rawHome, _ := json.Marshal(binding)
	metadata, _ := json.Marshal(map[string]any{"bot_content_home_v1": map[string]any{"home": binding, "binding": home}})
	scopeDBPath := func(root string) string {
		if botScope == store.BotScopeTeam {
			return filepath.Join(root, "orgs", org.Slug, "teams", team.Slug+".db")
		}
		return filepath.Join(root, "orgs", org.Slug, "personal", owner.String()+".db")
	}
	dbPath := scopeDBPath(data)
	db, err := sql.Open("sqlite", dbPath)
	must(err)
	_, err = db.Exec(`INSERT INTO bots (id,owner_id,name,status,revision,version,schema_version,home_session_id,home_binding,create_key,last_seq,fence,data,cleanup_complete,created_at,updated_at) VALUES (?,?,?,'paused',1,1,1,?,?,?,0,0,?,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id.String(), owner.String(), "Backup", session.String(), string(rawHome), uuid.NewString(), string(metadata))
	must(err)
	_, err = db.Exec(`INSERT INTO sessions (id,user_id,bot_id,purpose,retention,title,metadata,created_at,updated_at) VALUES (?,?,?,'bot_home','bot','Backup','{}',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, session.String(), owner.String(), id.String())
	must(err)
	_, err = db.Exec(`INSERT INTO bot_versions (id,bot_id,version,schema_version,definition,content_hash,created_by,change_reason,created_at) VALUES (?,?,1,1,?,?,?,'',CURRENT_TIMESTAMP)`, uuid.NewString(), id.String(), string(definition), "hash", owner.String())
	must(err)
	must(db.Close())
	must(s.Close())
	var sourceCompute string
	if stoppedSource {
		// Share the isolated default registry/recipe path with the public CLI,
		// but mount the actual base-layer directory (not a symlink across mounts).
		registry, err := sandbox.NewSessionRegistry()
		must(err)
		templates, err := sandbox.NewTemplateRegistry()
		must(err)
		computeBackend, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindDocker, Sessions: registry, Templates: templates, Docker: sandbox.DockerRuntimeConfig{SandboxImage: image, LayersDir: layers}})
		must(err)
		must(computeBackend.(sandbox.BotHomeSessionBinder).BindBotHomeSession(home, session.String()))
		created, err := computeBackend.CreateSession(ctx, sandbox.SessionSpec{SessionID: session.String(), Type: sandbox.SessionTypeChat, UserID: owner.String(), BotHome: home.Clone(), Image: image, TemplateID: sandbox.BaseTemplateID, LayerChain: []string{sandbox.BaseTemplateID}})
		must(err)
		defer func() {
			c, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			if err := computeBackend.DestroySession(c, session.String()); err != nil {
				t.Error(err)
			}
		}()
		must(computeBackend.WaitForSessionReady(ctx, session.String()))
		must(computeBackend.StopSession(ctx, session.String()))
		sourceCompute = created.BackendRef
	} else {
		// Retain the original detached-source acceptance path.
		compute := "astonish-backup-cli-" + uuid.NewString()
		cmd := exec.CommandContext(ctx, "docker", "container", "create", "--name="+compute, "--network=none", "--mount=type=volume,source="+home.Ref+",target=/home-files,volume-nocopy", "--entrypoint=/bin/true", image)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("source compute: %v %s", err, out)
		}
		if out, err := exec.CommandContext(ctx, "docker", "rm", compute).CombinedOutput(); err != nil {
			t.Fatalf("remove source compute: %v %s", err, out)
		}
	}
	binary := filepath.Join(dir, "astonish")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	archive := filepath.Join(dir, "backup.archive")
	run := func(root string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "DOCKER_HOST=" + os.Getenv("DOCKER_HOST"), "ASTONISH_SANDBOX_IMAGE=" + image}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
	}
	var stoppedIdentity []byte
	if stoppedSource {
		stoppedIdentity, err = exec.CommandContext(ctx, "docker", "inspect", "--format={{.Id}} {{.State.Running}}", sourceCompute).Output()
		must(err)
		if !strings.HasSuffix(strings.TrimSpace(string(stoppedIdentity)), " false") {
			t.Fatal("source compute not stopped")
		}
	}
	run(source, "platform", "backup", "create", "--output", archive)
	run(source, "platform", "backup", "verify", archive)
	if stoppedSource {
		after, err := exec.CommandContext(ctx, "docker", "inspect", "--format={{.Id}} {{.State.Running}}", sourceCompute).Output()
		must(err)
		if !bytes.Equal(after, stoppedIdentity) {
			t.Fatal("public backup resumed or replaced source compute")
		}
		t.Log("public CLI backup retained exact stopped Chat compute identity")
	}
	// Inject an ambiguous allocation failure: real Docker creates the journaled
	// volume, then a test-only runtime shim returns an error to the public CLI.
	// No production hook, daemon, or application configuration is changed.
	realDocker, err := exec.LookPath("docker")
	must(err)
	shimDir := filepath.Join(dir, "failure-runtime")
	must(os.Mkdir(shimDir, 0700))
	shim := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *'volume create'*'-restore-'*) '%s' \"$@\" || exit $?; echo 'test restore interruption' >&2; exit 73;;\nesac\nexec '%s' \"$@\"\n", realDocker, realDocker)
	must(os.WriteFile(filepath.Join(shimDir, "docker"), []byte(shim), 0700))
	failed := exec.CommandContext(ctx, binary, "platform", "restore", archive, "--confirm")
	failed.Env = []string{"PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + target, "XDG_CONFIG_HOME=" + filepath.Join(target, "config"), "DOCKER_HOST=" + os.Getenv("DOCKER_HOST"), "ASTONISH_SANDBOX_IMAGE=" + image}
	out, err := failed.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("test restore interruption")) || !bytes.Contains(out, []byte("cleanup")) {
		t.Fatalf("physical restore failure not injected: %v %s", err, out)
	}
	var failedJournal struct {
		Homes []struct {
			Target sandbox.BotHomeBinding `json:"target"`
		} `json:"homes"`
	}
	journalBytes, err := os.ReadFile(filepath.Join(targetData, ".filefirst-restore", "journal.json"))
	must(err)
	must(json.Unmarshal(journalBytes, &failedJournal))
	if len(failedJournal.Homes) != 1 || failedJournal.Homes[0].Target.Ref == home.Ref {
		t.Fatal("missing fresh failed target")
	}
	failedHome := failedJournal.Homes[0].Target
	owned = append(owned, &failedHome)
	if out, err := exec.CommandContext(ctx, "docker", "volume", "inspect", failedHome.Ref).CombinedOutput(); err != nil {
		t.Fatalf("physical failed target absent: %v %s", err, out)
	}
	// A stopped, still-attached container is not permission to detach storage.
	attachment := "astonish-backup-cleanup-" + uuid.NewString()
	if out, err := exec.CommandContext(ctx, "docker", "container", "create", "--name="+attachment, "--network=none", "--mount=type=volume,source="+failedHome.Ref+",target=/home-files,volume-nocopy", "--entrypoint=/bin/true", image).CombinedOutput(); err != nil {
		t.Fatalf("cleanup attachment: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", attachment).Run() // test-owned, never force
	blocked := exec.CommandContext(ctx, binary, "platform", "restore", "cleanup", archive, "--confirm")
	blocked.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + target, "XDG_CONFIG_HOME=" + filepath.Join(target, "config"), "DOCKER_HOST=" + os.Getenv("DOCKER_HOST"), "ASTONISH_SANDBOX_IMAGE=" + image}
	if out, err := blocked.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("attached")) {
		t.Fatalf("attached cleanup not refused: %v %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "container", "inspect", attachment).CombinedOutput(); err != nil {
		t.Fatalf("cleanup detached test compute: %v %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "rm", attachment).CombinedOutput(); err != nil {
		t.Fatalf("remove cleanup attachment: %v %s", err, out)
	}
	run(target, "platform", "restore", "cleanup", archive, "--confirm")
	if out, err := exec.CommandContext(ctx, "docker", "volume", "inspect", failedHome.Ref).CombinedOutput(); err == nil || !bytes.Contains(bytes.ToLower(out), []byte("no such volume")) {
		t.Fatalf("failed target retained: %v %s", err, out)
	}
	sourceSnapshot, err := files.OpenBotContent(ctx, home, *artifact, store.BotFileRange{})
	must(err)
	sourceBytes, err := io.ReadAll(sourceSnapshot)
	must(err)
	must(sourceSnapshot.Close())
	if !bytes.Equal(sourceBytes, body) {
		t.Fatal("cleanup changed source snapshot")
	}
	failureDB, err := sql.Open("sqlite", filepath.Join(targetData, "platform.db"))
	must(err)
	var visible int
	must(failureDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&visible))
	if visible != 0 {
		t.Fatal("failed restore exposed platform users")
	}
	must(failureDB.Close())
	t.Log("public CLI physical failure -> cleanup removed exact destination, retained source, zero platform users -> retry")
	run(target, "platform", "restore", archive, "--confirm")
	_, reopened, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(targetData, "platform.db"), DataDir: targetData})
	must(err)
	defer reopened.Close()
	// Reopen with target-local registries and recipes, as a target daemon would.
	// The source backend may still own stopped compute with the same logical ID.
	t.Setenv("HOME", target)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(target, "config"))
	targetConfig, err := config.LoadAppConfig()
	must(err)
	targetBackend, targetCleanup, err := sandbox.BackendFromAppConfig(targetConfig)
	must(err)
	if targetCleanup != nil {
		defer targetCleanup()
	}
	targetFiles, ok := targetBackend.(entstore.BotRestoreBackend)
	if !ok {
		t.Fatal("target restore backend unavailable")
	}
	must(reopened.InstallBotContentHomeBackend(targetFiles))
	targetSQL, err := sql.Open("sqlite", scopeDBPath(targetData))
	must(err)
	var restoredDefinition, restoredHash string
	must(targetSQL.QueryRowContext(ctx, `SELECT definition,content_hash FROM bot_versions WHERE bot_id=?`, id.String()).Scan(&restoredDefinition, &restoredHash))
	must(targetSQL.Close())
	if restoredDefinition != string(definition) || restoredHash != "hash" {
		t.Fatal("restore rewrote immutable SQL reference or hash")
	}
	restoredOrg, err := reopened.ForOrg(org.Slug)
	must(err)
	bots := restoredOrg.ForUser(owner.String()).Bots()
	if botScope == store.BotScopeTeam {
		bots = restoredOrg.ForTeam(team.Slug).Bots()
	}
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio}
	auth, err := execution.WithPrincipal(ctx, principal)
	must(err)
	restored, err := bots.Get(auth, id)
	must(err)
	targetHome := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindDocker, Ref: restored.Home.StorageKey}
	owned = append(owned, targetHome)
	if targetHome.Ref == home.Ref || restored.Status != store.BotPaused || restored.Home.ID != binding.ID || restored.Home.Revision != binding.Revision+1 {
		t.Fatal("restore changed logical identity, reused source or resumed execution")
	}
	if _, err := bots.Get(auth, id); err == nil {
		t.Fatal("restored authority activated without reconciliation")
	}
	restoredVersion, err := bots.GetVersion(auth, id, 1)
	must(err)
	if restoredVersion.Charter != "CLI-disposable-file-first-charter" {
		t.Fatal("reopened content differs")
	}
	snap, err := targetFiles.OpenBotContent(auth, targetHome, *artifact, store.BotFileRange{})
	must(err)
	snapshotBytes, err := io.ReadAll(snap)
	must(err)
	must(snap.Close())
	if !bytes.Equal(body, snapshotBytes) {
		t.Fatal("snapshot bytes differ")
	}
	manager := &launcher.BotHomeManager{Backend: targetBackend.(sandbox.BotHomeBackend), Bots: bots, Scope: botScope}
	svc := &store.Services{PersonalBots: bots, BotFilesFactory: func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return manager, nil }}
	if botScope == store.BotScopeTeam {
		svc.Bots = bots
		svc.PersonalBots = nil
	}
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}/files", api.BotFilesHandler)
	router.HandleFunc("/bots/{botID}/download", api.BotFileDownloadHandler)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
		router.ServeHTTP(w, r.WithContext(store.WithServices(c, svc)))
	}))
	defer server.Close()
	listing, err := server.Client().Get(server.URL + "/bots/" + id.String() + "/files?path=reports&scope=" + string(botScope))
	must(err)
	listed, err := io.ReadAll(listing.Body)
	must(err)
	must(listing.Body.Close())
	if listing.StatusCode != http.StatusOK || !bytes.Contains(listed, []byte("large.bin")) {
		t.Fatalf("reopened file tree status=%d body=%s", listing.StatusCode, listed)
	}
	response, err := server.Client().Get(server.URL + "/bots/" + id.String() + "/download?path=reports/large.bin&scope=" + string(botScope))
	must(err)
	download, err := io.ReadAll(response.Body)
	must(err)
	must(response.Body.Close())
	if response.StatusCode != 200 || !bytes.Equal(body, download) {
		t.Fatalf("reopened API download status=%d bytes=%d", response.StatusCode, len(download))
	}
	if stoppedSource {
		after, err := exec.CommandContext(ctx, "docker", "inspect", "--format={{.Id}} {{.State.Running}}", sourceCompute).Output()
		must(err)
		if !bytes.Equal(after, stoppedIdentity) {
			t.Fatal("restore or reopened reads mutated source compute")
		}
	}
	attachments, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter=volume="+targetHome.Ref).Output()
	must(err)
	if strings.TrimSpace(string(attachments)) != "" {
		t.Fatal("restored reads left compute or storage attachments running")
	}
	t.Logf("public CLI create/verify/restore, stopped_source=%t, fresh Home=%s, reopened HTTP and protected snapshot bytes=%d exact", stoppedSource, targetHome.Ref, len(body))
}
