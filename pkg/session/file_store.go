package session

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// FileStore implements ADK's session.Service interface with file-based persistence.
// Sessions are stored as JSONL transcript files with a JSON metadata index.
type FileStore struct {
	baseDir     string // e.g. ~/.config/astonish/sessions/
	index       *SessionIndex
	threadIndex *ThreadIndex
	mu          sync.RWMutex
	sessions    map[string]*fileSession // sessionID -> in-memory session (loaded on demand)

	// RedactFunc, if set, sanitizes text before persisting to disk.
	// Used to strip credential values from session transcripts.
	RedactFunc func(string) string

	// Separate state stores mirroring ADK's in-memory service
	appState  map[string]stateMap            // appName -> state
	userState map[string]map[string]stateMap // appName -> userID -> state
}

type stateMap = map[string]any

// StateKeyParentID is the session state key used to pass a parent session ID
// during session creation. FileStore.Create() extracts this key from the
// initial state and stores it in the index metadata, then removes it from state.
const StateKeyParentID = "_astonish_parent_id"

// NewFileStore creates a file-based session store at the given directory.
func NewFileStore(baseDir string) (*FileStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create session directory: %w", err)
	}

	indexPath := filepath.Join(baseDir, "index.json")
	threadIndexPath := filepath.Join(baseDir, "thread_index.json")
	return &FileStore{
		baseDir:     baseDir,
		index:       NewSessionIndex(indexPath),
		threadIndex: NewThreadIndex(threadIndexPath),
		sessions:    make(map[string]*fileSession),
		appState:    make(map[string]stateMap),
		userState:   make(map[string]map[string]stateMap),
	}, nil
}

// Create creates a new session with optional initial state.
func (s *FileStore) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	if req.AppName == "" || req.UserID == "" {
		return nil, fmt.Errorf("app_name and user_id are required, got app_name: %q, user_id: %q", req.AppName, req.UserID)
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicates
	if _, exists := s.sessions[sessionID]; exists {
		return nil, fmt.Errorf("session %s already exists", sessionID)
	}

	now := time.Now()

	// Initialize state
	state := req.State
	if state == nil {
		state = make(stateMap)
	}

	// Extract parent ID from state if present (used for sub-agent sessions)
	var parentID string
	if pid, ok := state[StateKeyParentID].(string); ok && pid != "" {
		parentID = pid
		delete(state, StateKeyParentID)
	}

	// Extract and store scoped state deltas
	appDelta, userDelta, sessionDelta := extractStateDeltas(state)
	appState := s.updateAppState(appDelta, req.AppName)
	userState := s.updateUserState(userDelta, req.AppName, req.UserID)
	mergedState := mergeStates(appState, userState, sessionDelta)
	// Also include any session-level keys from the original state
	for k, v := range state {
		if !strings.HasPrefix(k, adksession.KeyPrefixApp) &&
			!strings.HasPrefix(k, adksession.KeyPrefixUser) &&
			!strings.HasPrefix(k, adksession.KeyPrefixTemp) {
			mergedState[k] = v
		}
	}

	// Create transcript
	transcriptDir := filepath.Join(s.baseDir, req.AppName, req.UserID)
	transcriptPath := filepath.Join(transcriptDir, sessionID+".jsonl")
	transcript := NewTranscript(transcriptPath)
	if err := transcript.WriteHeader(sessionID); err != nil {
		return nil, fmt.Errorf("failed to write transcript header: %w", err)
	}

	// Create in-memory session
	sess := &fileSession{
		id:        sessionID,
		appName:   req.AppName,
		userID:    req.UserID,
		state:     mergedState,
		events:    nil,
		updatedAt: now,
	}
	s.sessions[sessionID] = sess

	// Add to index
	meta := SessionMeta{
		ID:           sessionID,
		AppName:      req.AppName,
		UserID:       req.UserID,
		CreatedAt:    now,
		UpdatedAt:    now,
		MessageCount: 0,
		ParentID:     parentID,
	}
	if err := s.index.Add(meta); err != nil {
		return nil, fmt.Errorf("failed to add session to index: %w", err)
	}

	// Return a copy
	copiedSession := sess.copy()
	return &adksession.CreateResponse{
		Session: copiedSession,
	}, nil
}

