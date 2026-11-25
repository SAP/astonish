package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
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
	home, err := b.(sandbox.BotHomeBackend).EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
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
	files, ok := b.(sandbox.BotHomeFilesBackend)
	if !ok {
		t.Fatal("production backend lacks retained file capability")
	}
	barrier := func(commit func() error) error { return commit() }
	body := bytes.Repeat([]byte("retained-file-fixture\x00"), 1<<20)
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(body))
	entry, err := files.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "outputs/large.bin", SizeBytes: int64(len(body)), Body: bytes.NewReader(body), CreateOnly: true, BeforeCommit: barrier})
	if err != nil || entry.SHA256 != wantDigest {
		t.Fatalf("large write: %+v %v", entry, err)
	}
	published, err := files.PublishBotArtifactSnapshot(ctx, home, entry.Path, "large.bin", barrier)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DestroySession(ctx, id); err != nil {
		t.Fatal(err)
	}
	destroyed = true
	pool.Remove(id)
	page, err := files.ListBotHomeFiles(ctx, home, "outputs", "", 100)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Path != entry.Path {
		t.Fatalf("offline listing: %+v %v", page, err)
	}
	stream, err := files.OpenBotHomeFile(ctx, home, entry.Path, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || n != int64(len(body)) || fmt.Sprintf("%x", hash.Sum(nil)) != wantDigest {
		t.Fatalf("offline stream: bytes=%d read=%v close=%v", n, readErr, closeErr)
	}
	_, err = files.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: entry.Path, SizeBytes: 7, Body: strings.NewReader("changed"), ExpectedSHA256: entry.SHA256, BeforeCommit: barrier})
	if err != nil {
		t.Fatal(err)
	}
	stream, err = files.OpenBotContent(ctx, home, *published, store.BotFileRange{Offset: 13, Length: 100})
	if err != nil {
		t.Fatal(err)
	}
	part, readErr := io.ReadAll(stream)
	closeErr = stream.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(part, body[13:113]) {
		t.Fatalf("immutable range after working edit: %v %v", readErr, closeErr)
	}
	data, err := b.(sandbox.BotHomeArtifactBackend).ReadBotHomeArtifact(ctx, home, marker, 1024)
	if err != nil || string(data) != "retained-through-real-node\n" {
		t.Fatalf("independent offline read: %q %v receipt=%s", data, err, receipt)
	}
	nextID := id + "-next"
	t.Cleanup(func() {
		pool.Remove(nextID)
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := b.DestroySession(cleanup, nextID); err != nil {
			t.Errorf("cleanup replacement session: %v", err)
		}
	})
	if err := sandbox.BindBotHome(pool, nextID, home); err != nil {
		t.Fatal(err)
	}
	next := pool.GetOrCreateWithTemplate(nextID, sandbox.BaseTemplateID)
	if err := sandbox.EnsureToolClientReady(ctx, next, nextID); err != nil {
		t.Fatal(err)
	}
	result, err := b.Exec(ctx, nextID, sandbox.ExecSpec{Command: []string{"sh", "-c", "test \"$(cat /root/outputs/large.bin)\" = changed && test -f /root/production-workspace-marker"}})
	if err != nil || result == nil || result.ExitCode != 0 {
		t.Fatalf("replacement session retained files: %+v %v", result, err)
	}
	t.Logf("physical=%s workspace=%s streamed=%d sha256=%s; offline listing, immutable range and replacement-session remount verified", strings.TrimSpace(string(container)), workspace, n, wantDigest)
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
