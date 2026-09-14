package xai_oauth

import (
	"context"
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
// attaching the Authorization header. If the upstream server returns 401 or 403
// despite a seemingly valid token, the transport refreshes and retries once.
func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()

	var refreshed bool
	// Refresh if token is expired or will expire within 60 seconds.
	// Also refresh when expiresAt is zero — that means the expiration time
	// was never stored (legacy config), so the token may well be stale.
	needsRefresh := t.refreshToken != "" && (t.expiresAt.IsZero() || time.Until(t.expiresAt) < 60*time.Second)
	if needsRefresh {
		if err := t.doRefreshLocked(req.Context()); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("%w: refresh xAI OAuth token: %v", ErrReauthRequired, err)
		}
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

	resp, err := t.base.RoundTrip(reqClone)
	if err != nil {
		return nil, err
	}

	// Reactive retry: if the server rejected the token (401/403), refresh once
	// and retry. This covers clock skew, early server-side revocation, or a
	// token that was valid by our clock but invalid server-side.
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !refreshed && t.refreshToken != "" {
		resp.Body.Close()

		t.mu.Lock()
		if err := t.doRefreshLocked(req.Context()); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("%w: refresh xAI OAuth token after %d: %v", ErrReauthRequired, resp.StatusCode, err)
		}
		token = t.accessToken
		refreshToken = t.refreshToken
		expiresAt = t.expiresAt
		onTokenRefresh = t.onTokenRefresh
		t.mu.Unlock()

		if onTokenRefresh != nil {
			onTokenRefresh(token, refreshToken, expiresAt)
		}

		retryClone := req.Clone(req.Context())
		// The first RoundTrip consumed req.Body; rewind it so a POST body is
		// actually resent. Without this the retry would deliver an empty body.
		if req.GetBody != nil {
			body, berr := req.GetBody()
			if berr != nil {
				return nil, fmt.Errorf("%w: rewind request body for retry after %d: %v", ErrReauthRequired, resp.StatusCode, berr)
			}
			retryClone.Body = body
		}
		retryClone.Header.Set("Authorization", "Bearer "+token)
		return t.base.RoundTrip(retryClone)
	}

	return resp, nil
}

// doRefreshLocked performs the token refresh while the caller holds t.mu.
func (t *oauthTransport) doRefreshLocked(ctx context.Context) error {
	endpoint := tokenURL
	if t.tokenURL != "" {
		endpoint = t.tokenURL
	}
	tokenResp, err := refreshAccessTokenFromURL(ctx, t.clientID, t.refreshToken, endpoint)
	if err != nil {
		return err
	}
	t.accessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		t.refreshToken = tokenResp.RefreshToken
	}
	t.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return nil
}
