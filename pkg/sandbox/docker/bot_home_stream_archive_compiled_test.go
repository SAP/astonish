package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// Exercises the real node entry point and detached stream lifecycle without a
// Bot session or overlay Python. Every volume is unique and fixture-owned.
func TestBotHomeCompiledArchiveDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: image}}
	homes := map[string]bool{}
	fresh := func() *sandbox.BotHomeBinding {
		b, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		homes[b.Ref] = true
		t.Cleanup(func() {
			c, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if err := db.DeleteBotHome(c, b); err != nil {
				t.Error(err)
			}
		})
		return b
	}
	source, target, broken := fresh(), fresh(), fresh()
	if _, err := db.ensureWorkspaceVolume(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := runDocker(ctx, "docker", "run", "--rm", "--network=none", "--pull=never", "--mount=type=volume,source="+source.Ref+",target=/home-files,volume-nocopy", "--mount=type=volume,source="+workspaceVolume(source)+",target=/private-files,volume-nocopy", "--entrypoint=/bin/sh", image, "-ec", "test ! -x /usr/bin/python3; mkdir /home-files/reports; printf 'exact archive bytes\\n' > /home-files/reports/out.txt; printf 'protected object\\n' > /private-files/object"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	barrier := func(c context.Context, b *sandbox.BotHomeBinding, op func(context.Context) error) error {
		mu.Lock()
		defer mu.Unlock()
		if !homes[b.Ref] {
			return errors.New("not fixture owned")
		}
		return op(c)
	}
	var body bytes.Buffer
	m, err := db.ExportBotHomeArchive(ctx, source, &body, barrier)
	if err != nil {
		t.Fatal(err)
	}
	if body.String() != "exact archive bytes\nprotected object\n" {
		t.Fatalf("wrong stream %q", body.String())
	}
	if err = db.RestoreBotHomeArchive(ctx, target, m, bytes.NewReader(body.Bytes()), barrier); err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	again, err := db.ExportBotHomeArchive(ctx, target, &restored, barrier)
	if err != nil {
		t.Fatal(err)
	}
	again.Binding = m.Binding
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) || !bytes.Equal(body.Bytes(), restored.Bytes()) {
		t.Fatal("manifest or bytes changed")
	}
	if err = db.RestoreBotHomeArchive(ctx, target, m, bytes.NewReader(body.Bytes()), barrier); err == nil {
		t.Fatal("restore replay accepted")
	}
	if err = db.RestoreBotHomeArchive(ctx, broken, m, bytes.NewReader(body.Bytes()[:2]), barrier); err == nil {
		t.Fatal("truncation accepted")
	}
	if _, err = db.ExportBotHomeArchive(ctx, broken, io.Discard, barrier); err == nil {
		t.Fatal("partial target selected")
	}
	if _, err = db.ExportBotHomeArchive(ctx, source, io.Discard, nil); err == nil {
		t.Fatal("missing barrier accepted")
	}
	for _, h := range []*sandbox.BotHomeBinding{source, target, broken} {
		raw, err := runDocker(ctx, "docker", "ps", "-aq", "--filter=label="+botHomeScopeLabel+"="+h.Scope.Key(), "--filter=label="+botHomeReaderLabel)
		if err != nil || strings.TrimSpace(string(raw)) != "" {
			t.Fatalf("archive attachment leak %s %v", raw, err)
		}
	}
	t.Log("compiled node: no Python; cold export/restore/reexport preserved exact manifest and bytes; replay/truncation/fence rejected; attachments reaped")
}
