package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// LoginResult contains the information returned after a successful login.
type LoginResult struct {
	UserEmail      string
	DisplayName    string
	OrgSlug        string
	OrgName        string
	TeamSlug       string
	Role           string
	AvailableOrgs  []LoginOrgOption
	AvailableTeams []LoginTeamOption
}

// LoginOrgOption represents an available org choice during login.
type LoginOrgOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

// LoginTeamOption represents an available team choice during login.
type LoginTeamOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// LoginWithPassword authenticates with email and password against the remote server.
// If org/team are empty, the server picks defaults and returns available options.
// If org/team are specified, the token is scoped to those values.
// It stores the tokens and remote config on success.
func LoginWithPassword(serverURL, email, password, org, team string) (*LoginResult, error) {
	c := NewWithConfig(serverURL)

	reqBody := map[string]string{
		"email":       email,
		"password":    password,
		"client_type": "cli",
	}
	if org != "" {
		reqBody["org"] = org
	}
	if team != "" {
		reqBody["team"] = team
	}

	resp, err := c.doOnce("POST", "/api/auth/login", reqBody)
	if err != nil {
		return nil, fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(resp)
	}

	var authResp struct {
		User struct {
			ID           string `json:"id"`
			Email        string `json:"email"`
			DisplayName  string `json:"display_name"`
			Role         string `json:"role"`
			PlatformRole string `json:"platform_role"`
		} `json:"user"`
		Org struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"org"`
		AccessToken    string            `json:"access_token"`
		RefreshToken   string            `json:"refresh_token"`
		ExpiresIn      int               `json:"expires_in"`
		TeamSlug       string            `json:"team"`
		AvailableOrgs  []LoginOrgOption  `json:"available_orgs"`
		AvailableTeams []LoginTeamOption `json:"available_teams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, fmt.Errorf("decode login response: %w", err)
	}

	if authResp.AccessToken == "" {
		return nil, fmt.Errorf("server did not return tokens (ensure server version supports CLI login)")
	}

	// Use team from response (server determined it)
	teamSlug := authResp.TeamSlug
	if teamSlug == "" {
		// Fallback: get from /api/auth/me (older servers)
		teamSlug = getTeamFromMe(c, authResp.AccessToken)
	}

	// Store tokens
	ts, err := NewTokenStore()
	if err != nil {
		return nil, fmt.Errorf("init token store: %w", err)
	}

	tokens := &Tokens{
		AccessToken:      authResp.AccessToken,
		RefreshToken:     authResp.RefreshToken,
		AccessExpiresAt:  time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second),
		RefreshExpiresAt: time.Now().Add(90 * 24 * time.Hour), // 90 days default
		Kind:             TokenKindPlatform,
	}
	if err := ts.Save(tokens); err != nil {
		return nil, fmt.Errorf("save tokens: %w", err)
	}

	// Save remote config
	cfg := &RemoteConfig{
		URL:       serverURL,
		Org:       authResp.Org.Slug,
		Team:      teamSlug,
		UserEmail: authResp.User.Email,
	}
	if err := SaveRemoteConfig(cfg); err != nil {
		return nil, fmt.Errorf("save remote config: %w", err)
	}

	return &LoginResult{
		UserEmail:      authResp.User.Email,
		DisplayName:    authResp.User.DisplayName,
		OrgSlug:        authResp.Org.Slug,
		OrgName:        authResp.Org.Name,
		TeamSlug:       teamSlug,
		Role:           authResp.User.Role,
		AvailableOrgs:  authResp.AvailableOrgs,
		AvailableTeams: authResp.AvailableTeams,
	}, nil
}

