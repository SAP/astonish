package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// MCPPath is Astonish's protected Streamable HTTP MCP endpoint.
	MCPPath             = "/api/mcp"
	mcpToolExecuteScope = string(execution.CapabilityToolExecute)
)

// BearerPrincipalValidator is implemented by the Astonish OAuth server. Keeping
// this narrow boundary means foreign IdP tokens must first pass through an
// explicit federation adapter rather than being accepted at the MCP endpoint.
type BearerPrincipalValidator interface {
	ValidateBearer(context.Context, string, string, []string, execution.Surface) (execution.Principal, error)
}

// MCPTenantResolver translates OAuth's immutable organization and team IDs into
// the canonical slugs required by tenant-scoped stores.
type MCPTenantResolver func(context.Context, string, string) (orgSlug, teamSlug string, err error)

// RegisterMCPRoutes mounts the protected, stateless Streamable HTTP MCP server.
// tenantMW is applied after bearer validation, because only a validated canonical
// principal may choose the organization and team used for scoped stores.
func RegisterMCPRoutes(router *mux.Router, validator BearerPrincipalValidator, resolveTenant MCPTenantResolver, tenantMW func(http.Handler) http.Handler) {
	if router == nil || validator == nil {
		return
	}

	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, ok := execution.PrincipalFromContext(r.Context())
		if !ok {
			return nil
		}
		return newMCPServer(principal)
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          1 << 20,
		PropagateRequestCancellation: true,
	})

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
		ctx, err := execution.WithPrincipal(r.Context(), principal)
		if err != nil {
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

		// OAuth clients are bound to opaque platform IDs. Resolve those IDs before
		// handing the request to the slug-based tenant router. Service clients do
		// not own a personal schema, so use the client ID as their stable owner.
		userID := principal.Subject
		if userID == "" {
			userID = principal.ClientID
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{
			OrgSlug:  orgSlug,
			TeamSlug: teamSlug,
			UserID:   userID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newMCPServer(principal execution.Principal) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "astonish", Version: "1.0"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "astonish_identity",
		Title:       "Astonish identity",
		Description: "Returns the authenticated Astonish principal for this MCP request.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		identity, err := json.Marshal(map[string]any{
			"kind":      principal.Kind,
			"subject":   principal.Subject,
			"client_id": principal.ClientID,
			"actor":     principal.Actor,
			"issuer":    principal.Issuer,
			"org_slug":  principal.OrgSlug,
			"team_slug": principal.TeamSlug,
			"surface":   principal.Surface,
			"scopes":    principal.Scopes,
		})
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(identity)}}}, nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "astonish_chat",
		Title:       "Astonish chat",
		Description: "Runs a conversational request through Astonish using the authenticated principal.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "The request to send to Astonish."},
			},
			"required": []string{"message"},
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return mcpToolError("invalid astonish_chat arguments"), nil
		}
		args.Message = strings.TrimSpace(args.Message)
		if args.Message == "" {
			return mcpToolError("message is required"), nil
		}
		if err := (execution.CapabilityAuthorizer{}).Authorize(principal, execution.CapabilityChat); err != nil {
			return mcpToolError(err.Error()), nil
		}
		return runMCPChat(ctx, args.Message)
	})
	return server
}

func runMCPChat(ctx context.Context, message string) (*mcp.CallToolResult, error) {
	request := httptest.NewRequest(http.MethodPost, "/api/studio/chat", strings.NewReader(mustJSON(StudioChatRequest{Message: message}))).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	StudioChatHandler(recorder, request)
	if recorder.Code >= http.StatusBadRequest {
		return mcpToolError(strings.TrimSpace(recorder.Body.String())), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: recorder.Body.String()}}}, nil
}

func mcpToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, IsError: true}
}

func mustJSON(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}