// Get retrieves a session by ID, loading from disk if not in memory.
func (s *FileStore) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return nil, fmt.Errorf("app_name, user_id, session_id are required, got app_name: %q, user_id: %q, session_id: %q",
			req.AppName, req.UserID, req.SessionID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[req.SessionID]
	if !ok {
		// Try loading from disk
		loaded, err := s.loadFromDisk(req.AppName, req.UserID, req.SessionID)
		if err != nil {
			return nil, fmt.Errorf("session %s not found: %w", req.SessionID, err)
		}
		s.sessions[req.SessionID] = loaded
		sess = loaded
	}

	// Verify app/user match
	if sess.appName != req.AppName || sess.userID != req.UserID {
		return nil, fmt.Errorf("session %s not found for app %q user %q", req.SessionID, req.AppName, req.UserID)
	}

	// Make a copy with merged state (app + user + session)
	copiedSession := sess.copyWithoutStateAndEvents()
	copiedSession.state = s.mergeStates(sess.state, req.AppName, req.UserID)

	// Apply event filters
	filteredEvents := sess.events
	if req.NumRecentEvents > 0 {
		start := max(len(filteredEvents)-req.NumRecentEvents, 0)
		filteredEvents = filteredEvents[start:]
	}
	if !req.After.IsZero() && len(filteredEvents) > 0 {
		firstIdx := sort.Search(len(filteredEvents), func(i int) bool {
			return !filteredEvents[i].Timestamp.Before(req.After)
		})
		filteredEvents = filteredEvents[firstIdx:]
	}

	copiedSession.events = make([]*adksession.Event, len(filteredEvents))
	copy(copiedSession.events, filteredEvents)

	return &adksession.GetResponse{
		Session: copiedSession,
	}, nil
}

// List lists sessions for an app/user.
func (s *FileStore) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	if req.AppName == "" {
		return nil, fmt.Errorf("app_name is required, got app_name: %q", req.AppName)
	}

	metas, err := s.index.List(req.AppName, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	sessions := make([]adksession.Session, 0, len(metas))
	for _, meta := range metas {
		// Try to get each session (loading from disk if needed)
		s.mu.RLock()
		sess, ok := s.sessions[meta.ID]
		s.mu.RUnlock()

		if !ok {
			s.mu.Lock()
			loaded, err := s.loadFromDisk(meta.AppName, meta.UserID, meta.ID)
			if err != nil {
				s.mu.Unlock()
				continue // skip sessions that can't be loaded
			}
			s.sessions[meta.ID] = loaded
			sess = loaded
			s.mu.Unlock()
		}

		copiedSession := sess.copyWithoutStateAndEvents()
		s.mu.RLock()
		copiedSession.state = s.mergeStates(sess.state, meta.AppName, sess.userID)
		s.mu.RUnlock()
		sessions = append(sessions, copiedSession)
	}

	return &adksession.ListResponse{
		Sessions: sessions,
	}, nil
}

