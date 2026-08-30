package agent

import (
	"context"
	"math"
	"sort"
	"sync"
)

// inMemoryToolVectorStore is a simple in-memory ToolVectorStore for tests.
// It stores documents and performs brute-force cosine similarity search.
type inMemoryToolVectorStore struct {
	mu            sync.RWMutex
	docs          []ToolVectorDoc
	embeddings    [][]float32
	embeddingFunc EmbedFunc
}

// NewInMemoryToolVectorStore creates a ToolVectorStore backed by an in-memory
// slice. It uses the provided embedding function to embed documents on add
// and performs brute-force cosine similarity search on query.
func NewInMemoryToolVectorStore(embeddingFunc EmbedFunc) (ToolVectorStore, error) {
	if embeddingFunc == nil {
		return nil, ErrNilEmbedFunc
	}
	return &inMemoryToolVectorStore{embeddingFunc: embeddingFunc}, nil
}

func (s *inMemoryToolVectorStore) AddDocuments(ctx context.Context, docs []ToolVectorDoc, concurrency int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(docs) {
		concurrency = len(docs)
	}

	type embedResult struct {
		embedding []float32
		err       error
	}
	results := make([]embedResult, len(docs))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, d := range docs {
		wg.Add(1)
		go func(idx int, doc ToolVectorDoc) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-workCtx.Done():
				results[idx].err = workCtx.Err()
				return
			}
			if len(doc.Embedding) > 0 {
				results[idx].embedding = append([]float32(nil), doc.Embedding...)
				return
			}
			emb, err := s.embeddingFunc(workCtx, doc.Content)
			results[idx] = embedResult{embedding: emb, err: err}
			if err != nil {
				cancel()
			}
		}(i, d)
	}
	wg.Wait()
	for _, result := range results {
		if result.err != nil {
			return result.err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	positions := make(map[string]int, len(s.docs))
	for i, doc := range s.docs {
		positions[doc.ID] = i
	}
	for i, doc := range docs {
		doc.Embedding = nil
		emb := append([]float32(nil), results[i].embedding...)
		if pos, ok := positions[doc.ID]; ok {
			s.docs[pos] = doc
			s.embeddings[pos] = emb
			continue
		}
		positions[doc.ID] = len(s.docs)
		s.docs = append(s.docs, doc)
		s.embeddings = append(s.embeddings, emb)
	}
	return nil
}

func (s *inMemoryToolVectorStore) QueryByEmbedding(ctx context.Context, queryEmbedding []float32, topK int) ([]ToolVectorResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.docs) == 0 || topK <= 0 {
		return nil, nil
	}
	type scored struct {
		idx int
		sim float32
	}
	scores := make([]scored, 0, len(s.docs))
	for i, emb := range s.embeddings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scores = append(scores, scored{idx: i, sim: cosineSimilarity(queryEmbedding, emb)})
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].sim > scores[j].sim })
	if topK > len(scores) {
		topK = len(scores)
	}
	results := make([]ToolVectorResult, topK)
	for i := 0; i < topK; i++ {
		results[i] = ToolVectorResult{ToolVectorDoc: s.docs[scores[i].idx], Similarity: scores[i].sim}
	}
	return results, nil
}

func (s *inMemoryToolVectorStore) GetByID(ctx context.Context, id string) (*ToolVectorDoc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.docs {
		if d.ID == id {
			copy := d
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *inMemoryToolVectorStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.docs)
}

func (s *inMemoryToolVectorStore) EmbeddingDimension() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.embeddings) == 0 {
		return 0
	}
	return len(s.embeddings[0])
}

func (s *inMemoryToolVectorStore) AllIDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, len(s.docs))
	for i, d := range s.docs {
		ids[i] = d.ID
	}
	return ids, nil
}

func (s *inMemoryToolVectorStore) DeleteByIDs(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	toDelete := make(map[string]bool, len(ids))
	for _, id := range ids {
		toDelete[id] = true
	}
	n := 0
	for i, d := range s.docs {
		if !toDelete[d.ID] {
			s.docs[n] = s.docs[i]
			s.embeddings[n] = s.embeddings[i]
			n++
		}
	}
	s.docs = s.docs[:n]
	s.embeddings = s.embeddings[:n]
	return nil
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
