package xai_oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/provider/httpool"
)

const (
	// deviceCodeURL is the xAI OAuth 2.0 Device Authorization endpoint (RFC 8628).
	deviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	// tokenURL is the xAI OAuth 2.0 Token endpoint.
	tokenURL = "https://auth.x.ai/oauth2/token"
	// apiBaseURL is the xAI API base URL (OpenAI-compatible).
	apiBaseURL = "https://api.x.ai/v1"
	// DefaultClientID is the well-known public OAuth client_id for xAI/Grok CLI
	// device-code flow. This is a public client (not a secret) shared across
	// CLI integrations that authenticate via SuperGrok / X Premium+ subscriptions.
	DefaultClientID = "b1a00492-073a-47ea-816f-4c329264a828"
)

// DeviceCodeResponse represents the response from the device authorization endpoint.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenResponse represents the response from the token endpoint.
type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// RequestDeviceCode initiates the device authorization flow by requesting a
// device code from the xAI authorization server.
func RequestDeviceCode(ctx context.Context, clientID string) (*DeviceCodeResponse, error) {
	return requestDeviceCodeFromURL(ctx, clientID, deviceCodeURL)
}

// requestDeviceCodeFromURL is the internal implementation that accepts a custom URL for testing.
func requestDeviceCodeFromURL(ctx context.Context, clientID, endpoint string) (*DeviceCodeResponse, error) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"openid profile email offline_access api:access"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device code request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request returned %s: %s", resp.Status, string(body))
	}

	var dcResp DeviceCodeResponse
	if err := json.Unmarshal(body, &dcResp); err != nil {
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
func PollForToken(ctx context.Context, clientID, deviceCode string, interval int) (*TokenResponse, error) {
	return pollForTokenFromURL(ctx, clientID, deviceCode, interval, tokenURL)
}

// pollForTokenFromURL is the internal implementation that accepts a custom URL for testing.
func pollForTokenFromURL(ctx context.Context, clientID, deviceCode string, interval int, endpoint string) (*TokenResponse, error) {
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
			tokenResp, err := requestToken(ctx, clientID, deviceCode, endpoint)
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
					// Increase polling interval by 5 seconds
					ticker.Reset(time.Duration(interval+5) * time.Second)
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
		}
	}
}

// requestToken makes a single token request to the token endpoint.
func requestToken(ctx context.Context, clientID, deviceCode, endpoint string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
		"client_id":   {clientID},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	return &tokenResp, nil
}

// RefreshAccessToken uses a refresh token to obtain a new access token.
func RefreshAccessToken(ctx context.Context, clientID, refreshToken string) (*TokenResponse, error) {
	return refreshAccessTokenFromURL(ctx, clientID, refreshToken, tokenURL)
}

// refreshAccessTokenFromURL is the internal implementation that accepts a custom URL for testing.
func refreshAccessTokenFromURL(ctx context.Context, clientID, refreshToken, endpoint string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	if tokenResp.Error != "" {
		return nil, fmt.Errorf("refresh failed: %s - %s", tokenResp.Error, tokenResp.ErrorDescription)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access_token in refresh response")
	}

	return &tokenResp, nil
}