// Delete deletes a session by removing its transcript and index entry.
// If the session has child sub-sessions, they are cascade-deleted as well.
// Thread index entries pointing to this session are also removed.
func (s *FileStore) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if req.AppName == "" || req.UserID == "" || req.SessionID == "" {
		return fmt.Errorf("app_name, user_id, session_id are required, got app_name: %q, user_id: %q, session_id: %q",
			req.AppName, req.UserID, req.SessionID)
	}

	// Collect child session IDs before acquiring the write lock
	children, _ := s.index.ListChildren(req.SessionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove child sessions' transcripts and in-memory cache
	for _, child := range children {
		delete(s.sessions, child.ID)
		transcriptPath := filepath.Join(s.baseDir, child.AppName, child.UserID, child.ID+".jsonl")
		_ = os.Remove(transcriptPath) // best-effort cleanup
	}

	// Remove the parent session from in-memory cache
	delete(s.sessions, req.SessionID)

	// Remove transcript file
	transcriptPath := filepath.Join(s.baseDir, req.AppName, req.UserID, req.SessionID+".jsonl")
	os.Remove(transcriptPath) // ignore error if file doesn't exist

	// Remove thread index entries that point to this session (best-effort)
	if err := s.threadIndex.RemoveSession(req.SessionID); err != nil {
		slog.Warn("failed to clean up thread index", "session", req.SessionID, "error", err)
	}

	// Remove from index (cascades children automatically)
	return s.index.Remove(req.SessionID)
}

