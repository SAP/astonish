package tui

import (
	"strings"
)

// slashCommand is one completable TUI command.
type slashCommand struct {
	Name        string // without leading slash, e.g. "help"
	Aliases     []string
	Description string
}

// builtInSlashCommands is the local command palette for the terminal app.
// Keep in sync with handleSlash.
var builtInSlashCommands = []slashCommand{
	{Name: "help", Aliases: []string{"?"}, Description: "Show available commands and keys"},
	{Name: "status", Description: "Show session, server, provider, and model"},
	{Name: "sessions", Aliases: []string{"session"}, Description: "Browse and resume sessions"},
	{Name: "model", Aliases: []string{"models"}, Description: "Choose provider and model"},
	{Name: "new", Description: "Start a fresh conversation"},
	{Name: "files", Description: "Show @file context help"},
	{Name: "plan", Description: "Toggle plan-only responses"},
	{Name: "exit", Aliases: []string{"quit", "q"}, Description: "Quit the terminal app"},
}

var skillsSlashCommand = slashCommand{
	Name: "skills", Description: "List local runtime skills",
}


// rollbackSlashCommand is offered only when the backend supports rollback
// (code mode). It reverts chat and file changes to an earlier message.
var rollbackSlashCommand = slashCommand{
	Name: "rollback", Aliases: []string{"revert"}, Description: "Revert chat and file changes to an earlier message",
}

// compactSlashCommand is offered only when the backend supports on-demand
// compaction (code mode). It compacts the conversation context to free window.
var compactSlashCommand = slashCommand{
	Name: "compact", Description: "Compact the conversation context to free up the window",
}

// webSearchSlashCommand is offered only when the backend supports local web
// search configuration (code mode). It opens the web search provider picker.
var webSearchSlashCommand = slashCommand{
	Name: "websearch", Aliases: []string{"search"}, Description: "Configure web search provider",
}

// slashCompletion holds the active / completion popup state.
type slashCompletion struct {
	active  bool
	query   string // text after leading /, may be empty
	matches []slashCommand
	cursor  int
}

// filterSlashCommands returns commands whose name or alias has prefix query
// (case-insensitive, without leading slash). Extra commands (capability-gated,
// e.g. /provider in code mode) are appended to the always-on palette.
func filterSlashCommands(query string, extra ...slashCommand) []slashCommand {
	q := strings.ToLower(strings.TrimSpace(query))
	// Strip leading slash if present.
	q = strings.TrimPrefix(q, "/")
	// Only complete the first token (before space).
	if i := strings.IndexAny(q, " \t"); i >= 0 {
		q = q[:i]
	}

	var out []slashCommand
	all := append(append([]slashCommand(nil), builtInSlashCommands...), extra...)
	for _, cmd := range all {
		if slashMatches(cmd, q) {
			out = append(out, cmd)
		}
	}
	return out
}

func slashMatches(cmd slashCommand, q string) bool {
	if q == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(cmd.Name), q) {
		return true
	}
	for _, a := range cmd.Aliases {
		if strings.HasPrefix(strings.ToLower(a), q) {
			return true
		}
	}
	return false
}

// parseSlashInput returns (isSlash, query) for the current composer value.
// Completion is only active when the input is a single-line command starting with /.
func parseSlashInput(value string) (bool, string) {
	if !strings.HasPrefix(value, "/") {
		return false, ""
	}
	// Multi-line pastes are not command completion.
	if strings.Contains(value, "\n") {
		return false, ""
	}
	// query is everything after /
	return true, value[1:]
}

// selectedCommand returns the currently highlighted match, if any.
func (s slashCompletion) selectedCommand() (slashCommand, bool) {
	if !s.active || len(s.matches) == 0 {
		return slashCommand{}, false
	}
	if s.cursor < 0 || s.cursor >= len(s.matches) {
		return slashCommand{}, false
	}
	return s.matches[s.cursor], true
}

// completionValue returns the string to put in the composer for cmd (with /).
func completionValue(cmd slashCommand) string {
	return "/" + cmd.Name
}
