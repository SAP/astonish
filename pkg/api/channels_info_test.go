package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/store"
)

// mockChannel is a minimal Channel implementation for testing.
type mockChannel struct {
	id     string
	status channels.ChannelStatus
}

func (m *mockChannel) ID() string                                               { return m.id }
func (m *mockChannel) Name() string                                             { return m.id }
func (m *mockChannel) Start(_ context.Context, _ channels.MessageHandler) error { return nil }
func (m *mockChannel) Stop(_ context.Context) error                             { return nil }
func (m *mockChannel) Send(_ context.Context, _ channels.Target, _ channels.OutboundMessage) error {
	return nil
}
func (m *mockChannel) BroadcastTargets() []channels.Target                   { return nil }
func (m *mockChannel) SendTyping(_ context.Context, _ channels.Target) error { return nil }
func (m *mockChannel) Status() channels.ChannelStatus                        { return m.status }

// For Telegram: implements BotUsernameProvider
func (m *mockChannel) BotUsername() string { return m.status.AccountID }

// channelInfoResponse matches the JSON returned by handleGetChannelInfo.
type channelInfoResponse struct {
	Telegram struct {
		BotUsername string `json:"bot_username"`
		Configured  bool   `json:"configured"`
		Connected   bool   `json:"connected"`
		Enabled     bool   `json:"enabled"`
		Error       string `json:"error"`
	} `json:"telegram"`
	Email struct {
		Configured bool   `json:"configured"`
		Connected  bool   `json:"connected"`
		Enabled    bool   `json:"enabled"`
		Error      string `json:"error"`
		Address    string `json:"address"`
	} `json:"email"`
	Slack struct {
		BotUserID  string `json:"bot_user_id"`
		Configured bool   `json:"configured"`
		Connected  bool   `json:"connected"`
		Enabled    bool   `json:"enabled"`
		Error      string `json:"error"`
	} `json:"slack"`
}

func callChannelInfo(t *testing.T) channelInfoResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/channels/info", nil)
	w := httptest.NewRecorder()
	handleGetChannelInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp channelInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	return resp
}

// TestChannelInfo_EmailEnabledNotConnected tests the primary bug scenario:
// email channel is enabled in config (admin set it up) but IMAP hasn't
// connected yet. The "configured" field should be true so the UI shows
// the "Link Email" button.
func TestChannelInfo_EmailEnabledNotConnected(t *testing.T) {
	// Save and restore global state
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	// Simulate: email enabled in config, no channel manager connected
	SetChannelConfigStatuses(map[string]ChannelConfigStatus{
		"email": {Enabled: true, Error: ""},
	})
	SetChannelManager(nil) // No channel manager yet (or email not connected)

	resp := callChannelInfo(t)

	if !resp.Email.Configured {
		t.Error("expected email.configured=true when enabled in config (even if not connected)")
	}
	if resp.Email.Connected {
		t.Error("expected email.connected=false when channel manager is nil")
	}
	if !resp.Email.Enabled {
		t.Error("expected email.enabled=true")
	}
}

// TestChannelInfo_EmailEnabledAndConnected tests steady state:
// email is enabled and the IMAP connection has completed.
func TestChannelInfo_EmailEnabledAndConnected(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	SetChannelConfigStatuses(map[string]ChannelConfigStatus{
		"email": {Enabled: true, Error: ""},
	})

	// Create a channel manager with a mock email channel that is connected
	mgr := channels.NewChannelManager(nil, nil, log.Default(), nil)
	mgr.Register(&mockChannel{
		id:     "email",
		status: channels.ChannelStatus{Connected: true, AccountID: "bot@example.com"},
	})
	SetChannelManager(mgr)

	resp := callChannelInfo(t)

	if !resp.Email.Configured {
		t.Error("expected email.configured=true")
	}
	if !resp.Email.Connected {
		t.Error("expected email.connected=true")
	}
	if !resp.Email.Enabled {
		t.Error("expected email.enabled=true")
	}
	if resp.Email.Address != "bot@example.com" {
		t.Errorf("expected email.address='bot@example.com', got %q", resp.Email.Address)
	}
}

