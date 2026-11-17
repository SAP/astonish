package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestResolveBudgetOutputTokens(t *testing.T) {
	ctx := WithConfinedProviderSecrets(context.Background())
	sap := &config.AppConfig{Providers: map[string]config.ProviderConfig{"scoped": {"type": "sap_ai_core"}}}
	for _, tt := range []struct {
		name, model    string
		cfg            *config.AppConfig
		explicit, want int32
	}{
		{"reasoning capacity not full reservation", "gpt-5.6-terra", sap, 0, 32768},
		{"unknown fallback", "unknown", nil, 0, 16384},
		{"explicit override", "gpt-5.6-terra", sap, 65536, 65536},
		{"explicit small", "gpt-5.6-terra", sap, 1024, 1024},
		{"known maximum", "gpt-5.6-terra", sap, 200000, 128000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveBudgetOutputTokens(ctx, "scoped", tt.model, tt.cfg, tt.explicit); got != tt.want {
				t.Fatalf("got %d want %d", got, tt.want)
			}
		})
	}
}

func TestBudgetOutputMetadataScopedAndBounded(t *testing.T) {
	for _, capacity := range []int{2048, 12000, 128000} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer scoped-key" {
					t.Error("missing scoped credential")
				}
				fmt.Fprintf(w, `{"data":[{"model_name":"same-name","model_info":{"max_output_tokens":%d,"max_input_tokens":272000}}]}`, capacity)
			}))
			defer srv.Close()
			cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"local": {"type": "openai_compat", "base_url": srv.URL, "api_key": "scoped-key"}}}
			got := ResolveBudgetOutputTokens(context.Background(), "local", "same-name", cfg, 0)
			if got != int32(min(capacity, 32768)) {
				t.Fatalf("got %d for capacity %d", got, capacity)
			}
			if cfg.Providers["local"]["api_key"] != "scoped-key" {
				t.Fatal("mutated source config")
			}
		})
	}
}
