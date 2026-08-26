package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/predicate"
	"github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/store"

	adksession "google.golang.org/adk/session"
)

// teamSessionStore implements store.SessionStore backed by the team Ent client.
type teamSessionStore struct {
	client   *teament.Client
	redactFn func(string) string
}

var _ store.SessionStore = (*teamSessionStore)(nil)

// ---------------------------------------------------------------------------
// teamSession implements adksession.Session interface
// ---------------------------------------------------------------------------

type teamSession struct {
	id        string
	appName   string
	userID    string
	mu        sync.RWMutex
	state     map[string]any
	events    []*adksession.Event
	updatedAt time.Time
}

func (s *teamSession) ID() string      { return s.id }
func (s *teamSession) AppName() string { return s.appName }
func (s *teamSession) UserID() string  { return s.userID }
func (s *teamSession) State() adksession.State {
	return &teamSessionState{mu: &s.mu, state: s.state}
}
func (s *teamSession) Events() adksession.Events {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return teamSessionEvents(s.events)
}
func (s *teamSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// teamSessionState implements adksession.State.
type teamSessionState struct {
	mu    *sync.RWMutex
	state map[string]any
}

func (ss *teamSessionState) Get(key string) (any, error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	val, ok := ss.state[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return val, nil
}

func (ss *teamSessionState) Set(key string, value any) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.state[key] = value
	return nil
}

func (ss *teamSessionState) All() iter.Seq2[string, any] {
	ss.mu.RLock()
	stateCopy := maps.Clone(ss.state)
	ss.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for k, v := range stateCopy {
			if !yield(k, v) {
				return
			}
		}
	}
}

// teamSessionEvents implements adksession.Events.
type teamSessionEvents []*adksession.Event

func (e teamSessionEvents) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}
func (e teamSessionEvents) Len() int { return len(e) }
func (e teamSessionEvents) At(i int) *adksession.Event {
	if i >= 0 && i < len(e) {
		return e[i]
	}
	return nil
}

// ---------------------------------------------------------------------------
// ADK session.Service methods
// ---------------------------------------------------------------------------

func (s *teamSessionStore) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	id := req.SessionID
	if id == "" {
		id = uuid.NewString()
	}

	// Initialize state from request.
	state := req.State
	if state == nil {
		state = make(map[string]any)
	}

	// Extract parent_id from state (mirrors FileStore behavior).
	// Sub-agents pass "_astonish_parent_id" in state to link child sessions.
	var parentID *string
	if pid, ok := state["_astonish_parent_id"].(string); ok && pid != "" {
		parentID = &pid
		delete(state, "_astonish_parent_id")
	}

	meta := map[string]interface{}{
		"app_name": req.AppName,
	}
	if len(state) > 0 {
		meta["state"] = state
	}

	var userID *uuid.UUID
	if req.UserID != "" {
		uid, err := uuid.Parse(req.UserID)
		if err == nil {
			userID = &uid
		}
	}

	now := time.Now().UTC()
	create := s.client.Session.Create().
		SetID(id).
		SetNillableUserID(userID).
		SetNillableParentID(parentID).
		SetMetadata(meta).
		SetCreatedAt(now).
		SetUpdatedAt(now)

	_, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	sess := &teamSession{
		id:        id,
		appName:   req.AppName,
		userID:    req.UserID,
		state:     state,
		updatedAt: now,
	}
	return &adksession.CreateResponse{Session: sess}, nil
}

func (s *teamSessionStore) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	ent, err := s.client.Session.Get(ctx, req.SessionID)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, fmt.Errorf("session not found: %s", req.SessionID)
		}
		return nil, fmt.Errorf("get session: %w", err)
	}

	// Load events ordered by ID (sequential).
	events, err := s.client.SessionEvent.Query().
		Where(sessionevent.SessionIDEQ(req.SessionID)).
		Order(sessionevent.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load session events: %w", err)
	}

	adkEvents := make([]*adksession.Event, 0, len(events))
	for _, ev := range events {
		adkEv, err := eventDataToADKEvent(ev.EventData)
		if err != nil {
			continue // skip malformed events
		}
		adkEvents = append(adkEvents, adkEv)
	}

	// Rebuild state from metadata + replaying state deltas.
	state := extractState(ent.Metadata)
	if state == nil {
		state = make(map[string]any)
	}
	for _, ev := range adkEvents {
		if ev.Actions.StateDelta != nil {
			maps.Copy(state, ev.Actions.StateDelta)
		}
	}

	// Apply filters.
	filtered := adkEvents
	if req.NumRecentEvents > 0 {
		start := max(len(filtered)-req.NumRecentEvents, 0)
		filtered = filtered[start:]
	}
	if !req.After.IsZero() && len(filtered) > 0 {
		for i, ev := range filtered {
			if !ev.Timestamp.Before(req.After) {
				filtered = filtered[i:]
				break
			}
		}
	}

	sess := &teamSession{
		id:        ent.ID,
		appName:   extractAppName(ent.Metadata),
		userID:    uuidPtrToStr(ent.UserID),
		state:     state,
		events:    filtered,
		updatedAt: ent.UpdatedAt,
	}
	return &adksession.GetResponse{Session: sess}, nil
}

