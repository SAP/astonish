package xai_oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestOAuthTransport_ProactiveRefreshThen401(t *testing.T) {
	var rotations atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rotations.Add(1)
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: fmt.Sprintf("token-%d", n), RefreshToken: fmt.Sprintf("refresh-%d", n), ExpiresIn: 3600})
	}))
	defer tokens.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-2" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer api.Close()
	tr := NewOAuthTransport("client", "expired", "refresh", time.Time{}, nil)
	tr.tokenURL = tokens.URL
	resp, err := (&http.Client{Transport: tr}).Get(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || rotations.Load() != 2 {
		t.Fatalf("status=%d rotations=%d", resp.StatusCode, rotations.Load())
	}
}

func TestOAuthGenerationAuthRecovery(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var rotations atomic.Int32
			tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := rotations.Add(1)
				r.ParseForm()
				want := "refresh"
				if n == 2 {
					want = "refresh-1"
				}
				if r.FormValue("refresh_token") != want {
					t.Errorf("rotation used %q want %q", r.FormValue("refresh_token"), want)
				}
				json.NewEncoder(w).Encode(TokenResponse{AccessToken: fmt.Sprintf("token-%d", n), RefreshToken: fmt.Sprintf("refresh-%d", n), ExpiresIn: 3600})
			}))
			defer tokens.Close()
			var calls int
			var bodies, auths []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				auths = append(auths, r.Header.Get("Authorization"))
				calls++
				if calls == 2 {
					w.WriteHeader(401)
					fmt.Fprint(w, `{"error":{"message":"revoked","type":"authentication_error"}}`)
					return
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			defer api.Close()
			tr := NewOAuthTransport("client", "expired", "refresh", time.Time{}, nil)
			tr.tokenURL = tokens.URL
			cfg := openai.DefaultConfig("placeholder")
			cfg.BaseURL = api.URL
			cfg.HTTPClient = &http.Client{Transport: tr}
			p := openai_provider.NewProvider(openai.NewClientWithConfig(cfg), "grok", true)
			req := &model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hello"}}}}}
			for i := 0; i < 3; i++ {
				seen := false
				for response, err := range p.GenerateContent(context.Background(), req, streaming) {
					if err != nil {
						t.Fatal(err)
					}
					if response != nil && response.Content != nil {
						seen = true
					}
				}
				if !seen {
					t.Fatal("no generation output")
				}
			}
			if calls != 4 || rotations.Load() != 2 || bodies[1] != bodies[2] || auths[0] != "Bearer token-1" || auths[1] != "Bearer token-1" || auths[2] != "Bearer token-2" || auths[3] != "Bearer token-2" {
				t.Fatalf("calls=%d rotations=%d auth=%v bodies=%v", calls, rotations.Load(), auths, bodies)
			}
		})
	}
}

func TestOAuthTransport_Simultaneous401SingleRotation(t *testing.T) {
	var rotations atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rotations.Add(1)
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "new", ExpiresIn: 3600})
	}))
	defer tokens.Close()
	const count = 8
	var arrived atomic.Int32
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer old" {
			if arrived.Add(1) == count {
				close(release)
			}
			<-release
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer api.Close()
	tr := NewOAuthTransport("client", "old", "refresh", time.Now().Add(time.Hour), nil)
	tr.tokenURL = tokens.URL
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := (&http.Client{Transport: tr}).Get(api.URL)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("status=%d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	if rotations.Load() != 1 {
		t.Fatalf("rotations=%d", rotations.Load())
	}
}

func TestOAuthTransport_SecondRejectionAndLaterRecovery(t *testing.T) {
	var rotations, calls atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rotations.Add(1)
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: fmt.Sprint(n), ExpiresIn: 3600})
	}))
	defer tokens.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(401)
		} else {
			w.WriteHeader(200)
		}
	}))
	defer api.Close()
	tr := NewOAuthTransport("client", "old", "refresh", time.Now().Add(time.Hour), nil)
	tr.tokenURL = tokens.URL
	client := &http.Client{Transport: tr}
	for _, want := range []int{401, 200} {
		resp, err := client.Get(api.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("status=%d want=%d", resp.StatusCode, want)
		}
	}
	if rotations.Load() != 2 {
		t.Fatalf("rotations=%d", rotations.Load())
	}
}

