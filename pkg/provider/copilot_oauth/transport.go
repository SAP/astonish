package copilot_oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ErrReauthRequired is a sentinel error indicating that the Copilot OAuth tokens
// can no longer be renewed automatically and the user must re-authenticate
// (run the device-code flow again).
var ErrReauthRequired = errors.New("copilot oauth: re-authentication required")

// copilotTransport is an http.RoundTripper that manages the two-step Copilot
// token model: a persistent GitHub OAuth token (ghu_*) is exchanged for a
// short-lived (~25 min) Copilot session token that is cached and refreshed
// automatically.
type copilotTransport struct {
	base http.RoundTripper

	// githubToken is the long-lived GitHub OAuth token (ghu_*).
	githubToken string

	// sessionToken is the short-lived Copilot session token.
	sessionToken    string
	sessionExpiresAt time.Time
	mu              sync.Mutex

	// copilotTokenURL allows overriding the token exchange endpoint for testing.
	copilotTokenURL string
}

// NewCopilotTransport creates a new HTTP transport that manages the Copilot
// two-step token exchange. It accepts the persistent GitHub OAuth token and
// transparently obtains/refreshes the short-lived Copilot session token.
func NewCopilotTransport(githubToken string) *copilotTransport {
	return &copilotTransport{
		base:        http.DefaultTransport,
		githubToken: githubToken,
	}
}

// RoundTrip implements http.RoundTripper. It ensures a valid Copilot session
// token exists (refreshing proactively with a 60-second buffer), injects
// the required Copilot headers, and retries once on 401/403.
func (t *copilotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()

	var refreshed bool
	// Refresh if session token is empty or expired (with 60s buffer).
	needsRefresh := t.sessionToken == "" || t.sessionExpiresAt.IsZero() || time.Until(t.sessionExpiresAt) < 60*time.Second
	if needsRefresh {
		if err := t.doRefreshLocked(req.Context()); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("%w: exchange copilot session token: %v", ErrReauthRequired, err)
		}
		refreshed = true
	}

	token := t.sessionToken
	t.mu.Unlock()

	// Clone the request to avoid mutating the original.
	reqClone := req.Clone(req.Context())
	reqClone.Header.Set("Authorization", "Bearer "+token)
	reqClone.Header.Set("Copilot-Integration-Id", "vscode-chat")
	reqClone.Header.Set("Editor-Version", "vscode/1.107.0")
	reqClone.Header.Set("Editor-Plugin-Version", "copilot-chat/0.35.0")
	reqClone.Header.Set("User-Agent", userAgent)
	reqClone.Header.Set("OpenAI-Intent", "conversation-panel")

	resp, err := t.base.RoundTrip(reqClone)
	if err != nil {
		return nil, err
	}

	// Reactive retry: if the server rejected the token (401/403), refresh
	// the session token once and retry.
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && !refreshed {
		resp.Body.Close()

		t.mu.Lock()
		if err := t.doRefreshLocked(req.Context()); err != nil {
			t.mu.Unlock()
			return nil, fmt.Errorf("%w: refresh copilot session token after %d: %v", ErrReauthRequired, resp.StatusCode, err)
		}
		token = t.sessionToken
		t.mu.Unlock()

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
		retryClone.Header.Set("Copilot-Integration-Id", "vscode-chat")
		retryClone.Header.Set("Editor-Version", "vscode/1.107.0")
		retryClone.Header.Set("Editor-Plugin-Version", "copilot-chat/0.35.0")
		retryClone.Header.Set("User-Agent", userAgent)
		retryClone.Header.Set("OpenAI-Intent", "conversation-panel")
		return t.base.RoundTrip(retryClone)
	}

	return resp, nil
}

// doRefreshLocked exchanges the GitHub token for a fresh Copilot session token.
// The caller must hold t.mu.
func (t *copilotTransport) doRefreshLocked(ctx context.Context) error {
	endpoint := copilotTokenURL
	if t.copilotTokenURL != "" {
		endpoint = t.copilotTokenURL
	}
	tokenResp, err := exchangeForCopilotTokenFromURL(ctx, t.githubToken, endpoint)
	if err != nil {
		return err
	}
	t.sessionToken = tokenResp.Token
	t.sessionExpiresAt = time.Unix(tokenResp.ExpiresAt, 0)
	return nil
}
