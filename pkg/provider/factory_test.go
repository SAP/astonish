package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/SAP/astonish/pkg/provider/httpool"
	xai_oauth "github.com/SAP/astonish/pkg/provider/xai_oauth"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

func TestResolveOAuthConfig(t *testing.T) {
	t.Setenv("XAI_OAUTH_ACCESS_TOKEN", "env-access")
	t.Setenv("XAI_OAUTH_REFRESH_TOKEN", "env-refresh")
	t.Setenv("XAI_OAUTH_CLIENT_ID", "env-client")
	t.Setenv("GITHUB_COPILOT_TOKEN", "env-github")
	resolved, err := resolveOAuthConfig(context.Background(), "xai_oauth", nil)
	if err != nil || resolved.accessToken != "env-access" || resolved.refreshToken != "env-refresh" || resolved.clientID != "env-client" {
		t.Fatalf("resolved=%+v error=%v", resolved, err)
	}
	resolved, err = resolveOAuthConfig(WithConfinedProviderSecrets(context.Background()), "xai_oauth", config.ProviderConfig{"access_token": "explicit", "expires_at": "malformed"})
	if err != nil || resolved.accessToken != "explicit" || resolved.refreshToken != "" || resolved.clientID != xai_oauth.DefaultClientID || !resolved.expiresAt.IsZero() {
		t.Fatalf("confined=%+v error=%v", resolved, err)
	}
	resolved, err = resolveOAuthConfig(context.Background(), "xai_oauth", config.ProviderConfig{"access_token": "a", "refresh_token": "r", "client_id": "c", "expires_at": "2099-01-01T00:00:00Z"})
	if err != nil || resolved.accessToken != "a" || resolved.refreshToken != "r" || resolved.clientID != "c" || resolved.expiresAt.IsZero() {
		t.Fatalf("explicit=%+v error=%v", resolved, err)
	}
	for _, kind := range []string{"xai_oauth", "copilot_oauth"} {
		ctx := WithConfinedProviderSecrets(context.Background())
		cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"tenant": {"type": kind}}}
		if _, err := GetProvider(ctx, "tenant", "", cfg); err == nil {
			t.Errorf("%s generation inherited env", kind)
		}
		if _, err := ListModelsForProvider(ctx, "tenant", cfg); err == nil {
			t.Errorf("%s listing inherited env", kind)
		}
		if _, err := TestProviderConnection(ctx, kind, nil); err == nil {
			t.Errorf("%s test inherited env", kind)
		}
	}
	resolved, err = resolveOAuthConfig(context.Background(), "copilot_oauth", nil)
	if err != nil || resolved.githubToken != "env-github" {
		t.Fatalf("copilot=%+v error=%v", resolved, err)
	}
}

// Route only the fixed OAuth hosts to a local TLS fixture. No credentials or
// requests can escape to production, including through environment proxies.
func TestFactoryOAuthDiscoveryRecoveryAndPersistence(t *testing.T) {
	t.Setenv("XAI_OAUTH_ACCESS_TOKEN", "")
	t.Setenv("XAI_OAUTH_REFRESH_TOKEN", "")
	t.Setenv("XAI_OAUTH_CLIENT_ID", "")
	var tokenCalls, modelCalls, callbacks int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "auth.x.ai" {
			tokenCalls++
			r.ParseForm()
			if r.FormValue("client_id") != xai_oauth.DefaultClientID || r.FormValue("refresh_token") != "refresh" {
				t.Errorf("form=%v", r.Form)
			}
			fmt.Fprintf(w, `{"access_token":"renewed-%d","refresh_token":"rotated","expires_in":3600}`, tokenCalls)
			return
		}
		if r.Host != "api.x.ai" {
			t.Errorf("unexpected host %q", r.Host)
			w.WriteHeader(500)
			return
		}
		modelCalls++
		if r.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"grok"}]}`)
	}))
	defer server.Close()
	transport := httpool.Transport().(*http.Transport)
	oldDial, oldTLS, oldProxy := transport.DialContext, transport.TLSClientConfig, transport.Proxy
	transport.CloseIdleConnections()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		if host != "api.x.ai" && host != "auth.x.ai" {
			return nil, fmt.Errorf("external connection forbidden: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	defer func() {
		transport.CloseIdleConnections()
		transport.DialContext = oldDial
		transport.TLSClientConfig = oldTLS
		transport.Proxy = oldProxy
	}()
	cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"subscription": {"type": "xai_oauth", "access_token": "old", "refresh_token": "refresh", "expires_at": "2099-01-01T00:00:00Z"}}, ProviderTokenRefresh: func(name, access, refresh string, expiry time.Time) error {
		callbacks++
		if name != "subscription" || !strings.HasPrefix(access, "renewed-") || refresh != "rotated" || !expiry.After(time.Now()) {
			t.Errorf("callback=%q %q %q %v", name, access, refresh, expiry)
		}
		return nil
	}}
	models, err := ListModelsForProvider(context.Background(), "SUBSCRIPTION", cfg)
	if err != nil || len(models) != 1 || callbacks != 1 || tokenCalls != 1 || modelCalls != 2 {
		t.Fatalf("models=%v err=%v callbacks=%d tokens=%d modelsCalls=%d", models, err, callbacks, tokenCalls, modelCalls)
	}
	// Connection test consumes supplied renewal credentials but must not persist.
	models, err = TestProviderConnection(context.Background(), "xai_oauth", map[string]string{"access_token": "old", "refresh_token": "refresh", "expires_at": "2099-01-01T00:00:00Z"})
	if err != nil || len(models) != 1 || callbacks != 1 || tokenCalls != 2 || modelCalls != 4 {
		t.Fatalf("connection models=%v err=%v callbacks=%d tokens=%d modelCalls=%d", models, err, callbacks, tokenCalls, modelCalls)
	}
	// Access-only setup remains compatible, with no renewal or callback.
	if _, err = TestProviderConnection(context.Background(), "xai_oauth", map[string]string{"access_token": "accepted"}); err != nil || callbacks != 1 || tokenCalls != 2 {
		t.Fatalf("access-only err=%v callbacks=%d tokens=%d", err, callbacks, tokenCalls)
	}
}

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

	// Bump when a provider is registered (most recently: copilot_oauth).
	expectedCount := 14
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
