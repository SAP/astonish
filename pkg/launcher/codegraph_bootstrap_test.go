package launcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureCodegraph_IndexAlreadyExists verifies that when .codegraph/ already
// exists as a directory, EnsureCodegraph returns "" without running npx.
func TestEnsureCodegraph_IndexAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	indexDir := filepath.Join(dir, ".codegraph")
	if err := os.Mkdir(indexDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	notice := EnsureCodegraph(context.Background(), dir)
	if notice != "" {
		t.Errorf("expected empty notice when index exists, got: %q", notice)
	}
}

// TestEnsureCodegraph_NpxFailsReturnsNotice verifies that when .codegraph/ does
// not exist and npx is unavailable (or fails), EnsureCodegraph returns a
// non-empty notice instructing the model to call gplan_gaps.
func TestEnsureCodegraph_NpxFailsReturnsNotice(t *testing.T) {
	dir := t.TempDir()

	// Clear PATH so npx cannot be found, guaranteeing exec failure.
	t.Setenv("PATH", "")

	notice := EnsureCodegraph(context.Background(), dir)
	if notice == "" {
		t.Error("expected a non-empty notice when npx fails, got empty string")
	}
	if !strings.Contains(notice, "gplan_gaps") {
		t.Errorf("notice should mention gplan_gaps, got: %q", notice)
	}
}

// TestEnsureCodegraph_EmptyWorkingDirDefaultsToDot verifies the "" → "."
// fallback does not panic.
func TestEnsureCodegraph_EmptyWorkingDirDefaultsToDot(t *testing.T) {
	_ = EnsureCodegraph(context.Background(), "")
}
