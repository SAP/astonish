package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	MCPPath             = "/api/mcp"
	mcpToolExecuteScope = string(execution.CapabilityToolExecute)
)

type BearerPrincipalValidator interface {
	ValidateBearer(context.Context, string, string, []string, execution.Surface) (execution.Principal, error)
}

type MCPTenantResolver func(context.Context, string, string) (orgSlug, teamSlug string, err error)

// RegisterMCPRoutes mounts the protected Streamable HTTP MCP server. The public
// contract deliberately contains only the four client-driven orchestration tools.
func RegisterMCPRoutes(router *mux.Router, validator BearerPrincipalValidator, resolveTenant MCPTenantResolver, tenantMW func(http.Handler) http.Handler) {
	if router == nil || validator == nil {
		return
	}
	tokens := newMCPContextTokens()
	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, ok := execution.PrincipalFromContext(r.Context())
		if !ok {
			return nil
		}
		return newMCPServer(r, principal, tokens)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, PropagateRequestCancellation: true})
	handler := http.Handler(streamable)
	if tenantMW != nil {
		handler = tenantMW(handler)
	}
	router.Handle(MCPPath, mcpBearerMiddleware(validator, resolveTenant, handler)).Methods(http.MethodPost, http.MethodGet, http.MethodDelete)
}

func mcpBearerMiddleware(validator BearerPrincipalValidator, resolveTenant MCPTenantResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := validator.ValidateBearer(r.Context(), r.Header.Get("Authorization"), "", []string{mcpToolExecuteScope}, execution.SurfaceMCP)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "MCP authentication required", http.StatusUnauthorized)
			return
		}
		if err := (execution.CapabilityAuthorizer{}).Authorize(principal, execution.CapabilityToolExecute); err != nil {
			http.Error(w, "MCP principal is not authorized", http.StatusForbidden)
			return
		}
		orgSlug, teamSlug := principal.OrgSlug, principal.TeamSlug
		if resolveTenant != nil {
			orgSlug, teamSlug, err = resolveTenant(r.Context(), principal.OrgSlug, principal.TeamSlug)
			if err != nil {
				http.Error(w, "MCP tenant is unavailable", http.StatusForbidden)
				return
			}
		}
		principal.OrgSlug, principal.TeamSlug = orgSlug, teamSlug
		ctx, err := execution.WithPrincipal(r.Context(), principal)
		if err != nil {
			http.Error(w, "MCP principal is not authorized", http.StatusForbidden)
			return
		}
		userID := principal.Subject
		if userID == "" {
			userID = principal.ClientID
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{OrgSlug: orgSlug, TeamSlug: teamSlug, UserID: userID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newMCPServer(request *http.Request, principal execution.Principal, tokens *mcpContextTokens) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "astonish", Version: "1.0"}, nil)
	add := func(name, description string, schema map[string]any, handler func(context.Context, json.RawMessage) (*mcp.CallToolResult, error)) {
		server.AddTool(&mcp.Tool{Name: name, Title: name, Description: description, InputSchema: schema}, func(ctx context.Context, call *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handler(ctx, call.Params.Arguments)
		})
	}
	add("get_agent_context", "MANDATORY at the start of every user turn. Returns the authenticated Astonish instructions, relevant memory, tenant-scoped tool catalog, and a token required for discovery and execution.", mcpObjectSchema(map[string]any{"session_id": map[string]any{"type": "string"}, "user_message": map[string]any{"type": "string"}}, "session_id", "user_message"), func(_ context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		var args struct {
			SessionID   string `json:"session_id"`
			UserMessage string `json:"user_message"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.SessionID) == "" || strings.TrimSpace(args.UserMessage) == "" {
			return mcpToolJSON(map[string]any{"state": "invalid_arguments", "error": "session_id and user_message are required"}), nil
		}
		runtime, err := prepareMCPProgressiveRuntime(request, principal, args.SessionID)
		if err != nil {
			return mcpToolJSON(map[string]any{"state": agent.ProgressiveStateExecutionError, "error": err.Error()}), nil
		}
		turnContext, memories, memoryState := runtime.contextForTurn(args.UserMessage)
		token, err := tokens.issue(principal, runtime)
		if err != nil {
			return nil, err
		}
		return mcpToolJSON(map[string]any{
			"state": agent.ProgressiveStateReady, "instructions": runtime.prompt.BuildExternal(), "turn_context": turnContext,
			"relevant_memory": memories, "memory_state": memoryState, "tools": runtime.catalogList,
			"context_version": "v1", "context_token": token,
		}), nil
	})
	validate := func(raw json.RawMessage) (mcpContextToken, *mcp.CallToolResult) {
		var args struct {
			Token string `json:"context_token"`
		}
		if json.Unmarshal(raw, &args) != nil || strings.TrimSpace(args.Token) == "" {
			return mcpContextToken{}, mcpToolJSON(map[string]any{"state": agent.ProgressiveStateContextRequired, "error": "call get_agent_context first"})
		}
		entry, ok := tokens.validate(args.Token, principal)
		if !ok {
			return mcpContextToken{}, mcpToolJSON(map[string]any{"state": agent.ProgressiveStateContextStale, "error": "context token is invalid, expired, or belongs to another principal"})
		}
		return entry, nil
	}
	add("search_tools", "Search the authenticated context catalog. Call get_agent_context first, then use its context token.", mcpObjectSchema(map[string]any{"context_token": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"}, "max_results": map[string]any{"type": "integer"}}, "context_token", "query"), func(_ context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		entry, failure := validate(raw)
		if failure != nil {
			return failure, nil
		}
		var args struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Query) == "" {
			return mcpToolJSON(map[string]any{"state": "invalid_arguments", "error": "query is required"}), nil
		}
		return mcpToolJSON(map[string]any{"state": agent.ProgressiveStateReady, "tools": entry.runtime.search(args.Query, args.MaxResults)}), nil
	})
	add("describe_tools", "Describe exact schemas from the authenticated context catalog. Call get_agent_context first, then describe a searched tool before execution.", mcpObjectSchema(map[string]any{"context_token": map[string]any{"type": "string"}, "names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "context_token", "names"), func(_ context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		entry, failure := validate(raw)
		if failure != nil {
			return failure, nil
		}
		var args struct {
			Names []string `json:"names"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || len(args.Names) == 0 {
			return mcpToolJSON(map[string]any{"state": "invalid_arguments", "error": "names is required"}), nil
		}
		described, unavailable := entry.runtime.describe(args.Names)
		return mcpToolJSON(map[string]any{"state": agent.ProgressiveStateReady, "tools": described, "unavailable": unavailable}), nil
	})
	add("execute_tool", "Execute exactly one catalog tool using the protected Astonish runtime. Call get_agent_context and describe_tools first.", mcpObjectSchema(map[string]any{"context_token": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "context_token", "name", "arguments"), func(_ context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
		entry, failure := validate(raw)
		if failure != nil {
			return failure, nil
		}
		var args struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Name) == "" {
			return mcpToolJSON(map[string]any{"state": "invalid_arguments", "error": "name is required"}), nil
		}
		if entry.runtime.catalog.GetToolEntry(args.Name) == nil {
			return mcpToolJSON(map[string]any{"state": "tool_unavailable", "tool": args.Name}), nil
		}
		return mcpToolJSON(entry.runtime.execute(args.Name, args.Arguments)), nil
	})
	return server
}

func mcpObjectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}
func mcpToolJSON(value any) *mcp.CallToolResult {
	body, err := json.Marshal(value)
	if err != nil {
		return mcpToolError(err.Error())
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}
}
func mcpToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, IsError: true}
}
