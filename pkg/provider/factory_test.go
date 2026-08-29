package provider

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

func TestGetProviderDisplayName(t *testing.T) {
	tests := []struct {
		providerType string
		expected     string
	}{
		{"openai", "OpenAI"},
		{"anthropic", "Anthropic"},
		{"gemini", "Google GenAI"},
		{"groq", "Groq"},
		{"litellm", "LiteLLM"},
		{"lm_studio", "LM Studio"},
		{"ollama", "Ollama"},
		{"openrouter", "Openrouter"},
		{"poe", "Poe"},
		{"sap_ai_core", "SAP AI Core"},
		{"xai", "xAI"},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.providerType, func(t *testing.T) {
			result := GetProviderDisplayName(tt.providerType)
			if result != tt.expected {
				t.Errorf("GetProviderDisplayName(%q) = %q, expected %q",
					tt.providerType, result, tt.expected)
			}
		})
	}
}

func TestGetProviderIDs(t *testing.T) {
	ids := GetProviderIDs()

	if len(ids) == 0 {
		t.Error("GetProviderIDs returned empty slice")
	}

	expectedCount := 13
	if len(ids) != expectedCount {
		t.Errorf("GetProviderIDs returned %d IDs, expected %d", len(ids), expectedCount)
	}

	idSet := make(map[string]bool)
	for _, id := range ids {
		if idSet[id] {
			t.Errorf("duplicate provider ID: %s", id)
		}
		idSet[id] = true
	}

	expectedIDs := []string{"anthropic", "gemini", "groq", "litellm", "lm_studio", "ollama", "openai", "openai_compat", "openrouter", "poe", "sap_ai_core", "xai"}
	for _, expected := range expectedIDs {
		if !idSet[expected] {
			t.Errorf("expected provider ID %s not found", expected)
		}
	}
}

func TestGetProvider_InstanceLookup(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"openai-prod": {"type": "openai", "api_key": "sk-prod-key"},
			"openai-dev":  {"type": "openai", "api_key": "sk-dev-key"},
			"anthropic":   {"api_key": "sk-ant-..."},
			"litellm-dev": {"type": "litellm", "api_key": "sk-litellm"},
		},
	}

	tests := []struct {
		name         string
		instanceName string
		modelName    string
		expectError  bool
	}{
		{"new format with explicit type", "openai-prod", "gpt-4o", false},
		{"new format dev instance", "openai-dev", "gpt-4o", false},
		{"old format instance name", "anthropic", "claude-3-opus-20240229", false},
		{"litellm with type", "litellm-dev", "gpt-4", false},
		{"nonexistent instance", "nonexistent", "gpt-4", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GetProvider(context.Background(), tt.instanceName, tt.modelName, cfg)
			if tt.expectError && err == nil {
				t.Errorf("expected error for instance %q, got nil", tt.instanceName)
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error for instance %q: %v", tt.instanceName, err)
			}
		})
	}
}

func TestGetProvider_BackwardCompatibility(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"openai": {"api_key": "sk-old-key"},
			"gemini": {"api_key": "AIza-old-key"},
		},
	}

	tests := []struct {
		name         string
		instanceName string
		modelName    string
	}{
		{"openai old format", "openai", "gpt-4"},
		{"gemini old format", "gemini", "gemini-1.5-flash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := GetProvider(context.Background(), tt.instanceName, tt.modelName, cfg)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if provider == nil {
				t.Error("expected non-nil provider")
			}
		})
	}
}

func TestGetProvider_WiresXAIOAuthRefreshPersistence(t *testing.T) {
	var gotInstance, gotAccess, gotRefresh string
	var gotExpiry time.Time
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"grok-subscription": {
				"type":          "xai_oauth",
				"access_token":  "access",
				"refresh_token": "refresh",
				"expires_at":    "2099-01-01T00:00:00Z",
			},
		},
		ProviderTokenRefresh: func(instanceName, accessToken, refreshToken string, expiresAt time.Time) error {
			gotInstance, gotAccess, gotRefresh, gotExpiry = instanceName, accessToken, refreshToken, expiresAt
			return nil
		},
	}

	if _, err := GetProvider(context.Background(), "grok-subscription", "grok-3", cfg); err != nil {
		t.Fatal(err)
	}
	callback := xaiOAuthRefreshCallback(cfg, "grok-subscription")
	if callback == nil {
		t.Fatal("refresh callback is nil")
	}
	callback("new-access", "new-refresh", time.Unix(123, 0))
	if gotInstance != "grok-subscription" || gotAccess != "new-access" || gotRefresh != "new-refresh" || gotExpiry.Unix() != 123 {
		t.Fatalf("refresh callback got (%q, %q, %q, %v)", gotInstance, gotAccess, gotRefresh, gotExpiry)
	}
}

func TestGetProvider_MissingAPIKey(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"openai-test": {"type": "openai"},
		},
	}

	_, err := GetProvider(context.Background(), "openai-test", "gpt-4", cfg)
	if err == nil {
		t.Error("expected error for missing API key, got nil")
	}
}

