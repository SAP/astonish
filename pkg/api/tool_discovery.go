package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/mcpstore"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// ToolSearchRequest is the request for POST /api/ai/tool-search
type ToolSearchRequest struct {
	Requirement string `json:"requirement"` // What the user needs (e.g., "take screenshots of websites")
}

// ToolSearchResult represents a matching tool from the store
type ToolSearchResult struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Source         string            `json:"source"`
	Tags           []string          `json:"tags"`
	Installable    bool              `json:"installable"`
	Reason         string            `json:"reason,omitempty"`  // Why this tool matches the requirement
	RequiresApiKey bool              `json:"requiresApiKey"`    // Whether tool requires API key
	EnvVars        map[string]string `json:"envVars,omitempty"` // Required env vars (key -> placeholder value)
}

// ToolSearchResponse is the response for POST /api/ai/tool-search
type ToolSearchResponse struct {
	Results []ToolSearchResult `json:"results"`
	Total   int                `json:"total"`
}

// minimalToolContext implements tool.Context for calling MCP tools
type minimalToolContext struct {
	context.Context
}

func (m *minimalToolContext) Actions() *session.EventActions       { return &session.EventActions{} }
func (m *minimalToolContext) Branch() string                       { return "" }
func (m *minimalToolContext) AgentName() string                    { return "tool-discovery" }
func (m *minimalToolContext) AppName() string                      { return "astonish" }
func (m *minimalToolContext) Artifacts() agent.Artifacts           { return nil }
func (m *minimalToolContext) FunctionCallID() string               { return "" }
func (m *minimalToolContext) InvocationID() string                 { return "" }
func (m *minimalToolContext) SessionID() string                    { return "" }
func (m *minimalToolContext) UserID() string                       { return "" }
func (m *minimalToolContext) UserContent() *genai.Content          { return nil }
func (m *minimalToolContext) ReadonlyState() session.ReadonlyState { return nil }
func (m *minimalToolContext) State() session.State                 { return nil }
func (m *minimalToolContext) SearchMemory(ctx context.Context, query string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (m *minimalToolContext) RequestConfirmation(hint string, payload any) error   { return nil }
func (m *minimalToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

// normalizeToolName normalizes a tool name for fuzzy matching
// Removes spaces, hyphens, underscores. Converts to lowercase.
func normalizeToolName(name string) string {
	normalized := strings.ToLower(name)
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	return normalized
}

// AIToolSearchHandler handles POST /api/ai/tool-search
// Uses AI to semantically evaluate which store tools can fulfill the requirement
func AIToolSearchHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req ToolSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// If no requirement, return empty
	if req.Requirement == "" {
		json.NewEncoder(w).Encode(ToolSearchResponse{Results: []ToolSearchResult{}, Total: 0})
		return
	}

	// Load all servers from taps (only installable ones)
	servers, err := loadAllServersFromTaps()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to load servers: "+err.Error())
		return
	}

	// Filter to only installable servers and build a summary
	var toolSummaries []string
	var installableServers []mcpstore.Server
	for _, srv := range servers {
		if srv.Config != nil {
			installableServers = append(installableServers, srv)
			tags := ""
			if len(srv.Tags) > 0 {
				tags = " [tags: " + strings.Join(srv.Tags, ", ") + "]"
			}
			toolSummaries = append(toolSummaries, fmt.Sprintf("- %s: %s%s", srv.Name, srv.Description, tags))
		}
	}

	// If no tools available, return empty
	if len(installableServers) == 0 {
		json.NewEncoder(w).Encode(ToolSearchResponse{Results: []ToolSearchResult{}, Total: 0})
		return
	}

	// Use AI to find matching tools
	matchingTools := findToolsWithAI(ctx, req.Requirement, toolSummaries, installableServers, effectiveAppConfig(r))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ToolSearchResponse{
		Results: matchingTools,
		Total:   len(matchingTools),
	})
}

