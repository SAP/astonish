package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Client is an HTTP client for communicating with a remote Astonish server.
// It handles JWT auth headers, automatic token refresh, and org/team context.
type Client struct {
	baseURL    string
	org        string
	team       string
	tokens     *TokenStore
	cached     *Tokens
	httpClient *http.Client
	mu         sync.Mutex
}

// New creates a new remote client from the stored configuration and tokens.
// Returns an error if remote mode is not configured or tokens cannot be loaded.
func New() (*Client, error) {
	cfg, err := LoadRemoteConfig()
	if err != nil {
		return nil, fmt.Errorf("load remote config: %w", err)
	}
	if cfg == nil || cfg.URL == "" {
		return nil, fmt.Errorf("not in remote mode (no remote.yaml)")
	}

	ts, err := NewTokenStore()
	if err != nil {
		return nil, fmt.Errorf("init token store: %w", err)
	}

	tokens, err := ts.Load()
	if err != nil {
		return nil, fmt.Errorf("load tokens: %w", err)
	}
	if tokens == nil {
		return nil, fmt.Errorf("no stored tokens; run 'astonish login' first")
	}

	return &Client{
		baseURL: cfg.URL,
		org:     cfg.Org,
		team:    cfg.Team,
		tokens:  ts,
		cached:  tokens,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// NewWithConfig creates a client from explicit config (used during login flow
// before remote.yaml is fully saved).
func NewWithConfig(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BaseURL returns the server base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// SetTeam updates the team header for subsequent requests.
func (c *Client) SetTeam(team string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.team = team
}

// Do executes an authenticated HTTP request. It auto-refreshes the access token
// on 401 responses and retries the original request once.
func (c *Client) Do(method, path string, body any) (*http.Response, error) {
	resp, err := c.doOnce(method, path, body)
	if err != nil {
		return nil, err
	}

	// Auto-refresh on 401
	if resp.StatusCode == http.StatusUnauthorized && c.cached != nil {
		resp.Body.Close()
		if refreshErr := c.refresh(); refreshErr != nil {
			return nil, fmt.Errorf("session expired: %w (run 'astonish login' to re-authenticate)", refreshErr)
		}
		// Retry with new token
		return c.doOnce(method, path, body)
	}

	return resp, nil
}

// DoJSON executes an authenticated request and decodes the JSON response into dst.
func (c *Client) DoJSON(method, path string, body any, dst any) error {
	resp, err := c.Do(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return parseErrorResponse(resp)
	}

	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// SSE starts an SSE streaming request. The caller must close the returned stream.
func (c *Client) SSE(method, path string, body any) (*SSEStream, error) {
	req, err := c.buildRequest(method, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	// Use a client without timeout for streaming
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SSE request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized && c.cached != nil {
		resp.Body.Close()
		if refreshErr := c.refresh(); refreshErr != nil {
			return nil, fmt.Errorf("session expired: %w (run 'astonish login' to re-authenticate)", refreshErr)
		}
		// Rebuild request with new token
		req, err = c.buildRequest(method, path, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err = streamClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("SSE request (retry): %w", err)
		}
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, parseErrorResponse(resp)
	}

	return NewSSEStream(resp.Body), nil
}

// doOnce performs a single HTTP request with current credentials.
func (c *Client) doOnce(method, path string, body any) (*http.Response, error) {
	req, err := c.buildRequest(method, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	return resp, nil
}

// buildRequest creates an HTTP request with auth and team headers.
func (c *Client) buildRequest(method, path string, body any) (*http.Request, error) {
	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Add auth header
	c.mu.Lock()
	if c.cached != nil && c.cached.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cached.AccessToken)
	}
	c.mu.Unlock()

	return req, nil
}

// refresh exchanges the refresh token for a new access token.
func (c *Client) refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cached == nil || c.cached.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	if c.cached.IsRefreshExpired() {
		return fmt.Errorf("refresh token expired")
	}

	if c.cached.isOAuth() {
		refreshed, err := refreshOAuthToken(c.baseURL, c.cached.RefreshToken)
		if err != nil {
			return err
		}
		c.cached.AccessToken = refreshed.AccessToken
		c.cached.AccessExpiresAt = refreshed.AccessExpiresAt
		if refreshed.RefreshToken != "" {
			c.cached.RefreshToken = refreshed.RefreshToken
			c.cached.RefreshExpiresAt = refreshed.RefreshExpiresAt
		}
		c.cached.Kind = TokenKindOAuth
		c.cached.ClientID = refreshed.ClientID
		if c.tokens != nil {
			if err := c.tokens.Save(c.cached); err != nil {
				fmt.Fprintf(io.Discard, "warning: failed to save refreshed tokens: %v\n", err)
			}
		}
		return nil
	}

	reqBody := map[string]string{
		"refresh_token": c.cached.RefreshToken,
	}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", c.baseURL+"/api/auth/refresh", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var authResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return fmt.Errorf("decode refresh response: %w", err)
	}

	// Update cached tokens
	c.cached.AccessToken = authResp.AccessToken
	c.cached.AccessExpiresAt = time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)
	if authResp.RefreshToken != "" {
		c.cached.RefreshToken = authResp.RefreshToken
		// Assume 90 days for refresh token (server doesn't tell us)
		c.cached.RefreshExpiresAt = time.Now().Add(90 * 24 * time.Hour)
	}

	// Persist updated tokens
	if c.tokens != nil {
		if err := c.tokens.Save(c.cached); err != nil {
			// Non-fatal: tokens work in memory, just won't persist across restarts
			fmt.Fprintf(io.Discard, "warning: failed to save refreshed tokens: %v\n", err)
		}
	}

	return nil
}

// parseErrorResponse extracts an error message from an HTTP error response.
func parseErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil {
		msg := errResp.Message
		if msg == "" {
			msg = errResp.Error
		}
		if msg != "" {
			return fmt.Errorf("server error (%d): %s", resp.StatusCode, msg)
		}
	}
	if len(body) > 0 {
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}
	return fmt.Errorf("server error (%d)", resp.StatusCode)
}