func (s *teamSessionStore) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	query := s.client.Session.Query().
		Where(session.ParentIDIsNil()).
		Order(session.ByCreatedAt(sql.OrderDesc()))

	// Filter by user_id if provided.
	if req.UserID != "" {
		uid, err := uuid.Parse(req.UserID)
		if err == nil {
			query = query.Where(session.UserIDEQ(uid))
		}
	}

	ents, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	// Filter by app_name in metadata (the sessions table doesn't have an app_name column).
	var sessions []adksession.Session
	for _, e := range ents {
		appName := extractAppName(e.Metadata)
		if req.AppName != "" && appName != "" && appName != req.AppName {
			continue
		}
		sessions = append(sessions, &teamSession{
			id:        e.ID,
			appName:   appName,
			userID:    uuidPtrToStr(e.UserID),
			state:     make(map[string]any),
			updatedAt: e.UpdatedAt,
		})
	}
	return &adksession.ListResponse{Sessions: sessions}, nil
}

func (s *teamSessionStore) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	// Delete events first (may not cascade depending on schema config).
	_, _ = s.client.SessionEvent.Delete().
		Where(sessionevent.SessionIDEQ(req.SessionID)).
		Exec(ctx)

	err := s.client.Session.DeleteOneID(req.SessionID).Exec(ctx)
	if err != nil && !teament.IsNotFound(err) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *teamSessionStore) AppendEvent(ctx context.Context, sess adksession.Session, event *adksession.Event) error {
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	if event.Partial {
		return nil
	}

	// Update the in-memory session so that subsequent reads (e.g.
	// ContentsRequestProcessor) see the appended event without a DB round-trip.
	if ts, ok := sess.(*teamSession); ok {
		ts.mu.Lock()
		if event.Actions.StateDelta != nil {
			if ts.state == nil {
				ts.state = make(map[string]any)
			}
			maps.Copy(ts.state, event.Actions.StateDelta)
		}
		processed := trimTempState(event)
		ts.events = append(ts.events, processed)
		ts.updatedAt = event.Timestamp
		ts.mu.Unlock()
	}

	return s.appendEventInternal(ctx, sess.ID(), event)
}

// ---------------------------------------------------------------------------
// Astonish-specific SessionStore methods
// ---------------------------------------------------------------------------

func (s *teamSessionStore) ListSessionMetas(ctx context.Context, appName, userID string) ([]store.SessionMeta, error) {
	query := s.client.Session.Query().
		Where(session.ParentIDIsNil()).
		Order(session.ByUpdatedAt(sql.OrderDesc()))

	if userID != "" {
		uid, err := uuid.Parse(userID)
		if err == nil {
			query = query.Where(session.UserIDEQ(uid))
		}
	}

	ents, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session metas: %w", err)
	}

	var metas []store.SessionMeta
	for _, e := range ents {
		an := extractAppName(e.Metadata)
		if appName != "" && an != "" && an != appName {
			continue
		}
		metas = append(metas, entSessionToMeta(e))
	}
	return metas, nil
}

func (s *teamSessionStore) GetSessionMeta(ctx context.Context, sessionID string) (*store.SessionMeta, error) {
	ent, err := s.client.Session.Get(ctx, sessionID)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get session meta: %w", err)
	}
	m := entSessionToMeta(ent)
	return &m, nil
}

func (s *teamSessionStore) SetSessionTitle(ctx context.Context, sessionID, title string) error {
	return s.client.Session.UpdateOneID(sessionID).
		SetTitle(title).
		SetUpdatedAt(time.Now().UTC()).
		Exec(ctx)
}