func TestListModelsForProvider_InstanceLookup(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"openai-prod": {"type": "openai", "api_key": "sk-prod-key"},
			"openai-dev":  {"type": "openai", "api_key": "sk-dev-key"},
			"anthropic":   {"api_key": "sk-ant-..."},
		},
	}

	tests := []struct {
		name         string
		instanceName string
		expectError  bool
	}{
		{"new format instance", "openai-prod", true},
		{"old format instance", "anthropic", true},
		{"nonexistent instance", "nonexistent", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ListModelsForProvider(context.Background(), tt.instanceName, cfg)
			if !tt.expectError && err == nil {
				t.Error("expected models or error")
			}
			if tt.expectError && err == nil {
				t.Errorf("expected error for instance %q, got nil", tt.instanceName)
			}
		})
	}
}

func TestListModelsForProvider_MissingAPIKey(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"openai-test": {"type": "openai"},
		},
	}

	_, err := ListModelsForProvider(context.Background(), "openai-test", cfg)
	if err == nil {
		t.Error("expected error for missing API key, got nil")
	}
}

func TestProviderDisplayNames_AllHaveDisplayNames(t *testing.T) {
	ids := GetProviderIDs()
	for _, id := range ids {
		name := GetProviderDisplayName(id)
		if name == "" {
			t.Errorf("provider %s has empty display name", id)
		}
		if name == id {
			t.Errorf("provider %s display name should be different from ID", id)
		}
	}
}

func TestResolveProviderInstance(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"SAP AI Core":  {"type": "sap_ai_core", "client_id": "xxx"},
			"openai":       {"api_key": "sk-123"},
			"My Anthropic": {"type": "anthropic", "api_key": "sk-ant"},
		},
	}

	tests := []struct {
		name      string
		instance  string
		wantKey   string
		wantFound bool
	}{
		{"exact match", "openai", "openai", true},
		{"exact match display name key", "SAP AI Core", "SAP AI Core", true},
		{"ID resolves to display name key", "sap_ai_core", "SAP AI Core", true},
		{"case insensitive match", "sap ai core", "SAP AI Core", true},
		{"case insensitive match caps", "OPENAI", "openai", true},
		{"normalized custom name", "my_anthropic", "My Anthropic", true},
		{"nonexistent", "nonexistent", "", false},
		{"empty string", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, inst, found := resolveProviderInstance(tt.instance, cfg)
			if found != tt.wantFound {
				t.Errorf("resolveProviderInstance(%q) found=%v, want %v", tt.instance, found, tt.wantFound)
			}
			if found && key != tt.wantKey {
				t.Errorf("resolveProviderInstance(%q) key=%q, want %q", tt.instance, key, tt.wantKey)
			}
			if found && inst == nil {
				t.Errorf("resolveProviderInstance(%q) returned nil instance", tt.instance)
			}
			if !found && inst != nil {
				t.Errorf("resolveProviderInstance(%q) returned non-nil instance when not found", tt.instance)
			}
		})
	}
}

func TestGetProvider_DisplayNameKey(t *testing.T) {
	// Regression test: config uses display name as key (e.g. "SAP AI Core"),
	// but caller passes the normalized ID (e.g. "sap_ai_core").
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"SAP AI Core": {
				"type":          "sap_ai_core",
				"client_id":     "test-id",
				"client_secret": "test-secret",
				"auth_url":      "https://auth.example.com",
				"base_url":      "https://api.example.com",
			},
		},
	}

	// Should resolve "sap_ai_core" → "SAP AI Core" and reach the sap_ai_core case
	_, err := GetProvider(context.Background(), "sap_ai_core", "gpt-4", cfg)
	// We expect it to get past the "not found" error and into the provider-specific
	// logic. The SAP provider will likely fail on auth, but it should NOT fail with
	// "provider instance 'sap_ai_core' not found".
	if err != nil && err.Error() == "provider instance 'sap_ai_core' not found" {
		t.Errorf("resolveProviderInstance failed to find 'SAP AI Core' via ID 'sap_ai_core': %v", err)
	}
}

func TestGetProvider_MultipleInstances(t *testing.T) {
	cfg := &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"openai-prod": {
				"type":    "openai",
				"api_key": "prod-key-12345",
			},
			"openai-dev": {
				"type":    "openai",
				"api_key": "dev-key-67890",
			},
			"anthropic-prod": {
				"type":    "anthropic",
				"api_key": "prod-anthropic-key",
			},
		},
	}

	instances := []string{"openai-prod", "openai-dev", "anthropic-prod"}
	for _, instance := range instances {
		t.Run(instance, func(t *testing.T) {
			provider, err := GetProvider(context.Background(), instance, "test-model", cfg)
			if err != nil {
				t.Fatalf("failed to get provider %s: %v", instance, err)
			}
			if provider == nil {
				t.Errorf("provider %s is nil", instance)
			}
		})
	}
}
