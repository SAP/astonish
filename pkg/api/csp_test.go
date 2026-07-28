package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSPMiddleware_AllowsBlobMedia(t *testing.T) {
	handler := CSPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing Content-Security-Policy header")
	}
	if !strings.Contains(csp, "media-src 'self' blob:") {
		t.Fatalf("CSP must allow blob: media for recording playback, got %q", csp)
	}
	// Ensure img-src blob: was not regressed.
	if !strings.Contains(csp, "img-src 'self' data: blob:") {
		t.Fatalf("CSP missing img-src blob: allowance, got %q", csp)
	}
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com") {
		t.Fatalf("CSP missing Google Fonts stylesheet allowance, got %q", csp)
	}
	if !strings.Contains(csp, "font-src 'self' https://fonts.gstatic.com") {
		t.Fatalf("CSP missing Google Fonts font allowance, got %q", csp)
	}
	if !strings.Contains(csp, "connect-src 'self' ws: wss: https://api.github.com") {
		t.Fatalf("CSP missing GitHub release-check allowance, got %q", csp)
	}
}
