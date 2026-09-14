package copilot_oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestDeviceCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("expected Accept application/json, got %s", accept)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if cid := body["client_id"]; cid != "test-client-id" {
			t.Errorf("expected client_id=test-client-id, got %s", cid)
		}
		if scope := body["scope"]; scope != oauthScope {
			t.Errorf("expected scope=%s, got %s", oauthScope, scope)
		}

		resp := DeviceCodeResponse{
			DeviceCode:      "dev-code-123",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       600,
			Interval:        5,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dcResp, err := requestDeviceCodeFromURL(context.Background(), "test-client-id", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dcResp.DeviceCode != "dev-code-123" {
		t.Errorf("DeviceCode = %q, want dev-code-123", dcResp.DeviceCode)
	}
	if dcResp.UserCode != "ABCD-1234" {
		t.Errorf("UserCode = %q, want ABCD-1234", dcResp.UserCode)
	}
	if dcResp.VerificationURI != "https://github.com/login/device" {
		t.Errorf("VerificationURI = %q", dcResp.VerificationURI)
	}
	if dcResp.Interval != 5 {
		t.Errorf("Interval = %d, want 5", dcResp.Interval)
	}
}

func TestRequestDeviceCode_DefaultInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DeviceCodeResponse{
			DeviceCode:      "dev-code-456",
			UserCode:        "EFGH-5678",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       600,
			Interval:        0, // Not specified
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dcResp, err := requestDeviceCodeFromURL(context.Background(), "test-client", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dcResp.Interval != 5 {
		t.Errorf("Interval = %d, want 5 (default)", dcResp.Interval)
	}
}

func TestRequestDeviceCode_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid_client"}`))
	}))
	defer server.Close()

	_, err := requestDeviceCodeFromURL(context.Background(), "bad-client", server.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPollForToken_Success(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if gt := body["grant_type"]; gt != "urn:ietf:params:oauth:grant-type:device_code" {
			t.Errorf("grant_type = %q", gt)
		}
		if dc := body["device_code"]; dc != "dev-code-123" {
			t.Errorf("device_code = %q", dc)
		}
		if cid := body["client_id"]; cid != "test-client" {
			t.Errorf("client_id = %q", cid)
		}

		n := attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			// First 2 attempts return pending
			json.NewEncoder(w).Encode(OAuthTokenResponse{
				Error:            "authorization_pending",
				ErrorDescription: "waiting for user",
			})
			return
		}
		// 3rd attempt returns success
		json.NewEncoder(w).Encode(OAuthTokenResponse{
			AccessToken: "ghu_test_token_xyz",
			TokenType:   "bearer",
			Scope:       "read:user",
		})
	}))
	defer server.Close()

	// Use 1-second interval for fast testing
	tokenResp, err := pollForTokenFromURL(context.Background(), "test-client", "dev-code-123", 1, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenResp.AccessToken != "ghu_test_token_xyz" {
		t.Errorf("AccessToken = %q, want ghu_test_token_xyz", tokenResp.AccessToken)
	}
	if tokenResp.TokenType != "bearer" {
		t.Errorf("TokenType = %q, want bearer", tokenResp.TokenType)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("poll attempts = %d, want 3", got)
	}
}

func TestPollForTokenRejectsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, err := pollForTokenFromURL(context.Background(), "test-client", "dev-code", 1, server.URL)
	if err == nil || !strings.Contains(err.Error(), "neither an access token nor an OAuth error") {
		t.Fatalf("error = %v", err)
	}
}

func TestRequestGitHubTokenReportsServerStatusBeforeParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary outage", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := requestGitHubToken(context.Background(), "test-client", "dev-code", server.URL)
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("error = %v", err)
	}
}

func TestPollForToken_Expired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(OAuthTokenResponse{
			Error:            "expired_token",
			ErrorDescription: "the device code has expired",
		})
	}))
	defer server.Close()

	_, err := pollForTokenFromURL(context.Background(), "test-client", "dev-code-expired", 1, server.URL)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if got := err.Error(); got != "device code expired: the device code has expired" {
		t.Errorf("error = %q", got)
	}
}