func TestOAuthTransport_CancellationAndUnavailableReplay(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(401) }))
	defer api.Close()
	tr := NewOAuthTransport("client", "old", "refresh", time.Now().Add(time.Hour), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", api.URL, nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	req, _ = http.NewRequest("POST", api.URL, io.NopCloser(strings.NewReader("body")))
	if _, err := tr.RoundTrip(req); !errors.Is(err, httpool.ErrAuthReplayUnavailable) {
		t.Fatalf("replay=%v", err)
	}
	tr.mu.Lock()
	cached := tr.accessToken
	tr.mu.Unlock()
	if cached != "" {
		t.Fatal("rejected token retained")
	}
}

func TestOAuthRefreshMalformedAndSanitized(t *testing.T) {
	for _, body := range []string{`{"access_token":"secret","expires_in":0}`, `{"expires_in":3600}`, `{"access_token":"secret","expires_in":-1}`, `{"error":"secret","error_description":"secret"}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			_, err := refreshAccessTokenFromURL(context.Background(), "client", "refresh", server.URL)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"access_token":"secret","expires_in":3600}`)
	}))
	defer server.Close()
	if _, err := refreshAccessTokenFromURL(context.Background(), "client", "refresh", server.URL); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%v", err)
	}
}

func TestOAuthRefreshNotificationsOrderedAndReentrant(t *testing.T) {
	tr := NewOAuthTransport("client", "new", "refresh", time.Now().Add(time.Hour), nil)
	tr.generation = 2
	var notified []string
	tr.onTokenRefresh = func(access, refresh string, expiry time.Time) {
		token, err := tr.acquireToken(context.Background(), "")
		if err != nil || token != "new" {
			t.Errorf("reentry %q %v", token, err)
		}
		notified = append(notified, access)
	}
	tr.notifyRefresh(2, "new", "refresh", tr.expiresAt)
	tr.notifyRefresh(1, "old", "old-refresh", tr.expiresAt)
	if len(notified) != 1 || notified[0] != "new" {
		t.Fatalf("notifications=%v", notified)
	}
}

func TestOAuthModelListingRecovery(t *testing.T) {
	var rotations atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rotations.Add(1)
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: fmt.Sprint(n), ExpiresIn: 3600})
	}))
	defer tokens.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer 2" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"b"},{"id":"a"}]}`)
	}))
	defer api.Close()
	var notifications atomic.Int32
	tr := NewOAuthTransport("client", "old", "refresh", time.Time{}, func(string, string, time.Time) { notifications.Add(1) })
	tr.tokenURL = tokens.URL
	models, err := listModelsWithClient(context.Background(), "old", api.URL, &http.Client{Transport: tr})
	if err != nil || strings.Join(models, ",") != "a,b" || notifications.Load() != 2 {
		t.Fatalf("models=%v error=%v notifications=%d", models, err, notifications.Load())
	}
}

func TestOAuthRefreshConcurrentNotificationsStayOrdered(t *testing.T) {
	tr := NewOAuthTransport("client", "one", "refresh", time.Now().Add(time.Hour), nil)
	tr.generation = 1
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var notified []string
	tr.onTokenRefresh = func(access, refresh string, expiry time.Time) {
		if access == "one" {
			close(started)
			<-release
		}
		notified = append(notified, access)
	}
	go func() { tr.notifyRefresh(1, "one", "refresh", time.Now().Add(time.Hour)); close(finished) }()
	<-started
	// A new token can be published while persistence is blocked: mu is free.
	tr.mu.Lock()
	tr.generation = 2
	tr.accessToken = "two"
	tr.mu.Unlock()
	newerDone := make(chan struct{})
	go func() { tr.notifyRefresh(2, "two", "refresh", time.Now().Add(time.Hour)); close(newerDone) }()
	close(release)
	<-finished
	<-newerDone
	if strings.Join(notified, ",") != "one,two" {
		t.Fatalf("notifications=%v", notified)
	}
	// A delayed older notification must never overwrite the newest value.
	tr.notifyRefresh(1, "one", "refresh", time.Now().Add(time.Hour))
	if strings.Join(notified, ",") != "one,two" {
		t.Fatalf("stale notification=%v", notified)
	}
}

func TestOAuthTransport_ReactiveRenewalFailureAndRecovery(t *testing.T) {
	var attempts atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":"secret"}`)
			return
		}
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "new", ExpiresIn: 3600})
	}))
	defer tokens.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer api.Close()
	tr := NewOAuthTransport("client", "old", "refresh", time.Now().Add(time.Hour), nil)
	tr.tokenURL = tokens.URL
	if _, err := (&http.Client{Transport: tr}).Get(api.URL); !errors.Is(err, ErrReauthRequired) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%v", err)
	}
	resp, err := (&http.Client{Transport: tr}).Get(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || attempts.Load() != 2 {
		t.Fatalf("status=%d attempts=%d", resp.StatusCode, attempts.Load())
	}
}

