package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestStudioChatDebugRequiresSuperadmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/studio/chat", strings.NewReader(`{"message":"hello","debug":true}`))
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "user"}))
	w := httptest.NewRecorder()

	StudioChatHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestStudioChatRequiresExecutionPrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/studio/chat", strings.NewReader(`{"message":"hello"}`))
	w := httptest.NewRecorder()

	StudioChatHandler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestStudioChatRejectsServicePrincipalPersonalMemory(t *testing.T) {
	principal := execution.Principal{
		Kind:           execution.PrincipalKindService,
		Authentication: execution.AuthMethodOAuth,
		Surface:        execution.SurfaceMCP,
		ClientID:       "service-client",
		OrgSlug:        "org",
		TeamSlug:       "team",
		Scopes:         []string{string(execution.CapabilityChat)},
		Authenticated:  true,
	}
	ctx, err := execution.WithPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/studio/chat", strings.NewReader(`{"message":"hello","memoryScope":"personal"}`)).WithContext(ctx)
	w := httptest.NewRecorder()

	StudioChatHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "service principals cannot access personal memory") {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestChatRunnerDebugContext(t *testing.T) {
	runner := newChatRunner("session", "user", studioChatAppName, true)
	if store.DebugEnabledFromContext(runner.ctx) {
		t.Fatal("debug unexpectedly enabled")
	}
	runner.ctx = store.WithDebugEnabled(runner.ctx, true)
	if !store.DebugEnabledFromContext(runner.ctx) {
		t.Fatal("debug was not injected")
	}
}

func TestChatRunnerCacheDiagnosticRecorder(t *testing.T) {
	sessionStore := &diagnosticSessionStore{}
	runner := newChatRunner("session", "user", studioChatAppName, true)
	runner.ctx = store.WithCacheDiagnosticRecorder(runner.ctx, func(ctx context.Context, diagnostic store.CacheDiagnostic) error {
		return sessionStore.AppendCacheDiagnostic(ctx, runner.SessionID, diagnostic)
	})
	recorder := store.CacheDiagnosticRecorderFromContext(runner.ctx)
	if recorder == nil {
		t.Fatal("diagnostic recorder was not injected")
	}
	if err := recorder(context.Background(), store.CacheDiagnostic{Call: 2}); err != nil {
		t.Fatalf("record diagnostic: %v", err)
	}
	if sessionStore.sessionID != "session" || sessionStore.diagnostic.Call != 2 {
		t.Fatalf("recorded = %q %#v", sessionStore.sessionID, sessionStore.diagnostic)
	}
}

type diagnosticSessionStore struct {
	store.SessionStore
	sessionID  string
	diagnostic store.CacheDiagnostic
}

func (s *diagnosticSessionStore) AppendCacheDiagnostic(_ context.Context, sessionID string, diagnostic store.CacheDiagnostic) error {
	if strings.TrimSpace(sessionID) == "" {
		return context.Canceled
	}
	s.sessionID = sessionID
	s.diagnostic = diagnostic
	return nil
}
