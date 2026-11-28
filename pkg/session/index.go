package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// SessionIndex manages the session metadata index file.
type SessionIndex struct {
	path string
	mu   sync.Mutex
}

// SessionMeta contains metadata about a single session.
type SessionMeta struct {
	ID           string    `json:"id"`
	AppName      string    `json:"appName"`
	UserID       string    `json:"userId"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Title        string    `json:"title,omitempty"`
	MessageCount int       `json:"messageCount"`
	ParentID     string    `json:"parentId,omitempty"`     // Non-empty for sub-agent sessions
	FleetKey     string    `json:"fleetKey,omitempty"`     // Non-empty for fleet sessions
	FleetName    string    `json:"fleetName,omitempty"`    // Human-readable fleet name
	IssueNumber  int       `json:"issueNumber,omitempty"`  // GitHub issue number (if triggered by issue)
	Repo         string    `json:"repo,omitempty"`         // GitHub repo "owner/repo" (if triggered by issue)
	WorkspaceDir string    `json:"workspaceDir,omitempty"` // Per-session workspace directory (for cleanup on delete)
}

// IndexData is the top-level structure of the index file.
type IndexData struct {
	Version  int                    `json:"version"`
	Sessions map[string]SessionMeta `json:"sessions"`
}

// NewSessionIndex creates a SessionIndex at the given path.
func NewSessionIndex(path string) *SessionIndex {
	return &SessionIndex{path: path}
}

// Load reads the index from disk. Returns an empty index if the file doesn't exist.
func (idx *SessionIndex) Load() (*IndexData, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	return idx.loadUnsafe()
}

// loadUnsafe reads the index without locking (caller must hold the lock).
func (idx *SessionIndex) loadUnsafe() (*IndexData, error) {
	data, err := os.ReadFile(idx.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &IndexData{
				Version:  1,
				Sessions: make(map[string]SessionMeta),
			}, nil
		}
		return nil, fmt.Errorf("failed to read index: %w", err)
	}

	var index IndexData
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("failed to parse index: %w", err)
	}

	if index.Sessions == nil {
		index.Sessions = make(map[string]SessionMeta)
	}

	return &index, nil
}

// withWriteLock serializes mutations across instances and processes. The sidecar
// is permanent: locking the index itself would lose protection when atomicWrite
// replaces its inode. Never remove the sidecar after releasing the lock.
func (idx *SessionIndex) withWriteLock(fn func() error) (err error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(idx.path), 0755); err != nil {
		return fmt.Errorf("failed to create index directory: %w", err)
	}
	lock := flock.New(idx.path+".lock", flock.SetFlag(os.O_CREATE|os.O_RDWR), flock.SetPermissions(0600))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("failed to lock session index: %w", err)
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil && err == nil {
			err = fmt.Errorf("failed to unlock session index: %w", unlockErr)
		}
	}()
	return fn()
}

// Save atomically replaces the whole index under the mutation locks. It does not
// merge a previously loaded snapshot; use Add, Update or RemoveSelected for edits.
func (idx *SessionIndex) Save(index *IndexData) error {
	return idx.withWriteLock(func() error { return idx.saveUnsafe(index) })
}

// saveUnsafe writes the index; caller must hold both mutation locks.
func (idx *SessionIndex) saveUnsafe(index *IndexData) error {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize index: %w", err)
	}

	return atomicWrite(idx.path, data, 0644)
}

// Add adds a new session to the index.
func (idx *SessionIndex) Add(meta SessionMeta) error {
	return idx.withWriteLock(func() error {
		index, err := idx.loadUnsafe()
		if err != nil {
			return err
		}

		index.Sessions[meta.ID] = meta
		return idx.saveUnsafe(index)
	})
}

// Remove removes a session and all its child sub-sessions from the index.
func (idx *SessionIndex) Remove(id string) error {
	return idx.withWriteLock(func() error {
		index, err := idx.loadUnsafe()
		if err != nil {
			return err
		}

		// Cascade: remove any sessions whose ParentID matches this session
		for childID, meta := range index.Sessions {
			if meta.ParentID == id {
				delete(index.Sessions, childID)
			}
		}

		delete(index.Sessions, id)
		return idx.saveUnsafe(index)
	})
}

// RemoveSelected removes only the supplied, confirmed IDs from the latest index.
// Unlike Remove, it does not cascade to children created after confirmation.
func (idx *SessionIndex) RemoveSelected(ids []string) error {
	return idx.withWriteLock(func() error {
		index, err := idx.loadUnsafe()
		if err != nil {
			return err
		}
		for _, id := range ids {
			delete(index.Sessions, id)
		}
		return idx.saveUnsafe(index)
	})
}

// Update modifies a session in the index using the provided function.
// The function runs inside the mutation locks and must not call index methods.
func (idx *SessionIndex) Update(id string, fn func(*SessionMeta)) error {
	return idx.withWriteLock(func() error {
		index, err := idx.loadUnsafe()
		if err != nil {
			return err
		}

		meta, ok := index.Sessions[id]
		if !ok {
			return fmt.Errorf("session %s not found in index", id)
		}

		fn(&meta)
		index.Sessions[id] = meta
		return idx.saveUnsafe(index)
	})
}

// Get retrieves metadata for a specific session.
func (idx *SessionIndex) Get(id string) (*SessionMeta, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	index, err := idx.loadUnsafe()
	if err != nil {
		return nil, err
	}

	meta, ok := index.Sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}

	return &meta, nil
}

// List returns all top-level sessions matching the given app name and user ID.
// Sub-sessions (those with a non-empty ParentID) are excluded.
// If userID is empty, all sessions for the app are returned.
func (idx *SessionIndex) List(appName, userID string) ([]SessionMeta, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	index, err := idx.loadUnsafe()
	if err != nil {
		return nil, err
	}

	var result []SessionMeta
	for _, meta := range index.Sessions {
		if meta.ParentID != "" {
			continue // skip sub-sessions
		}
		if meta.AppName != appName {
			continue
		}
		if userID != "" && meta.UserID != userID {
			continue
		}
		result = append(result, meta)
	}

	return result, nil
}

// ListChildren returns all sessions whose ParentID matches the given parent session ID.
func (idx *SessionIndex) ListChildren(parentID string) ([]SessionMeta, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	index, err := idx.loadUnsafe()
	if err != nil {
		return nil, err
	}

	var result []SessionMeta
	for _, meta := range index.Sessions {
		if meta.ParentID == parentID {
			result = append(result, meta)
		}
	}

	return result, nil
}

// AllSessionIDs returns a set of all session IDs (including sub-sessions).
func (idx *SessionIndex) AllSessionIDs() (map[string]bool, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	index, err := idx.loadUnsafe()
	if err != nil {
		return nil, err
	}

	ids := make(map[string]bool, len(index.Sessions))
	for id := range index.Sessions {
		ids[id] = true
	}
	return ids, nil
}
