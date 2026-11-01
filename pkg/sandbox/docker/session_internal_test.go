package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestStopSessionPersistFailureLeavesContainerAndVolumeIntact(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "docker.log")
	binary := writeDockerTestScript(t, logPath, `
case "$1" in
inspect) printf 'running\n' ;;
exec) case "$*" in *'upper.tar.zst'*) exit 42 ;; *) exit 0 ;; esac ;;
*) printf '%s\n' "$*" >> "$DOCKER_TEST_LOG" ;;
esac
`)
	t.Setenv("DOCKER_TEST_LOG", logPath)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: binary}}

	err := db.StopSession(context.Background(), "persist-fails")
	if err == nil || !strings.Contains(err.Error(), "persist upper") {
		t.Fatalf("StopSession() error = %v, want persist error", err)
	}
	assertDockerTestLogExcludes(t, logPath, "rm -f -v")
	assertDockerTestLogExcludes(t, logPath, "volume rm -f")
}

func TestStopSessionWaitsForOverlayBeforeArchive(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	binary := writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1" = inspect ]; then echo running; fi
if [ "$1" = exec ]; then exit 1; fi`)
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: binary}}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := db.StopSession(ctx, "not-ready"); err == nil {
		t.Fatal("unready overlay archived")
	}
	assertDockerTestLogExcludes(t, log, "upper.tar.zst")
	assertDockerTestLogExcludes(t, log, "stop ")
}

func TestStopSessionPersistSuccessRetainsOverlayVolume(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "docker.log")
	binary := writeDockerTestScript(t, logPath, `
printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1" = inspect ]; then printf 'running\n'; fi
`)
	t.Setenv("DOCKER_TEST_LOG", logPath)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: binary}}

	if err := db.StopSession(context.Background(), "persist-succeeds"); err != nil {
		t.Fatalf("StopSession() error = %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker log: %v", err)
	}
	logText := string(data)
	cname := containerName("persist-succeeds")
	assertDockerTestLogExcludes(t, logPath, "volume rm")
	assertDockerTestLogExcludes(t, logPath, "rm -f -v")
	for _, want := range []string{"exec " + cname, "stop " + cname} {
		if !strings.Contains(logText, want) {
			t.Errorf("docker log missing %q:\n%s", want, logText)
		}
	}
}

func TestStopSessionAlreadyGoneIsIdempotent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "docker.log")
	binary := writeDockerTestScript(t, logPath, `
if [ "$1" = inspect ]; then echo 'No such container' >&2; exit 1; fi
printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
`)
	t.Setenv("DOCKER_TEST_LOG", logPath)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: binary}}

	if err := db.StopSession(context.Background(), "already-gone"); err != nil {
		t.Fatalf("StopSession() error = %v", err)
	}
	assertDockerTestLogExcludes(t, logPath, "rm")
}

func writeDockerTestScript(t *testing.T, logPath, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-test")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write docker test script: %v", err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("initialize docker test log: %v", err)
	}
	return path
}

func assertDockerTestLogExcludes(t *testing.T, path, unwanted string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read docker test log: %v", err)
	}
	if strings.Contains(string(data), unwanted) {
		t.Fatalf("docker log contains %q:\n%s", unwanted, data)
	}
}

func TestDockerRunArgs_OverlayContract(t *testing.T) {
	db := &DockerBackend{cfg: Config{
		LayersDir:    "/tmp/layers",
		UppersDir:    "/tmp/uppers",
		SandboxImage: "ghcr.io/sap/astonish-sandbox-base:latest",
	}}
	spec := sandbox.SessionSpec{
		SessionID:  "sess-1",
		Type:       sandbox.SessionTypeChat,
		LayerChain: []string{"seed"},
	}
	args := db.dockerRunArgs(spec, containerName(spec.SessionID), "seed", "/tmp/uppers/sess-1")
	joined := strings.Join(args, "\n")

	mustContain := []string{
		mountLayers,
		mountUppers,
		mountOverlay,
		envLayerChain + "=seed",
		envUpperDir + "=" + mountUpper,
		envWorkDir + "=" + mountWork,
		envHandoff + "=/bin/sleep",
		"--privileged",
		"--device=/dev/fuse",
	}
	for _, want := range mustContain {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run args missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--entrypoint") {
		t.Error("must not skip the sandbox-base entrypoint")
	}
	if strings.Contains(joined, "/mnt/astonish-uppers/sess-1") && strings.Contains(joined, mountOverlay) {
		// host persist dir is bound at mountUppers, not as overlay upperdir
	}
	// Live overlay upperdir must be the named overlay volume, not the host persist bind.
	wantVol := overlayVolumeName(spec.SessionID) + ":" + mountOverlay
	if !strings.Contains(joined, wantVol) {
		t.Errorf("docker run args missing overlay volume %q\n%s", wantVol, joined)
	}
	if len(args) < 2 || args[0] != "container" || args[1] != "run" {
		t.Errorf("docker run args must use management form `container run`, got %v", args[:min(2, len(args))])
	}
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasSuffix(args[i+1], ":"+mountUpper) {
			t.Errorf("must not bind host path onto live overlay upperdir: %s", args[i+1])
		}
	}
	assertWrapperSafeCLI(t, args, db.cfg.SandboxImage)
}

func TestDockerRunArgs_A2ASessionIDUsesStructuredUpperMount(t *testing.T) {
	db := &DockerBackend{cfg: Config{
		LayersDir:    "/tmp/layers",
		UppersDir:    "/tmp/uppers",
		SandboxImage: "ghcr.io/sap/astonish-sandbox-base:latest",
	}}
	spec := sandbox.SessionSpec{
		SessionID: "a2a:direct:ast_client:context-id",
		Type:      sandbox.SessionTypeChat,
	}
	upperDir := db.upperPath(spec.SessionID)
	args := db.dockerRunArgs(spec, containerName(spec.SessionID), "seed", upperDir)

	shortMount := "--volume=" + upperDir + ":" + mountUppers
	structuredMount := "--mount=type=bind,source=" + upperDir + ",target=" + mountUppers
	for _, arg := range args {
		if arg == shortMount {
			t.Fatalf("A2A session ID must not use ambiguous short volume syntax: %q", arg)
		}
		if arg == structuredMount {
			return
		}
	}
	t.Fatalf("docker run args missing structured upper bind mount %q\n%s", structuredMount, strings.Join(args, "\n"))
}

func TestSeedCreateArgs_WrapperSafe(t *testing.T) {
	image := "ghcr.io/sap/astonish-sandbox-base:latest"
	args := seedCreateArgs("astonish-seed-base", image)
	if len(args) < 2 || args[0] != "container" || args[1] != "create" {
		t.Fatalf("seed argv must use `container create`, got %v", args)
	}
	assertWrapperSafeCLI(t, args, image)
}

func TestLXCApparmorWrapper_TopLevelCreateBreaksNameValue(t *testing.T) {
	// The host wrapper that caused `astonish sandbox init` to fail with
	// "invalid reference format" rewrites top-level create/run and treats
	// the first non-dash token as the image. Split `--name value` is what
	// it mishandles; `container create --name=...` must pass through.
	broken := []string{"create", "--name", "astonish-seed-base", "ghcr.io/sap/astonish-sandbox-base:latest"}
	rewritten := lxcApparmorRewrite(broken)
	if firstNonDash(rewritten[1:]) != "apparmor=unconfined" {
		t.Fatalf("expected wrapper to treat apparmor=unconfined as the image, got %v", rewritten)
	}
	safe := seedCreateArgs("astonish-seed-base", "ghcr.io/sap/astonish-sandbox-base:latest")
	if got := lxcApparmorRewrite(safe); !equalStrings(got, safe) {
		t.Fatalf("wrapper must pass through container create, got %v", got)
	}
	runArgs := (&DockerBackend{cfg: Config{
		LayersDir:    "/tmp/layers",
		UppersDir:    "/tmp/uppers",
		SandboxImage: "ghcr.io/sap/astonish-sandbox-base:latest",
	}}).dockerRunArgs(sandbox.SessionSpec{SessionID: "sess-1"}, "astonish-session-sess-1", "seed", "/tmp/uppers/sess-1")
	if got := lxcApparmorRewrite(runArgs); !equalStrings(got, runArgs) {
		t.Fatalf("wrapper must pass through container run, got %v", got)
	}
}

func assertWrapperSafeCLI(t *testing.T, args []string, image string) {
	t.Helper()
	if len(args) < 2 || args[0] != "container" {
		t.Fatalf("want container <verb> ..., got %v", args)
	}
	verbOff := 2
	for _, a := range args[verbOff:] {
		if a == image {
			break
		}
		if !strings.HasPrefix(a, "-") {
			t.Errorf("non-flag token %q before image; wrappers treat this as the image", a)
			continue
		}
		if a == "--name" || a == "-v" || a == "-e" || a == "-d" || a == "--device" || a == "--volume" || a == "--env" || a == "--label" || a == "--network" {
			t.Errorf("split flag %q; use --flag=value so wrappers do not eat the value", a)
		}
	}
	if firstNonDash(args[verbOff:]) != image {
		t.Errorf("first non-flag after verb = %q, want image %q\n%s", firstNonDash(args[verbOff:]), image, strings.Join(args, " "))
	}
}

func firstNonDash(args []string) string {
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func lxcApparmorRewrite(args []string) []string {
	if len(args) == 0 || (args[0] != "run" && args[0] != "create") {
		return args
	}
	for i, tok := range args[1:] {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		out := []string{"run"}
		out = append(out, args[1:i+1]...)
		out = append(out, "--security-opt", "apparmor=unconfined")
		out = append(out, args[i+1:]...)
		return out
	}
	return args
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestContainerName_DoesNotTruncateTo12(t *testing.T) {
	a := containerName("aaaaaaaaaaaa-one")
	b := containerName("aaaaaaaaaaaa-two")
	if a == b {
		t.Fatalf("names collided after truncation: %s", a)
	}
	if !strings.HasPrefix(a, "astonish-session-") {
		t.Errorf("name = %q", a)
	}
}

func TestResolveLayerChain_RequiresRootfs(t *testing.T) {
	db := &DockerBackend{cfg: Config{LayersDir: t.TempDir(), UppersDir: t.TempDir()}}
	if _, err := db.resolveLayerChain(nil, sandbox.BaseTemplateID); err == nil {
		t.Fatal("expected error when no layers exist")
	}
}

func TestRecordSession_SetsContainerNameAndPodName(t *testing.T) {
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	reg := sandbox.NewSessionRegistryFromStore(st)
	db := &DockerBackend{cfg: Config{Sessions: reg}}
	spec := sandbox.SessionSpec{SessionID: "c1b27dfb-f828-40f2-b510-c8e85df3cda7"}
	cname := containerName(spec.SessionID)
	if err := db.recordSession(spec, cname, sandbox.BaseTemplateID); err != nil {
		t.Fatal(err)
	}

	rec, err := reg.GetSession(spec.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil {
		t.Fatal("GetSession returned nil")
	}
	if rec.ContainerName != cname {
		t.Errorf("ContainerName = %q, want %q", rec.ContainerName, cname)
	}
	if rec.PodName != cname {
		t.Errorf("PodName = %q, want %q (browser resolve historically required PodName)", rec.PodName, cname)
	}
	if rec.Backend != string(sandbox.BackendKindDocker) {
		t.Errorf("Backend = %q, want docker", rec.Backend)
	}
	if rec.State != store.SandboxSessionStateRunning {
		t.Errorf("State = %q, want running", rec.State)
	}
}

func TestDockerPSEntry_MissingCreatedIsNotAncient(t *testing.T) {
	sess := (&dockerPSEntry{
		Names:  "astonish-session-x",
		State:  "running",
		Labels: "astonish.session_id=abc",
	}).toSession()
	if sess == nil {
		t.Fatal("toSession returned nil")
	}
	if sess.CreatedAt.IsZero() {
		t.Fatal("missing Created must not be the zero time")
	}
	if time.Since(sess.CreatedAt) > time.Minute {
		t.Fatalf("missing Created parsed as %s, want approximately now", sess.CreatedAt)
	}
}

func TestRecordSession_SetsCreatedBy(t *testing.T) {
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	reg := sandbox.NewSessionRegistryFromStore(st)
	db := &DockerBackend{cfg: Config{Sessions: reg}}
	spec := sandbox.SessionSpec{SessionID: "owner-session", UserID: "user-owner"}
	if err := db.recordSession(spec, containerName(spec.SessionID), sandbox.BaseTemplateID); err != nil {
		t.Fatal(err)
	}

	rec, err := reg.GetSession(spec.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil {
		t.Fatal("GetSession returned nil")
	}
	if rec.CreatedBy != "user-owner" {
		t.Errorf("CreatedBy = %q, want user-owner", rec.CreatedBy)
	}
}

func TestAlreadyStoppedRetainsContainerAndVolume(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	binary := writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1" = inspect ]; then printf 'exited\n'; fi`)
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: binary}}
	if err := db.StopSession(t.Context(), "stopped"); err != nil {
		t.Fatal(err)
	}
	assertDockerTestLogExcludes(t, log, "exec ")
	assertDockerTestLogExcludes(t, log, "rm ")
	assertDockerTestLogExcludes(t, log, "stop ")
}

