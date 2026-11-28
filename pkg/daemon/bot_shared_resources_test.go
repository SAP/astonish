package daemon

import (
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store/entstore"
	"path/filepath"
	"testing"
)

// Test-owned bootstrap mirrors Chat ownership; worker cleanup must not own it.
func botTestChatResources(t *testing.T, cfg *config.AppConfig) *launcher.ChatCatalogResources {
	t.Helper()
	b, cleanup, err := sandbox.BackendFromAppConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{})
	t.Cleanup(pool.Cleanup)
	return &launcher.ChatCatalogResources{Backend: b, Pool: pool}
}

func TestProductionBotWorkerBorrowsChatResources(t *testing.T) {
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(t.Context(), entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b := newRegressionHomeBackend(t)
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{})
	defer pool.Cleanup()
	shared := &launcher.ChatCatalogResources{Backend: b, Pool: pool}
	worker, release, err := newProductionBotWorker(db, &config.AppConfig{}, shared, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	if worker.config.Pool != pool || worker.config.Sandbox != b {
		t.Fatal("worker replaced borrowed infrastructure")
	}
	client := pool.GetOrCreate("test-owned-lazy-session")
	release()
	if pool.GetOrCreate("test-owned-lazy-session") != client {
		t.Fatal("worker cleanup closed borrowed pool")
	}
	if _, _, err := newProductionBotWorker(db, &config.AppConfig{}, nil, nil); err == nil {
		t.Fatal("missing shared infrastructure accepted")
	}
}