// AppendEvent appends an event to the session, persisting it to the transcript.
func (s *FileStore) AppendEvent(ctx context.Context, curSession adksession.Session, event *adksession.Event) error {
	if curSession == nil {
		return fmt.Errorf("session is nil")
	}
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	if event.Partial {
		return nil
	}

	sess, ok := curSession.(*fileSession)
	if !ok {
		return fmt.Errorf("unexpected session type %T (expected *fileSession)", curSession)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Look up the stored session
	storedSession, ok := s.sessions[sess.id]
	if !ok {
		return fmt.Errorf("session not found, cannot apply event")
	}

	// Trim temp state keys from the event
	trimmedEvent := trimTempDeltaState(event)

	// Update the caller's in-memory session (like ADK does)
	if err := sess.appendEvent(trimmedEvent); err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	// Update the stored session
	storedSession.events = append(storedSession.events, event)
	storedSession.updatedAt = event.Timestamp

	// Apply state deltas
	if len(event.Actions.StateDelta) > 0 {
		appDelta, userDelta, sessionDelta := extractStateDeltas(event.Actions.StateDelta)
		s.updateAppState(appDelta, curSession.AppName())
		s.updateUserState(userDelta, curSession.AppName(), curSession.UserID())
		maps.Copy(storedSession.state, sessionDelta)
	}

	// Persist to transcript
	transcriptPath := filepath.Join(s.baseDir, sess.appName, sess.userID, sess.id+".jsonl")
	transcript := NewTranscript(transcriptPath)
	if s.RedactFunc != nil {
		if err := transcript.AppendEventRedacted(event, s.RedactFunc); err != nil {
			return fmt.Errorf("failed to persist event: %w", err)
		}
	} else {
		if err := transcript.AppendEvent(event); err != nil {
			return fmt.Errorf("failed to persist event: %w", err)
		}
	}

	// Update index metadata
	if err := s.index.Update(sess.id, func(meta *SessionMeta) {
		meta.UpdatedAt = event.Timestamp
		meta.MessageCount++
	}); err != nil {
		slog.Warn("failed to update session index metadata", "session_id", sess.id, "error", err)
	}

	return nil
}

// TruncateEvents rewrites a session to keep only its first keepCount events,
// discarding the rest. It updates the in-memory session, the on-disk transcript
// (via Transcript.Rewrite), and the index message count so a resumed session
// reflects the truncation.
//
// This is the sanctioned mechanism behind code-mode /rollback: unlike the
// package's usual "never delete a transcript entry" rule (which protects the
// audit/resume chain during compaction), rollback is an explicit, user-driven
// request to discard later turns. It rewrites the transcript rather than
// silently dropping lines.
//
// keepCount is clamped to [0, len(events)]. Truncating to the full length is a
// no-op. Returns the number of events retained.
func (s *FileStore) TruncateEvents(appName, userID, sessionID string, keepCount int) (int, error) {
	if appName == "" || userID == "" || sessionID == "" {
		return 0, fmt.Errorf("app_name, user_id, session_id are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok {
		loaded, err := s.loadFromDisk(appName, userID, sessionID)
		if err != nil {
			return 0, fmt.Errorf("session %s not found: %w", sessionID, err)
		}
		s.sessions[sessionID] = loaded
		sess = loaded
	}
	if sess.appName != appName || sess.userID != userID {
		return 0, fmt.Errorf("session %s not found for app %q user %q", sessionID, appName, userID)
	}

	if keepCount < 0 {
		keepCount = 0
	}
	if keepCount > len(sess.events) {
		keepCount = len(sess.events)
	}
	if keepCount == len(sess.events) {
		return keepCount, nil
	}

	kept := make([]*adksession.Event, keepCount)
	copy(kept, sess.events[:keepCount])
	sess.events = kept
	if keepCount > 0 {
		sess.updatedAt = kept[keepCount-1].Timestamp
	} else {
		sess.updatedAt = time.Time{}
	}

	// Rewrite the transcript on disk with the retained events, applying the
	// configured redactor so previously redacted content stays redacted.
	transcriptPath := filepath.Join(s.baseDir, appName, userID, sessionID+".jsonl")
	transcript := NewTranscript(transcriptPath)
	if err := transcript.Rewrite(sessionID, kept); err != nil {
		return 0, fmt.Errorf("failed to rewrite transcript: %w", err)
	}
	if s.RedactFunc != nil {
		if err := transcript.RedactTranscript(s.RedactFunc); err != nil {
			return 0, fmt.Errorf("failed to redact truncated transcript: %w", err)
		}
	}

	if err := s.index.Update(sessionID, func(meta *SessionMeta) {
		meta.MessageCount = keepCount
		meta.UpdatedAt = sess.updatedAt
	}); err != nil {
		slog.Warn("failed to update session index after truncate", "session_id", sessionID, "error", err)
	}

	return keepCount, nil
}

// ResolveSessionID resolves a partial session ID (prefix match) to a full ID.
// Returns an error if zero or multiple sessions match.
func (s *FileStore) ResolveSessionID(partial string) (string, error) {
	data, err := s.index.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load index: %w", err)
	}

	// Exact match first
	if _, ok := data.Sessions[partial]; ok {
		return partial, nil
	}

	// Prefix match
	var matches []string
	for id := range data.Sessions {
		if strings.HasPrefix(id, partial) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matching %q", partial)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("ambiguous session ID %q matches %d sessions:\n  %s", partial, len(matches), strings.Join(matches, "\n  "))
	}
}

// ListSessionMetas returns session metadata from the index for an app/user pair.
// This is a lightweight operation that does not load session transcripts from disk.
func (s *FileStore) ListSessionMetas(appName, userID string) ([]SessionMeta, error) {
	return s.index.List(appName, userID)
}

// GetSessionMeta returns metadata for a single session from the index.
func (s *FileStore) GetSessionMeta(sessionID string) (*SessionMeta, error) {
	return s.index.Get(sessionID)
}

// GetSessionTitle returns the title for a session, or empty string if not set.
func (s *FileStore) GetSessionTitle(_ context.Context, sessionID string) (string, error) {
	meta, err := s.index.Get(sessionID)
	if err != nil {
		return "", err
	}
	return meta.Title, nil
}

// SetSessionTitle updates the title in the index for a given session.
func (s *FileStore) SetSessionTitle(_ context.Context, sessionID, title string) error {
	return s.index.Update(sessionID, func(meta *SessionMeta) {
		meta.Title = title
	})
}

// ListChildren returns metadata for all child sessions of the given parent.
func (s *FileStore) ListChildren(parentID string) ([]SessionMeta, error) {
	return s.index.ListChildren(parentID)
}

// AddSessionMeta adds a metadata entry to the session index directly.
// This is used by fleet sessions that need to appear in the session list
// without creating a full ADK session or transcript file.
func (s *FileStore) AddSessionMeta(meta SessionMeta) error {
	return s.index.Add(meta)
}

// UpdateSessionMeta updates an existing session's metadata in the index.
func (s *FileStore) UpdateSessionMeta(sessionID string, fn func(*SessionMeta)) error {
	return s.index.Update(sessionID, fn)
}

// RemoveSessionMeta removes a session's metadata from the index.
func (s *FileStore) RemoveSessionMeta(sessionID string) error {
	return s.index.Remove(sessionID)
}

// BaseDir returns the base directory for session storage.
func (s *FileStore) BaseDir() string {
	return s.baseDir
}

// Index returns the underlying SessionIndex for direct access.
// This is used by the trace API to walk child sessions.
func (s *FileStore) Index() *SessionIndex {
	return s.index
}

// ThreadIndex returns the underlying ThreadIndex for direct access.
// Used by the email channel to map Message-IDs to session keys.
func (s *FileStore) ThreadIndex() *ThreadIndex {
	return s.threadIndex
}

// ReadTranscriptEvents reads all events from a session's transcript file.
// This is a lightweight read that does not load the full session into the cache.
func (s *FileStore) ReadTranscriptEvents(appName, userID, sessionID string) ([]*adksession.Event, error) {
	transcriptPath := filepath.Join(s.baseDir, appName, userID, sessionID+".jsonl")
	transcript := NewTranscript(transcriptPath)
	if !transcript.Exists() {
		return nil, nil
	}
	return transcript.ReadEvents()
}

// loadFromDisk loads a session from its transcript file and index metadata.
// Caller must hold s.mu lock.
func (s *FileStore) loadFromDisk(appName, userID, sessionID string) (*fileSession, error) {
	// Verify session exists in index
	meta, err := s.index.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Load events from transcript
	transcriptPath := filepath.Join(s.baseDir, appName, userID, sessionID+".jsonl")
	transcript := NewTranscript(transcriptPath)
	events, err := transcript.ReadEvents()
	if err != nil {
		return nil, fmt.Errorf("failed to read transcript: %w", err)
	}

	// Sanitize loaded events: strip large binary data (e.g., image_base64
	// from browser screenshots) that would bloat LLM context on replay.
	sanitizeEventsOnLoad(events)

	// Repair orphaned tool calls: if the daemon was killed mid-execution,
	// the last event may contain FunctionCall parts without matching
	// FunctionResponse parts. Providers like OpenAI reject this with HTTP 400.
	// Inject synthetic error responses so the conversation history is valid.
	events = repairOrphanedToolCalls(events)

	// Rebuild state from events
	state := make(stateMap)
	for _, event := range events {
		if event.Actions.StateDelta != nil {
			for k, v := range event.Actions.StateDelta {
				if !strings.HasPrefix(k, adksession.KeyPrefixTemp) {
					state[k] = v
				}
			}
		}
	}

	return &fileSession{
		id:        sessionID,
		appName:   appName,
		userID:    userID,
		state:     state,
		events:    events,
		updatedAt: meta.UpdatedAt,
	}, nil
}

// sanitizeEventsOnLoad strips large binary data from loaded session events
// to prevent bloating the LLM context when sessions are replayed. This is a
// defense-in-depth measure for sessions persisted before the AfterToolCallback
// began stripping image data at source.
func sanitizeEventsOnLoad(events []*adksession.Event) {
	for _, event := range events {
		if event.LLMResponse.Content == nil {
			continue
		}
		for _, part := range event.LLMResponse.Content.Parts {
			if part.FunctionResponse == nil || part.FunctionResponse.Response == nil {
				continue
			}
			resp := part.FunctionResponse.Response
			b64, ok := resp["image_base64"].(string)
			if !ok || len(b64) <= 200 {
				// Not an image or already a placeholder — skip
				continue
			}
			resp["image_base64"] = fmt.Sprintf("[screenshot data stripped on load, %d bytes]", len(b64))
		}
	}
}

// repairOrphanedToolCalls scans the event list for FunctionCall parts that
// have no matching FunctionResponse in any subsequent event. This happens when
// the daemon is killed mid-tool-execution: the FunctionCall event is persisted
// but the FunctionResponse never arrives. Without repair, the malformed history
// causes LLM providers (especially OpenAI) to reject the request with HTTP 400.
//
// For each orphaned FunctionCall, a synthetic FunctionResponse event is appended
// with an error message explaining the interruption.
func repairOrphanedToolCalls(events []*adksession.Event) []*adksession.Event {
	// Collect all FunctionResponse IDs across all events
	answeredIDs := make(map[string]bool)
	for _, event := range events {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				answeredIDs[part.FunctionResponse.ID] = true
			}
		}
	}

	// Find orphaned FunctionCalls (have ID but no matching FunctionResponse)
	var orphanParts []*genai.Part
	for _, event := range events {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				if !answeredIDs[part.FunctionCall.ID] {
					orphanParts = append(orphanParts, part)
				}
			}
		}
	}

	if len(orphanParts) == 0 {
		return events
	}

	slog.Warn("repairing orphaned tool calls from interrupted session", "component", "session", "count", len(orphanParts))

	// Build synthetic FunctionResponse parts for each orphan
	var responseParts []*genai.Part
	for _, orphan := range orphanParts {
		slog.Debug("orphaned tool call", "component", "session", "tool", orphan.FunctionCall.Name, "id", orphan.FunctionCall.ID)
		responseParts = append(responseParts, &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				ID:   orphan.FunctionCall.ID,
				Name: orphan.FunctionCall.Name,
				Response: map[string]any{
					"error": "Tool call was interrupted (daemon restarted). The result is unavailable.",
				},
			},
		})
	}

	// Append a synthetic event with all the error responses
	syntheticEvent := &adksession.Event{
		ID:        "repair-" + fmt.Sprintf("%d", time.Now().UnixMilli()),
		Timestamp: time.Now(),
		Author:    "tool",
	}
	syntheticEvent.Content = &genai.Content{
		Role:  "user",
		Parts: responseParts,
	}

	return append(events, syntheticEvent)
}
func (s *FileStore) updateAppState(appDelta stateMap, appName string) stateMap {
	innerMap, ok := s.appState[appName]
	if !ok {
		innerMap = make(stateMap)
		s.appState[appName] = innerMap
	}
	maps.Copy(innerMap, appDelta)
	return innerMap
}

