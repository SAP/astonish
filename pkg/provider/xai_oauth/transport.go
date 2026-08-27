package xai_oauth

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// oauthTransport is an http.RoundTripper that automatically manages OAuth
// access tokens, refreshing them when they are about to expire.
type oauthTransport struct {
	base         http.RoundTripper
	clientID     string
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	mu           sync.Mutex

	// onTokenRefresh is an optional callback invoked after a successful token
	// refresh, allowing the caller to persist the new tokens.
	onTokenRefresh func(accessToken, refreshToken string, expiresAt time.Time)

	// tokenURL allows overriding the token endpoint for testing.
	tokenURL string
}

// NewOAuthTransport creates a new HTTP transport that injects OAuth Bearer
// tokens and auto-refreshes them before expiry.
func NewOAuthTransport(clientID, accessToken, refreshToken string, expiresAt time.Time, onRefresh func(string, string, time.Time)) *oauthTransport {
	return &oauthTransport{
		base:           http.DefaultTransport,
		clientID:       clientID,
		accessToken:    accessToken,
		refreshToken:   refreshToken,
		expiresAt:      expiresAt,
		onTokenRefresh: onRefresh,
	}
}

// RoundTrip implements http.RoundTripper. It checks whether the current access
// token is expired (with a 60-second buffer) and refreshes it if needed before
// attaching the Authorization header.
func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()

	// Refresh if token is expired or will expire within 60 seconds
	if t.refreshToken != "" && !t.expiresAt.IsZero() && time.Until(t.expiresAt) < 60*time.Second {
		endpoint := tokenURL
		if t.tokenURL != "" {
			endpoint = t.tokenURL
		}
		tokenResp, err := refreshAccessTokenFromURL(context.Background(), t.clientID, t.refreshToken, endpoint)
		if err == nil {
			t.accessToken = tokenResp.AccessToken
			if tokenResp.RefreshToken != "" {
				t.refreshToken = tokenResp.RefreshToken
			}
			t.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

			if t.onTokenRefresh != nil {
				t.onTokenRefresh(t.accessToken, t.refreshToken, t.expiresAt)
			}
		}
		// If refresh fails, try with the existing token anyway
	}

	token := t.accessToken
	t.mu.Unlock()

	// Clone the request to avoid mutating the original
	reqClone := req.Clone(req.Context())
	reqClone.Header.Set("Authorization", "Bearer "+token)

	return t.base.RoundTrip(reqClone)
}
