package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestResolveContextWindow_ConfigOverride(t *testing.T) {
	cfg := &config.AppConfig{
		General: config.GeneralConfig{
			ContextLength: 500000,
		},
	}
	got := ResolveContextWindow(context.Background(), "sap_ai_core", "anthropic--claude-4.6-opus", cfg)
	if got != 500000 {
		t.Errorf("ResolveContextWindow() = %d, want 500000 (config override)", got)
	}
}

func TestResolveContextWindow_StaticMap(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-3-opus", 200_000},
		{"claude-3.5-sonnet", 200_000},
		{"gpt-4o", 128_000},
		{"gpt-4-turbo", 128_000},
		{"gpt-4", 8_192},
		{"gpt-3.5-turbo", 16_385},
		{"gemini-2.0-flash", 1_048_576},
		{"gemini-1.5-flash", 1_048_576},
		{"llama-3.3-70b", 131_072},
		{"grok-4.6", 500_000},
		{"grok-4.3", 1_000_000},
		{"grok-3", 131_072},
		{"deepseek-chat", 128_000},
		{"mistral-large-latest", 128_000},
		{"gpt-5", 272_000},
		{"gpt-4.1", 1_047_576},
		{"gemini-2.5-pro", 1_048_576},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := ResolveFromStaticMap(tt.model)
			if got != tt.want {
				t.Errorf("ResolveFromStaticMap(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveContextWindow_UnknownModelDefault(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{},
	}
	got := ResolveContextWindow(context.Background(), "unknown_provider", "totally-unknown-model-xyz", cfg)
	if got != DefaultContextWindow {
		t.Errorf("ResolveContextWindow() = %d, want %d (default)", got, DefaultContextWindow)
	}
}

func TestResolveContextWindow_SAPProvider(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"sap_ai_core": {"type": "sap_ai_core"},
		},
	}
	// Without credentials, SAP known models still resolve from static fallback.
	got := ResolveContextWindow(context.Background(), "sap_ai_core", "anthropic--claude-3.5-sonnet", cfg)
	if got <= 0 {
		t.Errorf("ResolveContextWindow() = %d, want > 0 for SAP known model", got)
	}
}

func TestResolveContextWindowCached(t *testing.T) {
	cfg := &config.AppConfig{
		General: config.GeneralConfig{
			ContextLength: 42000,
		},
	}

	// First call
	got1 := ResolveContextWindowCached(context.Background(), "test", "model", cfg)
	if got1 != 42000 {
		t.Errorf("first call = %d, want 42000", got1)
	}

	// Second call should return cached value even if config changes
	cfg.General.ContextLength = 99999
	got2 := ResolveContextWindowCached(context.Background(), "test", "model", cfg)
	if got2 != 42000 {
		t.Errorf("cached call = %d, want 42000 (should be cached)", got2)
	}

	// After invalidation, should reflect new value
	InvalidateContextWindowCache()
	got3 := ResolveContextWindowCached(context.Background(), "test", "model", cfg)
	if got3 != 99999 {
		t.Errorf("after invalidation = %d, want 99999", got3)
	}

	// Cleanup
	InvalidateContextWindowCache()
}

func TestResolveFromStaticMap_UnknownModel(t *testing.T) {
	got := ResolveFromStaticMap("totally-custom-model-v1")
	if got != 0 {
		t.Errorf("ResolveFromStaticMap(unknown) = %d, want 0", got)
	}
}

