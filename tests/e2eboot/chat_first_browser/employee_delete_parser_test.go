package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Optional offline diagnosis of the original failure. Never opens its database,
// invokes a provider, changes saved evidence, or resumes the failed workflow.
func TestEmployeeDeleteSavedArtifact(t *testing.T) {
	root := os.Getenv("EMPLOYEE_DELETE_SAVED_ARTIFACT")
	if root == "" {
		t.Skip("offline saved-artifact opt-in")
	}
	raw, err := os.ReadFile(filepath.Join(root, "employee-peer-disk-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk employeeDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Bots) != 1 {
		t.Fatal("expected exact peer row")
	}
	row := disk.Bots[0]
	document, _, err := employeeStoredDocument(row.HomeBinding)
	if err != nil {
		t.Fatal(err)
	}
	var binding store.BotHomeBinding
	if err := json.Unmarshal([]byte(document), &binding); err != nil {
		t.Fatal(err)
	}
	// Reproduce the former second decode on the exact saved field. This is
	// offline diagnosis, not a fallback accepted by the harness.
	var nested json.RawMessage
	legacyErr := json.Unmarshal([]byte(binding.StorageKey), &nested)
	if legacyErr == nil || legacyErr.Error() != "invalid character 'a' looking for beginning of value" {
		t.Fatalf("saved field did not reproduce original parser failure: %v", legacyErr)
	}
	t.Logf("former storage_key JSON decode: %v", legacyErr)
	got, err := employeeDiskHomeVolume(raw, "employee-peer-disk-before.json", row.ID)
	if err != nil {
		t.Fatalf("employee-peer-disk-before.json bots[0].home_binding -> bot_document -> storage_key=%q -> employeeHomeVolume: %v", binding.StorageKey, err)
	}
	if got != binding.StorageKey {
		t.Fatal("storage key changed")
	}
	t.Logf("exact saved peer=%s volume=%s", row.ID, got)
	// Re-evaluate the exact earlier SSE artifact and run receipt. Their names
	// remain historical: that invocation saved raw SSE with a .json suffix.
	read := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var identity struct {
		Bot store.Bot `json:"bot"`
	}
	if err := json.Unmarshal(read("employee-peer-identity.json"), &identity); err != nil {
		t.Fatal(err)
	}
	var runs struct{ Runs []store.BotRun }
	if err := json.Unmarshal(read("employee-peer-title-runs.json"), &runs); err != nil {
		t.Fatal(err)
	}
	title, err := employeePeerTitleRun(string(read("employee-peer-title-history.json")), identity.Bot, runs.Runs)
	if err != nil || title.ID == uuid.Nil {
		t.Fatalf("saved title SSE: %v", err)
	}
	if title.ID.String() != "6bce92dd-4eab-4d37-97f8-766383d7fb05" || title.UserEventID != "bot-message:6bce92dd-4eab-4d37-97f8-766383d7fb05" || title.HistoryCursor != 377 {
		t.Fatal("historical title source mismatch")
	}
	t.Logf("exact saved title run=%s user_event=%s cursor=%d", title.ID, title.UserEventID, title.HistoryCursor)
}

func TestEmployeeHomeVolumeOpaqueStorageKey(t *testing.T) {
	b := store.BotHomeBinding{ID: uuid.New(), BotID: uuid.New(), Scope: store.BotScopePersonal, Backend: "docker", StorageKey: "astonish-bot-home-0123456789abcdef", Revision: 1}
	for _, wrapped := range []bool{false, true} {
		raw := employeeDiskJSON(t, b)
		if wrapped {
			raw = employeeDiskJSON(t, map[string]string{"bot_document": raw})
		}
		got, err := employeeHomeVolume(raw, b.BotID.String())
		if err != nil || got != b.StorageKey {
			t.Fatalf("canonical opaque storage key: got %q err %v", got, err)
		}
	}
}
