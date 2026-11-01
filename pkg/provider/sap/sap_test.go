package sap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// ---------- GetModelConfig ----------

func TestGetModelConfig_KnownModels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		maxTokens     int
		contextWindow int
	}{
		{"anthropic--claude-4.5-sonnet", 64000, 200000},
		{"gemini-2.5-pro", 65536, 1048576},
		{"gpt-4o", 4096, 200000},
		{"o3", 100000, 200000},
		{"gpt-5", 128000, 272000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := GetModelConfig(tt.name)
			if cfg.MaxTokens != tt.maxTokens {
				t.Errorf("MaxTokens: got %d, want %d", cfg.MaxTokens, tt.maxTokens)
			}
			if cfg.ContextWindow != tt.contextWindow {
				t.Errorf("ContextWindow: got %d, want %d", cfg.ContextWindow, tt.contextWindow)
			}
		})
	}
}

func TestGetModelConfig_UnknownFallback(t *testing.T) {
	t.Parallel()
	cfg := GetModelConfig("totally-unknown-model-xyz")
	if cfg.MaxTokens != 64000 {
		t.Errorf("expected fallback MaxTokens 64000, got %d", cfg.MaxTokens)
	}
	if cfg.ContextWindow != 200000 {
		t.Errorf("expected fallback ContextWindow 200000, got %d", cfg.ContextWindow)
	}
}

func TestGetModelConfig_NoTemperatureFlag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model   string
		wantNoT bool
		desc    string
	}{
		// Explicit map entries — Claude 4.x+
		{"anthropic--claude-4.8-opus", true, "claude-4.8-opus (explicit)"},
		{"anthropic--claude-4.6-opus", true, "claude-4.6-opus (explicit)"},
		{"anthropic--claude-4.5-sonnet", true, "claude-4.5-sonnet (explicit)"},
		{"anthropic--claude-4-opus", true, "claude-4-opus (explicit)"},
		// Explicit map entries — Claude 3.x (temperature still supported)
		{"anthropic--claude-3.7-sonnet", false, "claude-3.7-sonnet (explicit)"},
		{"anthropic--claude-3.5-sonnet", false, "claude-3.5-sonnet (explicit)"},
		{"anthropic--claude-3-haiku", false, "claude-3-haiku (explicit)"},
		// Pattern-inferred: unknown future models not in the static map
		{"anthropic--claude-5-opus", true, "claude-5-opus (inferred, major=5)"},
		{"anthropic--claude-5.1-sonnet", true, "claude-5.1-sonnet (inferred, major=5)"},
		{"anthropic--claude-4.9-fable", true, "claude-4.9-fable (inferred, major=4)"},
		{"anthropic--claude-3.9-unknown", false, "claude-3.9-unknown (inferred, major=3)"},
		{"anthropic--claude-2-instant", false, "claude-2-instant (inferred, major=2)"},
		// Non-Anthropic models — temperature always included
		{"gpt-4o", false, "gpt-4o (non-anthropic)"},
		{"gemini-2.5-pro", false, "gemini-2.5-pro (non-anthropic)"},
		{"totally-unknown-model", false, "unknown non-anthropic model"},
	}

	for _, tc := range cases {
		cfg := GetModelConfig(tc.model)
		if cfg.NoTemperature != tc.wantNoT {
			t.Errorf("%s: NoTemperature = %v, want %v", tc.desc, cfg.NoTemperature, tc.wantNoT)
		}
	}
}

// ---------- ModelIDMap ----------

func TestModelIDMap_Contains(t *testing.T) {
	t.Parallel()
	expected := []string{"gpt-4o", "gpt-4o-mini", "gpt-4", "o1", "o4-mini", "gpt-5"}
	for _, key := range expected {
		if _, ok := ModelIDMap[key]; !ok {
			t.Errorf("ModelIDMap missing key %q", key)
		}
	}
}

// ---------- sapTransport: OAuth token fetching ----------

