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
	// Kind: user | agent | tool_call | tool_result | system | thinking | artifact
	Kind     string
	Text     string
	ToolName string
	ToolID   string
	Args     map[string]any
	Result   any
	Artifact *events.Artifact
}

// Attachment is a file/image payload to send with a chat turn.
type Attachment struct {
	Filename string
	MimeType string
	// Data is raw file bytes (not base64).
	Data []byte
}

// ArtifactContent is file content loaded for the terminal artifact viewer.
type ArtifactContent struct {
	Path    string
	Content string
}

// TurnOptions configures per-turn behavior for the platform request.
type TurnOptions struct {
	// SystemContext is a per-turn hidden instruction sent to Studio chat. It is
	// not persisted as a visible user message.
	SystemContext string
	// PlanMode, when true, requests the runtime plan-mode gate for this turn:
	// mutating tools and delegate_tasks are refused server-side. Callers should
	// also set SystemContext to the plan-mode prompt so the model produces a plan.
	PlanMode bool
	// Attachments are optional multimodal file/image payloads for this turn.
	Attachments []Attachment
}

// NetworkGrantBackend is implemented by platform backends that can resolve
// sandbox network-denial prompts without sending a normal chat approval message.
type NetworkGrantBackend interface {
	ApproveNetworkGrant(ctx context.Context, sessionID string, denial events.NetworkDenial, broader bool, sandboxName string) error
	DenyNetworkGrant(ctx context.Context, sessionID string, denial events.NetworkDenial, sandboxName string) error
}

// ProviderInstance is one configured provider entry shown in the /provider
// manager overlay.
type ProviderInstance struct {
	Name string // instance name (config key)
	Type string // provider type id (e.g. "openai")
}

// ProviderField describes one input a provider type requires when being added
// through the /provider overlay.
type ProviderField struct {
	Key      string // config key (e.g. "api_key", "base_url")
	Label    string // human label shown in the form
	Secret   bool   // render the input masked
	Default  string // pre-filled default value
	Optional bool   // may be left blank
}

// ProviderTypeInfo is one selectable provider type in the /provider overlay.
type ProviderTypeInfo struct {
	ID          string
	DisplayName string
	Fields      []ProviderField
}

// ProviderAdminBackend is an optional capability implemented by backends that
// can manage provider configuration locally (code mode). It is intentionally
// separate from Backend so the platform backend — which manages providers in
// its database — is not required to implement it. The /provider overlay is
// only offered when the active backend implements this interface.
//
// Implementations persist to the local config file only; they must not touch
// any platform database.
type ProviderAdminBackend interface {
	// ListProviderInstances returns the currently configured provider instances.
	ListProviderInstances(ctx context.Context) ([]ProviderInstance, error)
	// ProviderTypes returns the catalog of provider types that can be added,
	// each with the input fields it requires.
	ProviderTypes() []ProviderTypeInfo
	// AddProvider adds (or updates) a provider instance from the given fields
	// and persists it to the config file. On success the provider is usable
	// immediately in the running process.
	AddProvider(ctx context.Context, name, typeID string, fields map[string]string) error
	// RemoveProvider deletes a provider instance and persists the change.
	RemoveProvider(ctx context.Context, name string) error
}

// RollbackPoint is one selectable "revert to here" target in the /rollback
// picker. Each point corresponds to a user message (turn) in the session.
type RollbackPoint struct {
	// ID uniquely identifies the point to RollbackTo (opaque to the TUI).
	ID string
	// Label is a short, single-line preview of the user message at this turn.
	Label string
	// Timestamp is a human-readable time the message was sent (may be empty).
	Timestamp string
	// FileCount is how many files would be restored if rolling back to here.
	FileCount int
	// TurnNumber is the 1-based ordinal of the user message for display.
	TurnNumber int
}

// RollbackBackend is an optional capability implemented by backends that can
// revert both the conversation and the working-directory file changes to an
// earlier user message. It is intentionally separate from Backend so the
// platform/Studio backend — which has no host filesystem to snapshot — is not
// required to implement it. The /rollback command is only offered when the
// active backend implements this interface (code mode).
type RollbackBackend interface {
	// ListRollbackPoints returns the selectable revert targets for the active
	// session, oldest first. Empty when there is nothing to roll back to.
	ListRollbackPoints(ctx context.Context) ([]RollbackPoint, error)
	// RollbackTo reverts the conversation and file changes to the given point,
	// returning the rebuilt history for the truncated session.
	RollbackTo(ctx context.Context, pointID string) ([]HistoryEntry, error)
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

	// DeleteSession deletes a saved session by ID.
	DeleteSession(ctx context.Context, sessionID string) error

	// NewSession clears the active session so the next RunTurn creates a new one.
	NewSession()

	// ListProviders returns configured provider instance names for model selection.
	ListProviders(ctx context.Context) ([]string, error)

	// ListModels returns model IDs available for a provider instance.
	ListModels(ctx context.Context, provider string) ([]string, error)

	// SetModelPin pins provider/model on the active session (or stores a pending
	// pin for the next new session). Empty provider and model clear the pin.
	// Returns the effective provider/model after the change.
	SetModelPin(ctx context.Context, provider, model string) (effectiveProvider, effectiveModel string, err error)

	// ReadArtifactContent loads generated file content for the artifact viewer.
	ReadArtifactContent(ctx context.Context, sessionID, path string) (ArtifactContent, error)

	// Close releases resources (sandbox cleanup, HTTP clients, etc.).
	Close() error
}
