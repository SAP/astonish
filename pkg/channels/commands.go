// Package channels — command registry for cross-channel slash commands.
//
// Commands registered here work identically in the console TUI and in
// any channel adapter (Telegram, Slack, etc.). Each command receives a
// CommandContext with enough information to inspect or mutate the current
// session, and returns a plain-text response string.
package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"

	"google.golang.org/adk/session"
)

// DistillSession identifies a session for distillation.
type DistillSession = agent.DistillSession

// Distiller is the interface for on-demand flow distillation.
// ChatAgent satisfies this interface.
type Distiller interface {
	PreviewDistill(ctx context.Context, ds agent.DistillSession) (string, error)
	ConfirmAndDistill(ctx context.Context, ds agent.DistillSession, print func(string)) error
}

// CommandContext provides everything a command handler needs to inspect
// or mutate the current conversation state.
type CommandContext struct {
	// ChannelID is the channel adapter that received the command
	// (e.g. "telegram", "console"). Empty for console.
	ChannelID string
	// ChatID is the chat/conversation where the command was issued.
	ChatID string
	// SenderID is the normalized sender identifier.
	SenderID string
	// SenderName is the human-readable sender name.
	SenderName string
	// SessionKey is the persistent session key for this conversation.
	SessionKey string
	// UserID is the ADK user ID for session operations.
	UserID string
	// AppName is the ADK app name (always "astonish").
	AppName string

	// SessionService is the session store for session operations.
	SessionService session.Service
	// ProviderName is the active LLM provider name.
	ProviderName string
	// ModelName is the active LLM model name.
	ModelName string
	// ToolCount is the number of available tools.
	ToolCount int

	// Distiller provides on-demand flow distillation (nil if unavailable).
	Distiller Distiller

	// AuthorizeFunc validates a device authorization code for Studio access.
	// Returns a human-readable message and whether the code was accepted.
	// Nil if device auth is not available (e.g. no daemon, auth disabled).
	AuthorizeFunc func(code string) (string, bool)

	// RawText is the full command text as typed by the user (e.g. "/authorize A7X-K2M").
	RawText string

	// Manager is the ChannelManager that processed this command.
	// Used by fleet commands to manage fleet sessions and route messages.
	Manager *ChannelManager

	// Presenter optionally adapts command display for a specific channel.
	Presenter CommandPresenter
}

// CommandFunc is the handler signature for a slash command.
// It returns the text response to send back to the user.
type CommandFunc func(ctx context.Context, cc CommandContext) (string, error)

// Command describes a single slash command.
type Command struct {
	// Name is the slash command name without the leading slash (e.g. "status").
	Name string
	// Description is a short human-readable description for /help.
	Description string
	// SessionScoped marks commands whose effect depends on the current conversation session.
	// Threaded channels should only expose these when the invocation carries thread context.
	SessionScoped bool
	// Handler is the function that executes the command.
	Handler CommandFunc
}

// CommandRegistry holds all registered slash commands.
type CommandRegistry struct {
	mu       sync.RWMutex
	commands map[string]*Command
	order    []string // insertion order for /help
}

// NewCommandRegistry creates a new empty command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]*Command),
	}
}

// Register adds a command to the registry. Panics if the name is already taken.
func (r *CommandRegistry) Register(cmd *Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.commands[cmd.Name]; exists {
		panic(fmt.Sprintf("command /%s already registered", cmd.Name))
	}
	r.commands[cmd.Name] = cmd
	r.order = append(r.order, cmd.Name)
}

// Lookup returns the command for the given name (without leading slash).
// Returns nil if not found.
func (r *CommandRegistry) Lookup(name string) *Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commands[name]
}

// IsCommand returns true if the text starts with "/" and matches a registered command.
func (r *CommandRegistry) IsCommand(text string) bool {
	name := parseCommandName(text)
	if name == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.commands[name]
	return exists
}

