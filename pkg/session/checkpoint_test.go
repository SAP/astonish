package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointStore_CaptureAndRestore(t *testing.T) {
	base := t.TempDir()
	work := t.TempDir()
	store, err := NewCheckpointStore(base)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}

	fileA := filepath.Join(work, "a.txt")
	if err := os.WriteFile(fileA, []byte("original A"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileB := filepath.Join(work, "b.txt") // does not exist yet

	const sess = "sess-1"

	// Turn 0: modify A (existing) and create B (new).
	store.BeginTurn(sess, 0)
	if err := store.Capture(sess, 0, fileA); err != nil {
		t.Fatalf("capture A: %v", err)
	}
	if err := store.Capture(sess, 0, fileB); err != nil {
		t.Fatalf("capture B: %v", err)
	}
	// Simulate the tool writes.
	if err := os.WriteFile(fileA, []byte("modified A turn0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("created B turn0"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn 1: modify A again.
	store.BeginTurn(sess, 1)
	if err := store.Capture(sess, 1, fileA); err != nil {
		t.Fatalf("capture A turn1: %v", err)
	}
	if err := os.WriteFile(fileA, []byte("modified A turn1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// FileCountFrom(0) counts distinct files across turns 0 and 1 = {A, B} = 2.
	if got := store.FileCountFrom(sess, 0); got != 2 {
		t.Errorf("FileCountFrom(0) = %d, want 2", got)
	}
	// FileCountFrom(1) = {A} = 1.
	if got := store.FileCountFrom(sess, 1); got != 1 {
		t.Errorf("FileCountFrom(1) = %d, want 1", got)
	}

	// Roll back to turn 0: A should return to "original A" (oldest pre-image
	// wins), and B should be deleted (it did not exist before turn 0).
	res, err := store.RestoreTo(sess, 0)
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	gotA, _ := os.ReadFile(fileA)
	if string(gotA) != "original A" {
		t.Errorf("A after rollback = %q, want %q", gotA, "original A")
	}
	if _, err := os.Stat(fileB); !os.IsNotExist(err) {
		t.Errorf("B should have been deleted, stat err = %v", err)
	}
	if len(res.Restored) != 1 || res.Restored[0] != fileA {
		t.Errorf("Restored = %v, want [%s]", res.Restored, fileA)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != fileB {
		t.Errorf("Deleted = %v, want [%s]", res.Deleted, fileB)
	}

	// Checkpoints consumed: a second rollback to 0 is a no-op.
	if got := store.FileCountFrom(sess, 0); got != 0 {
		t.Errorf("FileCountFrom(0) after restore = %d, want 0", got)
	}
}

func TestCheckpointStore_CaptureOncePerTurn(t *testing.T) {
	base := t.TempDir()
	work := t.TempDir()
	store, _ := NewCheckpointStore(base)

	f := filepath.Join(work, "f.txt")
	if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sess = "s"
	store.BeginTurn(sess, 0)
	_ = store.Capture(sess, 0, f)
	// Second write in the same turn: pre-image must remain "v1".
	if err := os.WriteFile(f, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = store.Capture(sess, 0, f)
	if err := os.WriteFile(f, []byte("v3"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.RestoreTo(sess, 0); err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	got, _ := os.ReadFile(f)
	if string(got) != "v1" {
		t.Errorf("after rollback = %q, want v1 (first capture wins)", got)
	}
}

func TestCheckpointStore_DeleteSession(t *testing.T) {
	base := t.TempDir()
	work := t.TempDir()
	store, _ := NewCheckpointStore(base)
	f := filepath.Join(work, "f.txt")
	_ = os.WriteFile(f, []byte("x"), 0o644)

	const sess = "s"
	store.BeginTurn(sess, 0)
	_ = store.Capture(sess, 0, f)
	if got := store.FileCountFrom(sess, 0); got != 1 {
		t.Fatalf("precondition FileCountFrom = %d, want 1", got)
	}
	if err := store.DeleteSession(sess); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if got := store.FileCountFrom(sess, 0); got != 0 {
		t.Errorf("FileCountFrom after delete = %d, want 0", got)
	}
}
