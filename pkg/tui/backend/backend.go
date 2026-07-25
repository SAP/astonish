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
	IsResumed bool
	// AutoApprove reflects the session tool-approval mode for footer chrome.
	AutoApprove bool
	// Notices are shown once at startup (startup warnings, etc.).
	Notices []string
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
	// Info returns header metadata (may be called after Open).
	Info() Info

	// Open prepares the session (create or resume). Emits no chat events.
	Open(ctx context.Context) error

	// RunTurn sends a user message (or approval response) and returns a
	// channel of events. The channel is closed when the turn is finished
	// (after KindDone or a terminal error event). The caller must drain it.
	RunTurn(ctx context.Context, message string) (<-chan events.Event, error)

	// Close releases resources (sandbox cleanup, HTTP clients, etc.).
	Close() error
}
