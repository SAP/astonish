package slack

import (
	"context"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/channels"

	slackapi "github.com/slack-go/slack"
)

const (
	maxSlackCommandDescriptionLength = 2000
	maxSlackCommandUsageHintLength   = 1000
	slackCommandPrefix               = "astonish"
)

var slackBuiltinCommands = []slackapi.ManifestSlashCommand{
	{
		Command:     "/link",
		Description: "Link your Slack account to Astonish",
		UsageHint:   "CODE",
	},
}

func (s *SlackChannel) refreshCommandsBestEffort(ctx context.Context, commands *channels.CommandRegistry) {
	if ok, reason := s.manifestSyncConfigured(); !ok {
		s.logger.Printf("[slack] Slash command manifest sync skipped: %s", reason)
		return
	}
	commandCount := len(buildAstonishSlackCommands(commands, s.manifestCommandURL()))
	s.logger.Printf("[slack] Syncing %d slash command(s) via app manifest for app %s", commandCount, s.config.AppID)
	if err := s.syncCommandManifest(ctx, commands); err != nil {
		s.logger.Printf("[slack] Failed to sync slash commands via app manifest: %v", err)
	}
}

func (s *SlackChannel) manifestSyncConfigured() (bool, string) {
	if s.config == nil {
		return false, "missing Slack config"
	}
	missing := make([]string, 0, 2)
	if strings.TrimSpace(s.config.AppID) == "" {
		missing = append(missing, "app_id")
	}
	configToken := strings.TrimSpace(s.config.ConfigToken)
	if configToken == "" {
		missing = append(missing, "config_token")
	}
	if len(missing) > 0 {
		return false, "missing " + strings.Join(missing, " and ")
	}
	if strings.HasPrefix(configToken, "xapp-") {
		return false, "config_token is an App-Level Token (xapp-...), but Slack App Manifest APIs require an App Configuration Token"
	}
	if strings.HasPrefix(configToken, "xoxb-") {
		return false, "config_token is a Bot User OAuth Token (xoxb-...), but Slack App Manifest APIs require an App Configuration Token"
	}
	if s.usesSocketMode() {
		return true, ""
	}
	if strings.TrimSpace(s.config.CommandURL) == "" {
		return false, "missing command_url; Slack App Manifest slash commands require a public HTTPS request URL such as https://<host>/api/slack/commands when Socket Mode is disabled"
	}
	if !strings.HasPrefix(strings.TrimSpace(s.config.CommandURL), "https://") {
		return false, "command_url must be a public HTTPS URL for Slack slash commands when Socket Mode is disabled"
	}
	return true, ""
}

func (s *SlackChannel) usesSocketMode() bool {
	if s.config == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(s.config.Mode), "") || strings.EqualFold(strings.TrimSpace(s.config.Mode), "socket")
}

func (s *SlackChannel) manifestCommandURL() string {
	if s.usesSocketMode() {
		return ""
	}
	if s.config == nil {
		return ""
	}
	return s.config.CommandURL
}

type manifestValidator interface {
	ValidateManifestContext(ctx context.Context, manifest *slackapi.Manifest, token string, appId string) (*slackapi.ManifestResponse, error)
}

func (s *SlackChannel) syncCommandManifest(ctx context.Context, commands *channels.CommandRegistry) error {
	if s.config.AppID == "" || s.config.ConfigToken == "" {
		return nil
	}

	api := s.api
	if api == nil {
		api = slackapi.New(s.config.BotToken)
	}
	manifest, err := api.ExportManifestContext(ctx, s.config.ConfigToken, s.config.AppID)
	if err != nil {
		return fmt.Errorf("export manifest: %w%s", err, manifestAuthHint(err))
	}

	configureManifestTransport(manifest, s.usesSocketMode())
	mergeAstonishSlashCommands(manifest, buildAstonishSlackCommands(commands, s.manifestCommandURL()), commands)
	if err := validateSlackManifest(ctx, api, manifest, s.config.ConfigToken, s.config.AppID); err != nil {
		return err
	}

	resp, err := api.UpdateManifestContext(ctx, manifest, s.config.ConfigToken, s.config.AppID)
	if err != nil {
		return fmt.Errorf("update manifest: %w", err)
	}
	if resp != nil && resp.PermissionsUpdated {
		s.logger.Printf("[slack] Slash commands synced; Slack reports permission changes, so the app may need reinstalling")
	} else {
		s.logger.Printf("[slack] Slash commands synced via app manifest")
	}
	return nil
}

func configureManifestTransport(manifest *slackapi.Manifest, socketMode bool) {
	if manifest == nil {
		return
	}
	manifest.Settings.SocketModeEnabled = socketMode
}

func buildAstonishSlackCommands(registry *channels.CommandRegistry, commandURL string) []slackapi.ManifestSlashCommand {
	seen := make(map[string]bool)
	out := make([]slackapi.ManifestSlashCommand, 0)
	appendCommand := func(cmd slackapi.ManifestSlashCommand) {
		cmd.Command = normalizeSlackCommandName(cmd.Command)
		if cmd.Command == "/" || seen[cmd.Command] {
			return
		}
		if !validSlackCommandName(cmd.Command) {
			return
		}
		seen[cmd.Command] = true
		cmd.Description = truncateSlackManifestText(strings.TrimSpace(cmd.Description), maxSlackCommandDescriptionLength)
		cmd.UsageHint = truncateSlackManifestText(strings.TrimSpace(cmd.UsageHint), maxSlackCommandUsageHintLength)
		cmd.Url = strings.TrimSpace(commandURL)
		out = append(out, cmd)
	}

	for _, cmd := range slackBuiltinCommands {
		appendCommand(cmd)
	}
	if registry != nil {
		for _, cmd := range registry.List() {
			if !shouldExposeSlackCommand(cmd) {
				continue
			}
			appendCommand(slackapi.ManifestSlashCommand{
				Command:     slackCommandAlias(cmd.Name),
				Description: cmd.Description,
			})
		}
	}
	return out
}

