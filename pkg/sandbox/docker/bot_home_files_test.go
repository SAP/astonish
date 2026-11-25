package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func filesProgram(t *testing.T, home, private string, q map[string]any, body []byte, approve bool) ([]byte, error) {
	t.Helper()
	program := fmt.Sprintf("import os\nhome=os.open(%q,os.O_RDONLY|os.O_DIRECTORY)\nprivate=os.open(%q,os.O_RDONLY|os.O_DIRECTORY)\n", home, private) + sandbox.BotHomeFilesProgram
	cmd := exec.Command("python3", "-I", "-c", program)
	input, _ := cmd.StdinPipe()
	output, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	go func() {
		defer input.Close()
		_ = json.NewEncoder(input).Encode(q)
		_, _ = input.Write(body)
		if approve {
			_, _ = io.WriteString(input, "commit\n")
		}
	}()
	data, e := io.ReadAll(output)
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Wait(); e != nil {
		return data, fmt.Errorf("%w: %s", e, stderr.String())
	}
	return data, nil
}
func TestBotHomeFilesDockerRejectForgedContentKey(t *testing.T) {
	ref := store.BotContentRef{Kind: store.BotContentEvidence, Revision: 4, Key: "content-v1/evidence-4-" + strings.ReplaceAll(uuid.NewString(), "-", "")}
	if !validContentKey(ref) {
		t.Fatal("server key rejected")
	}
	for _, key := range []string{strings.Replace(ref.Key, "evidence", "artifact", 1), strings.Replace(ref.Key, "-4-", "-5-", 1), "content-v1/check", ref.Key + "/x"} {
		bad := ref
		bad.Key = key
		if validContentKey(bad) {
			t.Fatalf("forged key accepted: %s", key)
		}
	}
}

func TestBotHomeFilesProgramConcurrentCAS(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "cas.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, body := range []string{"one", "two"} {
		go func(body string) {
			<-start
			_, err := filesProgram(t, home, private, map[string]any{"op": "write", "path": "cas.txt", "size": 3, "expected": "cba06b5736faf67e54b07b561eae94395e774c517a7d910a54369e1263ccfbd4"}, []byte(body), true)
			results <- err
		}(body)
	}
	close(start)
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("CAS winners: %d", successes)
	}
	data, err := os.ReadFile(filepath.Join(home, "cas.txt"))
	if err != nil || string(data) != "one" && string(data) != "two" {
		t.Fatalf("invalid winning bytes %q: %v", data, err)
	}
}

