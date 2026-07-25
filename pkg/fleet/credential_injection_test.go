package fleet

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
)

type memCredStore struct {
	creds map[string]*store.Credential
}

func (m *memCredStore) Get(_ context.Context, name string) *store.Credential {
	return m.creds[name]
}
func (m *memCredStore) Set(_ context.Context, name string, cred *store.Credential) error {
	m.creds[name] = cred
	return nil
}
func (m *memCredStore) Remove(_ context.Context, name string) error {
	delete(m.creds, name)
	return nil
}
func (m *memCredStore) List(_ context.Context) map[string]store.CredentialType {
	out := make(map[string]store.CredentialType, len(m.creds))
	for k, v := range m.creds {
		out[k] = v.Type
	}
	return out
}
func (m *memCredStore) Count(_ context.Context) int { return len(m.creds) }
func (m *memCredStore) Resolve(_ context.Context, name string) (string, string, error) {
	return store.ResolveCredentialHeader(name, m.Get(context.Background(), name), nil)
}
func (m *memCredStore) InvalidateToken(_ context.Context, _ string)                 {}
func (m *memCredStore) SetSecret(_ context.Context, _, _ string) error              { return nil }
func (m *memCredStore) SetSecretBatch(_ context.Context, _ map[string]string) error { return nil }
func (m *memCredStore) GetSecret(_ context.Context, _ string) string                { return "" }
func (m *memCredStore) RemoveSecret(_ context.Context, _ string) error              { return nil }
func (m *memCredStore) HasSecrets(_ context.Context) bool                           { return false }
func (m *memCredStore) SecretCount(_ context.Context) int                           { return 0 }
func (m *memCredStore) ListSecrets(_ context.Context) []string                      { return nil }
func (m *memCredStore) Reload(_ context.Context) error                              { return nil }

func TestPlanBoundCredentialStore_Allowlist(t *testing.T) {
	inner := &memCredStore{creds: map[string]*store.Credential{
		"github-pat": {Type: store.CredBearer, Token: "gh-secret"},
		"jira-api":   {Type: store.CredBearer, Token: "jira-secret"},
	}}
	plan := &FleetPlan{
		Key: "test-plan",
		Credentials: map[string]string{
			"github": "github-pat",
		},
	}
	bound := NewPlanBoundCredentialStore(inner, plan).(*PlanBoundCredentialStore)

	if got := bound.Get(context.Background(), "github-pat"); got == nil || got.Token != "gh-secret" {
		t.Fatalf("expected github-pat accessible, got %#v", got)
	}
	if got := bound.Get(context.Background(), "jira-api"); got != nil {
		t.Fatalf("expected jira-api blocked, got %#v", got)
	}
	if n := bound.Count(context.Background()); n != 1 {
		t.Fatalf("List count = %d, want 1", n)
	}
}

