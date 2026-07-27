package astonish

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/launcher"
)

// chatFlags holds the parsed CLI flag values for `astonish chat`.
type chatFlags struct {
	Provider    string
	Model       string
	AutoApprove bool
	Debug       bool
	Resume      string
	ClearModel  bool
}

// parseChatFlags parses the argv slice into a chatFlags struct.
func parseChatFlags(args []string) (chatFlags, error) {
	chatCmd := flag.NewFlagSet("chat", flag.ContinueOnError)

	providerName := chatCmd.String("provider", "", "AI provider to pin on the session (with --resume or after first turn via chat model)")
	modelName := chatCmd.String("model", "", "Model name to pin on the session")
	autoApprove := chatCmd.Bool("auto-approve", false, "Auto-approve all tool executions")
	debugMode := chatCmd.Bool("debug", false, "Enable debug mode")
	resumeSession := chatCmd.String("resume", "", "Resume an existing session by ID")
	clearModel := chatCmd.Bool("clear-model", false, "Clear the model pin on the resumed session (requires --resume)")

	chatCmd.StringVar(providerName, "p", "", "AI provider (short)")
	chatCmd.StringVar(modelName, "m", "", "Model name (short)")
	chatCmd.StringVar(resumeSession, "r", "", "Resume session (short)")

	if err := chatCmd.Parse(args); err != nil {
		return chatFlags{}, err
	}

	return chatFlags{
		Provider:    *providerName,
		Model:       *modelName,
		AutoApprove: *autoApprove,
		Debug:       *debugMode,
		Resume:      *resumeSession,
		ClearModel:  *clearModel,
	}, nil
}

func handleChatCommand(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		printChatUsage()
		return nil
	}

	// Sub-command: `astonish chat model <provider>:<model>`
	if len(args) > 0 && args[0] == "model" {
		return handleChatModelCommand(args[1:])
	}

	// Chat always uses the authenticated platform client (Studio SSE).
	// Local/in-process chat has been removed.
	if !client.IsRemoteMode() {
		return fmt.Errorf("not logged in to a platform\nRun: astonish login <url>\n\nChat requires an authenticated Astonish platform (local install or cloud)")
	}

	flags, err := parseChatFlags(args)
	if err != nil {
		return err
	}
	if flags.ClearModel && flags.Resume == "" {
		return fmt.Errorf("--clear-model requires --resume")
	}

	// Optional pin mutations before opening the TUI (need an existing session).
	if flags.Resume != "" && (flags.ClearModel || flags.Provider != "" || flags.Model != "") {
		c, err := client.New()
		if err != nil {
			return err
		}
		provider, model := flags.Provider, flags.Model
		if flags.ClearModel {
			provider, model = "", ""
		}
		if _, err := c.PatchSessionModel(flags.Resume, provider, model); err != nil {
			return fmt.Errorf("failed to update session model pin: %w", err)
		}
	}

	cfg := &launcher.ChatConfig{
		AutoApprove: flags.AutoApprove,
		SessionID:   flags.Resume,
		DebugMode:   flags.Debug,
	}
	return launcher.RunChatTUI(context.Background(), cfg)
}

// parseModelPin splits a `provider:model` argument on the FIRST colon so
// model names that contain colons (e.g. `openai:gpt-4o:2024-08-06`) survive
// intact. An empty input clears the pin (both provider and model empty).
func parseModelPin(arg string) (provider, model string, err error) {
	if arg == "" {
		return "", "", nil
	}
	idx := strings.IndexByte(arg, ':')
	if idx < 0 {
		return "", "", fmt.Errorf("expected provider:model, got %q", arg)
	}
	provider = arg[:idx]
	model = arg[idx+1:]
	if provider == "" || model == "" {
		return "", "", fmt.Errorf("expected provider:model with both provider and model set, or an empty string to clear")
	}
	return provider, model, nil
}

// resolveLastRemoteSessionID returns the most-recently-updated session ID
// from the platform server.
func resolveLastRemoteSessionID(c *client.Client) (string, error) {
	sessions, err := c.ListSessions()
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions found; start one with 'astonish chat'")
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt > sessions[j].UpdatedAt
	})
	return sessions[0].ID, nil
}

// handleChatModelCommand implements `astonish chat model <provider>:<model>`.
// Always uses the platform API (requires login).
func handleChatModelCommand(args []string) error {
	if !client.IsRemoteMode() {
		return fmt.Errorf("not logged in to a platform\nRun: astonish login <url>")
	}

	fs := flag.NewFlagSet("chat model", flag.ContinueOnError)
	session := fs.String("session", "", "Target session ID (default: most recent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: astonish chat model [--session <id>] <provider>:<model>")
	}
	provider, model, err := parseModelPin(rest[0])
	if err != nil {
		return err
	}

	c, err := client.New()
	if err != nil {
		return err
	}
	sessionID := *session
	if sessionID == "" {
		sessionID, err = resolveLastRemoteSessionID(c)
		if err != nil {
			return err
		}
	}
	resp, err := c.PatchSessionModel(sessionID, provider, model)
	if err != nil {
		return fmt.Errorf("failed to patch session model: %w", err)
	}
	printModelResult(sessionID, resp.PinnedProvider, resp.PinnedModel,
		resp.EffectiveProvider, resp.EffectiveModel, resp.CredentialsAvailable)
	return nil
}

func printModelResult(sessionID, pinnedProvider, pinnedModel, effectiveProvider, effectiveModel string, credentialsAvailable bool) {
	fmt.Printf("Session: %s\n", sessionID)
	if pinnedProvider == "" && pinnedModel == "" {
		fmt.Println("Pin cleared; using cascade default.")
	} else {
		fmt.Printf("Pinned:    %s / %s\n", pinnedProvider, pinnedModel)
	}
	fmt.Printf("Effective: %s / %s\n", effectiveProvider, effectiveModel)
	if !credentialsAvailable {
		fmt.Println("Warning: no credential configured for the pinned provider (pin persisted, hot-swap skipped)")
	}
}

func printChatUsage() {
	fmt.Println("usage: astonish chat [options]")
	fmt.Println("")
	fmt.Println("Start an interactive chat session against your Astonish platform.")
	fmt.Println("Requires authentication: astonish login <url>")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -p, --provider      Pin provider on --resume session (or use: chat model)")
	fmt.Println("  -m, --model         Pin model on --resume session")
	fmt.Println("  -r, --resume        Resume an existing session by ID")
	fmt.Println("  --clear-model       Clear the model pin (requires --resume)")
	fmt.Println("  --auto-approve      Auto-approve all tool executions")
	fmt.Println("  --debug             Enable debug output")
	fmt.Println("  -h, --help          Show this help message")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  astonish login https://astonish.example.com")
	fmt.Println("  astonish chat")
	fmt.Println("  astonish chat --auto-approve")
	fmt.Println("  astonish chat --resume <session-id>")
	fmt.Println("  astonish chat --resume <session-id> -p openai -m gpt-4o")
	fmt.Println("  astonish chat --resume <session-id> --clear-model")
	fmt.Println("  astonish chat model openai:gpt-4o")
	fmt.Println("  astonish chat model \"\"                     # clear pin on latest session")
}
