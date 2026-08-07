package astonish

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	persistentsession "github.com/SAP/astonish/pkg/session"
)

// sessionSidecarSuffixes are the per-session sidecar file suffixes removed
// alongside a session's transcript when it is deleted. The ".PLAN.md" sidecar
// is written in code mode (see localAgentBackend.planFilePath); keep this list
// in sync with any new per-session files created next to "<id>.jsonl".
var sessionSidecarSuffixes = []string{".jsonl", ".PLAN.md"}

// removeSessionFiles deletes a session's transcript and all per-session sidecar
// files (e.g. the code-mode PLAN.md). Best-effort: missing files are ignored.
func removeSessionFiles(sessDir, appName, userID, id string) {
	for _, suffix := range sessionSidecarSuffixes {
		os.Remove(fmt.Sprintf("%s/%s/%s/%s%s", sessDir, appName, userID, id, suffix))
	}
}

func handleSessionsCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printSessionsUsage()
		return nil
	}

	// Remote mode: delegate to API
	if client.IsRemoteMode() {
		return handleSessionsRemote(args)
	}

	subcommand := args[0]
	switch subcommand {
	case "list", "ls":
		return handleSessionsList()
	case "show":
		if len(args) < 2 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: astonish sessions show <session-id> [flags]")
			return fmt.Errorf("session ID required")
		}
		return handleSessionsShow(args[1], args[2:])
	case "delete", "rm":
		if len(args) < 2 {
			fmt.Println("Error: session ID required")
			fmt.Println("Usage: astonish sessions delete <session-id>")
			return fmt.Errorf("session ID required")
		}
		return handleSessionsDelete(args[1])
	case "clear":
		return handleSessionsClear()
	default:
		fmt.Printf("Unknown sessions subcommand: %s\n", subcommand)
		printSessionsUsage()
		return fmt.Errorf("unknown subcommand: %s", subcommand)
	}
}

func handleSessionsList() error {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if appCfg.Sessions.Storage == "memory" {
		fmt.Println("Session persistence is disabled (storage: memory).")
		fmt.Println("Remove 'sessions: { storage: memory }' from your config to enable it.")
		return nil
	}

	sessDir, err := config.GetSessionsDir(&appCfg.Sessions)
	if err != nil {
		return fmt.Errorf("failed to resolve sessions dir: %w", err)
	}

	index := persistentsession.NewSessionIndex(sessDir + "/index.json")
	data, err := index.Load()
	if err != nil {
		return fmt.Errorf("failed to load session index: %w", err)
	}

	if len(data.Sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	// Sort by UpdatedAt descending, excluding sub-sessions
	metas := make([]persistentsession.SessionMeta, 0, len(data.Sessions))
	for _, m := range data.Sessions {
		if m.ParentID != "" {
			continue // skip sub-agent sessions
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})

	if len(metas) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tMESSAGES\tUPDATED\tAGE")

	// Compute shortest unique prefix for each session ID
	allIDs := make([]string, len(metas))
	for i, m := range metas {
		allIDs[i] = m.ID
	}
	shortIDs := persistentsession.ShortIDs(allIDs)

	for _, m := range metas {
		id := shortIDs[m.ID]
		title := m.Title
		if title == "" {
			title = "-"
		}
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		age := formatAge(m.UpdatedAt)
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
			id, title, m.MessageCount,
			m.UpdatedAt.Format("2006-01-02 15:04"), age)
	}
	w.Flush()

	fmt.Printf("\n%d session(s) total\n", len(metas))
	return nil
}

