package oauthserver

import (
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestMatchRedirectURIAcceptsCLILoopbackPort(t *testing.T) {
	client := cliClient()
	got := matchRedirectURI(client, "http://127.0.0.1:54321/oauth/callback")
	if got != "http://127.0.0.1:54321/oauth/callback" {
		t.Fatalf("loopback redirect = %q", got)
	}
}

func TestMatchRedirectURIRejectsNonLoopbackCLIRedirect(t *testing.T) {
	client := cliClient()
	for _, raw := range []string{
		"http://example.com/oauth/callback",
		"https://127.0.0.1:54321/oauth/callback",
		"http://127.0.0.1:54321/elsewhere",
		"http://127.0.0.1/oauth/callback",
		"http://127.0.0.1:0/oauth/callback",
		"http://127.0.0.1:54321/oauth/callback?next=https://evil.example",
	} {
		if got := matchRedirectURI(client, raw); got != "" {
			t.Fatalf("accepted %q as %q", raw, got)
		}
	}
}

func TestMatchRedirectURIKeepsExactRegisteredURIs(t *testing.T) {
	client := &store.OAuthClient{
		ClientID:     "registered",
		RedirectURIs: []string{"https://app.example/callback"},
	}
	if got := matchRedirectURI(client, "https://app.example/callback"); got != "https://app.example/callback" {
		t.Fatalf("registered redirect = %q", got)
	}
	if got := matchRedirectURI(client, "http://127.0.0.1:54321/oauth/callback"); got != "" {
		t.Fatalf("non-CLI client accepted loopback: %q", got)
	}
}

func TestFirstPartyPublicClients(t *testing.T) {
	if !isFirstPartyPublicClient(cliClient()) || !isFirstPartyPublicClient(chromeExtensionClient()) {
		t.Fatal("expected CLI and Chrome extension to be first-party public clients")
	}
	if got := cliClient().Scopes; len(got) != 1 || got[0] != ScopeChat {
		t.Fatalf("CLI granted scopes = %#v, want [%q]", got, ScopeChat)
	}
	if isFirstPartyPublicClient(&store.OAuthClient{ClientID: "ast_other"}) {
		t.Fatal("registered clients must not inherit first-party tenant binding")
	}
}
