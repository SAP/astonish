package agent

import (
	"context"
	"errors"
)

var (
	// ErrNilDB is returned when a nil database is provided.
	ErrNilDB = errors.New("database is required")
	// ErrNilEmbedFunc is returned when a nil embedding function is provided.
	ErrNilEmbedFunc = errors.New("embedding function is required")
)

// ToolVectorStore abstracts vector storage for the ToolIndex.
// Implementations:
//   - inMemoryToolVectorStore: in-memory brute-force search (tests/development)
//   - pgToolVectorStore: uses pgvector in PostgreSQL (platform mode)
//
// The ToolIndex uses this interface for storing tool description embeddings
// and performing nearest-neighbor search. The BM25 keyword index remains
// in-memory regardless of the vector store backend.
type toolVectorDimensioner interface {
	EmbeddingDimension() int
}

type ToolVectorStore interface {
	// AddDocuments stores tool documents with their embeddings.
	// The implementation is responsible for generating embeddings from Content.
	// Concurrency hints how many parallel embedding calls to allow.
	AddDocuments(ctx context.Context, docs []ToolVectorDoc, concurrency int) error

	// QueryByEmbedding performs a nearest-neighbor search using a pre-computed
	// query embedding. Returns up to topK results sorted by similarity (descending).
	QueryByEmbedding(ctx context.Context, queryEmbedding []float32, topK int) ([]ToolVectorResult, error)

	// GetByID retrieves a single document by its ID.
	// Returns nil, nil if the document does not exist.
	GetByID(ctx context.Context, id string) (*ToolVectorDoc, error)

	// DeleteByIDs removes documents with the given IDs from the store.
	// IDs that don't exist are silently ignored.
	DeleteByIDs(ctx context.Context, ids []string) error

	// Count returns the number of documents in the store.
	Count() int
}

// ToolVectorDoc represents a tool document in the vector store.
type ToolVectorDoc struct {
	ID        string
	Content   string
	Metadata  map[string]string
	Embedding []float32
}

// ToolVectorResult is a search result from the vector store.
type ToolVectorResult struct {
	ToolVectorDoc
	Similarity float32 // cosine similarity score (0.0 - 1.0)
}

// EmbedFunc generates a vector embedding for a text string.
// Returns a float32 slice (e.g., 384-dim for all-MiniLM-L6-v2).
type EmbedFunc func(ctx context.Context, text string) ([]float32, error)
