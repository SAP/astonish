package oauthserver

import (
	"context"
	"net"
	"net/url"

	"github.com/SAP/astonish/pkg/store"
)

const (
	// ChromeExtensionClientID is the fixed public OAuth client embedded in the
	// first-party Astonish Chrome extension. It has no secret; PKCE is mandatory.
	ChromeExtensionClientID = "astonish-chrome-extension"

	// ChromeExtensionRedirectURI is derived from the pinned public key in
	// extension/manifest.json. Chrome computes this stable extension ID for both
	// packaged and unpacked builds of the first-party extension.
	ChromeExtensionRedirectURI = "https://oeiocpkpaiphjhoapdlihnbdpimipelg.chromiumapp.org/oauth2"

	// CLIClientID is the fixed public OAuth client embedded in the first-party
	// Astonish CLI. It has no secret; PKCE and a loopback redirect are mandatory.
	CLIClientID = "astonish-cli"

	// CLIRedirectPath is the only path the CLI loopback listener may register.
	CLIRedirectPath = "/oauth/callback"
)

func chromeExtensionClient() *store.OAuthClient {
	return &store.OAuthClient{
		ClientID:     ChromeExtensionClientID,
		Name:         "Astonish Chrome Extension",
		ClientType:   "public",
		Active:       true,
		RedirectURIs: []string{ChromeExtensionRedirectURI},
		GrantTypes:   []string{GrantAuthorizationCode, GrantRefreshToken},
		Scopes:       []string{ScopeChat, ScopeToolExecute},
	}
}

func cliClient() *store.OAuthClient {
	return &store.OAuthClient{
		ClientID:     CLIClientID,
		Name:         "Astonish CLI",
		ClientType:   "public",
		Active:       true,
		RedirectURIs: []string{},
		GrantTypes:   []string{GrantAuthorizationCode, GrantRefreshToken},
		Scopes:       []string{ScopeChat, ScopeToolExecute},
	}
}

func (s *Server) client(ctx context.Context, id string) (*store.OAuthClient, error) {
	switch id {
	case ChromeExtensionClientID:
		return chromeExtensionClient(), nil
	case CLIClientID:
		return cliClient(), nil
	}
	return s.store.GetOAuthClient(ctx, id)
}

func isChromeExtensionClient(client *store.OAuthClient) bool {
	return client != nil && client.ClientID == ChromeExtensionClientID
}

func isCLIClient(client *store.OAuthClient) bool {
	return client != nil && client.ClientID == CLIClientID
}

func isFirstPartyPublicClient(client *store.OAuthClient) bool {
	return isChromeExtensionClient(client) || isCLIClient(client)
}

func matchRedirectURI(client *store.OAuthClient, requested string) string {
	if client == nil {
		return ""
	}
	for _, registeredURI := range client.RedirectURIs {
		if registeredURI == requested {
			return registeredURI
		}
	}
	// RFC 8252 §7.3: native public clients use an ephemeral loopback port.
	// Exact matching cannot register every port, so the first-party CLI client
	// may use http://127.0.0.1|localhost|::1:<port>/oauth/callback only.
	if isCLIClient(client) && isCLILoopbackRedirect(requested) {
		return requested
	}
	return ""
}

func isCLILoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Path != CLIRedirectPath || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	port := u.Port()
	if port == "" || port == "0" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
