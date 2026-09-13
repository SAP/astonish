package client

import (
	"net/url"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/oauthserver"
)

func TestBuildCLIAuthorizeURLUsesFirstPartyClientAndPKCE(t *testing.T) {
	got := buildCLIAuthorizeURL("https://astonish.example", "http://127.0.0.1:54321/oauth/callback", "state-1", "challenge-1")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/oauth/authorize" {
		t.Fatalf("path = %q", parsed.Path)
	}
	query := parsed.Query()
	if query.Get("client_id") != oauthserver.CLIClientID {
		t.Fatalf("client_id = %q", query.Get("client_id"))
	}
	if query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("query = %s", parsed.RawQuery)
	}
	if query.Get("redirect_uri") != "http://127.0.0.1:54321/oauth/callback" {
		t.Fatalf("redirect_uri = %q", query.Get("redirect_uri"))
	}
	if !strings.Contains(query.Get("scope"), "chat") || !strings.Contains(query.Get("scope"), "offline_access") {
		t.Fatalf("scope = %q", query.Get("scope"))
	}
}

func TestIdentityFromAccessTokenReadsTenantClaims(t *testing.T) {
	token := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyLTEiLCJvcmdfaWQiOiJvcmciLCJ0ZWFtX2lkIjoidGVhbSJ9."
	got := identityFromAccessToken(token)
	if got.UserEmail != "user-1" || got.OrgSlug != "org" || got.TeamSlug != "team" {
		t.Fatalf("identity = %#v", got)
	}
}
