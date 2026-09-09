package xai_oauth

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ErrReauthRequired is a sentinel error indicating that the xAI OAuth tokens
// can no longer be renewed automatically and the user must re-authenticate
// (run the device-code flow again). Callers detect it with errors.Is; it is
// wrapped around both the refresh-failure case and the case where the access
// token is expired with no usable refresh token.
var ErrReauthRequired = errors.New("xai oauth: re-authentication required")

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

	var refreshed bool
	// Refresh if token is expired or will expire within 60 seconds
	if t.refreshToken != "" && !t.expiresAt.IsZero() && time.Until(t.expiresAt) < 60*time.Second {
		endpoint := tokenURL
		if t.tokenURL != "" {
			endpoint = t.tokenURL
		}
		tokenResp, err := refreshAccessTokenFromURL(req.Context(), t.clientID, t.refreshToken, endpoint)
		if err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("%w: refresh xAI OAuth token: %v", ErrReauthRequired, err)
		}
		t.accessToken = tokenResp.AccessToken
		if tokenResp.RefreshToken != "" {
			t.refreshToken = tokenResp.RefreshToken
		}
		t.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		refreshed = true
	}

	token := t.accessToken
	refreshToken := t.refreshToken
	expiresAt := t.expiresAt
	onTokenRefresh := t.onTokenRefresh
	t.mu.Unlock()

	// Terminal case: the access token is expired and there is no refresh token
	// to renew it (e.g. the refresh token was never present or was cleared).
	// No automatic recovery is possible — signal that the user must re-auth.
	if !expiresAt.IsZero() && time.Until(expiresAt) < 0 && refreshToken == "" {
		return nil, fmt.Errorf("%w: xAI access token expired and no refresh token available", ErrReauthRequired)
	}

	if refreshed && onTokenRefresh != nil {
		onTokenRefresh(token, refreshToken, expiresAt)
	}

	// Clone the request to avoid mutating the original
	reqClone := req.Clone(req.Context())
	reqClone.Header.Set("Authorization", "Bearer "+token)

	return t.base.RoundTrip(reqClone)
}