func mergeAstonishSlashCommands(manifest *slackapi.Manifest, commands []slackapi.ManifestSlashCommand, registry *channels.CommandRegistry) {
	if manifest == nil {
		return
	}
	owned := astonishOwnedSlackCommandNames(commands, registry)

	merged := make([]slackapi.ManifestSlashCommand, 0, len(manifest.Features.SlashCommands)+len(commands))
	for _, existing := range manifest.Features.SlashCommands {
		if owned[normalizeSlackCommandName(existing.Command)] {
			continue
		}
		merged = append(merged, existing)
	}
	manifest.Features.SlashCommands = append(merged, commands...)
}

func astonishOwnedSlackCommandNames(commands []slackapi.ManifestSlashCommand, registry *channels.CommandRegistry) map[string]bool {
	owned := make(map[string]bool, len(commands)+2)
	for _, cmd := range commands {
		owned[normalizeSlackCommandName(cmd.Command)] = true
	}
	owned["/link"] = true
	owned["/start"] = true
	owned[slackCommandAlias("start")] = true
	if registry != nil {
		for _, cmd := range registry.List() {
			if cmd == nil {
				continue
			}
			owned[slackCommandAlias(cmd.Name)] = true
			owned[legacySlackCommandAlias(cmd.Name)] = true
			owned[normalizeSlackCommandName(cmd.Name)] = true
		}
	}
	return owned
}

func shouldExposeSlackCommand(cmd *channels.Command) bool {
	return cmd != nil && !cmd.SessionScoped
}

func (s *SlackChannel) FormatCommandName(name string) string {
	return slackCommandAlias(name)
}

func (s *SlackChannel) ShouldExposeCommand(cmd *channels.Command) bool {
	return shouldExposeSlackCommand(cmd)
}

func validSlackCommandName(name string) bool {
	if len(name) < 2 || len(name) > 32 || !strings.HasPrefix(name, "/") {
		return false
	}
	for _, r := range name[1:] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func slackCommandAlias(name string) string {
	name = slackCommandAliasSuffix(name)
	if name == "" {
		return "/"
	}
	return "/" + slackCommandPrefix + "-" + name
}

func slackCommandAliasSuffix(name string) string {
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	var b strings.Builder
	lastWasDash := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastWasDash = false
			continue
		}
		if r == '-' || r == '_' {
			if b.Len() > 0 && !lastWasDash {
				b.WriteByte('-')
				lastWasDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func legacySlackCommandAlias(name string) string {
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "/")
	if name == "" {
		return "/"
	}
	return "/" + slackCommandPrefix + "-" + name
}

func normalizeSlackCommandName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "/")
	return "/" + name
}

func truncateSlackManifestText(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	suffix := "…"
	if maxLen <= len(suffix) {
		return value[:maxLen]
	}
	return strings.TrimSpace(value[:maxLen-len(suffix)]) + suffix
}

func validateSlackManifest(ctx context.Context, api manifestValidator, manifest *slackapi.Manifest, token, appID string) error {
	validation, err := api.ValidateManifestContext(ctx, manifest, token, appID)
	if validation != nil && len(validation.Errors) > 0 {
		if err != nil {
			return fmt.Errorf("validate manifest: %w: %s", err, formatManifestValidationErrors(validation.Errors))
		}
		return fmt.Errorf("validate manifest: %s", formatManifestValidationErrors(validation.Errors))
	}
	if err != nil {
		return fmt.Errorf("validate manifest: %w%s", err, invalidManifestHint(err, manifest))
	}
	return nil
}

func invalidManifestHint(err error, manifest *slackapi.Manifest) string {
	if err == nil || !strings.Contains(err.Error(), "invalid_manifest") {
		return ""
	}
	if manifest != nil {
		if !manifest.Settings.SocketModeEnabled {
			for _, cmd := range manifest.Features.SlashCommands {
				if strings.TrimSpace(cmd.Url) == "" {
					return "; Slack slash commands require either manifest settings.socket_mode_enabled=true or a public HTTPS command_url for every command"
				}
				if !strings.HasPrefix(strings.TrimSpace(cmd.Url), "https://") {
					return "; Slack slash command URLs must use public HTTPS URLs when Socket Mode is disabled"
				}
			}
		}
	}
	return "; check the exported Slack App Manifest for required fields. Slack may require Socket Mode settings, event request URLs, valid OAuth scope settings, and non-reserved slash command names. Astonish registers app-prefixed command names such as /astonish-status to avoid Slack's reserved generic command names"
}

func manifestAuthHint(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "invalid_auth") || strings.Contains(err.Error(), "not_allowed_token_type") {
		return "; use a Slack App Configuration Token from the Slack app settings, not an xapp Socket Mode App-Level Token or xoxb bot token"
	}
	return ""
}

func formatManifestValidationErrors(errors []slackapi.ManifestValidationError) string {
	parts := make([]string, 0, len(errors))
	for _, validationErr := range errors {
		if validationErr.Pointer != "" {
			parts = append(parts, validationErr.Pointer+": "+validationErr.Message)
		} else {
			parts = append(parts, validationErr.Message)
		}
	}
	return strings.Join(parts, "; ")
}
