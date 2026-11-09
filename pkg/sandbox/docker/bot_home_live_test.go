package docker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// botHomeAcceptanceInputs never supplies defaults for an explicitly requested
// live acceptance. In particular, missing prerequisites must not become a skip.
func botHomeAcceptanceInputs(image, layers string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("ASTONISH_SANDBOX_IMAGE must name an existing local acceptance image")
	}
	if !filepath.IsAbs(layers) {
		return fmt.Errorf("ASTONISH_BOT_HOME_TEST_LAYERS must name an absolute existing Docker layers directory")
	}
	if st, err := os.Stat(filepath.Join(layers, sandbox.BaseTemplateID, "rootfs", "bin", "sh")); err != nil || st.IsDir() {
		return fmt.Errorf("ASTONISH_BOT_HOME_TEST_LAYERS must contain @base/rootfs/bin/sh")
	}
	return nil
}

func TestBotHomeAcceptanceInputs(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, image, layers string }{
		{"missing image", "", root}, {"missing layers", "local-image", ""},
		{"relative layers", "local-image", "layers"}, {"unseeded layers", "local-image", root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := botHomeAcceptanceInputs(tc.image, tc.layers); err == nil {
				t.Fatal("missing prerequisite accepted")
			}
		})
	}
	bin := filepath.Join(root, sandbox.BaseTemplateID, "rootfs", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sh"), nil, 0700); err != nil {
		t.Fatal(err)
	}
	if err := botHomeAcceptanceInputs("explicit-local-image", root); err != nil {
		t.Fatal(err)
	}
}

// Kept as a direct opt-in entry point; the designated persistence acceptance
// dispatches to exactly the same production-factory/registry fixture.
func TestBotHomeDockerPersistenceLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 for isolated real-Docker acceptance")
	}
	runBotHomeDockerPersistence(t)
}

