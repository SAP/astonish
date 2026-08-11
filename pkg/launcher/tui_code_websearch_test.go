package launcher

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/tui/backend"
)

// writeAppConfigFile writes a config.yaml to a temp config dir for testing.
func writeAppConfigFile(t *testing.T, yamlBody string) {
	t.Helper()
	tmpDir := t.TempDir()
	var configDir string
	if runtime.GOOS == "darwin" {
		configDir = filepath.Join(tmpDir, "Library", "Application Support", "astonish")
		t.Setenv("HOME", tmpDir)
	} else {
		configDir = filepath.Join(tmpDir, "astonish")
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

func TestWebSearchAdmin_ListProviders(t *testing.T) {
	appCfg := &config.AppConfig{
		General: config.GeneralConfig{
			WebSearchTool: "tavily:tavily_search",
		},
	}

	// Register a getter that says Tavily is installed.
	prevGetter := config.GetInstalledSecretGetterForTest()
	config.SetInstalledSecretGetter(func(key string) string {
		if key == "web_servers.tavily.api_key" {
			return "test-key"
		}
		return ""
	})
	defer config.SetInstalledSecretGetter(prevGetter)

	b := &localAgentBackend{
		appConfig: appCfg,
		result:    &ChatFactoryResult{},
	}

	providers, err := b.ListWebSearchProviders(context.Background())
	if err != nil {
		t.Fatalf("ListWebSearchProviders failed: %v", err)
	}

	if len(providers) == 0 {
		t.Fatal("expected at least one provider")
	}

	// Find Tavily and check it's marked installed + active.
	var tavily *backend.WebSearchProvider
	for i := range providers {
		if providers[i].ID == "tavily" {
			tavily = &providers[i]
			break
		}
	}
	if tavily == nil {
		t.Fatal("expected Tavily in providers list")
	}
	if !tavily.Installed {
		t.Error("expected Tavily to be marked installed")
	}
	if !tavily.Active {
		t.Error("expected Tavily to be marked active")
	}
	if tavily.Kind != "" && tavily.Kind != "mcp" {
		t.Errorf("expected Tavily kind to be empty or 'mcp', got %q", tavily.Kind)
	}

	// Check Perplexity is present but not installed.
	var perp *backend.WebSearchProvider
	for i := range providers {
		if providers[i].ID == "perplexity" {
			perp = &providers[i]
			break
		}
	}
	if perp == nil {
		t.Fatal("expected Perplexity in providers list")
	}
	if perp.Installed {
		t.Error("expected Perplexity to be not installed (no provider/model configured)")
	}
	if perp.Kind != "model" {
		t.Errorf("expected Perplexity kind = 'model', got %q", perp.Kind)
	}
}

func TestWebSearchAdmin_ClearWebSearch(t *testing.T) {
	writeAppConfigFile(t, `
general:
  web_search_tool: "tavily:tavily_search"
  web_extract_tool: "tavily:tavily_extract"
perplexity_web_search:
  provider: my_provider
  model: sonar-pro
`)

	appCfg, err := config.LoadAppConfig()
	if err != nil {
		t.Fatalf("failed to load app config: %v", err)
	}

	b := &localAgentBackend{
		appConfig: appCfg,
		result:    &ChatFactoryResult{},
	}

	if err := b.ClearWebSearch(context.Background()); err != nil {
		t.Fatalf("ClearWebSearch failed: %v", err)
	}

	if b.appConfig.General.WebSearchTool != "" {
		t.Errorf("expected WebSearchTool cleared, got %q", b.appConfig.General.WebSearchTool)
	}
	if b.appConfig.General.WebExtractTool != "" {
		t.Errorf("expected WebExtractTool cleared, got %q", b.appConfig.General.WebExtractTool)
	}
	if b.appConfig.PerplexityWebSearch.Provider != "" {
		t.Errorf("expected PerplexityWebSearch cleared, got %+v", b.appConfig.PerplexityWebSearch)
	}

	// Verify persisted to disk.
	reloaded, err := config.LoadAppConfig()
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.General.WebSearchTool != "" {
		t.Errorf("expected persisted WebSearchTool cleared, got %q", reloaded.General.WebSearchTool)
	}
}

func TestWebSearchAdmin_ConfigurePerplexity(t *testing.T) {
	writeAppConfigFile(t, `
general:
  default_provider: test
`)

	appCfg, err := config.LoadAppConfig()
	if err != nil {
		t.Fatalf("failed to load app config: %v", err)
	}

	b := &localAgentBackend{
		appConfig: appCfg,
		result:    &ChatFactoryResult{},
	}

	err = b.ConfigurePerplexityWebSearch(context.Background(), "my_provider", "sonar-pro")
	if err != nil {
		t.Fatalf("ConfigurePerplexityWebSearch failed: %v", err)
	}

	if b.appConfig.General.WebSearchTool != "perplexity:perplexity_web_search" {
		t.Errorf("expected WebSearchTool = 'perplexity:perplexity_web_search', got %q", b.appConfig.General.WebSearchTool)
	}
	if b.appConfig.PerplexityWebSearch.Provider != "my_provider" {
		t.Errorf("expected PerplexityWebSearch.Provider = 'my_provider', got %q", b.appConfig.PerplexityWebSearch.Provider)
	}
	if b.appConfig.PerplexityWebSearch.Model != "sonar-pro" {
		t.Errorf("expected PerplexityWebSearch.Model = 'sonar-pro', got %q", b.appConfig.PerplexityWebSearch.Model)
	}

	// Verify persisted.
	reloaded, err := config.LoadAppConfig()
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.General.WebSearchTool != "perplexity:perplexity_web_search" {
		t.Errorf("expected persisted WebSearchTool, got %q", reloaded.General.WebSearchTool)
	}
}

func TestWebSearchAdmin_ConfigurePerplexity_RejectsNonSonarModel(t *testing.T) {
	appCfg := &config.AppConfig{}
	b := &localAgentBackend{
		appConfig: appCfg,
		result:    &ChatFactoryResult{},
	}

	err := b.ConfigurePerplexityWebSearch(context.Background(), "provider", "gpt-4o")
	if err == nil {
		t.Fatal("expected error for non-perplexity model")
	}
}