func TestSapTransport_GetToken_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/oauth/token") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("grant_type") != "client_credentials" {
			t.Errorf("expected grant_type=client_credentials, got %q", form.Get("grant_type"))
		}
		if form.Get("client_id") != "test-id" {
			t.Errorf("expected client_id=test-id, got %q", form.Get("client_id"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc123",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "test-id",
		clientSecret: "test-secret",
		authURL:      srv.URL,
	}

	token, err := tr.getToken()
	if err != nil {
		t.Fatalf("getToken() error: %v", err)
	}
	if token != "tok-abc123" {
		t.Errorf("expected tok-abc123, got %q", token)
	}
}

func TestSapTransport_GetToken_Caching(t *testing.T) {
	t.Parallel()
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("tok-%d", callCount),
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "id",
		clientSecret: "secret",
		authURL:      srv.URL,
	}

	tok1, err := tr.getToken()
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := tr.getToken()
	if err != nil {
		t.Fatal(err)
	}

	if tok1 != tok2 {
		t.Errorf("expected cached token, got %q then %q", tok1, tok2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 token request (cached), got %d", callCount)
	}
}

func TestSapTransport_GetToken_Expired(t *testing.T) {
	t.Parallel()
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("tok-%d", callCount),
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "id",
		clientSecret: "secret",
		authURL:      srv.URL,
	}

	tok1, err := tr.getToken()
	if err != nil {
		t.Fatal(err)
	}

	// Force expiry
	tr.mu.Lock()
	tr.expiresAt = time.Now().Add(-1 * time.Second)
	tr.mu.Unlock()

	tok2, err := tr.getToken()
	if err != nil {
		t.Fatal(err)
	}

	if tok1 == tok2 {
		t.Error("expected new token after expiry, got same")
	}
	if callCount != 2 {
		t.Errorf("expected 2 token requests, got %d", callCount)
	}
}

func TestSapTransport_GetToken_AuthError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "bad-id",
		clientSecret: "bad-secret",
		authURL:      srv.URL,
	}

	_, err := tr.getToken()
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain 401, got: %v", err)
	}
}

// ---------- sapTransport: RoundTrip ----------

func TestSapTransport_RoundTrip_SetsHeaders(t *testing.T) {
	t.Parallel()

	// Token server
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "bearer-tok",
			"expires_in":   3600,
		})
	}))
	defer tokenSrv.Close()

	// Target server that verifies headers
	var gotAuth, gotResourceGroup string
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotResourceGroup = r.Header.Get("AI-Resource-Group")
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	tr := &sapTransport{
		base:          http.DefaultTransport,
		clientID:      "id",
		clientSecret:  "secret",
		authURL:       tokenSrv.URL,
		resourceGroup: "my-rg",
	}

	req, _ := http.NewRequest("GET", targetSrv.URL+"/some/path", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer bearer-tok" {
		t.Errorf("expected 'Bearer bearer-tok', got %q", gotAuth)
	}
	if gotResourceGroup != "my-rg" {
		t.Errorf("expected 'my-rg', got %q", gotResourceGroup)
	}
}

func TestSapTransport_RoundTrip_InjectsApiVersion(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	var gotQuery string
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "id",
		clientSecret: "secret",
		authURL:      tokenSrv.URL,
	}

	req, _ := http.NewRequest("POST", targetSrv.URL+"/chat/completions", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.Contains(gotQuery, "api-version=2024-12-01-preview") {
		t.Errorf("expected api-version query param, got query %q", gotQuery)
	}
}

func TestSapTransport_RoundTrip_PreservesExistingApiVersion(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	var gotQuery string
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "id",
		clientSecret: "secret",
		authURL:      tokenSrv.URL,
	}

	req, _ := http.NewRequest("POST", targetSrv.URL+"/chat/completions?api-version=2023-01-01", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.Contains(gotQuery, "api-version=2023-01-01") {
		t.Errorf("expected preserved api-version, got query %q", gotQuery)
	}
	if strings.Contains(gotQuery, "2024-12-01-preview") {
		t.Error("should not have overwritten existing api-version")
	}
}

// ---------- sapTransport: concurrent token access ----------

func TestSapTransport_GetToken_Concurrent(t *testing.T) {
	t.Parallel()
	var callCount int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer srv.Close()

	tr := &sapTransport{
		base:         http.DefaultTransport,
		clientID:     "id",
		clientSecret: "secret",
		authURL:      srv.URL,
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := tr.getToken()
			if err != nil {
				t.Errorf("getToken error: %v", err)
			}
			if tok != "tok" {
				t.Errorf("expected 'tok', got %q", tok)
			}
		}()
	}
	wg.Wait()

	// Due to mutex, many goroutines should share the cached token.
	// At most a few should have triggered a request.
	mu.Lock()
	defer mu.Unlock()
	if callCount > 5 {
		t.Errorf("expected <=5 token fetches with caching, got %d", callCount)
	}
}