func runBotHomeDockerPersistence(t *testing.T) {
	t.Helper()
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if err := botHomeAcceptanceInputs(image, layers); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dockerCommand := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// Resolve once: no implicit pulls and no mutable-tag changes during the test.
	image = dockerCommand("image", "inspect", "--format", "{{.Id}}", image)
	t.Logf("Docker server=%s image=%s layers=%s (read-only)", dockerCommand("version", "--format", "{{.Server.Version}}"), image, layers)
	// Docker Desktop shares the checkout, not necessarily macOS TMPDIR.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
	root := t.TempDir()
	openBackend := func() (sandbox.Backend, *sandbox.SessionRegistry) {
		t.Helper()
		st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
		if err != nil {
			t.Fatal(err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{
			Kind: sandbox.BackendKindDocker, Sessions: reg,
			Docker: sandbox.DockerRuntimeConfig{SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b, reg
	}
	b, reg := openBackend()
	nonce := fmt.Sprintf("home-live-%d", time.Now().UnixNano())
	first, second, other := nonce+"-a", nonce+"-b", nonce+"-other"
	var volumes []string
	var containers []string
	// Cleanup is bounded, checked, and limited to resources owned by this test.
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		for _, id := range []string{first, second, other} {
			if reg.Get(id) != nil {
				if err := b.DestroySession(cleanupCtx, id); err != nil {
					t.Errorf("cleanup session %s: %v", id, err)
				}
			}
			// Creation can fail after Docker launched but before registration.
			// The exact, uniquely generated session label is our cleanup boundary.
			out, err := exec.CommandContext(cleanupCtx, "docker", "ps", "-aq", "--filter", "label=astonish.session_id="+id).CombinedOutput()
			if err != nil {
				t.Errorf("find partial session %s: %v: %s", id, err, out)
				continue
			}
			for _, container := range strings.Fields(string(out)) {
				mounts, err := exec.CommandContext(cleanupCtx, "docker", "inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/var/astonish/overlay"}}{{.Name}}{{end}}{{end}}`, container).CombinedOutput()
				if err != nil {
					t.Errorf("inspect partial session %s: %v: %s", id, err, mounts)
					continue
				}
				if out, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-fv", container).CombinedOutput(); err != nil {
					t.Errorf("remove partial session %s: %v: %s", id, err, out)
					continue
				}
				if volume := strings.TrimSpace(string(mounts)); volume != "" {
					if out, err := exec.CommandContext(cleanupCtx, "docker", "volume", "rm", volume).CombinedOutput(); err != nil {
						t.Errorf("remove partial overlay %s: %v: %s", volume, err, out)
					}
				}
			}
		}
		for _, volume := range volumes {
			if out, err := exec.CommandContext(cleanupCtx, "docker", "volume", "rm", volume).CombinedOutput(); err != nil {
				t.Errorf("cleanup home %s: %v: %s", volume, err, out)
			}
		}
	})
	ensure := func(scope sandbox.BotHomeScope) *sandbox.BotHomeBinding {
		t.Helper()
		capability, ok := b.(sandbox.BotHomeBackend)
		if !ok {
			t.Fatal("Docker backend missing durable-home capability")
		}
		home, err := capability.EnsureBotHome(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		return home
	}
	scope := sandbox.BotHomeScope{UserID: "physical-test", BotID: nonce}
	home := ensure(scope)
	volumes = append(volumes, home.Ref)
	otherHome := ensure(sandbox.BotHomeScope{UserID: scope.UserID, BotID: nonce + "-other"})
	volumes = append(volumes, otherHome.Ref)
	if home.Ref == otherHome.Ref {
		t.Fatal("different Bots share a home")
	}
	create := func(id string, binding *sandbox.BotHomeBinding) string {
		t.Helper()
		if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: scope.UserID, BotHome: binding}); err != nil {
			t.Fatal(err)
		}
		entry := reg.Get(id)
		if entry == nil {
			t.Fatal("production session registration missing")
		}
		if err := b.WaitForSessionReady(ctx, id); err != nil {
			t.Fatalf("ready: %v\n%s", err, dockerCommand("logs", entry.ContainerName))
		}
		physicalID := dockerCommand("inspect", "--format", "{{.Id}}", entry.ContainerName)
		for _, prior := range containers {
			if prior == physicalID {
				t.Fatal("run reused a physical container")
			}
		}
		containers = append(containers, physicalID)
		mountJSON := dockerCommand("inspect", "--format", "{{json .Mounts}}", entry.ContainerName)
		var mounts []struct {
			Type, Source, Name, Destination string
			RW                              bool
		}
		if err := json.Unmarshal([]byte(mountJSON), &mounts); err != nil {
			t.Fatal(err)
		}
		var readOnlyLayers, boundHome bool
		for _, mount := range mounts {
			if mount.Destination == "/mnt/astonish-layers" {
				readOnlyLayers = mount.Type == "bind" && mount.Source == layers && !mount.RW
			}
			if mount.Destination == "/mnt/astonish-bot-home" {
				boundHome = mount.Type == "volume" && mount.Name == binding.Ref && mount.RW
			}
		}
		if !readOnlyLayers || !boundHome {
			t.Fatalf("unexpected isolation mounts: %s", mountJSON)
		}
		t.Logf("session=%s container=%s mounts=%s", id, physicalID, mountJSON)
		return physicalID
	}
	check := func(id, script string) {
		t.Helper()
		res, err := b.Exec(ctx, id, sandbox.ExecSpec{Command: []string{"/bin/sh", "-c", script}})
		if err != nil || res == nil || res.ExitCode != 0 {
			t.Fatalf("exec %s: %+v %v", id, res, err)
		}
	}
	a := create(first, home)
	check(first, `test "$PWD" = /root && printf relative > relative-marker`)
	for _, dir := range []string{"", "/tmp"} {
		stream, err := b.ExecStreaming(ctx, first, sandbox.ExecStreamSpec{WorkDir: dir, Command: []string{"/usr/local/bin/astonish-shell", "/bin/sh", "-c", `test "$PWD" = "$1" && printf stream > stream-marker`, "cwd-check", map[bool]string{true: "/root", false: "/tmp"}[dir == ""]}})
		if err != nil {
			t.Fatal(err)
		}
		code, err := stream.Wait()
		stream.Close()
		if err != nil || code != 0 {
			t.Fatalf("stream cwd %q: %d %v", dir, code, err)
		}
	}
	check(first, `test "$(cat /root/stream-marker)" = stream && test "$(cat /tmp/stream-marker)" = stream`)
	check(first, `test "$HOME" = /root && printf 'durable-marker' > "$HOME/marker" && printf 'ephemeral' > /tmp/run-only`)
	if err := b.DestroySession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "inspect", a).CombinedOutput(); err == nil || !strings.Contains(strings.ToLower(string(out)), "no such") {
		t.Fatalf("run A container was not destroyed: %s %v", out, err)
	}
	// Reopen both registry and backend; obtain the home from its server scope,
	// rather than reusing an in-memory binding from the previous backend.
	b, reg = openBackend()
	reopened := ensure(scope)
	if !home.Equal(reopened) {
		t.Fatal("reopened home binding changed")
	}
	create(second, reopened)
	check(second, `test "$PWD" = /root && test "$(cat relative-marker)" = relative && test "$(cat stream-marker)" = stream && test ! -e /tmp/stream-marker`)
	check(second, `test "$HOME" = /root && test "$(cat "$HOME/marker")" = durable-marker && test ! -e /tmp/run-only`)
	create(other, otherHome)
	check(other, `test ! -e "$HOME/marker" && test ! -e /tmp/run-only && printf other-bot > "$HOME/marker"`)
	check(second, `test "$(cat "$HOME/marker")" = durable-marker && ln -s /etc/passwd "$HOME/escape" && ln -s marker "$HOME/local-link" && mkdir "$HOME/folder"`)
	reader := b.(sandbox.BotHomeArtifactBackend)
	deleter := b.(sandbox.BotHomeDeleteBackend)
	if err := deleter.DeleteBotHome(ctx, home); err == nil {
		t.Fatal("deleted attached home")
	}
	if err := b.DestroySession(ctx, second); err != nil {
		t.Fatal(err)
	}
	if reg.Get(first) != nil || reg.Get(second) != nil {
		t.Fatal("destroyed run resurrected")
	}
	for _, physical := range containers[:2] {
		if out, err := exec.CommandContext(ctx, "docker", "inspect", physical).CombinedOutput(); err == nil || !strings.Contains(strings.ToLower(string(out)), "no such") {
			t.Fatalf("container survives: %s %v", out, err)
		}
	}
	for _, path := range []string{"marker", "local-link"} {
		data, err := reader.ReadBotHomeArtifact(ctx, home, path, 14)
		if err != nil || string(data) != "durable-marker" {
			t.Fatalf("offline artifact %q: %q %v", path, data, err)
		}
	}
	for _, path := range []string{"../etc/passwd", "/etc/passwd", "escape", "folder", "missing"} {
		if data, err := reader.ReadBotHomeArtifact(ctx, home, path, 1024); err == nil || len(data) != 0 {
			t.Fatalf("unsafe read %q: %q %v", path, data, err)
		}
	}
	if _, err := reader.ReadBotHomeArtifact(ctx, home, "marker", 3); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	foreign := home.Clone()
	foreign.Ref = otherHome.Ref
	if err := deleter.DeleteBotHome(ctx, foreign); err == nil {
		t.Fatal("foreign home deleted")
	}
	if err := deleter.DeleteBotHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	volumes = volumes[1:] // successful product purge owns this cleanup
	if err := deleter.DeleteBotHome(ctx, home); err != nil {
		t.Fatalf("idempotent purge: %v", err)
	}
	if _, err := reader.ReadBotHomeArtifact(ctx, home, "marker", 1024); err == nil {
		t.Fatal("missing home recreated")
	}
	if out := dockerCommand("volume", "ls", "-q", "--filter", "name="+home.Ref); out != "" {
		t.Fatalf("purged volume survived: %s", out)
	}
	if out := dockerCommand("ps", "-aq", "--filter", "label=astonish.bot-home.scope="+scope.Key(), "--filter", "label=astonish.bot-home.reader"); out != "" {
		t.Fatalf("leaked helper: %s", out)
	}
	check(other, `test "$(cat "$HOME/marker")" = other-bot`)
	t.Log("physical Docker: both runs destroyed; offline artifact bytes and bounded/symlink guards verified; owned home purged idempotently; other Bot retained; no reader helpers leaked")
}
