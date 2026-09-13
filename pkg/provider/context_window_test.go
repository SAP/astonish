package provider

import (
	"context"
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
	// SAP provider uses hardcoded map, should resolve without API call
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