// ---------- resolveDeploymentIDWithConfig ----------

func TestResolveDeploymentID_Success(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/lm/deployments" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"resources": []map[string]any{
					{
						"id":     "dep-123",
						"status": "RUNNING",
						"details": map[string]any{
							"resources": map[string]any{
								"backendDetails": map[string]any{
									"model": map[string]any{
										"name": "gpt-4o",
									},
								},
							},
						},
					},
					{
						"id":     "dep-456",
						"status": "STOPPED",
						"details": map[string]any{
							"resources": map[string]any{
								"backendDetails": map[string]any{
									"model": map[string]any{
										"name": "gpt-4o",
									},
								},
							},
						},
					},
				},
			})
			return
		}
		// Token endpoint
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer deploySrv.Close()

	id, err := resolveDeploymentIDWithConfig(
		context.Background(),
		"gpt-4o",
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL+"/v2",
		"default",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "dep-123" {
		t.Errorf("expected dep-123, got %q", id)
	}
}

func TestResolveDeploymentID_UsesRunningResourceGroupFilter(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("status") != "RUNNING" || r.URL.Query().Get("resourceGroup") != "configured-rg" {
			json.NewEncoder(w).Encode(map[string]any{"resources": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{{
				"id":     "dep-opus-46",
				"status": "RUNNING",
				"details": map[string]any{
					"resources": map[string]any{
						"backendDetails": map[string]any{
							"model": map[string]any{"name": "anthropic--claude-4.6-opus"},
						},
					},
				},
			}},
		})
	}))
	defer deploySrv.Close()

	id, err := resolveDeploymentIDWithConfig(
		context.Background(),
		"anthropic--claude-4.6-opus",
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL+"/v2",
		"configured-rg",
	)
	if err != nil {
		t.Fatalf("resolve model returned by filtered deployment list: %v", err)
	}
	if id != "dep-opus-46" {
		t.Fatalf("expected dep-opus-46, got %q", id)
	}
}

func TestResolveDeploymentID_DefaultsEmptyResourceGroup(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("AI-Resource-Group") != "default" || r.URL.Query().Get("resourceGroup") != "default" {
			http.Error(w, `{"error":{"message":"missing default resource group"}}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{{
				"id":     "dep-opus-46",
				"status": "RUNNING",
				"details": map[string]any{
					"resources": map[string]any{
						"backendDetails": map[string]any{
							"model": map[string]any{"name": "anthropic--claude-4.6-opus"},
						},
					},
				},
			}},
		})
	}))
	defer deploySrv.Close()

	id, err := resolveDeploymentIDWithConfig(
		context.Background(),
		"anthropic--claude-4.6-opus",
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL+"/v2",
		"",
	)
	if err != nil {
		t.Fatalf("resolve deployment with omitted resource group: %v", err)
	}
	if id != "dep-opus-46" {
		t.Fatalf("expected dep-opus-46, got %q", id)
	}
}

func TestResolveDeploymentID_NotFound(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"resources": []any{}})
	}))
	defer deploySrv.Close()

	_, err := resolveDeploymentIDWithConfig(
		context.Background(),
		"nonexistent-model",
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL+"/v2",
		"default",
	)
	if err == nil {
		t.Fatal("expected error for missing deployment")
	}
	if !strings.Contains(err.Error(), "no running deployment") {
		t.Errorf("expected 'no running deployment' error, got: %v", err)
	}
}

func TestResolveDeploymentID_ModelIDMapping(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	var gotModelName string
	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return a deployment matching the mapped name
		json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{
				{
					"id":     "dep-mapped",
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gpt-4o"},
							},
						},
					},
				},
			},
		})
	}))
	defer deploySrv.Close()

	// "gpt-4o" is in ModelIDMap and maps to "gpt-4o"
	id, err := resolveDeploymentIDWithConfig(
		context.Background(),
		"gpt-4o",
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL+"/v2",
		"default",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "dep-mapped" {
		t.Errorf("expected dep-mapped, got %q", id)
	}
	_ = gotModelName // suppress unused
}

// ---------- ListModels ----------

func TestListModels_Success(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gpt-4o"},
							},
						},
					},
				},
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gemini-2.5-pro"},
							},
						},
					},
				},
				{
					"status": "STOPPED",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "should-be-excluded"},
							},
						},
					},
				},
			},
		})
	}))
	defer deploySrv.Close()

	models, err := ListModels(
		context.Background(),
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL,
		"default",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(models), models)
	}
	// Results are sorted
	if models[0] != "gemini-2.5-pro" {
		t.Errorf("expected first model gemini-2.5-pro, got %q", models[0])
	}
	if models[1] != "gpt-4o" {
		t.Errorf("expected second model gpt-4o, got %q", models[1])
	}
}

func TestListModels_DeduplicatesRunning(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gpt-4o"},
							},
						},
					},
				},
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gpt-4o"},
							},
						},
					},
				},
			},
		})
	}))
	defer deploySrv.Close()

	models, err := ListModels(
		context.Background(),
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL,
		"default",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Errorf("expected 1 deduplicated model, got %d", len(models))
	}
}

func TestSAPGPT6FunctionToolsUseResponsesAPI(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1","status":"completed","model":"gpt-6-luna",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}
		}`))
	}))
	defer server.Close()

	config := goopenai.DefaultConfig("test")
	config.BaseURL = server.URL
	provider := newOpenAIProvider(goopenai.NewClientWithConfig(config), "gpt-6-luna")
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("test", genai.RoleUser)},
		Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{
			FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name:                 "lookup",
				ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}},
		}}},
	}

	for _, err := range provider.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if path != "/responses" {
		t.Fatalf("SAP gpt-6 request path = %q, want /responses", path)
	}
}

