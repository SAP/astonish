package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

func TestLoadChannelsConfigFromDB_NilBackend(t *testing.T) {
	cfg := loadChannelsConfigFromDB(nil, nil)
	if anyChannelEnabled(cfg) {
		t.Error("expected no channels enabled for nil backend")
	}
}

func TestLoadChannelsConfigFromDB_NoSettings(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	cfg := loadChannelsConfigFromDB(esStore, nil)
	if anyChannelEnabled(cfg) {
		t.Error("expected no channels when nothing stored in DB")
	}
}

func TestLoadChannelsConfigFromDB_TelegramEnabled(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	settings := &store.PlatformSettings{
		Channels: &store.PlatformChannelSettings{
			Telegram: &store.PlatformTelegramConfig{Enabled: true},
		},
	}
	if err := esStore.PlatformSettings().Save(context.Background(), settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	cfg := loadChannelsConfigFromDB(esStore, nil)
	if !cfg.Telegram.IsTelegramEnabled() {
		t.Error("expected Telegram to be enabled")
	}
	if !anyChannelEnabled(cfg) {
		t.Error("expected anyChannelEnabled to be true")
	}
	if cfg.Email.IsEmailEnabled() || cfg.Slack.IsSlackEnabled() {
		t.Error("expected only Telegram enabled")
	}
}

func TestLoadChannelsConfigFromDB_EmailFullConfig(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	markRead := true
	settings := &store.PlatformSettings{
		Channels: &store.PlatformChannelSettings{
			Email: &store.PlatformEmailConfig{
				Enabled:      true,
				Provider:     "imap",
				IMAPServer:   "imap.example.com:993",
				SMTPServer:   "smtp.example.com:587",
				Address:      "bot@example.com",
				Username:     "bot",
				PollInterval: 45,
				Folder:       "INBOX",
				MarkRead:     &markRead,
				MaxBodyChars: 5000,
			},
		},
	}
	if err := esStore.PlatformSettings().Save(context.Background(), settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	cfg := loadChannelsConfigFromDB(esStore, nil)
	if !cfg.Email.IsEmailEnabled() {
		t.Fatal("expected Email enabled")
	}
	e := cfg.Email
	if e.Provider != "imap" || e.IMAPServer != "imap.example.com:993" || e.Address != "bot@example.com" {
		t.Errorf("unexpected email fields: %+v", e)
	}
	if e.GetPollInterval() != 45 {
		t.Errorf("expected poll interval 45, got %d", e.GetPollInterval())
	}
	if !anyChannelEnabled(cfg) {
		t.Error("expected anyChannelEnabled true")
	}
}

func TestLoadChannelsConfigFromDB_SlackEnabled(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	settings := &store.PlatformSettings{
		Channels: &store.PlatformChannelSettings{
			Slack: &store.PlatformSlackConfig{
				Enabled:     true,
				Mode:        "events",
				AppID:       "A123",
				ConfigToken: "xoxe-test",
				CommandURL:  "https://example.com/api/slack/commands",
			},
		},
	}
	if err := esStore.PlatformSettings().Save(context.Background(), settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	cfg := loadChannelsConfigFromDB(esStore, nil)
	if !cfg.Slack.IsSlackEnabled() {
		t.Fatal("expected Slack enabled")
	}
	if cfg.Slack.GetMode() != "events" {
		t.Errorf("expected mode events, got %s", cfg.Slack.GetMode())
	}
	if cfg.Slack.AppID != "A123" {
		t.Errorf("expected app ID A123, got %q", cfg.Slack.AppID)
	}
	if cfg.Slack.ConfigToken != "xoxe-test" {
		t.Errorf("expected config token fallback to be loaded, got %q", cfg.Slack.ConfigToken)
	}
	if cfg.Slack.CommandURL != "https://example.com/api/slack/commands" {
		t.Errorf("expected command URL to be loaded, got %q", cfg.Slack.CommandURL)
	}
	if !anyChannelEnabled(cfg) {
		t.Error("expected anyChannelEnabled true")
	}
}

func TestLoadChannelsConfigFromDB_AllDisabled(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	settings := &store.PlatformSettings{
		Channels: &store.PlatformChannelSettings{
			Telegram: &store.PlatformTelegramConfig{Enabled: false},
			Email:    &store.PlatformEmailConfig{Enabled: false},
			Slack:    &store.PlatformSlackConfig{Enabled: false},
		},
	}
	if err := esStore.PlatformSettings().Save(context.Background(), settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	cfg := loadChannelsConfigFromDB(esStore, nil)
	if anyChannelEnabled(cfg) {
		t.Error("expected anyChannelEnabled false when all are explicitly disabled")
	}
}

func TestAnyChannelEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ChannelsConfig
		want bool
	}{
		{"empty", config.ChannelsConfig{}, false},
		{"telegram only", func() config.ChannelsConfig {
			en := true
			c := config.ChannelsConfig{}
			c.Telegram.Enabled = &en
			return c
		}(), true},
		{"email only", func() config.ChannelsConfig {
			en := true
			c := config.ChannelsConfig{}
			c.Email.Enabled = &en
			return c
		}(), true},
		{"slack only", func() config.ChannelsConfig {
			en := true
			c := config.ChannelsConfig{}
			c.Slack.Enabled = &en
			return c
		}(), true},
		{"a2a only", func() config.ChannelsConfig {
			en := true
			c := config.ChannelsConfig{}
			c.A2A.Enabled = &en
			return c
		}(), true},
		{"all disabled", func() config.ChannelsConfig {
			f := false
			c := config.ChannelsConfig{}
			c.Telegram.Enabled = &f
			c.Email.Enabled = &f
			c.Slack.Enabled = &f
			return c
		}(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyChannelEnabled(tt.cfg); got != tt.want {
				t.Errorf("anyChannelEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadChannelsConfigFromDB_A2AEnabled(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	settings := &store.PlatformSettings{
		Channels: &store.PlatformChannelSettings{
			A2A: &store.PlatformA2AConfig{
				Enabled: true,
				BaseURL: "https://a2a.example.com",
				TaskTTL: "48h",
			},
		},
	}
	if err := esStore.PlatformSettings().Save(context.Background(), settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	cfg := loadChannelsConfigFromDB(esStore, nil)
	if !cfg.A2A.IsA2AEnabled() {
		t.Error("expected A2A to be enabled")
	}
	if cfg.A2A.BaseURL != "https://a2a.example.com" {
		t.Errorf("expected base_url 'https://a2a.example.com', got %q", cfg.A2A.BaseURL)
	}
	if cfg.A2A.TaskTTL != "48h" {
		t.Errorf("expected task_ttl '48h', got %q", cfg.A2A.TaskTTL)
	}
}

func TestLoadChannelsConfigFromDB_A2ATrustedIssuers(t *testing.T) {
	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	defer esStore.Close()

	settings := &store.PlatformSettings{
		Channels: &store.PlatformChannelSettings{
			A2A: &store.PlatformA2AConfig{
				Enabled:           true,
				BaseURL:           "https://a2a.example.com",
				DefaultAudience:   "https://api.example.com",
				RequireActorClaim: true,
				TrustedIssuers: []store.PlatformTrustedIssuer{
					{
						Name:      "corp-idp",
						Issuer:    "https://auth.corp.com",
						JWKSURL:   "https://auth.corp.com/.well-known/jwks.json",
						Audience:  "https://api.example.com",
						UserClaim: "email",
					},
					{
						Name:     "partner-idp",
						Issuer:   "https://partner.example.com",
						JWKSURL:  "https://partner.example.com/keys",
						Audience: "https://api.example.com",
					},
				},
				AllowedAgents: []store.PlatformAllowedAgent{
					{
						Name:      "CI Bot",
						ActorSub:  "ci-bot-123",
						Issuer:    "corp-idp",
						RateLimit: 100,
						MaxTasks:  10,
					},
				},
			},
		},
	}
	if err := esStore.PlatformSettings().Save(context.Background(), settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	cfg := loadChannelsConfigFromDB(esStore, nil)

	if !cfg.A2A.IsA2AEnabled() {
		t.Error("expected A2A to be enabled")
	}
	if cfg.A2A.DefaultAudience != "https://api.example.com" {
		t.Errorf("expected default_audience, got %q", cfg.A2A.DefaultAudience)
	}
	if !cfg.A2A.RequireActorClaim {
		t.Error("expected require_actor_claim to be true")
	}

	// Verify trusted issuers
	if len(cfg.A2A.TrustedIssuers) != 2 {
		t.Fatalf("expected 2 trusted issuers, got %d", len(cfg.A2A.TrustedIssuers))
	}
	iss := cfg.A2A.TrustedIssuers[0]
	if iss != (config.TrustedIssuerConfig{
		Name:      "corp-idp",
		Issuer:    "https://auth.corp.com",
		JWKSURL:   "https://auth.corp.com/.well-known/jwks.json",
		Audience:  "https://api.example.com",
		UserClaim: "email",
	}) {
		t.Errorf("issuer[0] mismatch: %+v", iss)
	}
	if cfg.A2A.TrustedIssuers[1].Name != "partner-idp" {
		t.Errorf("issuer[1] name mismatch: %q", cfg.A2A.TrustedIssuers[1].Name)
	}

	// Verify allowed agents
	if len(cfg.A2A.AllowedAgents) != 1 {
		t.Fatalf("expected 1 allowed agent, got %d", len(cfg.A2A.AllowedAgents))
	}
	ag := cfg.A2A.AllowedAgents[0]
	if ag != (config.AllowedAgentConfig{
		Name:      "CI Bot",
		ActorSub:  "ci-bot-123",
		Issuer:    "corp-idp",
		RateLimit: 100,
		MaxTasks:  10,
	}) {
		t.Errorf("agent[0] mismatch: %+v", ag)
	}
}