func TestBotHomeFilesProgramStreamingAndConfinement(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	body := bytes.Repeat([]byte("large-body-"), 2<<20)
	raw, e := filesProgram(t, home, private, map[string]any{"op": "write", "path": "reports/work.md", "size": len(body), "create_only": true}, body, true)
	if e != nil {
		t.Fatal(e)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var entry store.BotFileEntry
	if len(lines) != 2 {
		t.Fatalf("write protocol: %s", raw)
	}
	if e = json.Unmarshal(lines[1], &entry); e != nil {
		t.Fatal(e)
	}
	raw, e = filesProgram(t, home, private, map[string]any{"op": "open", "path": "reports/work.md", "offset": 100, "length": 200}, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	_, _ = reader.ReadBytes('\n')
	got, _ := io.ReadAll(reader)
	if !bytes.Equal(got, body[100:300]) {
		t.Fatal("range differs")
	}
	raw, e = filesProgram(t, home, private, map[string]any{"op": "snapshot", "path": "reports/work.md"}, nil, true)
	if e != nil {
		t.Fatal(e)
	}
	lines = bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var snapshot store.BotFileEntry
	_ = json.Unmarshal(lines[1], &snapshot)
	if snapshot.SHA256 != entry.SHA256 || !strings.HasPrefix(snapshot.Path, "content-v1/") {
		t.Fatal("snapshot receipt mismatch")
	}
	_, e = filesProgram(t, home, private, map[string]any{"op": "write", "path": "reports/work.md", "size": 3, "expected": strings.Repeat("0", 64)}, []byte("bad"), true)
	if e == nil {
		t.Fatal("stale CAS accepted")
	}
	_, e = filesProgram(t, home, private, map[string]any{"op": "write", "path": "reports/work.md", "size": 3, "expected": entry.SHA256}, []byte("new"), false)
	if e == nil {
		t.Fatal("denied commit accepted")
	}
	got, _ = os.ReadFile(filepath.Join(home, "reports/work.md"))
	if !bytes.Equal(got, body) {
		t.Fatal("failed write changed existing bytes")
	}
	if e = os.Symlink(private, filepath.Join(home, "escape")); e != nil {
		t.Fatal(e)
	}
	if e = os.Link(filepath.Join(home, "reports/work.md"), filepath.Join(home, "hard.md")); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"../x", "escape/anything", "hard.md", "reports/work.md"} {
		_, e = filesProgram(t, home, private, map[string]any{"op": "open", "path": path}, nil, false)
		if e == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	raw, e = filesProgram(t, home, private, map[string]any{"op": "list", "path": ".", "limit": 100}, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	var page store.BotFilePage
	if e = json.Unmarshal(raw, &page); e != nil {
		t.Fatal(e)
	}
	if len(page.Entries) != 1 || page.Entries[0].Path != "reports" {
		t.Fatalf("unsafe listing: %s", raw)
	}
}
func TestBotHomeFilesDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	if os.Getenv("ASTONISH_SANDBOX_IMAGE") == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE")}}
	home, e := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if e := db.DeleteBotHome(c, home); e != nil {
			t.Error(e)
		}
	})
	barrier := func(commit func() error) error { return commit() }
	body := bytes.Repeat([]byte("retained-data"), 2<<20)
	entry, e := db.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "report.md", SizeBytes: int64(len(body)), Body: bytes.NewReader(body), CreateOnly: true, BeforeCommit: barrier})
	if e != nil {
		t.Fatal(e)
	}
	page, e := db.ListBotHomeFiles(ctx, home, ".", "", 100)
	if e != nil || len(page.Entries) != 1 {
		t.Fatalf("list: %+v %v", page, e)
	}
	ref, e := db.PublishBotArtifactSnapshot(ctx, home, "report.md", "report.md", barrier)
	if e != nil {
		t.Fatal(e)
	}
	entry, e = db.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "report.md", SizeBytes: 7, Body: strings.NewReader("changed"), ExpectedSHA256: entry.SHA256, BeforeCommit: barrier})
	if e != nil {
		t.Fatal(e)
	}
	stream, e := db.OpenBotContent(ctx, home, *ref, store.BotFileRange{})
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(stream)
	if e != nil {
		t.Fatal(e)
	}
	if e = stream.Close(); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("published snapshot changed")
	}
	stream, e = db.OpenBotHomeFile(ctx, home, "report.md", store.BotFileRange{Offset: 1, Length: 3})
	if e != nil {
		t.Fatal(e)
	}
	got, e = io.ReadAll(stream)
	if e != nil {
		t.Fatal(e)
	}
	_ = stream.Close()
	if string(got) != "han" {
		t.Fatalf("range: %q", got)
	}
	foreign := *home
	foreign.Scope.BotID = uuid.NewString()
	if _, e = db.OpenBotContent(ctx, &foreign, *ref, store.BotFileRange{}); e == nil {
		t.Fatal("cross-scope read accepted")
	}
	assertNoHelpers := func(t *testing.T) {
		t.Helper()
		raw, err := runDocker(ctx, "docker", "ps", "-aq", "--filter", "label="+botHomeScopeLabel+"="+home.Scope.Key(), "--filter", "label="+botHomeReaderLabel)
		if err != nil || strings.TrimSpace(string(raw)) != "" {
			t.Fatalf("owned helper leak: %s %v", raw, err)
		}
	}
	t.Run("startup-failure", func(t *testing.T) {
		runtime := filepath.Join(t.TempDir(), "docker-start-failure")
		if err := os.WriteFile(runtime, []byte("#!/bin/sh\nif [ \"$1\" = start ]; then exit 23; fi\nexec docker \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		broken := &DockerBackend{cfg: db.cfg}
		broken.cfg.ContainerRuntimePath = runtime
		if s, err := broken.OpenBotHomeFile(ctx, home, "report.md", store.BotFileRange{}); err == nil {
			_ = s.Close()
			t.Fatal("failed attachment reported success")
		}
		assertNoHelpers(t)
	})
	t.Run("concurrent-read-close", func(t *testing.T) {
		s, err := db.OpenBotContent(ctx, home, *ref, store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, s); close(done) }()
		_ = s.Close()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("Close did not release concurrent reader")
		}
		assertNoHelpers(t)
	})
	t.Run("paused-close", func(t *testing.T) {
		s, err := db.OpenBotContent(ctx, home, *ref, store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		proc := s.ReadCloser.(*homeFileProcess)
		closed := make(chan struct{})
		go func() { _ = s.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(20 * time.Second):
			t.Fatal("Close did not join paused stream")
		}
		if proc.cmd.ProcessState == nil {
			t.Fatal("attachment process not joined")
		}
		_ = s.Close() // Idempotent, including cleanup.
		assertNoHelpers(t)
	})
	t.Run("paused-context-cancel", func(t *testing.T) {
		readCtx, stop := context.WithCancel(ctx)
		defer stop()
		s, err := db.OpenBotContent(readCtx, home, *ref, store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		proc := s.ReadCloser.(*homeFileProcess)
		stop()
		select {
		case <-proc.done:
		case <-time.After(20 * time.Second):
			t.Fatal("context cancellation did not clean paused helper")
		}
		if proc.err == nil || proc.cmd.ProcessState == nil {
			t.Fatal("cancelled attachment lacks terminal error or join")
		}
		_ = s.Close()
		assertNoHelpers(t)
	})
	t.Run("private-integrity", func(t *testing.T) {
		for _, mutate := range []func(*store.BotContentRef){
			func(r *store.BotContentRef) { r.Kind = store.BotContentEvidence },
			func(r *store.BotContentRef) { r.Revision++ },
		} {
			bad := *ref
			mutate(&bad)
			if s, err := db.OpenBotContent(ctx, home, bad, store.BotFileRange{}); err == nil {
				_ = s.Close()
				t.Fatal("forged private kind/revision accepted")
			}
		}
		bad := *ref
		bad.SHA256 = strings.Repeat("0", 64)
		if s, err := db.OpenBotContent(ctx, home, bad, store.BotFileRange{}); err == nil {
			_ = s.Close()
			t.Fatal("private hash mismatch accepted")
		}
		bad = *ref
		bad.SizeBytes++
		if s, err := db.OpenBotContent(ctx, home, bad, store.BotFileRange{}); err == nil {
			_ = s.Close()
			t.Fatal("private size mismatch accepted")
		}
		if s, err := db.OpenBotHomeFile(ctx, home, ref.Key, store.BotFileRange{}); err == nil {
			_ = s.Close()
			t.Fatal("private object visible through ordinary Home")
		}
		assertNoHelpers(t)
	})
	t.Run("swallowed-barrier-error", func(t *testing.T) {
		_, err := db.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "barrier.txt", SizeBytes: 3, Body: strings.NewReader("abc"), CreateOnly: true, BeforeCommit: func(commit func() error) error {
			if err := commit(); err != nil {
				return err
			}
			_ = commit() // A misbehaving barrier cannot hide a repeated commit error.
			return nil
		}})
		if err == nil {
			t.Fatal("barrier swallowed repeated commit failure")
		}
		assertNoHelpers(t)
	})
	t.Run("cancel-blocked-upload", func(t *testing.T) {
		uploadCtx, stop := context.WithCancel(ctx)
		defer stop()
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		done := make(chan error, 1)
		go func() {
			_, err := db.WriteBotHomeFile(uploadCtx, home, store.BotFileWrite{Path: "cancel.bin", SizeBytes: 2, Body: reader, CreateOnly: true, BeforeCommit: barrier})
			done <- err
		}()
		if _, err := writer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		stop()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled upload succeeded")
			}
		case <-time.After(20 * time.Second):
			_ = reader.Close()
			<-done
			t.Fatal("cancelled upload did not return")
		}
		assertNoHelpers(t)
	})
	t.Run("denied-commit", func(t *testing.T) {
		_, err := db.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: "report.md", SizeBytes: 3, Body: strings.NewReader("bad"), ExpectedSHA256: entry.SHA256, BeforeCommit: func(func() error) error { return store.ErrBotForbidden }})
		if err == nil {
			t.Fatal("denied write succeeded")
		}
		s, err := db.OpenBotHomeFile(ctx, home, "report.md", store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(s)
		_ = s.Close()
		if err != nil || string(got) != "changed" {
			t.Fatalf("denied write changed prior file: %q %v", got, err)
		}
		assertNoHelpers(t)
	})
	assertNoHelpers(t)
	t.Log("physical retained Home streamed >16MiB; immutable snapshot, range, private hash/size, scope isolation, paused Close/cancellation and helper cleanup verified")
}