// Execute parses the command name from text and runs the matching handler.
// Returns the response text and any error.
func (r *CommandRegistry) Execute(ctx context.Context, text string, cc CommandContext) (string, error) {
	name := parseCommandName(text)
	if name == "" {
		return "", fmt.Errorf("not a command: %q", text)
	}

	cmd := r.Lookup(name)
	if cmd == nil {
		return fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", name), nil
	}

	cc.RawText = text
	return cmd.Handler(ctx, cc)
}

// List returns all registered commands in registration order.
func (r *CommandRegistry) List() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Command, 0, len(r.order))
	for _, name := range r.order {
		result = append(result, r.commands[name])
	}
	return result
}

// parseCommandName extracts the command name from "/name" or "/name args".
// Returns empty string if text doesn't start with "/".
func parseCommandName(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	// Remove leading slash and take the first word
	rest := text[1:]
	if idx := strings.IndexByte(rest, ' '); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.ToLower(rest)
}

// DefaultCommands creates a CommandRegistry pre-loaded with the standard
// cross-channel commands: /status, /new, /distill, /jobs, /org, /team, /context, /help.
func DefaultCommands() *CommandRegistry {
	r := NewCommandRegistry()
	r.Register(statusCommand())
	r.Register(newSessionCommand())
	r.Register(distillCommand())
	r.Register(jobsCommand())
	r.Register(authorizeCommand())
	RegisterRoutingCommands(r)
	r.Register(helpCommand(r))
	return r
}

// --- Built-in command implementations ---

func statusCommand() *Command {
	return &Command{
		Name:        "status",
		Description: "Show provider, model, and session info",
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			var b strings.Builder
			b.WriteString("Status\n")
			b.WriteString(fmt.Sprintf("  Provider: %s\n", cc.ProviderName))
			b.WriteString(fmt.Sprintf("  Model:    %s\n", cc.ModelName))
			if cc.ToolCount > 0 {
				b.WriteString(fmt.Sprintf("  Tools:    %d\n", cc.ToolCount))
			}
			if cc.SessionKey != "" {
				b.WriteString(fmt.Sprintf("  Session:  %s\n", cc.SessionKey))
			}
			if cc.ChannelID != "" {
				b.WriteString(fmt.Sprintf("  Channel:  %s\n", cc.ChannelID))
			}
			return b.String(), nil
		},
	}
}

func newSessionCommand() *Command {
	return &Command{
		Name:          "new",
		Description:   "Start a fresh conversation (new session)",
		SessionScoped: true,
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			if cc.SessionService == nil {
				return welcomeMessage(cc.SenderName), nil
			}

			// Delete the existing session so the next message creates a fresh one.
			if err := cc.SessionService.Delete(ctx, &session.DeleteRequest{
				AppName:   cc.AppName,
				UserID:    cc.UserID,
				SessionID: cc.SessionKey,
			}); err != nil {
				slog.Warn("failed to delete session during reset", "session", cc.SessionKey, "error", err)
			}

			return welcomeMessage(cc.SenderName), nil
		},
	}
}

// welcomeMessage returns a friendly greeting for new/reset sessions.
func welcomeMessage(name string) string {
	if name == "" {
		name = "there"
	}
	return fmt.Sprintf("Hey %s! Fresh start. What can I help you with?", name)
}

func distillCommand() *Command {
	return &Command{
		Name:          "distill",
		Description:   "Distill the last task into a reusable flow",
		SessionScoped: true,
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			if cc.Distiller == nil {
				return "Flow distillation is not available.", nil
			}

			ds := DistillSession{
				SessionID: cc.SessionKey,
				AppName:   cc.AppName,
				UserID:    cc.UserID,
			}

			// One-shot: preview + confirm without user interaction.
			// Channel users explicitly requested /distill, so proceed directly.
			description, err := cc.Distiller.PreviewDistill(ctx, ds)
			if err != nil {
				return fmt.Sprintf("Nothing to distill: %v", err), nil
			}

			var result strings.Builder
			result.WriteString(fmt.Sprintf("Task identified: %s\n\n", description))

			distillErr := cc.Distiller.ConfirmAndDistill(ctx, ds, func(s string) {
				result.WriteString(s)
			})
			if distillErr != nil {
				result.WriteString(fmt.Sprintf("\nDistillation failed: %v", distillErr))
			}

			return result.String(), nil
		},
	}
}

