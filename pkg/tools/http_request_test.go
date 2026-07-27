package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	storepkg "github.com/SAP/astonish/pkg/store"
)

// --- URL validation tests ---

func TestHttpRequest_URLValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"empty URL", "", "url is required"},
		{"no scheme", "example.com/path", "invalid URL"},
		{"ftp scheme", "ftp://example.com/file", "only http and https"},
		{"file scheme", "file:///etc/passwd", "only http and https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := HttpRequest(nil, HttpRequestArgs{URL: tt.url})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// --- SSRF protection tests ---

func TestHttpRequest_SSRFProtection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"loopback IPv4", "http://127.0.0.1/api", "private/loopback"},
		{"loopback IPv6", "http://[::1]/api", "private/loopback"},
		{"private 10.x", "http://10.0.0.1/api", "private/loopback"},
		{"private 192.168.x", "http://192.168.1.1/api", "private/loopback"},
		{"private 172.16.x", "http://172.16.0.1/api", "private/loopback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := HttpRequest(nil, HttpRequestArgs{URL: tt.url})
			if err == nil {
				t.Fatal("expected SSRF error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// --- Method validation tests ---

func TestHttpRequest_MethodValidation(t *testing.T) {
	// Start a test server for valid method tests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "method=%s", r.Method)
	}))
	defer srv.Close()

	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	t.Run("valid methods", func(t *testing.T) {
		validMethods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
		for _, m := range validMethods {
			result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Method: m})
			if err != nil {
				t.Errorf("method %s: unexpected error: %v", m, err)
				continue
			}
			if result.StatusCode != 200 {
				t.Errorf("method %s: expected 200, got %d", m, result.StatusCode)
			}
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Method: "post"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Body, "method=POST") {
			t.Errorf("expected POST, got body: %s", result.Body)
		}
	})

	t.Run("empty defaults to GET", func(t *testing.T) {
		result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Body, "method=GET") {
			t.Errorf("expected GET, got body: %s", result.Body)
		}
	})

	t.Run("invalid method", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Method: "INVALID"})
		if err == nil {
			t.Fatal("expected error for invalid method")
		}
		if !strings.Contains(err.Error(), "unsupported HTTP method") {
			t.Errorf("error %q should contain 'unsupported HTTP method'", err.Error())
		}
	})
}

// --- Credential resolution tests ---

