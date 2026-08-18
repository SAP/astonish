package daemon

import (
	"context"

	"github.com/SAP/astonish/pkg/config"
)

// loadChannelsConfigFromDB returns channel configuration sourced exclusively
// from the platform database (PlatformSettings.Channels + platform_secrets).
// This is the single source of truth for channels in platform mode (SQLite or Postgres).
//
// Non-secret fields (servers, addresses, poll intervals, etc.) come from
// PlatformChannelSettings in the platform_settings table.
//
// Secrets (bot tokens, passwords, signing secrets, etc.) are NOT populated here.
// Callers must resolve them on demand using resolveDaemonSecret(backend, credStore, key)
// so that the latest encrypted values are always used.
func loadChannelsConfigFromDB(backend platformDB, logger *Logger) config.ChannelsConfig {
	out := config.ChannelsConfig{}

	if backend == nil {
		return out
	}

	settingsStore := backend.PlatformSettings()
	settings, err := settingsStore.Get(context.Background())
	if err != nil || settings == nil || settings.Channels == nil {
		return out
	}

	ch := settings.Channels

	// Telegram: only the enabled flag is stored as non-secret config.
	if ch.Telegram != nil {
		enabled := ch.Telegram.Enabled
		out.Telegram.Enabled = &enabled
		if enabled && logger != nil {
			logger.Printf("[channels] DB config: Telegram enabled")
		}
	}

	// Email: full non-secret configuration
	if ch.Email != nil {
		enabled := ch.Email.Enabled
		out.Email.Enabled = &enabled
		out.Email.Provider = ch.Email.Provider
		out.Email.IMAPServer = ch.Email.IMAPServer
		out.Email.SMTPServer = ch.Email.SMTPServer
		out.Email.Address = ch.Email.Address
		out.Email.Username = ch.Email.Username
		out.Email.Credential = ch.Email.Credential
		out.Email.PollInterval = ch.Email.PollInterval
		out.Email.Folder = ch.Email.Folder
		out.Email.MarkRead = ch.Email.MarkRead
		out.Email.MaxBodyChars = ch.Email.MaxBodyChars

		if enabled && logger != nil {
			if ch.Email.Provider == "msgraph" {
				logger.Printf("[channels] DB config: Email enabled (msgraph) address=%s credential=%s",
					out.Email.Address, out.Email.Credential)
			} else {
				logger.Printf("[channels] DB config: Email enabled address=%s imap=%s smtp=%s",
					out.Email.Address, out.Email.IMAPServer, out.Email.SMTPServer)
			}
		}
	}

	// Slack
	if ch.Slack != nil {
		enabled := ch.Slack.Enabled
		out.Slack.Enabled = &enabled
		out.Slack.Mode = ch.Slack.Mode
		out.Slack.AppID = ch.Slack.AppID
		out.Slack.ConfigToken = ch.Slack.ConfigToken
		out.Slack.CommandURL = ch.Slack.CommandURL

		if enabled && logger != nil {
			logger.Printf("[channels] DB config: Slack enabled mode=%s", out.Slack.GetMode())
		}
	}

	// A2A (Agent-to-Agent protocol)
	if ch.A2A != nil {
		enabled := ch.A2A.Enabled
		out.A2A.Enabled = &enabled
		out.A2A.BaseURL = ch.A2A.BaseURL
		out.A2A.TaskTTL = ch.A2A.TaskTTL
		out.A2A.DefaultAudience = ch.A2A.DefaultAudience
		out.A2A.AutoLinkByEmail = ch.A2A.AutoLinkByEmail
		out.A2A.RequireActorClaim = ch.A2A.RequireActorClaim

		// Populate trusted issuers from DB config
		for _, iss := range ch.A2A.TrustedIssuers {
			out.A2A.TrustedIssuers = append(out.A2A.TrustedIssuers, config.TrustedIssuerConfig{
				Name:      iss.Name,
				Issuer:    iss.Issuer,
				JWKSURL:   iss.JWKSURL,
				Audience:  iss.Audience,
				UserClaim: iss.UserClaim,
			})
		}

		// Populate allowed agents from DB config
		for _, ag := range ch.A2A.AllowedAgents {
			out.A2A.AllowedAgents = append(out.A2A.AllowedAgents, config.AllowedAgentConfig{
				Name:      ag.Name,
				ActorSub:  ag.ActorSub,
				Issuer:    ag.Issuer,
				RateLimit: ag.RateLimit,
				MaxTasks:  ag.MaxTasks,
			})
		}

		if enabled && logger != nil {
			logger.Printf("[channels] DB config: A2A enabled base_url=%s issuers=%d agents=%d",
				out.A2A.BaseURL, len(out.A2A.TrustedIssuers), len(out.A2A.AllowedAgents))
		}
	}

	return out
}

// anyChannelEnabled returns true if at least one channel has its Enabled flag set.
func anyChannelEnabled(ch config.ChannelsConfig) bool {
	return ch.Telegram.IsTelegramEnabled() ||
		ch.Email.IsEmailEnabled() ||
		ch.Slack.IsSlackEnabled() ||
		ch.A2A.IsA2AEnabled()
}