// TestChannelInfo_EmailNotConfigured tests when no email config exists at all.
func TestChannelInfo_EmailNotConfigured(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	// No email in config statuses at all
	SetChannelConfigStatuses(map[string]ChannelConfigStatus{})
	SetChannelManager(nil)

	resp := callChannelInfo(t)

	if resp.Email.Configured {
		t.Error("expected email.configured=false when not in config")
	}
	if resp.Email.Connected {
		t.Error("expected email.connected=false")
	}
	if resp.Email.Enabled {
		t.Error("expected email.enabled=false")
	}
}

// TestChannelInfo_EmailEnabledWithError tests when email is enabled but has
// a config error (e.g., "password not configured"). Should still show
// configured=true so the UI recognizes the admin has set it up.
func TestChannelInfo_EmailEnabledWithError(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	SetChannelConfigStatuses(map[string]ChannelConfigStatus{
		"email": {Enabled: true, Error: "password not configured"},
	})
	SetChannelManager(nil)

	resp := callChannelInfo(t)

	if !resp.Email.Configured {
		t.Error("expected email.configured=true even with error (admin set it up)")
	}
	if resp.Email.Connected {
		t.Error("expected email.connected=false when there's a config error")
	}
	if !resp.Email.Enabled {
		t.Error("expected email.enabled=true")
	}
	if resp.Email.Error != "password not configured" {
		t.Errorf("expected error='password not configured', got %q", resp.Email.Error)
	}
}

// TestChannelInfo_TelegramEnabledNotConnected tests the same race condition
// for Telegram: enabled but bot hasn't authenticated yet.
func TestChannelInfo_TelegramEnabledNotConnected(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	SetChannelConfigStatuses(map[string]ChannelConfigStatus{
		"telegram": {Enabled: true, Error: ""},
	})
	SetChannelManager(nil)

	resp := callChannelInfo(t)

	if !resp.Telegram.Configured {
		t.Error("expected telegram.configured=true when enabled in config")
	}
	if resp.Telegram.Connected {
		t.Error("expected telegram.connected=false when not yet authenticated")
	}
	if !resp.Telegram.Enabled {
		t.Error("expected telegram.enabled=true")
	}
}

// TestChannelInfo_TelegramConnected tests Telegram fully connected state.
func TestChannelInfo_TelegramConnected(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	SetChannelConfigStatuses(map[string]ChannelConfigStatus{
		"telegram": {Enabled: true, Error: ""},
	})

	mgr := channels.NewChannelManager(nil, nil, log.Default(), nil)
	mgr.Register(&mockChannel{
		id:     "telegram",
		status: channels.ChannelStatus{Connected: true, AccountID: "testbot"},
	})
	SetChannelManager(mgr)

	resp := callChannelInfo(t)

	if !resp.Telegram.Configured {
		t.Error("expected telegram.configured=true")
	}
	if !resp.Telegram.Connected {
		t.Error("expected telegram.connected=true")
	}
	if resp.Telegram.BotUsername != "testbot" {
		t.Errorf("expected bot_username='testbot', got %q", resp.Telegram.BotUsername)
	}
}

// TestChannelInfo_SlackEnabledNotConnected tests Slack enabled but not connected.
func TestChannelInfo_SlackEnabledNotConnected(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
	}()

	SetChannelConfigStatuses(map[string]ChannelConfigStatus{
		"slack": {Enabled: true, Error: ""},
	})
	SetChannelManager(nil)

	resp := callChannelInfo(t)

	if !resp.Slack.Configured {
		t.Error("expected slack.configured=true when enabled in config")
	}
	if resp.Slack.Connected {
		t.Error("expected slack.connected=false")
	}
	if !resp.Slack.Enabled {
		t.Error("expected slack.enabled=true")
	}
}

