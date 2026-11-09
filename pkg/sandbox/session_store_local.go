package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// localSessionStore is the personal-mode implementation of
// store.SandboxSessionStore that pkg/sandbox uses directly. It lives here
// (rather than in pkg/store/filestore) to avoid an import cycle:
// pkg/store/filestore transitively imports pkg/sandbox via pkg/fleet +
// pkg/agent, so pkg/sandbox cannot import pkg/store/filestore.
//
// The behavior is intentionally identical to filestore.SandboxSessionStore
// (see pkg/store/filestore/sandbox_sessions.go) so that platform/personal
// code can treat the interface uniformly. The filestore variant is kept for
// direct non-sandbox consumers.
type localSessionStore struct {
	mu       sync.RWMutex
	entries  map[string]*store.SandboxSession
	filePath string
}

// NewLocalSessionStore constructs a file-backed session store rooted at
// dataDir. On first call it loads sandbox_sessions.json if present; a
// missing file is not an error.
func NewLocalSessionStore(dataDir string) (store.SandboxSessionStore, error) {
	return newLocalSessionStore(dataDir)
}

// newLocalSessionStore is the internal implementation.
func newLocalSessionStore(dataDir string) (store.SandboxSessionStore, error) {
	if dataDir == "" {
		return nil, errors.New("sandbox session store: dataDir is required")
	}
	s := &localSessionStore{
		entries:  make(map[string]*store.SandboxSession),
		filePath: filepath.Join(dataDir, "sandbox_sessions.json"),
	}
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sandbox session store: load %s: %w", s.filePath, err)
	}
	return s, nil
}

func (s *localSessionStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var rows []*store.SandboxSession
	if err := json.Unmarshal(data, &rows); err != nil {
		return fmt.Errorf("parse sandbox_sessions.json: %w", err)
	}
	s.entries = make(map[string]*store.SandboxSession, len(rows))
	for _, r := range rows {
		if r == nil || r.SessionID == "" {
			continue
		}
		cp := *r
		if cp.BotHomeBinding != nil {
			binding := *cp.BotHomeBinding
			cp.BotHomeBinding = &binding
		}
		s.entries[cp.SessionID] = &cp
	}
	return nil
}

// saveLocked writes the store to disk atomically (tmp file + rename).
// Caller must hold s.mu (read or write).
func (s *localSessionStore) saveLocked() error {
	rows := make([]*store.SandboxSession, 0, len(s.entries))
	for _, e := range s.entries {
		rows = append(rows, e)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sandbox_sessions.json: %w", err)
	}
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".sandbox_sessions.json.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.filePath)
}

func (s *localSessionStore) Put(_ context.Context, sess *store.SandboxSession) error {
	if sess == nil {
		return errors.New("sandbox session is nil")
	}
	if sess.SessionID == "" {
		return errors.New("sandbox session: SessionID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	cp := *sess
	if cp.BotHomeBinding != nil {
		binding := *cp.BotHomeBinding
		cp.BotHomeBinding = &binding
	}
	if existing, ok := s.entries[cp.SessionID]; ok {
		if !existing.CreatedAt.IsZero() && (cp.CreatedAt.IsZero() || existing.CreatedAt.Before(cp.CreatedAt)) {
			cp.CreatedAt = existing.CreatedAt
		}
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	if cp.LastActiveAt.IsZero() {
		cp.LastActiveAt = now
	}
	cp.UpdatedAt = now
	if cp.Backend == "" {
		cp.Backend = "incus"
	}
	if cp.State == "" {
		cp.State = store.SandboxSessionStateCreating
	}
	if cp.ChatSessionID == "" {
		cp.ChatSessionID = cp.SessionID
	}
	s.entries[cp.SessionID] = &cp
	return s.saveLocked()
}

func (s *localSessionStore) Get(_ context.Context, sessionID string) (*store.SandboxSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return nil, nil
	}
	cp := *e
	if cp.BotHomeBinding != nil {
		binding := *cp.BotHomeBinding
		cp.BotHomeBinding = &binding
	}
	return &cp, nil
}

func (s *localSessionStore) GetByContainerName(_ context.Context, containerName string) (*store.SandboxSession, error) {
	if containerName == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ContainerName == containerName {
			cp := *e
			if cp.BotHomeBinding != nil {
				binding := *cp.BotHomeBinding
				cp.BotHomeBinding = &binding
			}
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *localSessionStore) List(_ context.Context, filter store.SandboxSessionFilter) ([]*store.SandboxSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.SandboxSession, 0, len(s.entries))
	for _, e := range s.entries {
		if filter.State != "" && e.State != filter.State {
			continue
		}
		if filter.CreatedBy != "" && e.CreatedBy != filter.CreatedBy {
			continue
		}
		if filter.Pinned != nil && e.Pinned != *filter.Pinned {
			continue
		}
		if filter.ContainerName != "" && e.ContainerName != filter.ContainerName {
			continue
		}
		cp := *e
		if cp.BotHomeBinding != nil {
			binding := *cp.BotHomeBinding
			cp.BotHomeBinding = &binding
		}
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *localSessionStore) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[sessionID]; !ok {
		return nil
	}
	delete(s.entries, sessionID)
	return s.saveLocked()
}

func (s *localSessionStore) UpdateState(_ context.Context, sessionID string, state store.SandboxSessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	now := time.Now().UTC()
	e.State = state
	e.UpdatedAt = now
	e.LastActiveAt = now
	return s.saveLocked()
}

func (s *localSessionStore) UpdatePorts(_ context.Context, sessionID string, ports []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	if len(ports) == 0 {
		e.ExposedPorts = nil
	} else {
		cp := make([]int, len(ports))
		copy(cp, ports)
		e.ExposedPorts = cp
	}
	e.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

func (s *localSessionStore) SetBaseDomain(_ context.Context, sessionID, baseDomain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	e.BaseDomain = baseDomain
	e.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

func (s *localSessionStore) SetPinned(_ context.Context, sessionID string, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	e.Pinned = pinned
	e.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

func (s *localSessionStore) SetUpperLayer(_ context.Context, sessionID, upperLayerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	e.UpperLayerID = upperLayerID
	e.UpdatedAt = time.Now().UTC()
	return s.saveLocked()
}

func (s *localSessionStore) TouchActivity(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[sessionID]
	if !ok {
		return nil // no-op if absent
	}
	e.LastActiveAt = time.Now().UTC()
	return s.saveLocked()
}

// Compile-time assertion.
var _ store.SandboxSessionStore = (*localSessionStore)(nil)

// Install the default factory for NewSessionRegistry via package init.
// Platform-mode callers bypass this factory via NewSessionRegistryFromStore.
func init() {
	RegisterDefaultSessionStoreFactory(newLocalSessionStore)
}