// ---------- Auth recovery edge cases ----------

func TestSapTransport_DoesNotRecover403(t *testing.T) {
	var authCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		fmt.Fprint(w, `{"access_token":"new","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) }))
	defer srv.Close()
	tr := &sapTransport{base: http.DefaultTransport, authURL: tokenSrv.URL, token: "old", expiresAt: time.Now().Add(time.Hour)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 || authCalls.Load() != 0 || tr.token != "old" {
		t.Fatal("403 caused auth renewal")
	}
}

func TestSapTransport_TokenExpiryBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new","expires_in":3600}`)
	}))
	defer srv.Close()
	tr := &sapTransport{authURL: srv.URL}
	before := time.Now()
	if _, err := tr.getToken(); err != nil {
		t.Fatal(err)
	}
	if tr.expiresAt.Before(before.Add(3570*time.Second)) || tr.expiresAt.After(time.Now().Add(3570*time.Second)) {
		t.Fatal("30-second expiry buffer not applied")
	}
}

func TestSapTransport_Concurrent401SingleRenewal(t *testing.T) {
	const workers = 12
	var authCalls atomic.Int32
	var rejected atomic.Int32
	barrier := make(chan struct{})
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		fmt.Fprint(w, `{"access_token":"new","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer old" {
			if rejected.Add(1) == workers {
				close(barrier)
			}
			<-barrier
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tr := &sapTransport{base: http.DefaultTransport, authURL: tokenSrv.URL, token: "old", expiresAt: time.Now().Add(time.Hour)}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if authCalls.Load() != 1 {
		t.Fatalf("renewals=%d, want 1", authCalls.Load())
	}
	// A delayed rejection must not clear a newer cached token.
	tr.invalidateToken("old")
	tok, err := tr.getTokenForRequest(context.Background(), "old")
	if err != nil || tok != "new" || authCalls.Load() != 1 {
		t.Fatalf("newer token not reused: %q %v", tok, err)
	}
}

func TestSapTransport_RefreshFailureInvalidatesRejectedToken(t *testing.T) {
	var authCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authCalls.Add(1) == 1 {
			http.Error(w, "secret token old client-secret", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"access_token":"new","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	tr := &sapTransport{base: http.DefaultTransport, authURL: tokenSrv.URL, token: "old", expiresAt: time.Now().Add(time.Hour)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := tr.RoundTrip(req)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "old") {
		t.Fatalf("unsanitized/missing auth failure: %v", err)
	}
	tr.mu.Lock()
	cached := tr.token
	tr.mu.Unlock()
	if cached != "" {
		t.Fatal("rejected token remains cached")
	}
	tok, err := tr.getToken()
	if err != nil || tok != "new" || authCalls.Load() != 2 {
		t.Fatalf("later exchange: %q %v", tok, err)
	}
}

func TestSapTransport_Second401IsTerminal(t *testing.T) {
	var authCalls, requests atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		fmt.Fprint(w, `{"access_token":"new","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "terminal", http.StatusUnauthorized)
	}))
	defer srv.Close()
	tr := &sapTransport{base: http.DefaultTransport, authURL: tokenSrv.URL, token: "old", expiresAt: time.Now().Add(time.Hour)}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 401 || string(body) != "terminal\n" || authCalls.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("unbounded/lost terminal response: %d %s", resp.StatusCode, body)
	}
	tr.mu.Lock()
	cached := tr.token
	tr.mu.Unlock()
	if cached != "" {
		t.Fatal("second rejected token cached")
	}
}

