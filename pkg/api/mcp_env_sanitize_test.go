package api

import "testing"

func TestOmitEmptySensitiveMCPEnv(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"GITHUB_TOKEN": "",
		"API_SECRET":   "   ",
		"LOG_LEVEL":    "",
		"API_KEY":      "{{CREDENTIAL:api:value}}",
	}

	got := omitEmptySensitiveMCPEnv(env)
	if _, ok := got["GITHUB_TOKEN"]; ok {
		t.Fatalf("blank sensitive token was retained: %#v", got)
	}
	if _, ok := got["API_SECRET"]; ok {
		t.Fatalf("blank sensitive secret was retained: %#v", got)
	}
	if got["LOG_LEVEL"] != "" {
		t.Fatalf("blank non-sensitive env value was not retained: %#v", got)
	}
	if got["API_KEY"] != "{{CREDENTIAL:api:value}}" {
		t.Fatalf("non-empty sensitive placeholder was not retained: %#v", got)
	}
}

func TestOmitEmptySensitiveMCPEnv_AllOmittedReturnsNil(t *testing.T) {
	t.Parallel()
	got := omitEmptySensitiveMCPEnv(map[string]string{"GITHUB_TOKEN": ""})
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}
