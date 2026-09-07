package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// SessionEntry maps a session to its container. This is the legacy in-memory
// view retained for backward compatibility with the many call sites that
// consume it directly (cmd/astonish/sandbox.go, pkg/api/*, pkg/fleet/*,
// pkg/tools/*). Internally the registry persists via store.SandboxSessionStore
// which is populated from either a filestore (personal mode) or a pgstore
// (platform mode). The SessionEntry.TemplateName field maps to
// store.SandboxSession.TemplateID; in personal mode the ID is the slug.
type SessionEntry struct {
	SessionID     string    `json:"session_id"`
	ContainerName string    `json:"container_name"`
	TemplateName  string    `json:"template_name"`
	CreatedAt     time.Time `json:"created_at"`
	ExposedPorts  []int     `json:"exposed_ports,omitempty"`
	BaseDomain    string    `json:"base_domain,omitempty"`
	Pinned        bool      `json:"pinned,omitempty"`
}

// SessionRegistry maps session IDs to container names. Persistence is
// delegated to a store.SandboxSessionStore; this type provides the
// higher-level semantics (prefix resolve, reap-against-live-containers,
// port/domain/pin bookkeeping by container name) that the rest of the
// sandbox code depends on.
//
// Personal mode: constructed via NewSessionRegistry(), which wraps a
// filestore-backed store over ~/.local/share/astonish/sandbox.
//
// Platform mode (Phase C+): construct via NewSessionRegistryFromStore(...)
// passing a pgstore-backed store.
//
// SessionRegistry is safe for concurrent use: all mutation and lookup
// methods delegate to the underlying store, which has its own concurrency
// control (filestore: a per-file mutex; pgstore: PostgreSQL transactions).
type SessionRegistry struct {
	store store.SandboxSessionStore
}

// NewSessionRegistry creates the personal-mode registry. It wires a
// filestore-backed store rooted at the same data directory that the legacy
// sessions.json lived in; if a legacy sessions.json exists in that dir, it
// is migrated into sandbox_sessions.json on first construction so the new
// code sees the same sessions as the old registry.
func NewSessionRegistry() (*SessionRegistry, error) {
	dataDir, err := sandboxDataDir()
	if err != nil {
		return nil, err
	}
	// Intentionally avoid an import cycle with pkg/store/filestore by
	// constructing the store through a small shim callers provide. The
	// shim lives in sandbox/registry_personal.go to keep this file free of
	// the filestore import; the default shim is installed by an init() in
	// that file.
	st, err := newDefaultSessionStore(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to init sandbox session store: %w", err)
	}
	r := &SessionRegistry{store: st}

	// Best-effort legacy import: if a sessions.json exists alongside
	// sandbox_sessions.json and the new store is empty, copy the old
	// entries over. This is idempotent for repeated process starts
	// because subsequent runs find rows already populated.
	if err := r.importLegacyIfNeeded(dataDir); err != nil {
		slog.Warn("sandbox session registry: legacy import failed", "component", "sandbox", "error", err)
	}
	return r, nil
}

// NewSessionRegistryFromStore wraps an arbitrary SandboxSessionStore. Used
// by platform-mode wiring (pgstore) and tests.
func NewSessionRegistryFromStore(st store.SandboxSessionStore) *SessionRegistry {
	return &SessionRegistry{store: st}
}

// newDefaultSessionStore is the shim populated by registry_personal.go.
// It exists so this file can stay free of pkg/store/filestore imports.
var newDefaultSessionStore = func(dataDir string) (store.SandboxSessionStore, error) {
	return nil, fmt.Errorf("sandbox: default session store factory not installed (missing pkg/sandbox init?)")
}

// RegisterDefaultSessionStoreFactory installs the default factory used by
// NewSessionRegistry. The personal-mode wiring in registry_personal.go
// invokes this in its init(); platform-mode callers do not need to use it.
func RegisterDefaultSessionStoreFactory(factory func(dataDir string) (store.SandboxSessionStore, error)) {
	if factory == nil {
		return
	}
	newDefaultSessionStore = factory
}

// --------------------------------------------------------------------------
// Legacy JSON import
// --------------------------------------------------------------------------

// importLegacyIfNeeded reads sessions.json from dataDir (if it exists) and
// copies any entries into the backing store that aren't already present.
// This supports a smooth upgrade: a first run after deploying the new
// SessionRegistry picks up the existing session list instead of starting
// from scratch. The legacy file is left in place (not deleted) so that
// downgrades remain possible while the cutover is rolled out.
func (r *SessionRegistry) importLegacyIfNeeded(dataDir string) error {
	legacy := legacySessionsJSONPath(dataDir)
	info, err := os.Stat(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return err
	}
	var legacyEntries []*SessionEntry
	if err := jsonUnmarshalPreserveEmpty(data, &legacyEntries); err != nil {
		return fmt.Errorf("parse legacy sessions.json: %w", err)
	}
	ctx := context.Background()
	for _, e := range legacyEntries {
		if e == nil || e.SessionID == "" {
			continue
		}
		existing, err := r.store.Get(ctx, e.SessionID)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		sess := entryToSession(e)
		if err := r.store.Put(ctx, sess); err != nil {
			return fmt.Errorf("import legacy session %s: %w", e.SessionID, err)
		}
	}
	return nil
}

