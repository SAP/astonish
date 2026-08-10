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

	var messages []string
	notice := EnsureCodegraph(context.Background(), dir, func(msg string) {
		messages = append(messages, msg)
	})
	if notice != "" {
		t.Errorf("expected empty notice when index exists, got: %q", notice)
	}
	if len(messages) != 0 {
		t.Errorf("expected no progress messages when index exists, got: %v", messages)
	}
}

// TestEnsureCodegraph_NpxFailsReturnsNotice verifies that when .codegraph/ does
// not exist and npx is unavailable (or fails), EnsureCodegraph returns a
// non-empty notice instructing the model to call gplan_gaps.
func TestEnsureCodegraph_NpxFailsReturnsNotice(t *testing.T) {
	dir := t.TempDir()

	// Clear PATH so npx cannot be found, guaranteeing exec failure.
	t.Setenv("PATH", "")

	var messages []string
	notice := EnsureCodegraph(context.Background(), dir, func(msg string) {
		messages = append(messages, msg)
	})
	if notice == "" {
		t.Error("expected a non-empty notice when npx fails, got empty string")
	}
	if !strings.Contains(notice, "gplan_gaps") {
		t.Errorf("notice should mention gplan_gaps, got: %q", notice)
	}
	// The "indexing" progress message should still be emitted before the failure.
	if len(messages) == 0 {
		t.Error("expected at least one progress message (indexing start) before failure")
	}
	if len(messages) > 0 && !strings.Contains(messages[0], "Indexing") {
		t.Errorf("first progress message should mention indexing, got: %q", messages[0])
	}
}

// TestEnsureCodegraph_EmptyWorkingDirDefaultsToDot verifies the "" → "."
// fallback does not panic.
func TestEnsureCodegraph_EmptyWorkingDirDefaultsToDot(t *testing.T) {
	_ = EnsureCodegraph(context.Background(), "", nil)
}

// TestEnsureCodegraph_NilCallbackDoesNotPanic verifies that passing nil for
// onProgress is safe.
func TestEnsureCodegraph_NilCallbackDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	indexDir := filepath.Join(dir, ".codegraph")
	if err := os.Mkdir(indexDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Should not panic with nil callback.
	_ = EnsureCodegraph(context.Background(), dir, nil)
}