// findToolsWithAI uses the LLM to semantically match tools to requirements
func findToolsWithAI(ctx context.Context, requirement string, toolSummaries []string, servers []mcpstore.Server, appCfg *config.AppConfig) []ToolSearchResult {
	if appCfg == nil {
		return nil
	}
	injectProviderSecrets(appCfg)

	providerName := appCfg.General.DefaultProvider
	modelName := appCfg.General.DefaultModel
	if providerName == "" {
		providerName = "gemini"
	}
	if modelName == "" {
		modelName = "gemini-2.0-flash"
	}

	llm, err := provider.GetProvider(ctx, providerName, modelName, appCfg)
	if err != nil {
		return nil
	}

	// Build prompt for tool matching
	toolList := strings.Join(toolSummaries, "\n")
	prompt := fmt.Sprintf(`You are searching for MCP tools that match a user's request.

USER REQUEST: %s

AVAILABLE TOOLS:
%s

TASK: Find ALL tools that could match this request. Be FLEXIBLE and GENEROUS.

MATCHING RULES:
1. PARTIAL NAME: Match if the request contains part of a tool's name
2. SYNONYMS: Match related concepts (e.g., "code" ↔ "programming", "files" ↔ "filesystem")
3. PURPOSE: Match if the tool can accomplish the user's stated goal
4. TAGS: Consider the tool's tags, not just its name
5. WHEN IN DOUBT, INCLUDE IT - better to show more options

Respond in JSON format:
{"matches": [{"name": "EXACT Tool Name From Above List", "reason": "Why this matches"}]}

If absolutely NO tools match, respond: {"matches": []}

CRITICAL: The "name" field MUST be copied EXACTLY from the AVAILABLE TOOLS list.`, requirement, toolList)

	// Call LLM
	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					genai.NewPartFromText(prompt),
				},
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(0.1)), // Low temperature for consistent results
		},
	}

	var responseText strings.Builder
	for resp, err := range llm.GenerateContent(ctx, llmReq, true) {
		if err != nil {
			return nil
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part.Text != "" {
					responseText.WriteString(part.Text)
				}
			}
		}
	}

	// Parse the JSON response
	response := responseText.String()

	// Extract JSON from response (may be wrapped in markdown code blocks)
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart == -1 || jsonEnd == -1 || jsonEnd <= jsonStart {
		return nil
	}
	jsonStr := response[jsonStart : jsonEnd+1]

	var parsed struct {
		Matches []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil
	}

	// Build results by matching names back to servers
	// Use fuzzy matching to handle LLM returning slightly different names
	var results []ToolSearchResult
	for _, match := range parsed.Matches {
		matchNameLower := strings.ToLower(match.Name)
		matchNameNormalized := normalizeToolName(match.Name)

		var bestMatch *mcpstore.Server
		for i := range servers {
			srv := &servers[i]
			srvNameLower := strings.ToLower(srv.Name)
			srvNameNormalized := normalizeToolName(srv.Name)

			// Exact match
			if srvNameLower == matchNameLower {
				bestMatch = srv
				break
			}

			// Normalized match (removes spaces, hyphens, underscores)
			if srvNameNormalized == matchNameNormalized {
				bestMatch = srv
				break
			}

			// Contains match (for partial matches)
			if strings.Contains(srvNameLower, matchNameLower) || strings.Contains(matchNameLower, srvNameLower) {
				bestMatch = srv
				// Don't break - keep looking for better match
			}
		}

		if bestMatch != nil {
			// Check if already added (avoid duplicates)
			alreadyAdded := false
			for _, r := range results {
				if r.ID == bestMatch.McpId {
					alreadyAdded = true
					break
				}
			}
			if alreadyAdded {
				continue
			}

			// Extract env vars from config if present
			var envVars map[string]string
			if bestMatch.Config != nil && len(bestMatch.Config.Env) > 0 {
				envVars = bestMatch.Config.Env
			}

			results = append(results, ToolSearchResult{
				ID:             bestMatch.McpId,
				Name:           bestMatch.Name,
				Description:    bestMatch.Description,
				Source:         bestMatch.Source,
				Tags:           bestMatch.Tags,
				Installable:    true,
				Reason:         match.Reason,
				RequiresApiKey: bestMatch.RequiresApiKey,
				EnvVars:        envVars,
			})
		}
	}

	return results
}

