package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type archiveBlockingWriter struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *archiveBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestBotHomeStartWithoutMetadataRejectsRetainedMount(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("DOCKER_TEST_LOG", log)
	script := writeDockerTestScript(t, log, `printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
if [ "$3" = '{{json .Mounts}}' ]; then printf '[{"Destination":"/mnt/astonish-bot-home","Type":"volume","Name":"foreign","RW":true}]\n'; else printf 'exited\n'; fi`)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: script, UppersDir: t.TempDir()}}
	if err := db.StartSession(t.Context(), "lost-metadata"); err == nil {
		t.Fatal("retained start bypassed admission")
	}
	assertDockerTestLogExcludes(t, log, "start ")
	assertDockerTestLogExcludes(t, log, "rm ")
}

func TestBotHomeAdmissionSubprocess(t *testing.T) {
	raw := os.Getenv("ASTONISH_ADMISSION_CHILD")
	if raw == "" {
		return
	}
	var b sandbox.BotHomeBinding
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")}}
	p, err := db.lockBotHomeAdmission(t.Context(), &b, true)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("LOCKED " + p.name)
	// Parent kills this process to test Docker StdinOnce / pipe closure, not a
	// cooperative release or a Go process-local mutex.
	select {}
}

func TestBotHomeAdmissionDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if _, err := runDocker(ctx, "docker", "image", "inspect", image); err != nil {
		t.Fatal(err)
	}
	if _, err := runDocker(ctx, "docker", "container", "run", "--rm", "--pull=never", "--network=none", "--entrypoint=/bin/sh", image, "-ec", "test -x /usr/local/bin/astonish-host; ! command -v python3; ! command -v python"); err != nil {
		t.Fatal("test-owned image must ship compiled node without Python", err)
	}
	cfg := Config{ContainerRuntimePath: "docker", SandboxImage: image, UppersDir: t.TempDir(), LayersDir: t.TempDir()}
	// Layer validation for a blocked create/recovery only; no overlay is launched.
	if err := os.MkdirAll(filepath.Join(cfg.LayersDir, "base", "rootfs"), 0700); err != nil {
		t.Fatal(err)
	}
	db := &DockerBackend{cfg: cfg}
	other := &DockerBackend{cfg: cfg}
	fresh := func() *sandbox.BotHomeBinding {
		b, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			c, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			if e := db.DeleteBotHome(c, b); e != nil {
				t.Error(e)
			}
		})
		return b
	}
	b, unrelated := fresh(), fresh()
	if _, err := db.WriteBotHomeFile(ctx, b, store.BotFileWrite{Path: "report.txt", SizeBytes: 5, Body: strings.NewReader("hello"), CreateOnly: true, BeforeCommit: func(op func() error) error { return op() }}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ensureWorkspaceVolume(ctx, b); err != nil {
		t.Fatal(err)
	}
	writer := &archiveBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := db.ExportBotHomeArchive(ctx, b, writer, func(c context.Context, h *sandbox.BotHomeBinding, op func(context.Context) error) error {
			// This disposable fixture has no registered workers; the backend supplies
			// the actual durable attachment lock, not this callback.
			if !h.Equal(b) {
				return errors.New("scope")
			}
			return op(c)
		})
		done <- err
	}()
	select {
	case <-writer.entered:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(writer.release) }) }
	defer release()
	blocked := func(name string, op func(context.Context) error) {
		t.Helper()
		c, stop := context.WithTimeout(ctx, 700*time.Millisecond)
		defer stop()
		if err := op(c); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s did not block on durable lock: %v", name, err)
		}
	}
	spec := sandbox.SessionSpec{SessionID: uuid.NewString(), Type: sandbox.SessionTypeChat, TemplateID: "base", LayerChain: []string{"base"}, Image: image, BotHome: b}
	blocked("create", func(c context.Context) error { _, e := other.CreateSession(c, spec); return e })
	if err := other.writeSessionMetadata(newDockerSessionMetadata(spec)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other.upperPath(spec.SessionID), layerChainFile), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked("recover", func(c context.Context) error { return other.StartSession(c, spec.SessionID) })
	p, err := other.lockBotHomeAdmission(ctx, unrelated, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = releaseBotHomeAdmission(p); err != nil {
		t.Fatal(err)
	}
	release()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	// Separately prove Start on an already-created Home container uses the same
	// inode. This disposable container does not compose or modify an overlay.
	name := containerName(spec.SessionID)
	if _, err = runDocker(ctx, "docker", "container", "create", "--name="+name, "--pull=never", "--network=none", "--mount=type=volume,source="+b.Ref+",target="+botHomeMount+",volume-nocopy", "--entrypoint=/bin/sh", image, "-c", "exit 0"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = runDocker(context.Background(), "docker", "rm", "-f", name) }()
	if _, err = runDocker(ctx, "docker", "start", "--attach", name); err != nil {
		t.Fatal(err)
	}
	holderCtx, cancelHolder := context.WithCancel(ctx)
	defer cancelHolder()
	holder, err := db.lockBotHomeAdmission(holderCtx, b, true)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	// Once acquired, caller cancellation cannot release the lock before the
	// operation's deferred helper cleanup. Explicit release remains mandatory.
	cancelHolder()
	blocked("start after holder caller cancellation", func(c context.Context) error { return other.StartSession(c, spec.SessionID) })
	if err = releaseBotHomeAdmission(holder); err != nil {
		t.Fatal(err)
	}
	if err = other.StartSession(ctx, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = runDocker(ctx, "docker", "rm", "-f", name); err != nil {
		t.Fatal(err)
	}
	// Independent process holds the real kernel flock. Its death must release it.
	raw, _ := json.Marshal(b)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBotHomeAdmissionSubprocess$", "-test.timeout=60s")
	child.Env = append(os.Environ(), "ASTONISH_ADMISSION_CHILD="+string(raw))
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()
	line, err := bufio.NewReader(pipe).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "LOCKED ") {
		t.Fatalf("child %q %v %s", line, err, stderr.String())
	}
	childHelper := strings.TrimSpace(strings.TrimPrefix(line, "LOCKED "))
	defer func() { _, _ = runDocker(context.Background(), "docker", "rm", "-f", childHelper) }()
	blocked("subprocess", func(c context.Context) error {
		p, e := other.lockBotHomeAdmission(c, b, true)
		if e == nil {
			_ = releaseBotHomeAdmission(p)
		}
		return e
	})
	_ = child.Process.Kill()
	_ = child.Wait()
	c, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	p, err = other.lockBotHomeAdmission(c, b, true)
	if err != nil {
		t.Fatalf("process death retained flock: %v", err)
	}
	if err = releaseBotHomeAdmission(p); err != nil {
		t.Fatal(err)
	}
	// Unknown Home attachments still reject archive without any foreign detach.
	foreign := "astonish-archive-foreign-" + uuid.NewString()
	if _, err = runDocker(ctx, "docker", "container", "create", "--name="+foreign, "--pull=never", "--network=none", "--mount=type=volume,source="+b.Ref+",target=/foreign,volume-nocopy", image); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = runDocker(context.Background(), "docker", "rm", foreign) }()
	if _, err = db.ExportBotHomeArchive(ctx, b, io.Discard, func(c context.Context, _ *sandbox.BotHomeBinding, op func(context.Context) error) error { return op(c) }); err == nil {
		t.Fatal("foreign attachment accepted")
	}
	if _, err = runDocker(ctx, "docker", "inspect", foreign); err != nil {
		t.Fatal("foreign attachment detached", err)
	}
	t.Log("physical exact-Home flock: export blocks independent create/recover, holder blocks start, unrelated Home progresses, cancellation/process death release, foreign attachment preserved")
}
