package daemon

import (
	"context"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
)

// platformDB is the daemon-level interface for the platform database backend.
// It embeds store.PlatformBackend (which covers auth, tenant routing, settings,
// embeddings, secrets, sandbox layers, migrations, and cleanup) and adds factory
// methods for higher-level stores that require external package types.
//
// Both pgstore.PGStore and sqlitestore.SQLiteStore satisfy this interface.
// Using a single interface variable in Run() eliminates all "if pgStore != nil"
// / "else if sqlStore != nil" branching — the two backends are interchangeable.
type platformDB interface {
	store.PlatformBackend

	// AllChatSessionIDs returns a complete cross-tenant chat-session snapshot
	// for platform background cleanup. Partial results must be returned as errors.
	AllChatSessionIDs(ctx context.Context) (map[string]bool, error)

	// NewToolVectorStore creates a ToolVectorStore for semantic tool discovery.
	// PG: uses pgvector extension. SQLite: BLOB embeddings + brute-force cosine.
	// Returns (nil, nil) if the embedding function is not configured.
	NewToolVectorStore(ctx context.Context) (agent.ToolVectorStore, error)

	// NewThreadIndex creates a thread indexer for email session routing.
	// PG: backed by email_thread_index table. SQLite: same table in platform.db.
	NewThreadIndex() session.ThreadIndexer

	// NewLinkCodeStore creates a link code store for channel verification.
	// PG: backed by pending_link_codes table. SQLite: same table in platform.db.
	NewLinkCodeStore() store.LinkCodeStore

	// NewMonitorStateStore creates a monitor state store for fleet plan monitors.
	// Scoped to a specific org+team for state isolation.
	NewMonitorStateStore(orgSlug, teamSlug string) fleet.MonitorStateStore
}
