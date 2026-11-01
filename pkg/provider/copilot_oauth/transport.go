package copilot_oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
)

var ErrReauthRequired = errors.New("copilot oauth: re-authentication required")

// copilotTransport exchanges a persistent GitHub token for cached session tokens.
type copilotTransport struct {
	base                      http.RoundTripper
	githubToken, sessionToken string
	sessionExpiresAt          time.Time
	mu                        sync.Mutex
	copilotTokenURL           string
}

func NewCopilotTransport(githubToken string) *copilotTransport {
	return &copilotTransport{base: httpool.Transport(), githubToken: githubToken}
}

func (t *copilotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return httpool.RoundTripWithToken(req, t.base, t.acquireToken, t.invalidateToken,
		func(r *http.Request, token string) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Copilot-Integration-Id", "vscode-chat")
			r.Header.Set("Editor-Version", "vscode/1.107.0")
			r.Header.Set("Editor-Plugin-Version", "copilot-chat/0.35.0")
			r.Header.Set("User-Agent", userAgent)
			r.Header.Set("OpenAI-Intent", "conversation-panel")
		}, func(status int) bool { return status == http.StatusUnauthorized || status == http.StatusForbidden })
}

func (t *copilotTransport) invalidateToken(rejected string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessionToken == rejected {
		t.sessionToken = ""
	}
}

func (t *copilotTransport) acquireToken(ctx context.Context, rejected string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	valid := t.sessionToken != "" && !t.sessionExpiresAt.IsZero() && time.Until(t.sessionExpiresAt) >= 60*time.Second
	if valid && (rejected == "" || t.sessionToken != rejected) {
		return t.sessionToken, nil
	}
	if err := t.doRefreshLocked(ctx); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: exchange copilot token: %w", ErrReauthRequired, err)
	}
	return t.sessionToken, nil
}

func (t *copilotTransport) doRefreshLocked(ctx context.Context) error {
	endpoint := copilotTokenURL
	if t.copilotTokenURL != "" {
		endpoint = t.copilotTokenURL
	}
	response, err := exchangeForCopilotTokenFromURL(ctx, t.githubToken, endpoint)
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.Token) == "" || response.ExpiresAt <= time.Now().Unix() {
		return errors.New("invalid copilot session token or expiration")
	}
	t.sessionToken = response.Token
	t.sessionExpiresAt = time.Unix(response.ExpiresAt, 0)
	return nil
}
