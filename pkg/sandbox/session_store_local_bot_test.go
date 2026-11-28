package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestLocalBotHomeBindingReopen(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	s, err := NewLocalSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := &store.SandboxSession{SessionID: uuid.NewString(), ContainerName: "ordinary", Backend: "incus", State: store.SandboxSessionStateEvicted}
	if err := s.Put(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(ctx, ordinary.SessionID)
	binding := store.BotHomeBinding{ID: uuid.New(), BotID: uuid.New(), Scope: store.BotScopePersonal, Backend: "fixture", StorageKey: "opaque-key", Revision: 1}
	row := &store.SandboxSession{SessionID: uuid.NewString(), ContainerName: "metadata-only", Backend: "fixture", State: store.SandboxSessionStateEvicted, BotHomeBinding: &binding}
	want := binding
	if err := s.Put(ctx, row); err != nil {
		t.Fatal(err)
	}
	binding.StorageKey = "mutated-input"
	got, _ := s.Get(ctx, row.SessionID)
	if *got.BotHomeBinding != want {
		t.Fatal("input alias")
	}
	got.BotHomeBinding.StorageKey = "mutated-output"
	rows, _ := s.List(ctx, store.SandboxSessionFilter{ContainerName: row.ContainerName})
	if *rows[0].BotHomeBinding != want {
		t.Fatal("output alias")
	}
	rows[0].BotHomeBinding.StorageKey = "mutated-list"
	got, _ = s.GetByContainerName(ctx, row.ContainerName)
	if *got.BotHomeBinding != want {
		t.Fatal("list alias")
	}
	if err := s.SetPinned(ctx, row.SessionID, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLocalSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = reopened.Get(ctx, row.SessionID)
	if got.BotHomeBinding == nil || *got.BotHomeBinding != want || !got.Pinned {
		t.Fatalf("reopen: %+v", got)
	}
	after, _ := reopened.Get(ctx, ordinary.SessionID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("ordinary record changed: %+v %+v", before, after)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sandbox_sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw {
		if r["session_id"] == ordinary.SessionID {
			if _, ok := r["bot_home_binding"]; ok {
				t.Fatal("ordinary binding serialized")
			}
		}
	}
}
