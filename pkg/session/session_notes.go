package session

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// SessionNotes maintains structured state about a coding session,
// accumulated incrementally as work proceeds. Used by the code-mode
// compaction strategy as a pre-built summary (amortized cost: no LLM
// call needed at compaction time when notes are fresh).
type SessionNotes struct {
	mu sync.RWMutex

	Objective      string              // User's high-level goal
	FilesModified  map[string]FileNote // path → what changed
	TasksCompleted []string            // Finished work items
	TasksPending   []string            // Known remaining work
	Decisions      []string            // Key technical decisions
	Errors         []ErrorNote         // Errors encountered + resolutions
	CurrentState   string              // What's being worked on right now
	LastUpdated    time.Time
}

// FileNote describes a single file's involvement in the session.
type FileNote struct {
	Action      string    // "created", "modified", "deleted", "read"
	Description string    // One-line description
	LastTouched time.Time
}

// ErrorNote records an error encountered during the session.
type ErrorNote struct {
	Error      string
	Resolution string // empty if unresolved
	Resolved   bool
}

// NewSessionNotes creates a new empty SessionNotes instance.
func NewSessionNotes() *SessionNotes {
	return &SessionNotes{
		FilesModified: make(map[string]FileNote),
	}
}

// RecordFileChange records a file operation. Thread-safe. Deduplicates by path,
// upgrading action priority (edit > read) and updating the description.
func (n *SessionNotes) RecordFileChange(path, action, description string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	existing, exists := n.FilesModified[path]
	if exists {
		// Upgrade action: write/edit > read
		if actionPriority(action) > actionPriority(existing.Action) {
			existing.Action = action
		}
		if description != "" {
			existing.Description = description
		}
		existing.LastTouched = time.Now()
		n.FilesModified[path] = existing
	} else {
		n.FilesModified[path] = FileNote{
			Action:      action,
			Description: description,
			LastTouched: time.Now(),
		}
	}
	n.LastUpdated = time.Now()
}

// RecordTaskCompleted records a finished work item.
func (n *SessionNotes) RecordTaskCompleted(task string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.TasksCompleted = append(n.TasksCompleted, task)
	n.LastUpdated = time.Now()
}

// AddTaskPending adds a known remaining work item.
func (n *SessionNotes) AddTaskPending(task string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.TasksPending = append(n.TasksPending, task)
	n.LastUpdated = time.Now()
}

// RemoveTaskPending removes a task from pending and marks it completed.
func (n *SessionNotes) RemoveTaskPending(task string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, t := range n.TasksPending {
		if t == task {
			n.TasksPending = append(n.TasksPending[:i], n.TasksPending[i+1:]...)
			n.TasksCompleted = append(n.TasksCompleted, task)
			break
		}
	}
	n.LastUpdated = time.Now()
}

// RecordDecision records a key technical decision.
func (n *SessionNotes) RecordDecision(decision string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Decisions = append(n.Decisions, decision)
	n.LastUpdated = time.Now()
}

// RecordError records an error and optionally its resolution.
func (n *SessionNotes) RecordError(err, resolution string, resolved bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Errors = append(n.Errors, ErrorNote{
		Error:      err,
		Resolution: resolution,
		Resolved:   resolved,
	})
	n.LastUpdated = time.Now()
}

// SetObjective sets the user's high-level goal.
func (n *SessionNotes) SetObjective(obj string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Objective = obj
	n.LastUpdated = time.Now()
}

// SetCurrentState updates the current working state description.
func (n *SessionNotes) SetCurrentState(state string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.CurrentState = state
	n.LastUpdated = time.Now()
}

// IsEmpty returns true if no meaningful state has been recorded.
func (n *SessionNotes) IsEmpty() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.Objective == "" &&
		len(n.FilesModified) == 0 &&
		len(n.TasksCompleted) == 0 &&
		len(n.TasksPending) == 0 &&
		len(n.Decisions) == 0 &&
		len(n.Errors) == 0 &&
		n.CurrentState == ""
}

