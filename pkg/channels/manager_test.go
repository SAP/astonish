package channels

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/store"
)

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short unchanged", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncation adds ellipsis", "hello world", 8, "hello..."},
		{"empty string", "", 10, ""},
		{"truncate to minimum", "abcdef", 4, "a..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestChannelHints(t *testing.T) {
	t.Parallel()
	t.Run("telegram returns non-empty", func(t *testing.T) {
		t.Parallel()
		hints := channelHints("telegram")
		if hints == "" {
			t.Error("channelHints(\"telegram\") should return non-empty string")
		}
		if !strings.Contains(hints, "Telegram") {
			t.Error("telegram hints should mention Telegram")
		}
	})

	t.Run("slack encourages compact markdown tables for structured records", func(t *testing.T) {
		t.Parallel()
		hints := channelHints("slack")
		if hints == "" {
			t.Fatal("channelHints(\"slack\") should return non-empty string")
		}
		if !strings.Contains(hints, "Slack") {
			t.Error("slack hints should mention Slack")
		}
		if !strings.Contains(hints, "structured records") || !strings.Contains(hints, "compact Markdown table") {
			t.Errorf("slack hints should encourage compact Markdown tables for structured records, got %q", hints)
		}
		if strings.Contains(hints, "NEVER use markdown tables") {
			t.Errorf("slack hints should not forbid Markdown tables, got %q", hints)
		}
		for _, forbidden := range []string{"OpenStack", "VM", "Kubernetes", "ticket", "invoice"} {
			if strings.Contains(hints, forbidden) {
				t.Errorf("slack hints should be domain-agnostic; found %q in %q", forbidden, hints)
			}
		}
	})

	t.Run("telegram still avoids markdown tables", func(t *testing.T) {
		t.Parallel()
		hints := channelHints("telegram")
		if !strings.Contains(hints, "NEVER use markdown tables") {
			t.Errorf("telegram hints should still forbid Markdown tables, got %q", hints)
		}
	})

	t.Run("email returns non-empty", func(t *testing.T) {
		t.Parallel()
		hints := channelHints("email")
		if hints == "" {
			t.Error("channelHints(\"email\") should return non-empty string")
		}
		if !strings.Contains(hints, "email") {
			t.Error("email hints should mention email")
		}
	})

	t.Run("unknown returns empty", func(t *testing.T) {
		t.Parallel()
		hints := channelHints("unknown_channel")
		if hints != "" {
			t.Errorf("channelHints(\"unknown_channel\") = %q, want empty", hints)
		}
	})

	t.Run("empty returns empty", func(t *testing.T) {
		t.Parallel()
		hints := channelHints("")
		if hints != "" {
			t.Errorf("channelHints(\"\") = %q, want empty", hints)
		}
	})
}

func TestFriendlyErrorMessage(t *testing.T) {
	t.Parallel()
	t.Run("rate limited error", func(t *testing.T) {
		t.Parallel()
		err := llmerror.NewLLMError("openai", 429, "rate limited", "")
		msg := friendlyErrorMessage(err)
		if !strings.Contains(msg, "rate limited") {
			t.Errorf("expected rate limit message, got %q", msg)
		}
	})

	t.Run("auth error", func(t *testing.T) {
		t.Parallel()
		err := llmerror.NewLLMError("openai", 401, "unauthorized", "")
		msg := friendlyErrorMessage(err)
		if !strings.Contains(msg, "Authentication") {
			t.Errorf("expected auth error message, got %q", msg)
		}
	})

	t.Run("server error", func(t *testing.T) {
		t.Parallel()
		err := llmerror.NewLLMError("openai", 500, "server error", "")
		msg := friendlyErrorMessage(err)
		if !strings.Contains(msg, "provider is experiencing issues") {
			t.Errorf("expected server error message, got %q", msg)
		}
	})

	t.Run("other status code", func(t *testing.T) {
		t.Parallel()
		err := llmerror.NewLLMError("openai", 400, "bad request", "")
		msg := friendlyErrorMessage(err)
		if !strings.Contains(msg, "HTTP 400") {
			t.Errorf("expected HTTP status code in message, got %q", msg)
		}
	})

	t.Run("plain error", func(t *testing.T) {
		t.Parallel()
		err := errors.New("something broke")
		msg := friendlyErrorMessage(err)
		if !strings.Contains(msg, "something broke") {
			t.Errorf("expected error text in message, got %q", msg)
		}
		if !strings.Contains(msg, "Sorry") {
			t.Errorf("expected 'Sorry' prefix, got %q", msg)
		}
	})

	t.Run("long error message is truncated", func(t *testing.T) {
		t.Parallel()
		longMsg := strings.Repeat("x", 300)
		err := fmt.Errorf("%s", longMsg)
		msg := friendlyErrorMessage(err)
		if len(msg) > 300 {
			// The output should contain the truncated error message (200 chars + "...")
			if !strings.Contains(msg, "...") {
				t.Errorf("expected truncation ellipsis in long error message")
			}
		}
	})
}

