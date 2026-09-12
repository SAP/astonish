package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
)

type studioOAuthValidatorStub struct {
	principal execution.Principal
	err       error
}

func (s studioOAuthValidatorStub) ValidateBearer(_ context.Context, _ string, _ string, scopes []string, surface execution.Surface) (execution.Principal, error) {
	if s.err != nil {
		return execution.Principal{}, s.err
	}
	if surface != execution.SurfaceStudio || len(scopes) != 1 || scopes[0] != string(execution.CapabilityChat) {
		return execution.Principal{}, errors.New("unexpected Studio OAuth validation request")
	}
	return s.principal, nil
}

func TestOAuthStudioBearerMiddlewareInstallsScopedStudioPrincipal(t *testing.T) {
	principal := execution.Principal{
		Kind:           execution.PrincipalKindUser,
		Authentication: execution.AuthMethodOAuth,
		Surface:        execution.SurfaceStudio,
		Subject:        "user-1",
		OrgSlug:        "org-id",
		TeamSlug:       "team-id",
		Scopes:         []string{string(execution.CapabilityChat)},
		Authenticated:  true,
	}
	called := false
	handler := OAuthStudioBearerMiddleware(studioOAuthValidatorStub{principal: principal}, func(_ context.Context, orgID, teamID string) (string, string, error) {
		if orgID != "org-id" || teamID != "team-id" {
			t.Fatalf("tenant IDs = %q, %q", orgID, teamID)
		}
		return "acme", "platform", nil
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got, ok := execution.PrincipalFromContext(r.Context())
		if !ok || got.OrgSlug != "acme" || got.TeamSlug != "platform" {
			t.Fatalf("principal = %#v, present=%v", got, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/studio/sessions", nil)
	req.Header.Set("Authorization", "Bearer astonish-oauth-token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)

	if !called || result.Code != http.StatusNoContent {
		t.Fatalf("called=%v status=%d body=%s", called, result.Code, result.Body.String())
	}
}

func TestOAuthStudioBearerMiddlewareRejectsInvalidToken(t *testing.T) {
	handler := OAuthStudioBearerMiddleware(studioOAuthValidatorStub{err: errors.New("invalid token")}, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("next handler must not be reached")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/studio/sessions", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)

	if result.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	if got := result.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate header")
	}
}
