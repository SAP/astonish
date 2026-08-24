package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/flowstore"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

// AgentListItem represents an agent in the list response
type AgentListItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Source       string `json:"source"`          // "system", "local", or "store"
	Scope        string `json:"scope,omitempty"` // "personal" or "team" (platform mode only)
	HasError     bool   `json:"hasError,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	IsReadOnly   bool   `json:"isReadOnly,omitempty"` // True for store flows
	TapName      string `json:"tapName,omitempty"`    // For store flows: which tap
}

// AgentListResponse is the response for GET /api/agents
type AgentListResponse struct {
	Agents []AgentListItem `json:"agents"`
}

// AgentDetailResponse is the response for GET /api/agents/:name
type AgentDetailResponse struct {
	Name   string              `json:"name"`
	Source string              `json:"source"`
	Scope  string              `json:"scope,omitempty"` // "personal" or "team" (platform mode only)
	YAML   string              `json:"yaml"`
	Config *config.AgentConfig `json:"config,omitempty"`
}

// scanAgentsDir scans a directory for agent YAML files
func scanAgentsDir(dir string, source string, agents map[string]AgentListItem) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // Ignore errors (directory might not exist)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}

		name := entry.Name()[:len(entry.Name())-5] // Remove .yaml extension

		// Skip if already found (system has priority)
		if _, exists := agents[name]; exists {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		cfg, err := config.LoadAgent(path)
		if err != nil {
			// Still add the agent but mark it as having an error
			agents[name] = AgentListItem{
				ID:           name,
				Name:         name,
				Description:  "(Error loading agent)",
				Source:       source,
				HasError:     true,
				ErrorMessage: err.Error(),
			}
			continue
		}

		// Skip drill suites and drills — they are not regular flows
		if cfg.Type == "drill" || cfg.Type == "drill_suite" || cfg.Type == "test" || cfg.Type == "test_suite" {
			continue
		}

		agents[name] = AgentListItem{
			ID:          name,
			Name:        name,
			Description: cfg.Description,
			Source:      source,
		}
	}
}

// findAgentPath finds the path to an agent YAML file
// Returns path, source ("system", "local", or "store"), error
// Handles special ID formats: "store:tapName:flowName" for store flows
func findAgentPath(name string) (string, string, error) {
	// Check for store: prefix (new format)
	if strings.HasPrefix(name, "store:") {
		parts := strings.SplitN(name, ":", 3)
		if len(parts) == 3 {
			tapName, flowName := parts[1], parts[2]
			if strings.ContainsAny(tapName, "/\\") || strings.Contains(tapName, "..") {
				return "", "", fmt.Errorf("invalid tap name")
			}
			if strings.ContainsAny(flowName, "/\\") || strings.Contains(flowName, "..") {
				return "", "", fmt.Errorf("invalid flow name")
			}
			if store, err := flowstore.NewStore(); err == nil {
				if path, ok := store.GetInstalledFlowPath(tapName, flowName); ok {
					return path, "store", nil
				}
			}
		}
		return "", "", os.ErrNotExist
	}

	// Reject names with path traversal sequences before using in any path construction
	if strings.ContainsAny(name, "\\") || strings.Contains(name, "..") {
		return "", "", os.ErrNotExist
	}

	// Check system directory first
	if sysDir, err := config.GetAgentsDir(); err == nil {
		absDir, _ := filepath.Abs(sysDir)
		absPath, err := filepath.Abs(filepath.Join(sysDir, name+".yaml"))
		if err == nil && strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
			if _, err := os.Stat(absPath); err == nil {
				return absPath, "system", nil
			}
		}
	}

	// Check local directory
	absAgents, _ := filepath.Abs("agents")
	absLocal, err := filepath.Abs(filepath.Join("agents", name+".yaml"))
	if err == nil && strings.HasPrefix(absLocal, absAgents+string(filepath.Separator)) {
		if _, err := os.Stat(absLocal); err == nil {
			return absLocal, "local", nil
		}
	}

	// Check user flows directory
	if flowsDir, err := flowstore.GetFlowsDir(); err == nil {
		absFlows, _ := filepath.Abs(flowsDir)
		absPath, err := filepath.Abs(filepath.Join(flowsDir, name+".yaml"))
		if err == nil && strings.HasPrefix(absPath, absFlows+string(filepath.Separator)) {
			if _, err := os.Stat(absPath); err == nil {
				return absPath, "system", nil
			}
		}
	}

	// Check store (for legacy format: "flow" or "tap/flow")
	if store, err := flowstore.NewStore(); err == nil {
		var tapName, flowName string
		if !containsSlash(name) {
			// No slash - assume official
			tapName = flowstore.OfficialStoreName
			flowName = name
		} else {
			// Has slash - parse tap/flow
			parts := splitFirst(name, "/")
			if len(parts) == 2 {
				tapName = parts[0]
				flowName = parts[1]
			}
		}

		if tapName != "" && flowName != "" {
			if path, ok := store.GetInstalledFlowPath(tapName, flowName); ok {
				return path, "store", nil
			}
		}
	}

	return "", "", os.ErrNotExist
}

// containsSlash checks if string contains a slash
func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}

// splitFirst splits string on first occurrence of sep
func splitFirst(s, sep string) []string {
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			return []string{s[:i], s[i+len(sep):]}
		}
	}
	return []string{s}
}

// ListAgentsHandler handles GET /api/agents
func ListAgentsHandler(w http.ResponseWriter, r *http.Request) {
	// Platform mode: merge personal + team flows (private-first ownership).
	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform && (svc.PersonalFlows != nil || svc.Flows != nil) {
		result := make([]AgentListItem, 0)

		// Personal flows first (user's private flows)
		if svc.PersonalFlows != nil {
			for _, f := range svc.PersonalFlows.ListAllFlows(r.Context()) {
				result = append(result, AgentListItem{
					ID:          f.Name,
					Name:        f.Name,
					Description: f.Description,
					Source:      "system",
					Scope:       "personal",
				})
			}
		}

		// Team flows (shared/published flows)
		if svc.Flows != nil {
			personalNames := make(map[string]bool, len(result))
			for _, item := range result {
				personalNames[item.ID] = true
			}
			for _, f := range svc.Flows.ListAllFlows(r.Context()) {
				// If the same name exists in both personal and team,
				// include both — they are separate copies.
				id := f.Name
				if personalNames[id] {
					id = "team:" + f.Name
				}
				result = append(result, AgentListItem{
					ID:          id,
					Name:        f.Name,
					Description: f.Description,
					Source:      "system",
					Scope:       "team",
				})
			}
		}

		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		respondJSON(w, http.StatusOK, AgentListResponse{Agents: result})
		return
	}

	// Personal mode: scan filesystem directories.
	agents := make(map[string]AgentListItem)

	// Scan system directory first (has priority)
	if sysDir, err := config.GetAgentsDir(); err == nil {
		scanAgentsDir(sysDir, "system", agents)
	}

	// Scan local directory
	scanAgentsDir("agents", "local", agents)

	// Scan new user flows directory
	if flowsDir, err := flowstore.GetFlowsDir(); err == nil {
		scanAgentsDir(flowsDir, "system", agents)
	}

	// Add installed store flows
	if fs, err := flowstore.NewStore(); err == nil {
		for _, tap := range fs.GetAllTaps() {
			storeDir := fs.GetStoreDir()
			tapDir := filepath.Join(storeDir, tap.Name)
			entries, err := os.ReadDir(tapDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
					continue
				}
				if entry.Name() == "manifest.yaml" {
					continue
				}
				name := entry.Name()[:len(entry.Name())-5]

				displayName := name
				if tap.Name != flowstore.OfficialStoreName {
					displayName = tap.Name + "/" + name
				}

				storeID := "store:" + tap.Name + ":" + name

				path := filepath.Join(tapDir, entry.Name())
				cfg, err := config.LoadAgent(path)
				desc := "(Installed from store)"
				if err == nil && cfg.Description != "" {
					desc = cfg.Description
				}

				agents[storeID] = AgentListItem{
					ID:          storeID,
					Name:        displayName,
					Description: desc,
					Source:      "store",
					IsReadOnly:  true,
					TapName:     tap.Name,
				}
			}
		}
	}

	// Convert map to sorted slice
	result := make([]AgentListItem, 0, len(agents))
	var names []string
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		result = append(result, agents[name])
	}

	respondJSON(w, http.StatusOK, AgentListResponse{Agents: result})
}

// GetAgentHandler handles GET /api/agents/{name}
func GetAgentHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]
	scope := r.URL.Query().Get("scope") // optional: "personal" or "team"

	// Strip "team:" prefix used by the frontend for React key uniqueness.
	// If the name had a "team:" prefix, force scope to "team" unless explicitly overridden.
	if strings.HasPrefix(name, "team:") {
		name = strings.TrimPrefix(name, "team:")
		if scope == "" {
			scope = "team"
		}
	}

	// Platform mode: use scope-aware flow resolution.
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
		var yamlContent string
		var resolvedScope string
		var err error

		// Explicit scope requested
		if scope == "team" && svc.Flows != nil {
			yamlContent, err = svc.Flows.GetFlow(r.Context(), name)
			resolvedScope = "team"
		} else if scope == "personal" && svc.PersonalFlows != nil {
			yamlContent, err = svc.PersonalFlows.GetFlow(r.Context(), name)
			resolvedScope = "personal"
		} else {
			// Default: try personal first, then team
			if svc.PersonalFlows != nil {
				yamlContent, err = svc.PersonalFlows.GetFlow(r.Context(), name)
				resolvedScope = "personal"
			}
			if err != nil && svc.Flows != nil {
				yamlContent, err = svc.Flows.GetFlow(r.Context(), name)
				resolvedScope = "team"
			}
		}

		if err != nil {
			respondError(w, http.StatusNotFound, "Agent not found")
			return
		}

		// Parse config from YAML content
		var cfg *config.AgentConfig
		tmpCfg, parseErr := config.LoadAgentFromBytes([]byte(yamlContent))
		if parseErr == nil {
			cfg = tmpCfg
		}

		respondJSON(w, http.StatusOK, AgentDetailResponse{
			Name:   name,
			Source: "system",
			Scope:  resolvedScope,
			YAML:   yamlContent,
			Config: cfg,
		})
		return
	}

	// Personal mode fallback: filesystem.
	path, source, err := findAgentPath(name)
	if err != nil {
		respondError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Read raw YAML content
	yamlData, err := os.ReadFile(path)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read agent file")
		return
	}

	// Parse config
	cfg, err := config.LoadAgent(path)
	if err != nil {
		// Return YAML even if parsing fails
		cfg = nil
	}

	response := AgentDetailResponse{
		Name:   name,
		Source: source,
		YAML:   string(yamlData),
		Config: cfg,
	}

	respondJSON(w, http.StatusOK, response)
}

// SaveAgentHandler handles PUT /api/agents/{name}
func SaveAgentHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	// Strip "team:" prefix used by the frontend for React key uniqueness.
	if strings.HasPrefix(name, "team:") {
		name = strings.TrimPrefix(name, "team:")
	}

	var request struct {
		YAML string `json:"yaml"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Parse the incoming YAML to analyze tools and resolve dependencies
	finalYAML := request.YAML
	var agentConfig config.AgentConfig
	if err := yaml.Unmarshal([]byte(request.YAML), &agentConfig); err == nil {
		mcpCfg := loadMCPConfigForRequest(r)
		cachedTools := GetCachedToolsForRequest(r)
		finalYAML = resolveAgentYAMLDependencies(request.YAML, agentConfig, mcpCfg, cachedTools)
	}

	// Platform mode: scope-aware save (personal by default, team with explicit scope).
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
		scope := r.URL.Query().Get("scope")
		if scope == "team" && svc.Flows != nil {
			// Writing to team scope requires team admin.
			if !RequireTeamAdmin(w, r) {
				return
			}
			if err := svc.Flows.SaveFlow(r.Context(), name, finalYAML); err != nil {
				respondError(w, http.StatusInternalServerError, "Failed to save agent: "+err.Error())
				return
			}
		} else if svc.PersonalFlows != nil {
			if err := svc.PersonalFlows.SaveFlow(r.Context(), name, finalYAML); err != nil {
				respondError(w, http.StatusInternalServerError, "Failed to save agent: "+err.Error())
				return
			}
		} else if svc.Flows != nil {
			// Fallback: no personal store, save to team
			if err := svc.Flows.SaveFlow(r.Context(), name, finalYAML); err != nil {
				respondError(w, http.StatusInternalServerError, "Failed to save agent: "+err.Error())
				return
			}
		} else {
			respondError(w, http.StatusServiceUnavailable, "No flow store available")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "yaml": finalYAML})
		return
	}

	// Personal mode fallback: filesystem.
	// Determine save path: check if flow already exists somewhere
	var path string
	existingPath, _, findErr := findAgentPath(name)
	if findErr == nil && existingPath != "" {
		// Flow exists - save to original location
		path = existingPath
	} else {
		// New flow - save to flows directory (~/.config/astonish/flows/)
		flowsDir, err := flowstore.GetFlowsDir()
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to get flows directory")
			return
		}
		path = filepath.Join(flowsDir, name+".yaml")

		// Ensure directory exists
		if err := os.MkdirAll(flowsDir, 0755); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to create flows directory")
			return
		}
	}

	// Write file
	if err := os.WriteFile(path, []byte(finalYAML), 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save agent file")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "path": path, "yaml": finalYAML})
}

