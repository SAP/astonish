package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

func TestProvisionSingleUser(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	entCfg := entstore.Config{
		DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
		DataDir: dataDir,
	}
	if err := entstore.BootstrapPlatform(ctx, entCfg, nil); err != nil {
		t.Fatalf("bootstrap platform: %v", err)
	}
	backend, err := entstore.New(ctx, entCfg)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	if err := store.ProvisionSingleUser(ctx, backend, "Local Organization", "local"); err != nil {
		t.Fatalf("provision single user: %v", err)
	}
	if err := store.ProvisionSingleUser(ctx, backend, "Local Organization", "local"); err != nil {
		t.Fatalf("repeat provisioning should be idempotent: %v", err)
	}

	user, err := backend.Users().GetByID(ctx, config.SingleUserID)
	if err != nil {
		t.Fatalf("get default user: %v", err)
	}
	if user.Email != config.SingleUserEmail || user.PlatformRole != "superadmin" {
		t.Fatalf("unexpected default user: %#v", user)
	}
	if count, err := backend.Organizations().Count(ctx); err != nil || count != 1 {
		t.Fatalf("organization count = %d, %v; want 1, nil", count, err)
	}
	org, err := backend.Organizations().GetBySlug(ctx, "local")
	if err != nil {
		t.Fatalf("get default organization: %v", err)
	}
	orgStore, err := backend.ForOrg(org.Slug)
	if err != nil {
		t.Fatalf("open default organization: %v", err)
	}
	teams, err := orgStore.Teams().ListTeams(ctx)
	if err != nil || len(teams) != 1 || teams[0].Slug != "general" {
		t.Fatalf("default teams = %#v, %v; want one general team", teams, err)
	}
}
