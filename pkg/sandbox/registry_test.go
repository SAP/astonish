package sandbox

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// newTestRegistry creates a SessionRegistry backed by a temp-file local
// store. Post-cutover the registry owns a store.SandboxSessionStore
// internally; tests exercise it through the public API rather than
// touching internal fields.
func newTestRegistry(t *testing.T) *SessionRegistry {
	t.Helper()
	dir := t.TempDir()
	st, err := newLocalSessionStore(dir)
	if err != nil {
		t.Fatalf("newLocalSessionStore: %v", err)
	}
	return NewSessionRegistryFromStore(st)
}

// newTestRegistryAt creates a registry rooted at the caller-supplied
// directory. Used by persistence tests that need a second registry instance
// pointed at the same files.
func newTestRegistryAt(t *testing.T, dir string) *SessionRegistry {
	t.Helper()
	st, err := newLocalSessionStore(dir)
	if err != nil {
		t.Fatalf("newLocalSessionStore: %v", err)
	}
	return NewSessionRegistryFromStore(st)
}

func TestSessionRegistryScopesSessionToResolvedStore(t *testing.T) {
	defaultReg := newTestRegistry(t)
	teamDir := t.TempDir()
	teamReg := newTestRegistryAt(t, teamDir)

	defaultReg.SetStoreResolver(func(orgSlug, teamSlug string) store.SandboxSessionStore {
		if orgSlug == "org" && teamSlug == "team" {
			return teamReg.store
		}
		return nil
	})
	defaultReg.SetSessionScope("session-personal", "org", "team")

	if err := defaultReg.PutSession(&store.SandboxSession{
		SessionID: "session-personal",
		Backend:   "k8s",
		PodName:   "astn-session-personal",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if got := teamReg.Get("session-personal"); got == nil {
		t.Fatal("session was not persisted to the resolved team store")
	}
	if got, err := defaultReg.GetSession("session-personal"); err != nil || got == nil {
		t.Fatal("session was not readable through the resolved store")
	}

	// A later rebind/lifecycle write must stay on the original team store even
	// if a different scope is supplied after the session already exists.
	defaultReg.SetSessionScope("session-personal", "org", "other-team")
	defaultReg.TouchActivity("session-personal")
	if got := teamReg.Get("session-personal"); got == nil {
		t.Fatal("session moved away from its original team store")
	}
}

func TestSessionRegistryPutGet(t *testing.T) {
	r := newTestRegistry(t)

	err := r.Put("sess-1", "astn-sess-abcd1234", "base")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	entry := r.Get("sess-1")
	if entry == nil {
		t.Fatal("Get returned nil")
	}
	if entry.ContainerName != "astn-sess-abcd1234" {
		t.Errorf("ContainerName = %q, want astn-sess-abcd1234", entry.ContainerName)
	}
	if entry.TemplateName != "base" {
		t.Errorf("TemplateName = %q, want base", entry.TemplateName)
	}
}

func TestSessionRegistryRemove(t *testing.T) {
	r := newTestRegistry(t)

	_ = r.Put("sess-1", "astn-sess-abcd1234", "base")
	_ = r.Put("sess-2", "astn-sess-efgh5678", "base")

	if err := r.Remove("sess-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if r.Get("sess-1") != nil {
		t.Error("sess-1 should be removed")
	}
	if r.Get("sess-2") == nil {
		t.Error("sess-2 should still exist")
	}
}

func TestSessionRegistryGetContainerName(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abcd1234", "base")

	if got := r.GetContainerName("sess-1"); got != "astn-sess-abcd1234" {
		t.Errorf("got %q, want astn-sess-abcd1234", got)
	}
	if got := r.GetContainerName("nonexistent"); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestSessionRegistryList(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "c1", "base")
	_ = r.Put("sess-2", "c2", "custom")

	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("List() returned %d entries, want 2", len(entries))
	}
}

func TestSessionRegistrySessionIDs(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "c1", "base")
	_ = r.Put("sess-2", "c2", "base")

	ids := r.SessionIDs()
	if len(ids) != 2 {
		t.Fatalf("SessionIDs() returned %d, want 2", len(ids))
	}

	found := make(map[string]bool)
	for _, id := range ids {
		found[id] = true
	}
	if !found["sess-1"] || !found["sess-2"] {
		t.Errorf("expected sess-1 and sess-2, got %v", ids)
	}
}

