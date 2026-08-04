package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/gorilla/mux"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
)

// mcpManagerForRequest creates an MCP Manager from the DB store (platform mode)
// or from the filesystem config (personal mode). This ensures the inspector
// can find servers regardless of where they were saved.
func mcpManagerForRequest(r *http.Request, serverName string) (*mcp.Manager, config.MCPServerConfig, error) {
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		srv, err := mcpStore.Get(r.Context(), serverName)
		if err != nil || srv == nil {
			return nil, config.MCPServerConfig{}, fmt.Errorf("server '%s' not found in config", serverName)
		}
		serverCfg := mcpServerToConfig(srv)
		cfg := &config.MCPConfig{
			MCPServers: map[string]config.MCPServerConfig{
				serverName: serverCfg,
			},
		}
		mgr := mcp.NewManagerFromConfig(cfg)
		if resolver := credentialResolverForRequest(r); resolver != nil {
			mgr.SetCredentialResolver(resolver)
		}
		return mgr, serverCfg, nil
	}
	return nil, config.MCPServerConfig{}, fmt.Errorf("MCP server store not available")
}

func mcpServerConfigForRequest(r *http.Request, serverName string) (config.MCPServerConfig, bool) {
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		srv, err := mcpStore.Get(r.Context(), serverName)
		if err == nil && srv != nil {
			return mcpServerToConfig(srv), true
		}
	}
	return config.MCPServerConfig{}, false
}

func logMCPInspectorFailure(message, serverName string, serverCfg config.MCPServerConfig, err error, stderr *bytes.Buffer, extra ...any) {
	attrs := mcp.FailureLogAttrs(serverName, serverCfg, err, stderr)
	var diagErr *mcpSandboxDiagnosticsError
	if errors.As(err, &diagErr) && diagErr.stdout != "" {
		attrs = append(attrs, "stdout", diagErr.stdout)
	}
	attrs = append(attrs, extra...)
	slog.Warn(message, attrs...)
}

func mcpServerUsesSandbox(serverCfg config.MCPServerConfig) bool {
	transport := strings.ToLower(strings.TrimSpace(serverCfg.Transport))
	return transport != "sse" && transport != "streamable-http"
}

func mcpInspectorUsesSandbox(serverCfg config.MCPServerConfig) bool {
	return mcpServerUsesSandbox(serverCfg)
}

func listMCPToolsForInspector(ctx context.Context, r *http.Request, serverName string, serverCfg config.MCPServerConfig) ([]ToolSchema, error) {
	if mcpInspectorUsesSandbox(serverCfg) {
		// Attach network policy + request credential store so env placeholders resolve.
		discoveryCtx := withRuntimeNetworkPolicyContext(ctx, r, effectiveAppConfig(r))
		data, err := discoverMCPToolsInSandbox(discoveryCtx, serverName, serverCfg, buildPGSessionRegistry(r.Context()))
		if err != nil {
			return nil, err
		}
		return decodeMCPToolSchemas(data)
	}

	transport := &mcpsdk.SSEClientTransport{Endpoint: serverCfg.URL}
	toolset, err := mcptoolset.New(mcptoolset.Config{Transport: transport})
	if err != nil {
		return nil, err
	}
	minimalCtx := &minimalReadonlyContext{Context: ctx}
	toolsList, err := toolset.Tools(minimalCtx)
	if err != nil {
		return nil, err
	}
	return toolSchemasFromTools(toolsList), nil
}

func decodeMCPToolSchemas(data json.RawMessage) ([]ToolSchema, error) {
	var entries []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse MCP tool discovery result: %w", err)
	}

	tools := make([]ToolSchema, 0, len(entries))
	for _, entry := range entries {
		schema := ToolSchema{
			Name:        entry.Name,
			Description: entry.Description,
		}
		if len(entry.InputSchema) > 0 {
			var params any
			if err := json.Unmarshal(entry.InputSchema, &params); err != nil {
				return nil, fmt.Errorf("failed to parse MCP tool schema for %q: %w", entry.Name, err)
			}
			schema.Parameters = params
		}
		tools = append(tools, schema)
	}
	return tools, nil
}

