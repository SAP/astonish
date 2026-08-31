package tui

import (
	"strings"
	"testing"
)

func TestFilterSlashCommands_EmptyShowsAll(t *testing.T) {
	got := filterSlashCommands("")
	if len(got) != len(builtInSlashCommands) {
		t.Fatalf("got %d want %d", len(got), len(builtInSlashCommands))
	}
}

func TestFilterSlashCommands_Prefix(t *testing.T) {
	got := filterSlashCommands("se")
	// sessions, (maybe nothing else)
	if len(got) < 1 {
		t.Fatal("expected sessions")
	}
	found := false
	for _, c := range got {
		if c.Name == "sessions" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sessions in %v", got)
	}
}

func TestFilterSlashCommands_IncludesPlan(t *testing.T) {
	got := filterSlashCommands("pl")
	if len(got) != 1 || got[0].Name != "plan" {
		t.Fatalf("expected plan command, got %#v", got)
	}
}

func TestFilterSlashCommands_Alias(t *testing.T) {
	got := filterSlashCommands("q")
	// exit has alias q; also nothing that starts with q alone except quit path
	found := false
	for _, c := range got {
		if c.Name == "exit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected exit for q: %v", got)
	}
}

func TestParseSlashInput(t *testing.T) {
	ok, q := parseSlashInput("/hel")
	if !ok || q != "hel" {
		t.Fatalf("%v %q", ok, q)
	}
	ok, _ = parseSlashInput("hello")
	if ok {
		t.Fatal("not a slash")
	}
	ok, _ = parseSlashInput("/help\nmore")
	if ok {
		t.Fatal("multiline not slash complete")
	}
}

// TestHelpText_DocumentsEscCancel guards the reported regression: /help must
// tell the user that Esc cancels the current turn (Claude Code / OpenCode
// style), not only ctrl+c. See cancelInFlightTurn.
func TestHelpText_DocumentsEscCancel(t *testing.T) {
	h := helpText(false, false, false)
	if !strings.Contains(h, "esc") {
		t.Fatalf("help text missing esc key:\n%s", h)
	}
	// The Keys section must document esc as a turn-cancellation key, not only
	// the completion-popup "Close completion" usage.
	if !strings.Contains(h, "esc            Cancel the current turn") {
		t.Fatalf("help text does not document esc as turn cancel:\n%s", h)
	}
}

// TestHelpText_ListsAllBuiltInCommands keeps /help in sync with the command
// palette so no supported command is silently missing from help.
func TestHelpText_ListsAllBuiltInCommands(t *testing.T) {
	h := helpText(false, false, false)
	for _, cmd := range builtInSlashCommands {
		if !strings.Contains(h, "/"+cmd.Name) {
			t.Errorf("help text missing command /%s:\n%s", cmd.Name, h)
		}
	}
}

// TestHelpText_CapabilityGatedCommands ensures /websearch, /rollback, and /compact appear
// only when their backend capability is available. /provider is no longer a
// separate command (provider management is inline in /model).
func TestHelpText_CapabilityGatedCommands(t *testing.T) {
	off := helpText(false, false, false)
	if strings.Contains(off, "/rollback") || strings.Contains(off, "/websearch") || strings.Contains(off, "/skills") {
		t.Fatalf("gated commands should be hidden when unavailable:\n%s", off)
	}
	on := helpText(true, true, true, true)
	if !strings.Contains(on, "/websearch") {
		t.Errorf("help text missing /websearch when web search admin available:\n%s", on)
	}
	if !strings.Contains(on, "/rollback") {
		t.Errorf("help text missing /rollback when rollback available:\n%s", on)
	}
	// /compact is gated on the compaction capability (3rd arg).
	if strings.Contains(off, "/compact") {
		t.Fatalf("/compact should be hidden when compaction unavailable:\n%s", off)
	}
	if !strings.Contains(on, "/compact") {
		t.Errorf("help text missing /compact when compaction available:\n%s", on)
	}
	if !strings.Contains(on, "/skills") {
		t.Errorf("help text missing /skills when local skills available:\n%s", on)
	}
}