func TestSessionRegistryResolveSessionID(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("abc12345-6789", "astn-sess-abc12345", "base")

	tests := []struct {
		name  string
		input string
		want  string
		found bool
	}{
		{"exact session ID", "abc12345-6789", "abc12345-6789", true},
		{"container name", "astn-sess-abc12345", "abc12345-6789", true},
		{"session ID prefix", "abc12", "abc12345-6789", true},
		{"container prefix", "astn-sess-abc", "abc12345-6789", true},
		{"no match", "zzz", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := r.ResolveSessionID(tt.input)
			if ok != tt.found {
				t.Errorf("found = %v, want %v", ok, tt.found)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionRegistrySaveLoad(t *testing.T) {
	dir := t.TempDir()

	// Create and populate
	r1 := newTestRegistryAt(t, dir)
	_ = r1.Put("sess-1", "c1", "base")
	_ = r1.Put("sess-2", "c2", "custom")

	// Second registry reads the same underlying file.
	r2 := newTestRegistryAt(t, dir)
	if e := r2.Get("sess-1"); e == nil || e.ContainerName != "c1" {
		t.Error("sess-1 not loaded correctly")
	}
	if e := r2.Get("sess-2"); e == nil || e.ContainerName != "c2" {
		t.Error("sess-2 not loaded correctly")
	}
}

func TestSessionRegistryLoadMissing(t *testing.T) {
	// Missing sandbox_sessions.json is not an error; the registry starts
	// empty. This replaces the old Load() ENOENT check.
	dir := t.TempDir()
	r := newTestRegistryAt(t, dir)
	if got := r.List(); len(got) != 0 {
		t.Errorf("expected empty registry when file is missing, got %d entries", len(got))
	}
	// Verify the directory really has no sandbox_sessions.json yet.
	if _, err := os.Stat(filepath.Join(dir, "sandbox_sessions.json")); err == nil {
		t.Error("sandbox_sessions.json should not exist until a write occurs")
	}
}

func TestSessionRegistryConcurrentPutRemove(t *testing.T) {
	r := newTestRegistry(t)
	var wg sync.WaitGroup

	// Concurrent puts
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "sess-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
			_ = r.Put(id, "c-"+id, "base")
		}(i)
	}
	wg.Wait()

	// Concurrent removes
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "sess-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
			_ = r.Remove(id)
		}(i)
	}
	wg.Wait()

	// Should not have panicked; verify the registry is in a consistent state
	entries := r.List()
	ids := r.SessionIDs()
	if len(entries) != len(ids) {
		t.Errorf("List() has %d entries but SessionIDs() has %d", len(entries), len(ids))
	}
}

func TestSessionRegistryGetByContainerName(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abc123", "base")
	_ = r.Put("sess-2", "astn-sess-def456", "custom")

	entry := r.GetByContainerName("astn-sess-abc123")
	if entry == nil {
		t.Fatal("expected entry for astn-sess-abc123")
	}
	if entry.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", entry.SessionID)
	}

	entry = r.GetByContainerName("astn-sess-def456")
	if entry == nil {
		t.Fatal("expected entry for astn-sess-def456")
	}
	if entry.SessionID != "sess-2" {
		t.Errorf("SessionID = %q, want sess-2", entry.SessionID)
	}

	entry = r.GetByContainerName("nonexistent")
	if entry != nil {
		t.Errorf("expected nil for nonexistent container, got %+v", entry)
	}
}

func TestSessionRegistryExposePort(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abc123", "base")

	added, err := r.ExposePort("astn-sess-abc123", 3000)
	if err != nil {
		t.Fatalf("ExposePort: %v", err)
	}
	if !added {
		t.Error("expected port to be newly added")
	}

	if !r.IsPortExposed("astn-sess-abc123", 3000) {
		t.Error("port 3000 should be exposed")
	}

	added, err = r.ExposePort("astn-sess-abc123", 3000)
	if err != nil {
		t.Fatalf("ExposePort duplicate: %v", err)
	}
	if added {
		t.Error("expected port to NOT be newly added (duplicate)")
	}

	added, err = r.ExposePort("astn-sess-abc123", 8080)
	if err != nil {
		t.Fatalf("ExposePort 8080: %v", err)
	}
	if !added {
		t.Error("expected port 8080 to be newly added")
	}

	entry := r.GetByContainerName("astn-sess-abc123")
	if entry == nil {
		t.Fatal("entry not found")
	}
	if len(entry.ExposedPorts) != 2 {
		t.Fatalf("ExposedPorts has %d entries, want 2", len(entry.ExposedPorts))
	}

	_, err = r.ExposePort("nonexistent", 3000)
	if err == nil {
		t.Error("expected error for nonexistent container")
	}
}

func TestSessionRegistryUnexposePort(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abc123", "base")

	_, _ = r.ExposePort("astn-sess-abc123", 3000)
	_, _ = r.ExposePort("astn-sess-abc123", 8080)

	removed, err := r.UnexposePort("astn-sess-abc123", 3000)
	if err != nil {
		t.Fatalf("UnexposePort: %v", err)
	}
	if !removed {
		t.Error("expected port 3000 to be removed")
	}

	if r.IsPortExposed("astn-sess-abc123", 3000) {
		t.Error("port 3000 should no longer be exposed")
	}
	if !r.IsPortExposed("astn-sess-abc123", 8080) {
		t.Error("port 8080 should still be exposed")
	}

	removed, err = r.UnexposePort("astn-sess-abc123", 9999)
	if err != nil {
		t.Fatalf("UnexposePort 9999: %v", err)
	}
	if removed {
		t.Error("expected port 9999 to NOT be removed (not exposed)")
	}

	_, err = r.UnexposePort("nonexistent", 3000)
	if err == nil {
		t.Error("expected error for nonexistent container")
	}
}

