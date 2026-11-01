package xai_oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
)

// ErrReauthRequired signals that the user must run the device-code flow again.
var ErrReauthRequired = errors.New("xai oauth: re-authentication required")

// oauthTransport owns the token cache and serializes refresh-token rotation.
type oauthTransport struct {
	base                                http.RoundTripper
	clientID, accessToken, refreshToken string
	expiresAt                           time.Time
	mu                                  sync.Mutex
	notificationMu                      sync.Mutex
	generation                          uint64
	onTokenRefresh                      func(string, string, time.Time)
	tokenURL                            string
}

func NewOAuthTransport(clientID, accessToken, refreshToken string, expiresAt time.Time, onRefresh func(string, string, time.Time)) *oauthTransport {
	return &oauthTransport{base: httpool.Transport(), clientID: clientID, accessToken: accessToken, refreshToken: refreshToken, expiresAt: expiresAt, onTokenRefresh: onRefresh}
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return httpool.RoundTripWithToken(req, t.base, t.acquireToken, t.invalidateToken,
		func(r *http.Request, token string) { r.Header.Set("Authorization", "Bearer "+token) },
		func(status int) bool {
			t.mu.Lock()
			renewable := t.refreshToken != ""
			t.mu.Unlock()
			return renewable && (status == http.StatusUnauthorized || status == http.StatusForbidden)
		})
}

// invalidateToken must not discard a token rotated by another in-flight request.
func (t *oauthTransport) invalidateToken(rejected string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.accessToken == rejected {
		t.accessToken = ""
	}
}

func (t *oauthTransport) acquireToken(ctx context.Context, rejected string) (string, error) {
	t.mu.Lock()
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()
		return "", err
	}
	valid := t.accessToken != "" && (t.expiresAt.IsZero() || time.Until(t.expiresAt) >= 60*time.Second)
	// Access-only legacy credentials have no known expiration and remain usable.
	needsRefresh := !valid || (t.refreshToken != "" && t.expiresAt.IsZero()) || (rejected != "" && t.accessToken == rejected)
	if !needsRefresh {
		token := t.accessToken
		t.mu.Unlock()
		return token, nil
	}
	if t.refreshToken == "" {
		// Preserve access-only tokens until actual expiry, not the refresh buffer.
		if rejected == "" && t.accessToken != "" && (t.expiresAt.IsZero() || time.Until(t.expiresAt) > 0) {
			token := t.accessToken
			t.mu.Unlock()
			return token, nil
		}
		t.mu.Unlock()
		return "", fmt.Errorf("%w: xAI access token expired and no refresh token available", ErrReauthRequired)
	}
	if err := t.doRefreshLocked(ctx); err != nil {
		t.mu.Unlock()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: refresh xAI OAuth token: %w", ErrReauthRequired, err)
	}
	token, refresh, expiry, generation := t.accessToken, t.refreshToken, t.expiresAt, t.generation
	t.mu.Unlock()
	t.notifyRefresh(generation, token, refresh, expiry)
	return token, nil
}

// Persistence runs outside the token lock, so callbacks can reenter a valid
// transport. Serial notification plus a generation check prevents stale writes.
func (t *oauthTransport) notifyRefresh(generation uint64, token, refresh string, expiry time.Time) {
	if t.onTokenRefresh == nil {
		return
	}
	t.notificationMu.Lock()
	defer t.notificationMu.Unlock()
	t.mu.Lock()
	current := t.generation == generation
	t.mu.Unlock()
	if current {
		t.onTokenRefresh(token, refresh, expiry)
	}
}

// doRefreshLocked runs while holding mu; unsuccessful renewal changes no state.
func (t *oauthTransport) doRefreshLocked(ctx context.Context) error {
	endpoint := tokenURL
	if t.tokenURL != "" {
		endpoint = t.tokenURL
	}
	response, err := refreshAccessTokenFromURL(ctx, t.clientID, t.refreshToken, endpoint)
	if err != nil {
		return err
	}
	t.accessToken = response.AccessToken
	if response.RefreshToken != "" {
		t.refreshToken = response.RefreshToken
	}
	t.expiresAt = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	t.generation++
	return nil
}
