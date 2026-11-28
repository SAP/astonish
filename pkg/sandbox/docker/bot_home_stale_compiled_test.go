package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestCompiledHomeFilesStaleAttachmentDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit Docker acceptance required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("test-owned current binary image required")
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: image}}
	ctx := t.Context()
	h, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := db.DeleteBotHome(c, h); err != nil {
			t.Error(err)
		}
	}()
	operation, end := sandbox.WithBotHomeFileOperation(ctx)
	defer end()
	guard := func(f func() error) error { return f() }
	if _, err := db.WriteBotHomeFile(operation, h, store.BotFileWrite{Path: "initial", SizeBytes: 1, Body: strings.NewReader("x"), CreateOnly: true, BeforeCommit: guard}); err != nil {
		t.Fatal(err)
	}
	raw, err := runDocker(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter=volume="+h.Ref)
	ids := strings.Fields(string(raw))
	if err != nil || len(ids) != 1 {
		t.Fatalf("expected cached attachment: %s %v", raw, err)
	}
	original := ids[0]
	raw, err = runDocker(ctx, "docker", "inspect", "--format={{.Name}}", original)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(strings.TrimSpace(string(raw)), "/")
	if _, err = runDocker(ctx, "docker", "rename", original, name+"-old"); err != nil {
		t.Fatal(err)
	}
	// Reuse the logical name and even labels; neither grants physical ownership.
	raw, err = runDocker(ctx, "docker", "container", "create", "--name="+name,
		"--label="+botHomeReaderLabel+"="+name, "--label="+botHomeScopeLabel+"="+h.Scope.Key(),
		"--pull=never", "--network=none", "--entrypoint=/bin/sleep", image, "infinity")
	if err != nil {
		t.Fatal(err)
	}
	replacement := strings.TrimSpace(string(raw))
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// These are fixture IDs, not name-based ownership guesses.
		_, _ = runDocker(c, "docker", "rm", "-f", original, replacement)
	}()
	if _, err = runDocker(ctx, "docker", "start", replacement); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = db.WriteBotHomeFile(operation, h, store.BotFileWrite{Path: "stale", SizeBytes: 1, Body: strings.NewReader("x"), CreateOnly: true, BeforeCommit: func(f func() error) error { calls++; return f() }})
	if err == nil || calls != 0 {
		t.Fatalf("stale target mutated/retried: err=%v commits=%d", err, calls)
	}
	raw, err = runDocker(ctx, "docker", "inspect", "--format={{.State.Running}}", replacement)
	if err != nil || strings.TrimSpace(string(raw)) != "true" {
		t.Fatalf("stale cleanup destroyed replacement: %s %v", raw, err)
	}
	if _, err = runDocker(ctx, "docker", "inspect", original); err == nil {
		t.Fatal("owned original attachment leaked")
	}
	if _, err = db.StatBotHomeFile(ctx, h, "stale"); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("stale mutation was retried: %v", err)
	}
	if err = end(); err != nil {
		t.Fatal(err)
	}
	t.Log("stale attachment refused mutation before commit; cleanup removed only original physical ID and preserved same-name replacement")
}