func TestSapTransport_RefreshCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer srv.Close()
	defer close(release)
	tr := &sapTransport{base: http.DefaultTransport, authURL: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := tr.getTokenForRequest(ctx, ""); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("exchange ignored request cancellation")
	}
}

func TestSapTransport_RejectsInvalidTokenResponse(t *testing.T) {
	for _, body := range []string{`{"access_token":"","expires_in":3600}`, `{"access_token":"secret","expires_in":0}`, `{"access_token":"secret","expires_in":-1}`, `{"access_token":"secret","expires_in":9223372036854775807}`, `{"access_token":"secret","expires_in":"oops"}`, `not-json secret`, strings.Repeat("x", maxSAPAuthResponseBytes+1)} {
		t.Run(fmt.Sprintf("case-%d", len(body)), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			tr := &sapTransport{authURL: srv.URL, token: "old", expiresAt: time.Now().Add(-time.Second)}
			_, err := tr.getToken()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid/unsanitized response: %v", err)
			}
			if tr.token != "" {
				t.Fatal("invalid token cached")
			}
		})
	}
}

func TestSAPDiscovery401Recovery(t *testing.T) {
	for _, mode := range []string{"configured", "environment", "models"} {
		t.Run(mode, func(t *testing.T) {
			var authCalls, requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3600}`, authCalls.Add(1))
					return
				}
				if r.URL.Path != "/v2/lm/deployments" || r.Header.Get("AI-Resource-Group") != "a & b" || r.URL.Query().Get("resourceGroup") != "a & b" || r.URL.Query().Get("status") != "RUNNING" {
					t.Errorf("discovery endpoint/filters changed: %s", r.URL)
				}
				if requests.Add(1) == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.Header.Get("Authorization") != "Bearer token-2" {
					t.Error("missing renewed token")
				}
				fmt.Fprint(w, `{"resources":[{"id":"deployment","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"gpt-4o"}}}}}]}`)
			}))
			defer srv.Close()
			var err error
			switch mode {
			case "configured":
				var id string
				id, err = resolveDeploymentIDWithConfig(context.Background(), "gpt-4o", "id", "secret", srv.URL, srv.URL, "a & b")
				if err == nil && id != "deployment" {
					t.Fatalf("id=%q", id)
				}
			case "environment":
				t.Setenv("AICORE_CLIENT_ID", "id")
				t.Setenv("AICORE_CLIENT_SECRET", "secret")
				t.Setenv("AICORE_AUTH_URL", srv.URL)
				t.Setenv("AICORE_BASE_URL", srv.URL)
				t.Setenv("AICORE_RESOURCE_GROUP", "a & b")
				var id string
				id, err = ResolveDeploymentID(context.Background(), "gpt-4o")
				if err == nil && id != "deployment" {
					t.Fatalf("id=%q", id)
				}
			case "models":
				var models []string
				models, err = ListModels(context.Background(), "id", "secret", srv.URL, srv.URL, "a & b")
				if err == nil && (len(models) != 1 || models[0] != "gpt-4o") {
					t.Fatalf("models=%v", models)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if authCalls.Load() != 2 || requests.Load() != 2 {
				t.Fatalf("auth=%d discovery=%d", authCalls.Load(), requests.Load())
			}
		})
	}
}

func TestSAPGenerationAuthRecovery(t *testing.T) {
	for _, name := range []string{"gpt-4o", "gemini-2.5-pro", "anthropic--claude-3-sonnet"} {
		for _, stream := range []bool{false, true} {
			for _, expired := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/expired=%t", name, stream, expired), func(t *testing.T) {
					var authCalls, generationCalls atomic.Int32
					var rejectNext atomic.Bool
					var mu sync.Mutex
					var rejectedBody, replayBody, rejectedPath, replayPath, rejectedQuery, replayQuery string
					var rejectedHeader, replayHeader http.Header
					var awaitingReplay bool
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/oauth/token" {
							fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3600}`, authCalls.Add(1))
							return
						}
						if r.URL.Path == "/v2/lm/deployments" {
							fmt.Fprintf(w, `{"resources":[{"id":"dep","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":%q}}}}}]}`, name)
							return
						}
						generationCalls.Add(1)
						body, _ := io.ReadAll(r.Body)
						if r.Header.Get("AI-Resource-Group") != "group" {
							t.Error("resource group missing")
						}
						if rejectNext.Swap(false) {
							mu.Lock()
							rejectedBody = string(body)
							rejectedPath = r.URL.Path
							rejectedQuery = r.URL.RawQuery
							rejectedHeader = r.Header.Clone()
							awaitingReplay = true
							mu.Unlock()
							http.Error(w, `{"error":{"message":"server expired token"}}`, http.StatusUnauthorized)
							return
						}
						mu.Lock()
						if awaitingReplay {
							replayBody = string(body)
							replayPath = r.URL.Path
							replayQuery = r.URL.RawQuery
							replayHeader = r.Header.Clone()
							awaitingReplay = false
						}
						mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						switch {
						case strings.HasPrefix(name, "gpt-"):
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprint(w, "data: {\"id\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
							} else {
								fmt.Fprint(w, `{"id":"chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
							}
						case strings.HasPrefix(name, "gemini-"):
							response := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprintf(w, "data: %s\n\n", response)
							} else {
								fmt.Fprint(w, response)
							}
						default:
							if stream {
								w.Header().Set("Content-Type", "text/event-stream")
								fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
							} else {
								fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}],"role":"assistant","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
							}
						}
					}))
					defer srv.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					llm, err := NewProviderWithConfig(ctx, name, "id", "secret", srv.URL, srv.URL, "group")
					if err != nil {
						t.Fatal(err)
					}
					p := llm.(*Provider)
					req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("unchanged prompt", genai.RoleUser)}}
					generate := func() {
						t.Helper()
						var text strings.Builder
						for response, err := range p.GenerateContent(ctx, req, stream) {
							if err != nil {
								t.Fatal(err)
							}
							if response != nil && response.Content != nil {
								for _, part := range response.Content.Parts {
									if part != nil {
										text.WriteString(part.Text)
									}
								}
							}
						}
						if !strings.Contains(text.String(), "ok") {
							t.Fatalf("no generated content: %q", text.String())
						}
					}
					generate() // Same instance succeeds before rejection.
					beforeAuth := authCalls.Load()
					if expired {
						p.authConfig.mu.Lock()
						p.authConfig.expiresAt = time.Now().Add(-time.Second)
						p.authConfig.mu.Unlock()
					}
					rejectNext.Store(true)
					generate() // Server rejection must be transparent even after proactive refresh.
					generate() // New token is retained for subsequent generation.
					wantAuth := beforeAuth + 1
					if expired {
						wantAuth++
					}
					if authCalls.Load() != wantAuth || generationCalls.Load() != 4 {
						t.Fatalf("auth=%d want=%d generation=%d want=4", authCalls.Load(), wantAuth, generationCalls.Load())
					}
					mu.Lock()
					defer mu.Unlock()
					if rejectedBody == "" || rejectedBody != replayBody || rejectedPath != replayPath || rejectedQuery != replayQuery {
						t.Fatal("generation request changed during replay")
					}
					if rejectedHeader.Get("Authorization") == replayHeader.Get("Authorization") {
						t.Fatal("replay used rejected token")
					}
					rejectedHeader.Del("Authorization")
					replayHeader.Del("Authorization")
					oldHeaders, _ := json.Marshal(rejectedHeader)
					newHeaders, _ := json.Marshal(replayHeader)
					if string(oldHeaders) != string(newHeaders) {
						t.Fatalf("non-auth headers changed: %s vs %s", oldHeaders, newHeaders)
					}
				})
			}
		}
	}
}