func TestFleetSessionTracking(t *testing.T) {
	t.Parallel()
	logger := log.Default()
	m := &ChannelManager{
		channels:        make(map[string]Channel),
		activeFleets:    make(map[string]string),
		pendingContexts: make(map[string]string),
		logger:          logger,
	}

	t.Run("get returns empty for unset key", func(t *testing.T) {
		got := m.GetActiveFleet("nonexistent")
		if got != "" {
			t.Errorf("GetActiveFleet for unset key = %q, want empty", got)
		}
	})

	t.Run("set and get", func(t *testing.T) {
		m.SetActiveFleet("chat1", "fleet-session-123")
		got := m.GetActiveFleet("chat1")
		if got != "fleet-session-123" {
			t.Errorf("GetActiveFleet = %q, want %q", got, "fleet-session-123")
		}
	})

	t.Run("clear removes mapping", func(t *testing.T) {
		m.SetActiveFleet("chat2", "fleet-session-456")
		m.ClearActiveFleet("chat2")
		got := m.GetActiveFleet("chat2")
		if got != "" {
			t.Errorf("GetActiveFleet after Clear = %q, want empty", got)
		}
	})

	t.Run("clear non-existent is safe", func(t *testing.T) {
		m.ClearActiveFleet("never-set")
		// Should not panic
	})

	t.Run("overwrite existing", func(t *testing.T) {
		m.SetActiveFleet("chat3", "first")
		m.SetActiveFleet("chat3", "second")
		got := m.GetActiveFleet("chat3")
		if got != "second" {
			t.Errorf("GetActiveFleet after overwrite = %q, want %q", got, "second")
		}
	})
}

func TestSessionContext(t *testing.T) {
	t.Parallel()
	logger := log.Default()
	m := &ChannelManager{
		channels:        make(map[string]Channel),
		activeFleets:    make(map[string]string),
		pendingContexts: make(map[string]string),
		logger:          logger,
	}

	t.Run("set then consume", func(t *testing.T) {
		m.SetSessionContext("sess1", "wizard prompt here")
		got := m.consumeSessionContext("sess1")
		if got != "wizard prompt here" {
			t.Errorf("consumeSessionContext = %q, want %q", got, "wizard prompt here")
		}
	})

	t.Run("double consume returns empty", func(t *testing.T) {
		m.SetSessionContext("sess2", "one-shot context")
		_ = m.consumeSessionContext("sess2")
		got := m.consumeSessionContext("sess2")
		if got != "" {
			t.Errorf("second consumeSessionContext = %q, want empty", got)
		}
	})

	t.Run("consume unset key returns empty", func(t *testing.T) {
		got := m.consumeSessionContext("never-set")
		if got != "" {
			t.Errorf("consumeSessionContext for unset key = %q, want empty", got)
		}
	})

	t.Run("overwrite before consume", func(t *testing.T) {
		m.SetSessionContext("sess3", "first")
		m.SetSessionContext("sess3", "second")
		got := m.consumeSessionContext("sess3")
		if got != "second" {
			t.Errorf("consumeSessionContext after overwrite = %q, want %q", got, "second")
		}
	})
}

type stubTeamDataStore struct {
	store.TeamDataStore
	pin *store.SessionPin
	err error
}

func (s *stubTeamDataStore) SessionPin(_ context.Context, _ string) (*store.SessionPin, error) {
	return s.pin, s.err
}

