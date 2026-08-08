package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

// TestSetLocalProviders_SnapshotIsolated verifies SetLocalProviders takes a deep
// copy so later mutations of the caller's map do not leak into the snapshot.
func TestSetLocalProviders_SnapshotIsolated(t *testing.T) {
	t.Cleanup(func() { SetLocalProviders(nil) })

	src := map[string]config.ProviderConfig{
		"cfg-openai": {"type": "openai", "api_key": "sk-local"},
	}
	SetLocalProviders(src)

	// Mutate the caller's map afterwards.
	src["cfg-openai"]["api_key"] = "mutated"
	src["added-later"] = config.ProviderConfig{"type": "anthropic"}

	got := getLocalProviders()
	if len(got) != 1 {
		t.Fatalf("expected 1 provider in snapshot, got %d: %+v", len(got), got)
	}
	if got["cfg-openai"]["api_key"] != "sk-local" {
		t.Errorf("snapshot leaked caller mutation: api_key = %q", got["cfg-openai"]["api_key"])
	}
	if _, ok := got["added-later"]; ok {
		t.Error("snapshot leaked a key added to the caller map after SetLocalProviders")
	}
}

// TestSetLocalProviders_ClearsOnEmpty verifies passing nil/empty clears the snapshot.
func TestSetLocalProviders_ClearsOnEmpty(t *testing.T) {
	t.Cleanup(func() { SetLocalProviders(nil) })

	SetLocalProviders(map[string]config.ProviderConfig{"x": {"type": "openai"}})
	if getLocalProviders() == nil {
		t.Fatal("expected non-nil snapshot after set")
	}
	SetLocalProviders(nil)
	if getLocalProviders() != nil {
		t.Error("expected nil snapshot after clearing")
	}
	SetLocalProviders(map[string]config.ProviderConfig{})
	if getLocalProviders() != nil {
		t.Error("expected nil snapshot after clearing with empty map")
	}
}

// TestGetEffectiveProvidersHandler_MasksSecrets is a security guard: the
// effective-providers response must NEVER return a raw provider secret. It
// asserts that a config.yaml api_key is masked (only the last 4 chars survive)
// and that the full secret string never appears anywhere in the JSON body.
func TestGetEffectiveProvidersHandler_MasksSecrets(t *testing.T) {
	t.Cleanup(func() { SetLocalProviders(nil) })

	// Isolate config home so LoadAppConfig reads our fixture, not the user's.
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	appDir := filepath.Join(cfgHome, "astonish")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	const secret = "sk-supersecretvalue-1234"
	cfgYAML := "providers:\n" +
		"  cfg-openai:\n" +
		"    type: openai\n" +
		"    api_key: " + secret + "\n"
	if err := os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Publish the config.yaml providers as the daemon bootstrap would.
	SetLocalProviders(map[string]config.ProviderConfig{
		"cfg-openai": {"type": "openai", "api_key": secret},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/settings/providers/effective", nil)
	rec := httptest.NewRecorder()
	GetEffectiveProvidersHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, secret) {
		t.Fatalf("SECURITY: raw secret leaked in effective-providers response:\n%s", body)
	}

	var resp struct {
		Providers map[string]map[string]string `json:"providers"`
		Sources   map[string]struct {
			Source   string `json:"source"`
			ReadOnly bool   `json:"read_only"`
		} `json:"provider_sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	p, ok := resp.Providers["cfg-openai"]
	if !ok {
		t.Fatalf("config.yaml provider missing from effective view: %+v", resp.Providers)
	}
	if p["api_key"] == secret {
		t.Fatal("SECURITY: api_key returned unmasked")
	}
	if !strings.HasPrefix(p["api_key"], "****") {
		t.Errorf("api_key not masked: %q", p["api_key"])
	}
	if src := resp.Sources["cfg-openai"]; src.Source != "local" || !src.ReadOnly {
		t.Errorf("config.yaml provider source = %+v, want {local, read_only}", src)
	}
}
// keyed by instance name, so the same config loaded by multiple pods yields a
// single entry per name (the map naturally dedupes).
func TestGetLocalProviders_DedupeByName(t *testing.T) {
	t.Cleanup(func() { SetLocalProviders(nil) })

	// Two pods would each call SetLocalProviders with identical config; the last
	// write wins and there is exactly one entry per instance name.
	SetLocalProviders(map[string]config.ProviderConfig{
		"shared": {"type": "openai", "api_key": "sk-a"},
	})
	SetLocalProviders(map[string]config.ProviderConfig{
		"shared": {"type": "openai", "api_key": "sk-a"},
	})
	got := getLocalProviders()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 deduped entry, got %d", len(got))
	}
}
