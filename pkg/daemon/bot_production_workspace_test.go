package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

type workspaceProbeBackend struct {
	sandbox.Backend
	result  *sandbox.ExecResult
	err     error
	session string
}

func (b *workspaceProbeBackend) Exec(_ context.Context, id string, _ sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	b.session = id
	return b.result, b.err
}
func TestProductionBotWorkspaceUsesBoundHome(t *testing.T) {
	for _, home := range []string{"/root", "/home/astonish"} {
		b := &workspaceProbeBackend{result: &sandbox.ExecResult{Stdout: []byte(home)}}
		got, err := productionBotWorkspaceDir(context.Background(), b, "physical-run")
		if err != nil || got != home || b.session != "physical-run" {
			t.Fatalf("home=%q got=%q session=%q err=%v", home, got, b.session, err)
		}
	}
}
func TestProductionBotWorkspaceDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("requires explicit real Docker opt-in")
	}
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if image == "" || !filepath.IsAbs(layers) {
		t.Fatal("explicit image/layers required")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
	root := t.TempDir()
	st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindDocker, Sessions: sandbox.NewSessionRegistryFromStore(st), Docker: sandbox.DockerRuntimeConfig{SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id := fmt.Sprintf("workspace-proof-%d", time.Now().UnixNano())
	home, err := b.(sandbox.BotHomeBackend).EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: id, BotID: id})
	if err != nil {
		t.Fatal(err)
	}
	destroyed := false
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{})
	t.Cleanup(func() {
		pool.Remove(id)
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if !destroyed {
			if err := b.DestroySession(c, id); err != nil {
				t.Errorf("cleanup session: %v", err)
			}
		}
		if err := b.(sandbox.BotHomeDeleteBackend).DeleteBotHome(c, home); err != nil {
			t.Errorf("cleanup home: %v", err)
		}
	})
	if err := sandbox.BindBotHome(pool, id, home); err != nil {
		t.Fatal(err)
	}
	client := pool.GetOrCreateWithTemplate(id, sandbox.BaseTemplateID)
	if err := sandbox.EnsureToolClientReady(ctx, client, id); err != nil {
		t.Fatal(err)
	}
	workspace, err := productionBotWorkspaceDir(ctx, b, id)
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "/root" {
		t.Fatalf("workspace=%q", workspace)
	}
	container, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "label=astonish.session_id="+id).CombinedOutput()
	if err != nil || strings.TrimSpace(string(container)) == "" {
		t.Fatalf("container: %s %v", container, err)
	}
	mounts, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Mounts}}", strings.TrimSpace(string(container))).CombinedOutput()
	if err != nil || !strings.Contains(string(mounts), home.Ref) {
		t.Fatalf("mounts: %s %v", mounts, err)
	}
	const marker = "production-workspace-marker"
	receipt, err := client.Call(id, "write_file", map[string]any{"path": workspace + "/" + marker, "content": "retained-through-real-node\n"})
	if err != nil {
		t.Fatalf("write: %s %v", receipt, err)
	}
	if err := b.DestroySession(ctx, id); err != nil {
		t.Fatal(err)
	}
	destroyed = true
	data, err := b.(sandbox.BotHomeArtifactBackend).ReadBotHomeArtifact(ctx, home, marker, 1024)
	if err != nil || string(data) != "retained-through-real-node\n" {
		t.Fatalf("independent offline read: %q %v receipt=%s", data, err, receipt)
	}
	t.Logf("physical=%s workspace=%s retained bytes verified after destruction; mounts=%s", strings.TrimSpace(string(container)), workspace, mounts)
}

func TestProductionBotWorkspaceFailsClosed(t *testing.T) {
	for _, b := range []*workspaceProbeBackend{
		{err: errors.New("sandbox unavailable")},
		{},
		{result: &sandbox.ExecResult{ExitCode: 1, Stdout: []byte("/root")}},
		{result: &sandbox.ExecResult{Stdout: []byte("")}},
		{result: &sandbox.ExecResult{Stdout: []byte("relative")}},
		{result: &sandbox.ExecResult{Stdout: []byte("/")}},
		{result: &sandbox.ExecResult{Stdout: []byte("/root/../workspace")}},
		{result: &sandbox.ExecResult{Stdout: []byte("/root\n/workspace")}},
	} {
		if got, err := productionBotWorkspaceDir(context.Background(), b, "physical-run"); err == nil || got != "" {
			t.Fatalf("unexpected fallback %q: %v", got, err)
		}
	}
}