// --------------------------------------------------------------------------
// Public API (preserved from the pre-cutover implementation)
// --------------------------------------------------------------------------

// Put registers a session-to-container mapping.
func (r *SessionRegistry) Put(sessionID, containerName, templateName string) error {
	if sessionID == "" {
		return fmt.Errorf("session ID is required")
	}
	ctx := context.Background()
	sess := &store.SandboxSession{
		SessionID:     sessionID,
		ChatSessionID: sessionID,
		Backend:       "incus",
		ContainerName: containerName,
		TemplateID:    templateName,
		State:         store.SandboxSessionStateRunning,
	}
	return r.store.Put(ctx, sess)
}

// PutSession inserts or replaces a full session record. Unlike Put, which
// presumes the Incus backend and builds a minimal SandboxSession for the
// legacy call sites, PutSession accepts a fully-populated *store.SandboxSession
// so that backends (e.g., K8sBackend) can record backend-specific fields
// like PodName, NodeName, and the non-incus Backend tag.
//
// The caller is responsible for setting SessionID (required) and Backend
// ("incus" | "k8s"); empty values of CreatedAt/UpdatedAt/LastActiveAt are
// populated by the underlying store.
func (r *SessionRegistry) PutSession(sess *store.SandboxSession) error {
	if sess == nil {
		return fmt.Errorf("session is required")
	}
	if sess.SessionID == "" {
		return fmt.Errorf("session ID is required")
	}
	if sess.ChatSessionID == "" {
		sess.ChatSessionID = sess.SessionID
	}
	if sess.Backend == "" {
		return fmt.Errorf("session.Backend is required")
	}
	if sess.State == "" {
		sess.State = store.SandboxSessionStateRunning
	}
	return r.store.Put(context.Background(), sess)
}

// GetSession returns the full store.SandboxSession by ID, or nil if absent.
// This is a lower-level accessor than Get (which returns a legacy SessionEntry
// view). Backends that need backend-specific fields (PodName, Backend,
// UpperLayerID) should use GetSession.
func (r *SessionRegistry) GetSession(sessionID string) (*store.SandboxSession, error) {
	return r.store.Get(context.Background(), sessionID)
}

// Get returns the session entry, or nil if not found.
func (r *SessionRegistry) Get(sessionID string) *SessionEntry {
	ctx := context.Background()
	sess, err := r.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return nil
	}
	return sessionToEntry(sess)
}

// GetContainerName returns the container name for a session, or empty string.
func (r *SessionRegistry) GetContainerName(sessionID string) string {
	if e := r.Get(sessionID); e != nil {
		return e.ContainerName
	}
	return ""
}

// Remove deletes a session-to-container mapping.
func (r *SessionRegistry) Remove(sessionID string) error {
	return r.store.Delete(context.Background(), sessionID)
}

// List returns all session entries.
func (r *SessionRegistry) List() []*SessionEntry {
	ctx := context.Background()
	rows, err := r.store.List(ctx, store.SandboxSessionFilter{})
	if err != nil {
		slog.Warn("sandbox session registry: list failed", "component", "sandbox", "error", err)
		return nil
	}
	out := make([]*SessionEntry, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionToEntry(s))
	}
	return out
}

// SessionIDs returns all registered session IDs.
func (r *SessionRegistry) SessionIDs() []string {
	entries := r.List()
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.SessionID)
	}
	return ids
}

// ResolveSessionID resolves a user-provided identifier to a session ID.
// Accepts: exact session ID, container name (via the store's reverse-lookup),
// session ID prefix, or container name prefix.
func (r *SessionRegistry) ResolveSessionID(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	ctx := context.Background()

	// Exact session ID
	if sess, _ := r.store.Get(ctx, input); sess != nil {
		return sess.SessionID, true
	}
	// Exact container name (indexed in the store)
	if sess, _ := r.store.GetByContainerName(ctx, input); sess != nil {
		return sess.SessionID, true
	}
	// Prefix match — scan the list.
	entries := r.List()
	for _, e := range entries {
		if strings.HasPrefix(e.SessionID, input) {
			return e.SessionID, true
		}
		if strings.HasPrefix(e.ContainerName, input) {
			return e.SessionID, true
		}
	}
	return "", false
}

