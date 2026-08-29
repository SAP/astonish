package config

import "testing"

func TestInjectProviderSecretsToConfigXAIOAuth(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]ProviderConfig{
		"subscription": {"type": "xai_oauth"},
	}}
	secrets := map[string]string{
		"provider.subscription.access_token":  "access",
		"provider.subscription.refresh_token": "refresh",
		"provider.subscription.expires_at":    "2099-01-01T00:00:00Z",
	}
	InjectProviderSecretsToConfig(cfg, func(key string) string { return secrets[key] })

	got := cfg.Providers["subscription"]
	if got["access_token"] != "access" || got["refresh_token"] != "refresh" || got["expires_at"] != "2099-01-01T00:00:00Z" {
		t.Fatalf("xAI OAuth secrets not injected: %#v", got)
	}
}
