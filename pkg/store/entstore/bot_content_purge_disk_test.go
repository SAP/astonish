package entstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotStagingPurgeLinksAndBatching(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	disk, err := newBotDiskContent(dir, store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), BotID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("foreign bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := disk.scopeRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Symlink(outside, "foreign-link"); err != nil {
		t.Fatal(err)
	}
	// More than two batches, covering all orphaned/uncommitted revisions.
	for i := 0; i < 300; i++ {
		file, err := root.Create(fmt.Sprintf(".stage-%s", uuid.NewString()))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := disk.purgeNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.root.Lstat(disk.namespace()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("namespace remains: %v", err)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "foreign bytes" {
		t.Fatalf("link followed: %q %v", body, err)
	}
	if err := disk.purgeNamespace(ctx); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	// A namespace symlink cannot redirect cleanup into a different Bot or directory.
	if err := disk.root.Symlink(filepath.Dir(outside), disk.namespace()); err != nil {
		t.Fatal(err)
	}
	if err := disk.purgeNamespace(ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("namespace symlink accepted: %v", err)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "foreign bytes" {
		t.Fatalf("namespace link followed: %q %v", body, err)
	}
}

func TestBotStagingPurgeRejectsIndependentNamespaces(t *testing.T) {
	for _, scope := range []store.BotContentScope{
		{OrgID: "org", TeamID: "team"},
		{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), IntakeID: uuid.New()},
	} {
		disk, err := newBotDiskContent(t.TempDir(), scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := disk.purgeNamespace(context.Background()); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("independent namespace purge: %v", err)
		}
		_ = disk.Close()
	}
}