func TestBotHomeFilesRejectPathsBeforeRuntime(t *testing.T) {
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "/must-not-execute"}}
	for _, op := range []string{"list", "stat", "open", "write", "snapshot"} {
		for _, path := range []string{"", "..", "../escape", "/absolute", "a/../b", "a//b", "a/./b", "a\\b", "a\x00b", "a\nb"} {
			p, err := db.startHomeFiles(t.Context(), nil, map[string]any{"op": op, "path": path})
			if p != nil || !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("%s %q reached runtime: %v", op, path, err)
			}
		}
	}
	for _, op := range []string{"stat", "open", "write", "snapshot"} {
		if _, err := db.startHomeFiles(t.Context(), nil, map[string]any{"op": op, "path": "."}); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("root accepted for %s: %v", op, err)
		}
	}
	if _, err := db.startHomeFiles(t.Context(), nil, map[string]any{"op": "list", "path": ".", "after": "../escape"}); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("cursor reached runtime: %v", err)
	}
}

func TestBotHomeFilesProgramPutAndSnapshotIsolation(t *testing.T) {
	home, private := t.TempDir(), t.TempDir()
	raw, e := filesProgram(t, home, private, map[string]any{"op": "put", "size": 4}, []byte("data"), true)
	if e != nil {
		t.Fatal(e)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var entry store.BotFileEntry
	_ = json.Unmarshal(lines[1], &entry)
	raw, e = filesProgram(t, home, private, map[string]any{"op": "open", "path": entry.Path, "private": true, "sha256": entry.SHA256, "size": 4}, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.HasSuffix(raw, []byte("\ndata")) {
		t.Fatal("content stream differs")
	}
	if _, e = os.Stat(filepath.Join(home, entry.Path)); !os.IsNotExist(e) {
		t.Fatal("private bytes leaked into Home")
	}
	_, e = filesProgram(t, home, private, map[string]any{"op": "open", "path": entry.Path, "private": true, "sha256": strings.Repeat("0", 64)}, nil, false)
	if e == nil {
		t.Fatal("digest mismatch accepted")
	}
}