func TestOAuthTransport_CancellationDuringRenewal(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(500) }))
	defer tokens.Close()
	tr := NewOAuthTransport("client", "old", "refresh", time.Time{}, nil)
	tr.tokenURL = tokens.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := tr.acquireToken(ctx, ""); done <- err }()
	<-started
	cancel()
	err := <-done
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

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
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want errors.Is ErrReauthRequired", err)
	}
}

func TestOAuthTransport_NoRefreshTokenExpired(t *testing.T) {
	// An expired access token with no refresh token cannot be renewed; the
	// transport must return ErrReauthRequired without making any HTTP call.
	var called atomic.Bool
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "expired-token",
		refreshToken: "",
		expiresAt:    time.Now().Add(-time.Minute),
	}
	client := &http.Client{Transport: transport}
	_, err := client.Get(apiServer.URL)
	if err == nil || !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want errors.Is ErrReauthRequired", err)
	}
	if called.Load() {
		t.Error("expected no HTTP request to be made when re-auth is required")
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

func TestOAuthTransport_ReactiveRetryOn401(t *testing.T) {
	// Token server returns a fresh access token.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "fresh-token", RefreshToken: "new-rt", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	// API server rejects the first call with 401, then accepts the retry.
	var calls atomic.Int32
	var lastAuth string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	var refreshCalled bool
	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "stale-token",
		refreshToken: "rt",
		expiresAt:    time.Now().Add(10 * time.Hour), // not expired by clock
		tokenURL:     tokenServer.URL,
		onTokenRefresh: func(at, rt string, _ time.Time) {
			refreshCalled = true
		},
	}

	resp, err := (&http.Client{Transport: transport}).Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (retry should succeed)", resp.StatusCode)
	}
	if !refreshCalled {
		t.Error("onTokenRefresh not called on reactive retry")
	}
	if lastAuth != "Bearer fresh-token" {
		t.Errorf("retry Authorization = %q, want Bearer fresh-token", lastAuth)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2 (original + retry)", got)
	}
}

func TestOAuthTransport_ReactiveRetryOn403(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "fresh-token-403", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	var calls atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"unauthenticated:bad-credentials","error":"The OAuth2 access token could not be validated."}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "stale-token",
		refreshToken: "rt",
		expiresAt:    time.Now().Add(10 * time.Hour),
		tokenURL:     tokenServer.URL,
	}

	resp, err := (&http.Client{Transport: transport}).Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2", got)
	}
}

func TestOAuthTransport_ZeroExpiresAtTriggersRefresh(t *testing.T) {
	// When expiresAt is zero (legacy config / missing), the transport should
	// proactively refresh rather than sending a potentially stale token.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "refreshed-zero-exp", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	var receivedAuth string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "maybe-stale-token",
		refreshToken: "valid-rt",
		expiresAt:    time.Time{}, // zero — should trigger refresh
		tokenURL:     tokenServer.URL,
	}

	resp, err := (&http.Client{Transport: transport}).Get(apiServer.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedAuth != "Bearer refreshed-zero-exp" {
		t.Errorf("Authorization = %q, want Bearer refreshed-zero-exp", receivedAuth)
	}
}

// TestOAuthTransport_ReactiveRetryResendsBody guards the POST path: the first
// RoundTrip consumes req.Body, so the retry must rewind it via GetBody or the
// server would receive an empty body on the retried request.
func TestOAuthTransport_ReactiveRetryResendsBody(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "fresh-token", RefreshToken: "new-rt", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	var calls atomic.Int32
	var bodies []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	transport := &oauthTransport{
		base:         http.DefaultTransport,
		clientID:     "test-client",
		accessToken:  "stale-token",
		refreshToken: "rt",
		expiresAt:    time.Now().Add(10 * time.Hour),
		tokenURL:     tokenServer.URL,
	}

	const payload = `{"model":"grok","messages":[]}`
	resp, err := (&http.Client{Transport: transport}).Post(apiServer.URL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	if bodies[1] != payload {
		t.Errorf("retried body = %q, want %q (body not rewound)", bodies[1], payload)
	}
}