func TestSessionRegistryIsPortExposed(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abc123", "base")

	if r.IsPortExposed("astn-sess-abc123", 3000) {
		t.Error("port 3000 should not be exposed initially")
	}
	if r.IsPortExposed("nonexistent", 3000) {
		t.Error("should return false for nonexistent container")
	}

	_, _ = r.ExposePort("astn-sess-abc123", 3000)
	if !r.IsPortExposed("astn-sess-abc123", 3000) {
		t.Error("port 3000 should be exposed after ExposePort")
	}
	if r.IsPortExposed("astn-sess-abc123", 8080) {
		t.Error("port 8080 should not be exposed")
	}
}

func TestSessionRegistryExposedPortsPersistence(t *testing.T) {
	dir := t.TempDir()

	r1 := newTestRegistryAt(t, dir)
	_ = r1.Put("sess-1", "astn-sess-abc123", "base")
	_, _ = r1.ExposePort("astn-sess-abc123", 3000)
	_, _ = r1.ExposePort("astn-sess-abc123", 8080)

	r2 := newTestRegistryAt(t, dir)
	if !r2.IsPortExposed("astn-sess-abc123", 3000) {
		t.Error("port 3000 should persist across save/load")
	}
	if !r2.IsPortExposed("astn-sess-abc123", 8080) {
		t.Error("port 8080 should persist across save/load")
	}
	if r2.IsPortExposed("astn-sess-abc123", 9999) {
		t.Error("port 9999 should not be exposed")
	}
}

func TestBaseDomainSetAndGet(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abc123", "base")

	if got := r.GetBaseDomain("astn-sess-abc123"); got != "" {
		t.Errorf("GetBaseDomain initially = %q, want empty", got)
	}

	if err := r.SetBaseDomain("astn-sess-abc123", "astonish.local.muxpie.com"); err != nil {
		t.Fatalf("SetBaseDomain: %v", err)
	}

	if got := r.GetBaseDomain("astn-sess-abc123"); got != "astonish.local.muxpie.com" {
		t.Errorf("GetBaseDomain = %q, want %q", got, "astonish.local.muxpie.com")
	}

	if got := r.GetBaseDomain("nonexistent"); got != "" {
		t.Errorf("GetBaseDomain for unknown = %q, want empty", got)
	}

	if err := r.SetBaseDomain("nonexistent", "example.com"); err == nil {
		t.Error("SetBaseDomain for unknown container should return error")
	}
}

func TestBaseDomainPersistence(t *testing.T) {
	dir := t.TempDir()
	r1 := newTestRegistryAt(t, dir)

	_ = r1.Put("sess-1", "astn-sess-abc123", "base")
	_ = r1.SetBaseDomain("astn-sess-abc123", "astonish.local.muxpie.com")

	r2 := newTestRegistryAt(t, dir)
	if got := r2.GetBaseDomain("astn-sess-abc123"); got != "astonish.local.muxpie.com" {
		t.Errorf("GetBaseDomain after reload = %q, want %q", got, "astonish.local.muxpie.com")
	}
}

func TestSetPinned(t *testing.T) {
	r := newTestRegistry(t)
	_ = r.Put("sess-1", "astn-sess-abc123", "base")

	entry := r.GetByContainerName("astn-sess-abc123")
	if entry.Pinned {
		t.Error("expected Pinned to be false by default")
	}

	if err := r.SetPinned("astn-sess-abc123", true); err != nil {
		t.Fatalf("SetPinned(true): %v", err)
	}
	entry = r.GetByContainerName("astn-sess-abc123")
	if !entry.Pinned {
		t.Error("expected Pinned to be true after SetPinned(true)")
	}

	if err := r.SetPinned("astn-sess-abc123", false); err != nil {
		t.Fatalf("SetPinned(false): %v", err)
	}
	entry = r.GetByContainerName("astn-sess-abc123")
	if entry.Pinned {
		t.Error("expected Pinned to be false after SetPinned(false)")
	}

	if err := r.SetPinned("nonexistent", true); err == nil {
		t.Error("SetPinned for unknown container should return error")
	}
}

func TestPinnedPersistence(t *testing.T) {
	dir := t.TempDir()
	r1 := newTestRegistryAt(t, dir)

	_ = r1.Put("sess-1", "astn-sess-abc123", "base")
	_ = r1.SetPinned("astn-sess-abc123", true)

	r2 := newTestRegistryAt(t, dir)
	entry := r2.GetByContainerName("astn-sess-abc123")
	if entry == nil {
		t.Fatal("expected entry to exist after reload")
	}
	if !entry.Pinned {
		t.Error("expected Pinned to persist across save/load")
	}
}
