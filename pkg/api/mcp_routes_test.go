package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

type mcpValidatorStub struct {
	principal execution.Principal
	err       error
	resource  string
	scopes    []string
	surface   execution.Surface
}

func (s *mcpValidatorStub) ValidateBearer(_ context.Context, _ string, resource string, scopes []string, surface execution.Surface) (execution.Principal, error) {
	s.resource = resource
	s.scopes = append([]string(nil), scopes...)
	s.surface = surface
	return s.principal, s.err
}

func TestMCPRoutesRejectsUnauthenticatedRequest(t *testing.T) {
	router := mux.NewRouter()
	RegisterMCPRoutes(router, &mcpValidatorStub{err: context.Canceled}, nil)

	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want Bearer challenge", got)
	}
}

func TestMCPRoutesRejectsPrincipalWithoutExecutionScope(t *testing.T) {
	validator := &mcpValidatorStub{principal: execution.Principal{
		Kind:           execution.PrincipalKindUser,
		Authentication: execution.AuthMethodOAuth,
		Surface:        execution.SurfaceMCP,
		Subject:        "user-1",
		Issuer:         "https://issuer.example",
		OrgSlug:        "org",
		TeamSlug:       "team",
		Scopes:         []string{string(execution.CapabilityChat)},
		Authenticated:  true,
	}}
	router := mux.NewRouter()
	RegisterMCPRoutes(router, validator, nil)

	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestMCPRoutesPassesValidatedTenantToTenantMiddleware(t *testing.T) {
	validator := &mcpValidatorStub{principal: execution.Principal{
		Kind:           execution.PrincipalKindUser,
		Authentication: execution.AuthMethodOAuth,
		Surface:        execution.SurfaceMCP,
		Subject:        "user-1",
		Issuer:         "https://issuer.example",
		OrgSlug:        "org-a",
		TeamSlug:       "team-a",
		Scopes:         []string{string(execution.CapabilityToolExecute)},
		Authenticated:  true,
	}}
	var gotOrg, gotTeam, gotUser string
	tenantMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant := store.TenantContextFrom(r.Context())
			if tenant != nil {
				gotOrg, gotTeam, gotUser = tenant.OrgSlug, tenant.TeamSlug, tenant.UserID
			}
			next.ServeHTTP(w, r)
		})
	}
	router := mux.NewRouter()
	RegisterMCPRoutes(router, validator, tenantMW)

	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer valid")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	router.ServeHTTP(httptest.NewRecorder(), req)

	if gotOrg != "org-a" || gotTeam != "team-a" || gotUser != "user-1" {
		t.Fatalf("tenant middleware got org=%q team=%q user=%q", gotOrg, gotTeam, gotUser)
	}
}

func TestMCPRoutesInitializesForAuthorizedPrincipal(t *testing.T) {
	validator := &mcpValidatorStub{principal: execution.Principal{
		Kind:           execution.PrincipalKindUser,
		Authentication: execution.AuthMethodOAuth,
		Surface:        execution.SurfaceMCP,
		Subject:        "user-1",
		ClientID:       "client-1",
		Issuer:         "https://issuer.example",
		OrgSlug:        "org",
		TeamSlug:       "team",
		Scopes:         []string{string(execution.CapabilityToolExecute)},
		Authenticated:  true,
	}}
	router := mux.NewRouter()
	RegisterMCPRoutes(router, validator, nil)

	req := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer valid")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"serverInfo":{"name":"astonish"`) {
		t.Fatalf("initialize response does not identify Astonish MCP server: %s", rec.Body.String())
	}
	if validator.surface != execution.SurfaceMCP || validator.resource != "" || len(validator.scopes) != 1 || validator.scopes[0] != mcpToolExecuteScope {
		t.Fatalf("validator received resource=%q scopes=%v surface=%q", validator.resource, validator.scopes, validator.surface)
	}
}