// ExtractMissingToolsFromResponse parses the AI response to detect missing tools
// Returns a list of search queries for tools the AI says are missing
func ExtractMissingToolsFromResponse(response string) []string {
	var queries []string

	// Pattern 1: "tools that are not currently installed:"
	// - tool description: for reason
	missingPattern := regexp.MustCompile(`(?i)tools?\s+(?:that\s+are\s+)?not\s+(?:currently\s+)?installed[:\s]*\n((?:[-•*]\s*[^\n]+\n?)+)`)
	if matches := missingPattern.FindStringSubmatch(response); len(matches) > 1 {
		lines := strings.Split(matches[1], "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "- ")
			line = strings.TrimPrefix(line, "• ")
			line = strings.TrimPrefix(line, "* ")
			if line != "" {
				// Extract the tool description part (before the colon if present)
				parts := strings.SplitN(line, ":", 2)
				toolDesc := strings.TrimSpace(parts[0])
				if toolDesc != "" {
					queries = append(queries, toolDesc)
				}
			}
		}
	}

	// Pattern 2: "**Missing:** [tool1, tool2]" or "**Missing:**\n- tool1"
	missingPattern2 := regexp.MustCompile(`\*\*Missing:\*\*\s*(?:\[([^\]]+)\]|((?:\n[-•*]\s*[^\n]+)+))`)
	if matches := missingPattern2.FindStringSubmatch(response); len(matches) > 0 {
		if matches[1] != "" {
			// Bracket format: [tool1, tool2]
			tools := strings.Split(matches[1], ",")
			for _, tool := range tools {
				tool = strings.TrimSpace(tool)
				if tool != "" && tool != "none" && !strings.HasPrefix(strings.ToLower(tool), "all") {
					queries = append(queries, tool)
				}
			}
		} else if matches[2] != "" {
			// List format
			lines := strings.Split(matches[2], "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, "- ")
				line = strings.TrimPrefix(line, "• ")
				line = strings.TrimPrefix(line, "* ")
				if line != "" {
					queries = append(queries, line)
				}
			}
		}
	}

	// Pattern 3: "Would you like me to help you find and install" (indicates tool request)
	if strings.Contains(strings.ToLower(response), "help you find and install") {
		// Look for tool-related keywords in the response
		keywords := []string{"screenshot", "browser", "github", "file", "database", "api", "search", "web"}
		for _, kw := range keywords {
			if strings.Contains(strings.ToLower(response), kw) {
				queries = append(queries, kw)
			}
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	unique := make([]string, 0, len(queries))
	for _, q := range queries {
		lower := strings.ToLower(q)
		if !seen[lower] {
			seen[lower] = true
			unique = append(unique, q)
		}
	}

	return unique
}

// InternetSearchRequest is the request for POST /api/ai/tool-search-internet
type InternetSearchRequest struct {
	Requirement string `json:"requirement"`
}

// InternetMCPResult represents an MCP server found via internet search
type InternetMCPResult struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	URL         string            `json:"url"`
	InstallType string            `json:"installType"` // npx, github, etc
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	EnvVars     map[string]string `json:"envVars,omitempty"`
	Confidence  float64           `json:"confidence"`
	Source      string            `json:"source"` // tavily-search
}

// InternetSearchResponse is the response for POST /api/ai/tool-search-internet
type InternetSearchResponse struct {
	TavilyAvailable bool                `json:"tavilyAvailable"`
	Results         []InternetMCPResult `json:"results"`
	Message         string              `json:"message,omitempty"`
	ToolUsed        string              `json:"toolUsed,omitempty"`    // Name of the MCP tool used for search
	SearchQuery     string              `json:"searchQuery,omitempty"` // The query sent to the tool
}

// URLExtractRequest is the request for POST /api/ai/url-extract
type URLExtractRequest struct {
	URL string `json:"url"`
}

// URLExtractResponse is the response for POST /api/ai/url-extract
type URLExtractResponse struct {
	Found     bool               `json:"found"`
	MCPServer *InternetMCPResult `json:"mcpServer,omitempty"`
	Message   string             `json:"message,omitempty"`
	ToolUsed  string             `json:"toolUsed,omitempty"`
	URL       string             `json:"url"`
}

// IsWebSearchConfigured checks if a web search tool is configured in settings.
// Returns (configured, serverName, toolName).
// The setting value format is "serverName:toolName" to uniquely identify tools.
// Uses host filesystem config — prefer isWebSearchConfiguredWith(appCfg) when request is available.
func IsWebSearchConfigured() (bool, string, string) {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return false, "", ""
	}
	return isWebSearchConfiguredWith(appCfg)
}

// isWebSearchConfiguredWith checks web search config using a pre-loaded AppConfig.
//
// Tool names are normalized hyphen→underscore. MCP servers register their tools
// with underscore identifiers (e.g. tavily_search, tavily_extract); a hyphen
// form (tavily-search) is never a valid tool function name and would never
// resolve at the LLM level. Older config.yaml hand-edits and pre-1.0 install
// flows produced the hyphen form, so we canonicalize at the parse boundary.
func isWebSearchConfiguredWith(appCfg *config.AppConfig) (bool, string, string) {
	if appCfg == nil || appCfg.General.WebSearchTool == "" {
		return false, "", ""
	}
	// Parse the format "serverName:toolName"
	parts := strings.SplitN(appCfg.General.WebSearchTool, ":", 2)
	if len(parts) == 2 {
		return true, parts[0], strings.ReplaceAll(parts[1], "-", "_")
	}
	// No tool name specified, return just server name
	return true, parts[0], ""
}

// IsWebExtractConfigured checks if a web extract tool is configured in settings.
// Returns (configured, serverName, toolName).
// The setting value format is "serverName:toolName" to uniquely identify tools.
// Uses host filesystem config — prefer isWebExtractConfiguredWith(appCfg) when request is available.
func IsWebExtractConfigured() (bool, string, string) {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return false, "", ""
	}
	return isWebExtractConfiguredWith(appCfg)
}

