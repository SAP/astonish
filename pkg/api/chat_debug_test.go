package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestChatRunnerDebugContext(t *testing.T) {
	runner := newChatRunner("session", "user", true)
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
	runner := newChatRunner("session", "user", true)
	runner.ctx = store.WithCacheDiagnosticRecorder(runner.ctx, func(ctx context.Context, diagnostic store.CacheDiagnostic) error {
		return sessionStore.AppendCacheDiagnostic(ctx, runner.SessionID, diagnostic)
	})
	recorder := store.CacheDiagnosticRecorderFromContext(runner.ctx)
	if recorder == nil {
		t.Fatal("diagnostic recorder was not injected")
	}
	if err := recorder(context.Background(), store.CacheDiagnostic{Round: 2}); err != nil {
		t.Fatalf("record diagnostic: %v", err)
	}
	if sessionStore.sessionID != "session" || sessionStore.diagnostic.Round != 2 {
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