// Render produces a structured markdown summary in the same 7-section format
// expected by the CodeStrategy summarization output.
func (n *SessionNotes) Render() string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var sb strings.Builder

	sb.WriteString("## OBJECTIVE\n")
	if n.Objective != "" {
		sb.WriteString(n.Objective)
	} else {
		sb.WriteString("(not yet determined)")
	}
	sb.WriteString("\n\n")

	sb.WriteString("## FILES MODIFIED\n")
	if len(n.FilesModified) > 0 {
		for path, note := range n.FilesModified {
			desc := note.Description
			if desc == "" {
				desc = note.Action
			}
			sb.WriteString(fmt.Sprintf("- `%s` — %s\n", path, desc))
		}
	} else {
		sb.WriteString("(none yet)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## TASKS COMPLETED\n")
	if len(n.TasksCompleted) > 0 {
		for _, task := range n.TasksCompleted {
			sb.WriteString(fmt.Sprintf("- %s\n", task))
		}
	} else {
		sb.WriteString("(none yet)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## TASKS PENDING\n")
	if len(n.TasksPending) > 0 {
		for _, task := range n.TasksPending {
			sb.WriteString(fmt.Sprintf("- %s\n", task))
		}
	} else {
		sb.WriteString("(none known)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## KEY DECISIONS\n")
	if len(n.Decisions) > 0 {
		for _, d := range n.Decisions {
			sb.WriteString(fmt.Sprintf("- %s\n", d))
		}
	} else {
		sb.WriteString("(none recorded)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## ERRORS & FIXES\n")
	if len(n.Errors) > 0 {
		for _, e := range n.Errors {
			if e.Resolved {
				sb.WriteString(fmt.Sprintf("- ✅ %s — Fixed: %s\n", e.Error, e.Resolution))
			} else {
				sb.WriteString(fmt.Sprintf("- ❌ %s (unresolved)\n", e.Error))
			}
		}
	} else {
		sb.WriteString("(none)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## CURRENT STATE\n")
	if n.CurrentState != "" {
		sb.WriteString(n.CurrentState)
	} else {
		sb.WriteString("(session starting)")
	}
	sb.WriteString("\n")

	return sb.String()
}

// Clone returns a deep copy of the SessionNotes for safe concurrent reads.
func (n *SessionNotes) Clone() *SessionNotes {
	n.mu.RLock()
	defer n.mu.RUnlock()

	clone := &SessionNotes{
		Objective:      n.Objective,
		FilesModified:  make(map[string]FileNote, len(n.FilesModified)),
		TasksCompleted: make([]string, len(n.TasksCompleted)),
		TasksPending:   make([]string, len(n.TasksPending)),
		Decisions:      make([]string, len(n.Decisions)),
		Errors:         make([]ErrorNote, len(n.Errors)),
		CurrentState:   n.CurrentState,
		LastUpdated:    n.LastUpdated,
	}
	for k, v := range n.FilesModified {
		clone.FilesModified[k] = v
	}
	copy(clone.TasksCompleted, n.TasksCompleted)
	copy(clone.TasksPending, n.TasksPending)
	copy(clone.Decisions, n.Decisions)
	copy(clone.Errors, n.Errors)
	return clone
}

// RecordToolOutcome automatically extracts file paths from known tool call
// arguments and categorizes the action. This is the primary integration hook
// called after each tool execution.
func (n *SessionNotes) RecordToolOutcome(toolName string, args map[string]any, result map[string]any, err error) {
	if err != nil {
		errMsg := fmt.Sprintf("%s failed: %s", toolName, err.Error())
		n.RecordError(errMsg, "", false)
	}

	switch toolName {
	case "edit_file":
		if path, ok := args["path"].(string); ok && path != "" {
			n.RecordFileChange(path, "modified", "")
		}
	case "write_file":
		if path, ok := args["file_path"].(string); ok && path != "" {
			n.RecordFileChange(path, "created", "")
		}
	case "read_file":
		if path, ok := args["path"].(string); ok && path != "" {
			n.RecordFileChange(path, "read", "")
		}
	case "shell_command":
		// Record shell command errors from exit code
		if result != nil {
			if exitCode, ok := result["exit_code"]; ok {
				if code, isFloat := exitCode.(float64); isFloat && code != 0 {
					cmd, _ := args["command"].(string)
					if cmd != "" {
						truncCmd := cmd
						if len(truncCmd) > 80 {
							truncCmd = truncCmd[:77] + "..."
						}
						n.RecordError(fmt.Sprintf("shell_command '%s' exited with code %.0f", truncCmd, code), "", false)
					}
				}
			}
		}
	}
}

// actionPriority returns a priority value for file actions so that higher-impact
// actions (like edit) are not downgraded when a later read is recorded.
func actionPriority(action string) int {
	switch action {
	case "created":
		return 4
	case "deleted":
		return 3
	case "modified":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}
