package store

import "context"

// EmbedFunc generates a vector embedding for a text string.
// Returns a float32 slice (e.g., 384-dim for all-MiniLM-L6-v2).
// This is the store-level abstraction; callers can wrap HugotEmbedder
// or any other embedding provider into this type.
type EmbedFunc func(ctx context.Context, text string) ([]float32, error)

// MemoryScope identifies the knowledge tier for a memory entry.
type MemoryScope string

const (
	MemoryScopePersonal MemoryScope = "personal"
	MemoryScopeTeam     MemoryScope = "team"
	MemoryScopeOrg      MemoryScope = "org"
)

// MemoryEntry represents a memory chunk to be stored.
type MemoryEntry struct {
	Content    string         `json:"content"`
	Category   string         `json:"category,omitempty"`
	SourcePath string         `json:"sourcePath,omitempty"`
	Embedding  []float32      `json:"-"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedBy  string         `json:"createdBy,omitempty"` // user ID (for team memories)
	SessionID  string         `json:"sessionId,omitempty"` // session that created this memory
}

// MemorySearchResult represents a single result from memory search.
type MemorySearchResult struct {
	ID        string  `json:"id,omitempty"`
	Path      string  `json:"path"`
	StartLine int     `json:"startLine"`
	EndLine   int     `json:"endLine"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
	Category  string  `json:"category,omitempty"`
	Scope     string  `json:"scope,omitempty"`      // "personal", "team", "org" (multi-tenant)
	CreatedBy string  `json:"created_by,omitempty"` // user ID of the creator
	CreatedAt string  `json:"created_at,omitempty"` // RFC3339 timestamp
	SessionID string  `json:"session_id,omitempty"` // session that created this memory
}

// MemoryStore provides access to the vector + BM25 memory search system.
//
// In platform mode, queries target the appropriate schema (personal/team/org).
type MemoryStore interface {
	// Search performs a hybrid vector + keyword search.
	Search(ctx context.Context, query string, maxResults int, minScore float64) ([]MemorySearchResult, error)

	// SearchByCategory performs a hybrid search filtered by category.
	SearchByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error)

	// Add inserts a memory chunk into the store.
	Add(ctx context.Context, entry MemoryEntry) error

	// Get retrieves a single memory entry by ID.
	// Returns nil, nil if not found.
	Get(ctx context.Context, id string) (*MemorySearchResult, error)

	// Update modifies the content and/or category of an existing memory.
	// Re-generates the embedding if content changes.
	Update(ctx context.Context, id string, content string, category string) error

	// Delete removes a memory chunk by ID.
	Delete(ctx context.Context, id string) error

	// List returns memory chunks, optionally filtered by category.
	// If category is empty, all chunks are returned.
	List(ctx context.Context, category string, limit, offset int) ([]MemorySearchResult, error)

	// ListBySession returns all memory chunks created during a specific session.
	ListBySession(ctx context.Context, sessionID string) ([]MemorySearchResult, error)

	// Count returns the number of indexed memory chunks.
	Count() int

	// Close releases resources held by the memory store.
	Close() error
}

// ThreeTierSearcher can search across personal, team, and org memories
// simultaneously with weighted scoring. Used in platform mode only.
type ThreeTierSearcher interface {
	// SearchAllTiers performs a hybrid search across personal, team, and org
	// memory stores, applying tier-based score weighting:
	//   personal: 1.2x, team: 1.0x, org: 0.8x
	// Results are merged and sorted by weighted score.
	SearchAllTiers(ctx context.Context, query string, maxResults int, minScore float64) ([]MemorySearchResult, error)

	// SearchAllTiersByCategory performs a cross-tier search filtered by category.
	SearchAllTiersByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error)
}

// MemoryManager provides higher-level memory operations: loading the core
// memory file, appending to it, and managing knowledge files.
type MemoryManager interface {
	// Load returns the contents of the core MEMORY.md file.
	Load(ctx context.Context) (string, error)

	// Append adds content to a memory category (or the core memory file).
	Append(ctx context.Context, category, content string, overwrite bool) error

	// EnsureDir creates the memory directory if it doesn't exist.
	EnsureDir(ctx context.Context) error

	// Path returns the base path of the memory directory.
	MemoryPath() string
}