// ---------- BaseURL normalization ----------

func TestSapTransport_Reactive401Recovery(t *testing.T) {
	testSAPTransportRecovery(t, false)
}

func TestSapTransport_RefreshThen401Recovery(t *testing.T) {
	testSAPTransportRecovery(t, true)
}

func testSAPTransportRecovery(t *testing.T, expired bool) {
	var authCalls atomic.Int32
	var requests atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := authCalls.Add(1)
		fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3600}`, n)
	}))
	defer tokenSrv.Close()
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"prompt":"unchanged"}` {
			t.Errorf("replay body changed: %s", body)
		}
		if r.Header.Get("AI-Resource-Group") != "group" || r.URL.Query().Get("api-version") != "2024-12-01-preview" {
			t.Error("missing decoration")
		}
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-2" {
			t.Errorf("retry token = %s", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()
	tr := &sapTransport{base: http.DefaultTransport, authURL: tokenSrv.URL, resourceGroup: "group", token: "token-1", expiresAt: time.Now().Add(time.Hour)}
	if expired {
		tr.expiresAt = time.Now().Add(-time.Second)
	} else {
		authCalls.Store(1)
	}
	req, _ := http.NewRequest(http.MethodPost, targetSrv.URL+"/chat/completions?custom=preserved", strings.NewReader(`{"prompt":"unchanged"}`))
	req.Header.Set("Authorization", "original")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after renewal", resp.StatusCode)
	}
	if requests.Load() != 2 || authCalls.Load() != 2 {
		t.Fatalf("requests=%d auth=%d", requests.Load(), authCalls.Load())
	}
	if req.Header.Get("Authorization") != "original" || req.URL.Query().Get("api-version") != "" {
		t.Error("caller request mutated")
	}
}