func (s *teamSessionStore) GetSessionTitle(ctx context.Context, sessionID string) (string, error) {
	meta, err := s.GetSessionMeta(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return meta.Title, nil
}

func (s *teamSessionStore) ListChildren(ctx context.Context, parentID string) ([]store.SessionMeta, error) {
	ents, err := s.client.Session.Query().
		Where(session.ParentIDEQ(parentID)).
		Order(session.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	metas := make([]store.SessionMeta, len(ents))
	for i, e := range ents {
		metas[i] = entSessionToMeta(e)
	}
	return metas, nil
}

func (s *teamSessionStore) AddSessionMeta(ctx context.Context, meta store.SessionMeta) error {
	id := meta.ID
	if id == "" {
		id = uuid.NewString()
	}

	var userID *uuid.UUID
	if meta.UserID != "" {
		uid, err := uuid.Parse(meta.UserID)
		if err == nil {
			userID = &uid
		}
	}

	now := time.Now().UTC()
	createdAt := meta.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := meta.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}

	mdMap := map[string]interface{}{
		"app_name": meta.AppName,
	}

	create := s.client.Session.Create().
		SetID(id).
		SetNillableUserID(userID).
		SetTitle(meta.Title).
		SetMessageCount(meta.MessageCount).
		SetNillableParentID(nilStrPtr(meta.ParentID)).
		SetFleetKey(meta.FleetKey).
		SetFleetName(meta.FleetName).
		SetIssueNumber(meta.IssueNumber).
		SetRepo(meta.Repo).
		SetWorkspaceDir(meta.WorkspaceDir).
		SetMetadata(mdMap).
		SetCreatedAt(createdAt).
		SetUpdatedAt(updatedAt)

	_, err := create.Save(ctx)
	return err
}

func (s *teamSessionStore) UpdateSessionMeta(ctx context.Context, sessionID string, fn func(*store.SessionMeta)) error {
	ent, err := s.client.Session.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("update session meta: get: %w", err)
	}

	meta := entSessionToMeta(ent)
	fn(&meta)

	var userID *uuid.UUID
	if meta.UserID != "" {
		uid, err := uuid.Parse(meta.UserID)
		if err == nil {
			userID = &uid
		}
	}

	mdMap := ent.Metadata
	if mdMap == nil {
		mdMap = map[string]interface{}{}
	}
	mdMap["app_name"] = meta.AppName

	return s.client.Session.UpdateOneID(sessionID).
		SetNillableUserID(userID).
		SetTitle(meta.Title).
		SetMessageCount(meta.MessageCount).
		SetNillableParentID(nilStrPtr(meta.ParentID)).
		SetFleetKey(meta.FleetKey).
		SetFleetName(meta.FleetName).
		SetIssueNumber(meta.IssueNumber).
		SetRepo(meta.Repo).
		SetWorkspaceDir(meta.WorkspaceDir).
		SetMetadata(mdMap).
		SetUpdatedAt(time.Now().UTC()).
		Exec(ctx)
}

func (s *teamSessionStore) RemoveSessionMeta(ctx context.Context, sessionID string) error {
	// Delete events first.
	_, _ = s.client.SessionEvent.Delete().
		Where(sessionevent.SessionIDEQ(sessionID)).
		Exec(ctx)

	err := s.client.Session.DeleteOneID(sessionID).Exec(ctx)
	if err != nil && !teament.IsNotFound(err) {
		return fmt.Errorf("remove session: %w", err)
	}
	return nil
}

func (s *teamSessionStore) ReadTranscriptEvents(ctx context.Context, _, _, sessionID string) ([]*adksession.Event, error) {
	events, err := s.client.SessionEvent.Query().
		Where(sessionevent.SessionIDEQ(sessionID)).
		Order(sessionevent.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("read transcript events: %w", err)
	}

	adkEvents := make([]*adksession.Event, 0, len(events))
	for _, ev := range events {
		adkEv, err := eventDataToADKEvent(ev.EventData)
		if err != nil {
			continue
		}
		adkEvents = append(adkEvents, adkEv)
	}
	return adkEvents, nil
}

func (s *teamSessionStore) AppendFleetEvent(ctx context.Context, sessionID string, event *adksession.Event) error {
	return s.appendEventInternal(ctx, sessionID, event)
}

func (s *teamSessionStore) ResolveSessionID(ctx context.Context, partial string) (string, error) {
	ents, err := s.client.Session.Query().
		Where(predicate.Session(sql.FieldHasPrefix(session.FieldID, partial))).
		Limit(2).
		All(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve session ID: %w", err)
	}
	switch len(ents) {
	case 0:
		return "", fmt.Errorf("no session found matching prefix %q", partial)
	case 1:
		return ents[0].ID, nil
	default:
		return "", fmt.Errorf("ambiguous session prefix %q: multiple matches", partial)
	}
}

