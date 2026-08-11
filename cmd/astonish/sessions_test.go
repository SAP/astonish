package astonish

import (
	"os"
	"path/filepath"
	"testing"
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