// resolveAgentYAMLDependencies analyzes tools used in an agent config and
// resolves MCP dependencies, returning the final YAML with injected deps.
func resolveAgentYAMLDependencies(rawYAML string, agentConfig config.AgentConfig, mcpCfg *config.MCPConfig, cachedTools []ToolInfo) string {
	// Collect all tools used in the flow
	tools := CollectToolsFromNodes(agentConfig.Nodes)

	if len(tools) > 0 {
		// Get MCP store servers (from all taps)
		storeServers, storeErr := loadAllServersFromTaps()
		if storeErr != nil {
			slog.Warn("failed to load MCP store servers", "error", storeErr)
		}

		// Resolve dependencies
		deps := ResolveMCPDependencies(tools, cachedTools, storeServers, agentConfig.MCPDependencies, mcpCfg)
		deps = sanitizeMCPDependencies(deps)
		existingDeps := sanitizeMCPDependencies(agentConfig.MCPDependencies)

		// Check if mcp_dependencies section already exists
		hasMcpDeps := len(agentConfig.MCPDependencies) > 0

		// Add, update, or remove mcp_dependencies when the sanitized dependency
		// set differs. This also cleans stale built-in-tool deps from old YAML.
		if !mcpDependenciesEqual(deps, existingDeps) || (hasMcpDeps && len(deps) == 0) {
			// Parse as yaml.Node to preserve ordering
			var rootNode yaml.Node
			if err := yaml.Unmarshal([]byte(rawYAML), &rootNode); err == nil && rootNode.Kind == yaml.DocumentNode && len(rootNode.Content) > 0 {
				mapNode := rootNode.Content[0]
				if mapNode.Kind == yaml.MappingNode {
					// Find and remove existing mcp_dependencies, and find layout index
					var layoutIndex = -1
					var mcpDepsIndex = -1
					for i := 0; i < len(mapNode.Content); i += 2 {
						if mapNode.Content[i].Value == "mcp_dependencies" {
							mcpDepsIndex = i
						}
						if mapNode.Content[i].Value == "layout" {
							layoutIndex = i
						}
					}

					// Remove existing mcp_dependencies if present
					if mcpDepsIndex >= 0 {
						mapNode.Content = append(mapNode.Content[:mcpDepsIndex], mapNode.Content[mcpDepsIndex+2:]...)
						// Adjust layout index if it was after mcp_dependencies
						if layoutIndex > mcpDepsIndex {
							layoutIndex -= 2
						}
					}
					if len(deps) == 0 {
						var buf bytes.Buffer
						encoder := yaml.NewEncoder(&buf)
						encoder.SetIndent(2)
						if err := encoder.Encode(&rootNode); err == nil {
							return buf.String()
						}
						encoder.Close()
						return rawYAML
					}

					// Marshal deps to yaml.Node
					depsBytes, _ := yaml.Marshal(deps)
					var depsNode yaml.Node
					yaml.Unmarshal(depsBytes, &depsNode)

					// Create key node
					keyNode := &yaml.Node{
						Kind:  yaml.ScalarNode,
						Tag:   "!!str",
						Value: "mcp_dependencies",
					}

					// Insert before layout if layout exists, otherwise append
					if layoutIndex >= 0 {
						// Insert before layout
						newContent := make([]*yaml.Node, 0, len(mapNode.Content)+2)
						newContent = append(newContent, mapNode.Content[:layoutIndex]...)
						newContent = append(newContent, keyNode, depsNode.Content[0])
						newContent = append(newContent, mapNode.Content[layoutIndex:]...)
						mapNode.Content = newContent
					} else {
						// Append at end
						mapNode.Content = append(mapNode.Content, keyNode, depsNode.Content[0])
					}

					// Re-marshal with 2-space indentation to match frontend
					var buf bytes.Buffer
					encoder := yaml.NewEncoder(&buf)
					encoder.SetIndent(2)
					if err := encoder.Encode(&rootNode); err == nil {
						return buf.String()
					}
					encoder.Close()
				}
			}
		}
	} else {
		// No tools - remove mcp_dependencies if it exists
		var rootNode yaml.Node
		if err := yaml.Unmarshal([]byte(rawYAML), &rootNode); err == nil && rootNode.Kind == yaml.DocumentNode && len(rootNode.Content) > 0 {
			mapNode := rootNode.Content[0]
			if mapNode.Kind == yaml.MappingNode {
				// Find mcp_dependencies
				for i := 0; i < len(mapNode.Content); i += 2 {
					if mapNode.Content[i].Value == "mcp_dependencies" {
						// Remove it
						mapNode.Content = append(mapNode.Content[:i], mapNode.Content[i+2:]...)
						// Re-marshal
						var buf bytes.Buffer
						encoder := yaml.NewEncoder(&buf)
						encoder.SetIndent(2)
						if err := encoder.Encode(&rootNode); err == nil {
							return buf.String()
						}
						encoder.Close()
						break
					}
				}
			}
		}
	}

	return rawYAML
}