func TestNewProviderWithConfig_BaseURLNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com", "https://example.com/v2"},
		{"https://example.com/", "https://example.com/v2"},
		{"https://example.com/v2", "https://example.com/v2"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			baseURL := tt.input
			if !strings.HasSuffix(baseURL, "/v2") {
				if strings.HasSuffix(baseURL, "/") {
					baseURL += "v2"
				} else {
					baseURL += "/v2"
				}
			}
			if baseURL != tt.expected {
				t.Errorf("normalized %q to %q, want %q", tt.input, baseURL, tt.expected)
			}
		})
	}
}

// ---------- Provider.Name ----------

func TestProvider_Name(t *testing.T) {
	t.Parallel()
	p := &Provider{modelName: "gpt-4o"}
	if p.Name() != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", p.Name())
	}
}

// ---------- ListModelsWithMetadata ----------

func TestListModelsWithMetadata_EnrichesConfig(t *testing.T) {
	// Reset cache for this test
	sapModelCacheMu.Lock()
	sapModelCache = nil
	sapModelCacheTime = time.Time{}
	sapModelCacheMu.Unlock()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	deploySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"resources": []map[string]any{
				{
					"status": "RUNNING",
					"details": map[string]any{
						"resources": map[string]any{
							"backendDetails": map[string]any{
								"model": map[string]any{"name": "gpt-4o"},
							},
						},
					},
				},
			},
		})
	}))
	defer deploySrv.Close()

	models, err := ListModelsWithMetadata(
		context.Background(),
		"id", "secret",
		tokenSrv.URL,
		deploySrv.URL,
		"default",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	m := models[0]
	if m.ID != "gpt-4o" {
		t.Errorf("expected ID gpt-4o, got %q", m.ID)
	}
	expectedCfg := GetModelConfig("gpt-4o")
	if m.ContextLength != expectedCfg.ContextWindow {
		t.Errorf("ContextLength: got %d, want %d", m.ContextLength, expectedCfg.ContextWindow)
	}
	if m.MaxOutputTokens != expectedCfg.MaxTokens {
		t.Errorf("MaxOutputTokens: got %d, want %d", m.MaxOutputTokens, expectedCfg.MaxTokens)
	}
}

func TestListModelsWithMetadata_UsesCache(t *testing.T) {
	cached := []ModelInfo{{ID: "cached-model", Name: "cached-model", ContextLength: 100, MaxOutputTokens: 50}}
	sapModelCacheMu.Lock()
	sapModelCache = cached
	sapModelCacheTime = time.Now()
	sapModelCacheMu.Unlock()

	// No servers needed — should return cache
	models, err := ListModelsWithMetadata(context.Background(), "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "cached-model" {
		t.Errorf("expected cached result, got %v", models)
	}

	// Clean up
	sapModelCacheMu.Lock()
	sapModelCache = nil
	sapModelCacheTime = time.Time{}
	sapModelCacheMu.Unlock()
}
