package slack

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/channels"

	slackapi "github.com/slack-go/slack"
)

func TestBuildAstonishSlackCommandsIncludesBuiltinsAndRegistry(t *testing.T) {
	t.Parallel()
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	registry.Register(&channels.Command{Name: "new", Description: "Start fresh", SessionScoped: true})
	registry.Register(&channels.Command{Name: "fleet_stop", Description: "Stop fleet", SessionScoped: true})
	registry.Register(&channels.Command{Name: "authorize", Description: "Authorize device"})

	cmds := buildAstonishSlackCommands(registry, "https://example.com/api/slack/commands")
	byName := map[string]slackapi.ManifestSlashCommand{}
	for _, cmd := range cmds {
		byName[cmd.Command] = cmd
	}

	for _, name := range []string{"/link", "/astonish-status", "/astonish-authorize"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("expected command %s in %#v", name, cmds)
		}
		if byName[name].Url != "https://example.com/api/slack/commands" {
			t.Fatalf("command %s URL = %q", name, byName[name].Url)
		}
	}
	if byName["/link"].UsageHint != "CODE" {
		t.Fatalf("/link usage hint = %q, want CODE", byName["/link"].UsageHint)
	}
	if _, ok := byName["/astonish-new"]; ok {
		t.Fatalf("session-scoped /new should not be registered for Slack: %#v", byName["/astonish-new"])
	}
	if _, ok := byName["/astonish-start"]; ok {
		t.Fatalf("unsupported /start should not be registered for Slack: %#v", byName["/astonish-start"])
	}
	if _, ok := byName["/astonish-fleet-stop"]; ok {
		t.Fatalf("session-scoped /fleet_stop should not be registered for Slack: %#v", byName["/astonish-fleet-stop"])
	}
}

func TestBuildAstonishSlackCommandsOmitsURLsForSocketMode(t *testing.T) {
	t.Parallel()
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})

	cmds := buildAstonishSlackCommands(registry, "")
	for _, cmd := range cmds {
		if cmd.Url != "" {
			t.Fatalf("socket command %s URL = %q, want omitted", cmd.Command, cmd.Url)
		}
	}
}

func TestBuildAstonishSlackCommandsDeduplicatesBuiltins(t *testing.T) {
	t.Parallel()
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "link", Description: "duplicate link"})
	registry.Register(&channels.Command{Name: "/START", Description: "legacy session start", SessionScoped: true})
	registry.Register(&channels.Command{Name: "help", Description: strings.Repeat("x", maxSlackCommandDescriptionLength+20)})

	cmds := buildAstonishSlackCommands(registry, "")
	counts := map[string]int{}
	for _, cmd := range cmds {
		counts[cmd.Command]++
		if len(cmd.Description) > maxSlackCommandDescriptionLength {
			t.Fatalf("description for %s was not truncated", cmd.Command)
		}
	}
	if counts["/link"] != 1 {
		t.Fatalf("/link count = %d, want 1", counts["/link"])
	}
	if counts["/astonish-start"] != 0 {
		t.Fatalf("/astonish-start count = %d, want 0", counts["/astonish-start"])
	}
	if counts["/astonish-help"] != 1 {
		t.Fatalf("/astonish-help count = %d, want 1", counts["/astonish-help"])
	}
}

func TestSlackCommandAliasUsesAppPrefix(t *testing.T) {
	t.Parallel()
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	registry.Register(&channels.Command{Name: "fleet_plan", Description: "Plan fleet"})
	if got := slackCommandAlias("status"); got != "/astonish-status" {
		t.Fatalf("slackCommandAlias(status) = %q", got)
	}
	if got := slackCommandAlias("fleet_plan"); got != "/astonish-fleet-plan" {
		t.Fatalf("slackCommandAlias(fleet_plan) = %q", got)
	}
	if got := slackCommandNameFromInvocation("/astonish-status", registry); got != "status" {
		t.Fatalf("slackCommandNameFromInvocation(/astonish-status) = %q", got)
	}
	if got := slackCommandNameFromInvocation("/astonish-fleet-plan", registry); got != "fleet_plan" {
		t.Fatalf("slackCommandNameFromInvocation(/astonish-fleet-plan) = %q", got)
	}
}