// DeleteAgentHandler handles DELETE /api/agents/{name}
func DeleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	// Strip "team:" prefix used by the frontend for React key uniqueness.
	if strings.HasPrefix(name, "team:") {
		name = strings.TrimPrefix(name, "team:")
	}

	// Platform mode: scope-aware delete (try personal first, then team).
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
		scope := r.URL.Query().Get("scope")
		var err error
		if scope == "team" && svc.Flows != nil {
			// Deleting from team scope requires team admin.
			if !RequireTeamAdmin(w, r) {
				return
			}
			err = svc.Flows.DeleteFlow(r.Context(), name)
		} else if scope == "personal" && svc.PersonalFlows != nil {
			err = svc.PersonalFlows.DeleteFlow(r.Context(), name)
		} else {
			// Default: try personal first, then team
			if svc.PersonalFlows != nil {
				err = svc.PersonalFlows.DeleteFlow(r.Context(), name)
			}
			if err != nil && svc.Flows != nil {
				err = svc.Flows.DeleteFlow(r.Context(), name)
			}
		}
		if err != nil {
			respondError(w, http.StatusNotFound, "Agent not found")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "deleted": name})
		return
	}

	// Personal mode fallback: filesystem.
	path, _, err := findAgentPath(name)
	if err != nil {
		respondError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Delete the file
	if err := os.Remove(path); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to delete agent file")
		return
	}

	// Remove the corresponding knowledge doc so the vector store
	// watcher picks up the deletion and cleans up indexed chunks.
	if memDir, err := config.GetMemoryDir(nil); err == nil {
		docPath := filepath.Join(memDir, "flows", name+".md")
		os.Remove(docPath) // best-effort — may not exist
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "deleted": name})
}

// CopyAgentToLocalHandler handles POST /api/agents/{name}/copy-to-local
// In personal mode: copies a store flow to the user's local directory for editing.
// In platform mode: flows in the database are already editable — this is a no-op.
func CopyAgentToLocalHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	// Platform mode: flows in the DB are already editable.
	if svc := store.FromRequest(r); svc != nil && svc.Flows != nil {
		// Extract the flow name from store:tap:name format
		flowName := name
		if strings.HasPrefix(name, "store:") {
			parts := strings.SplitN(name, ":", 3)
			if len(parts) == 3 {
				flowName = parts[2]
			}
		}

		// Verify the flow exists in the team's DB
		if _, err := svc.Flows.GetFlow(r.Context(), flowName); err != nil {
			respondError(w, http.StatusNotFound, "Flow not found in team database. Install it first.")
			return
		}

		respondJSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"newName": flowName,
			"message": "Flow is already editable in platform mode.",
		})
		return
	}

	// Personal mode: copy store flow to local filesystem for editing.
	sourcePath, source, err := findAgentPath(name)
	if err != nil {
		respondError(w, http.StatusNotFound, "Agent not found")
		return
	}

	if source != "store" {
		respondError(w, http.StatusBadRequest, "Agent is not from store, already editable")
		return
	}

	content, err := os.ReadFile(sourcePath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read agent file")
		return
	}

	// Determine destination name
	destName := name
	if strings.HasPrefix(name, "store:") {
		parts := strings.SplitN(name, ":", 3)
		if len(parts) == 3 {
			destName = parts[2]
		}
	} else if containsSlash(name) {
		parts := splitFirst(name, "/")
		if len(parts) == 2 {
			destName = parts[1]
		}
	}

	flowsDir, err := flowstore.GetFlowsDir()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to get flows directory")
		return
	}

	destPath := filepath.Join(flowsDir, destName+".yaml")

	if _, err := os.Stat(destPath); err == nil {
		respondError(w, http.StatusConflict, "Agent already exists locally: "+destName)
		return
	}

	if err := os.MkdirAll(flowsDir, 0755); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to create flows directory")
		return
	}

	if err := os.WriteFile(destPath, content, 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to copy agent file")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"newName": destName,
		"path":    destPath,
		"message": "Flow copied to local. You can now edit it.",
	})
}

// ToolInfo represents a tool in the list response
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "internal" or MCP server name
}

// ToolsListResponse is the response for GET /api/tools
type ToolsListResponse struct {
	Tools []ToolInfo `json:"tools"`
}

// ListToolsHandler handles GET /api/tools
func ListToolsHandler(w http.ResponseWriter, r *http.Request) {
	// Use request-scoped tools (platform-aware: reads from DB stores in platform mode)
	allTools := GetCachedToolsForRequest(r)

	// If cache not ready, return empty list
	if allTools == nil {
		allTools = []ToolInfo{}
	}

	// Sort tools by name
	sort.Slice(allTools, func(i, j int) bool {
		return allTools[i].Name < allTools[j].Name
	})

	respondJSON(w, http.StatusOK, ToolsListResponse{Tools: allTools})
}

// minimalReadonlyContext implements agent.ReadonlyContext for tool listing
type minimalReadonlyContext struct {
	context.Context
}

func (m *minimalReadonlyContext) AgentName() string                    { return "tools-api" }
func (m *minimalReadonlyContext) AppName() string                      { return "astonish" }
func (m *minimalReadonlyContext) UserContent() *genai.Content          { return nil }
func (m *minimalReadonlyContext) InvocationID() string                 { return "" }
func (m *minimalReadonlyContext) ReadonlyState() session.ReadonlyState { return nil }
func (m *minimalReadonlyContext) UserID() string                       { return "" }
func (m *minimalReadonlyContext) SessionID() string                    { return "" }
func (m *minimalReadonlyContext) Branch() string                       { return "" }

// MCPServerInfo represents an MCP server in the list response
type MCPServerInfo struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Command   string `json:"command,omitempty"`
	URL       string `json:"url,omitempty"`
	Enabled   bool   `json:"enabled"`
	// Source indicates where the server config originates: "config" for servers
	// declared in the local mcp_config.json file (read-only in the UI, they are
	// the platform base layer), or "" for database-backed org/team servers.
	Source string `json:"source,omitempty"`
}

// MCPServersListResponse is the response for GET /api/mcp/servers
type MCPServersListResponse struct {
	Servers []MCPServerInfo `json:"servers"`
}