// updateUserState updates user-scoped state and returns the merged user state.
func (s *FileStore) updateUserState(userDelta stateMap, appName, userID string) stateMap {
	innerUsersMap, ok := s.userState[appName]
	if !ok {
		innerUsersMap = make(map[string]stateMap)
		s.userState[appName] = innerUsersMap
	}
	innerMap, ok := innerUsersMap[userID]
	if !ok {
		innerMap = make(stateMap)
		innerUsersMap[userID] = innerMap
	}
	maps.Copy(innerMap, userDelta)
	return innerMap
}

// mergeStates combines app, user, and session state maps.
func (s *FileStore) mergeStates(sessionState stateMap, appName, userID string) stateMap {
	appState := s.appState[appName]
	var userState stateMap
	if usersMap, ok := s.userState[appName]; ok {
		userState = usersMap[userID]
	}
	return mergeStates(appState, userState, sessionState)
}

// --- fileSession implements session.Session ---

type fileSession struct {
	id        string
	appName   string
	userID    string
	mu        sync.RWMutex
	events    []*adksession.Event
	state     stateMap
	updatedAt time.Time
}

func (fs *fileSession) ID() string              { return fs.id }
func (fs *fileSession) AppName() string         { return fs.appName }
func (fs *fileSession) UserID() string          { return fs.userID }
func (fs *fileSession) State() adksession.State { return &fileState{mu: &fs.mu, state: fs.state} }
func (fs *fileSession) Events() adksession.Events {
	return fileEvents(fs.events)
}
func (fs *fileSession) LastUpdateTime() time.Time {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.updatedAt
}