func TestPollForToken_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(OAuthTokenResponse{
			Error:            "authorization_pending",
			ErrorDescription: "waiting for user",
		})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := pollForTokenFromURL(ctx, "test-client", "dev-code-123", 1, server.URL)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestExchangeForCopilotToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "token ghu_test_token" {
			t.Errorf("Authorization = %q, want 'token ghu_test_token'", auth)
		}
		if ua := r.Header.Get("User-Agent"); ua != userAgent {
			t.Errorf("User-Agent = %q, want %q", ua, userAgent)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "tid_copilot_session_token_abc",
			ExpiresAt: time.Now().Add(25 * time.Minute).Unix(),
		})
	}))
	defer server.Close()

	tokenResp, err := exchangeForCopilotTokenFromURL(context.Background(), "ghu_test_token", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenResp.Token != "tid_copilot_session_token_abc" {
		t.Errorf("Token = %q, want tid_copilot_session_token_abc", tokenResp.Token)
	}
	if tokenResp.ExpiresAt == 0 {
		t.Error("ExpiresAt should be non-zero")
	}
}

func TestExchangeForCopilotToken_EmptyGitHubToken(t *testing.T) {
	_, err := exchangeForCopilotTokenFromURL(context.Background(), "", "http://unused")
	if err == nil {
		t.Fatal("expected error for empty github token, got nil")
	}
	if !strings.Contains(err.Error(), "github token is required") {
		t.Errorf("error = %q, want 'github token is required'", err.Error())
	}
}

func TestExchangeForCopilotToken_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	_, err := exchangeForCopilotTokenFromURL(context.Background(), "ghu_bad_token", server.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want 401 status", err.Error())
	}
}

func TestCopilotTransport_InjectsHeaders(t *testing.T) {
	// Copilot token exchange server
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "session-token-abc",
			ExpiresAt: time.Now().Add(25 * time.Minute).Unix(),
		})
	}))
	defer tokenServer.Close()

	// Backend API server that checks all required headers
	var receivedHeaders http.Header
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer apiServer.Close()

	transport := &copilotTransport{
		base:            http.DefaultTransport,
		githubToken:     "ghu_test_token",
		copilotTokenURL: tokenServer.URL,
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// Check all required headers
	if auth := receivedHeaders.Get("Authorization"); auth != "Bearer session-token-abc" {
		t.Errorf("Authorization = %q, want 'Bearer session-token-abc'", auth)
	}
	if cid := receivedHeaders.Get("Copilot-Integration-Id"); cid != "vscode-chat" {
		t.Errorf("Copilot-Integration-Id = %q, want 'vscode-chat'", cid)
	}
	if ev := receivedHeaders.Get("Editor-Version"); ev != "vscode/1.107.0" {
		t.Errorf("Editor-Version = %q, want 'vscode/1.107.0'", ev)
	}
	if epv := receivedHeaders.Get("Editor-Plugin-Version"); epv != "copilot-chat/0.35.0" {
		t.Errorf("Editor-Plugin-Version = %q, want 'copilot-chat/0.35.0'", epv)
	}
	if ua := receivedHeaders.Get("User-Agent"); ua != userAgent {
		t.Errorf("User-Agent = %q, want %q", ua, userAgent)
	}
	if intent := receivedHeaders.Get("OpenAI-Intent"); intent != "conversation-panel" {
		t.Errorf("OpenAI-Intent = %q, want 'conversation-panel'", intent)
	}
}

func TestCopilotTransport_CachesSessionToken(t *testing.T) {
	var tokenExchangeCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenExchangeCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "session-token-cached",
			ExpiresAt: time.Now().Add(25 * time.Minute).Unix(),
		})
	}))
	defer tokenServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &copilotTransport{
		base:            http.DefaultTransport,
		githubToken:     "ghu_test_token",
		copilotTokenURL: tokenServer.URL,
	}

	client := &http.Client{Transport: transport}

	// Make two requests — the session token should be cached
	for i := 0; i < 2; i++ {
		resp, err := client.Get(apiServer.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
		resp.Body.Close()
	}

	if got := tokenExchangeCalls.Load(); got != 1 {
		t.Errorf("token exchange calls = %d, want 1 (should be cached)", got)
	}
}

