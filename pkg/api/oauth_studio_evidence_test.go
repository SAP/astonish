package api

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type studioEvidenceValidator struct{ studioOAuthValidatorStub }

func (v studioEvidenceValidator) ValidateBearerWithEvidence(ctx context.Context, token, audience string, scopes []string, surface execution.Surface) (execution.Principal, execution.AuthenticationEvidence, error) {
	p, err := v.ValidateBearer(ctx, token, audience, scopes, surface)
	return p, execution.AuthenticationEvidence{Method: execution.AuthMethodOAuth, ExpiresAt: time.Now().Add(time.Minute)}, err
}
func TestOAuthStudioEvidenceChatOnlyCannotExecuteTools(t *testing.T) {
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: "u", OrgSlug: "o", TeamSlug: "t", Scopes: []string{"chat"}, Authenticated: true}
	called := false
	h := OAuthStudioBearerMiddleware(studioEvidenceValidator{studioOAuthValidatorStub{principal: p}}, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if _, ok := execution.AuthenticationEvidenceFromContext(r.Context()); !ok {
			t.Fatal("verified OAuth evidence missing")
		}
		principal, _ := execution.PrincipalFromContext(r.Context())
		if err := (execution.CapabilityAuthorizer{}).Authorize(principal, execution.CapabilityToolExecute); err == nil {
			t.Fatal("chat-only token gained tool execution")
		}
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest("POST", "/api/studio/bots/id/messages", nil)
	req.Header.Set("Authorization", "Bearer verified-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != 204 {
		t.Fatalf("status %d", rec.Code)
	}
}
