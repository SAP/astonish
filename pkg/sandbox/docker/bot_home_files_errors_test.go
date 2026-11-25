package docker

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"io"
	"os/exec"
	"time"
)

func TestBotHomeFilesMissingDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	if os.Getenv("ASTONISH_SANDBOX_IMAGE") == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")}}
	home, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if e := db.DeleteBotHome(c, home); e != nil {
			t.Error(e)
		}
	})
	for _, path := range []string{"SOUL.md", "missing/SOUL.md"} {
		if _, err := db.StatBotHomeFile(ctx, home, path); !errors.Is(err, store.ErrBotNotFound) || !strings.Contains(err.Error(), "not_found") {
			t.Fatalf("stat %s: %v", path, err)
		}
		if _, err := db.OpenBotHomeFile(ctx, home, path, store.BotFileRange{}); !errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("open %s: %v", path, err)
		}
	}
	entry, err := db.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "SOUL.md", SizeBytes: 2, Body: strings.NewReader("ok"), CreateOnly: true, BeforeCommit: func(f func() error) error { return f() }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.StatBotHomeFile(ctx, home, "SOUL.md")
	if err != nil || got.SHA256 != entry.SHA256 {
		t.Fatalf("existing stat: %+v %v", got, err)
	}
	page, err := db.ListBotHomeFiles(ctx, home, ".", "", 100)
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("missing parent was provisioned: %+v %v", page, err)
	}
	raw, err := runDocker(ctx, "docker", "ps", "-aq", "--filter", "label="+botHomeScopeLabel+"="+home.Scope.Key(), "--filter", "label="+botHomeReaderLabel)
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("helper leak: %s %v", raw, err)
	}
	if err := db.DeleteBotHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StatBotHomeFile(ctx, home, "SOUL.md"); err == nil {
		t.Fatal("missing physical home accepted")
	}
	if _, err := runDocker(ctx, "docker", "volume", "inspect", home.Ref); err == nil {
		t.Fatal("read provisioned missing physical home")
	}
	t.Log("physical Docker: missing leaf/parent typed; stderr retained; write/stat handshake; no read-time provisioning or helper leaks")
}

func TestBotHomeFilesDockerResponseJoin(t *testing.T) {
	for _, wire := range []string{"{\"error\":{\"code\":\"not_found\"}}\n", "{bad}\n", ""} {
		t.Run(fmt.Sprintf("wire-%d", len(wire)), func(t *testing.T) {
			runtime := filepath.Join(t.TempDir(), "runtime")
			// This models failed terminal inspection/cleanup without touching Docker.
			if err := os.WriteFile(runtime, []byte("#!/bin/sh\necho terminal-cleanup-sentinel >&2\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "-I", "-c", "import sys; sys.stderr.write('helper-stderr-sentinel'); sys.exit(1)")
			stderr := &artifactBuffer{remaining: 4096}
			cmd.Stderr = stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			p := &homeFileProcess{binding: &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: "test-user", BotID: "test-bot"}}, db: &DockerBackend{cfg: Config{ContainerRuntimePath: runtime}}, name: "test", cmd: cmd, cancel: cancel, stderr: stderr, done: make(chan struct{}), reader: bufio.NewReader(strings.NewReader(wire))}
			var entry store.BotFileEntry
			err := p.response(&entry)
			if err == nil || !strings.Contains(err.Error(), "terminal-cleanup-sentinel") {
				t.Fatalf("terminal discarded: %v", err)
			}
			if strings.Contains(wire, "not_found") && !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("missing classification: %v", err)
			}
			if wire != "{bad}\n" && !strings.Contains(err.Error(), "helper-stderr-sentinel") {
				t.Fatalf("helper stderr discarded: %v", err)
			}
			if closeErr := p.Close(); closeErr == nil {
				t.Fatal("close discarded terminal error")
			}
			if wire == "" && !errors.Is(err, io.EOF) {
				t.Fatalf("EOF discarded: %v", err)
			}
		})
	}
}

func TestBotHomeFilesDockerTruncatedMetadata(t *testing.T) {
	for _, wire := range []string{`{"path":"SOUL.md"}`, `{"error":{"code":"not_found"}}`} {
		p := &homeFileProcess{reader: bufio.NewReader(strings.NewReader(wire))}
		var entry store.BotFileEntry
		if err := p.line(&entry); !errors.Is(err, io.EOF) || errors.Is(err, store.ErrBotNotFound) {
			t.Fatalf("truncated line accepted: %v", err)
		}
	}
}

func TestBotHomeFilesMissingProtocol(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "existing.md"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want error
	}{
		{"SOUL.md", store.ErrBotNotFound}, {"missing/SOUL.md", store.ErrBotNotFound},
		{"existing.md", nil}, {"escape/missing", store.ErrBotForbidden}, {"../missing", store.ErrBotForbidden},
	} {
		t.Run(tc.path, func(t *testing.T) {
			raw, _ := filesProgram(t, home, private, map[string]any{"op": "stat", "path": tc.path}, nil, false)
			p := &homeFileProcess{reader: bufio.NewReader(strings.NewReader(string(raw)))}
			var entry store.BotFileEntry
			err := p.line(&entry)
			if !errors.Is(err, tc.want) {
				t.Fatalf("stat %q: got %v want %v; wire=%s", tc.path, err, tc.want, raw)
			}
			if tc.want == nil && entry.Path != tc.path {
				t.Fatalf("entry: %+v", entry)
			}
		})
	}
}
