package credentials

import "testing"

type oauthClientTokenResolver struct {
	testCredentialResolver
	calls int
}

func (r *oauthClientTokenResolver) Resolve(string) (string, string, error) {
	r.calls++
	return "Authorization", "Bearer freshly-issued-client-token", nil
}

func TestSubstitutePlaceholders_OAuthClientToken(t *testing.T) {
	r := &oauthClientTokenResolver{testCredentialResolver: testCredentialResolver{creds: map[string]*Credential{"oauth": {Type: CredOAuthClientCreds, Token: "stale-token"}}}}
	got := SubstitutePlaceholders("Bearer {{CREDENTIAL:oauth:token}}", r)
	if got != "Bearer freshly-issued-client-token" || r.calls != 1 {
		t.Fatalf("got %q calls=%d", got, r.calls)
	}
}
