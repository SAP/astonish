package xai_oauth

import (
	"context"
	"encoding/json"
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
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("expected Content-Type application/x-www-form-urlencoded, got %s", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if cid := r.FormValue("client_id"); cid != "test-client-id" {
			t.Errorf("expected client_id=test-client-id, got %s", cid)
		}

		resp := DeviceCodeResponse{
			DeviceCode:              "dev-code-123",
			UserCode:                "ABCD-1234",
			VerificationURI:         "https://accounts.x.ai/oauth2/device",
			VerificationURIComplete: "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234",
			ExpiresIn:               600,
			Interval:                5,
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
	if dcResp.VerificationURIComplete != "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234" {
		t.Errorf("VerificationURIComplete = %q", dcResp.VerificationURIComplete)
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
			VerificationURI: "https://accounts.x.ai/oauth2/device",
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
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if gt := r.FormValue("grant_type"); gt != "urn:ietf:params:oauth:grant-type:device_code" {
			t.Errorf("grant_type = %q", gt)
		}
		if dc := r.FormValue("device_code"); dc != "dev-code-123" {
			t.Errorf("device_code = %q", dc)
		}
		if cid := r.FormValue("client_id"); cid != "test-client" {
			t.Errorf("client_id = %q", cid)
		}

		n := attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			// First 2 attempts return pending
			json.NewEncoder(w).Encode(TokenResponse{
				Error:            "authorization_pending",
				ErrorDescription: "waiting for user",
			})
			return
		}
		// 3rd attempt returns success
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "access-token-xyz",
			RefreshToken: "refresh-token-abc",
			ExpiresIn:    3600,
			TokenType:    "Bearer",
		})
	}))
	defer server.Close()

	// Use 1-second interval for fast testing
	tokenResp, err := pollForTokenFromURL(context.Background(), "test-client", "dev-code-123", 1, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenResp.AccessToken != "access-token-xyz" {
		t.Errorf("AccessToken = %q, want access-token-xyz", tokenResp.AccessToken)
	}
	if tokenResp.RefreshToken != "refresh-token-abc" {
		t.Errorf("RefreshToken = %q, want refresh-token-abc", tokenResp.RefreshToken)
	}
	if tokenResp.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", tokenResp.ExpiresIn)
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

func TestRequestTokenReportsServerStatusBeforeParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary outage", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := requestToken(context.Background(), "test-client", "dev-code", server.URL)
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Fatalf("error = %v", err)
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

func TestPollForToken_Expired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
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
		json.NewEncoder(w).Encode(TokenResponse{
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

func TestRefreshAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if gt := r.FormValue("grant_type"); gt != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", gt)
		}
		if rt := r.FormValue("refresh_token"); rt != "old-refresh-token" {
			t.Errorf("refresh_token = %q", rt)
		}
		if cid := r.FormValue("client_id"); cid != "test-client" {
			t.Errorf("client_id = %q", cid)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    7200,
			TokenType:    "Bearer",
		})
	}))
	defer server.Close()

	tokenResp, err := refreshAccessTokenFromURL(context.Background(), "test-client", "old-refresh-token", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenResp.AccessToken != "new-access-token" {
		t.Errorf("AccessToken = %q, want new-access-token", tokenResp.AccessToken)
	}
	if tokenResp.RefreshToken != "new-refresh-token" {
		t.Errorf("RefreshToken = %q, want new-refresh-token", tokenResp.RefreshToken)
	}
}

func TestRefreshAccessToken_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			Error:            "invalid_grant",
			ErrorDescription: "refresh token revoked",
		})
	}))
	defer server.Close()

	_, err := refreshAccessTokenFromURL(context.Background(), "test-client", "revoked-token", server.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestOAuthTransport_RefreshesExpiredToken(t *testing.T) {
	// Token server that returns a new access token
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "refreshed-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    3600,
			TokenType:    "Bearer",
		})
	}))
	defer tokenServer.Close()

	// Backend API server that checks the Authorization header
	var receivedAuth string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer apiServer.Close()

	// Create transport with an already-expired token
	var refreshCalled bool
	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "old-expired-token",
		refreshToken: "valid-refresh-token",
		expiresAt:    time.Now().Add(-10 * time.Minute), // Already expired
		tokenURL:     tokenServer.URL,
		onTokenRefresh: func(at, rt string, exp time.Time) {
			refreshCalled = true
			if at != "refreshed-access-token" {
				t.Errorf("onTokenRefresh accessToken = %q", at)
			}
		},
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if !refreshCalled {
		t.Error("onTokenRefresh was not called")
	}
	if receivedAuth != "Bearer refreshed-access-token" {
		t.Errorf("Authorization = %q, want Bearer refreshed-access-token", receivedAuth)
	}
}

func TestOAuthTransportCallsRefreshCallbackWithoutLock(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "new-token", ExpiresIn: 3600})
	}))
	defer tokenServer.Close()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "old-token",
		refreshToken: "refresh-token",
		expiresAt:    time.Now().Add(-time.Minute),
		tokenURL:     tokenServer.URL,
	}
	transport.onTokenRefresh = func(string, string, time.Time) {
		if !transport.mu.TryLock() {
			t.Error("transport mutex held during refresh callback")
			return
		}
		transport.mu.Unlock()
	}

	resp, err := (&http.Client{Transport: transport}).Get(apiServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestOAuthTransport_ReturnsRefreshError(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(TokenResponse{Error: "invalid_grant", ErrorDescription: "revoked"})
	}))
	defer tokenServer.Close()

	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "expired-token",
		refreshToken: "revoked-token",
		expiresAt:    time.Now().Add(-time.Minute),
		tokenURL:     tokenServer.URL,
	}
	client := &http.Client{Transport: transport}
	_, err := client.Get("https://api.example.invalid")
	if err == nil || !strings.Contains(err.Error(), "refresh xAI OAuth token") {
		t.Fatalf("request error = %v, want refresh error", err)
	}
}

func TestOAuthTransport_UsesExistingValidToken(t *testing.T) {
	// Backend API server that checks the Authorization header
	var receivedAuth string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	// Create transport with a valid (not expired) token
	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "valid-token",
		refreshToken: "refresh-token",
		expiresAt:    time.Now().Add(2 * time.Hour), // Still valid
	}

	client := &http.Client{Transport: transport}
	resp, err := client.Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedAuth != "Bearer valid-token" {
		t.Errorf("Authorization = %q, want Bearer valid-token", receivedAuth)
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"grok-3","object":"model","created":1700000000,"owned_by":"xai"},{"id":"grok-2","object":"model","created":1690000000,"owned_by":"xai"}]}`)
	}))
	defer server.Close()

	models, err := listModelsFromURL(context.Background(), "test-token", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	// Should be sorted
	if models[0] != "grok-2" || models[1] != "grok-3" {
		t.Errorf("models = %v, want [grok-2 grok-3]", models)
	}
}

func TestListModels_EmptyToken(t *testing.T) {
	_, err := listModelsFromURL(context.Background(), "", "http://unused")
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}