// LoginWithSSO initiates an SSO/OIDC login flow using device-code polling.
// It calls the server's /api/auth/sso/init endpoint, opens the browser to the
// verify URL, and polls /api/auth/sso/poll until the flow completes.
// The onStatus callback is called with status updates for UI display.
func LoginWithSSO(serverURL string, providerID string, onStatus func(status string)) (*LoginResult, error) {
	c := NewWithConfig(serverURL)

	// Step 1: Initialize the device flow
	initBody := map[string]string{}
	if providerID != "" {
		initBody["provider_id"] = providerID
	}

	resp, err := c.doOnce("POST", "/api/auth/sso/init", initBody)
	if err != nil {
		return nil, fmt.Errorf("SSO init request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(resp)
	}

	var initResp struct {
		DeviceCode string `json:"device_code"`
		VerifyURL  string `json:"verify_url"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
		return nil, fmt.Errorf("decode SSO init response: %w", err)
	}

	// Step 2: Open the verify URL in the browser
	if onStatus != nil {
		onStatus("opening_browser")
	}
	if err := openBrowser(initResp.VerifyURL); err != nil {
		// Don't fail — user can open manually
		if onStatus != nil {
			onStatus("browser_failed")
		}
	}

	// Step 3: Poll for completion
	interval := time.Duration(initResp.Interval) * time.Second
	if interval < 1*time.Second {
		interval = 2 * time.Second
	}
	timeout := time.Duration(initResp.ExpiresIn) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	deadline := time.Now().Add(timeout)

	if onStatus != nil {
		onStatus("polling")
	}

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		pollResp, pollErr := c.doOnce("POST", "/api/auth/sso/poll", map[string]string{
			"device_code": initResp.DeviceCode,
		})
		if pollErr != nil {
			continue // Retry on network errors
		}

		var pollResult struct {
			Status         string            `json:"status"`
			Error          string            `json:"error,omitempty"`
			AccessToken    string            `json:"access_token,omitempty"`
			RefreshToken   string            `json:"refresh_token,omitempty"`
			ExpiresIn      int               `json:"expires_in,omitempty"`
			User           json.RawMessage   `json:"user,omitempty"`
			Org            json.RawMessage   `json:"org,omitempty"`
			TeamSlug       string            `json:"team,omitempty"`
			AvailableOrgs  []LoginOrgOption  `json:"available_orgs,omitempty"`
			AvailableTeams []LoginTeamOption `json:"available_teams,omitempty"`
		}
		decErr := json.NewDecoder(pollResp.Body).Decode(&pollResult)
		pollResp.Body.Close()
		if decErr != nil {
			continue
		}

		switch pollResult.Status {
		case "pending":
			// Keep polling
			continue

		case "failed":
			return nil, fmt.Errorf("SSO login failed: %s", pollResult.Error)

		case "complete":
			// Parse user and org from response
			var user struct {
				ID           string `json:"id"`
				Email        string `json:"email"`
				DisplayName  string `json:"display_name"`
				Role         string `json:"role"`
				PlatformRole string `json:"platform_role"`
			}
			var org struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			}
			_ = json.Unmarshal(pollResult.User, &user)
			_ = json.Unmarshal(pollResult.Org, &org)

			teamSlug := pollResult.TeamSlug

			// Store tokens
			ts, tsErr := NewTokenStore()
			if tsErr != nil {
				return nil, fmt.Errorf("init token store: %w", tsErr)
			}
			tokens := &Tokens{
				AccessToken:      pollResult.AccessToken,
				RefreshToken:     pollResult.RefreshToken,
				AccessExpiresAt:  time.Now().Add(time.Duration(pollResult.ExpiresIn) * time.Second),
				RefreshExpiresAt: time.Now().Add(90 * 24 * time.Hour),
				Kind:             TokenKindPlatform,
			}
			if saveErr := ts.Save(tokens); saveErr != nil {
				return nil, fmt.Errorf("save tokens: %w", saveErr)
			}

			// Save remote config
			cfg := &RemoteConfig{
				URL:       serverURL,
				Org:       org.Slug,
				Team:      teamSlug,
				UserEmail: user.Email,
			}
			if saveErr := SaveRemoteConfig(cfg); saveErr != nil {
				return nil, fmt.Errorf("save remote config: %w", saveErr)
			}

			return &LoginResult{
				UserEmail:      user.Email,
				DisplayName:    user.DisplayName,
				OrgSlug:        org.Slug,
				OrgName:        org.Name,
				TeamSlug:       teamSlug,
				Role:           user.Role,
				AvailableOrgs:  pollResult.AvailableOrgs,
				AvailableTeams: pollResult.AvailableTeams,
			}, nil
		}
	}

	return nil, fmt.Errorf("SSO login timed out. Please try again")
}

// SSOProviderInfo represents an available SSO provider.
type SSOProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListSSOProviders returns the available SSO providers from the server.
func ListSSOProviders(serverURL string) ([]SSOProviderInfo, error) {
	c := NewWithConfig(serverURL)

	resp, err := c.doOnce("GET", "/api/auth/sso/providers", nil)
	if err != nil {
		return nil, fmt.Errorf("list SSO providers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil // Not an error — just no providers
	}

	var result struct {
		Providers []SSOProviderInfo `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil
	}
	return result.Providers, nil
}

// Logout clears stored tokens and remote config.
func Logout() error {
	ts, err := NewTokenStore()
	if err == nil {
		_ = ts.Remove()
	}
	return RemoveRemoteConfig()
}

// --- Helpers ---

// meResponse represents the /api/auth/me response.
type meResponse struct {
	Email       string
	DisplayName string
	OrgSlug     string
	Team        string
	Role        string
}

// getTeamFromMe calls /api/auth/me to determine the user's default team.
func getTeamFromMe(c *Client, accessToken string) string {
	me := getMeWithToken(c, accessToken)
	if me != nil {
		return me.Team
	}
	return ""
}

// getMeWithToken calls /api/auth/me with a specific access token.
func getMeWithToken(c *Client, accessToken string) *meResponse {
	req, err := http.NewRequest("GET", c.baseURL+"/api/auth/me", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var body struct {
		User struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Role        string `json:"role"`
		} `json:"user"`
		Org struct {
			Slug string `json:"slug"`
		} `json:"org"`
		Team string `json:"team"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}

	return &meResponse{
		Email:       body.User.Email,
		DisplayName: body.User.DisplayName,
		OrgSlug:     body.Org.Slug,
		Team:        body.Team,
		Role:        body.User.Role,
	}
}

// openBrowser opens a URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