func TestConfigureManifestTransportEnablesSocketMode(t *testing.T) {
	t.Parallel()
	manifest := &slackapi.Manifest{}
	configureManifestTransport(manifest, true)
	if !manifest.Settings.SocketModeEnabled {
		t.Fatal("socket mode was not enabled in manifest settings")
	}

	configureManifestTransport(manifest, false)
	if manifest.Settings.SocketModeEnabled {
		t.Fatal("socket mode was not disabled in manifest settings")
	}
}

func TestManifestCommandBuilderSkipsInvalidNames(t *testing.T) {
	t.Parallel()
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: strings.Repeat("a", 40), Description: "Too long"})

	cmds := buildAstonishSlackCommands(registry, "https://example.com/api/slack/commands")
	for _, cmd := range cmds {
		if cmd.Description == "Too long" {
			t.Fatalf("invalid long command was included: %#v", cmd)
		}
	}
}

func TestManifestSyncConfiguredAllowsSocketModeWithoutCommandURL(t *testing.T) {
	t.Parallel()
	ch := New(&Config{Mode: "socket", AppID: "A123", ConfigToken: "config-token"}, nil)
	if ok, reason := ch.manifestSyncConfigured(); !ok || reason != "" {
		t.Fatalf("socket mode without command URL was rejected: ok=%v reason=%q", ok, reason)
	}
}

func TestManifestSyncConfiguredRequiresHTTPSCommandURLForEventsMode(t *testing.T) {
	t.Parallel()
	ch := New(&Config{Mode: "events", AppID: "A123", ConfigToken: "config-token"}, nil)
	if ok, reason := ch.manifestSyncConfigured(); ok || !strings.Contains(reason, "command_url") {
		t.Fatalf("missing command URL accepted or wrong reason: ok=%v reason=%q", ok, reason)
	}

	ch = New(&Config{Mode: "events", AppID: "A123", ConfigToken: "config-token", CommandURL: "http://example.com/api/slack/commands"}, nil)
	if ok, reason := ch.manifestSyncConfigured(); ok || !strings.Contains(reason, "HTTPS") {
		t.Fatalf("non-HTTPS command URL accepted or wrong reason: ok=%v reason=%q", ok, reason)
	}
}

func TestManifestSyncConfiguredRejectsWrongTokenTypes(t *testing.T) {
	t.Parallel()
	ch := New(&Config{AppID: "A123", ConfigToken: "xapp-wrong"}, nil)
	if ok, reason := ch.manifestSyncConfigured(); ok || !strings.Contains(reason, "App-Level Token") {
		t.Fatalf("xapp token accepted or wrong reason: ok=%v reason=%q", ok, reason)
	}

	ch = New(&Config{AppID: "A123", ConfigToken: "xoxb-wrong"}, nil)
	if ok, reason := ch.manifestSyncConfigured(); ok || !strings.Contains(reason, "Bot User OAuth Token") {
		t.Fatalf("xoxb token accepted or wrong reason: ok=%v reason=%q", ok, reason)
	}
}

func TestManifestAuthHintExplainsInvalidAuth(t *testing.T) {
	t.Parallel()
	got := manifestAuthHint(assertStringError("invalid_auth"))
	if !strings.Contains(got, "App Configuration Token") || !strings.Contains(got, "xapp") {
		t.Fatalf("unexpected auth hint: %q", got)
	}
}

