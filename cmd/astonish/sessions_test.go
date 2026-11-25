package astonish

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	persistentsession "github.com/SAP/astonish/pkg/session"
)

// TestRemoveSessionFiles verifies that removeSessionFiles deletes both the
// session transcript and the per-session PLAN.md sidecar (written in code
// mode), and that a missing sidecar is not an error.
func TestRemoveSessionFiles(t *testing.T) {
	sessDir := t.TempDir()
	const (
		appName = "code"
		userID  = "user-abc"
		id      = "sess-123"
	)

	dir := filepath.Join(sessDir, appName, userID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(dir, id+".jsonl")
	plan := filepath.Join(dir, id+".PLAN.md")
	for _, p := range []string{transcript, plan} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}

	removeSessionFiles(sessDir, appName, userID, id)

	for _, p := range []string{transcript, plan} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", p, err)
		}
	}

	// Second call must be a best-effort no-op even though files are gone.
	removeSessionFiles(sessDir, appName, userID, id)
}

// TestRemoveSessionFiles_SidecarOnly verifies the PLAN.md sidecar is removed
// even when the transcript is already gone (e.g. a plan without messages).
func TestRemoveSessionFiles_SidecarOnly(t *testing.T) {
	sessDir := t.TempDir()
	const (
		appName = "code"
		userID  = "user-xyz"
		id      = "sess-999"
	)
	dir := filepath.Join(sessDir, appName, userID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plan := filepath.Join(dir, id+".PLAN.md")
	if err := os.WriteFile(plan, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	removeSessionFiles(sessDir, appName, userID, id)

	if _, err := os.Stat(plan); !os.IsNotExist(err) {
		t.Errorf("expected sidecar to be removed, stat err = %v", err)
	}
}

// Exercise the real confirmation/read/cleanup path in an isolated process so
// concurrent tests never need to replace the parent process's stdin or stdout.
func TestSessionsClearConcurrentCreation(t *testing.T) {
	if os.Getenv("ASTONISH_SESSIONS_CLEAR_TEST") == "1" {
		if err := handleSessionsClear(); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "astonish")
	sessDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("sessions:\n  storage: file\n  base_dir: %q\nsandbox:\n  enabled: false\n", sessDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	idx := persistentsession.NewSessionIndex(filepath.Join(sessDir, "index.json"))
	writeSession := func(id, parent string) {
		t.Helper()
		if err := idx.Add(persistentsession.SessionMeta{ID: id, AppName: "code", UserID: "user", ParentID: parent}); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(sessDir, "code", "user")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range sessionSidecarSuffixes {
			if err := os.WriteFile(filepath.Join(dir, id+suffix), []byte("session data"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeSession("confirmed", "")
	writeSession("confirmed-child", "confirmed")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionsClearConcurrentCreation$")
	// Explicit isolated configuration and no inherited sudo-user redirection.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "XDG_CONFIG_HOME=") && !strings.HasPrefix(env, "SUDO_USER=") && !strings.HasPrefix(env, "ASTONISH_SESSIONS_CLEAR_TEST=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+root, "ASTONISH_SESSIONS_CLEAR_TEST=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	var prompt strings.Builder
	for !strings.HasSuffix(prompt.String(), "[y/N] ") {
		b, err := reader.ReadByte()
		if err != nil {
			t.Fatalf("reading confirmation prompt %q: %v", prompt.String(), err)
		}
		prompt.WriteByte(b)
	}
	if !strings.Contains(prompt.String(), "all 2 session(s)") {
		t.Fatalf("unexpected confirmation: %q", prompt.String())
	}
	// These arrive after the confirmed snapshot, before the affirmative answer.
	writeSession("concurrent", "")
	writeSession("concurrent-child", "confirmed")
	if _, err := fmt.Fprintln(stdin, "yes"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("clear subprocess: %v, output %s", err, output)
	}
	if !strings.Contains(string(output), "Deleted 2 session(s).") {
		t.Fatalf("unexpected output: %s", output)
	}
	data, err := idx.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 2 || data.Sessions["concurrent"].ID == "" || data.Sessions["concurrent-child"].ID == "" {
		t.Fatalf("clear deleted unconfirmed records: %+v", data.Sessions)
	}
	for _, id := range []string{"confirmed", "confirmed-child", "concurrent", "concurrent-child"} {
		for _, suffix := range sessionSidecarSuffixes {
			_, err := os.Stat(filepath.Join(sessDir, "code", "user", id+suffix))
			if strings.HasPrefix(id, "confirmed") {
				if !os.IsNotExist(err) {
					t.Errorf("confirmed %s%s not removed: %v", id, suffix, err)
				}
			} else if err != nil {
				t.Errorf("unconfirmed %s%s missing: %v", id, suffix, err)
			}
		}
	}
}