// isWebExtractConfiguredWith checks web extract config using a pre-loaded AppConfig.
// See isWebSearchConfiguredWith for the hyphen→underscore normalization rationale.
func isWebExtractConfiguredWith(appCfg *config.AppConfig) (bool, string, string) {
	if appCfg == nil || appCfg.General.WebExtractTool == "" {
		return false, "", ""
	}
	// Parse the format "serverName:toolName"
	parts := strings.SplitN(appCfg.General.WebExtractTool, ":", 2)
	if len(parts) == 2 {
		return true, parts[0], strings.ReplaceAll(parts[1], "-", "_")
	}
	// No tool name specified, return just server name
	return true, parts[0], ""
}

// AIToolSearchInternetHandler handles POST /api/ai/tool-search-internet
func AIToolSearchInternetHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req InternetSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Check if web search tool is configured
	configured, serverName, searchToolName := isWebSearchConfiguredWith(effectiveAppConfig(r))
	slog.Debug("web search tool configuration", "component", "internet-search", "configured", configured, "server", serverName, "tool", searchToolName)
	if !configured {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(InternetSearchResponse{
			TavilyAvailable: false,
			Results:         []InternetMCPResult{},
			Message:         "No web search tool configured. Go to Settings → General to configure an MCP server with 'websearch' in its name.",
		})
		return
	}

	// Clean up the requirement - extract ONLY the technology/tool keywords
	// The requirement often contains garbage like "that is not currently installed"
	cleanedReq := req.Requirement
	cleanedReq = strings.ToLower(cleanedReq)

	// Remove common garbage phrases
	garbagePatterns := []string{
		"that is not currently installed",
		"that is currently not installed",
		"not currently installed",
		"currently installed",
		"interacting with",
		"for interacting with",
		"to interact with",
		"would need a tool for",
		"need a tool for",
		"using the tool",
		"using the",
		"the tool",
		"mcp servers",
		"mcp server",
		"model context protocol",
		"development",
		"tool for",
	}
	for _, pattern := range garbagePatterns {
		cleanedReq = strings.ReplaceAll(cleanedReq, pattern, " ")
	}

	// Remove quotes, semicolons, and other punctuation
	cleanedReq = regexp.MustCompile(`[";:,\(\)\[\]]`).ReplaceAllString(cleanedReq, " ")

	// Clean up whitespace and deduplicate words
	cleanedReq = strings.TrimSpace(cleanedReq)
	cleanedReq = regexp.MustCompile(`\s+`).ReplaceAllString(cleanedReq, " ")

	words := strings.Fields(cleanedReq)
	seen := make(map[string]bool)
	uniqueWords := []string{}
	for _, w := range words {
		w = strings.TrimSpace(w)
		if !seen[w] && w != "" && len(w) > 1 {
			seen[w] = true
			uniqueWords = append(uniqueWords, w)
		}
	}
	cleanedReq = strings.Join(uniqueWords, " ")

	// Build a clean search query - ALWAYS include "MCP server" for proper results
	searchQuery := fmt.Sprintf("%s MCP server github npm", cleanedReq)
	slog.Debug("internet search query", "component", "internet-search", "query", searchQuery)

	// Search the internet using the configured MCP tool
	results, err := searchInternetForMCPServers(ctx, serverName, searchToolName, searchQuery, effectiveAppConfig(r))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(InternetSearchResponse{
			TavilyAvailable: true,
			Results:         []InternetMCPResult{},
			Message:         fmt.Sprintf("Search failed: %v", err),
			ToolUsed:        serverName,
			SearchQuery:     searchQuery,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(InternetSearchResponse{
		TavilyAvailable: true,
		Results:         results,
		ToolUsed:        serverName,
		SearchQuery:     searchQuery,
	})
}

// URLExtractHandler handles POST /api/ai/url-extract
// Uses tavily-extract or similar to extract MCP server info from a URL
func URLExtractHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req URLExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.URL == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(URLExtractResponse{
			Found:   false,
			Message: "No URL provided",
			URL:     req.URL,
		})
		return
	}

	slog.Debug("starting url extraction", "component", "url-extract", "url", req.URL)

	// Check if web extract tool is configured
	// Check if web extract tool is configured
	configured, serverName, extractToolName := isWebExtractConfiguredWith(effectiveAppConfig(r))
	if !configured {
		// Fall back to web search tool
		var searchConfigured bool
		searchConfigured, serverName, _ = isWebSearchConfiguredWith(effectiveAppConfig(r))
		extractToolName = "" // No specific tool name, will fallback to "extract" search
		configured = searchConfigured
	}

	if !configured {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(URLExtractResponse{
			Found:   false,
			Message: "No web extract tool configured. Go to Settings → General to configure one.",
			URL:     req.URL,
		})
		return
	}

	slog.Debug("using extract tool", "component", "url-extract", "server", serverName, "extractTool", extractToolName)

	// Extract content from URL
	mcpServer, err := extractMCPServerFromURL(ctx, req.URL, serverName, extractToolName, effectiveAppConfig(r))
	if err != nil {
		slog.Error("url extraction failed", "component", "url-extract", "error", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(URLExtractResponse{
			Found:    false,
			Message:  fmt.Sprintf("Failed to extract: %v", err),
			ToolUsed: serverName,
			URL:      req.URL,
		})
		return
	}

	if mcpServer == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(URLExtractResponse{
			Found:    false,
			Message:  "No MCP server configuration found at this URL",
			ToolUsed: serverName,
			URL:      req.URL,
		})
		return
	}

	slog.Info("found mcp server from url", "component", "url-extract", "server", mcpServer.Name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(URLExtractResponse{
		Found:     true,
		MCPServer: mcpServer,
		ToolUsed:  serverName,
		URL:       req.URL,
	})
}