func toolSchemasFromTools(toolsList []tool.Tool) []ToolSchema {
	tools := make([]ToolSchema, 0, len(toolsList))
	for _, t := range toolsList {
		schema := ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
		}

		if declTool, ok := t.(common.ToolWithDeclaration); ok {
			decl := declTool.Declaration()
			if decl != nil && decl.ParametersJsonSchema != nil {
				if genaiSchema, ok := decl.ParametersJsonSchema.(*genai.Schema); ok {
					schema.Parameters = convertGenaiSchemaToMap(genaiSchema)
				} else if mapSchema, ok := decl.ParametersJsonSchema.(map[string]interface{}); ok {
					schema.Parameters = mapSchema
				} else {
					schema.Parameters = decl.ParametersJsonSchema
				}
			}
		}

		tools = append(tools, schema)
	}
	return tools
}

// ToolSchema represents a tool's parameter schema
type ToolSchema struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// ListServerToolsResponse is the response for GET /api/mcp/{serverName}/tools
type ListServerToolsResponse struct {
	Tools                []ToolSchema             `json:"tools"`
	Error                string                   `json:"error,omitempty"`
	NetworkAuthorization *MCPNetworkAuthorization `json:"network_authorization,omitempty"`
}

// ToolRunRequest is the request for POST /api/mcp/{serverName}/tools/{toolName}/run
type ToolRunRequest struct {
	Params map[string]interface{} `json:"params"`
}

// ToolRunResponse is the response for POST /api/mcp/{serverName}/tools/{toolName}/run
type ToolRunResponse struct {
	Success              bool                     `json:"success"`
	Result               interface{}              `json:"result,omitempty"`
	Error                string                   `json:"error,omitempty"`
	TimeTaken            string                   `json:"time_taken"`
	NetworkAuthorization *MCPNetworkAuthorization `json:"network_authorization,omitempty"`
}

// ListServerToolsHandler handles GET /api/mcp/{serverName}/tools
// Lists all tools available from a specific MCP server
func ListServerToolsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]

	ctx, cancel := context.WithTimeout(r.Context(), mcpDiscoveryTimeout)
	defer cancel()

	_, serverCfg, err := mcpManagerForRequest(r, serverName)
	if err != nil {
		if cfg, ok := mcpServerConfigForRequest(r, serverName); ok {
			logMCPInspectorFailure("MCP inspector failed to load server tools", serverName, cfg, err, nil, "phase", "load_config")
		} else {
			slog.Warn("MCP inspector failed to load server tools", "component", "mcp", "server", serverName, "phase", "load_config", "error", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ListServerToolsResponse{
			Error: fmt.Sprintf("Failed to load tools, %v", err),
		})
		return
	}

	tools, err := listMCPToolsForInspector(ctx, r, serverName, serverCfg)
	if err != nil {
		logMCPInspectorFailure("MCP inspector failed to list tools", serverName, serverCfg, err, nil, "phase", "list_tools", "sandbox", mcpInspectorUsesSandbox(serverCfg))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ListServerToolsResponse{
			Error:                fmt.Sprintf("Failed to list tools: %v", err),
			NetworkAuthorization: networkAuthorizationFromMCPError(err, serverCfg),
		})
		return
	}

	// Persist discovered tools so Studio chat can inject them. Chat only loads
	// MCP tools from cached_tools / the file tools cache — listing in the
	// inspector used to discover tools live without writing them anywhere,
	// which left search_tools with no mcp:* groups.
	if len(tools) > 0 {
		persistMCPToolsFromInspector(r, serverName, serverCfg, tools)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ListServerToolsResponse{
		Tools: tools,
	})
}

