package store

import (
	"context"
	"encoding/json"
	"time"

	adksession "google.golang.org/adk/session"
)

// MaxSessionCacheDiagnostics bounds preparation and provider diagnostic records per session.
const MaxSessionCacheDiagnostics = 100

// SessionMeta contains metadata about a chat session.
// This mirrors the existing session.SessionMeta type.

// CacheDiagnostic is a bounded, sanitized record of one model request.
type CacheDiagnostic struct {
	InvocationID         string               `json:"invocationId"`
	Kind                 string               `json:"kind"`
	Stage                string               `json:"stage"`
	Status               string               `json:"status"`
	Call                 int                  `json:"call"`
	Stream               bool                 `json:"stream"`
	Provider             string               `json:"provider,omitempty"`
	Model                string               `json:"model,omitempty"`
	CaptureLevel         string               `json:"captureLevel"`
	InputHash            string               `json:"inputHash"`
	Elements             []ModelInputElement  `json:"elements"`
	Payload              json.RawMessage      `json:"payload,omitempty"`
	PayloadOriginalBytes int                  `json:"payloadOriginalBytes"`
	PayloadCapturedBytes int                  `json:"payloadCapturedBytes"`
	PayloadTruncated     bool                 `json:"payloadTruncated"`
	BinaryElisions       int                  `json:"binaryElisions"`
	StablePrefixElements int                  `json:"stablePrefixElements"`
	StablePrefixBytes    int                  `json:"stablePrefixBytes"`
	FirstDivergence      string               `json:"firstDivergence,omitempty"`
	StartedAt            time.Time            `json:"startedAt"`
	TimeToFirstResponse  time.Duration        `json:"timeToFirstResponse"`
	Duration             time.Duration        `json:"duration"`
	ResponseCount        int                  `json:"responseCount"`
	Usage                CacheDiagnosticUsage `json:"usage"`
	Error                string               `json:"error,omitempty"`
	CreatedAt            time.Time            `json:"createdAt"`
}

type ModelInputElement struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Bytes int    `json:"bytes"`
}

type CacheDiagnosticUsage struct {
	Reported         bool  `json:"reported"`
	CacheReported    bool  `json:"cacheReported"`
	PromptTokens     int32 `json:"promptTokens"`
	CachedTokens     int32 `json:"cachedTokens"`
	CacheWriteTokens int32 `json:"cacheWriteTokens"`
	CandidateTokens  int32 `json:"candidateTokens"`
	ThoughtTokens    int32 `json:"thoughtTokens"`
	ToolUseTokens    int32 `json:"toolUseTokens"`
	TotalTokens      int32 `json:"totalTokens"`
}

type SessionMeta struct {
	ID           string    `json:"id"`
	AppName      string    `json:"appName"`
	UserID       string    `json:"userId"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Title        string    `json:"title,omitempty"`
	MessageCount int       `json:"messageCount"`
	ParentID     string    `json:"parentId,omitempty"`
	FleetKey     string    `json:"fleetKey,omitempty"`
	FleetName    string    `json:"fleetName,omitempty"`
	IssueNumber  int       `json:"issueNumber,omitempty"`
	Repo         string    `json:"repo,omitempty"`
	WorkspaceDir string    `json:"workspaceDir,omitempty"`
}

// SessionStore manages chat sessions and their events.
//
// It embeds the ADK session.Service interface for compatibility with the
// ADK runner, and adds Astonish-specific operations for session metadata,
// transcripts, and lifecycle management.
type SessionStore interface {
	// ADK session.Service — required for the ADK runner.
	adksession.Service

	// Metadata operations.
	ListSessionMetas(ctx context.Context, appName, userID string) ([]SessionMeta, error)
	GetSessionMeta(ctx context.Context, sessionID string) (*SessionMeta, error)
	SetSessionTitle(ctx context.Context, sessionID, title string) error
	ListChildren(ctx context.Context, parentID string) ([]SessionMeta, error)
	AddSessionMeta(ctx context.Context, meta SessionMeta) error
	UpdateSessionMeta(ctx context.Context, sessionID string, fn func(*SessionMeta)) error
	RemoveSessionMeta(ctx context.Context, sessionID string) error

	// Transcript access.
	ReadTranscriptEvents(ctx context.Context, appName, userID, sessionID string) ([]*adksession.Event, error)

	// Cache diagnostics are bounded per session and contain only sanitized request content.
	AppendCacheDiagnostic(ctx context.Context, sessionID string, diagnostic CacheDiagnostic) error
	ListCacheDiagnostics(ctx context.Context, sessionID string) ([]CacheDiagnostic, error)

	// AppendFleetEvent persists a fleet message event to a session's transcript
	// without requiring a full ADK session object. Used by fleet sessions which
	// manage their own message loop outside the ADK runner.
	AppendFleetEvent(ctx context.Context, sessionID string, event *adksession.Event) error

	// Partial ID resolution.
	ResolveSessionID(ctx context.Context, partial string) (string, error)

	// Session lifecycle.
	AllSessionIDs(ctx context.Context) map[string]bool
	CleanupExpiredSessions(ctx context.Context, maxAgeDays int) []string
	RedactSession(ctx context.Context, appName, userID, sessionID string) error

	// SetRedactFunc sets the function used to redact sensitive content in session transcripts.
	SetRedactFunc(fn func(string) string)
}