func TestStartSessionUnpausesContainer(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	binary := writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1" = inspect ]; then printf 'paused\n'; fi`)
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: binary}}
	if err := db.StartSession(t.Context(), "paused"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(raw), "unpause astonish-session-") {
		t.Fatalf("unpause not called: %s %v", raw, err)
	}
	assertDockerTestLogExcludes(t, log, "start ")
}

func TestLegacyRecoveryPreservesRegistryIdentity(t *testing.T) {
	db, log := recoveryTestBackend(t)
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db.cfg.Sessions = sandbox.NewSessionRegistryFromStore(st)
	id := "legacy-chain"
	if err := db.cfg.Sessions.PutSession(&store.SandboxSession{SessionID: id, Backend: "docker", CreatedBy: "owner", TemplateID: "original-template", Image: "historical-image"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(db.upperPath(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db.upperPath(id), layerChainFile), []byte("old-chain\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.recreateFromPersist(t.Context(), id); err == nil {
		t.Fatal("legacy recovery without current configuration must fail")
	}
	spec := sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: "different-owner", TemplateID: "new-template", LayerChain: []string{"new-chain"}}
	if _, err := db.CreateSession(t.Context(), spec); err == nil {
		t.Fatal("owner mismatch accepted")
	}
	spec.UserID = "owner"
	if _, err := db.CreateSession(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	m, err := db.readSessionMetadata(id)
	if err != nil || m.TemplateID != "original-template" || m.Image != "historical-image" || m.UserID != "owner" || strings.Join(m.LayerChain, ",") != "old-chain" {
		t.Fatalf("legacy metadata %+v %v", m, err)
	}
	assertDockerTestLogExcludes(t, log, "volume rm")
}

func TestLegacyContainerAdoptsStructureWithoutSecrets(t *testing.T) {
	db, log := recoveryTestBackend(t)
	db.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, `
if [ "$1" = image ]; then printf '%s\n' '["PATH=/bin"]'; exit; fi
if [ "$3" = '{{.State.Status}}' ]; then echo running; exit; fi
printf '%s\n' '{"Config":{"Image":"legacy-image","Labels":{"astonish.session_id":"legacy-container","astonish.type":"chat","astonish.org":"org","astonish.team":"team","custom":"label"},"Env":["PATH=/bin","ASTONISH_LAYER_CHAIN=old-chain","TOKEN=never-serialize-me"]},"HostConfig":{"NanoCpus":2000000000,"Memory":268435456,"PidsLimit":64}}'
`)
	spec := sandbox.SessionSpec{SessionID: "legacy-container", UserID: "owner", TemplateID: "original-template"}
	if _, err := db.CreateSession(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	m, err := db.readSessionMetadata(spec.SessionID)
	if err != nil || m.Image != "legacy-image" || m.OrgSlug != "org" || m.TeamSlug != "team" || m.Limits.CPUs != 2 || m.Limits.MemoryMiB != 256 || m.Limits.PIDs != 64 || len(m.EnvKeys) != 1 || m.EnvKeys[0] != "TOKEN" {
		t.Fatalf("legacy metadata %+v %v", m, err)
	}
	raw, err := os.ReadFile(filepath.Join(db.upperPath(spec.SessionID), dockerSessionMetadataFile))
	if err != nil || strings.Contains(string(raw), "never-serialize-me") {
		t.Fatalf("secret in metadata or read error: %v", err)
	}
	if _, err := db.recoverySpec(m, spec, true); err == nil {
		t.Fatal("legacy recreation silently dropped required env")
	}
	spec.Env = map[string]string{"TOKEN": "fresh-value"}
	if _, err := db.recoverySpec(m, spec, true); err != nil {
		t.Fatal(err)
	}
}

func recoveryTestBackend(t *testing.T) (*DockerBackend, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "log")
	binary := writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1" = inspect ]; then echo 'No such container' >&2; exit 1; fi`)
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{networkRegistry: make(map[string]bool), cfg: Config{ContainerRuntimePath: binary, LayersDir: t.TempDir(), UppersDir: t.TempDir(), SandboxImage: "original-image"}}
	for _, layer := range []string{"old-chain", "new-chain"} {
		if err := os.MkdirAll(db.layerRootfs(layer), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return db, log
}
func TestCreateSessionRetainedVolumePreservesLayerChain(t *testing.T) {
	db, log := recoveryTestBackend(t)
	spec := sandbox.SessionSpec{SessionID: "retained", TemplateID: "template", LayerChain: []string{"old-chain"}}
	if _, err := db.CreateSession(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	spec.LayerChain = []string{"new-chain"}
	if _, err := db.CreateSession(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(db.upperPath(spec.SessionID), layerChainFile))
	if err != nil || string(raw) != "old-chain\n" {
		t.Fatalf("chain %q, %v", raw, err)
	}
	assertDockerTestLogExcludes(t, log, "volume rm")
	assertDockerTestLogExcludes(t, log, "ASTONISH_LAYER_CHAIN=new-chain")
}
func TestRecreateFromPersistPreservesLaunchMetadata(t *testing.T) {
	db, log := recoveryTestBackend(t)
	spec := sandbox.SessionSpec{SessionID: "metadata", Type: sandbox.SessionTypeChat, TemplateID: "original-template", LayerChain: []string{"old-chain"}, UserID: "owner", OrgSlug: "org", TeamSlug: "team", Labels: map[string]string{"custom": "label"}, Limits: sandbox.ResourceLimits{CPUs: 2, MemoryMiB: 256, PIDs: 64}, Env: map[string]string{"TOKEN": "secret-value"}}
	if _, err := db.CreateSession(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(db.upperPath(spec.SessionID), dockerSessionMetadataFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-value") {
		t.Fatal("secret persisted")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
	if err := db.recreateFromPersist(t.Context(), spec.SessionID); err != nil {
		t.Fatal(err)
	}
	reopened := &DockerBackend{cfg: db.cfg, networkRegistry: make(map[string]bool)}
	reopened.cfg.SandboxImage = "new-image"
	if err := reopened.recreateFromPersist(t.Context(), spec.SessionID); err == nil || !strings.Contains(err.Error(), "fresh authorized environment") {
		t.Fatalf("missing env should fail: %v", err)
	}
	if _, err := reopened.CreateSession(t.Context(), sandbox.SessionSpec{SessionID: spec.SessionID, Env: map[string]string{"TOKEN": "fresh-secret"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	for _, want := range []string{"original-image", "--cpus=2", "--memory=256m", "--pids-limit=64", "astonish.org=org", "astonish.team=team", "custom=label", "TOKEN=fresh-secret"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s", want)
		}
	}
	assertDockerTestLogExcludes(t, log, "new-image")
	m, err := reopened.readSessionMetadata(spec.SessionID)
	if err != nil || m.UserID != "owner" || m.TemplateID != "original-template" {
		t.Fatalf("metadata %+v %v", m, err)
	}
}
func TestRecordSessionPreservesOwnerPinnedPortsAndCreatedAt(t *testing.T) {
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := sandbox.NewSessionRegistryFromStore(st)
	created := time.Now().UTC().Add(-time.Hour)
	original := &store.SandboxSession{Backend: "docker", SessionID: "merge", ChatSessionID: "chat", TemplateID: "original", CreatedBy: "owner", Pinned: true, ExposedPorts: []int{8080}, CreatedAt: created, BaseDomain: "example.test"}
	if err := reg.PutSession(original); err != nil {
		t.Fatal(err)
	}
	db := &DockerBackend{cfg: Config{Sessions: reg}}
	if err := db.recordSession(sandbox.SessionSpec{SessionID: "merge", UserID: "replacement"}, "container", "new-template"); err != nil {
		t.Fatal(err)
	}
	rec, err := reg.GetSession("merge")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CreatedBy != "owner" || !rec.Pinned || len(rec.ExposedPorts) != 1 || rec.ExposedPorts[0] != 8080 || !rec.CreatedAt.Equal(created) || rec.TemplateID != "original" || rec.ChatSessionID != "chat" || rec.BaseDomain != "example.test" {
		t.Fatalf("lost metadata: %+v", rec)
	}
}
func TestDestroySessionVolumeFailurePreservesRecoveryMetadata(t *testing.T) {
	db, log := recoveryTestBackend(t)
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db.cfg.Sessions = sandbox.NewSessionRegistryFromStore(st)
	spec := sandbox.SessionSpec{SessionID: "destroy-retry", TemplateID: "old-chain", LayerChain: []string{"old-chain"}}
	if _, err := db.CreateSession(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	db.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, `if [ "$1" = volume ]; then echo 'volume in use' >&2; exit 1; fi`)
	if err := db.DestroySession(t.Context(), spec.SessionID); err == nil {
		t.Fatal("expected volume removal error")
	}
	if m, err := db.readSessionMetadata(spec.SessionID); err != nil || m == nil {
		t.Fatalf("metadata removed: %v", err)
	}
	if rec, err := db.cfg.Sessions.GetSession(spec.SessionID); err != nil || rec == nil {
		t.Fatalf("authority removed: %v", err)
	}
	db.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, `exit 0`)
	if err := db.DestroySession(t.Context(), spec.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db.upperPath(spec.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("persist retained after explicit destroy: %v", err)
	}
}
func TestSessionLifecycleSerializesAcrossBackendInstances(t *testing.T) {
	db, _ := recoveryTestBackend(t)
	other := &DockerBackend{cfg: db.cfg}
	unlock, err := db.lockSession(t.Context(), "same")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(entered)
		release, e := other.lockSession(t.Context(), "same")
		if e == nil {
			release()
		}
		done <- e
	}()
	<-entered
	select {
	case err := <-done:
		t.Fatalf("overlapping lifecycle: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lock not released")
	}
}
func TestLockHonorsCancellation(t *testing.T) {
	db, _ := recoveryTestBackend(t)
	unlock, err := db.lockSession(t.Context(), "same")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := db.lockSession(ctx, "same"); err != context.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

func TestDockerRequireRegistered_RejectsUnknownSession(t *testing.T) {
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	reg := sandbox.NewSessionRegistryFromStore(st)
	if err := reg.Put("known", "astonish-session-known", sandbox.BaseTemplateID); err != nil {
		t.Fatalf("Put: %v", err)
	}
	db := &DockerBackend{cfg: Config{
		Sessions:             reg,
		ContainerRuntimePath: "/nonexistent/docker-must-not-run",
	}}

	if _, err := db.PullFile(context.Background(), "foreign-session", "/tmp/secret"); err == nil {
		t.Fatal("PullFile of an unregistered session must fail before docker exec")
	}
	if _, err := db.Exec(context.Background(), "foreign-session", sandbox.ExecSpec{Command: []string{"id"}}); err == nil || !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("Exec of an unregistered session must fail at the registry gate, got %v", err)
	}
	if err := db.DestroySession(context.Background(), "foreign-session"); err == nil || !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("DestroySession of an unregistered session must fail at the registry gate, got %v", err)
	}
	if _, err := db.SessionState(context.Background(), "foreign-session"); err == nil {
		t.Fatal("SessionState of an unregistered session must fail before docker inspect")
	}
	// A registered ID passes the gate. The docker binary is fake, so the call
	// fails at exec, not at the registry check.
	_, pullErr := db.PullFile(context.Background(), "known", "/tmp/x")
	if pullErr == nil || strings.Contains(pullErr.Error(), "is not registered") {
		t.Fatalf("registered session must pass the registry gate, got %v", pullErr)
	}
}
