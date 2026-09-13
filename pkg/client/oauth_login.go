package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/SAP/astonish/pkg/oauthserver"
)

const (
	oauthLoginTimeout = 10 * time.Minute
	cliOAuthScopes    = "openid offline_access chat"
)

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

type pkcePair struct {
	verifier  string
	challenge string
}

func newPKCEPair() (pkcePair, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return pkcePair{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return pkcePair{
		verifier:  verifier,
		challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

func randomOAuthState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// LoginWithOAuth starts a first-party Authorization Code + PKCE flow for the CLI.
// It opens the browser at /oauth/authorize. Studio authenticates the user (SSO or
// password) and redirects the code to a loopback listener.
func LoginWithOAuth(serverURL string, onStatus func(status string)) (*LoginResult, error) {
	serverURL = strings.TrimRight(serverURL, "/")
	pkce, err := newPKCEPair()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE: %w", err)
	}
	state, err := randomOAuthState()
	if err != nil {
		return nil, fmt.Errorf("generate OAuth state: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, oauthserver.CLIRedirectPath)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(oauthserver.CLIRedirectPath, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("error") != "" {
			desc := query.Get("error_description")
			if desc == "" {
				desc = query.Get("error")
			}
			http.Error(w, "Astonish login failed: "+desc, http.StatusBadRequest)
			errCh <- fmt.Errorf("authorization failed: %s", desc)
			return
		}
		if query.Get("state") != state {
			http.Error(w, "OAuth state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("OAuth state mismatch")
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			errCh <- fmt.Errorf("authorization code is missing")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><p>Astonish CLI login complete. You can close this window.</p></body></html>")
		codeCh <- code
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		_ = listener.Close()
	}()

	authorizeURL := buildCLIAuthorizeURL(serverURL, redirectURI, state, pkce.challenge)
	if onStatus != nil {
		onStatus("opening_browser")
	}
	fmt.Printf("Open this URL if the browser does not launch:\n%s\n\n", authorizeURL)
	if err := openBrowser(authorizeURL); err != nil && onStatus != nil {
		onStatus("browser_failed")
	}
	if onStatus != nil {
		onStatus("polling")
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-time.After(oauthLoginTimeout):
		return nil, fmt.Errorf("OAuth login timed out")
	}

	tokens, err := exchangeCLIAuthorizationCode(serverURL, redirectURI, code, pkce.verifier)
	if err != nil {
		return nil, err
	}
	return PersistOAuthLogin(serverURL, tokens)
}

func buildCLIAuthorizeURL(serverURL, redirectURI, state, challenge string) string {
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", oauthserver.CLIClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", cliOAuthScopes)
	values.Set("state", state)
	values.Set("code_challenge_method", "S256")
	values.Set("code_challenge", challenge)
	return serverURL + "/oauth/authorize?" + values.Encode()
}

func exchangeCLIAuthorizationCode(serverURL, redirectURI, code, verifier string) (*Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", oauthserver.GrantAuthorizationCode)
	form.Set("client_id", oauthserver.CLIClientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	return requestOAuthToken(serverURL, form)
}

func refreshOAuthToken(serverURL, refreshToken string) (*Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", oauthserver.GrantRefreshToken)
	form.Set("client_id", oauthserver.CLIClientID)
	form.Set("refresh_token", refreshToken)
	return requestOAuthToken(serverURL, form)
}

func requestOAuthToken(serverURL string, form url.Values) (*Tokens, error) {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(serverURL, "/")+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token request: %w", err)
	}
	defer resp.Body.Close()
	var payload oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode OAuth token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || payload.AccessToken == "" {
		msg := payload.Description
		if msg == "" {
			msg = payload.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("OAuth token exchange failed: %s", msg)
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	return &Tokens{
		AccessToken:      payload.AccessToken,
		RefreshToken:     payload.RefreshToken,
		AccessExpiresAt:  time.Now().Add(time.Duration(expiresIn) * time.Second),
		RefreshExpiresAt: time.Now().Add(30 * 24 * time.Hour),
		Kind:             TokenKindOAuth,
		ClientID:         oauthserver.CLIClientID,
	}, nil
}

func PersistOAuthLogin(serverURL string, tokens *Tokens) (*LoginResult, error) {
	ts, err := NewTokenStore()
	if err != nil {
		return nil, fmt.Errorf("init token store: %w", err)
	}
	if err := ts.Save(tokens); err != nil {
		return nil, fmt.Errorf("save tokens: %w", err)
	}
	identity := identityFromAccessToken(tokens.AccessToken)
	cfg := &RemoteConfig{
		URL:       serverURL,
		Org:       identity.OrgSlug,
		Team:      identity.TeamSlug,
		UserEmail: identity.UserEmail,
	}
	if err := SaveRemoteConfig(cfg); err != nil {
		return nil, fmt.Errorf("save remote config: %w", err)
	}
	return &LoginResult{
		UserEmail:   identity.UserEmail,
		DisplayName: identity.DisplayName,
		OrgSlug:     identity.OrgSlug,
		TeamSlug:    identity.TeamSlug,
	}, nil
}

type tokenIdentity struct {
	UserEmail   string
	DisplayName string
	OrgSlug     string
	TeamSlug    string
}

func identityFromAccessToken(accessToken string) tokenIdentity {
	claims := jwt.MapClaims{}
	// This is a presentation-only decode used to populate optional CLI labels.
	// It is never used for authorization; every protected request is validated
	// server-side against the signed OAuth bearer.
	_, _, err := jwt.NewParser().ParseUnverified(accessToken, claims)
	if err != nil {
		return tokenIdentity{}
	}
	email, _ := claims["email"].(string)
	name, _ := claims["name"].(string)
	org, _ := claims["org"].(string)
	team, _ := claims["team"].(string)
	return tokenIdentity{UserEmail: email, DisplayName: name, OrgSlug: org, TeamSlug: team}
}

func ExchangePlatformSessionForOAuth(serverURL, platformAccessToken string) (*Tokens, error) {
	pkce, err := newPKCEPair()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE: %w", err)
	}
	state, err := randomOAuthState()
	if err != nil {
		return nil, fmt.Errorf("generate OAuth state: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, oauthserver.CLIRedirectPath)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(oauthserver.CLIRedirectPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state || r.URL.Query().Get("code") == "" {
			http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
			errCh <- fmt.Errorf("invalid OAuth callback")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		codeCh <- r.URL.Query().Get("code")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		_ = listener.Close()
	}()

	req, err := http.NewRequest(http.MethodGet, buildCLIAuthorizeURL(serverURL, redirectURI, state, pkce.challenge), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+platformAccessToken)
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), redirectURI) {
				return http.ErrUseLastResponse
			}
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth authorize: %w", err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, redirectURI) {
		u, err := url.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("parse OAuth redirect: %w", err)
		}
		if u.Query().Get("state") != state {
			return nil, fmt.Errorf("OAuth state mismatch")
		}
		code := u.Query().Get("code")
		if code == "" {
			return nil, fmt.Errorf("authorization code is missing")
		}
		return exchangeCLIAuthorizationCode(serverURL, redirectURI, code, pkce.verifier)
	}
	select {
	case code := <-codeCh:
		return exchangeCLIAuthorizationCode(serverURL, redirectURI, code, pkce.verifier)
	case err := <-errCh:
		return nil, err
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("password login succeeded but OAuth authorization did not return a code")
	}
}
