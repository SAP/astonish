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
	if !strings.Contains(csp, "script-src 'self' 'wasm-unsafe-eval' ") {
		t.Fatalf("CSP must allow WebAssembly compilation for DOCX export without general unsafe-eval, got %q", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-eval'") || strings.Contains(csp, " 'unsafe-eval'") {
		t.Fatalf("CSP must not allow general JavaScript unsafe-eval, got %q", csp)
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

func TestCSPMiddlewarePreservesSlidesPresentationPolicy(t *testing.T) {
	const presentationCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'"
	handler := CSPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", presentationCSP)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/einstein/present?scope=personal", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Security-Policy"); got != presentationCSP {
		t.Fatalf("presentation CSP = %q, want %q", got, presentationCSP)
	}
}
