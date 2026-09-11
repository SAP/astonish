package api

import (
	"context"
	"encoding/json"
	"net/http"

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

// RegisterMCPRoutes mounts the protected, stateless Streamable HTTP MCP server.
// tenantMW is applied after bearer validation, because only a validated canonical
// principal may choose the organization and team used for scoped stores.
func RegisterMCPRoutes(router *mux.Router, validator BearerPrincipalValidator, tenantMW func(http.Handler) http.Handler) {
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
	router.Handle(MCPPath, mcpBearerMiddleware(validator, handler)).Methods(http.MethodPost, http.MethodGet, http.MethodDelete)
}

func mcpBearerMiddleware(validator BearerPrincipalValidator, next http.Handler) http.Handler {
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

		// RegisterRoutes applies tenant middleware to this endpoint as well. Give
		// it the tenant selected by the validated token, never request input.
		userID := principal.Subject
		if userID == "" {
			userID = principal.ClientID
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{
			OrgSlug:  principal.OrgSlug,
			TeamSlug: principal.TeamSlug,
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
	return server
}