func TestBuildInjectionEnv_DefaultGitHub(t *testing.T) {
	plan := &FleetPlan{
		Key: "p1",
		Credentials: map[string]string{
			"github": "github-pat",
		},
	}
	cs := &memCredStore{creds: map[string]*store.Credential{
		"github-pat": {Type: store.CredBearer, Token: "tok123"},
	}}
	resolved, err := ResolveCredentialsPlatform(context.Background(), plan, cs)
	if err != nil {
		t.Fatal(err)
	}
	env, err := BuildInjectionEnv(plan, resolved, cs, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if env["GH_TOKEN"] != "tok123" {
		t.Fatalf("GH_TOKEN = %q, want tok123", env["GH_TOKEN"])
	}
}

func TestBuildInjectionEnv_FileField(t *testing.T) {
	yamlContent := "providers:\n  alpaca:\n    key: abc\n"
	plan := &FleetPlan{
		Key: "p1",
		Credentials: map[string]string{
			"trading": "juicytrade-providers",
		},
		CredentialInjection: &CredentialInjection{
			Env: []EnvInjection{
				{Credential: "trading", Var: "TRADING_JSON", Field: "value"},
			},
			Files: []FileInjection{
				{Credential: "trading", Path: "/root/app/config/credentials.yaml", Field: "value", Format: "yaml"},
			},
		},
	}
	cs := &memCredStore{creds: map[string]*store.Credential{
		"juicytrade-providers": {Type: store.CredAPIKey, Header: "X-Unused", Value: yamlContent},
	}}
	resolved, err := ResolveCredentialsPlatform(context.Background(), plan, cs)
	if err != nil {
		t.Fatal(err)
	}
	env, err := BuildInjectionEnv(plan, resolved, cs, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if env["TRADING_JSON"] != yamlContent {
		t.Fatalf("env value mismatch")
	}
	val, err := extractCredentialField(context.Background(), cs, "juicytrade-providers", "value", resolved["trading"])
	if err != nil || val != yamlContent {
		t.Fatalf("extract field: %v %q", err, val)
	}
	if err := ValidateFileInjectionPaths(plan.EffectiveCredentialInjection()); err != nil {
		t.Fatal(err)
	}
	_ = credentials.ResolveField(credentials.NewStoreAdapter(cs), "juicytrade-providers", "value")
}

func TestBuildInjectionEnv_RawContentField(t *testing.T) {
	yamlContent := "providers:\n  alpaca:\n    key: raw-secret-12345\n"
	plan := &FleetPlan{
		Key: "p1",
		Credentials: map[string]string{
			"providers": "providers-file",
		},
		CredentialInjection: &CredentialInjection{
			Files: []FileInjection{
				{Credential: "providers", Path: "/root/app/config/providers.yaml", Field: "content", Format: "yaml"},
			},
		},
	}
	cs := &memCredStore{creds: map[string]*store.Credential{
		"providers-file": {Type: store.CredRawContent, Content: yamlContent, ContentType: "application/yaml"},
	}}
	resolved, err := ResolveCredentialsPlatform(context.Background(), plan, cs)
	if err != nil {
		t.Fatal(err)
	}
	if resolved["providers"].Content != yamlContent {
		t.Fatalf("resolved content mismatch")
	}
	val, err := extractCredentialField(context.Background(), cs, "providers-file", "content", resolved["providers"])
	if err != nil || val != yamlContent {
		t.Fatalf("extract content: %v %q", err, val)
	}
}

func TestValidateFileInjectionPaths_RejectsRelative(t *testing.T) {
	err := ValidateFileInjectionPaths(CredentialInjection{
		Files: []FileInjection{{Path: "relative/path", Field: "value", Credential: "x"}},
	})
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestParseCredentialInjection_Map(t *testing.T) {
	raw := map[string]any{
		"env": []any{
			map[string]any{"credential": "github", "var": "GH_TOKEN", "field": "token"},
		},
		"files": []any{
			map[string]any{"credential": "trading", "path": "/root/app/creds.yaml", "field": "value"},
		},
	}
	inj, err := ParseCredentialInjection(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(inj.Env) != 1 || inj.Env[0].Var != "GH_TOKEN" {
		t.Fatalf("env = %+v", inj.Env)
	}
	if len(inj.Files) != 1 || inj.Files[0].Path != "/root/app/creds.yaml" {
		t.Fatalf("files = %+v", inj.Files)
	}
}

func TestNormalizeCredentialInjection_GitHubDefault(t *testing.T) {
	inj := NormalizeCredentialInjection(map[string]string{"github": "pat"}, nil)
	if inj == nil || len(inj.Env) != 1 || inj.Env[0].Var != "GH_TOKEN" {
		t.Fatalf("expected GH_TOKEN default, got %+v", inj)
	}
}

func TestValidateCredentialInjectionSpec_UnknownLogical(t *testing.T) {
	err := ValidateCredentialInjectionSpec(map[string]string{"github": "pat"}, &CredentialInjection{
		Files: []FileInjection{{Credential: "trading", Path: "/root/x.yaml", Field: "value"}},
	})
	if err == nil {
		t.Fatal("expected error for undeclared logical credential")
	}
}