// extractMCPServerFromURL uses the configured MCP tool to extract content and parse for MCP server config
func extractMCPServerFromURL(ctx context.Context, url string, serverName string, extractToolName string, appCfg *config.AppConfig) (*InternetMCPResult, error) {
	if appCfg == nil {
		return nil, fmt.Errorf("no app config available")
	}
	injectProviderSecrets(appCfg)

	// Initialize MCP manager
	mcpManager, err := mcp.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP manager: %w", err)
	}
	defer mcpManager.Cleanup()

	slog.Debug("initializing mcp server", "component", "url-extract", "server", serverName)
	namedToolset, err := mcpManager.InitializeSingleToolset(ctx, serverName)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MCP server '%s': %w", serverName, err)
	}

	// Get tools and find extract tool
	roCtx := &minimalReadonlyContext{Context: ctx}
	mcpTools, err := namedToolset.Toolset.Tools(roCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tools from '%s': %w", serverName, err)
	}

	slog.Debug("got tools from mcp server", "component", "url-extract", "count", len(mcpTools))

	// Find extract tool - use configured tool name if provided, otherwise search for "extract"
	var extractTool tool.Tool
	for _, t := range mcpTools {
		slog.Debug("found tool", "component", "url-extract", "tool", t.Name())
		if extractToolName != "" {
			// Use the specifically configured tool
			if t.Name() == extractToolName {
				extractTool = t
				break
			}
		} else {
			// Fallback: search for "extract" in the name
			if strings.Contains(strings.ToLower(t.Name()), "extract") {
				extractTool = t
				break
			}
		}
	}

	if extractTool == nil {
		if extractToolName != "" {
			return nil, fmt.Errorf("configured extract tool '%s' not found in MCP server '%s'", extractToolName, serverName)
		}
		return nil, fmt.Errorf("no extract tool found in MCP server '%s'", serverName)
	}

	slog.Debug("using extract tool", "component", "url-extract", "tool", extractTool.Name())

	// Get the tool's declaration (schema) for LLM
	type toolWithDeclaration interface {
		Declaration() *genai.FunctionDeclaration
	}

	declTool, ok := extractTool.(toolWithDeclaration)
	if !ok {
		return nil, fmt.Errorf("extract tool does not expose its schema")
	}

	declaration := declTool.Declaration()
	if declaration == nil {
		return nil, fmt.Errorf("extract tool has nil declaration")
	}

	slog.Debug("tool schema", "component", "url-extract", "name", declaration.Name, "description", declaration.Description)

	// Get LLM provider for tool execution
	providerName := appCfg.General.DefaultProvider
	modelName := appCfg.General.DefaultModel
	if providerName == "" {
		providerName = "gemini"
	}
	if modelName == "" {
		modelName = "gemini-2.0-flash"
	}

	llm, err := provider.GetProvider(ctx, providerName, modelName, appCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to get LLM provider: %w", err)
	}

	// Step 1: Ask LLM to use the tool to extract content from the URL
	// The LLM will see the tool schema and generate the correct parameters
	extractPrompt := fmt.Sprintf(`You need to extract content from this URL: %s

Use the available tool to fetch and extract the content from this URL. Call the tool with the appropriate parameters.`, url)

	extractReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					genai.NewPartFromText(extractPrompt),
				},
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(0.1)),
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{declaration},
				},
			},
		},
	}

	slog.Debug("asking llm to call extract tool", "component", "url-extract")
	var functionCall *genai.FunctionCall
	for resp, err := range llm.GenerateContent(ctx, extractReq, true) {
		if err != nil {
			return nil, fmt.Errorf("LLM tool call failed: %w", err)
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part.FunctionCall != nil {
					functionCall = part.FunctionCall
					break
				}
			}
		}
		if functionCall != nil {
			break
		}
	}

	if functionCall == nil {
		return nil, fmt.Errorf("LLM did not call the extract tool")
	}

	slog.Debug("llm function call", "component", "url-extract", "function", functionCall.Name, "args", functionCall.Args)

	// Step 2: Execute the tool with LLM-provided arguments
	type runnableTool interface {
		Run(tool.Context, any) (map[string]any, error)
	}

	runnable, ok := extractTool.(runnableTool)
	if !ok {
		return nil, fmt.Errorf("extract tool does not implement Run method")
	}

	toolCtx := &minimalToolContext{Context: ctx}
	extractResult, err := runnable.Run(toolCtx, functionCall.Args)
	if err != nil {
		return nil, fmt.Errorf("extract tool execution failed: %w", err)
	}

	extractResultJSON, err := json.Marshal(extractResult)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal extract results: %w", err)
	}
	slog.Debug("extracted content", "component", "url-extract", "bytes", len(extractResultJSON))

	// Step 3: Parse the extracted content for MCP server info
	prompt := fmt.Sprintf(`You are analyzing a web page to find MCP (Model Context Protocol) server configuration information.

Extracted page content:
%s

Look for:
1. Package name (e.g., @ui5/mcp-server, @sap-ux/fiori-mcp-server)
2. Installation command (usually npx or npm/node)
3. Configuration examples (mcpServers JSON blocks)
4. Required environment variables (API Keys, Tokens, Secrets). LOOK CLOSELY for mentions of API keys (e.g. OPENAI_API_KEY, HEYGEN_API_KEY).
5. If the server requires authentication (API Key, Token), YOU MUST add it to envVars with an empty string or placeholder value.

If you find an MCP server, respond with a JSON object:
{
  "name": "package-name",
  "description": "What this server does",
  "url": "%s",
  "installType": "npx",
  "command": "npx",
  "args": ["-y", "package-name"],
  "envVars": {"API_KEY": ""},
  "confidence": 0.95
}

If NO MCP server configuration is found, respond with: null

Respond ONLY with the JSON object or null.`, string(extractResultJSON), url)

	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					genai.NewPartFromText(prompt),
				},
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(0.2)),
		},
	}

	slog.Debug("calling ai to parse extracted content", "component", "url-extract")
	var responseText strings.Builder
	for resp, err := range llm.GenerateContent(ctx, llmReq, false) {
		if err != nil {
			return nil, fmt.Errorf("AI parsing failed: %w", err)
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part.Text != "" {
					responseText.WriteString(part.Text)
				}
			}
		}
	}

	response := strings.TrimSpace(responseText.String())
	slog.Debug("ai response", "component", "url-extract", "response", response)

	if response == "null" || response == "" {
		return nil, nil
	}

	// Extract JSON from response
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart == -1 || jsonEnd == -1 || jsonEnd <= jsonStart {
		return nil, nil
	}
	jsonStr := response[jsonStart : jsonEnd+1]

	var result InternetMCPResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		slog.Error("json parse error", "component", "url-extract", "error", err)
		return nil, nil
	}

	result.Source = serverName
	return &result, nil
}

