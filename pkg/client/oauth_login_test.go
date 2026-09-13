package client

import (
	"net/url"
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
	if got := query.Get("scope"); got != "openid offline_access chat" {
		t.Fatalf("scope = %q, want exactly chat plus protocol scopes", got)
	}
}

func TestIdentityFromAccessTokenReadsDisplayClaimsOnly(t *testing.T) {
	token := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyLTEiLCJvcmdfaWQiOiJvcmdfaWQiLCJ0ZWFtX2lkIjoidGVhbV9pZCIsImVtYWlsIjoidXNlckBleGFtcGxlLmNvbSIsIm5hbWUiOiJBbGljZSIsIm9yZyI6ImFjbWUiLCJ0ZWFtIjoib3BzIn0."
	got := identityFromAccessToken(token)
	if got.UserEmail != "user@example.com" || got.DisplayName != "Alice" || got.OrgSlug != "acme" || got.TeamSlug != "ops" {
		t.Fatalf("identity = %#v", got)
	}
}

func TestIdentityFromAccessTokenDoesNotTreatIDsAsDisplayFields(t *testing.T) {
	token := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1c2VyLTEiLCJvcmdfaWQiOiJvcmdfaWQiLCJ0ZWFtX2lkIjoidGVhbV9pZCJ9."
	got := identityFromAccessToken(token)
	if got.UserEmail != "" || got.DisplayName != "" || got.OrgSlug != "" || got.TeamSlug != "" {
		t.Fatalf("opaque IDs were exposed as display identity: %#v", got)
	}
}