// GetMCPServersHandler handles GET /api/mcp/servers
func GetMCPServersHandler(w http.ResponseWriter, r *http.Request) {
	// Platform mode: read from DB stores (org + team merged, team overrides org)
	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform {
		serverMap := make(map[string]MCPServerInfo) // name -> info, team overrides org

		// Config-file base (mcp_config.json). File-declared servers are the
		// platform base layer and appear in the list; org/team DB entries below
		// override them by name. Marked Source="config" so the UI can present
		// them as read-only defaults.
		for name, s := range config.FileMCPServers() {
			transport := s.Transport
			if transport == "" {
				transport = "stdio"
			}
			serverMap[name] = MCPServerInfo{
				Name:      name,
				Transport: transport,
				Command:   s.Command,
				URL:       s.URL,
				Enabled:   s.IsEnabled(),
				Source:    "config",
			}
		}

		// Org-level servers first
		if svc.MCPServers != nil {
			orgServers, err := svc.MCPServers.List(r.Context())
			if err == nil {
				for _, s := range orgServers {
					transport := s.Transport
					if transport == "" {
						transport = "stdio"
					}
					serverMap[s.Name] = MCPServerInfo{
						Name:      s.Name,
						Transport: transport,
						Command:   s.Command,
						URL:       s.URL,
						Enabled:   s.IsEnabled(),
					}
				}
			}
		}

		// Team-level servers override org
		if svc.TeamMCPServers != nil {
			teamServers, err := svc.TeamMCPServers.List(r.Context())
			if err == nil {
				for _, s := range teamServers {
					transport := s.Transport
					if transport == "" {
						transport = "stdio"
					}
					serverMap[s.Name] = MCPServerInfo{
						Name:      s.Name,
						Transport: transport,
						Command:   s.Command,
						URL:       s.URL,
						Enabled:   s.IsEnabled(),
					}
				}
			}
		}

		servers := make([]MCPServerInfo, 0, len(serverMap))
		for _, info := range serverMap {
			servers = append(servers, info)
		}
		sort.Slice(servers, func(i, j int) bool {
			return servers[i].Name < servers[j].Name
		})
		respondJSON(w, http.StatusOK, MCPServersListResponse{Servers: servers})
		return
	}

	respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
}

// UpdateMCPServerRequest is the request body for PATCH /api/mcp/servers/{name}
type UpdateMCPServerRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// UpdateMCPServerResponse is the response for PATCH /api/mcp/servers/{name}
type UpdateMCPServerResponse struct {
	Success bool          `json:"success"`
	Server  MCPServerInfo `json:"server"`
}

// UpdateMCPServerHandler handles PATCH /api/mcp/servers/{name}
func UpdateMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	// Team admins (or org admins) can toggle MCP servers.
	if !RequireTeamAdmin(w, r) {
		return
	}

	vars := mux.Vars(r)
	serverName := vars["name"]

	if serverName == "" {
		respondError(w, http.StatusBadRequest, "Server name is required")
		return
	}

	var req UpdateMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Platform mode: toggle in DB store
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		server, err := mcpStore.Get(r.Context(), serverName)
		if err != nil || server == nil {
			respondError(w, http.StatusNotFound, "Server not found")
			return
		}
		if req.Enabled != nil {
			server.Enabled = req.Enabled
			if err := mcpStore.Save(r.Context(), server); err != nil {
				respondError(w, http.StatusInternalServerError, "Failed to save MCP server")
				return
			}
			GetChatManager().Reset()
		}
		transport := server.Transport
		if transport == "" {
			transport = "stdio"
		}
		response := UpdateMCPServerResponse{
			Success: true,
			Server: MCPServerInfo{
				Name:      server.Name,
				Transport: transport,
				Command:   server.Command,
				URL:       server.URL,
				Enabled:   server.IsEnabled(),
			},
		}
		respondJSON(w, http.StatusOK, response)
		return
	}

	// Personal mode no longer supported
	respondError(w, http.StatusServiceUnavailable, "MCP server store not available")
}