func TestInvalidManifestHintExplainsMissingCommandURLWhenSocketModeDisabled(t *testing.T) {
	t.Parallel()
	manifest := &slackapi.Manifest{}
	manifest.Features.SlashCommands = []slackapi.ManifestSlashCommand{{Command: "/status", Description: "Status"}}
	got := invalidManifestHint(assertStringError("invalid_manifest"), manifest)
	if !strings.Contains(got, "Socket Mode") && !strings.Contains(got, "command_url") {
		t.Fatalf("unexpected invalid manifest hint: %q", got)
	}
}

func TestInvalidManifestHintDoesNotRequireCommandURLForSocketMode(t *testing.T) {
	t.Parallel()
	manifest := &slackapi.Manifest{}
	manifest.Settings.SocketModeEnabled = true
	manifest.Features.SlashCommands = []slackapi.ManifestSlashCommand{{Command: "/astonish-status", Description: "Status"}}
	got := invalidManifestHint(assertStringError("invalid_manifest"), manifest)
	if strings.Contains(got, "command_url") {
		t.Fatalf("socket mode hint should not require command_url: %q", got)
	}
}

type assertStringError string

func (e assertStringError) Error() string { return string(e) }

func TestMergeAstonishSlashCommandsPreservesUnownedCommands(t *testing.T) {
	t.Parallel()
	manifest := &slackapi.Manifest{}
	manifest.Display.Name = "Existing"
	manifest.Features.SlashCommands = []slackapi.ManifestSlashCommand{
		{Command: "/external", Description: "External command", Url: "https://external.example.com"},
		{Command: "/status", Description: "Old status", Url: "https://old.example.com"},
	}

	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	mergeAstonishSlashCommands(manifest, []slackapi.ManifestSlashCommand{
		{Command: "/astonish-status", Description: "New status", Url: "https://astonish.example.com"},
		{Command: "/link", Description: "Link account", Url: "https://astonish.example.com"},
	}, registry)

	if manifest.Display.Name != "Existing" {
		t.Fatalf("display name changed to %q", manifest.Display.Name)
	}
	byName := map[string]slackapi.ManifestSlashCommand{}
	for _, cmd := range manifest.Features.SlashCommands {
		byName[cmd.Command] = cmd
	}
	if byName["/external"].Url != "https://external.example.com" {
		t.Fatalf("external command was not preserved: %#v", byName["/external"])
	}
	if _, ok := byName["/status"]; ok {
		t.Fatalf("legacy bare status command was not removed: %#v", byName["/status"])
	}
	if byName["/astonish-status"].Description != "New status" {
		t.Fatalf("status command was not replaced: %#v", byName["/astonish-status"])
	}
	if _, ok := byName["/link"]; !ok {
		t.Fatal("link command was not added")
	}
	if len(manifest.Features.SlashCommands) != 3 {
		t.Fatalf("slash command count = %d, want 3", len(manifest.Features.SlashCommands))
	}
}

func TestMergeAstonishSlashCommandsRemovesLegacyBareBuiltins(t *testing.T) {
	t.Parallel()
	manifest := &slackapi.Manifest{}
	manifest.Features.SlashCommands = []slackapi.ManifestSlashCommand{
		{Command: "/start", Description: "Old start", Url: "https://old.example.com"},
		{Command: "/external", Description: "External command", Url: "https://external.example.com"},
	}

	mergeAstonishSlashCommands(manifest, []slackapi.ManifestSlashCommand{
		{Command: "/astonish-start", Description: "New start", Url: "https://astonish.example.com"},
	}, nil)

	byName := map[string]slackapi.ManifestSlashCommand{}
	for _, cmd := range manifest.Features.SlashCommands {
		byName[cmd.Command] = cmd
	}
	if _, ok := byName["/start"]; ok {
		t.Fatalf("legacy bare start command was not removed: %#v", byName["/start"])
	}
	if byName["/astonish-start"].Description != "New start" {
		t.Fatalf("prefixed start command was not added: %#v", byName["/astonish-start"])
	}
	if byName["/external"].Url != "https://external.example.com" {
		t.Fatalf("external command was not preserved: %#v", byName["/external"])
	}
}
