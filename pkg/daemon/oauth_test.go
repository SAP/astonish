package daemon

import (
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

func TestServesA2AOnlyOnHTTPModes(t *testing.T) {
	if !servesA2A(config.DaemonModeAPI) {
		t.Fatal("API mode must serve A2A")
	}
	if !servesA2A(config.DaemonModeDefault) {
		t.Fatal("default mode must serve A2A")
	}
	if servesA2A(config.DaemonModeWorker) {
		t.Fatal("worker mode must not own the A2A HTTP service")
	}
}

func TestA2AServiceBaseURLUsesConfiguredIssuer(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Storage.Auth.OAuthServer.Issuer = "https://astonish.example/"
	if got := a2aServiceBaseURL(cfg, 9393); got != "https://astonish.example" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestA2AServiceBaseURLUsesLoopbackDefault(t *testing.T) {
	cfg := &config.AppConfig{}
	if got := a2aServiceBaseURL(cfg, 1234); got != "http://127.0.0.1:1234" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestNewOAuthServerRuntimeConfigAppliesLoopbackDefaults(t *testing.T) {
	got := newOAuthServerRuntimeConfig(config.OAuthServerConfig{}, 9393, true)
	if got.Issuer != "http://127.0.0.1:9393" {
		t.Fatalf("issuer = %q", got.Issuer)
	}
	if got.Resource != "http://127.0.0.1:9393/api/mcp" {
		t.Fatalf("resource = %q", got.Resource)
	}
	if !got.Development {
		t.Fatal("loopback builtin issuer should allow HTTP development mode")
	}
	if got.AccessTokenTTL != 0 {
		t.Fatalf("unset access TTL should stay 0 for oauthserver defaults, got %s", got.AccessTokenTTL)
	}
}

func TestNewOAuthServerRuntimeConfigPreservesExplicitHTTPS(t *testing.T) {
	got := newOAuthServerRuntimeConfig(config.OAuthServerConfig{
		Issuer:                "https://astonish.example/",
		Resource:              "https://astonish.example/api/mcp",
		AccessTokenTTLMinutes: 20,
		RefreshTokenTTLDays:   7,
	}, 1234, true)
	if got.Issuer != "https://astonish.example/" {
		t.Fatalf("issuer = %q", got.Issuer)
	}
	if got.Resource != "https://astonish.example/api/mcp" {
		t.Fatalf("resource = %q", got.Resource)
	}
	if got.Development {
		t.Fatal("HTTPS issuer must not enable development HTTP")
	}
	if got.AccessTokenTTL != 20*time.Minute {
		t.Fatalf("access TTL = %s", got.AccessTokenTTL)
	}
	if got.RefreshTokenTTL != 7*24*time.Hour {
		t.Fatalf("refresh TTL = %s", got.RefreshTokenTTL)
	}
}

func TestNewOAuthServerRuntimeConfigDerivesResourceFromIssuer(t *testing.T) {
	got := newOAuthServerRuntimeConfig(config.OAuthServerConfig{
		Issuer: "https://astonish.example",
	}, 9393, false)
	if got.Resource != "https://astonish.example/api/mcp" {
		t.Fatalf("resource = %q", got.Resource)
	}
	if got.Development {
		t.Fatal("non-builtin HTTPS issuer must not enable development HTTP")
	}
}
