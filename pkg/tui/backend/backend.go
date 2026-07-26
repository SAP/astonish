// Package backend defines the chat backend interface used by the TUI.
// Chat is always platform-backed (authenticated Studio REST/SSE), including
// local platform installs. There is no in-process personal chat path.
package backend

import (
	"context"

	"github.com/SAP/astonish/pkg/tui/events"
)

// Info is static session metadata shown in the TUI header / footer.
type Info struct {
	SessionID string
	Provider  string
	Model     string
	Mode      string // "platform"
	ServerURL string
	Org       string
	Team      string
	User      string
	// Usage is cumulative token usage known when opening/resuming a session.
	Usage     *events.Usage
	IsResumed bool
	// AutoApprove reflects the session tool-approval mode for footer chrome.
	AutoApprove bool
	// Notices are shown once at startup (startup warnings, etc.).
	Notices []string
	// Title is the human session title when known.
	Title string
}

// SessionSummary is one row in the sessions picker.
type SessionSummary struct {
	ID           string
	Title        string
	UpdatedAt    string
	MessageCount int
}

// HistoryEntry is one message loaded when resuming a session.
type HistoryEntry struct {
	// Kind: user | agent | tool_call | tool_result | system | thinking
	Kind     string
	Text     string
	ToolName string
	ToolID   string
	Args     map[string]any
	Result   any
}

// TurnOptions configures per-turn behavior for the platform request.
type TurnOptions struct {
	// SystemContext is a per-turn hidden instruction sent to Studio chat. It is
	// not persisted as a visible user message.
	SystemContext string
}

// NetworkGrantBackend is implemented by platform backends that can resolve
// sandbox network-denial prompts without sending a normal chat approval message.
type NetworkGrantBackend interface {
	ApproveNetworkGrant(ctx context.Context, sessionID string, denial events.NetworkDenial, broader bool, sandboxName string) error
	DenyNetworkGrant(ctx context.Context, sessionID string, denial events.NetworkDenial, sandboxName string) error
}

// Backend drives one interactive chat session against the platform.
//
// Implementations must be safe for:
//   - RunTurn from the TUI's async command goroutine
//   - Cancel via context
//
// Approval: when a turn emits KindApproval and ends (or pauses), the TUI
// collects a Yes/No (or option) response and calls RunTurn again with that
// text as the message — same protocol as Studio chat.
type Backend interface {
	// Info returns header metadata (may be called after Open / Resume).
	Info() Info

	// Open prepares the connection. If a session was configured for resume,
	// LoadHistory should still be called by the TUI to populate the transcript.
	Open(ctx context.Context) error

	// RunTurn sends a user message (or approval response) and returns a
	// channel of events. The channel is closed when the turn is finished
	// (after KindDone or a terminal error event). The caller must drain it.
	RunTurn(ctx context.Context, message string, opts TurnOptions) (<-chan events.Event, error)

	// ListSessions returns recent chat sessions for the picker.
	ListSessions(ctx context.Context) ([]SessionSummary, error)

	// LoadHistory returns prior messages for the current session ID (if any).
	// Empty slice if new session / no history.
	LoadHistory(ctx context.Context) ([]HistoryEntry, error)

	// ResumeSession switches the backend to sessionID and returns its history.
	ResumeSession(ctx context.Context, sessionID string) ([]HistoryEntry, error)

	// NewSession clears the active session so the next RunTurn creates a new one.
	NewSession()

	// Close releases resources (sandbox cleanup, HTTP clients, etc.).
	Close() error
}