func handleSessionsShow(sessionID string, flags []string) error {
	// Parse flags
	opts := TraceOpts{}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--json":
			opts.JSONOutput = true
		case "--tools-only", "-t":
			opts.ToolsOnly = true
		case "--verbose", "-v":
			opts.Verbose = true
		case "--recursive", "-r":
			opts.Recursive = true
		case "--last", "-n":
			if i+1 < len(flags) {
				i++
				n, err := strconv.Atoi(flags[i])
				if err != nil {
					return fmt.Errorf("invalid value for --last: %s", flags[i])
				}
				opts.LastN = n
			} else {
				return fmt.Errorf("--last requires a number")
			}
		default:
			// Check for -n<number> form (e.g. -n20)
			if strings.HasPrefix(flags[i], "-n") && len(flags[i]) > 2 {
				n, err := strconv.Atoi(flags[i][2:])
				if err == nil {
					opts.LastN = n
					continue
				}
			}
			return fmt.Errorf("unknown flag: %s", flags[i])
		}
	}

	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if appCfg.Sessions.Storage == "memory" {
		fmt.Println("Session persistence is disabled (storage: memory).")
		return nil
	}

	sessDir, err := config.GetSessionsDir(&appCfg.Sessions)
	if err != nil {
		return fmt.Errorf("failed to resolve sessions dir: %w", err)
	}

	// Try to find session by prefix match
	index := persistentsession.NewSessionIndex(sessDir + "/index.json")
	fullID, err := resolveSessionID(index, sessionID)
	if err != nil {
		return err
	}

	meta, err := index.Get(fullID)
	if err != nil {
		return fmt.Errorf("session not found: %w", err)
	}

	// Load the transcript
	transcriptPath := fmt.Sprintf("%s/%s/%s/%s.jsonl", sessDir, meta.AppName, meta.UserID, meta.ID)
	transcript := persistentsession.NewTranscript(transcriptPath)

	if !transcript.Exists() {
		fmt.Printf("Session: %s\n", meta.ID)
		fmt.Printf("App:     %s\n", meta.AppName)
		fmt.Printf("User:    %s\n", meta.UserID)
		fmt.Printf("Created: %s\n", meta.CreatedAt.Format(time.RFC3339))
		fmt.Printf("Updated: %s\n", meta.UpdatedAt.Format(time.RFC3339))
		fmt.Println("\nNo transcript file found.")
		return nil
	}

	events, err := transcript.ReadEvents()
	if err != nil {
		return fmt.Errorf("failed to read transcript: %w", err)
	}

	if opts.JSONOutput {
		entries, toolCalls, toolErrors := collectTraceEntries(events, "", opts)

		// Include sub-session traces when --recursive is set
		if opts.Recursive {
			childEntries, childTC, childTE := collectChildSessionEntries(sessDir, fullID, index, opts)
			entries = append(entries, childEntries...)
			toolCalls += childTC
			toolErrors += childTE

			// Sort all entries chronologically so parent and child events interleave
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Timestamp < entries[j].Timestamp
			})
		}

		output := traceJSON{
			SessionID: meta.ID,
			App:       meta.AppName,
			User:      meta.UserID,
			Events:    entries,
			Summary: traceSummary{
				TotalEvents: len(events),
				ToolCalls:   toolCalls,
				Errors:      toolErrors,
			},
		}

		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("error serializing trace: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// Print metadata header
	fmt.Printf("Session: %s\n", meta.ID)
	fmt.Printf("App:     %s\n", meta.AppName)
	fmt.Printf("User:    %s\n", meta.UserID)
	fmt.Printf("Created: %s\n", meta.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated: %s\n", meta.UpdatedAt.Format(time.RFC3339))
	if meta.Title != "" {
		fmt.Printf("Title:   %s\n", meta.Title)
	}
	fmt.Printf("Events:  %d\n", len(events))

	// Show sub-session count if any
	children, _ := index.ListChildren(fullID)
	if len(children) > 0 {
		fmt.Printf("Sub-sessions: %d\n", len(children))
	}

	if len(events) == 0 {
		fmt.Println("\nNo events recorded.")
		return nil
	}

	// Render the trace
	renderSessionTrace(events, opts)

	// Render child sessions inline if --recursive
	if opts.Recursive && len(children) > 0 {
		renderChildSessions(sessDir, fullID, index, opts)
	}

	return nil
}

func handleSessionsDelete(sessionID string) error {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if appCfg.Sessions.Storage == "memory" {
		fmt.Println("Session persistence is disabled (storage: memory).")
		return nil
	}

	sessDir, err := config.GetSessionsDir(&appCfg.Sessions)
	if err != nil {
		return fmt.Errorf("failed to resolve sessions dir: %w", err)
	}

	index := persistentsession.NewSessionIndex(sessDir + "/index.json")
	fullID, err := resolveSessionID(index, sessionID)
	if err != nil {
		return err
	}

	// Get metadata for transcript path
	meta, err := index.Get(fullID)
	if err != nil {
		return fmt.Errorf("session not found: %w", err)
	}

	// Cascade: remove child sub-session transcripts (and their sidecars).
	children, _ := index.ListChildren(fullID)
	for _, child := range children {
		removeSessionFiles(sessDir, child.AppName, child.UserID, child.ID)
	}

	// Remove the parent's transcript and sidecar files.
	removeSessionFiles(sessDir, meta.AppName, meta.UserID, meta.ID)

	// Remove from index (cascades children automatically)
	if err := index.Remove(fullID); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	// Best-effort: destroy sandbox container if one exists for this session
	sandbox.TryDestroySession(appCfg, fullID, nil)
	for _, child := range children {
		sandbox.TryDestroySession(appCfg, child.ID, nil)
	}

	if len(children) > 0 {
		fmt.Printf("Deleted session %s and %d sub-session(s)\n", fullID, len(children))
	} else {
		fmt.Printf("Deleted session %s\n", fullID)
	}
	return nil
}

func handleSessionsClear() error {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if appCfg.Sessions.Storage == "memory" {
		fmt.Println("Session persistence is disabled (storage: memory).")
		return nil
	}

	sessDir, err := config.GetSessionsDir(&appCfg.Sessions)
	if err != nil {
		return fmt.Errorf("failed to resolve sessions dir: %w", err)
	}

	index := persistentsession.NewSessionIndex(sessDir + "/index.json")
	data, err := index.Load()
	if err != nil {
		return fmt.Errorf("failed to load session index: %w", err)
	}

	count := len(data.Sessions)
	if count == 0 {
		fmt.Println("No sessions to delete.")
		return nil
	}

	// Confirmation prompt
	fmt.Printf("This will delete all %d session(s). Are you sure? [y/N] ", count)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	// Delete all transcript files
	deleted := 0
	for id, meta := range data.Sessions {
		removeSessionFiles(sessDir, meta.AppName, meta.UserID, meta.ID)
		// Best-effort: destroy sandbox container if one exists
		sandbox.TryDestroySession(appCfg, id, nil)
		deleted++
	}

	// Clear the index
	data.Sessions = make(map[string]persistentsession.SessionMeta)
	if err := index.Save(data); err != nil {
		return fmt.Errorf("failed to save cleared index: %w", err)
	}

	fmt.Printf("Deleted %d session(s).\n", deleted)
	return nil
}

// resolveSessionID resolves a potentially partial session ID to a full one.
func resolveSessionID(index *persistentsession.SessionIndex, partial string) (string, error) {
	data, err := index.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load index: %w", err)
	}

	// Exact match first
	if _, ok := data.Sessions[partial]; ok {
		return partial, nil
	}

	// Prefix match
	var matches []string
	for id := range data.Sessions {
		if strings.HasPrefix(id, partial) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matching %q", partial)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("ambiguous session ID %q matches %d sessions:\n  %s", partial, len(matches), strings.Join(matches, "\n  "))
	}
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func printSessionsUsage() {
	fmt.Println("usage: astonish sessions <command> [args]")
	fmt.Println("")
	fmt.Println("Manage and inspect persistent chat sessions.")
	fmt.Println("")
	fmt.Println("commands:")
	fmt.Println("  list, ls              List all sessions")
	fmt.Println("  show <id> [flags]     Show session trace (tool calls, LLM responses, errors)")
	fmt.Println("  delete, rm <id>       Delete a session")
	fmt.Println("  clear                 Delete all sessions")
	fmt.Println("")
	fmt.Println("show flags:")
	fmt.Println("  --json                Output as JSON")
	fmt.Println("  -t, --tools-only      Only show tool calls (skip LLM text)")
	fmt.Println("  -v, --verbose         Show full tool args/results (no truncation)")
	fmt.Println("  -r, --recursive       Include sub-agent session traces inline")
	fmt.Println("  -n, --last N          Only show last N events")
	fmt.Println("")
	fmt.Println("Session IDs can be abbreviated (prefix match).")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  astonish sessions list")
	fmt.Println("  astonish sessions show abc123")
	fmt.Println("  astonish sessions show abc123 -t")
	fmt.Println("  astonish sessions show abc123 -r              # include sub-agent traces")
	fmt.Println("  astonish sessions show abc123 -r -v           # recursive + verbose")
	fmt.Println("  astonish sessions show telegram:direct:123 --tools-only --last 20")
	fmt.Println("  astonish sessions show abc123 --json")
	fmt.Println("  astonish sessions delete abc123")
	fmt.Println("  astonish sessions clear")
}

// --- Remote mode session handlers ---

func handleSessionsRemote(args []string) error {
	c, err := client.New()
	if err != nil {
		return err
	}

	subcommand := args[0]
	switch subcommand {
	case "list", "ls":
		return handleSessionsListRemote(c)
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("session ID required")
		}
		return handleSessionsShowRemote(c, args[1], args[2:])
	case "delete", "rm":
		if len(args) < 2 {
			return fmt.Errorf("session ID required")
		}
		return handleSessionsDeleteRemote(c, args[1])
	case "clear":
		return fmt.Errorf("'sessions clear' is not supported in remote mode (use Studio UI)")
	default:
		return fmt.Errorf("unknown subcommand: %s", subcommand)
	}
}

