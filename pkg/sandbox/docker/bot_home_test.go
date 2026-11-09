package docker

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/sandbox"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBotHome() *sandbox.BotHomeBinding {
	s := sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: "user", BotID: "bot"}
	return &sandbox.BotHomeBinding{Scope: s, Backend: sandbox.BackendKindDocker, Ref: botHomeVolume(s)}
}
func homeVolumeJSON(b *sandbox.BotHomeBinding, driver string, options map[string]string, owner string) string {
	raw, _ := json.Marshal([]any{map[string]any{"Name": b.Ref, "Driver": driver, "Options": options, "Labels": map[string]string{botHomeScopeLabel: owner, botHomeVersionLabel: "1"}}})
	return string(raw)
}
func TestBotHomeVolumeOwnership(t *testing.T) {
	b := testBotHome()
	for _, tc := range []struct {
		name, driver, owner string
		options             map[string]string
		ok                  bool
	}{
		{"owned", "local", b.Scope.Key(), nil, true}, {"foreign", "local", "other", nil, false},
		{"remote", "nfs", b.Scope.Key(), nil, false}, {"host-bind", "local", b.Scope.Key(), map[string]string{"device": "/", "o": "bind"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "log")
			script := writeDockerTestScript(t, log, "printf '%s\\n' '"+homeVolumeJSON(b, tc.driver, tc.options, tc.owner)+"'")
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: script}}
			got, err := db.EnsureBotHome(t.Context(), b.Scope)
			if (err == nil) != tc.ok {
				t.Fatalf("EnsureBotHome = %+v, %v", got, err)
			}
		})
	}
	for _, testMount := range []struct {
		name, kind, volume string
		rw, ok             bool
	}{
		{"owned", "volume", b.Ref, true, true},
		{"foreign", "volume", "other", true, false},
		{"host-bind", "bind", b.Ref, true, false},
		{"readonly", "volume", b.Ref, false, false},
	} {
		t.Run("mount-"+testMount.name, func(t *testing.T) {
			data, _ := json.Marshal([]any{map[string]any{"Type": testMount.kind, "Name": testMount.volume, "Destination": botHomeMount, "RW": testMount.rw}})
			log := filepath.Join(t.TempDir(), "log")
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: writeDockerTestScript(t, log, "printf '%s\\n' '"+string(data)+"'")}}
			if err := db.validateBotHomeContainer(t.Context(), "run", b); (err == nil) != testMount.ok {
				t.Fatalf("mount validation = %v", err)
			}
		})
	}
}
func TestBotHomeCreateVolumeAndRejectArbitraryReference(t *testing.T) {
	b := testBotHome()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	t.Setenv("DOCKER_TEST_LOG", log)
	t.Setenv("HOME_CREATED", filepath.Join(dir, "created"))
	script := writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$2" = create ]; then touch "$HOME_CREATED"; exit 0; fi
if [ ! -f "$HOME_CREATED" ]; then echo 'No such volume' >&2; exit 1; fi
printf '%s\n' '`+homeVolumeJSON(b, "local", nil, b.Scope.Key())+"'")
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: script}}
	if _, err := db.EnsureBotHome(t.Context(), b.Scope); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "volume create --driver=local --label="+botHomeScopeLabel+"="+b.Scope.Key()) {
		t.Fatalf("wrong create: %s", data)
	}
	b.Ref = "/tmp/user-supplied"
	if db.validateBotHome(t.Context(), b) == nil {
		t.Fatal("accepted host path")
	}
}
func TestBotHomeMountOutsideOverlayAndOrdinaryUnchanged(t *testing.T) {
	db := &DockerBackend{cfg: Config{LayersDir: "/layers", SandboxImage: "image"}}
	b := testBotHome()
	spec := sandbox.SessionSpec{SessionID: "run", BotHome: b}
	args := strings.Join(db.dockerRunArgs(spec, "container", "base", "/uppers"), " ")
	if !strings.Contains(args, "--mount=type=volume,source="+b.Ref+",target="+botHomeMount+",volume-nocopy") || !strings.Contains(args, "--env=HOME=/root") {
		t.Fatalf("wrong home args: %s", args)
	}
	spec.BotHome = nil
	ordinary := strings.Join(db.dockerRunArgs(spec, "container", "base", "/uppers"), " ")
	if strings.Contains(ordinary, "bot-home") || strings.Contains(ordinary, "HOME=") {
		t.Fatalf("ordinary changed: %s", ordinary)
	}
	script := botHomeMountScript()
	for _, s := range []string{"mountpoint -q /sandbox/rootfs", "test ! -L /sandbox/rootfs/root", "mount --bind " + botHomeMount + " /sandbox/rootfs/root", "stat -c '%d:%i'", "chmod 0700"} {
		if !strings.Contains(script, s) {
			t.Errorf("missing guard %q", s)
		}
	}
	if strings.Contains(persistUpperScript(), botHomeMount) {
		t.Fatal("home included in capture")
	}
}
func TestBotHomeRecoveryConflictAndRetention(t *testing.T) {
	b := testBotHome()
	spec := sandbox.SessionSpec{SessionID: "run", Type: sandbox.SessionTypeChat, TemplateID: "base", LayerChain: []string{"base"}, Image: "image", BotHome: b}
	db := &DockerBackend{cfg: Config{UppersDir: t.TempDir()}}
	m := newDockerSessionMetadata(spec)
	if err := db.writeSessionMetadata(m); err != nil {
		t.Fatal(err)
	}
	m, err := db.readSessionMetadata("run")
	if err != nil || !m.BotHome.Equal(b) {
		t.Fatalf("metadata home lost: %+v %v", m, err)
	}
	restored, err := db.recoverySpec(m, sandbox.SessionSpec{SessionID: "run"}, true)
	if err != nil || !restored.BotHome.Equal(b) {
		t.Fatalf("recovery lost home: %+v %v", restored, err)
	}
	conflict := spec
	conflict.BotHome = b.Clone()
	conflict.BotHome.Scope.UserID = "foreign"
	if _, err := db.recoverySpec(m, conflict, false); err == nil {
		t.Fatal("conflicting recovery accepted")
	}
	if _, err := db.adoptContainerMetadata(t.Context(), spec, "container"); err == nil {
		t.Fatal("legacy adoption accepted home")
	}
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("DOCKER_TEST_LOG", log)
	db.cfg.ContainerRuntimePath = writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"`)
	if err := db.DestroySession(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), b.Ref) || !strings.Contains(string(data), "volume rm -f "+overlayVolumeName("run")) {
		t.Fatalf("destroy touched home or missed run: %s", data)
	}
}
func TestBotHomeOrdinaryExecPreparationDoesNotCallDocker(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{cfg: Config{UppersDir: t.TempDir(), ContainerRuntimePath: writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"; exit 1`)}}
	for _, id := range []string{"legacy", "ordinary"} {
		if id == "ordinary" {
			if err := db.writeSessionMetadata(newDockerSessionMetadata(sandbox.SessionSpec{SessionID: id, TemplateID: "base", LayerChain: []string{"base"}, Image: "image"})); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.prepareBotHomeExec(t.Context(), id); err != nil {
			t.Fatalf("ordinary prepare %s: %v", id, err)
		}
	}
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Fatalf("ordinary prepare invoked Docker: %s", data)
	}
}

func TestBotHomeExecFailsClosedBeforeDispatch(t *testing.T) {
	b := testBotHome()
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("DOCKER_TEST_LOG", log)
	db := &DockerBackend{cfg: Config{UppersDir: t.TempDir(), ContainerRuntimePath: writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$1" = volume ]; then printf '%s\n' '`+homeVolumeJSON(b, "local", nil, b.Scope.Key())+`'; exit; fi
printf '%s\n' '[]'`)}}
	spec := sandbox.SessionSpec{SessionID: "run", TemplateID: "base", LayerChain: []string{"base"}, Image: "image", BotHome: b}
	if err := db.writeSessionMetadata(newDockerSessionMetadata(spec)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		exec func() error
	}{
		{"buffered", func() error {
			_, err := db.Exec(t.Context(), "run", sandbox.ExecSpec{Command: []string{"must-not-execute"}})
			return err
		}},
		{"interactive", func() error {
			_, err := db.ExecInteractive(t.Context(), "run", sandbox.PTYSpec{Command: []string{"must-not-execute"}})
			return err
		}},
		{"streaming", func() error {
			_, err := db.ExecStreaming(t.Context(), "run", sandbox.ExecStreamSpec{Command: []string{"must-not-execute"}})
			return err
		}},
		{"push", func() error {
			return db.PushFile(t.Context(), "run", "/root/must-not-execute", strings.NewReader("data"), 0600)
		}},
		{"pull", func() error { _, err := db.PullFile(t.Context(), "run", "/root/must-not-execute"); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.exec(); err == nil {
				t.Fatal("operation fell back without mounted home")
			}
		})
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "must-not-execute") {
		t.Fatalf("dispatch before mount: %s", data)
	}
}
