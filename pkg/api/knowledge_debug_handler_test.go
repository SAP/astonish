package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStudioKnowledgeDebugRequiresSuperadmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/studio/sessions/session/knowledge-debug?invocationId=inv", nil)
	w := httptest.NewRecorder()
	StudioKnowledgeDebugHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestStudioKnowledgeDebugRequiresInvocationId(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/studio/sessions/session/knowledge-debug", nil)
	req = req.WithContext(WithPlatformUser(req.Context(), &PlatformUser{ID: "admin", PlatformRole: "superadmin"}))
	w := httptest.NewRecorder()
	StudioKnowledgeDebugHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestPlatformAdminKnowledgeDebugRequiresSuperadmin(t *testing.T) {
	tests := []struct {
		name string
		user *PlatformUser
		want int
	}{
		{name: "unauthenticated", want: http.StatusUnauthorized},
		{name: "regular user", user: &PlatformUser{ID: "user", PlatformRole: ""}, want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/platform/admin/sessions/session/knowledge-debug?org=acme&scope=personal&user=user&invocationId=inv", nil)
			if tt.user != nil {
				req = req.WithContext(WithPlatformUser(req.Context(), tt.user))
			}
			w := httptest.NewRecorder()
			PlatformAdminKnowledgeDebugHandler(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}
