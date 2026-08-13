package a2a

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskFilter provides filtering options for task listing.
type TaskFilter struct {
	ContextID string
	State     TaskState // empty means all states
	Limit     int
}

// TaskStore manages A2A task lifecycle and persistence.
type TaskStore interface {
	// Create creates a new task owned by the given agent.
	Create(agentID, contextID string) *Task

	// Get returns a task by ID. Returns error if not found.
	Get(taskID string) (*Task, error)

	// GetByAgent returns tasks owned by the given agent, filtered by criteria.
	GetByAgent(agentID string, filter TaskFilter) []*Task

	// UpdateState transitions a task to a new state with an optional message.
	UpdateState(taskID string, state TaskState, msg *Message) error

	// AddArtifact appends an artifact to a task.
	AddArtifact(taskID string, artifact Artifact) error

	// Cancel transitions a task to the canceled state.
	Cancel(taskID string) error

	// SetPushConfig sets the push notification configuration for a task.
	SetPushConfig(taskID string, cfg PushNotificationConfig) error

	// GetPushConfig returns the push notification config for a task, or nil.
	GetPushConfig(taskID string) *PushNotificationConfig

	// DeletePushConfig removes the push notification config for a task.
	DeletePushConfig(taskID string) error
}

// InMemoryTaskStore is a thread-safe in-memory TaskStore implementation.
type InMemoryTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*Task
	ttl   time.Duration
	stop  chan struct{}
}

// NewInMemoryTaskStore creates a new in-memory task store with TTL-based cleanup.
func NewInMemoryTaskStore(ttl time.Duration) *InMemoryTaskStore {
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	s := &InMemoryTaskStore{
		tasks: make(map[string]*Task),
		ttl:   ttl,
		stop:  make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// Close stops the cleanup goroutine.
func (s *InMemoryTaskStore) Close() {
	close(s.stop)
}

func (s *InMemoryTaskStore) Create(agentID, contextID string) *Task {
	now := time.Now()
	task := &Task{
		ID:        uuid.New().String(),
		ContextID: contextID,
		Status: TaskStatus{
			State:     TaskStateSubmitted,
			Timestamp: now,
		},
		AgentID:   agentID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	s.tasks[task.ID] = task
	s.mu.Unlock()
	return task
}

func (s *InMemoryTaskStore) Get(taskID string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return task, nil
}

func (s *InMemoryTaskStore) GetByAgent(agentID string, filter TaskFilter) []*Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*Task
	for _, t := range s.tasks {
		if t.AgentID != agentID {
			continue
		}
		if filter.ContextID != "" && t.ContextID != filter.ContextID {
			continue
		}
		if filter.State != "" && t.Status.State != filter.State {
			continue
		}
		result = append(result, t)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result
}

func (s *InMemoryTaskStore) UpdateState(taskID string, state TaskState, msg *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	if !task.Status.State.ValidTransition(state) {
		return fmt.Errorf("invalid state transition: %s -> %s", task.Status.State, state)
	}
	now := time.Now()
	task.Status = TaskStatus{
		State:     state,
		Message:   msg,
		Timestamp: now,
	}
	if msg != nil {
		task.History = append(task.History, *msg)
	}
	task.UpdatedAt = now
	return nil
}

func (s *InMemoryTaskStore) AddArtifact(taskID string, artifact Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	task.Artifacts = append(task.Artifacts, artifact)
	task.UpdatedAt = time.Now()
	return nil
}

func (s *InMemoryTaskStore) Cancel(taskID string) error {
	return s.UpdateState(taskID, TaskStateCanceled, nil)
}

func (s *InMemoryTaskStore) SetPushConfig(taskID string, cfg PushNotificationConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	task.PushNotificationConfig = &cfg
	return nil
}

func (s *InMemoryTaskStore) GetPushConfig(taskID string) *PushNotificationConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return nil
	}
	return task.PushNotificationConfig
}

func (s *InMemoryTaskStore) DeletePushConfig(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	task.PushNotificationConfig = nil
	return nil
}

func (s *InMemoryTaskStore) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

func (s *InMemoryTaskStore) cleanup() {
	cutoff := time.Now().Add(-s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, task := range s.tasks {
		if task.Status.State.IsTerminal() && task.UpdatedAt.Before(cutoff) {
			delete(s.tasks, id)
		}
	}
}

// Compile-time assertion.
var _ TaskStore = (*InMemoryTaskStore)(nil)
