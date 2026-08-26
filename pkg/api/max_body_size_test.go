package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBodySizeMiddleware_DefaultLimit(t *testing.T) {
	handler := MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			respondError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Under limit: should succeed
	body := bytes.NewReader(make([]byte, 1000))
	req := httptest.NewRequest("POST", "/api/agents/test", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Over limit (1 MB + 1 byte): should fail
	body = bytes.NewReader(make([]byte, defaultMaxBody+1))
	req = httptest.NewRequest("POST", "/api/agents/test", body)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", w.Code)
	}
}

func TestMaxBodySizeMiddleware_LargeEndpoint(t *testing.T) {
	handler := MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			respondError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// 5 MB to a large endpoint: should succeed
	body := bytes.NewReader(make([]byte, 5<<20))
	req := httptest.NewRequest("POST", "/api/studio/apps/my-app", body)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for large endpoint, got %d", w.Code)
	}
}

func TestMaxBodySizeMiddleware_NilBody(t *testing.T) {
	handler := MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/agents", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLimitForPath(t *testing.T) {
	tests := []struct {
		path   string
		method string
		want   int64
	}{
		{"/api/agents", "POST", defaultMaxBody},
		{"/api/agents/test", "PUT", defaultMaxBody},
		{"/api/studio/apps/my-app", "POST", largeMaxBody},
		{"/api/studio/apps/my-app", "PUT", largeMaxBody},
		{"/api/ai/chat", "POST", largeMaxBody},
		{"/api/fleet/sessions/abc", "POST", largeMaxBody},
		{"/api/sandbox/templates/create", "POST", largeMaxBody},
		{"/api/studio/sessions/import", "POST", largeMaxBody},
		{"/api/docs/slides/import", "POST", pptxImportMaxBody},
		{"/api/agents", "GET", defaultMaxBody},
		{"/api/agents/test", "DELETE", defaultMaxBody},
	}

	for _, tt := range tests {
		got := limitForPath(tt.path, tt.method)
		if got != tt.want {
			t.Errorf("limitForPath(%q, %q) = %d, want %d", tt.path, tt.method, got, tt.want)
		}
	}
}

func TestMaxBodySizeMiddleware_EmptyStringBody(t *testing.T) {
	handler := MaxBodySizeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/agents/test", strings.NewReader(""))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