// appendEvent updates the session with the event's state delta.
func (fs *fileSession) appendEvent(event *adksession.Event) error {
	if event.Partial {
		return nil
	}

	// Apply non-temp state deltas
	if event.Actions.StateDelta != nil {
		if fs.state == nil {
			fs.state = make(stateMap)
		}
		for k, v := range event.Actions.StateDelta {
			if !strings.HasPrefix(k, adksession.KeyPrefixTemp) {
				fs.state[k] = v
			}
		}
	}

	fs.events = append(fs.events, event)
	fs.updatedAt = event.Timestamp
	return nil
}

// copy creates a deep copy of the session.
func (fs *fileSession) copy() *fileSession {
	cp := &fileSession{
		id:        fs.id,
		appName:   fs.appName,
		userID:    fs.userID,
		state:     maps.Clone(fs.state),
		updatedAt: fs.updatedAt,
	}
	cp.events = make([]*adksession.Event, len(fs.events))
	copy(cp.events, fs.events)
	return cp
}

// copyWithoutStateAndEvents creates a shallow copy without state or events.
func (fs *fileSession) copyWithoutStateAndEvents() *fileSession {
	return &fileSession{
		id:        fs.id,
		appName:   fs.appName,
		userID:    fs.userID,
		updatedAt: fs.updatedAt,
	}
}

