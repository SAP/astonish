package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/tui/backend"
)

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