func TestHttpRequest_CredentialResolution(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	// Track the auth header received by the server
	var receivedAuthKey, receivedAuthValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check all headers for the auth header sent by the credential
		receivedAuthKey = ""
		receivedAuthValue = ""
		// Check Authorization header
		if v := r.Header.Get("Authorization"); v != "" {
			receivedAuthKey = "Authorization"
			receivedAuthValue = v
		}
		// Check X-API-Key header (for api_key type)
		if v := r.Header.Get("X-API-Key"); v != "" {
			receivedAuthKey = "X-API-Key"
			receivedAuthValue = v
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Create a real credential store in a temp dir
	tmpDir := t.TempDir()
	store, err := credentials.Open(tmpDir)
	if err != nil {
		t.Fatalf("failed to open test credential store: %v", err)
	}

	// Save test credentials
	store.Set("test-apikey", &credentials.Credential{
		Type:   credentials.CredAPIKey,
		Header: "X-API-Key",
		Value:  "sk-test-123",
	})
	store.Set("test-bearer", &credentials.Credential{
		Type:  credentials.CredBearer,
		Token: "my-bearer-token",
	})
	store.Set("test-basic", &credentials.Credential{
		Type:     credentials.CredBasic,
		Username: "admin",
		Password: "secret",
	})

	// Set the package-level credential store
	oldStore := credentialStoreVar
	credentialStoreVar = store
	defer func() { credentialStoreVar = oldStore }()

	t.Run("api_key credential", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Credential: "test-apikey"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuthKey != "X-API-Key" || receivedAuthValue != "sk-test-123" {
			t.Errorf("expected X-API-Key: sk-test-123, got %s: %s", receivedAuthKey, receivedAuthValue)
		}
	})

	t.Run("bearer credential", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Credential: "test-bearer"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuthKey != "Authorization" || receivedAuthValue != "Bearer my-bearer-token" {
			t.Errorf("expected Authorization: Bearer my-bearer-token, got %s: %s", receivedAuthKey, receivedAuthValue)
		}
	})

	t.Run("basic credential", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Credential: "test-basic"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuthKey != "Authorization" || !strings.HasPrefix(receivedAuthValue, "Basic ") {
			t.Errorf("expected Authorization: Basic ..., got %s: %s", receivedAuthKey, receivedAuthValue)
		}
	})

	t.Run("missing credential", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Credential: "nonexistent"})
		if err == nil {
			t.Fatal("expected error for missing credential")
		}
		if !strings.Contains(err.Error(), "failed to resolve credential") {
			t.Errorf("error %q should contain 'failed to resolve credential'", err.Error())
		}
	})

	t.Run("no credential store", func(t *testing.T) {
		credentialStoreVar = nil
		defer func() { credentialStoreVar = store }()

		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Credential: "test-bearer"})
		if err == nil {
			t.Fatal("expected error when credential store is nil")
		}
		if !strings.Contains(err.Error(), "credential store is not available") {
			t.Errorf("error %q should contain 'credential store is not available'", err.Error())
		}
	})

	t.Run("context credential store", func(t *testing.T) {
		credentialStoreVar = nil
		defer func() { credentialStoreVar = store }()

		vault := sessioncreds.NewStore(map[string]*storepkg.Credential{
			"from-vault": {Type: storepkg.CredBearer, Token: "vault-token"},
		})
		ctx := storepkg.WithCredentialStore(context.Background(), vault)
		_, err := httpRequest(ctx, HttpRequestArgs{URL: srv.URL, Credential: "from-vault"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuthKey != "Authorization" || receivedAuthValue != "Bearer vault-token" {
			t.Errorf("expected Authorization: Bearer vault-token, got %s: %s", receivedAuthKey, receivedAuthValue)
		}
	})

	t.Run("merged context credential store resolves personal-first", func(t *testing.T) {
		credentialStoreVar = nil
		defer func() { credentialStoreVar = store }()

		personal := sessioncreds.NewStore(map[string]*storepkg.Credential{
			"openstack_keystone": {Type: storepkg.CredBearer, Token: "personal-token"},
		})
		team := sessioncreds.NewStore(map[string]*storepkg.Credential{
			"openstack_keystone": {Type: storepkg.CredBearer, Token: "team-token"},
		})
		ctx := storepkg.WithCredentialStore(context.Background(), storepkg.NewMergedCredentialStore(personal, team))
		_, err := httpRequest(ctx, HttpRequestArgs{URL: srv.URL, Credential: "openstack_keystone"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuthKey != "Authorization" || receivedAuthValue != "Bearer personal-token" {
			t.Errorf("expected Authorization: Bearer personal-token, got %s: %s", receivedAuthKey, receivedAuthValue)
		}
	})
}

// --- Timeout bounds tests ---

func TestHttpRequest_TimeoutBounds(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	// We can't easily test actual timeout behavior without slow servers,
	// but we can verify the bounds clamping logic by checking the request succeeds
	// with various timeout values.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		timeout int
	}{
		{"default (0)", 0},
		{"custom 10s", 10},
		{"exceeds max (300 clamped to 120)", 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL, Timeout: tt.timeout})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.StatusCode != 200 {
				t.Errorf("expected 200, got %d", result.StatusCode)
			}
		})
	}
}

// --- JSON auto-detection tests ---