// --- DB-fallback tests (API-mode pod scenario) ---

// mockPlatformSettingsStoreForChannelInfo implements store.PlatformSettingsStore.
type mockPlatformSettingsStoreForChannelInfo struct {
	settings *store.PlatformSettings
}

func (m *mockPlatformSettingsStoreForChannelInfo) Get(_ context.Context) (*store.PlatformSettings, error) {
	return m.settings, nil
}

func (m *mockPlatformSettingsStoreForChannelInfo) Save(_ context.Context, _ *store.PlatformSettings) error {
	return nil
}

// mockPlatformBackendForChannelInfo satisfies store.PlatformBackend with only
// PlatformSettings() returning useful data. All other methods are stubs.
type mockPlatformBackendForChannelInfo struct {
	settingsStore store.PlatformSettingsStore
}

func (m *mockPlatformBackendForChannelInfo) Organizations() store.OrganizationStore { return nil }
func (m *mockPlatformBackendForChannelInfo) Users() store.UserStore                 { return nil }
func (m *mockPlatformBackendForChannelInfo) LoginSessions() store.LoginSessionStore { return nil }
func (m *mockPlatformBackendForChannelInfo) OIDCProviders() store.OIDCProviderStore { return nil }
func (m *mockPlatformBackendForChannelInfo) UserChannels() store.UserChannelStore   { return nil }
func (m *mockPlatformBackendForChannelInfo) Close() error                           { return nil }
func (m *mockPlatformBackendForChannelInfo) ForOrg(_ string) (store.OrgDataStore, error) {
	return nil, nil
}
func (m *mockPlatformBackendForChannelInfo) ProvisionOrg(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockPlatformBackendForChannelInfo) DecommissionOrg(_ context.Context, _ string) error {
	return nil
}
func (m *mockPlatformBackendForChannelInfo) InstanceSuffix() string { return "" }
func (m *mockPlatformBackendForChannelInfo) PlatformSettings() store.PlatformSettingsStore {
	return m.settingsStore
}
func (m *mockPlatformBackendForChannelInfo) OrgSettings(_ string) store.OrgSettingsStore { return nil }
func (m *mockPlatformBackendForChannelInfo) PlatformMCPServers() store.MCPServerStore    { return nil }
func (m *mockPlatformBackendForChannelInfo) PlatformSkills() store.SkillStore            { return nil }
func (m *mockPlatformBackendForChannelInfo) PlatformSlideTemplates() store.SlideTemplateStore {
	return nil
}
func (m *mockPlatformBackendForChannelInfo) SetEmbedFunc(_ store.EmbedFunc)  {}
func (m *mockPlatformBackendForChannelInfo) GetEmbedFunc() store.EmbedFunc   { return nil }
func (m *mockPlatformBackendForChannelInfo) SandboxLayers() store.LayerStore { return nil }
func (m *mockPlatformBackendForChannelInfo) SandboxTemplates() store.SandboxTemplateStore {
	return nil
}
func (m *mockPlatformBackendForChannelInfo) SecretGetter() func(string) string      { return nil }
func (m *mockPlatformBackendForChannelInfo) MigrateAll(_ context.Context) error     { return nil }
func (m *mockPlatformBackendForChannelInfo) CleanupExpired(_ context.Context) error { return nil }
func (m *mockPlatformBackendForChannelInfo) PlatformDB() *sql.DB                    { return nil }
func (m *mockPlatformBackendForChannelInfo) MigrateAllSchemas(_ context.Context) error {
	return nil
}
func (m *mockPlatformBackendForChannelInfo) NewLinkCodeStore() store.LinkCodeStore { return nil }

