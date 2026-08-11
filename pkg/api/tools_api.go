package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/config"
)

// WebCapableToolsResponse is the response for GET /api/tools/web-capable
type WebCapableToolsResponse struct {
	WebSearch  []WebCapableTool `json:"webSearch"`
	WebExtract []WebCapableTool `json:"webExtract"`
}

// WebCapableTool represents a tool that can be used for web search or extract
type WebCapableTool struct {
	Name   string `json:"name"`   // Tool name within the MCP server (e.g., "tavily-search")
	Source string `json:"source"` // MCP server name (e.g., "tavily")
}

// WebCapableToolsHandler returns web-capable tools available for selection.
// It combines two sources:
//  1. Installed standard servers from the registry (Tavily, Brave, Firecrawl)
//  2. The currently configured web tools from config.yaml (handles servers
//     installed under non-standard names)
//
// GET /api/tools/web-capable
func WebCapableToolsHandler(w http.ResponseWriter, r *http.Request) {
	response := WebCapableToolsResponse{
		WebSearch:  []WebCapableTool{},
		WebExtract: []WebCapableTool{},
	}

	// Track what we've already added to avoid duplicates
	seenSearch := map[string]bool{}
	seenExtract := map[string]bool{}

	// 1. Add tools from installed standard servers
	standardServers := config.GetStandardServers()
	for _, srv := range standardServers {
		if srv.Category != "web" {
			continue
		}
		if !config.IsStandardServerInstalled(srv.ID) {
			if srv.ID != "perplexity" {
				continue
			}
			appCfg := effectiveAppConfig(r)
			// Perplexity is available whenever provider+model are configured.
			if appCfg.PerplexityWebSearch.Provider == "" || appCfg.PerplexityWebSearch.Model == "" {
				continue
			}
		}

		if srv.WebSearchTool != "" {
			if source, name, ok := parseToolRef(srv.WebSearchTool); ok {
				key := source + ":" + name
				if !seenSearch[key] {
					response.WebSearch = append(response.WebSearch, WebCapableTool{Name: name, Source: source})
					seenSearch[key] = true
				}
			}
		}

		if srv.WebExtractTool != "" {
			if source, name, ok := parseToolRef(srv.WebExtractTool); ok {
				key := source + ":" + name
				if !seenExtract[key] {
					response.WebExtract = append(response.WebExtract, WebCapableTool{Name: name, Source: source})
					seenExtract[key] = true
				}
			}
		}
	}

	// 2. Also include whatever is currently configured in config.yaml.
	//    This handles servers installed under non-standard names (e.g. "tavily-websearch").
	appCfg := effectiveAppConfig(r)
	if appCfg.General.WebSearchTool != "" {
		if source, name, ok := parseToolRef(appCfg.General.WebSearchTool); ok {
			key := source + ":" + name
			if !seenSearch[key] {
				response.WebSearch = append(response.WebSearch, WebCapableTool{Name: name, Source: source})
				seenSearch[key] = true
			}
		}
	}
	if appCfg.General.WebExtractTool != "" {
		if source, name, ok := parseToolRef(appCfg.General.WebExtractTool); ok {
			key := source + ":" + name
			if !seenExtract[key] {
				response.WebExtract = append(response.WebExtract, WebCapableTool{Name: name, Source: source})
				seenExtract[key] = true
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// parseToolRef splits a "serverName:toolName" reference into its parts.
func parseToolRef(ref string) (source string, name string, ok bool) {
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}
