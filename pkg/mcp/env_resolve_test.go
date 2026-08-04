package mcp

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
)

type mockResolver struct {
	creds map[string]*credentials.Credential
}

func (m *mockResolver) Get(name string) *credentials.Credential {
	if m.creds == nil {
		return nil
	}
	return m.creds[name]
}

func (m *mockResolver) Resolve(name string) (string, string, error) {
	cred := m.Get(name)
	if cred == nil {
		return "", "", nil
	}
	if cred.Type == credentials.CredBearer && cred.Token != "" {
		return "Authorization", "Bearer " + cred.Token, nil
	}
	return "", "", nil
}

func (m *mockResolver) Reload() error { return nil }

func TestResolveMCPServerEnv_PlainValues(t *testing.T) {
	env := map[string]string{"DEBUG": "1", "PATH": "/usr/bin"}
	got, err := ResolveMCPServerEnv(env, nil)
	if err != nil {
		t.Fatalf("ResolveMCPServerEnv: %v", err)
	}
	if got["DEBUG"] != "1" || got["PATH"] != "/usr/bin" {
		t.Fatalf("got %#v", got)
	}
}

func TestResolveMCPServerEnv_NilEnv(t *testing.T) {
	got, err := ResolveMCPServerEnv(nil, nil)
	if err != nil {
		t.Fatalf("ResolveMCPServerEnv: %v", err)
	}
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

func TestResolveMCPServerEnv_PlaceholderWithoutResolver(t *testing.T) {
	env := map[string]string{"GITHUB_TOKEN": "{{CREDENTIAL:github:token}}"}
	_, err := ResolveMCPServerEnv(env, nil)
	if err == nil {
		t.Fatal("expected error when resolver is nil")
	}
}

func TestResolveMCPServerEnv_ResolvesPlaceholder(t *testing.T) {
	resolver := &mockResolver{
		creds: map[string]*credentials.Credential{
			"github": {Type: credentials.CredBearer, Token: "ghp_secret_value"},
		},
	}
	env := map[string]string{
		"GITHUB_TOKEN": "{{CREDENTIAL:github:token}}",
		"DEBUG":        "1",
	}
	got, err := ResolveMCPServerEnv(env, resolver)
	if err != nil {
		t.Fatalf("ResolveMCPServerEnv: %v", err)
	}
	if got["GITHUB_TOKEN"] != "ghp_secret_value" {
		t.Fatalf("GITHUB_TOKEN = %q, want real token", got["GITHUB_TOKEN"])
	}
	if got["DEBUG"] != "1" {
		t.Fatalf("DEBUG = %q", got["DEBUG"])
	}
	// Input map must not be mutated.
	if env["GITHUB_TOKEN"] != "{{CREDENTIAL:github:token}}" {
		t.Fatalf("input env was mutated: %q", env["GITHUB_TOKEN"])
	}
}

func TestResolveMCPServerEnv_MissingCredential(t *testing.T) {
	resolver := &mockResolver{creds: map[string]*credentials.Credential{}}
	env := map[string]string{"TOKEN": "{{CREDENTIAL:missing:token}}"}
	_, err := ResolveMCPServerEnv(env, resolver)
	if err == nil {
		t.Fatal("expected error for missing credential")
	}
	if want := "missing"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q should mention %q", err.Error(), want)
	}
}

func TestResolveMCPServerConfig(t *testing.T) {
	resolver := &mockResolver{
		creds: map[string]*credentials.Credential{
			"api": {Type: credentials.CredAPIKey, Value: "sk-abc", Header: "X-API-Key"},
		},
	}
	cfg := config.MCPServerConfig{
		Command: "npx",
		Args:    []string{"-y", "server"},
		Env:     map[string]string{"API_KEY": "{{CREDENTIAL:api:value}}"},
	}
	got, err := ResolveMCPServerConfig(cfg, resolver)
	if err != nil {
		t.Fatalf("ResolveMCPServerConfig: %v", err)
	}
	if got.Command != "npx" || len(got.Args) != 2 {
		t.Fatalf("command/args mutated: %#v", got)
	}
	if got.Env["API_KEY"] != "sk-abc" {
		t.Fatalf("API_KEY = %q", got.Env["API_KEY"])
	}
	if cfg.Env["API_KEY"] != "{{CREDENTIAL:api:value}}" {
		t.Fatal("original config env was mutated")
	}
}

func TestEnvNeedsCredentialResolution(t *testing.T) {
	if EnvNeedsCredentialResolution(map[string]string{"A": "1"}) {
		t.Fatal("plain env should not need resolution")
	}
	if !EnvNeedsCredentialResolution(map[string]string{"A": "{{CREDENTIAL:x:token}}"}) {
		t.Fatal("placeholder env should need resolution")
	}
}