// GetByContainerName returns the session entry for a given container name, or nil.
func (r *SessionRegistry) GetByContainerName(containerName string) *SessionEntry {
	if containerName == "" {
		return nil
	}
	sess, err := r.store.GetByContainerName(context.Background(), containerName)
	if err != nil || sess == nil {
		return nil
	}
	return sessionToEntry(sess)
}

// ExposePort adds a port to the exposed ports list for a container.
// Returns true if the port was newly added, false if already exposed.
func (r *SessionRegistry) ExposePort(containerName string, port int) (bool, error) {
	ctx := context.Background()
	sess, err := r.store.GetByContainerName(ctx, containerName)
	if err != nil {
		return false, err
	}
	if sess == nil {
		return false, fmt.Errorf("container %q not found in registry", containerName)
	}
	for _, p := range sess.ExposedPorts {
		if p == port {
			return false, nil
		}
	}
	newPorts := append([]int(nil), sess.ExposedPorts...)
	newPorts = append(newPorts, port)
	if err := r.store.UpdatePorts(ctx, sess.SessionID, newPorts); err != nil {
		return false, err
	}
	return true, nil
}

// UnexposePort removes a port from the exposed ports list for a container.
// Returns true if the port was found and removed, false if it was not exposed.
func (r *SessionRegistry) UnexposePort(containerName string, port int) (bool, error) {
	ctx := context.Background()
	sess, err := r.store.GetByContainerName(ctx, containerName)
	if err != nil {
		return false, err
	}
	if sess == nil {
		return false, fmt.Errorf("container %q not found in registry", containerName)
	}
	newPorts := make([]int, 0, len(sess.ExposedPorts))
	found := false
	for _, p := range sess.ExposedPorts {
		if p == port {
			found = true
			continue
		}
		newPorts = append(newPorts, p)
	}
	if !found {
		return false, nil
	}
	if err := r.store.UpdatePorts(ctx, sess.SessionID, newPorts); err != nil {
		return false, err
	}
	return true, nil
}

// IsPortExposed checks if a specific port is exposed on a container.
func (r *SessionRegistry) IsPortExposed(containerName string, port int) bool {
	if e := r.GetByContainerName(containerName); e != nil {
		for _, p := range e.ExposedPorts {
			if p == port {
				return true
			}
		}
	}
	return false
}

// SetBaseDomain sets the base domain for a container's session entry.
func (r *SessionRegistry) SetBaseDomain(containerName, baseDomain string) error {
	ctx := context.Background()
	sess, err := r.store.GetByContainerName(ctx, containerName)
	if err != nil {
		return err
	}
	if sess == nil {
		return fmt.Errorf("container %q not found in registry", containerName)
	}
	return r.store.SetBaseDomain(ctx, sess.SessionID, baseDomain)
}

// GetBaseDomain returns the stored base domain for a container.
func (r *SessionRegistry) GetBaseDomain(containerName string) string {
	if e := r.GetByContainerName(containerName); e != nil {
		return e.BaseDomain
	}
	return ""
}

// SetPinned marks a container as pinned (exempt from orphan cleanup).
func (r *SessionRegistry) SetPinned(containerName string, pinned bool) error {
	ctx := context.Background()
	sess, err := r.store.GetByContainerName(ctx, containerName)
	if err != nil {
		return err
	}
	if sess == nil {
		return fmt.Errorf("container %q not found in registry", containerName)
	}
	return r.store.SetPinned(ctx, sess.SessionID, pinned)
}

// TouchActivity bumps the LastActiveAt timestamp for the given session.
// Called by the OpenShell backend on every exec/push/pull to drive the idle
// timeout watchdog.
func (r *SessionRegistry) TouchActivity(sessionID string) {
	_ = r.store.TouchActivity(context.Background(), sessionID)
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func sessionToEntry(s *store.SandboxSession) *SessionEntry {
	if s == nil {
		return nil
	}
	ports := append([]int(nil), s.ExposedPorts...)
	return &SessionEntry{
		SessionID:     s.SessionID,
		ContainerName: s.ContainerName,
		TemplateName:  s.TemplateID,
		CreatedAt:     s.CreatedAt,
		ExposedPorts:  ports,
		BaseDomain:    s.BaseDomain,
		Pinned:        s.Pinned,
	}
}

func entryToSession(e *SessionEntry) *store.SandboxSession {
	if e == nil {
		return nil
	}
	ports := append([]int(nil), e.ExposedPorts...)
	return &store.SandboxSession{
		SessionID:     e.SessionID,
		ChatSessionID: e.SessionID,
		Backend:       "incus",
		ContainerName: e.ContainerName,
		TemplateID:    e.TemplateName,
		State:         store.SandboxSessionStateRunning,
		ExposedPorts:  ports,
		BaseDomain:    e.BaseDomain,
		Pinned:        e.Pinned,
		CreatedAt:     e.CreatedAt,
	}
}
