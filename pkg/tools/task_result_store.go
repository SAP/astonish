package tools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TaskResultStore provides an in-memory store for full sub-agent task results.
// When delegate_tasks summarizes large results, the full text is stored here
// with a unique ID. The orchestrator LLM can selectively retrieve full results
// via the read_task_result tool when it needs the complete data for synthesis.
type TaskResultStore struct {
	results map[string]*storedResult
	mu      sync.RWMutex
}

type storedResult struct {
	TaskName  string    `json:"task_name"`
	Content   string    `json:"content"`
	Summary   string    `json:"summary"`
	StoredAt  time.Time `json:"stored_at"`
	CharCount int       `json:"char_count"`
}

var (
	globalTaskResultStore *TaskResultStore
	taskResultStoreOnce   sync.Once
)

type taskResultContextKey struct{}

// NewTaskResultStore creates an invocation-owned store with no global goroutine.
func NewTaskResultStore() *TaskResultStore {
	return &TaskResultStore{results: make(map[string]*storedResult)}
}
func WithTaskResultStore(ctx context.Context, s *TaskResultStore) context.Context {
	return context.WithValue(ctx, taskResultContextKey{}, s)
}
func taskResultsFromContext(ctx context.Context) *TaskResultStore {
	if ctx != nil {
		if s, ok := ctx.Value(taskResultContextKey{}).(*TaskResultStore); ok && s != nil {
			return s
		}
	}
	return GetTaskResultStore()
}

// GetTaskResultStore returns the singleton store.
func GetTaskResultStore() *TaskResultStore {
	taskResultStoreOnce.Do(func() {
		globalTaskResultStore = &TaskResultStore{
			results: make(map[string]*storedResult),
		}
		globalTaskResultStore.startCleanup()
	})
	return globalTaskResultStore
}

// Store saves a full task result and returns a unique ID for retrieval.
func (s *TaskResultStore) Store(taskName, content, summary string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("tr_%s_%d", taskName, time.Now().UnixNano())
	s.results[id] = &storedResult{
		TaskName:  taskName,
		Content:   content,
		Summary:   summary,
		StoredAt:  time.Now(),
		CharCount: len(content),
	}
	return id
}

// Get retrieves a stored task result by ID. Returns the content and true if found.
func (s *TaskResultStore) Get(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.results[id]
	if !ok {
		return "", false
	}
	return r.Content, true
}

// GetMeta retrieves metadata about a stored result without the full content.
func (s *TaskResultStore) GetMeta(id string) (taskName string, charCount int, found bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.results[id]
	if !ok {
		return "", 0, false
	}
	return r.TaskName, r.CharCount, true
}

// List returns metadata for all stored results.
func (s *TaskResultStore) List() map[string]map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]map[string]any, len(s.results))
	for id, r := range s.results {
		out[id] = map[string]any{
			"task_name":  r.TaskName,
			"char_count": r.CharCount,
			"summary":    r.Summary,
			"stored_at":  r.StoredAt.Format(time.RFC3339),
		}
	}
	return out
}

// startCleanup runs a background goroutine that removes results older than 30 minutes.
func (s *TaskResultStore) startCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.mu.Lock()
			cutoff := time.Now().Add(-30 * time.Minute)
			for id, r := range s.results {
				if r.StoredAt.Before(cutoff) {
					delete(s.results, id)
				}
			}
			s.mu.Unlock()
		}
	}()
}
