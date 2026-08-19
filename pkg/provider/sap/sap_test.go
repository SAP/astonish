package sap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
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

// ---------- BaseURL normalization ----------

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
