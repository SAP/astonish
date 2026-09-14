package copilot_oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
)

const (
	maxOAuthResponseBytes = 1 << 20

	// deviceCodeURL is the GitHub OAuth device authorization endpoint.
	deviceCodeURL = "https://github.com/login/device/code"
	// tokenURL is the GitHub OAuth token endpoint.
	tokenURL = "https://github.com/login/oauth/access_token"
	// copilotTokenURL is the Copilot internal token exchange endpoint.
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	// apiBaseURL is the GitHub Copilot API base URL (OpenAI-compatible).
	apiBaseURL = "https://api.githubcopilot.com"
	// oauthScope is the GitHub OAuth scope required for Copilot access.
	oauthScope = "read:user"

	// DefaultClientID is the well-known public OAuth client_id used by
	// VS Code Copilot Chat for the GitHub device-code flow. This is a
	// public client (not a secret) shared across editor integrations.
	DefaultClientID = "Iv1.b507a08c87ecfe98"

	// userAgent is the User-Agent header required by the Copilot API.
	userAgent = "GitHubCopilotChat/0.35.0"
)

// DeviceCodeResponse represents the response from the GitHub device authorization endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// OAuthTokenResponse represents the response from the GitHub token endpoint.
type OAuthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// CopilotTokenResponse represents the response from the Copilot internal token endpoint.
type CopilotTokenResponse struct {
	Token      string `json:"token"`
	ExpiresAt  int64  `json:"expires_at"`
	TrackingID string `json:"tracking_id,omitempty"`
}

// RequestDeviceCode initiates the device authorization flow by requesting a
// device code from the GitHub authorization server.
func RequestDeviceCode(ctx context.Context, clientID string) (*DeviceCodeResponse, error) {
	return requestDeviceCodeFromURL(ctx, clientID, deviceCodeURL)
}

// requestDeviceCodeFromURL is the internal implementation that accepts a custom URL for testing.
func requestDeviceCodeFromURL(ctx context.Context, clientID, endpoint string) (*DeviceCodeResponse, error) {
	body, err := json.Marshal(map[string]string{
		"client_id": clientID,
		"scope":     oauthScope,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal device code request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device code request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request returned %s: %s", resp.Status, string(respBody))
	}

	var dcResp DeviceCodeResponse
	if err := json.Unmarshal(respBody, &dcResp); err != nil {
		return nil, fmt.Errorf("parse device code response: %w", err)
	}

	if dcResp.DeviceCode == "" {
		return nil, fmt.Errorf("empty device_code in response")
	}

	// Default interval to 5 seconds if not specified
	if dcResp.Interval == 0 {
		dcResp.Interval = 5
	}

	return &dcResp, nil
}

// PollForToken polls the token endpoint until the user approves the device
// authorization or the code expires. Implements RFC 8628 polling semantics.
func PollForToken(ctx context.Context, clientID, deviceCode string, interval int) (*OAuthTokenResponse, error) {
	return pollForTokenFromURL(ctx, clientID, deviceCode, interval, tokenURL)
}

// pollForTokenFromURL is the internal implementation that accepts a custom URL for testing.
func pollForTokenFromURL(ctx context.Context, clientID, deviceCode string, interval int, endpoint string) (*OAuthTokenResponse, error) {
	if interval <= 0 {
		interval = 5
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			tokenResp, err := requestGitHubToken(ctx, clientID, deviceCode, endpoint)
			if err != nil {
				return nil, err
			}

			// Check for OAuth error responses
			if tokenResp.Error != "" {
				switch tokenResp.Error {
				case "authorization_pending":
					// User hasn't approved yet, keep polling
					continue
				case "slow_down":
					interval = nextPollingInterval(interval)
					ticker.Reset(time.Duration(interval) * time.Second)
					continue
				case "expired_token":
					return nil, fmt.Errorf("device code expired: %s", tokenResp.ErrorDescription)
				case "access_denied":
					return nil, fmt.Errorf("access denied: %s", tokenResp.ErrorDescription)
				default:
					return nil, fmt.Errorf("token error: %s - %s", tokenResp.Error, tokenResp.ErrorDescription)
				}
			}

			// Success
			if tokenResp.AccessToken != "" {
				return tokenResp, nil
			}
			return nil, fmt.Errorf("token response contained neither an access token nor an OAuth error")
		}
	}
}

func nextPollingInterval(interval int) int {
	return interval + 5
}

// requestGitHubToken makes a single token request to the GitHub token endpoint.
func requestGitHubToken(ctx context.Context, clientID, deviceCode, endpoint string) (*OAuthTokenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":   clientID,
		"device_code": deviceCode,
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return nil, fmt.Errorf("token request returned %s", resp.Status)
	}

	var tokenResp OAuthTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	return &tokenResp, nil
}

// ExchangeForCopilotToken exchanges a GitHub OAuth token (ghu_*) for a
// short-lived Copilot session token via the Copilot internal API.
func ExchangeForCopilotToken(ctx context.Context, githubToken string) (*CopilotTokenResponse, error) {
	return exchangeForCopilotTokenFromURL(ctx, githubToken, copilotTokenURL)
}

// exchangeForCopilotTokenFromURL is the internal implementation that accepts a custom URL for testing.
func exchangeForCopilotTokenFromURL(ctx context.Context, githubToken, endpoint string) (*CopilotTokenResponse, error) {
	if githubToken == "" {
		return nil, fmt.Errorf("github token is required for Copilot token exchange")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create copilot token request: %w", err)
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read copilot token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot token request returned %s: %s", resp.Status, string(respBody))
	}

	var tokenResp CopilotTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse copilot token response: %w", err)
	}

	if tokenResp.Token == "" {
		return nil, fmt.Errorf("empty token in copilot token response")
	}

	return &tokenResp, nil
}