// persistMCPToolsFromInspector writes tool declarations discovered by the
// inspector Test/list endpoint into the DB (platform) and/or file cache
// (personal), matching the shape expected by parseCachedToolsJSON / cache.ToolEntry.
func persistMCPToolsFromInspector(r *http.Request, serverName string, serverCfg config.MCPServerConfig, tools []ToolSchema) {
	type cacheShape struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema,omitempty"`
	}
	entries := make([]cacheShape, 0, len(tools))
	fileEntries := make([]cache.ToolEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, cacheShape{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
		var schemaJSON json.RawMessage
		if t.Parameters != nil {
			if b, err := json.Marshal(t.Parameters); err == nil {
				schemaJSON = b
			}
		}
		fileEntries = append(fileEntries, cache.ToolEntry{
			Name:        t.Name,
			Description: t.Description,
			Source:      serverName,
			InputSchema: schemaJSON,
		})
	}

	// File tools cache (personal mode + platform fallback).
	checksum := cache.ComputeServerChecksum(serverCfg.Command, serverCfg.Args, serverCfg.Env)
	cache.AddServerTools(serverName, fileEntries, checksum)
	if err := cache.SaveCache(); err != nil {
		slog.Warn("MCP inspector: failed to save file tools cache", "server", serverName, "error", err)
	}

	// DB cached_tools column (platform/team/org scopes).
	if mcpStore := effectiveMCPStore(r); mcpStore != nil {
		data, err := json.Marshal(entries)
		if err != nil {
			slog.Warn("MCP inspector: failed to marshal tools for cache", "server", serverName, "error", err)
			return
		}
		if err := mcpStore.UpdateCachedTools(r.Context(), serverName, data); err != nil {
			slog.Warn("MCP inspector: failed to update cached_tools", "server", serverName, "error", err)
			return
		}
		slog.Info("MCP inspector: cached tools for chat", "server", serverName, "count", len(tools))
	}
}

// RunServerToolHandler handles POST /api/mcp/{serverName}/tools/{toolName}/run
// Executes a specific tool with provided parameters
func RunServerToolHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverName := vars["serverName"]
	toolName := vars["toolName"]

	// Parse request body
	var req ToolRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ToolRunResponse{
			Success: false,
			Error:   fmt.Sprintf("Invalid request body: %v", err),
		})
		return
	}

	// Set a timeout for tool execution
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	startTime := time.Now()

	_, serverCfg, err := mcpManagerForRequest(r, serverName)
	if err != nil {
		if cfg, ok := mcpServerConfigForRequest(r, serverName); ok {
			logMCPInspectorFailure("MCP inspector failed to load tool runner", serverName, cfg, err, nil, "phase", "load_config", "tool", toolName)
		} else {
			slog.Warn("MCP inspector failed to load tool runner", "component", "mcp", "server", serverName, "phase", "load_config", "tool", toolName, "error", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ToolRunResponse{
			Success:   false,
			Error:     fmt.Sprintf("Failed to load tools, %v", err),
			TimeTaken: time.Since(startTime).String(),
		})
		return
	}

	var result any
	if mcpInspectorUsesSandbox(serverCfg) {
		result, err = invokeMCPToolInContainer(r.WithContext(ctx), effectiveUserID(r), serverName, toolName, serverCfg, req.Params)
	} else {
		result, err = invokeMCPToolSSE(ctx, serverName, toolName, serverCfg, req.Params)
	}
	if err != nil {
		logMCPInspectorFailure("MCP inspector failed to run tool", serverName, serverCfg, err, nil, "phase", "run_tool", "tool", toolName, "sandbox", mcpInspectorUsesSandbox(serverCfg))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ToolRunResponse{
			Success:              false,
			Error:                err.Error(),
			TimeTaken:            time.Since(startTime).String(),
			NetworkAuthorization: networkAuthorizationFromMCPError(err, serverCfg),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ToolRunResponse{
		Success:   true,
		Result:    result,
		TimeTaken: time.Since(startTime).String(),
	})
}

// convertGenaiSchemaToMap converts a genai.Schema to a JSON-serializable map
func convertGenaiSchemaToMap(schema *genai.Schema) map[string]interface{} {
	if schema == nil {
		return nil
	}

	result := make(map[string]interface{})

	if schema.Type != "" {
		result["type"] = string(schema.Type)
	}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}

	// Convert properties
	if len(schema.Properties) > 0 {
		props := make(map[string]interface{})
		for name, propSchema := range schema.Properties {
			props[name] = convertGenaiSchemaToMap(propSchema)
		}
		result["properties"] = props
	}

	// Handle enum values
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}

	// Handle items for arrays
	if schema.Items != nil {
		result["items"] = convertGenaiSchemaToMap(schema.Items)
	}

	return result
}
