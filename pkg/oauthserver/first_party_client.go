package oauthserver

import (
	"context"

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

func (s *Server) client(ctx context.Context, id string) (*store.OAuthClient, error) {
	if id == ChromeExtensionClientID {
		return chromeExtensionClient(), nil
	}
	return s.store.GetOAuthClient(ctx, id)
}

func isChromeExtensionClient(client *store.OAuthClient) bool {
	return client != nil && client.ClientID == ChromeExtensionClientID
}
