package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlidesWorkerPathsUsesRuntimeRoot(t *testing.T) {
	root := t.TempDir()
	workingDir := filepath.Join(root, "web")
	scriptPath := filepath.Join(root, "pkg", "docs", "slides", "pptxworker", "worker.mjs")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("export {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(astonishRootEnv, root)

	gotWorkingDir, gotScriptPath, err := slidesWorkerPaths("worker.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if gotWorkingDir != workingDir || gotScriptPath != scriptPath {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", gotWorkingDir, gotScriptPath, workingDir, scriptPath)
	}
}

func TestSlidesWorkerPathsRejectsIncompleteRuntimeRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv(astonishRootEnv, root)

	_, _, err := slidesWorkerPaths("missing-worker.mjs")
	if err == nil {
		t.Fatal("expected missing worker error")
	}
}