// TestChannelInfo_DBFallback_EmailEnabled tests the API-mode pod scenario:
// channelConfigStatuses is nil (no channels running locally), but the platform
// DB has email enabled. The endpoint should fall back to the DB and report
// configured=true so the UI shows the "Link Email" button.
func TestChannelInfo_DBFallback_EmailEnabled(t *testing.T) {
	// Save and restore global state
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	origBackend := platformBackendInstance
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
		platformBackendInstance = origBackend
	}()

	// Simulate API-mode pod: no channel config statuses, no channel manager
	SetChannelConfigStatuses(nil)
	SetChannelManager(nil)

	// But platform DB has email enabled
	platformBackendInstance = &mockPlatformBackendForChannelInfo{
		settingsStore: &mockPlatformSettingsStoreForChannelInfo{
			settings: &store.PlatformSettings{
				Channels: &store.PlatformChannelSettings{
					Email: &store.PlatformEmailConfig{
						Enabled: true,
						Address: "agent@example.com",
					},
					Telegram: &store.PlatformTelegramConfig{
						Enabled: true,
					},
					Slack: &store.PlatformSlackConfig{
						Enabled: true,
						Mode:    "socket",
					},
				},
			},
		},
	}

	resp := callChannelInfo(t)

	// Email should be configured (from DB)
	if !resp.Email.Configured {
		t.Error("expected email.configured=true from DB fallback")
	}
	if !resp.Email.Enabled {
		t.Error("expected email.enabled=true from DB fallback")
	}
	if resp.Email.Connected {
		t.Error("expected email.connected=false (no channel manager)")
	}
	if resp.Email.Address != "agent@example.com" {
		t.Errorf("expected email.address='agent@example.com', got %q", resp.Email.Address)
	}

	// Telegram should be configured (from DB)
	if !resp.Telegram.Configured {
		t.Error("expected telegram.configured=true from DB fallback")
	}
	if !resp.Telegram.Enabled {
		t.Error("expected telegram.enabled=true from DB fallback")
	}

	// Slack should be configured (from DB)
	if !resp.Slack.Configured {
		t.Error("expected slack.configured=true from DB fallback")
	}
	if !resp.Slack.Enabled {
		t.Error("expected slack.enabled=true from DB fallback")
	}
}

// TestChannelInfo_DBFallback_NothingEnabled tests when the DB has channels
// configured but none are enabled. Should report configured=false.
func TestChannelInfo_DBFallback_NothingEnabled(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	origBackend := platformBackendInstance
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
		platformBackendInstance = origBackend
	}()

	SetChannelConfigStatuses(nil)
	SetChannelManager(nil)

	platformBackendInstance = &mockPlatformBackendForChannelInfo{
		settingsStore: &mockPlatformSettingsStoreForChannelInfo{
			settings: &store.PlatformSettings{
				Channels: &store.PlatformChannelSettings{
					Email: &store.PlatformEmailConfig{
						Enabled: false,
						Address: "agent@example.com",
					},
				},
			},
		},
	}

	resp := callChannelInfo(t)

	if resp.Email.Configured {
		t.Error("expected email.configured=false when disabled in DB")
	}
	if resp.Email.Enabled {
		t.Error("expected email.enabled=false when disabled in DB")
	}
}

// TestChannelInfo_DBFallback_NoBackend tests the edge case where neither
// channelConfigStatuses nor a platform backend are available (personal mode
// with no channels). Should return configured=false for everything.
func TestChannelInfo_DBFallback_NoBackend(t *testing.T) {
	origStatuses := getChannelConfigStatuses()
	origMgr := GetChannelManager()
	origBackend := platformBackendInstance
	defer func() {
		SetChannelConfigStatuses(origStatuses)
		SetChannelManager(origMgr)
		platformBackendInstance = origBackend
	}()

	SetChannelConfigStatuses(nil)
	SetChannelManager(nil)
	platformBackendInstance = nil

	resp := callChannelInfo(t)

	if resp.Email.Configured {
		t.Error("expected email.configured=false when no backend")
	}
	if resp.Telegram.Configured {
		t.Error("expected telegram.configured=false when no backend")
	}
	if resp.Slack.Configured {
		t.Error("expected slack.configured=false when no backend")
	}
}