func sapContextFixture(t *testing.T, capacity int, unavailable bool) (*config.AppConfig, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	calls := &atomic.Int32{}
	outage := &atomic.Bool{}
	outage.Store(unavailable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			fmt.Fprint(w, `{"access_token":"fixture-token","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		calls.Add(1)
		if outage.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v2/lm/scenarios/foundation-models/models" || r.Header.Get("AI-Resource-Group") != "fixture" {
			t.Errorf("unexpected catalog request: %s headers %v", r.URL.Path, r.Header)
		}
		fmt.Fprintf(w, `{"resources":[{"model":"gpt-6-astra","versions":[{"isLatest":true,"contextLength":%d}]},{"model":"anthropic--claude-3.5-sonnet","versions":[{"isLatest":true,"contextLength":%d}]}]}`, capacity, capacity)
	}))
	t.Cleanup(server.Close)
	cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{
		"corporate-sap": {"type": "sap_ai_core", "client_id": "fixture", "client_secret": "secret", "auth_url": server.URL, "base_url": server.URL, "resource_group": "fixture"},
	}}
	return cfg, calls, outage
}

func TestResolveContextWindow_SAPCatalog(t *testing.T) {
	ctx := WithConfinedProviderSecrets(context.Background())
	for _, tc := range []struct {
		name     string
		capacity int
		outage   bool
		model    string
		want     ContextWindowResult
	}{
		{"catalog", 1_100_000, false, "gpt-6-astra", ContextWindowResult{Size: 1_100_000}},
		{"catalog overrides known static", 1_100_000, false, "anthropic--claude-3.5-sonnet", ContextWindowResult{Size: 1_100_000}},
		{"missing", 1_100_000, false, "unknown-sap-model", ContextWindowResult{Size: 200_000, IsFallback: true}},
		{"invalid", 0, false, "gpt-6-astra", ContextWindowResult{Size: 200_000, IsFallback: true}},
		{"negative", -1, false, "gpt-6-astra", ContextWindowResult{Size: 200_000, IsFallback: true}},
		{"outage", 1_100_000, true, "gpt-6-astra", ContextWindowResult{Size: 200_000, IsFallback: true}},
		{"static 200k is real", 1_100_000, true, "anthropic--claude-3.5-sonnet", ContextWindowResult{Size: 200_000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, _ := sapContextFixture(t, tc.capacity, tc.outage)
			if got := ResolveContextWindowFull(ctx, "corporate-sap", tc.model, cfg); got != tc.want {
				t.Fatalf("context = %+v, want %+v", got, tc.want)
			}
		})
	}
	t.Run("override avoids network", func(t *testing.T) {
		cfg, calls, _ := sapContextFixture(t, 1_100_000, false)
		cfg.General.ContextLength = 42_000
		if got := ResolveContextWindowFull(ctx, "corporate-sap", "gpt-6-astra", cfg); got.Size != 42_000 || got.IsFallback || calls.Load() != 0 {
			t.Fatalf("override = %+v, requests = %d", got, calls.Load())
		}
	})
	t.Run("confined secrets", func(t *testing.T) {
		cfg, calls, _ := sapContextFixture(t, 1_100_000, false)
		t.Setenv("AICORE_CLIENT_SECRET", "operator-secret")
		delete(cfg.Providers["corporate-sap"], "client_secret")
		if got := ResolveContextWindowFull(ctx, "corporate-sap", "gpt-6-astra", cfg); !got.IsFallback || calls.Load() != 0 {
			t.Fatalf("confined = %+v, requests = %d", got, calls.Load())
		}
		if got := ResolveContextWindowFull(context.Background(), "corporate-sap", "gpt-6-astra", cfg); got.Size != 1_100_000 {
			t.Fatalf("trusted local env fallback = %+v", got)
		}
	})
}

func TestResolveContextWindowCached_SAPIsolationRefresh(t *testing.T) {
	ctx := WithConfinedProviderSecrets(context.Background())
	cfg, calls, outage := sapContextFixture(t, 1_100_000, true)
	resolve := func(cfg *config.AppConfig, want ContextWindowResult) {
		t.Helper()
		if got := ResolveContextWindowCachedFull(ctx, "corporate-sap", "gpt-6-astra", cfg); got != want {
			t.Fatalf("cached context = %+v, want %+v", got, want)
		}
	}
	resolve(cfg, ContextWindowResult{Size: 200_000, IsFallback: true})
	outage.Store(false)
	resolve(cfg, ContextWindowResult{Size: 1_100_000})
	resolve(cfg, ContextWindowResult{Size: 1_100_000})
	if calls.Load() != 2 {
		t.Fatalf("expected retry then adapter cache, got %d calls", calls.Load())
	}
	other, _, _ := sapContextFixture(t, 700_000, false)
	resolve(other, ContextWindowResult{Size: 700_000})
	cfg.Providers["corporate-sap"]["client_secret"] = "rotated"
	resolve(cfg, ContextWindowResult{Size: 1_100_000})
	if calls.Load() != 3 {
		t.Fatal("credential rotation did not refresh catalog")
	}
	cfg.General.ContextLength = 42_000
	resolve(cfg, ContextWindowResult{Size: 42_000})
	cfg.General.ContextLength = 84_000
	resolve(cfg, ContextWindowResult{Size: 84_000})
	cfg.General.ContextLength = 0
	resolve(cfg, ContextWindowResult{Size: 1_100_000})
}
