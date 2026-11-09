package entstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotTenantMiddlewarePreservesStartupAuthority(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "startup", Name: "Startup", Status: "active"}
	if err := db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	registry := execution.NewBotTurnAuthorizationRegistry(1)
	defer registry.Close()
	for _, ready := range []bool{false, true} {
		base.BotInteractiveReady = ready
		base.BotTurnRegistry = registry
		called := false
		handler := TenantMiddleware(db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			scoped := store.FromRequest(r)
			if scoped == base {
				t.Error("request must have its own tenant service bundle")
			}
			if scoped.BotInteractiveReady != ready {
				t.Errorf("startup readiness lost: got %v want %v", scoped.BotInteractiveReady, ready)
			}
			if scoped.BotTurnRegistry != registry {
				t.Error("startup turn registry lost or replaced")
			}
		}))
		request := httptest.NewRequest("POST", "/api/studio/bots?scope=personal", nil)
		request = request.WithContext(store.WithTenantContext(store.WithServices(ctx, base), &store.TenantContext{OrgSlug: org.Slug}))
		handler.ServeHTTP(httptest.NewRecorder(), request)
		if !called {
			t.Fatal("tenant handler not reached")
		}
	}
}