// RegisterRoutes registers the API routes on a router.
//
// When svc is non-nil, every request flowing through these routes will have
// the Services instance injected into its context (via store.Middleware).
// Handlers can then retrieve it with store.FromRequest(r).
// Passing nil is safe — no middleware is applied and existing package-level
// globals continue to work unchanged. This keeps backward compatibility
// during the transition from global state to dependency injection.
//
// When pg is non-nil (platform mode), TenantMiddleware is also applied after
// store.Middleware, resolving per-request tenant stores based on the
// TenantContext set by PlatformAuthMiddleware.
// tenantMW is an optional middleware function for per-request tenant resolution.
func RegisterRoutes(router *mux.Router, svc *store.Services, backend store.PlatformBackend, tenantMW func(http.Handler) http.Handler) {
	// Register health endpoints BEFORE middleware (they must be auth-exempt and fast).
	router.HandleFunc("/api/healthz", HealthzHandler).Methods("GET")
	router.HandleFunc("/api/readyz", ReadyzHandler).Methods("GET")
	SetHealthBackend(backend)

	// Apply store middleware so every API handler can access Services via context.
	if svc != nil {
		router.Use(store.Middleware(svc))
	}

	// In platform mode, apply TenantMiddleware to resolve per-tenant stores.
	// This must run AFTER store.Middleware (which injects the base Services)
	// and AFTER PlatformAuthMiddleware (which sets TenantContext in the context).
	// PlatformAuthMiddleware is applied outside the router (wraps the entire handler),
	// so it always runs before router.Use() middleware.
	if tenantMW != nil {
		router.Use(tenantMW)
	}

	// Audit middleware logs API requests in platform mode.
	// Runs after auth and store middleware so it has access to the
	// authenticated user and tenant context.
	router.Use(AuditMiddleware)

	// Body size limiter to prevent oversized payloads (DoS mitigation).
	// Must run after auth (so 401 returns before reading the body) but before
	// route handlers that decode JSON request bodies.
	router.Use(MaxBodySizeMiddleware)

	router.HandleFunc("/api/agents", ListAgentsHandler).Methods("GET")
	router.HandleFunc("/api/agents/{name}", GetAgentHandler).Methods("GET")
	router.HandleFunc("/api/agents/{name}", SaveAgentHandler).Methods("PUT")
	router.HandleFunc("/api/agents/{name}", DeleteAgentHandler).Methods("DELETE")
	// Flow execution endpoint (headless with params, SSE streaming)
	router.HandleFunc("/api/agents/{name}/run", FlowRunHandler).Methods("POST")
	// Flow sharing endpoints (must be before wildcard copy-to-local route)
	router.HandleFunc("/api/agents/{name}/publish", FlowPublishToTeamHandler).Methods("POST")
	router.HandleFunc("/api/agents/{name}/fork", FlowForkToPersonalHandler).Methods("POST")
	router.HandleFunc("/api/agents/{name:.*}/copy-to-local", CopyAgentToLocalHandler).Methods("POST")
	router.HandleFunc("/api/tools", ListToolsHandler).Methods("GET")
	router.HandleFunc("/api/tools/web-capable", WebCapableToolsHandler).Methods("GET")
	router.HandleFunc("/api/ai/chat", AIChatHandler).Methods("POST")
	router.HandleFunc("/api/ai/classify-intent", IntentClassifyHandler).Methods("POST")
	router.HandleFunc("/api/ai/tool-search", AIToolSearchHandler).Methods("POST")
	router.HandleFunc("/api/ai/tool-search-internet", AIToolSearchInternetHandler).Methods("POST")
	router.HandleFunc("/api/ai/url-extract", URLExtractHandler).Methods("POST")
	router.HandleFunc("/api/mcp-internet-install", InternetMCPInstallHandler).Methods("POST")
	router.HandleFunc("/api/mcp-dependencies/check", CheckMCPDependenciesHandler).Methods("POST")

	// Tenant-scoped slide deck endpoints.
	router.HandleFunc("/api/docs", ListDocsHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/themes", ListSlidesThemesHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/components", ListSlidesComponentsHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/validate", ValidateSlidesHandler).Methods("POST")
	// Fixed paths MUST be registered before the parameterized {deckSlug} routes
	// so gorilla/mux does not treat 'publish'/'fork' as a deck slug.
	router.HandleFunc("/api/docs/slides/publish", SlidesPublishToTeamHandler).Methods("POST")
	router.HandleFunc("/api/docs/slides/fork", SlidesForkToPersonalHandler).Methods("POST")
	router.HandleFunc("/api/docs/slides/templates", ListSlidesTemplatesHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/import", ImportSlidesTemplateHandler).Methods("POST")
	// Fixed 'templates/' prefixed mutation routes MUST precede the {deckSlug}
	// wildcard so mux does not treat 'templates' as a deck slug.
	router.HandleFunc("/api/docs/slides/templates/{name}", DeleteSlidesTemplateHandler).Methods("DELETE")
	router.HandleFunc("/api/docs/slides/templates/{name}/duplicate", DuplicateSlidesTemplateHandler).Methods("POST")
	router.HandleFunc("/api/docs/slides/templates/{name}/recolor", RecolorSlidesTemplateHandler).Methods("PATCH")
	router.HandleFunc("/api/docs/slides/{deckSlug}", GetSlidesDeckHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/{deckSlug}", DeleteSlidesDeckHandler).Methods("DELETE")
	router.HandleFunc("/api/docs/slides/{deckSlug}/slides/{idx:[0-9]+}", GetSlideHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/{deckSlug}/present", PresentSlidesHandler).Methods("GET")
	router.HandleFunc("/api/docs/slides/{deckSlug}/export/html", ExportSlidesHTMLHandler).Methods("POST")
	router.HandleFunc("/api/docs/slides/{deckSlug}/export/pdf", ExportSlidesPDFHandler).Methods("POST")
	router.HandleFunc("/api/docs/slides/{deckSlug}/export/pptx", ExportSlidesPPTXHandler).Methods("POST")

	// Settings endpoints
	router.HandleFunc("/api/settings/config", GetSettingsHandler).Methods("GET")
	router.HandleFunc("/api/settings/config", UpdateSettingsHandler).Methods("PUT")
	router.HandleFunc("/api/settings/full", GetFullConfigHandler).Methods("GET")
	router.HandleFunc("/api/settings/full", UpdateFullConfigHandler).Methods("PUT")
	router.HandleFunc("/api/settings/mcp", GetMCPSettingsHandler).Methods("GET")
	router.HandleFunc("/api/settings/mcp", UpdateMCPSettingsHandler).Methods("PUT")

	router.HandleFunc("/api/user-settings/default-model", GetUserDefaultModelHandler).Methods("GET")
	router.HandleFunc("/api/user-settings/default-model", PatchUserDefaultModelHandler).Methods("PATCH")
	router.HandleFunc("/api/user-settings/brand-theme", GetUserBrandThemeHandler).Methods("GET")
	router.HandleFunc("/api/user-settings/brand-theme", PatchUserBrandThemeHandler).Methods("PATCH")
	// Public brand theme (login / pre-auth) — also listed in isAuthExemptPath.
	router.HandleFunc("/api/brand-theme", GetPublicBrandThemeHandler).Methods("GET")

	// Provider settings endpoints (cascading: platform → org → team)
	router.HandleFunc("/api/settings/platform/providers", GetPlatformProvidersHandler).Methods("GET")
	router.HandleFunc("/api/settings/platform/providers", SavePlatformProvidersHandler).Methods("PUT")
	router.HandleFunc("/api/settings/org/providers", GetOrgProvidersHandler).Methods("GET")
	router.HandleFunc("/api/settings/org/providers", SaveOrgProvidersHandler).Methods("PUT")
	router.HandleFunc("/api/settings/team/providers", GetTeamProvidersHandler).Methods("GET")
	router.HandleFunc("/api/settings/providers/effective", GetEffectiveProvidersHandler).Methods("GET")
	router.HandleFunc("/api/settings/providers/test", TestProviderHandler).Methods("POST")
	router.HandleFunc("/api/settings/{level}/providers/{name}", DeleteProviderHandler).Methods("DELETE")
	router.HandleFunc("/api/mcp/install-inline", InstallInlineMCPServerHandler).Methods("POST")
	router.HandleFunc("/api/mcp/status", MCPStatusHandler).Methods("GET")
	router.HandleFunc("/api/mcp/servers", GetMCPServersHandler).Methods("GET")
	router.HandleFunc("/api/mcp/servers/{name}", UpdateMCPServerHandler).Methods("PATCH")
	router.HandleFunc("/api/mcp/{serverName}/tools", ListServerToolsHandler).Methods("GET")
	router.HandleFunc("/api/mcp/{serverName}/tools/{toolName}/run", RunServerToolHandler).Methods("POST")
	router.HandleFunc("/api/mcp/{serverName}/network-grants", MCPNetworkGrantHandler).Methods("POST")
	router.HandleFunc("/api/mcp/{name}/refresh", RefreshMCPServerHandler).Methods("POST")
	router.HandleFunc("/api/settings/status", GetSetupStatusHandler).Methods("GET")
	router.HandleFunc("/api/version", GetVersionHandler).Methods("GET")

	// Platform setup endpoints (for deployment mode configuration)
	router.HandleFunc("/api/platform/init", PlatformInitHandler).Methods("POST")
	router.HandleFunc("/api/platform/init/sqlite", SQLitePlatformInitHandler).Methods("POST")
	router.HandleFunc("/api/platform/init/status", PlatformInitStatusHandler).Methods("GET")
	router.HandleFunc("/api/platform/mode", DeploymentModeHandler).Methods("GET")

	// Sandbox endpoints
	router.HandleFunc("/api/sandbox/status", SandboxStatusHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/details", SandboxDetailsHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/optional-tools", SandboxOptionalToolsHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/init", SandboxInitHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/containers", SandboxContainerListHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/containers/{id}", SandboxContainerDeleteHandler).Methods("DELETE")
	router.HandleFunc("/api/sandbox/containers/{id}/expose", SandboxListExposedPortsHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/containers/{id}/expose", SandboxExposePortHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/containers/{id}/expose/{port}", SandboxUnexposePortHandler).Methods("DELETE")
	router.HandleFunc("/api/sandbox/containers/{id}/pin", SandboxPinContainerHandler).Methods("POST")
	router.PathPrefix("/api/sandbox/proxy/{container}/{port}").HandlerFunc(SandboxProxyHandler)
	router.HandleFunc("/api/sandbox/prune", SandboxPruneHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/templates", SandboxTemplateListHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/templates", SandboxTemplateCreateHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/templates/{name}/snapshot", SandboxTemplateSnapshotHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/templates/{name}/promote", SandboxTemplatePromoteHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/templates/{name}", SandboxTemplateInfoHandler).Methods("GET")
	router.HandleFunc("/api/sandbox/templates/{name}", SandboxTemplateDeleteHandler).Methods("DELETE")
	router.HandleFunc("/api/sandbox/refresh", SandboxRefreshHandler).Methods("POST")
	router.HandleFunc("/api/sandbox/terminal", SandboxTerminalHandler).Methods("GET")

	// Team template endpoints (platform mode — per-team container customization)
	router.HandleFunc("/api/team/template/status", TeamTemplateStatusHandler).Methods("GET")
	router.HandleFunc("/api/team/template/create", TeamTemplateCreateHandler).Methods("POST")
	router.HandleFunc("/api/team/template/save", TeamTemplateSaveHandler).Methods("POST")
	router.HandleFunc("/api/team/template/restore", TeamTemplateRestoreHandler).Methods("POST")
	router.HandleFunc("/api/team/template/start", TeamTemplateStartHandler).Methods("POST")
	router.HandleFunc("/api/team/template/packages", TeamTemplatePackagesHandler).Methods("POST")
	router.HandleFunc("/api/team/template/image", TeamTemplateImageHandler).Methods("POST")
	router.HandleFunc("/api/team/template/build", TeamImageBuildHandler).Methods("POST")
	router.HandleFunc("/api/team/template/build/status", TeamImageBuildStatusHandler).Methods("GET")
	router.HandleFunc("/api/team/template/dockerfile", TeamDockerfileGetHandler).Methods("GET")
	router.HandleFunc("/api/team/template/dockerfile", TeamDockerfileSaveHandler).Methods("PUT")
	router.HandleFunc("/api/team/template", TeamTemplateDeleteHandler).Methods("DELETE")

	// Standard servers endpoints
	router.HandleFunc("/api/standard-servers", ListStandardServersHandler).Methods("GET")
	router.HandleFunc("/api/standard-servers/{id}/install", InstallStandardServerHandler).Methods("POST")
	router.HandleFunc("/api/standard-servers/{id}/activate", ActivateStandardServerHandler).Methods("POST")
	router.HandleFunc("/api/standard-servers/{id}", UninstallStandardServerHandler).Methods("DELETE")
	router.HandleFunc("/api/web-search/perplexity/options", GetPerplexityWebSearchOptionsHandler).Methods("GET")
	router.HandleFunc("/api/web-search/perplexity/config", SavePerplexityWebSearchConfigHandler).Methods("PUT")
	router.HandleFunc("/api/web-search/perplexity/config", ClearPerplexityWebSearchConfigHandler).Methods("DELETE")

	// Skills endpoints
	router.HandleFunc("/api/skills", ListSkillsHandler).Methods("GET")
	router.HandleFunc("/api/skills", CreateSkillHandler).Methods("POST")
	router.HandleFunc("/api/skills/install", InstallSkillHandler).Methods("POST")
	router.HandleFunc("/api/skills/{name}/content", GetSkillContentHandler).Methods("GET")
	router.HandleFunc("/api/skills/{name}/content", UpdateSkillContentHandler).Methods("PUT")
	router.HandleFunc("/api/skills/{name}/files", ListSkillFilesHandler).Methods("GET")
	router.HandleFunc("/api/skills/{name}/file", GetSkillFileHandler).Methods("GET")
	router.HandleFunc("/api/skills/{name}/file", SaveSkillFileHandler).Methods("PUT")
	router.HandleFunc("/api/skills/{name}/file", DeleteSkillFileHandler).Methods("DELETE")
	router.HandleFunc("/api/skills/{name}/validate", ValidateSkillHandler).Methods("POST")
	router.HandleFunc("/api/skills/{name}/acknowledge", AcknowledgeSkillHandler).Methods("POST")
	router.HandleFunc("/api/skills/{name}/force-status", ForceSkillStatusHandler).Methods("POST")
	router.HandleFunc("/api/skills/{name}", DeleteSkillHandler).Methods("DELETE")

	// MCP Platform endpoints (multi-tenant MCP server management)
	router.HandleFunc("/api/mcp-platform/servers", ListMCPPlatformServersHandler).Methods("GET")
	router.HandleFunc("/api/mcp-platform/servers", CreateMCPPlatformServerHandler).Methods("POST")
	router.HandleFunc("/api/mcp-platform/servers/{name}", GetMCPPlatformServerHandler).Methods("GET")
	router.HandleFunc("/api/mcp-platform/servers/{name}", UpdateMCPPlatformServerHandler).Methods("PUT")
	router.HandleFunc("/api/mcp-platform/servers/{name}", DeleteMCPPlatformServerHandler).Methods("DELETE")
	router.HandleFunc("/api/mcp-platform/servers/{name}", ToggleMCPPlatformServerHandler).Methods("PATCH")
	router.HandleFunc("/api/mcp-platform/servers/{name}/refresh", RefreshMCPPlatformServerHandler).Methods("POST")

	// A2A Client endpoints (external agent connections)
	router.HandleFunc("/api/a2a-agents", ListA2AAgentsHandler).Methods("GET")
	router.HandleFunc("/api/a2a-agents", CreateA2AAgentHandler).Methods("POST")
	router.HandleFunc("/api/a2a-agents/{name}", GetA2AAgentHandler).Methods("GET")
	router.HandleFunc("/api/a2a-agents/{name}", UpdateA2AAgentHandler).Methods("PUT")
	router.HandleFunc("/api/a2a-agents/{name}", DeleteA2AAgentHandler).Methods("DELETE")
	router.HandleFunc("/api/a2a-agents/{name}", ToggleA2AAgentHandler).Methods("PATCH")
	router.HandleFunc("/api/a2a-agents/{name}/refresh", RefreshA2AAgentHandler).Methods("POST")
	router.HandleFunc("/api/a2a-agents/{name}/test", TestA2AAgentHandler).Methods("POST")

	// Network policy settings endpoints (multi-tier allow/deny rules)
	router.HandleFunc("/api/network-policies", ListNetworkPoliciesHandler).Methods("GET")
	router.HandleFunc("/api/network-policies", CreateNetworkPolicyHandler).Methods("POST")
	router.HandleFunc("/api/network-policies/{id}", UpdateNetworkPolicyHandler).Methods("PUT")
	router.HandleFunc("/api/network-policies/{id}", DeleteNetworkPolicyHandler).Methods("DELETE")

	// Credentials endpoints (master-key routes before {name} to avoid mux conflict)
	router.HandleFunc("/api/credentials", ListCredentialsHandler).Methods("GET")
	router.HandleFunc("/api/credentials", SaveCredentialHandler).Methods("POST")
	router.HandleFunc("/api/credentials/master-key", SetMasterKeyHandler).Methods("POST")
	router.HandleFunc("/api/credentials/verify-master-key", VerifyMasterKeyHandler).Methods("POST")
	router.HandleFunc("/api/credentials/publish", PublishCredentialHandler).Methods("POST")
	router.HandleFunc("/api/credentials/fork", ForkCredentialHandler).Methods("POST")
	router.HandleFunc("/api/credentials/{name}", GetCredentialHandler).Methods("GET")
	router.HandleFunc("/api/credentials/{name}", DeleteCredentialHandler).Methods("DELETE")
	router.HandleFunc("/api/secrets/{key:.+}", GetSecretHandler).Methods("GET")
	router.HandleFunc("/api/secrets/{key:.+}", SaveSecretHandler).Methods("PUT")

	// Provider endpoints
	router.HandleFunc("/api/providers/{providerId}/models", ListProviderModelsHandler).Methods("GET")
	router.HandleFunc("/api/providers/{providerId}/models-metadata", ListProviderModelsWithMetadataHandler).Methods("GET")

	// MCP Store endpoints
	router.HandleFunc("/api/mcp-store", ListMCPStoreHandler).Methods("GET")
	router.HandleFunc("/api/mcp-store/tags", GetMCPStoreTagsHandler).Methods("GET")
	router.HandleFunc("/api/mcp-store/{id:.*}/install", InstallMCPStoreServerHandler).Methods("POST")
	router.HandleFunc("/api/mcp-store/{id:.*}", GetMCPStoreServerHandler).Methods("GET")

	// Unified Taps endpoints (new)
	router.HandleFunc("/api/taps", ListTapsHandler).Methods("GET")
	router.HandleFunc("/api/taps", AddTapHandler).Methods("POST")
	router.HandleFunc("/api/taps/{name}", RemoveTapHandler).Methods("DELETE")
	router.HandleFunc("/api/taps/update", UpdateFlowStoreHandler).Methods("POST")

	// Flow Store endpoints
	router.HandleFunc("/api/flow-store", ListFlowStoreHandler).Methods("GET")
	router.HandleFunc("/api/flow-store/update", UpdateFlowStoreHandler).Methods("POST")
	router.HandleFunc("/api/flow-store/taps", ListTapsHandler).Methods("GET")
	router.HandleFunc("/api/flow-store/taps", AddTapHandler).Methods("POST")
	router.HandleFunc("/api/flow-store/taps/{name}", RemoveTapHandler).Methods("DELETE")
	router.HandleFunc("/api/flow-store/{tap}/{flow}/install", InstallFlowHandler).Methods("POST")
	router.HandleFunc("/api/flow-store/{tap}/{flow}", UninstallFlowHandler).Methods("DELETE")

	// Execution endpoints
	router.HandleFunc("/api/chat", HandleChat).Methods("POST")
	router.HandleFunc("/api/session/{id}/stop", HandleStopSession).Methods("POST")
	router.HandleFunc("/api/session/{id}/keepalive", HandleSessionKeepalive).Methods("POST")

	// A2A Protocol endpoints (Agent Card, JSON-RPC, admin)
	RegisterA2ARoutes(router)

	// Channels endpoints
	router.HandleFunc("/api/channels/status", ChannelsStatusHandler).Methods("GET")
	router.HandleFunc("/api/channels/reload", ChannelsReloadHandler).Methods("POST")
	router.HandleFunc("/api/slack/events", SlackEventsHandler).Methods("POST")
	router.HandleFunc("/api/slack/commands", SlackSlashCommandHandler).Methods("POST")

	// Browser VNC proxy endpoints (KasmVNC in container)
	router.HandleFunc("/api/browser/vnc-info/{container}", BrowserVNCInfoHandler).Methods("GET")
	router.HandleFunc("/api/browser/vnc/{container}/{path:.*}", BrowserVNCProxyHandler)
	router.HandleFunc("/api/browser/handoff-done", BrowserHandoffDoneHandler).Methods("POST")

	// Scheduler endpoints
	router.HandleFunc("/api/scheduler/jobs", SchedulerJobsHandler).Methods("GET", "POST")
	router.HandleFunc("/api/scheduler/jobs/publish", SchedulerJobPublishHandler).Methods("POST")
	router.HandleFunc("/api/scheduler/jobs/fork", SchedulerJobForkHandler).Methods("POST")
	router.HandleFunc("/api/scheduler/jobs/{id}/run", SchedulerJobRunHandler).Methods("POST")
	router.HandleFunc("/api/scheduler/jobs/{id}", SchedulerJobHandler).Methods("GET", "PUT", "DELETE")

	// App Preview sandbox (generative UI iframe)
	router.HandleFunc("/api/app-preview/sandbox-full", AppPreviewSandboxFullHandler).Methods("GET")
	router.HandleFunc("/api/app-preview/sandbox", AppPreviewSandboxHandler).Methods("GET")
	router.HandleFunc("/api/app-preview/runtime.js", AppPreviewRuntimeHandler).Methods("GET")
	router.HandleFunc("/api/app-preview/tailwind.js", AppPreviewTailwindHandler).Methods("GET")

	// Visual Apps CRUD (generative UI persistence)
	// App data proxy endpoints (must be before /api/apps/{name} to avoid capture)
	router.HandleFunc("/api/apps/data", AppDataHandler).Methods("POST")
	router.HandleFunc("/api/apps/action", AppActionHandler).Methods("POST")
	router.HandleFunc("/api/apps/ai", AppAIHandler).Methods("POST")
	router.HandleFunc("/api/apps/state/query", AppStateQueryHandler).Methods("POST")
	router.HandleFunc("/api/apps/state/exec", AppStateExecHandler).Methods("POST")

	// App sharing endpoints (must be before /api/apps/{name} to avoid capture)
	router.HandleFunc("/api/apps/publish", AppPublishToTeamHandler).Methods("POST")
	router.HandleFunc("/api/apps/fork", AppForkToPersonalHandler).Methods("POST")
	router.HandleFunc("/api/apps/promote", AppPromoteToOrgHandler).Methods("POST")
	router.HandleFunc("/api/apps/org", ListOrgAppsHandler).Methods("GET")
	router.HandleFunc("/api/apps/org/{name}", DeleteOrgAppHandler).Methods("DELETE")

	router.HandleFunc("/api/apps", ListAppsHandler).Methods("GET")
	router.HandleFunc("/api/apps/{name}", GetAppHandler).Methods("GET")
	router.HandleFunc("/api/apps/{name}", SaveAppHandler).Methods("PUT")
	router.HandleFunc("/api/apps/{name}", DeleteAppHandler).Methods("DELETE")
	router.HandleFunc("/api/apps/{name}/model", PatchAppModelHandler).Methods("PATCH")
	router.HandleFunc("/api/apps/{name}/stream", AppStreamHandler).Methods("GET")

	// Studio Chat endpoints
	router.HandleFunc("/api/studio/chat", StudioChatHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions", StudioSessionsHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}", StudioSessionHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}", StudioDeleteSessionHandler).Methods("DELETE")
	router.HandleFunc("/api/studio/sessions/{id}/trace", StudioSessionTraceHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}/subtask-events", StudioSubtaskEventsHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}/stop", StudioStopHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions/{id}/stream", StudioChatStreamHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}/status", StudioChatStatusHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}/model-status", GetSessionModelStatusHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}/model", PatchSessionModelHandler).Methods("PATCH")

	// Network grant approval endpoints (dynamic network policy)
	router.HandleFunc("/api/studio/sessions/{id}/network-grants/approve", NetworkGrantApproveHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions/{id}/network-grants/approve-broader", NetworkGrantApproveBroaderHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions/{id}/network-grants/deny", NetworkGrantDenyHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions/{id}/network-denials", NetworkDenialCheckHandler).Methods("GET")

	router.HandleFunc("/api/studio/artifacts", StudioArtifactDownloadHandler).Methods("GET")
	router.HandleFunc("/api/studio/artifacts/content", StudioArtifactContentHandler).Methods("GET")
	router.HandleFunc("/api/studio/artifacts/pdf", StudioArtifactPDFHandler).Methods("GET")

	// Fleet endpoints
	router.HandleFunc("/api/fleets", ListFleetsHandler).Methods("GET")
	router.HandleFunc("/api/fleets/{key}", GetFleetHandler).Methods("GET")
	router.HandleFunc("/api/fleets/{key}", SaveFleetHandler).Methods("PUT")
	router.HandleFunc("/api/fleets/{key}", DeleteFleetHandler).Methods("DELETE")
	router.HandleFunc("/api/fleets/{key}/clone", CloneFleetHandler).Methods("POST")

	// Fleet Plan endpoints
	router.HandleFunc("/api/fleet-plans", ListFleetPlansHandler).Methods("GET")
	router.HandleFunc("/api/fleet-plans/{key}", GetFleetPlanHandler).Methods("GET")
	router.HandleFunc("/api/fleet-plans/{key}", SaveFleetPlanHandler).Methods("PUT")
	router.HandleFunc("/api/fleet-plans/{key}", DeleteFleetPlanHandler).Methods("DELETE")
	router.HandleFunc("/api/fleet-plans/{key}/agents/{agent_key}", PatchFleetPlanAgentHandler).Methods("PATCH")
	router.HandleFunc("/api/fleet-plans/{key}/activate", ActivateFleetPlanHandler).Methods("POST")
	router.HandleFunc("/api/fleet-plans/{key}/deactivate", DeactivateFleetPlanHandler).Methods("POST")
	router.HandleFunc("/api/fleet-plans/{key}/status", FleetPlanStatusHandler).Methods("GET")
	router.HandleFunc("/api/fleet-plans/{key}/retry/{issueNumber}", RetryFleetIssueHandler).Methods("POST")
	router.HandleFunc("/api/fleet-plans/{key}/duplicate", DuplicateFleetPlanHandler).Methods("POST")
	router.HandleFunc("/api/fleet-plans/{key}/yaml", GetFleetPlanYAMLHandler).Methods("GET")
	router.HandleFunc("/api/fleet-plans/{key}/yaml", SaveFleetPlanYAMLHandler).Methods("PUT")

	router.HandleFunc("/api/fleet-setup-profiles", ListFleetSetupProfilesHandler).Methods("GET")
	router.HandleFunc("/api/fleet-setup-profiles/{key}", GetFleetSetupProfileHandler).Methods("GET")
	router.HandleFunc("/api/fleet-setup-profiles/{key}", SaveFleetSetupProfileHandler).Methods("PUT")
	router.HandleFunc("/api/fleet-setup-profiles/{key}", DeleteFleetSetupProfileHandler).Methods("DELETE")
	router.HandleFunc("/api/fleet-setup-profiles/{key}/clone", CloneFleetSetupProfileHandler).Methods("POST")
	router.HandleFunc("/api/fleet-setup-profiles/{key}/yaml", GetFleetSetupProfileYAMLHandler).Methods("GET")
	router.HandleFunc("/api/fleet-setup-profiles/{key}/yaml", SaveFleetSetupProfileYAMLHandler).Methods("PUT")
	router.HandleFunc("/api/fleet-setup-profiles/{key}/steps/{stepId}", GetFleetSetupProfileStepHandler).Methods("GET")
	router.HandleFunc("/api/fleet-setup/tool-catalog", GetFleetSetupToolCatalogHandler).Methods("GET")
	router.HandleFunc("/api/fleet-setup/drafts", CreateFleetSetupDraftHandler).Methods("POST")
	router.HandleFunc("/api/fleet-setup/drafts/{id}", GetFleetSetupDraftHandler).Methods("GET")
	router.HandleFunc("/api/fleet-setup/drafts/{id}", PatchFleetSetupDraftHandler).Methods("PATCH")
	router.HandleFunc("/api/fleet-setup/drafts/{id}/validate-step", ValidateFleetSetupStepHandler).Methods("POST")
	router.HandleFunc("/api/fleet-setup/drafts/{id}/finalize", FinalizeFleetSetupDraftHandler).Methods("POST")

	// Fleet Session endpoints (fleet v2: autonomous agent team)
	router.HandleFunc("/api/studio/fleet/start", FleetStartHandler).Methods("POST")
	router.HandleFunc("/api/studio/fleet/sessions", FleetSessionsListHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/history", FleetSessionsHistoryHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}", FleetSessionStatusHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/message", FleetMessageHandler).Methods("POST")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/stop", FleetSessionStopHandler).Methods("POST")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/stream", FleetSessionStreamHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/trace", FleetSessionTraceHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/threads", FleetSessionThreadsHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/tasks", FleetSessionTasksHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/mailbox/{recipient}", FleetSessionMailboxHandler).Methods("GET")
	router.HandleFunc("/api/studio/fleet/sessions/{id}/messages", FleetSessionMessagesHandler).Methods("GET")

	// Drill endpoints
	router.HandleFunc("/api/drills", ListDrillSuitesHandler).Methods("GET")
	router.HandleFunc("/api/drills/{suite}", GetDrillSuiteHandler).Methods("GET")
	router.HandleFunc("/api/drills/{suite}", DeleteDrillSuiteHandler).Methods("DELETE")
	router.HandleFunc("/api/drills/{suite}/yaml", GetSuiteYAMLHandler).Methods("GET")
	router.HandleFunc("/api/drills/{suite}/yaml", SaveSuiteYAMLHandler).Methods("PUT")
	router.HandleFunc("/api/drills/{suite}/drills/{name}", GetDrillHandler).Methods("GET")
	router.HandleFunc("/api/drills/{suite}/drills/{name}", DeleteDrillHandler).Methods("DELETE")
	router.HandleFunc("/api/drills/{suite}/drills/{name}/yaml", GetDrillYAMLHandler).Methods("GET")
	router.HandleFunc("/api/drills/{suite}/drills/{name}/yaml", SaveDrillYAMLHandler).Methods("PUT")
	router.HandleFunc("/api/drill-reports", ListDrillReportsHandler).Methods("GET")
	router.HandleFunc("/api/drill-reports/{suite}", GetDrillReportHandler).Methods("GET")

	// Memory sharing endpoints (platform mode)
	router.HandleFunc("/api/memories/map", MemoryMapHandler).Methods("GET")
	router.HandleFunc("/api/memories/health", MemoryHealthHandler).Methods("GET")
	router.HandleFunc("/api/memories/consolidate/preview", MemoryConsolidationPreviewHandler).Methods("POST")
	router.HandleFunc("/api/memories/consolidate/apply", MemoryConsolidationApplyHandler).Methods("POST")
	router.HandleFunc("/api/memories/search", MemorySearchCrossTierHandler).Methods("POST")
	router.HandleFunc("/api/memories/team", MemoryShareToTeamHandler).Methods("POST")
	router.HandleFunc("/api/memories/team", MemoryListTeamHandler).Methods("GET")
	router.HandleFunc("/api/memories/team/{id}", MemoryDeleteTeamHandler).Methods("DELETE")
	router.HandleFunc("/api/memories/personal", MemorySavePersonalHandler).Methods("POST")
	router.HandleFunc("/api/memories/personal", MemoryListPersonalHandler).Methods("GET")
	router.HandleFunc("/api/memories/personal/{id}", MemoryDeletePersonalHandler).Methods("DELETE")
	router.HandleFunc("/api/memories/org", MemorySaveOrgHandler).Methods("POST")
	router.HandleFunc("/api/memories/org", MemoryListOrgHandler).Methods("GET")
	router.HandleFunc("/api/memories/org/{id}", MemoryDeleteOrgHandler).Methods("DELETE")
	router.HandleFunc("/api/memories/promote", MemoryPromoteToOrgHandler).Methods("POST")
	router.HandleFunc("/api/memories/promote-to-team", MemoryPromotePersonalToTeamHandler).Methods("POST")
	router.HandleFunc("/api/memories/{scope}/{id}", MemoryUpdateHandler).Methods("PUT")
	router.HandleFunc("/api/memories/session/{id}", MemoryListBySessionHandler).Methods("GET")
	router.HandleFunc("/api/memories/session/{id}/extract", MemoryExtractHandler).Methods("POST")

	// Audit log query (admin-only, platform mode)
	router.HandleFunc("/api/audit", AuditQueryHandler).Methods("GET")

	// Platform admin endpoints (superadmin only, platform mode)
	// These are NOT in the auth bypass list — PlatformAuthMiddleware runs first,
	// and each handler additionally verifies platform_role == "superadmin".
	if backend != nil {
		SetPlatformBackend(backend)
	}
	if getPlatformBackend() != nil {
		// User channel management (any authenticated user manages their own)
		router.HandleFunc("/api/user/channels", handleListUserChannels).Methods("GET")
		router.HandleFunc("/api/user/channels", handleLinkUserChannel).Methods("POST")
		router.HandleFunc("/api/user/channels/link-code", handleGenerateLinkCode).Methods("POST")
		router.HandleFunc("/api/user/channels/verify-email-code", handleVerifyEmailCode).Methods("POST")
		router.HandleFunc("/api/user/channels/{id}", handleUpdateUserChannel).Methods("PATCH")
		router.HandleFunc("/api/user/channels/{id}", handleUnlinkUserChannel).Methods("DELETE")
		router.HandleFunc("/api/user/channels/{id}/verify", handleVerifyUserChannel).Methods("POST")

		// Channel info (any authenticated user can see bot info)
		router.HandleFunc("/api/channels/info", handleGetChannelInfo).Methods("GET")

		router.HandleFunc("/api/platform/admin/orgs", PlatformAdminListOrgsHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/orgs", PlatformAdminCreateOrgHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/orgs/{slug}", PlatformAdminGetOrgHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/orgs/{slug}", PlatformAdminUpdateOrgHandler).Methods("PATCH")
		router.HandleFunc("/api/platform/admin/orgs/{slug}", PlatformAdminDeleteOrgHandler).Methods("DELETE")
		router.HandleFunc("/api/platform/admin/orgs/{slug}/teams", PlatformAdminCreateTeamHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/orgs/{slug}/teams/{teamSlug}", PlatformAdminDeleteTeamHandler).Methods("DELETE")
		router.HandleFunc("/api/platform/admin/orgs/{slug}/teams/{teamSlug}/members", PlatformAdminListTeamMembersHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/orgs/{slug}/teams/{teamSlug}/members", PlatformAdminAddTeamMemberHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/orgs/{slug}/teams/{teamSlug}/members/{userID}", PlatformAdminRemoveTeamMemberHandler).Methods("DELETE")
		router.HandleFunc("/api/platform/admin/orgs/{slug}/teams/{teamSlug}/members/{userID}/role", PlatformAdminSetTeamMemberRoleHandler).Methods("PUT")
		router.HandleFunc("/api/platform/admin/users", PlatformAdminListUsersHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/users", PlatformAdminCreateUserHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/users/{id}", PlatformAdminGetUserHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/users/{id}", PlatformAdminUpdateUserHandler).Methods("PATCH")
		router.HandleFunc("/api/platform/admin/users/{id}", PlatformAdminDeleteUserHandler).Methods("DELETE")
		router.HandleFunc("/api/platform/admin/users/{id}/orgs", PlatformAdminAddUserToOrgHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/users/{id}/orgs/{slug}", PlatformAdminRemoveUserFromOrgHandler).Methods("DELETE")

		// OIDC provider management (superadmin only)
		router.HandleFunc("/api/platform/admin/oidc-providers", PlatformAdminListOIDCProvidersHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/oidc-providers", PlatformAdminCreateOIDCProviderHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/oidc-providers/{id}", PlatformAdminGetOIDCProviderHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/oidc-providers/{id}", PlatformAdminUpdateOIDCProviderHandler).Methods("PATCH")
		router.HandleFunc("/api/platform/admin/oidc-providers/{id}", PlatformAdminDeleteOIDCProviderHandler).Methods("DELETE")

		// Auth policy settings (superadmin only)
		router.HandleFunc("/api/platform/admin/auth-settings", PlatformAdminGetAuthSettingsHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/auth-settings", PlatformAdminSaveAuthSettingsHandler).Methods("PUT")
		router.HandleFunc("/api/platform/admin/brand-theme", GetPlatformBrandThemeHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/brand-theme", PatchPlatformBrandThemeHandler).Methods("PATCH")

		// Channel adapter management (superadmin only)
		router.HandleFunc("/api/platform/admin/channels", PlatformAdminListChannelsHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/channels/email/test", PlatformAdminTestEmailHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/channels/{type}", PlatformAdminSaveChannelHandler).Methods("PUT")
		router.HandleFunc("/api/platform/admin/channels/{type}", PlatformAdminDeleteChannelHandler).Methods("DELETE")

		// Standard MCP servers / web services management (superadmin only)
		router.HandleFunc("/api/platform/admin/web-services", PlatformAdminListWebServicesHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/web-services/{id}", PlatformAdminSetWebServiceKeyHandler).Methods("PUT")
		router.HandleFunc("/api/platform/admin/web-services/{id}", PlatformAdminDeleteWebServiceHandler).Methods("DELETE")

		// Base sandbox configuration (superadmin only)
		router.HandleFunc("/api/platform/admin/sandbox/base", PlatformBaseConfigGetHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/sandbox/base/status", PlatformBaseConfigStatusHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/sandbox/base/configure", PlatformBaseConfigBuildHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/sandbox/base/image", PlatformBaseImageHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/sandbox/base/build", PlatformImageBuildHandler).Methods("POST")
		router.HandleFunc("/api/platform/admin/sandbox/base/build/status", PlatformImageBuildStatusHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/sandbox/base/dockerfile", PlatformDockerfileGetHandler).Methods("GET")
		router.HandleFunc("/api/platform/admin/sandbox/base/dockerfile", PlatformDockerfileSaveHandler).Methods("PUT")
		router.HandleFunc("/api/platform/admin/sandbox/base/tools", PlatformBaseConfigOptionalToolsHandler).Methods("GET")

		// OpenShell orphan sandbox management
		router.HandleFunc("/api/platform/admin/sandbox/orphans", PlatformAdminDeleteOrphanSandboxesHandler).Methods("DELETE")
		router.HandleFunc("/api/platform/admin/sandbox/orphans", PlatformAdminListOrphanSandboxesHandler).Methods("GET")
	}
}