// --- fileState implements session.State ---

type fileState struct {
	mu    *sync.RWMutex
	state stateMap
}

func (s *fileState) Get(key string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.state[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return val, nil
}

func (s *fileState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		s.mu.RLock()
		for k, v := range s.state {
			s.mu.RUnlock()
			if !yield(k, v) {
				return
			}
			s.mu.RLock()
		}
		s.mu.RUnlock()
	}
}

func (s *fileState) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[key] = value
	return nil
}

// --- fileEvents implements session.Events ---

type fileEvents []*adksession.Event

func (e fileEvents) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, event := range e {
			if !yield(event) {
				return
			}
		}
	}
}

func (e fileEvents) Len() int {
	return len(e)
}

func (e fileEvents) At(i int) *adksession.Event {
	if i >= 0 && i < len(e) {
		return e[i]
	}
	return nil
}

// --- State delta helpers ---

// extractStateDeltas splits a state delta map into app, user, and session scopes.
// Mirrors google.golang.org/adk/internal/sessionutils.ExtractStateDeltas.
func extractStateDeltas(delta stateMap) (appDelta, userDelta, sessionDelta stateMap) {
	appDelta = make(stateMap)
	userDelta = make(stateMap)
	sessionDelta = make(stateMap)

	if delta == nil {
		return
	}

	for key, value := range delta {
		if cleanKey, found := strings.CutPrefix(key, adksession.KeyPrefixApp); found {
			appDelta[cleanKey] = value
		} else if cleanKey, found := strings.CutPrefix(key, adksession.KeyPrefixUser); found {
			userDelta[cleanKey] = value
		} else if !strings.HasPrefix(key, adksession.KeyPrefixTemp) {
			sessionDelta[key] = value
		}
	}
	return
}

// mergeStates combines app, user, and session state maps, adding prefixes back.
// Mirrors google.golang.org/adk/internal/sessionutils.MergeStates.
func mergeStates(appState, userState, sessionState stateMap) stateMap {
	totalSize := len(appState) + len(userState) + len(sessionState)
	merged := make(stateMap, totalSize)

	maps.Copy(merged, sessionState)
	for k, v := range appState {
		merged[adksession.KeyPrefixApp+k] = v
	}
	for k, v := range userState {
		merged[adksession.KeyPrefixUser+k] = v
	}
	return merged
}

