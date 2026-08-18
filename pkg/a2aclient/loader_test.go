package a2aclient

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

func TestFileAgentToConfig(t *testing.T) {
	fa := config.A2AAgentFileConfig{
		URL:            "https://agent.example.com",
		CredentialName: "my-cred",
		AuthType:       "api_key",
		Timeout:        "45s",
	}

	cfg := fileAgentToConfig("test-agent", fa)

	if cfg.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got %q", cfg.Name)
	}
	if cfg.URL != "https://agent.example.com" {
		t.Errorf("expected URL 'https://agent.example.com', got %q", cfg.URL)
	}
	if cfg.CredentialName != "my-cred" {
		t.Errorf("expected credential 'my-cred', got %q", cfg.CredentialName)
	}
	if cfg.AuthType != "api_key" {
		t.Errorf("expected auth type 'api_key', got %q", cfg.AuthType)
	}
	if cfg.Timeout != 45*time.Second {
		t.Errorf("expected timeout 45s, got %v", cfg.Timeout)
	}
}

func TestFileAgentToConfig_DefaultTimeout(t *testing.T) {
	fa := config.A2AAgentFileConfig{
		URL: "https://agent.example.com",
	}

	cfg := fileAgentToConfig("test", fa)

	if cfg.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", cfg.Timeout)
	}
}

func TestStoreAgentToConfig(t *testing.T) {
	enabled := true
	a := store.A2AAgent{
		Name:           "store-agent",
		URL:            "https://remote.example.com",
		CredentialName: "remote-cred",
		AuthType:       "bearer",
		Enabled:        &enabled,
		Timeout:        "2m",
		Headers:        map[string]string{"X-Custom": "value"},
	}

	cfg := storeAgentToConfig(a)

	if cfg.Name != "store-agent" {
		t.Errorf("expected name 'store-agent', got %q", cfg.Name)
	}
	if cfg.URL != "https://remote.example.com" {
		t.Errorf("expected URL, got %q", cfg.URL)
	}
	if cfg.Timeout != 2*time.Minute {
		t.Errorf("expected timeout 2m, got %v", cfg.Timeout)
	}
	if cfg.Headers["X-Custom"] != "value" {
		t.Error("expected custom header")
	}
}

func TestLoadA2AAgentConfig_PersonalMode(t *testing.T) {
	// In personal mode with no config file, should return empty config
	cfg, err := LoadA2AAgentConfig(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// May have agents from the file or may be empty depending on environment
	// Just verify it doesn't crash
}

func TestLoadA2AAgentConfig_PlatformMode_NoStores(t *testing.T) {
	// Platform mode with no stores in context should still work (returns file-only)
	cfg, err := LoadA2AAgentConfig(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}
