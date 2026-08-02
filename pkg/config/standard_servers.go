package config

import (
	"fmt"
	"strings"
)

// StandardEnvVar describes an environment variable required by a standard MCP server.
type StandardEnvVar struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// StandardMCPServer describes a pre-configured MCP server that Astonish
// knows how to install with minimal user input (just an API key, or nothing for keyless servers).
// The server command, args, and env var names are hardcoded here.
// Credentials are stored in config.yaml under web_servers.
type StandardMCPServer struct {
	ID             string           `json:"id"`
	DisplayName    string           `json:"displayName"`
	Description    string           `json:"description"`
	Category       string           `json:"category"`       // "web" or "browser"
	Kind           string           `json:"kind,omitempty"` // "mcp" (default) or "model"
	Command        string           `json:"command"`
	Args           []string         `json:"args"`
	EnvVars        []StandardEnvVar `json:"envVars"`
	WebSearchTool  string           `json:"webSearchTool,omitempty"`  // "serverName:toolName"
	WebExtractTool string           `json:"webExtractTool,omitempty"` // "serverName:toolName" or empty
	IsDefault      bool             `json:"isDefault"`
	ExcludedTools  []string         `json:"excludedTools,omitempty"` // Tools to filter out (expensive/redundant)
}

// standardServers is the hardcoded registry of supported standard MCP servers.
var standardServers = []StandardMCPServer{
	{
		ID:            "perplexity",
		DisplayName:   "Perplexity / Sonar",
		Description:   "Model-backed web search using an existing provider model such as SAP AI Core Sonar Pro.",
		Category:      "web",
		Kind:          "model",
		EnvVars:       []StandardEnvVar{},
		WebSearchTool: "perplexity:perplexity_web_search",
		IsDefault:     false,
	},
	{
		ID:          "tavily",
		DisplayName: "Tavily",
		Description: "Web search and content extraction via Tavily API. Recommended for most users.",
		Category:    "web",
		Command:     "npx",
		Args:        []string{"-y", "tavily-mcp@latest"},
		EnvVars: []StandardEnvVar{
			{Name: "TAVILY_API_KEY", Required: true, Description: "Your Tavily API key (get one at tavily.com)"},
		},
		WebSearchTool:  "tavily:tavily_search",
		WebExtractTool: "tavily:tavily_extract",
		IsDefault:      true,
		ExcludedTools:  []string{"tavily_research", "tavily_crawl"},
	},
	{
		ID:          "brave-search",
		DisplayName: "Brave Search",
		Description: "Web search powered by Brave Search API. Search only, no content extraction.",
		Category:    "web",
		Command:     "npx",
		Args:        []string{"-y", "@brave/brave-search-mcp-server", "--transport", "stdio"},
		EnvVars: []StandardEnvVar{
			{Name: "BRAVE_API_KEY", Required: true, Description: "Your Brave Search API key (get one at brave.com/search/api)"},
		},
		WebSearchTool:  "brave-search:brave_web_search",
		WebExtractTool: "",
		IsDefault:      false,
	},
	{
		ID:          "firecrawl",
		DisplayName: "Firecrawl",
		Description: "Web search, scraping, and content extraction via Firecrawl API.",
		Category:    "web",
		Command:     "npx",
		Args:        []string{"-y", "firecrawl-mcp"},
		EnvVars: []StandardEnvVar{
			{Name: "FIRECRAWL_API_KEY", Required: true, Description: "Your Firecrawl API key (get one at firecrawl.dev)"},
		},
		WebSearchTool:  "firecrawl:firecrawl_search",
		WebExtractTool: "firecrawl:firecrawl_scrape",
		IsDefault:      false,
	},
}

// GetStandardServers returns all pre-configured standard MCP servers.
func GetStandardServers() []StandardMCPServer {
	result := make([]StandardMCPServer, len(standardServers))
	copy(result, standardServers)
	return result
}

// GetStandardServer returns a standard server by ID, or nil if not found.
func GetStandardServer(id string) *StandardMCPServer {
	for _, s := range standardServers {
		if s.ID == id {
			srv := s
			return &srv
		}
	}
	return nil
}

// GetStandardServerIDs returns the set of all standard server IDs for quick lookup.
func GetStandardServerIDs() map[string]bool {
	ids := make(map[string]bool, len(standardServers))
	for _, s := range standardServers {
		ids[s.ID] = true
	}
	return ids
}

// IsStandardServerInstalled checks if a standard server is available for use.
// Keyless servers are always considered installed — they need
// no API key and no explicit setup step.
// For servers requiring an API key, checks that the key is present in config.yaml
// or in the credential store (via the SecretGetter if set).
func IsStandardServerInstalled(id string) bool {
	// Keyless servers are always available — no config entry needed
	srv := GetStandardServer(id)
	if srv != nil && len(srv.EnvVars) == 0 {
		return true
	}

	if srv != nil && srv.Kind == "model" {
		return false
	}

	// Check credential store via registered getter (reads from platform_secrets DB
	// table with optional file-store fallback via daemonSecretGetter).
	if getter := getInstalledSecretGetter(); getter != nil {
		storeKey := "web_servers." + id + ".api_key"
		if getter(storeKey) != "" {
			return true
		}
	}

	return false
}