type InternetMCPInstallRequest struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// InternetMCPInstallResponse is the response for /api/mcp-internet-install
type InternetMCPInstallResponse struct {
	Status      string `json:"status"`
	ToolsLoaded int    `json:"toolsLoaded,omitempty"`
	Error       string `json:"error,omitempty"`
}

// InternetMCPInstallHandler handles POST /api/mcp-internet-install
func InternetMCPInstallHandler(w http.ResponseWriter, r *http.Request) {
	var req InternetMCPInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Name == "" || req.Command == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(InternetMCPInstallResponse{
			Status: "error",
			Error:  "Name and command are required",
		})
		return
	}

	// Sanitize server name
	serverName := strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))
	serverName = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(serverName, "")
	if serverName == "" {
		serverName = "internet-mcp-server"
	}

	// Platform mode: save to DB store
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		// Check if server already exists
		existing, _ := mcpStore.Get(r.Context(), serverName)
		if existing != nil {
			serverName = serverName + "-2"
		}

		newConfig := config.MCPServerConfig{
			Command:   req.Command,
			Args:      req.Args,
			Env:       req.Env,
			Transport: "stdio",
		}

		// Pre-flight: ensure stdio servers can be installed (sandbox must be enabled)
		if err := checkStdioMCPInstallable(newConfig.Transport); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(InternetMCPInstallResponse{
				Status: "error",
				Error:  err.Error(),
			})
			return
		}

		s := &store.MCPServer{
			Name:      serverName,
			Command:   newConfig.Command,
			Args:      newConfig.Args,
			Env:       newConfig.Env,
			Transport: newConfig.Transport,
			CreatedBy: effectiveUserID(r),
		}

		if err := mcpStore.Save(r.Context(), s); err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(InternetMCPInstallResponse{
				Status: "error",
				Error:  fmt.Sprintf("Failed to save MCP server: %v", err),
			})
			return
		}

		// Discover tools asynchronously with timeout.
		runtimeCtx := detachedRuntimeNetworkPolicyContext(r, effectiveAppConfig(r))
		asyncDiscoverAndCacheTools(runtimeCtx, mcpStore, serverName, newConfig, buildPGSessionRegistry(r.Context()))

		GetChatManager().Reset()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(InternetMCPInstallResponse{
			Status: "ok",
		})
		return
	}

	// Personal mode no longer supported
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(InternetMCPInstallResponse{
		Status: "error",
		Error:  "MCP server store not available",
	})
}