func TestHttpRequest_JSONAutoDetect(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	var receivedContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Run("JSON object body", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{
			URL:    srv.URL,
			Method: "POST",
			Body:   `{"key": "value"}`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedContentType != "application/json" {
			t.Errorf("expected application/json, got %q", receivedContentType)
		}
	})

	t.Run("JSON array body", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{
			URL:    srv.URL,
			Method: "POST",
			Body:   `[1, 2, 3]`,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedContentType != "application/json" {
			t.Errorf("expected application/json, got %q", receivedContentType)
		}
	})

	t.Run("non-JSON body no auto-set", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{
			URL:    srv.URL,
			Method: "POST",
			Body:   "plain text data",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedContentType == "application/json" {
			t.Error("should not set application/json for non-JSON body")
		}
	})

	t.Run("explicit Content-Type not overridden", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{
			URL:     srv.URL,
			Method:  "POST",
			Body:    `{"key": "value"}`,
			Headers: map[string]string{"Content-Type": "text/plain"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedContentType != "text/plain" {
			t.Errorf("expected text/plain (user override), got %q", receivedContentType)
		}
	})
}

// --- Response truncation tests ---

func TestHttpRequest_ResponseTruncation(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	// Create a response body larger than the limit
	largeBody := strings.Repeat("x", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	t.Run("truncated when over limit", func(t *testing.T) {
		result, err := HttpRequest(nil, HttpRequestArgs{
			URL:      srv.URL,
			MaxBytes: 100,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Truncated {
			t.Error("expected truncated=true")
		}
		if len(result.Body) != 100 {
			t.Errorf("expected body length 100, got %d", len(result.Body))
		}
	})

	t.Run("not truncated when under limit", func(t *testing.T) {
		result, err := HttpRequest(nil, HttpRequestArgs{
			URL:      srv.URL,
			MaxBytes: 2048,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Truncated {
			t.Error("expected truncated=false")
		}
		if len(result.Body) != len(largeBody) {
			t.Errorf("expected body length %d, got %d", len(largeBody), len(result.Body))
		}
	})
}

// --- Header merging tests ---

func TestHttpRequest_HeaderMerging(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	var receivedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Run("user headers applied", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{
			URL: srv.URL,
			Headers: map[string]string{
				"X-Custom":  "custom-value",
				"X-Another": "another-value",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedHeaders.Get("X-Custom") != "custom-value" {
			t.Errorf("expected X-Custom header, got %q", receivedHeaders.Get("X-Custom"))
		}
		if receivedHeaders.Get("X-Another") != "another-value" {
			t.Errorf("expected X-Another header, got %q", receivedHeaders.Get("X-Another"))
		}
	})

	t.Run("user-agent set", func(t *testing.T) {
		_, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedHeaders.Get("User-Agent") != httpReqUserAgent {
			t.Errorf("expected User-Agent %q, got %q", httpReqUserAgent, receivedHeaders.Get("User-Agent"))
		}
	})

	t.Run("credential overrides user auth header", func(t *testing.T) {
		tmpDir := t.TempDir()
		store, err := credentials.Open(tmpDir)
		if err != nil {
			t.Fatalf("failed to open test credential store: %v", err)
		}
		store.Set("test-cred", &credentials.Credential{
			Type:  credentials.CredBearer,
			Token: "correct-token",
		})

		oldStore := credentialStoreVar
		credentialStoreVar = store
		defer func() { credentialStoreVar = oldStore }()

		_, err = HttpRequest(nil, HttpRequestArgs{
			URL:        srv.URL,
			Headers:    map[string]string{"Authorization": "Bearer wrong-token"},
			Credential: "test-cred",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedHeaders.Get("Authorization") != "Bearer correct-token" {
			t.Errorf("credential should override user auth: got %q", receivedHeaders.Get("Authorization"))
		}
	})
}

// --- Successful request end-to-end tests ---

func TestHttpRequest_SuccessfulRequest(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "abc-123")
		w.WriteHeader(200)
		w.Write([]byte(`{"message":"hello","count":42}`))
	}))
	defer srv.Close()

	result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Status
	if result.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
	if !strings.Contains(result.Status, "200") {
		t.Errorf("expected status string containing '200', got %q", result.Status)
	}

	// Content type
	if result.ContentType != "application/json" {
		t.Errorf("expected application/json, got %q", result.ContentType)
	}

	// Duration
	if result.DurationMs < 0 {
		t.Errorf("expected non-negative duration, got %d", result.DurationMs)
	}

	// Response headers (all headers returned)
	if result.Headers["X-Request-Id"] != "abc-123" {
		t.Errorf("expected X-Request-Id header, got %q", result.Headers["X-Request-Id"])
	}
	if result.Headers["Content-Type"] != "application/json" {
		t.Errorf("expected Content-Type in headers, got %q", result.Headers["Content-Type"])
	}

	// Not truncated
	if result.Truncated {
		t.Error("expected truncated=false")
	}
}

// --- JSON pretty-print tests ---

func TestHttpRequest_JSONPrettyPrint(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	compact := `{"name":"test","items":[1,2,3]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(compact))
	}))
	defer srv.Close()

	result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the body was pretty-printed
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.Body), &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	// Pretty-printed JSON should contain newlines and indentation
	if !strings.Contains(result.Body, "\n") {
		t.Error("expected pretty-printed JSON with newlines")
	}
	if !strings.Contains(result.Body, "  ") {
		t.Error("expected pretty-printed JSON with indentation")
	}
}

// --- Non-200 status codes ---

func TestHttpRequest_NonSuccessStatus(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	result, err := HttpRequest(nil, HttpRequestArgs{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.StatusCode != 404 {
		t.Errorf("expected 404, got %d", result.StatusCode)
	}
	if result.Body != "not found" {
		t.Errorf("expected 'not found', got %q", result.Body)
	}
}

// --- POST with body ---

func TestHttpRequest_PostWithBody(t *testing.T) {
	// Allow loopback for httptest servers
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	var receivedBody string
	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	_, err := HttpRequest(nil, HttpRequestArgs{
		URL:    srv.URL,
		Method: "POST",
		Body:   `{"name": "test"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedMethod != "POST" {
		t.Errorf("expected POST, got %s", receivedMethod)
	}
	if receivedBody != `{"name": "test"}` {
		t.Errorf("expected JSON body, got %q", receivedBody)
	}
}

func TestHttpRequest_BodyPath(t *testing.T) {
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, []byte("raw-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotBody []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := HttpRequest(nil, HttpRequestArgs{
		URL:         srv.URL,
		Method:      "POST",
		BodyPath:    path,
		ContentType: "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(gotBody) != "raw-bytes" {
		t.Fatalf("body = %q", gotBody)
	}
	if gotCT != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
}

func TestHttpRequest_Multipart(t *testing.T) {
	httpReqSkipSSRF = true
	defer func() { httpReqSkipSSRF = false }()

	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(path, []byte("fake-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"asset-1"}`))
	}))
	defer srv.Close()

	res, err := HttpRequest(nil, HttpRequestArgs{
		URL:    srv.URL,
		Method: "POST",
		Multipart: []HttpMultipartPart{
			{Name: "title", Value: "demo"},
			{Name: "file", FilePath: path, Filename: "demo.mp4", ContentType: "video/mp4"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotCT, "multipart/form-data") {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if !bytes.Contains(gotBody, []byte("fake-mp4")) || !bytes.Contains(gotBody, []byte("demo")) {
		t.Fatalf("multipart body missing parts: %q", gotBody)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestHttpRequest_MutuallyExclusiveBody(t *testing.T) {
	_, err := HttpRequest(nil, HttpRequestArgs{
		URL:      "https://example.com",
		Body:     "{}",
		BodyPath: "/tmp/x",
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}
