package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestStartupEffectiveDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, backend string
		services      *store.Services
		want          string
		host          bool
	}{
		{"local empty DB", "file", &store.Services{Mode: store.ModePlatform, PlatformSettings: &providerMatrixPlatformStore{}}, "host", true},
		{"platform supplied without team", "sqlite", &store.Services{Mode: store.ModePlatform, PlatformSettings: &providerMatrixPlatformStore{value: &store.PlatformSettings{DefaultProvider: "shared", DefaultModel: "shared-model", Providers: map[string]map[string]string{"shared": {"type": "openai"}}}}}, "shared", false},
		{"local platform defaults override YAML", "file", &store.Services{Mode: store.ModePlatform, PlatformSettings: &providerMatrixPlatformStore{value: &store.PlatformSettings{DefaultProvider: "shared", DefaultModel: "shared-model"}}}, "shared", true},
		{"org defaults override platform without team", "sqlite", &store.Services{Mode: store.ModePlatform, PlatformSettings: &providerMatrixPlatformStore{value: &store.PlatformSettings{DefaultProvider: "shared", DefaultModel: "shared-model"}}, OrgSettings: &providerMatrixOrgStore{value: &store.OrgSettings{DefaultProvider: "org", DefaultModel: "org-model"}}}, "org", false},
		{"sqlite empty DB no host inheritance", "sqlite", &store.Services{Mode: store.ModePlatform, PlatformSettings: &providerMatrixPlatformStore{}}, "", false},
		{"postgres no stores no host inheritance", "postgres", &store.Services{Mode: store.ModePlatform}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			dir := filepath.Join(home, "astonish")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			data := []byte("storage:\n  backend: " + tc.backend + "\ngeneral:\n  default_provider: host\n  default_model: host-model\nproviders:\n  host:\n    type: openai\n    api_key: fixture-only-secret\n")
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx := store.WithServices(context.Background(), tc.services)
			for _, request := range []bool{false, true} {
				got := EffectiveAppConfigFromContext(ctx, true)
				if request {
					got = effectiveAppConfig(httptest.NewRequest("GET", "/", nil).WithContext(ctx))
				}
				if got.General.DefaultProvider != tc.want {
					t.Errorf("request=%v provider=%q want=%q", request, got.General.DefaultProvider, tc.want)
				}
				wantModel := ""
				if tc.want != "" {
					wantModel = tc.want + "-model"
				}
				if got.General.DefaultModel != wantModel {
					t.Errorf("request=%v model=%q want=%q", request, got.General.DefaultModel, wantModel)
				}
				_, host := got.Providers["host"]
				if host != tc.host {
					t.Errorf("request=%v host inheritance=%v want=%v", request, host, tc.host)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatal("resolution mutated source config")
			}
		})
	}
}