// searchInternetForMCPServers uses the configured MCP web search tool to find MCP servers
func searchInternetForMCPServers(ctx context.Context, serverName string, searchToolName string, searchQuery string, appCfg *config.AppConfig) ([]InternetMCPResult, error) {
	slog.Debug("starting internet search for mcp servers", "component", "internet-search", "server", serverName, "tool", searchToolName, "query", searchQuery)

	if appCfg == nil {
		return nil, fmt.Errorf("no app config available")
	}
	injectProviderSecrets(appCfg)

	// Initialize MCP manager and the specific web search server
	mcpManager, err := mcp.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP manager: %w", err)
	}
	defer mcpManager.Cleanup()

	slog.Debug("initializing mcp server", "component", "internet-search", "server", serverName)
	// Initialize just the web search MCP server
	namedToolset, err := mcpManager.InitializeSingleToolset(ctx, serverName)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize web search MCP server '%s': %w", serverName, err)
	}

	// Get the tools from this server using minimalReadonlyContext
	roCtx := &minimalReadonlyContext{Context: ctx}
	mcpTools, err := namedToolset.Toolset.Tools(roCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get tools from '%s': %w", serverName, err)
	}
	slog.Debug("got tools from mcp server", "component", "internet-search", "count", len(mcpTools))

	// Find search tool - use configured tool name if provided, otherwise search for "search"
	var searchTool tool.Tool
	for _, t := range mcpTools {
		slog.Debug("found tool", "component", "internet-search", "tool", t.Name())
		if searchToolName != "" {
			// Use the specifically configured tool
			if t.Name() == searchToolName {
				searchTool = t
				break
			}
		} else {
			// Fallback: search for "search" in the name
			if strings.Contains(strings.ToLower(t.Name()), "search") {
				searchTool = t
				break
			}
		}
	}

	if searchTool == nil {
		if searchToolName != "" {
			return nil, fmt.Errorf("configured search tool '%s' not found in MCP server '%s'", searchToolName, serverName)
		}
		return nil, fmt.Errorf("no search tool found in MCP server '%s'", serverName)
	}
	slog.Debug("using search tool", "component", "internet-search", "tool", searchTool.Name())

	// Get the tool's declaration (schema) for LLM
	type toolWithDeclaration interface {
		Declaration() *genai.FunctionDeclaration
	}

	declTool, ok := searchTool.(toolWithDeclaration)
	if !ok {
		return nil, fmt.Errorf("search tool does not expose its schema")
	}

	declaration := declTool.Declaration()
	if declaration == nil {
		return nil, fmt.Errorf("search tool has nil declaration")
	}

	// Get LLM provider for tool execution
	providerName := appCfg.General.DefaultProvider
	modelName := appCfg.General.DefaultModel
	if providerName == "" {
		providerName = "gemini"
	}
	if modelName == "" {
		modelName = "gemini-2.0-flash"
	}

	llm, err := provider.GetProvider(ctx, providerName, modelName, appCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to get LLM provider: %w", err)
	}

	// Ask LLM to use the tool to search
	searchPrompt := fmt.Sprintf(`You need to search the web for this query: %s

Use the available tool to search the web. Call the tool with the appropriate parameters.`, searchQuery)

	searchReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					genai.NewPartFromText(searchPrompt),
				},
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(0.1)),
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{declaration},
				},
			},
		},
	}

	slog.Debug("asking llm to call search tool", "component", "internet-search")
	var functionCall *genai.FunctionCall
	for resp, err := range llm.GenerateContent(ctx, searchReq, true) {
		if err != nil {
			return nil, fmt.Errorf("LLM tool call failed: %w", err)
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part.FunctionCall != nil {
					functionCall = part.FunctionCall
					break
				}
			}
		}
		if functionCall != nil {
			break
		}
	}

	if functionCall == nil {
		return nil, fmt.Errorf("LLM did not call the search tool")
	}

	slog.Debug("llm function call", "component", "internet-search", "function", functionCall.Name, "args", functionCall.Args)

	// Execute the tool with LLM-provided arguments
	type runnableTool interface {
		Run(tool.Context, any) (map[string]any, error)
	}

	runnable, ok := searchTool.(runnableTool)
	if !ok {
		return nil, fmt.Errorf("search tool does not implement Run method")
	}

	toolCtx := &minimalToolContext{Context: ctx}
	searchResult, err := runnable.Run(toolCtx, functionCall.Args)
	if err != nil {
		return nil, fmt.Errorf("web search failed: %w", err)
	}
	slog.Debug("search returned results", "component", "internet-search", "resultKeys", len(searchResult))

	// Convert search results to JSON for AI processing
	searchResultJSON, err := json.Marshal(searchResult)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search results: %w", err)
	}
	slog.Debug("search result json", "component", "internet-search", "bytes", len(searchResultJSON))
	// Log first 1000 chars of search results to see what Tavily returns
	previewLen := min(1000, len(searchResultJSON))
	slog.Debug("search result preview", "component", "internet-search", "preview", string(searchResultJSON[:previewLen]))

	// Use AI to parse search results into MCP server suggestions
	// Reuse the existing LLM provider
	// (providerName, modelName, and llm already defined above)
	if err != nil {
		return nil, fmt.Errorf("failed to get LLM provider: %w", err)
	}

	prompt := fmt.Sprintf(`You are analyzing web search results to find MCP (Model Context Protocol) servers.

Search results:
%s

Extract any MCP servers mentioned in these results. For each server found, provide:
- name: The npm package name or GitHub repo (e.g., "@anthropic/mcp-server-fetch" or "user/repo-name")
- description: What it does
- url: The GitHub or npm URL
- installType: "npx" for npm packages, "github" for GitHub repos
- command: "npx" for npm, "node" for github
- args: Installation arguments as JSON array (e.g., ["-y", "package-name"])
- envVars: Required environment variables (API Keys, Tokens) as JSON object. Look for mentions of "requires API key" or specific variable names.
- confidence: How confident you are (0.0-1.0)

Respond ONLY with a JSON array. If no MCP servers found, return [].

Example:
[{"name": "mcp-server-sqlite", "description": "SQLite database access", "url": "https://github.com/modelcontextprotocol/servers", "installType": "npx", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-sqlite"], "envVars": {}, "confidence": 0.9}, {"name": "mcp-server-gpt", "description": "GPT Client", "url": "...", "installType": "npx", "command": "npx", "args": [...], "envVars": {"OPENAI_API_KEY": ""}, "confidence": 0.95}]`, string(searchResultJSON))

	llmReq := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					genai.NewPartFromText(prompt),
				},
			},
		},
		Config: &genai.GenerateContentConfig{
			Temperature: genai.Ptr(float32(0.3)),
		},
	}

	slog.Debug("calling ai to parse search results", "component", "internet-search")
	var responseText strings.Builder
	for resp, err := range llm.GenerateContent(ctx, llmReq, false) {
		if err != nil {
			return nil, fmt.Errorf("AI parsing failed: %w", err)
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part.Text != "" {
					responseText.WriteString(part.Text)
				}
			}
		}
	}

	response := strings.TrimSpace(responseText.String())
	slog.Debug("ai response", "component", "internet-search", "length", len(response))

	jsonStart := strings.Index(response, "[")
	jsonEnd := strings.LastIndex(response, "]")
	if jsonStart == -1 || jsonEnd == -1 || jsonEnd <= jsonStart {
		slog.Warn("no json array found in ai response", "component", "internet-search", "preview", response[:min(200, len(response))])
		return []InternetMCPResult{}, nil
	}
	jsonStr := response[jsonStart : jsonEnd+1]
	slog.Debug("extracted json", "component", "internet-search", "length", len(jsonStr))

	var results []InternetMCPResult
	if err := json.Unmarshal([]byte(jsonStr), &results); err != nil {
		slog.Error("json parse error", "component", "internet-search", "error", err)
		return []InternetMCPResult{}, nil
	}

	slog.Debug("parsed mcp server results", "component", "internet-search", "count", len(results))
	for i := range results {
		results[i].Source = serverName
	}

	return results, nil
}
