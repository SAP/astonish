package launcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	copilot_oauth "github.com/SAP/astonish/pkg/provider/copilot_oauth"
	"github.com/SAP/astonish/pkg/tui/backend"
)

func TestCodeCompatibleMetadataContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/litellm/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-astra"}]}`)
			return
		}
		if r.URL.Path != "/litellm/v1/model/info" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"model_name":"gpt-6-astra","model_info":{"max_tokens":128000,"max_input_tokens":872000,"max_output_tokens":128000}}]}`)
	}))
	defer server.Close()
	b := &localAgentBackend{configured: true, provider: "hyperspace", model: "gpt-6-astra", appConfig: &config.AppConfig{Providers: map[string]config.ProviderConfig{"hyperspace": {"type": "openai_compat", "base_url": server.URL + "/litellm/v1", "api_key": "fixture"}}}}
	models, err := b.ListModels(context.Background(), "hyperspace")
	if err != nil || len(models) != 1 {
		t.Fatalf("discovery: %v %v", models, err)
	}
	for _, metadata := range []backend.ModelContextBackend{b, &lazyCodeBackend{inner: b}} {
		if got := metadata.ModelContextWindows(context.Background(), "hyperspace", models)["gpt-6-astra"]; got.Size != 872000 || got.IsFallback {
			t.Fatalf("picker: %+v", got)
		}
	}
	if info := b.Info(); info.ContextWindow != 872000 || info.ContextWindowFallback {
		t.Fatalf("header: %+v", info)
	}
}

func TestCodeSAPCatalogContext(t *testing.T) {
	fixture := func(t *testing.T, capacity int) (*config.AppConfig, *atomic.Int32) {
		t.Helper()
		calls := &atomic.Int32{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/oauth/token":
				fmt.Fprint(w, `{"access_token":"code-fixture-token","expires_in":3600,"token_type":"Bearer"}`)
			case "/v2/lm/scenarios/foundation-models/models":
				calls.Add(1)
				if r.Header.Get("AI-Resource-Group") != "code-fixture" || r.Header.Get("Authorization") != "Bearer code-fixture-token" {
					t.Errorf("unexpected catalog headers: %v", r.Header)
				}
				fmt.Fprintf(w, `{"resources":[{"model":"gpt-6-astra","versions":[{"isLatest":true,"contextLength":%d}]}]}`, capacity)
			default:
				t.Errorf("unexpected SAP request: %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		return &config.AppConfig{Providers: map[string]config.ProviderConfig{
			"code-sap": {
				"type": "sap_ai_core", "client_id": "code-fixture", "client_secret": "fixture-secret",
				"auth_url": server.URL, "base_url": server.URL, "resource_group": "code-fixture",
			},
		}}, calls
	}
	assertInfo := func(t *testing.T, b *localAgentBackend, window int, fallback bool) {
		t.Helper()
		// Repeated header refreshes must preserve both capacity and provenance.
		for i := 0; i < 2; i++ {
			info := b.Info()
			for _, metadata := range []backend.ModelContextBackend{b, &lazyCodeBackend{inner: b}} {
				windows := metadata.ModelContextWindows(context.Background(), b.provider, []string{b.model})
				if got := windows[b.model]; got.Size != window || got.IsFallback != fallback {
					t.Fatalf("picker metadata = %+v, want %d fallback=%t", got, window, fallback)
				}
			}
			if info.ContextWindow != window || info.ContextWindowFallback != fallback {
				t.Fatalf("Info refresh %d: context = %d, fallback = %t; want %d, %t", i, info.ContextWindow, info.ContextWindowFallback, window, fallback)
			}
			if info.Mode != "code" || info.Provider != b.provider || info.Model != b.model {
				t.Fatalf("unexpected code backend identity: %+v", info)
			}
		}
	}

	for _, tc := range []struct {
		name     string
		model    string
		override int
		window   int
		fallback bool
	}{
		{name: "catalog", model: "gpt-6-astra", window: 1_100_000},
		{name: "unknown fallback", model: "unknown-code-sap-model", window: 200_000, fallback: true},
		{name: "known static fallback", model: "anthropic--claude-3.5-sonnet", window: 200_000},
		{name: "explicit override", model: "gpt-6-astra", override: 42_000, window: 42_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, calls := fixture(t, 1_100_000)
			cfg.General.ContextLength = tc.override
			b := &localAgentBackend{appConfig: cfg, provider: "code-sap", model: tc.model, configured: true}
			assertInfo(t, b, tc.window, tc.fallback)
			wantCalls := int32(1)
			if tc.override > 0 {
				wantCalls = 0
			}
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("catalog requests = %d, want %d", got, wantCalls)
			}
		})
	}

	t.Run("changed connection", func(t *testing.T) {
		cfg, firstCalls := fixture(t, 1_100_000)
		b := &localAgentBackend{appConfig: cfg, provider: "code-sap", model: "gpt-6-astra", configured: true}
		assertInfo(t, b, 1_100_000, false)
		other, secondCalls := fixture(t, 700_000)
		// Keep the provider alias and model unchanged: only the SAP connection
		// changes, so a provider-name-only cache would return the stale 1.1M.
		cfg.Providers["code-sap"] = other.Providers["code-sap"]
		assertInfo(t, b, 700_000, false)
		cfg.General.ContextLength = 42_000
		assertInfo(t, b, 42_000, false)
		cfg.General.ContextLength = 84_000
		assertInfo(t, b, 84_000, false)
		cfg.General.ContextLength = 0
		assertInfo(t, b, 700_000, false)
		if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
			t.Fatalf("connection catalog requests = %d, %d; want 1, 1", firstCalls.Load(), secondCalls.Load())
		}
	})
}

// codeProviderCatalogSupported is the set of provider types that the /provider
// overlay in code mode must offer. It mirrors provider.ProviderDisplayNames —
// every provider Astonish supports and that can be configured from a plaintext
// config file must be addable via /provider.
func codeProviderCatalogSupported() map[string]bool {
	supported := make(map[string]bool, len(provider.ProviderDisplayNames))
	for id := range provider.ProviderDisplayNames {
		supported[id] = true
	}
	return supported
}

// TestCodeProviderTypes_CoversAllSupportedProviders guards against catalog drift:
// if a new provider is added to provider.ProviderDisplayNames it must also be
// offered by the /provider overlay (or explicitly excluded here with a reason).
func TestCodeProviderTypes_CoversAllSupportedProviders(t *testing.T) {
	catalog := codeProviderTypes()

	offered := make(map[string]bool, len(catalog))
	for _, ti := range catalog {
		offered[ti.ID] = true
	}

	for id := range codeProviderCatalogSupported() {
		if !offered[id] {
			t.Errorf("provider %q is in ProviderDisplayNames but not offered by /provider (codeProviderTypes)", id)
		}
	}

	// Every offered type must have a known display name (no typo IDs).
	for _, ti := range catalog {
		if provider.GetProviderDisplayName(ti.ID) == ti.ID && ti.ID != "openai_compat" {
			// GetProviderDisplayName returns the ID unchanged for unknown IDs.
			if _, ok := provider.ProviderDisplayNames[ti.ID]; !ok {
				t.Errorf("offered provider %q has no display name in ProviderDisplayNames", ti.ID)
			}
		}
		if len(ti.Fields) == 0 {
			t.Errorf("offered provider %q declares no input fields", ti.ID)
		}
	}
}

// TestCodeProviderTypes_SAPAICoreFields verifies the SAP AI Core entry declares
// the OAuth fields it needs, matching cmd/astonish/setup.go's runSAPAICoreForm.
func TestCodeProviderTypes_SAPAICoreFields(t *testing.T) {
	ti := findProviderType(t, "sap_ai_core")

	fields := fieldMap(ti)
	for _, key := range []string{"client_id", "client_secret", "auth_url", "base_url", "resource_group"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("sap_ai_core missing field %q", key)
		}
	}
	if !fields["client_secret"].Secret {
		t.Error("sap_ai_core client_secret must be marked Secret")
	}
	if !fields["resource_group"].Optional {
		t.Error("sap_ai_core resource_group should be Optional")
	}
	if fields["resource_group"].Default != "default" {
		t.Errorf("sap_ai_core resource_group default = %q, want %q", fields["resource_group"].Default, "default")
	}
	// The two obviously-required credentials must not be optional.
	if fields["client_id"].Optional || fields["client_secret"].Optional {
		t.Error("sap_ai_core client_id and client_secret must be required")
	}
}

// TestCodeProviderTypes_LiteLLMFields verifies the LiteLLM entry declares a
// base_url and a secret api_key.
func TestCodeProviderTypes_LiteLLMFields(t *testing.T) {
	ti := findProviderType(t, "litellm")

	fields := fieldMap(ti)
	if _, ok := fields["base_url"]; !ok {
		t.Error("litellm missing base_url field")
	}
	if _, ok := fields["api_key"]; !ok {
		t.Error("litellm missing api_key field")
	}
	if !fields["api_key"].Secret {
		t.Error("litellm api_key must be marked Secret")
	}
}

// TestCodeProviderTypes_XAIOAuthFields verifies the xAI OAuth entry declares
// the client_id field needed for device-code flow setup.
func TestCodeProviderTypes_XAIOAuthFields(t *testing.T) {
	ti := findProviderType(t, "xai_oauth")
	fields := fieldMap(ti)
	if _, ok := fields["client_id"]; !ok {
		t.Error("xai_oauth missing client_id field")
	}
}

// TestAddProvider_SAPAICore round-trips adding a SAP AI Core provider through the
// ProviderAdminBackend and confirms it is persisted with all fields.
func TestAddProvider_SAPAICore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{}}

	fields := map[string]string{
		"client_id":     "cid",
		"client_secret": "csecret",
		"auth_url":      "https://auth.example.com",
		"base_url":      "https://api.example.com",
		// resource_group intentionally omitted (optional).
	}
	if err := b.AddProvider(context.Background(), "SAP AI Core", "sap_ai_core", fields); err != nil {
		t.Fatalf("AddProvider(sap_ai_core) failed: %v", err)
	}

	inst := b.appConfig.Providers["SAP AI Core"]
	if inst == nil {
		t.Fatal("provider instance not stored in config")
	}
	if inst["type"] != "sap_ai_core" {
		t.Errorf("type = %q, want sap_ai_core", inst["type"])
	}
	for k, want := range fields {
		if inst[k] != want {
			t.Errorf("field %q = %q, want %q", k, inst[k], want)
		}
	}

	// Persisted to disk and reloadable.
	if err := config.SaveAppConfig(b.appConfig); err != nil {
		t.Fatalf("SaveAppConfig failed: %v", err)
	}
	reloaded, err := config.LoadAppConfig()
	if err != nil {
		t.Fatalf("LoadAppConfig failed: %v", err)
	}
	if reloaded.Providers["SAP AI Core"]["client_id"] != "cid" {
		t.Error("reloaded config missing sap_ai_core client_id")
	}
}

// TestAddProvider_SAPAICore_MissingRequiredField ensures required fields are
// enforced by AddProvider.
func TestAddProvider_SAPAICore_MissingRequiredField(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{}}

	// Missing client_secret (required).
	fields := map[string]string{
		"client_id": "cid",
		"auth_url":  "https://auth.example.com",
		"base_url":  "https://api.example.com",
	}
	if err := b.AddProvider(context.Background(), "SAP AI Core", "sap_ai_core", fields); err == nil {
		t.Fatal("expected error for missing required client_secret, got nil")
	}
	if _, ok := b.appConfig.Providers["SAP AI Core"]; ok {
		t.Error("provider should not be stored when a required field is missing")
	}
}

// TestAddProvider_LiteLLM round-trips adding a LiteLLM provider.
func TestAddProvider_LiteLLM(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{}}

	fields := map[string]string{
		"base_url": "http://localhost:4000/v1",
		"api_key":  "sk-litellm",
	}
	if err := b.AddProvider(context.Background(), "LiteLLM", "litellm", fields); err != nil {
		t.Fatalf("AddProvider(litellm) failed: %v", err)
	}
	inst := b.appConfig.Providers["LiteLLM"]
	if inst == nil {
		t.Fatal("litellm instance not stored")
	}
	if inst["base_url"] != "http://localhost:4000/v1" || inst["api_key"] != "sk-litellm" {
		t.Errorf("litellm fields not persisted: %+v", inst)
	}
}

// TestListProviderInstances_LocalOnly verifies that ListProviderInstances
// returns only config.yaml providers (hidden/empty keys filtered). Code mode
// never surfaces platform providers.
func TestListProviderInstances_LocalOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{
		Providers: map[string]config.ProviderConfig{
			"my-openai": {"type": "openai", "api_key": "sk-x"},
			"__hidden":  {"type": "openai"},
			"":          {"type": "openai"},
		},
	}}

	got, err := b.ListProviderInstances(context.Background())
	if err != nil {
		t.Fatalf("ListProviderInstances failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 visible instance (hidden/empty filtered), got %d: %+v", len(got), got)
	}
	inst := got[0]
	if inst.Name != "my-openai" {
		t.Errorf("name = %q, want my-openai", inst.Name)
	}
	if inst.Type != "openai" {
		t.Errorf("type = %q, want openai", inst.Type)
	}
}

// TestRemoveProvider_NotConfigured confirms removing an unknown provider
// returns the plain not-configured error.
func TestRemoveProvider_NotConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{
		Providers: map[string]config.ProviderConfig{},
	}}
	err := b.RemoveProvider(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected error removing unknown provider")
	}
	if got := err.Error(); got != `provider "ghost" is not configured` {
		t.Errorf("unexpected error: %q", got)
	}
}

func TestAddProvider_CopiesExtraOAuthTokenFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{}}
	fields := map[string]string{
		"client_id":     "cid",
		"access_token":  "at",
		"refresh_token": "rt",
		"expires_at":    "2026-01-01T00:00:00Z",
	}
	if err := b.AddProvider(context.Background(), "xai_oauth", "xai_oauth", fields); err != nil {
		t.Fatalf("AddProvider(xai_oauth) failed: %v", err)
	}
	inst := b.appConfig.Providers["xai_oauth"]
	if inst == nil {
		t.Fatal("provider instance not stored in config")
	}
	for k, want := range fields {
		if inst[k] != want {
			t.Errorf("field %q = %q, want %q", k, inst[k], want)
		}
	}
}

func TestAddProvider_CopilotOAuth(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	b := &localAgentBackend{appConfig: &config.AppConfig{}}
	fields := map[string]string{
		"client_id":    copilot_oauth.DefaultClientID,
		"github_token": "ghu_test_token_abc",
	}
	if err := b.AddProvider(context.Background(), "copilot_oauth", "copilot_oauth", fields); err != nil {
		t.Fatalf("AddProvider(copilot_oauth) failed: %v", err)
	}
	inst := b.appConfig.Providers["copilot_oauth"]
	if inst == nil {
		t.Fatal("copilot_oauth instance not stored in config")
	}
	if inst["type"] != "copilot_oauth" {
		t.Errorf("type = %q, want copilot_oauth", inst["type"])
	}
	// github_token should be persisted (or in credential store if available)
	if inst["github_token"] != "ghu_test_token_abc" {
		t.Errorf("github_token = %q, want ghu_test_token_abc", inst["github_token"])
	}
	if inst["client_id"] != copilot_oauth.DefaultClientID {
		t.Errorf("client_id = %q, want %s", inst["client_id"], copilot_oauth.DefaultClientID)
	}
}

func findProviderType(t *testing.T, id string) backend.ProviderTypeInfo {
	t.Helper()
	for _, ti := range codeProviderTypes() {
		if ti.ID == id {
			return ti
		}
	}
	t.Fatalf("provider type %q not found in codeProviderTypes()", id)
	return backend.ProviderTypeInfo{}
}

func fieldMap(ti backend.ProviderTypeInfo) map[string]backend.ProviderField {
	m := make(map[string]backend.ProviderField, len(ti.Fields))
	for _, f := range ti.Fields {
		m[f.Key] = f
	}
	return m
}