func handleSessionsListRemote(c *client.Client) error {
	sessions, err := c.ListSessions()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tMESSAGES\tUPDATED")

	for _, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		updated := s.UpdatedAt
		if t, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil {
			updated = t.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", s.ID, title, s.MessageCount, updated)
	}
	w.Flush()
	return nil
}

func handleSessionsShowRemote(c *client.Client, id string, flags []string) error {
	// Parse flags (same as local mode)
	opts := TraceOpts{}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--json":
			opts.JSONOutput = true
		case "--tools-only", "-t":
			opts.ToolsOnly = true
		case "--verbose", "-v":
			opts.Verbose = true
		case "--recursive", "-r":
			opts.Recursive = true
		case "--last", "-n":
			if i+1 < len(flags) {
				i++
				n, err := strconv.Atoi(flags[i])
				if err != nil {
					return fmt.Errorf("invalid value for --last: %s", flags[i])
				}
				opts.LastN = n
			} else {
				return fmt.Errorf("--last requires a number")
			}
		default:
			if strings.HasPrefix(flags[i], "-n") && len(flags[i]) > 2 {
				n, err := strconv.Atoi(flags[i][2:])
				if err == nil {
					opts.LastN = n
					continue
				}
			}
			return fmt.Errorf("unknown flag: %s", flags[i])
		}
	}

	// Determine which endpoint and rendering mode to use:
	// - --json: trace endpoint + raw JSON dump
	// - -t (tools-only): trace endpoint + compact tools rendering
	// - -r, -n: trace endpoint + pretty conversation rendering
	// - default: detail endpoint + pretty conversation rendering
	useTrace := opts.JSONOutput || opts.Recursive || opts.ToolsOnly || opts.LastN > 0

	if useTrace {
		traceOpts := client.TraceOpts{
			Recursive: opts.Recursive,
			ToolsOnly: opts.ToolsOnly,
			Verbose:   opts.Verbose,
			LastN:     opts.LastN,
		}

		trace, err := c.GetSessionTrace(id, traceOpts)
		if err != nil {
			return fmt.Errorf("get session trace: %w", err)
		}

		if opts.JSONOutput {
			out, _ := json.MarshalIndent(trace, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		if opts.ToolsOnly {
			// Compact tools-only rendering
			renderRemoteTrace(trace, opts)
			return nil
		}

		// Pretty conversation rendering from trace events (for -r, -n)
		renderRemoteTracePretty(trace, opts)
		return nil
	}

	// Default: get session detail and pretty-print as conversation
	detail, err := c.GetSession(id)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	renderRemoteSessionDetail(detail, opts.Verbose)
	return nil
}

// renderRemoteSessionDetail pretty-prints a session detail response as a readable conversation.
func renderRemoteSessionDetail(detail map[string]any, verbose bool) {
	// Print metadata header
	id, _ := detail["id"].(string)
	title, _ := detail["title"].(string)
	createdAt, _ := detail["createdAt"].(string)
	updatedAt, _ := detail["updatedAt"].(string)

	fmt.Printf("Session: %s\n", id)
	if title != "" {
		fmt.Printf("Title:   %s\n", title)
	}
	if createdAt != "" {
		fmt.Printf("Created: %s\n", createdAt)
	}
	if updatedAt != "" {
		fmt.Printf("Updated: %s\n", updatedAt)
	}

	// Usage summary
	if usage, ok := detail["totalUsage"].(map[string]any); ok {
		input, _ := usage["inputTokens"].(float64)
		output, _ := usage["outputTokens"].(float64)
		if input > 0 || output > 0 {
			fmt.Printf("Tokens:  %d input, %d output\n", int(input), int(output))
		}
	}

	// Messages
	messages, _ := detail["messages"].([]any)
	if len(messages) == 0 {
		fmt.Println("\nNo messages recorded.")
		return
	}

	fmt.Printf("Messages: %d\n", len(messages))
	fmt.Println()

	textMax := 500
	if verbose {
		textMax = 0
	}

	for _, msgRaw := range messages {
		msg, ok := msgRaw.(map[string]any)
		if !ok {
			continue
		}
		msgType, _ := msg["type"].(string)
		content, _ := msg["content"].(string)
		toolName, _ := msg["toolName"].(string)

		switch msgType {
		case "user":
			if content != "" {
				display := content
				if textMax > 0 && len(display) > textMax {
					display = display[:textMax-3] + "..."
				}
				fmt.Printf("[user] %s\n\n", display)
			}
		case "agent":
			if content != "" {
				display := content
				if textMax > 0 && len(display) > textMax {
					display = display[:textMax-3] + "..."
				}
				fmt.Printf("[model] %s\n\n", display)
			}
		case "tool_call":
			if toolName != "" {
				argsStr := ""
				if args, ok := msg["toolArgs"].(map[string]any); ok && verbose {
					argsJSON, _ := json.Marshal(args)
					argsStr = " " + string(argsJSON)
				}
				fmt.Printf("[tool] %s%s\n", toolName, argsStr)
			}
		case "tool_result":
			if toolName != "" {
				resultStr := "OK"
				if result, ok := msg["toolResult"].(map[string]any); ok {
					if errMsg, ok := result["error"].(string); ok && errMsg != "" {
						resultStr = "ERROR: " + errMsg
						if textMax > 0 && len(resultStr) > textMax {
							resultStr = resultStr[:textMax-3] + "..."
						}
					} else if verbose {
						rJSON, _ := json.Marshal(result)
						resultStr = string(rJSON)
					}
				}
				fmt.Printf("   -> %s %s\n\n", toolName, resultStr)
			}
		case "subtask_execution":
			tasks, _ := msg["tasks"].([]any)
			status, _ := msg["status"].(string)
			if len(tasks) > 0 {
				fmt.Printf("[subtasks] %d tasks", len(tasks))
				if status != "" {
					fmt.Printf(" (%s)", status)
				}
				fmt.Println()
				for _, t := range tasks {
					if task, ok := t.(map[string]any); ok {
						name, _ := task["name"].(string)
						fmt.Printf("   • %s\n", name)
					}
				}
				fmt.Println()
			}
		case "plan":
			goal, _ := msg["goal"].(string)
			steps, _ := msg["steps"].([]any)
			if goal != "" {
				fmt.Printf("[plan] %s\n", goal)
				for _, s := range steps {
					if step, ok := s.(map[string]any); ok {
						name, _ := step["name"].(string)
						stepStatus, _ := step["status"].(string)
						marker := "○"
						if stepStatus == "complete" {
							marker = "✓"
						} else if stepStatus == "in_progress" {
							marker = "▶"
						}
						fmt.Printf("   %s %s\n", marker, name)
					}
				}
				fmt.Println()
			}
		case "system":
			if content != "" {
				fmt.Printf("[system] %s\n\n", content)
			}
		}
	}
}

// renderRemoteTracePretty renders trace events in pretty conversation style,
// similar to renderRemoteSessionDetail but working from trace data.
// This is used for -r (recursive) and -n (last N) where we need the trace
// endpoint data but want conversation-style output.
func renderRemoteTracePretty(trace map[string]any, opts TraceOpts) {
	// Print header
	sessionID, _ := trace["session_id"].(string)
	fmt.Printf("Session: %s\n", sessionID)
	fmt.Println()

	events, _ := trace["events"].([]any)
	if len(events) == 0 {
		fmt.Println("No events recorded.")
		return
	}

	textMax := 500
	argsMax := 200
	if opts.Verbose {
		textMax = 0
		argsMax = 0
	}

	lastSession := ""

	for _, evRaw := range events {
		ev, ok := evRaw.(map[string]any)
		if !ok {
			continue
		}

		evType, _ := ev["type"].(string)
		session, _ := ev["session"].(string)
		text, _ := ev["text"].(string)
		toolName, _ := ev["tool_name"].(string)
		durationMs, _ := ev["duration_ms"].(float64)

		// Show sub-session header when session label changes
		if session != "" && session != lastSession {
			fmt.Printf("\n  === Sub-agent: %s ===\n\n", session)
			lastSession = session
		} else if session == "" && lastSession != "" {
			// Returning from sub-session to main
			fmt.Println()
			lastSession = ""
		}

		indent := ""
		if session != "" {
			indent = "  "
		}

		switch evType {
		case "user":
			if text != "" {
				display := text
				if textMax > 0 && len(display) > textMax {
					display = display[:textMax-3] + "..."
				}
				fmt.Printf("%s[user] %s\n\n", indent, display)
			}
		case "model":
			if text != "" {
				display := text
				if textMax > 0 && len(display) > textMax {
					display = display[:textMax-3] + "..."
				}
				fmt.Printf("%s[model] %s\n\n", indent, display)
			}
		case "tool_call":
			if toolName != "" {
				if opts.Verbose {
					argsRaw, _ := ev["args"].(map[string]any)
					argsStr := ""
					if argsRaw != nil {
						argsJSON, _ := json.Marshal(argsRaw)
						argsStr = " " + string(argsJSON)
					}
					fmt.Printf("%s[tool] %s%s\n", indent, toolName, argsStr)
				} else {
					// Show just tool name with brief args hint
					argsRaw, _ := ev["args"].(map[string]any)
					hint := ""
					if argsRaw != nil {
						argsJSON, _ := json.Marshal(argsRaw)
						s := string(argsJSON)
						if len(s) > argsMax {
							s = s[:argsMax-3] + "..."
						}
						hint = " " + s
					}
					fmt.Printf("%s[tool] %s%s\n", indent, toolName, hint)
				}
			}
		case "tool_result":
			if toolName != "" {
				// Check success
				success := true
				if s, ok := ev["success"].(bool); ok {
					success = s
				}
				errStr, _ := ev["error"].(string)

				durationStr := ""
				if durationMs > 0 {
					if durationMs < 1000 {
						durationStr = fmt.Sprintf(" (%dms)", int(durationMs))
					} else {
						durationStr = fmt.Sprintf(" (%.1fs)", durationMs/1000)
					}
				}

				if !success || errStr != "" {
					if errStr == "" {
						errStr = "error"
					}
					if textMax > 0 && len(errStr) > textMax {
						errStr = errStr[:textMax-3] + "..."
					}
					fmt.Printf("%s   -> %s ERROR: %s%s\n\n", indent, toolName, errStr, durationStr)
				} else {
					resultRaw, _ := ev["result"].(map[string]any)
					resultStr := "OK"
					if resultRaw != nil && opts.Verbose {
						rJSON, _ := json.Marshal(resultRaw)
						resultStr = string(rJSON)
					} else if resultRaw != nil {
						// Brief summary
						rJSON, _ := json.Marshal(resultRaw)
						resultStr = string(rJSON)
						if argsMax > 0 && len(resultStr) > argsMax {
							resultStr = resultStr[:argsMax-3] + "..."
						}
					}
					fmt.Printf("%s   -> %s %s%s\n\n", indent, toolName, resultStr, durationStr)
				}
			}
		}
	}

	// Summary
	if summary, ok := trace["summary"].(map[string]any); ok {
		totalEvents, _ := summary["total_events"].(float64)
		toolCalls, _ := summary["tool_calls"].(float64)
		errors, _ := summary["errors"].(float64)

		parts := []string{fmt.Sprintf("%d events", int(totalEvents))}
		if toolCalls > 0 {
			parts = append(parts, fmt.Sprintf("%d tool calls", int(toolCalls)))
		}
		if errors > 0 {
			parts = append(parts, fmt.Sprintf("%d errors", int(errors)))
		}
		fmt.Printf("--- %s ---\n", strings.Join(parts, " | "))
	}
}

// renderRemoteTrace renders trace entries in compact tools-only format.
func renderRemoteTrace(trace map[string]any, opts TraceOpts) {
	// Print header
	sessionID, _ := trace["session_id"].(string)
	app, _ := trace["app"].(string)
	user, _ := trace["user"].(string)

	fmt.Printf("Session: %s\n", sessionID)
	if app != "" {
		fmt.Printf("App:     %s\n", app)
	}
	if user != "" {
		fmt.Printf("User:    %s\n", user)
	}
	fmt.Println()

	events, _ := trace["events"].([]any)
	if len(events) == 0 {
		fmt.Println("No events recorded.")
		return
	}

	argsMax := 200
	textMax := 500
	if opts.Verbose {
		argsMax = 0
		textMax = 0
	}

	lastSession := ""

	for _, evRaw := range events {
		ev, ok := evRaw.(map[string]any)
		if !ok {
			continue
		}

		evType, _ := ev["type"].(string)
		timestamp, _ := ev["timestamp"].(string)
		session, _ := ev["session"].(string)
		text, _ := ev["text"].(string)
		toolName, _ := ev["tool_name"].(string)
		errStr, _ := ev["error"].(string)
		durationMs, _ := ev["duration_ms"].(float64)

		// Show sub-session header when session label changes
		if session != "" && session != lastSession {
			fmt.Printf("\n  === Sub-agent: %s ===\n", session)
			lastSession = session
		}

		indent := ""
		if session != "" {
			indent = "  "
		}

		// Format timestamp to HH:MM:SS
		ts := timestamp
		if t, parseErr := time.Parse(time.RFC3339Nano, timestamp); parseErr == nil {
			ts = t.Format("15:04:05")
		} else if t, parseErr := time.Parse(time.RFC3339, timestamp); parseErr == nil {
			ts = t.Format("15:04:05")
		}

		switch evType {
		case "user":
			if !opts.ToolsOnly && text != "" {
				display := text
				if textMax > 0 && len(display) > textMax {
					display = display[:textMax-3] + "..."
				}
				fmt.Printf("%s[user] %s\n\n", indent, display)
			}
		case "model":
			if !opts.ToolsOnly && text != "" {
				display := text
				if textMax > 0 && len(display) > textMax {
					display = display[:textMax-3] + "..."
				}
				fmt.Printf("%s[model] %s\n\n", indent, display)
			}
		case "tool_call":
			argsRaw, _ := ev["args"].(map[string]any)
			argsStr := ""
			if argsRaw != nil {
				argsJSON, _ := json.Marshal(argsRaw)
				argsStr = string(argsJSON)
				if argsMax > 0 && len(argsStr) > argsMax {
					argsStr = argsStr[:argsMax-3] + "..."
				}
			}
			if opts.ToolsOnly {
				fmt.Printf("%s%s  %s %s\n", indent, ts, toolName, argsStr)
			} else {
				fmt.Printf("%s[tool] %s %s\n", indent, toolName, argsStr)
			}
		case "tool_result":
			durationStr := ""
			if durationMs > 0 {
				if durationMs < 1000 {
					durationStr = fmt.Sprintf("%dms", int(durationMs))
				} else {
					durationStr = fmt.Sprintf("%.1fs", durationMs/1000)
				}
			}

			// Check success field
			success := true
			if s, ok := ev["success"].(*bool); ok && s != nil {
				success = *s
			} else if s, ok := ev["success"].(bool); ok {
				success = s
			}

			if !success || errStr != "" {
				if errStr == "" {
					errStr = "unknown error"
				}
				if argsMax > 0 && len(errStr) > argsMax {
					errStr = errStr[:argsMax-3] + "..."
				}
				if opts.ToolsOnly {
					fmt.Printf("%s         -> ERROR: %s  (%s)\n", indent, errStr, durationStr)
				} else {
					fmt.Printf("%s   -> ERROR: %s  (%s)\n\n", indent, errStr, durationStr)
				}
			} else {
				resultRaw, _ := ev["result"].(map[string]any)
				resultStr := "OK"
				if resultRaw != nil {
					rJSON, _ := json.Marshal(resultRaw)
					resultStr = string(rJSON)
					if argsMax > 0 && len(resultStr) > argsMax {
						resultStr = resultStr[:argsMax-3] + "..."
					}
				}
				if opts.ToolsOnly {
					fmt.Printf("%s         -> %s  (%s)\n", indent, resultStr, durationStr)
				} else {
					fmt.Printf("%s   -> %s  (%s)\n\n", indent, resultStr, durationStr)
				}
			}
		}
	}

	// Summary
	if summary, ok := trace["summary"].(map[string]any); ok {
		fmt.Println()
		totalEvents, _ := summary["total_events"].(float64)
		toolCalls, _ := summary["tool_calls"].(float64)
		errors, _ := summary["errors"].(float64)

		parts := []string{fmt.Sprintf("%d events", int(totalEvents))}
		if toolCalls > 0 {
			parts = append(parts, fmt.Sprintf("%d tool calls", int(toolCalls)))
		}
		if errors > 0 {
			parts = append(parts, fmt.Sprintf("%d errors", int(errors)))
		}
		fmt.Printf("--- %s ---\n", strings.Join(parts, " | "))
	}
}

func handleSessionsDeleteRemote(c *client.Client, id string) error {
	if err := c.DeleteSession(id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	fmt.Printf("Session %s deleted.\n", id)
	return nil
}