// --- Optional credential store getter for IsStandardServerInstalled ---
// This avoids an import cycle between config and credentials.

var installedSecretGetter SecretGetter

// SetInstalledSecretGetter registers a SecretGetter for IsStandardServerInstalled
// to use when checking the credential store. Called during startup.
func SetInstalledSecretGetter(g SecretGetter) {
	installedSecretGetter = g
}

func getInstalledSecretGetter() SecretGetter {
	return installedSecretGetter
}

// InstallStandardServer stores a standard server's configuration in config.yaml
// and configures it as the active web search/extract tool (if applicable).
// If storeKeyInConfig is false, the API key is NOT written to config.yaml
// (the caller is responsible for storing it in the credential store).
func InstallStandardServer(id string, envValues map[string]string, storeKeyInConfig bool) error {
	srv := GetStandardServer(id)
	if srv == nil {
		return &StandardServerError{ID: id, Message: "unknown standard server"}
	}

	// Extract the API key from the provided env values (if any are required)
	var apiKey string
	for _, ev := range srv.EnvVars {
		if val, ok := envValues[ev.Name]; ok && val != "" {
			apiKey = val
			break
		}
		if ev.Required {
			return &StandardServerError{ID: id, Message: "missing required environment variable: " + ev.Name}
		}
	}

	// Load and update app config
	appCfg, err := LoadAppConfig()
	if err != nil {
		appCfg = &AppConfig{
			Providers:  make(map[string]ProviderConfig),
			WebServers: make(map[string]WebServerConfig),
		}
	}
	if appCfg.WebServers == nil {
		appCfg.WebServers = make(map[string]WebServerConfig)
	}

	wsCfg := WebServerConfig{}
	if storeKeyInConfig {
		wsCfg.APIKey = apiKey
	}
	// For keyless servers, set the Installed flag
	if len(srv.EnvVars) == 0 {
		wsCfg.Installed = true
	}
	appCfg.WebServers[id] = wsCfg

	if srv.WebSearchTool != "" {
		appCfg.General.WebSearchTool = srv.WebSearchTool
	}
	if srv.WebExtractTool != "" {
		appCfg.General.WebExtractTool = srv.WebExtractTool
	}

	return SaveAppConfig(appCfg)
}

// UninstallStandardServer removes a standard server's configuration from config.yaml.
// It deletes the WebServers entry and clears General.WebSearchTool / WebExtractTool
// if they reference this server. The caller is responsible for removing the API key
// from the credential store.
func UninstallStandardServer(id string) error {
	srv := GetStandardServer(id)
	if srv == nil {
		return &StandardServerError{ID: id, Message: "unknown standard server"}
	}

	appCfg, err := LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Remove the web server entry
	delete(appCfg.WebServers, id)

	// Clear web tool references that belong to this server
	if strings.HasPrefix(appCfg.General.WebSearchTool, id+":") {
		appCfg.General.WebSearchTool = ""
	}
	if strings.HasPrefix(appCfg.General.WebExtractTool, id+":") {
		appCfg.General.WebExtractTool = ""
	}

	return SaveAppConfig(appCfg)
}

// BuildMCPServerConfig constructs an MCPServerConfig for a standard server
// using its hardcoded definition and the stored API key.
func BuildMCPServerConfig(srv *StandardMCPServer, apiKey string) MCPServerConfig {
	env := make(map[string]string)
	for _, ev := range srv.EnvVars {
		env[ev.Name] = apiKey
	}
	return MCPServerConfig{
		Command:   srv.Command,
		Args:      srv.Args,
		Env:       env,
		Transport: "stdio",
	}
}

// StandardServerError represents an error related to standard server operations.
type StandardServerError struct {
	ID      string
	Message string
}

func (e *StandardServerError) Error() string {
	return "standard server " + e.ID + ": " + e.Message
}

// GetExcludedTools returns the list of tool names that should be filtered out
// for a standard server. Returns nil for non-standard servers or servers with
// no exclusions. This ensures expensive/redundant tools like tavily_research
// are not exposed when using the standard server configuration.
// Users who want all tools can add the MCP server manually (non-standard).
func GetExcludedTools(serverName string) map[string]bool {
	srv := GetStandardServer(serverName)
	if srv == nil || len(srv.ExcludedTools) == 0 {
		return nil
	}
	excluded := make(map[string]bool, len(srv.ExcludedTools))
	for _, name := range srv.ExcludedTools {
		excluded[name] = true
	}
	return excluded
}
