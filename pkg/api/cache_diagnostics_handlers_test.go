package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStudioCacheDiagnosticsRequiresSuperadmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/studio/sessions/session/cache-diagnostics?invocationId=inv", nil)
	w := httptest.NewRecorder()
	StudioCacheDiagnosticsHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestPlatformAdminCacheDiagnosticsRequiresSuperadmin(t *testing.T) {
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
			req := httptest.NewRequest(http.MethodGet, "/api/platform/admin/sessions/session/cache-diagnostics?org=acme&scope=personal&user=user", nil)
			if tt.user != nil {
				req = req.WithContext(WithPlatformUser(req.Context(), tt.user))
			}
			w := httptest.NewRecorder()
			PlatformAdminCacheDiagnosticsHandler(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}
