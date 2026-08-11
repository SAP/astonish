package agent

// SubAgentAuthRequest is sent from a sub-agent to the parent when a tool
// needs user authorization. The sub-agent goroutine blocks until the parent
// responds via SubAgentAuthResponse.
type SubAgentAuthRequest struct {
	// TaskName is the sub-agent task name (for UI context, e.g., "researcher").
	TaskName string
	// Kind is "tool" or "folder".
	Kind string
	// ToolName is the tool that needs authorization.
	ToolName string
	// Args are the tool arguments (for display in the approval prompt).
	Args map[string]any
	// OutOfScopePaths are paths outside the project root (folder kind only).
	OutOfScopePaths []string
	// ParentSessionID is the parent session ID for policy lookup.
	ParentSessionID string
}

// SubAgentAuthResponse is the user's decision on a sub-agent authorization request.
type SubAgentAuthResponse struct {
	// Granted is true if the user allowed the tool to proceed.
	Granted bool
	// Choice is the raw user choice text (e.g., "Allow", "Always Allow", "Deny").
	Choice string
}