func (s *teamSessionStore) AllSessionIDs(ctx context.Context) map[string]bool {
	ents, err := s.client.Session.Query().
		Select(session.FieldID).
		All(ctx)
	if err != nil {
		return nil
	}
	ids := make(map[string]bool, len(ents))
	for _, e := range ents {
		ids[e.ID] = true
	}
	return ids
}

func (s *teamSessionStore) CleanupExpiredSessions(ctx context.Context, maxAgeDays int) []string {
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays)
	ents, err := s.client.Session.Query().
		Where(session.CreatedAtLT(cutoff)).
		Select(session.FieldID).
		All(ctx)
	if err != nil {
		return nil
	}

	var deleted []string
	for _, e := range ents {
		// Delete events.
		_, _ = s.client.SessionEvent.Delete().
			Where(sessionevent.SessionIDEQ(e.ID)).
			Exec(ctx)
		// Delete session.
		if err := s.client.Session.DeleteOneID(e.ID).Exec(ctx); err == nil {
			deleted = append(deleted, e.ID)
		}
	}
	return deleted
}

func (s *teamSessionStore) RedactSession(ctx context.Context, _, _, sessionID string) error {
	if s.redactFn == nil {
		return nil
	}

	events, err := s.client.SessionEvent.Query().
		Where(sessionevent.SessionIDEQ(sessionID)).
		Order(sessionevent.ByID()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("redact session: load events: %w", err)
	}

	for _, ev := range events {
		// Serialize, redact, deserialize, update.
		raw, err := json.Marshal(ev.EventData)
		if err != nil {
			continue
		}
		redacted := s.redactFn(string(raw))
		var newData map[string]interface{}
		if err := json.Unmarshal([]byte(redacted), &newData); err != nil {
			continue
		}
		_ = s.client.SessionEvent.UpdateOneID(ev.ID).
			SetEventData(newData).
			Exec(ctx)
	}
	return nil
}

func (s *teamSessionStore) SetRedactFunc(fn func(string) string) {
	s.redactFn = fn
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (s *teamSessionStore) appendEventInternal(ctx context.Context, sessionID string, event *adksession.Event) error {
	eventData, err := adkEventToMap(event)
	if err != nil {
		return fmt.Errorf("serialize event: %w", err)
	}

	_, err = s.client.SessionEvent.Create().
		SetSessionID(sessionID).
		SetEventData(eventData).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}

	// Update session last_seq and updated_at.
	_ = s.client.Session.UpdateOneID(sessionID).
		AddLastSeq(1).
		SetUpdatedAt(time.Now().UTC()).
		Exec(ctx)

	return nil
}

// entSessionToMeta converts a team Session entity to store.SessionMeta.
func entSessionToMeta(e *teament.Session) store.SessionMeta {
	m := store.SessionMeta{
		ID:           e.ID,
		AppName:      extractAppName(e.Metadata),
		CreatedAt:    e.CreatedAt,
		UpdatedAt:    e.UpdatedAt,
		Title:        e.Title,
		MessageCount: e.MessageCount,
		FleetKey:     e.FleetKey,
		FleetName:    e.FleetName,
		IssueNumber:  e.IssueNumber,
		Repo:         e.Repo,
		WorkspaceDir: e.WorkspaceDir,
	}
	if e.UserID != nil {
		m.UserID = e.UserID.String()
	}
	if e.ParentID != nil {
		m.ParentID = *e.ParentID
	}
	return m
}

// extractAppName extracts the app_name from session metadata.
func extractAppName(md map[string]interface{}) string {
	if md == nil {
		return ""
	}
	if v, ok := md["app_name"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// extractState extracts the state map from session metadata.
func extractState(md map[string]interface{}) map[string]any {
	if md == nil {
		return nil
	}
	if v, ok := md["state"]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	return nil
}

// uuidPtrToStr converts a *uuid.UUID to string.
func uuidPtrToStr(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}

// adkEventToMap serializes an ADK event to a map suitable for JSON storage.
func adkEventToMap(event *adksession.Event) (map[string]interface{}, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// eventDataToADKEvent deserializes a stored event map back into an ADK Event.
func eventDataToADKEvent(data map[string]interface{}) (*adksession.Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var event adksession.Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	return &event, nil
}