func jobsCommand() *Command {
	return &Command{
		Name:        "jobs",
		Description: "Show scheduled jobs",
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			// Determine daemon API URL from config
			daemonURL := "http://localhost:9393"
			if appCfg, err := config.LoadAppConfig(); err == nil {
				daemonURL = fmt.Sprintf("http://localhost:%d", appCfg.Daemon.GetPort())
			}

			// Fetch jobs from daemon API
			resp, err := http.Get(daemonURL + "/api/scheduler/jobs")
			if err != nil {
				return "Scheduler is not available (daemon not running?).", nil
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Sprintf("Scheduler returned an error (HTTP %d).", resp.StatusCode), nil
			}

			var result struct {
				Jobs []struct {
					Name     string `json:"name"`
					Mode     string `json:"mode"`
					Schedule struct {
						Cron     string `json:"cron"`
						Timezone string `json:"timezone"`
					} `json:"schedule"`
					Enabled    bool    `json:"enabled"`
					LastStatus string  `json:"last_status"`
					NextRun    *string `json:"next_run"`
				} `json:"jobs"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return "Failed to read scheduler status.", nil
			}

			if len(result.Jobs) == 0 {
				return "No scheduled jobs.\n\nTo create one, just tell me: \"Schedule X to run every day at 9am\"", nil
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("Scheduled Jobs (%d)\n\n", len(result.Jobs)))
			for _, j := range result.Jobs {
				status := "off"
				if j.Enabled {
					status = j.LastStatus
					if status == "" || status == "pending" {
						status = "waiting"
					}
				}
				nextRun := ""
				if j.NextRun != nil && j.Enabled {
					t, err := time.Parse(time.RFC3339, *j.NextRun)
					if err == nil {
						nextRun = fmt.Sprintf(" (next: %s)", t.Format("Jan 2 15:04"))
					}
				}
				b.WriteString(fmt.Sprintf("  %s [%s] %s — %s%s\n",
					j.Name, j.Mode, j.Schedule.Cron, status, nextRun))
			}
			return b.String(), nil
		},
	}
}

func authorizeCommand() *Command {
	return &Command{
		Name:        "authorize",
		Description: "Authorize a device to access Astonish Studio",
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			if cc.AuthorizeFunc == nil {
				return "Device authorization is not available. Studio auth is only active in daemon mode.", nil
			}

			// Extract code from the command arguments: /authorize CODE
			code := strings.TrimSpace(strings.TrimPrefix(cc.RawText, "/authorize"))
			if code == "" {
				return "Usage: /authorize CODE\n\nEnter the 6-character code shown on the Studio authorization page.", nil
			}

			msg, _ := cc.AuthorizeFunc(code)
			return msg, nil
		},
	}
}

// parseCommandArgs is a helper — it's not available here because the
// command handler only gets CommandContext. We need the raw text.
// Instead, we'll adjust the approach: pass the raw text in CommandContext.

func helpCommand(registry *CommandRegistry) *Command {
	return &Command{
		Name:        "help",
		Description: "Show available commands",
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			var b strings.Builder
			b.WriteString("Available commands:\n")
			for _, cmd := range registry.List() {
				if cc.Presenter != nil && !cc.Presenter.ShouldExposeCommand(cmd) {
					continue
				}
				name := "/" + cmd.Name
				if cc.Presenter != nil {
					name = cc.Presenter.FormatCommandName(cmd.Name)
				}
				b.WriteString(fmt.Sprintf("  %s — %s\n", name, cmd.Description))
			}
			return b.String(), nil
		},
	}
}