func TestCopilotTransport_ReactiveRetryOn401(t *testing.T) {
	var tokenExchangeCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := tokenExchangeCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     fmt.Sprintf("session-token-%d", n),
			ExpiresAt: time.Now().Add(25 * time.Minute).Unix(),
		})
	}))
	defer tokenServer.Close()

	var apiCalls atomic.Int32
	var lastAuth string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		if apiCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &copilotTransport{
		base:             http.DefaultTransport,
		githubToken:      "ghu_test_token",
		sessionToken:     "stale-session-token",
		sessionExpiresAt: time.Now().Add(10 * time.Hour), // not expired by clock
		copilotTokenURL:  tokenServer.URL,
	}

	resp, err := (&http.Client{Transport: transport}).Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (retry should succeed)", resp.StatusCode)
	}
	if lastAuth != "Bearer session-token-1" {
		t.Errorf("retry Authorization = %q, want Bearer session-token-1", lastAuth)
	}
	if got := apiCalls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2 (original + retry)", got)
	}
}

func TestCopilotTransport_ReactiveRetryOn403(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "fresh-session-403",
			ExpiresAt: time.Now().Add(25 * time.Minute).Unix(),
		})
	}))
	defer tokenServer.Close()

	var apiCalls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &copilotTransport{
		base:             http.DefaultTransport,
		githubToken:      "ghu_test_token",
		sessionToken:     "stale-session-token",
		sessionExpiresAt: time.Now().Add(10 * time.Hour),
		copilotTokenURL:  tokenServer.URL,
	}

	resp, err := (&http.Client{Transport: transport}).Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := apiCalls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2", got)
	}
}

func TestCopilotTransport_ExchangeFailureReturnsReauthRequired(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer tokenServer.Close()

	transport := &copilotTransport{
		base:            http.DefaultTransport,
		githubToken:     "ghu_bad_token",
		copilotTokenURL: tokenServer.URL,
	}

	client := &http.Client{Transport: transport}
	_, err := client.Get("https://api.example.invalid")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want errors.Is ErrReauthRequired", err)
	}
}

func TestListModels(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "token ghu_test_token" {
			t.Errorf("token exchange Authorization = %q, want 'token ghu_test_token'", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotTokenResponse{
			Token:     "session-for-models",
			ExpiresAt: time.Now().Add(25 * time.Minute).Unix(),
		})
	}))
	defer tokenServer.Close()

	modelsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer session-for-models" {
			t.Errorf("models Authorization = %q, want 'Bearer session-for-models'", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o","object":"model","created":1700000000,"owned_by":"copilot"},{"id":"claude-sonnet-4","object":"model","created":1690000000,"owned_by":"copilot"},{"id":"o3-mini","object":"model","created":1680000000,"owned_by":"copilot"}]}`)
	}))
	defer modelsServer.Close()

	models, err := listModelsFromURL(context.Background(), "ghu_test_token", modelsServer.URL, tokenServer.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	// Should be sorted
	if models[0] != "claude-sonnet-4" || models[1] != "gpt-4o" || models[2] != "o3-mini" {
		t.Errorf("models = %v, want [claude-sonnet-4 gpt-4o o3-mini]", models)
	}
}

func TestListModels_EmptyToken(t *testing.T) {
	_, err := listModelsFromURL(context.Background(), "", "http://unused", "http://unused")
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestListModels_TokenExchangeFails(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"You don't have a Copilot subscription"}`))
	}))
	defer tokenServer.Close()

	_, err := listModelsFromURL(context.Background(), "ghu_no_subscription", "http://unused", tokenServer.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "exchange copilot token") {
		t.Errorf("error = %q, want to contain 'exchange copilot token'", err.Error())
	}
}

func TestNextPollingIntervalAccumulatesSlowDown(t *testing.T) {
	interval := 5
	interval = nextPollingInterval(interval)
	interval = nextPollingInterval(interval)
	if interval != 15 {
		t.Fatalf("interval = %d, want 15", interval)
	}
}
