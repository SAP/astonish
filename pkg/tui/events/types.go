// Package events defines the mode-agnostic chat event model shared by the
// local (ADK) and remote (SSE) TUI backends. The bubbletea app reduces these
// events into transcript state; it never talks to ADK or HTTP directly.
package events

// Kind identifies a chat event. Values mirror Studio SSE types where possible
// so the remote backend can map 1:1 and soft-degrade unknown kinds.
type Kind string

const (
	KindSession      Kind = "session"
	KindText         Kind = "text"
	KindThinking     Kind = "thinking"
	KindToolCall     Kind = "tool_call"
	KindToolResult   Kind = "tool_result"
	KindApproval     Kind = "approval"
	KindAutoApproved Kind = "auto_approved"
	KindArtifact     Kind = "artifact"
	KindUsage        Kind = "usage"
	KindError        Kind = "error"
	KindErrorInfo    Kind = "error_info"
	KindDone         Kind = "done"
	KindSystem       Kind = "system"
	KindSubagent     Kind = "subagent"
	KindSessionTitle Kind = "session_title"
	KindModelChanged Kind = "model_changed"
	KindStatus       Kind = "status" // spinner / live status text
	KindUser         Kind = "user"   // local echo of user message
)

// Usage holds token accounting for a turn.
type Usage struct {
	Input  int64
	Output int64
	Total  int64
}

// Event is one unit of chat progress. Fields are optional by Kind.
type Event struct {
	Kind Kind

	// Text is used by KindText, KindThinking, KindSystem, KindError, KindStatus.
	Text string

	// Tool fields for KindToolCall / KindToolResult / KindApproval / KindAutoApproved.
	ToolName string
	ToolID   string
	Args     map[string]any
	Result   any
	Options  []string // approval options

	// SessionID for KindSession / KindSessionTitle.
	SessionID string
	Title     string

	// Usage for KindUsage.
	Usage *Usage

	// Structured error (KindErrorInfo).
	ErrorTitle      string
	ErrorReason     string
	ErrorSuggestion string

	// Provider/model for KindModelChanged.
	Provider string
	Model    string

	// Meta holds optional backend-specific keys without expanding the struct.
	Meta map[string]any
}

// NewText returns a streaming agent text event.
func NewText(s string) Event {
	return Event{Kind: KindText, Text: s}
}

// NewSystem returns a system notice event.
func NewSystem(s string) Event {
	return Event{Kind: KindSystem, Text: s}
}

// NewError returns a simple error event.
func NewError(s string) Event {
	return Event{Kind: KindError, Text: s}
}

// NewStatus returns a live status / spinner text event.
func NewStatus(s string) Event {
	return Event{Kind: KindStatus, Text: s}
}

// NewUser returns a user message echo event.
func NewUser(s string) Event {
	return Event{Kind: KindUser, Text: s}
}

// NewDone marks the end of a turn stream.
func NewDone() Event {
	return Event{Kind: KindDone}
}

// NewToolCall returns a tool invocation event.
func NewToolCall(name, id string, args map[string]any) Event {
	return Event{Kind: KindToolCall, ToolName: name, ToolID: id, Args: args}
}

// NewToolResult returns a tool result event.
func NewToolResult(name, id string, result any) Event {
	return Event{Kind: KindToolResult, ToolName: name, ToolID: id, Result: result}
}

// NewApproval returns a tool approval request.
func NewApproval(name string, args map[string]any, options []string) Event {
	return Event{Kind: KindApproval, ToolName: name, Args: args, Options: options}
}
