package session

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestIndex_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	data, err := idx.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if data.Version != 1 {
		t.Errorf("Version = %d, want 1", data.Version)
	}
	if len(data.Sessions) != 0 {
		t.Errorf("Sessions len = %d, want 0", len(data.Sessions))
	}
}

func TestIndex_AddAndGet(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	meta := SessionMeta{
		ID:        "sess-001",
		AppName:   "test-app",
		UserID:    "user-1",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Title:     "Test Session",
	}

	if err := idx.Add(meta); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got, err := idx.Get("sess-001")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != "sess-001" {
		t.Errorf("ID = %q, want %q", got.ID, "sess-001")
	}
	if got.AppName != "test-app" {
		t.Errorf("AppName = %q, want %q", got.AppName, "test-app")
	}
	if got.Title != "Test Session" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Session")
	}
}

func TestIndex_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	_, err := idx.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
}

func TestIndex_Remove(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	meta := SessionMeta{
		ID:      "sess-002",
		AppName: "test-app",
		UserID:  "user-1",
	}

	if err := idx.Add(meta); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := idx.Remove("sess-002"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	_, err := idx.Get("sess-002")
	if err == nil {
		t.Fatal("expected error after Remove, got nil")
	}
}

func TestIndex_Update(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	meta := SessionMeta{
		ID:      "sess-003",
		AppName: "test-app",
		UserID:  "user-1",
		Title:   "Original Title",
	}

	if err := idx.Add(meta); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if err := idx.Update("sess-003", func(m *SessionMeta) {
		m.Title = "Updated Title"
		m.MessageCount = 5
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := idx.Get("sess-003")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", got.Title, "Updated Title")
	}
	if got.MessageCount != 5 {
		t.Errorf("MessageCount = %d, want 5", got.MessageCount)
	}
}

func TestIndex_UpdateNotFound(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	err := idx.Update("nonexistent", func(m *SessionMeta) {
		m.Title = "test"
	})
	if err == nil {
		t.Fatal("expected error updating nonexistent session, got nil")
	}
}

func TestIndex_ListFilters(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	sessions := []SessionMeta{
		{ID: "s1", AppName: "app1", UserID: "user-a"},
		{ID: "s2", AppName: "app1", UserID: "user-b"},
		{ID: "s3", AppName: "app2", UserID: "user-a"},
	}
	for _, s := range sessions {
		if err := idx.Add(s); err != nil {
			t.Fatalf("Add(%s) error = %v", s.ID, err)
		}
	}

	// List all for app1
	result, err := idx.List("app1", "")
	if err != nil {
		t.Fatalf("List(app1, '') error = %v", err)
	}
	if len(result) != 2 {
		t.Errorf("List(app1, '') len = %d, want 2", len(result))
	}

	// List for app1, user-a only
	result, err = idx.List("app1", "user-a")
	if err != nil {
		t.Fatalf("List(app1, user-a) error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("List(app1, user-a) len = %d, want 1", len(result))
	}

	// List for app2
	result, err = idx.List("app2", "")
	if err != nil {
		t.Fatalf("List(app2, '') error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("List(app2, '') len = %d, want 1", len(result))
	}

	// List for nonexistent app
	result, err = idx.List("app3", "")
	if err != nil {
		t.Fatalf("List(app3, '') error = %v", err)
	}
	if len(result) != 0 {
		t.Errorf("List(app3, '') len = %d, want 0", len(result))
	}
}

func TestIndex_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	idx := NewSessionIndex(path)

	meta := SessionMeta{
		ID:           "sess-persist",
		AppName:      "app1",
		UserID:       "user-1",
		Title:        "Persistent",
		MessageCount: 10,
	}
	if err := idx.Add(meta); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Create a new index pointing to the same file
	idx2 := NewSessionIndex(path)
	got, err := idx2.Get("sess-persist")
	if err != nil {
		t.Fatalf("Get() on new index error = %v", err)
	}
	if got.Title != "Persistent" {
		t.Errorf("Title = %q, want %q", got.Title, "Persistent")
	}
	if got.MessageCount != 10 {
		t.Errorf("MessageCount = %d, want 10", got.MessageCount)
	}
}

func TestIndex_ConcurrentAdd(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	var wg sync.WaitGroup
	n := 10
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta := SessionMeta{
				ID:      fmt.Sprintf("concurrent-%d", i),
				AppName: "test-app",
				UserID:  "user-1",
			}
			_ = idx.Add(meta)
		}(i)
	}
	wg.Wait()

	data, err := idx.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(data.Sessions) != n {
		t.Errorf("Sessions len = %d, want %d", len(data.Sessions), n)
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "test.json")

	content := []byte(`{"test": true}`)
	if err := atomicWrite(path, content, 0644); err != nil {
		t.Fatalf("atomicWrite() error = %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content = %q, want %q", string(data), string(content))
	}
}

func TestIndex_ListFiltersSubSessions(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	sessions := []SessionMeta{
		{ID: "parent-1", AppName: "app1", UserID: "user-a"},
		{ID: "child-1", AppName: "app1", UserID: "user-a", ParentID: "parent-1"},
		{ID: "child-2", AppName: "app1", UserID: "user-a", ParentID: "parent-1"},
		{ID: "parent-2", AppName: "app1", UserID: "user-a"},
	}
	for _, s := range sessions {
		if err := idx.Add(s); err != nil {
			t.Fatalf("Add(%s) error = %v", s.ID, err)
		}
	}

	// List should only return top-level sessions (no ParentID)
	result, err := idx.List("app1", "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result) != 2 {
		t.Errorf("List() len = %d, want 2 (only top-level sessions)", len(result))
	}
	for _, m := range result {
		if m.ParentID != "" {
			t.Errorf("List() returned sub-session %s with ParentID=%q", m.ID, m.ParentID)
		}
	}
}

func TestIndex_ListChildren(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	sessions := []SessionMeta{
		{ID: "parent-1", AppName: "app1", UserID: "user-a"},
		{ID: "child-1", AppName: "app1", UserID: "user-a", ParentID: "parent-1"},
		{ID: "child-2", AppName: "app1", UserID: "user-a", ParentID: "parent-1"},
		{ID: "parent-2", AppName: "app1", UserID: "user-a"},
		{ID: "child-3", AppName: "app1", UserID: "user-a", ParentID: "parent-2"},
	}
	for _, s := range sessions {
		if err := idx.Add(s); err != nil {
			t.Fatalf("Add(%s) error = %v", s.ID, err)
		}
	}

	// Children of parent-1
	children, err := idx.ListChildren("parent-1")
	if err != nil {
		t.Fatalf("ListChildren(parent-1) error = %v", err)
	}
	if len(children) != 2 {
		t.Errorf("ListChildren(parent-1) len = %d, want 2", len(children))
	}

	// Children of parent-2
	children, err = idx.ListChildren("parent-2")
	if err != nil {
		t.Fatalf("ListChildren(parent-2) error = %v", err)
	}
	if len(children) != 1 {
		t.Errorf("ListChildren(parent-2) len = %d, want 1", len(children))
	}

	// No children
	children, err = idx.ListChildren("nonexistent")
	if err != nil {
		t.Fatalf("ListChildren(nonexistent) error = %v", err)
	}
	if len(children) != 0 {
		t.Errorf("ListChildren(nonexistent) len = %d, want 0", len(children))
	}
}

func TestIndex_RemoveCascadesChildren(t *testing.T) {
	dir := t.TempDir()
	idx := NewSessionIndex(filepath.Join(dir, "index.json"))

	sessions := []SessionMeta{
		{ID: "parent-1", AppName: "app1", UserID: "user-a"},
		{ID: "child-1", AppName: "app1", UserID: "user-a", ParentID: "parent-1"},
		{ID: "child-2", AppName: "app1", UserID: "user-a", ParentID: "parent-1"},
		{ID: "unrelated", AppName: "app1", UserID: "user-a"},
	}
	for _, s := range sessions {
		if err := idx.Add(s); err != nil {
			t.Fatalf("Add(%s) error = %v", s.ID, err)
		}
	}

	// Remove parent — should cascade children
	if err := idx.Remove("parent-1"); err != nil {
		t.Fatalf("Remove(parent-1) error = %v", err)
	}

	// Parent and children should be gone
	for _, id := range []string{"parent-1", "child-1", "child-2"} {
		_, err := idx.Get(id)
		if err == nil {
			t.Errorf("Get(%s) should return error after cascade delete, got nil", id)
		}
	}

	// Unrelated session should still exist
	_, err := idx.Get("unrelated")
	if err != nil {
		t.Errorf("Get(unrelated) error = %v, want nil (should survive cascade)", err)
	}
}

func TestIndex_IndependentInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "index.json")
	idx := NewSessionIndex(path)
	if err := idx.Add(SessionMeta{ID: "counter"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other := NewSessionIndex(path)
			for j := 0; j < 10; j++ {
				if err := other.Add(SessionMeta{ID: fmt.Sprintf("%d-%d", i, j)}); err != nil {
					t.Error(err)
					return
				}
				if err := other.Update("counter", func(m *SessionMeta) { m.MessageCount++ }); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	data, err := idx.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 81 || data.Sessions["counter"].MessageCount != 80 {
		t.Fatalf("lost mutations: %d sessions, counter %d", len(data.Sessions), data.Sessions["counter"].MessageCount)
	}
}

func TestIndex_SubprocessWrites(t *testing.T) {
	if path := os.Getenv("ASTONISH_INDEX_TEST_PATH"); path != "" {
		fmt.Println("ready")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		idx := NewSessionIndex(path)
		prefix := os.Getenv("ASTONISH_INDEX_TEST_WORKER")
		for i := 0; i < 30; i++ {
			id := fmt.Sprintf("%s-%d", prefix, i)
			if err := idx.Add(SessionMeta{ID: id}); err != nil {
				t.Fatal(err)
			}
			if err := idx.Update("counter", func(m *SessionMeta) {
				time.Sleep(time.Millisecond) // expose unlocked read/modify/write races
				m.MessageCount++
			}); err != nil {
				t.Fatal(err)
			}
			if i%2 == 0 {
				if err := idx.Remove(id); err != nil {
					t.Fatal(err)
				}
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "index.json")
	idx := NewSessionIndex(path)
	if err := idx.Add(SessionMeta{ID: "counter"}); err != nil {
		t.Fatal(err)
	}
	lockInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type worker struct {
		cmd   *exec.Cmd
		start func()
	}
	var workers []worker
	for i := 0; i < 4; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIndex_SubprocessWrites$")
		cmd.Env = append(os.Environ(), "ASTONISH_INDEX_TEST_PATH="+path, fmt.Sprintf("ASTONISH_INDEX_TEST_WORKER=%d", i))
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stdout)
		if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("worker ready: %q, %v", line, err)
		}
		go func() { _, _ = io.Copy(io.Discard, reader) }()
		workers = append(workers, worker{cmd: cmd, start: func() {
			_, _ = fmt.Fprintln(stdin, "start")
			_ = stdin.Close()
		}})
	}
	for _, w := range workers {
		w.start()
	}
	for _, w := range workers {
		if err := w.cmd.Wait(); err != nil {
			t.Fatalf("worker failed: %v", err)
		}
	}
	data, err := idx.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 61 || data.Sessions["counter"].MessageCount != 120 {
		t.Fatalf("lost subprocess mutations: %d sessions, counter %d", len(data.Sessions), data.Sessions["counter"].MessageCount)
	}
	newInfo, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(lockInfo, newInfo) {
		t.Fatalf("sidecar lock inode was replaced: %v", err)
	}
}

func TestIndex_RemoveSelectedPreservesNewSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	idx := NewSessionIndex(path)
	for _, meta := range []SessionMeta{{ID: "parent"}, {ID: "child", ParentID: "parent"}} {
		if err := idx.Add(meta); err != nil {
			t.Fatal(err)
		}
	}
	other := NewSessionIndex(path)
	for _, meta := range []SessionMeta{{ID: "new"}, {ID: "new-child", ParentID: "parent"}} {
		if err := other.Add(meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.RemoveSelected([]string{"parent", "child", "missing", "parent"}); err != nil {
		t.Fatal(err)
	}
	data, err := other.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 2 || data.Sessions["new"].ID != "new" || data.Sessions["new-child"].ID != "new-child" {
		t.Fatalf("unconfirmed sessions removed: %+v", data.Sessions)
	}
}

func TestIndex_SaveLockedReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	idx := NewSessionIndex(path)
	if err := idx.Add(SessionMeta{ID: "old"}); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(path+".lock", flock.SetFlag(os.O_CREATE|os.O_RDWR))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- idx.Save(&IndexData{Version: 1, Sessions: map[string]SessionMeta{"replacement": {ID: "replacement"}}})
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("Save returned while another instance held the lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := idx.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 1 || data.Sessions["replacement"].ID != "replacement" {
		t.Fatalf("Save must replace, not merge: %+v", data.Sessions)
	}
}

func TestIndex_LockFailureDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	idx := NewSessionIndex(path)
	if err := idx.Add(SessionMeta{ID: "keep"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".lock", 0755); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"add":      func() error { return idx.Add(SessionMeta{ID: "new"}) },
		"update":   func() error { return idx.Update("keep", func(m *SessionMeta) { m.Title = "changed" }) },
		"remove":   func() error { return idx.Remove("keep") },
		"selected": func() error { return idx.RemoveSelected([]string{"keep"}) },
		"save":     func() error { return idx.Save(&IndexData{Version: 1, Sessions: map[string]SessionMeta{}}) },
	} {
		if err := mutate(); err == nil {
			t.Errorf("%s unexpectedly succeeded without a lock", name)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("index changed despite lock failure")
	}
}
