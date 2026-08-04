package launcher

import (
	"context"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
	"github.com/SAP/astonish/pkg/store"
)

func TestNewBackendPDFResolveFunc_ExistingSessionUsesSessionIDHandle(t *testing.T) {
	st := newMemorySandboxSessionStore()
	reg := sandbox.NewSessionRegistryFromStore(st)
	if err := reg.PutSession(&store.SandboxSession{
		SessionID:     "sess-existing",
		ChatSessionID: "sess-existing",
		Backend:       string(sandbox.BackendKindK8s),
		TemplateID:    sandbox.BaseTemplateID,
		State:         store.SandboxSessionStateRunning,
		PodName:       "astn-sess-existing",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	backend := mock.New()

	resolve := newBackendPDFResolveFunc(backend, reg)
	name, ip, err := resolve("sess-existing")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if name != "sess-existing" || ip != "127.0.0.1" {
		t.Fatalf("resolve returned (%q, %q), want (sess-existing, 127.0.0.1)", name, ip)
	}
	if calls := backend.CreateSessionCalls(); len(calls) != 0 {
		t.Fatalf("CreateSession calls = %d, want 0", len(calls))
	}
}

func TestNewBackendPDFResolveFunc_CreatesMissingSession(t *testing.T) {
	backend := mock.New()
	resolve := newBackendPDFResolveFunc(backend, nil)

	name, ip, err := resolve("sess-new")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if name != "sess-new" || ip != "127.0.0.1" {
		t.Fatalf("resolve returned (%q, %q), want (sess-new, 127.0.0.1)", name, ip)
	}
	createCalls := backend.CreateSessionCalls()
	if len(createCalls) != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", len(createCalls))
	}
	if got := createCalls[0].Spec.SessionID; got != "sess-new" {
		t.Fatalf("CreateSession SessionID = %q, want sess-new", got)
	}
	if got := createCalls[0].Spec.TemplateID; got != sandbox.BaseTemplateID {
		t.Fatalf("CreateSession TemplateID = %q, want %q", got, sandbox.BaseTemplateID)
	}
	if calls := backend.StartSessionCalls(); len(calls) != 1 || calls[0] != "sess-new" {
		t.Fatalf("StartSession calls = %v, want [sess-new]", calls)
	}
}

type memorySandboxSessionStore struct {
	rows map[string]*store.SandboxSession
}

func newMemorySandboxSessionStore() *memorySandboxSessionStore {
	return &memorySandboxSessionStore{rows: make(map[string]*store.SandboxSession)}
}

func (s *memorySandboxSessionStore) Put(_ context.Context, sess *store.SandboxSession) error {
	if sess == nil || sess.SessionID == "" {
		return fmt.Errorf("session is required")
	}
	cp := *sess
	s.rows[sess.SessionID] = &cp
	return nil
}

func (s *memorySandboxSessionStore) Get(_ context.Context, sessionID string) (*store.SandboxSession, error) {
	row := s.rows[sessionID]
	if row == nil {
		return nil, nil
	}
	cp := *row
	return &cp, nil
}

func (s *memorySandboxSessionStore) GetByContainerName(_ context.Context, containerName string) (*store.SandboxSession, error) {
	for _, row := range s.rows {
		if row.ContainerName == containerName {
			cp := *row
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *memorySandboxSessionStore) List(_ context.Context, _ store.SandboxSessionFilter) ([]*store.SandboxSession, error) {
	out := make([]*store.SandboxSession, 0, len(s.rows))
	for _, row := range s.rows {
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (s *memorySandboxSessionStore) Delete(_ context.Context, sessionID string) error {
	delete(s.rows, sessionID)
	return nil
}

func (s *memorySandboxSessionStore) UpdateState(_ context.Context, sessionID string, state store.SandboxSessionState) error {
	row := s.rows[sessionID]
	if row == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	row.State = state
	return nil
}

func (s *memorySandboxSessionStore) UpdatePorts(_ context.Context, sessionID string, ports []int) error {
	row := s.rows[sessionID]
	if row == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	row.ExposedPorts = append([]int(nil), ports...)
	return nil
}

func (s *memorySandboxSessionStore) SetBaseDomain(_ context.Context, sessionID, baseDomain string) error {
	row := s.rows[sessionID]
	if row == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	row.BaseDomain = baseDomain
	return nil
}

func (s *memorySandboxSessionStore) SetPinned(_ context.Context, sessionID string, pinned bool) error {
	row := s.rows[sessionID]
	if row == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	row.Pinned = pinned
	return nil
}

func (s *memorySandboxSessionStore) SetUpperLayer(_ context.Context, sessionID, upperLayerID string) error {
	row := s.rows[sessionID]
	if row == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	row.UpperLayerID = upperLayerID
	return nil
}

func (s *memorySandboxSessionStore) TouchActivity(_ context.Context, sessionID string) error {
	if s.rows[sessionID] == nil {
		return nil
	}
	return nil
}