type stubTeamSettings struct {
	settings *store.TeamSettings
}

func (s *stubTeamSettings) Get(_ context.Context) (*store.TeamSettings, error) {
	return s.settings, nil
}

func (s *stubTeamSettings) Save(_ context.Context, _ *store.TeamSettings) error {
	return nil
}

func TestResolveEffectiveWithSessionPin(t *testing.T) {
	teamSettings := &stubTeamSettings{
		settings: &store.TeamSettings{
			Providers: map[string]store.ProviderConfig{
				"openai": {"api_key": "sk-team"},
			},
			DefaultProvider: "openai",
			DefaultModel:    "gpt-4",
		},
	}
	ps := &store.ProviderStores{
		Platform: nil,
		Org:      nil,
		Team:     teamSettings,
	}

	t.Run("no pin: cascade wins", func(t *testing.T) {
		tds := &stubTeamDataStore{pin: nil}
		ctx := store.WithTeamDataStore(context.Background(), tds)

		cfg := resolveEffectiveWithSessionPin(ctx, ps, "sess-1", "ch-1", "user-1")

		if cfg.General.DefaultProvider != "openai" {
			t.Errorf("DefaultProvider = %q, want openai", cfg.General.DefaultProvider)
		}
		if cfg.General.DefaultModel != "gpt-4" {
			t.Errorf("DefaultModel = %q, want gpt-4", cfg.General.DefaultModel)
		}
	})

	t.Run("valid pin: overrides cascade", func(t *testing.T) {
		tds := &stubTeamDataStore{pin: &store.SessionPin{Provider: "openai", Model: "gpt-5"}}
		ctx := store.WithTeamDataStore(context.Background(), tds)

		cfg := resolveEffectiveWithSessionPin(ctx, ps, "sess-1", "ch-1", "user-1")

		if cfg.General.DefaultProvider != "openai" {
			t.Errorf("DefaultProvider = %q, want openai", cfg.General.DefaultProvider)
		}
		if cfg.General.DefaultModel != "gpt-5" {
			t.Errorf("DefaultModel = %q, want gpt-5 (pin override)", cfg.General.DefaultModel)
		}
	})

	t.Run("missing-cred pin: warn + cascade fallback", func(t *testing.T) {
		tds := &stubTeamDataStore{pin: &store.SessionPin{Provider: "anthropic", Model: "claude-opus"}}
		ctx := store.WithTeamDataStore(context.Background(), tds)

		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		defer slog.SetDefault(prev)

		cfg := resolveEffectiveWithSessionPin(ctx, ps, "sess-1", "ch-1", "user-1")

		if cfg.General.DefaultProvider != "openai" {
			t.Errorf("DefaultProvider = %q, want openai (fallback)", cfg.General.DefaultProvider)
		}
		if cfg.General.DefaultModel != "gpt-4" {
			t.Errorf("DefaultModel = %q, want gpt-4 (fallback)", cfg.General.DefaultModel)
		}
		if !strings.Contains(buf.String(), "pinned provider has no credential") {
			t.Errorf("expected warn about missing credential, got log: %q", buf.String())
		}
		if !strings.Contains(buf.String(), "pinnedProvider=anthropic") {
			t.Errorf("expected pinnedProvider=anthropic in log, got: %q", buf.String())
		}
	})

	t.Run("no tds in context: cascade only", func(t *testing.T) {
		ctx := context.Background()

		cfg := resolveEffectiveWithSessionPin(ctx, ps, "sess-1", "ch-1", "user-1")

		if cfg.General.DefaultProvider != "openai" {
			t.Errorf("DefaultProvider = %q, want openai", cfg.General.DefaultProvider)
		}
	})

	t.Run("empty session key: skip pin lookup", func(t *testing.T) {
		tds := &stubTeamDataStore{pin: &store.SessionPin{Provider: "should-not-apply", Model: "x"}}
		ctx := store.WithTeamDataStore(context.Background(), tds)

		cfg := resolveEffectiveWithSessionPin(ctx, ps, "", "ch-1", "user-1")

		if cfg.General.DefaultProvider != "openai" {
			t.Errorf("DefaultProvider = %q, want openai (empty key skips lookup)", cfg.General.DefaultProvider)
		}
	})
}