// trimTempDeltaState removes temporary state delta keys from the event.
func trimTempDeltaState(event *adksession.Event) *adksession.Event {
	if len(event.Actions.StateDelta) == 0 {
		return event
	}

	filtered := make(stateMap)
	for key, value := range event.Actions.StateDelta {
		if !strings.HasPrefix(key, adksession.KeyPrefixTemp) {
			filtered[key] = value
		}
	}
	event.Actions.StateDelta = filtered
	return event
}

// Compile-time assertion that FileStore implements session.Service.
var _ adksession.Service = (*FileStore)(nil)

// AllSessionIDs returns a set of all known session IDs from the index.
func (s *FileStore) AllSessionIDs() map[string]bool {
	ids, err := s.index.AllSessionIDs()
	if err != nil {
		return nil
	}
	return ids
}

// CleanupExpiredSessions deletes all top-level sessions whose last activity
// (UpdatedAt) is older than maxAgeDays. Child sub-sessions are cascade-deleted
// with their parent. Returns the IDs of deleted top-level sessions so the
// caller can clean up associated resources (e.g., sandbox containers).
func (s *FileStore) CleanupExpiredSessions(maxAgeDays int) []string {
	if maxAgeDays <= 0 {
		return nil
	}

	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)

	// Load the index to find expired sessions
	index, err := s.index.Load()
	if err != nil {
		slog.Error("failed to load session index", "component", "session-cleanup", "error", err)
		return nil
	}

	// Collect expired top-level sessions
	var expired []SessionMeta
	for _, meta := range index.Sessions {
		if meta.ParentID != "" {
			continue // children are cascade-deleted with parent
		}
		if meta.UpdatedAt.Before(cutoff) {
			expired = append(expired, meta)
		}
	}

	if len(expired) == 0 {
		return nil
	}

	var deletedIDs []string
	ctx := context.Background()
	for _, meta := range expired {
		err := s.Delete(ctx, &adksession.DeleteRequest{
			AppName:   meta.AppName,
			UserID:    meta.UserID,
			SessionID: meta.ID,
		})
		if err != nil {
			slog.Error("failed to delete session", "component", "session-cleanup", "session", meta.ID, "error", err)
			continue
		}
		deletedIDs = append(deletedIDs, meta.ID)
		slog.Info("deleted expired session", "component", "session-cleanup", "session", meta.ID, "last_activity", meta.UpdatedAt.Format(time.RFC3339))
	}

	if len(deletedIDs) > 0 {
		slog.Info("cleaned up expired sessions", "component", "session-cleanup", "count", len(deletedIDs), "max_age_days", maxAgeDays)
	}

	return deletedIDs
}

// RedactSession retroactively applies the current RedactFunc to an existing
// session's transcript file. This is used after new credential values are
// registered (e.g. after save_credential) to scrub secrets from user messages
// that were persisted before the credential was known to the redactor.
//
// The method also redacts user-role text Parts in the in-memory session cache
// so that subsequent reads (e.g. session reload in the UI) return redacted text.
func (s *FileStore) RedactSession(appName, userID, sessionID string) error {
	if s.RedactFunc == nil {
		return nil
	}

	// Retroactively redact the on-disk transcript
	transcriptPath := filepath.Join(s.baseDir, appName, userID, sessionID+".jsonl")
	transcript := NewTranscript(transcriptPath)
	if err := transcript.RedactTranscript(s.RedactFunc); err != nil {
		return fmt.Errorf("failed to redact transcript for session %s: %w", sessionID, err)
	}

	// Redact in-memory user event text so the cached session matches disk
	s.mu.RLock()
	sess, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if ok {
		for _, event := range sess.events {
			if event == nil || event.Content == nil || event.Content.Role != "user" {
				continue
			}
			for i, part := range event.Content.Parts {
				if part != nil && part.Text != "" {
					event.Content.Parts[i].Text = s.RedactFunc(part.Text)
				}
			}
		}
	}

	return nil
}
