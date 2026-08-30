package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	personalent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/personal/cachediagnostic"
	"github.com/SAP/astonish/ent/personal/predicate"
	"github.com/SAP/astonish/ent/personal/session"
	"github.com/SAP/astonish/ent/personal/sessionevent"
	"github.com/SAP/astonish/pkg/store"

	adksession "google.golang.org/adk/session"
)

// personalSessionStore implements store.SessionStore for personal scope.
type personalSessionStore struct {
	client             *personalent.Client
	redactFn           func(string) string
	cacheDiagnosticsMu sync.Mutex
}

var _ store.SessionStore = (*personalSessionStore)(nil)

// ---------------------------------------------------------------------------
// personalSession implements adksession.Session interface
// ---------------------------------------------------------------------------

type personalSession struct {
	id        string
	appName   string
	userID    string
	mu        sync.RWMutex
	state     map[string]any
	events    []*adksession.Event
	updatedAt time.Time
}

func (s *personalSession) ID() string      { return s.id }
func (s *personalSession) AppName() string { return s.appName }
func (s *personalSession) UserID() string  { return s.userID }
func (s *personalSession) State() adksession.State {
	return &personalSessionState{mu: &s.mu, state: s.state}
}
func (s *personalSession) Events() adksession.Events {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return personalSessionEvents(s.events)
}
func (s *personalSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// personalSessionState implements adksession.State.
type personalSessionState struct {
	mu    *sync.RWMutex
	state map[string]any
}

func (ss *personalSessionState) Get(key string) (any, error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	val, ok := ss.state[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return val, nil
}

func (ss *personalSessionState) Set(key string, value any) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.state[key] = value
	return nil
}

func (ss *personalSessionState) All() iter.Seq2[string, any] {
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

// personalSessionEvents implements adksession.Events.
type personalSessionEvents []*adksession.Event

func (e personalSessionEvents) All() iter.Seq[*adksession.Event] {
	return func(yield func(*adksession.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}
func (e personalSessionEvents) Len() int { return len(e) }
func (e personalSessionEvents) At(i int) *adksession.Event {
	if i >= 0 && i < len(e) {
		return e[i]
	}
	return nil
}

// ---------------------------------------------------------------------------
// ADK session.Service methods
// ---------------------------------------------------------------------------

func (ss *personalSessionStore) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
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
	create := ss.client.Session.Create().
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

	sess := &personalSession{
		id:        id,
		appName:   req.AppName,
		userID:    req.UserID,
		state:     state,
		updatedAt: now,
	}
	return &adksession.CreateResponse{Session: sess}, nil
}

func (ss *personalSessionStore) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	ent, err := ss.client.Session.Get(ctx, req.SessionID)
	if err != nil {
		if personalent.IsNotFound(err) {
			return nil, fmt.Errorf("session not found: %s", req.SessionID)
		}
		return nil, fmt.Errorf("get session: %w", err)
	}

	// Load events ordered by ID.
	events, err := ss.client.SessionEvent.Query().
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
			continue
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

	sess := &personalSession{
		id:        ent.ID,
		appName:   extractAppName(ent.Metadata),
		userID:    uuidPtrToStr(ent.UserID),
		state:     state,
		events:    filtered,
		updatedAt: ent.UpdatedAt,
	}
	return &adksession.GetResponse{Session: sess}, nil
}

func (ss *personalSessionStore) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	query := ss.client.Session.Query().
		Where(session.ParentIDIsNil()).
		Order(session.ByCreatedAt(sql.OrderDesc()))

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

	var sessions []adksession.Session
	for _, e := range ents {
		appName := extractAppName(e.Metadata)
		if req.AppName != "" && appName != "" && appName != req.AppName {
			continue
		}
		sessions = append(sessions, &personalSession{
			id:        e.ID,
			appName:   appName,
			userID:    uuidPtrToStr(e.UserID),
			state:     make(map[string]any),
			updatedAt: e.UpdatedAt,
		})
	}
	return &adksession.ListResponse{Sessions: sessions}, nil
}

func (ss *personalSessionStore) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	_, _ = ss.client.CacheDiagnostic.Delete().
		Where(cachediagnostic.SessionIDEQ(req.SessionID)).
		Exec(ctx)
	// Delete events first.
	_, _ = ss.client.SessionEvent.Delete().
		Where(sessionevent.SessionIDEQ(req.SessionID)).
		Exec(ctx)

	err := ss.client.Session.DeleteOneID(req.SessionID).Exec(ctx)
	if err != nil && !personalent.IsNotFound(err) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (ss *personalSessionStore) AppendEvent(ctx context.Context, sess adksession.Session, event *adksession.Event) error {
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	if event.Partial {
		return nil
	}

	// Update the in-memory session so that subsequent reads (e.g.
	// ContentsRequestProcessor) see the appended event without a DB round-trip.
	if ps, ok := sess.(*personalSession); ok {
		ps.mu.Lock()
		// Apply state delta.
		if event.Actions.StateDelta != nil {
			if ps.state == nil {
				ps.state = make(map[string]any)
			}
			maps.Copy(ps.state, event.Actions.StateDelta)
		}
		// Trim temporary state keys before storing in the events list.
		processed := trimTempState(event)
		ps.events = append(ps.events, processed)
		ps.updatedAt = event.Timestamp
		ps.mu.Unlock()
	}

	return ss.appendEventInternal(ctx, sess.ID(), event)
}

// ---------------------------------------------------------------------------
// Astonish-specific SessionStore methods
// ---------------------------------------------------------------------------

func (ss *personalSessionStore) ListSessionMetas(ctx context.Context, appName, userID string) ([]store.SessionMeta, error) {
	query := ss.client.Session.Query().
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
		metas = append(metas, personalEntSessionToMeta(e))
	}
	return metas, nil
}

func (ss *personalSessionStore) GetSessionMeta(ctx context.Context, sessionID string) (*store.SessionMeta, error) {
	ent, err := ss.client.Session.Get(ctx, sessionID)
	if err != nil {
		if personalent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get session meta: %w", err)
	}
	m := personalEntSessionToMeta(ent)
	return &m, nil
}

func (ss *personalSessionStore) SetSessionTitle(ctx context.Context, sessionID, title string) error {
	return ss.client.Session.UpdateOneID(sessionID).
		SetTitle(title).
		SetUpdatedAt(time.Now().UTC()).
		Exec(ctx)
}

func (ss *personalSessionStore) GetSessionTitle(ctx context.Context, sessionID string) (string, error) {
	meta, err := ss.GetSessionMeta(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return meta.Title, nil
}

func (ss *personalSessionStore) ListChildren(ctx context.Context, parentID string) ([]store.SessionMeta, error) {
	ents, err := ss.client.Session.Query().
		Where(session.ParentIDEQ(parentID)).
		Order(session.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	metas := make([]store.SessionMeta, len(ents))
	for i, e := range ents {
		metas[i] = personalEntSessionToMeta(e)
	}
	return metas, nil
}

func (ss *personalSessionStore) AddSessionMeta(ctx context.Context, meta store.SessionMeta) error {
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

	create := ss.client.Session.Create().
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

func (ss *personalSessionStore) UpdateSessionMeta(ctx context.Context, sessionID string, fn func(*store.SessionMeta)) error {
	ent, err := ss.client.Session.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("update session meta: get: %w", err)
	}

	meta := personalEntSessionToMeta(ent)
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

	return ss.client.Session.UpdateOneID(sessionID).
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

func (ss *personalSessionStore) RemoveSessionMeta(ctx context.Context, sessionID string) error {
	_, _ = ss.client.CacheDiagnostic.Delete().
		Where(cachediagnostic.SessionIDEQ(sessionID)).
		Exec(ctx)
	// Delete events first.
	_, _ = ss.client.SessionEvent.Delete().
		Where(sessionevent.SessionIDEQ(sessionID)).
		Exec(ctx)

	err := ss.client.Session.DeleteOneID(sessionID).Exec(ctx)
	if err != nil && !personalent.IsNotFound(err) {
		return fmt.Errorf("remove session: %w", err)
	}
	return nil
}

func (ss *personalSessionStore) ReadTranscriptEvents(ctx context.Context, _, _, sessionID string) ([]*adksession.Event, error) {
	events, err := ss.client.SessionEvent.Query().
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

func (ss *personalSessionStore) AppendFleetEvent(ctx context.Context, sessionID string, event *adksession.Event) error {
	return ss.appendEventInternal(ctx, sessionID, event)
}

func (ss *personalSessionStore) AppendCacheDiagnostic(ctx context.Context, sessionID string, diagnostic store.CacheDiagnostic) error {
	ss.cacheDiagnosticsMu.Lock()
	defer ss.cacheDiagnosticsMu.Unlock()

	data, err := json.Marshal(diagnostic)
	if err != nil {
		return fmt.Errorf("encode cache diagnostic: %w", err)
	}
	tx, err := ss.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin cache diagnostic transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	create := tx.CacheDiagnostic.Create().
		SetSessionID(sessionID).
		SetInvocationID(diagnostic.InvocationID).
		SetCall(diagnostic.Call).
		SetData(data).
		SetRound(diagnostic.Call).
		SetCacheStablePath(diagnostic.StablePrefixElements > 0).
		SetSystemHash(diagnostic.InputHash).
		SetSystemChanged(false).
		SetSystemChangedSession(false).
		SetToolHash("").
		SetToolCount(0).
		SetToolsChanged(false).
		SetToolsChangedSession(false)
	if !diagnostic.CreatedAt.IsZero() {
		create.SetCreatedAt(diagnostic.CreatedAt)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("append cache diagnostic: %w", err)
	}

	excess, err := tx.CacheDiagnostic.Query().
		Where(cachediagnostic.SessionIDEQ(sessionID)).
		Order(cachediagnostic.ByCreatedAt(sql.OrderDesc()), cachediagnostic.ByID(sql.OrderDesc())).
		Offset(store.MaxSessionCacheDiagnostics).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list excess cache diagnostics: %w", err)
	}
	if len(excess) > 0 {
		ids := make([]int64, len(excess))
		for i, row := range excess {
			ids[i] = row.ID
		}
		if _, err := tx.CacheDiagnostic.Delete().Where(cachediagnostic.IDIn(ids...)).Exec(ctx); err != nil {
			return fmt.Errorf("trim cache diagnostics: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cache diagnostic: %w", err)
	}
	return nil
}

func (ss *personalSessionStore) ListCacheDiagnostics(ctx context.Context, sessionID string) ([]store.CacheDiagnostic, error) {
	rows, err := ss.client.CacheDiagnostic.Query().
		Where(cachediagnostic.SessionIDEQ(sessionID)).
		Order(cachediagnostic.ByCreatedAt(), cachediagnostic.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cache diagnostics: %w", err)
	}
	out := make([]store.CacheDiagnostic, 0, len(rows))
	for _, row := range rows {
		var diagnostic store.CacheDiagnostic
		if len(row.Data) > 0 {
			if err := json.Unmarshal(row.Data, &diagnostic); err != nil {
				return nil, fmt.Errorf("decode cache diagnostic: %w", err)
			}
		} else {
			diagnostic = store.CacheDiagnostic{InvocationID: row.InvocationID, Call: row.Call, InputHash: row.SystemHash}
		}
		diagnostic.CreatedAt = row.CreatedAt
		out = append(out, diagnostic)
	}
	return out, nil
}

func (ss *personalSessionStore) ResolveSessionID(ctx context.Context, partial string) (string, error) {
	ents, err := ss.client.Session.Query().
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

func (ss *personalSessionStore) AllSessionIDs(ctx context.Context) map[string]bool {
	ents, err := ss.client.Session.Query().
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

func (ss *personalSessionStore) CleanupExpiredSessions(ctx context.Context, maxAgeDays int) []string {
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays)
	ents, err := ss.client.Session.Query().
		Where(session.CreatedAtLT(cutoff)).
		Select(session.FieldID).
		All(ctx)
	if err != nil {
		return nil
	}

	var deleted []string
	for _, e := range ents {
		_, _ = ss.client.CacheDiagnostic.Delete().
			Where(cachediagnostic.SessionIDEQ(e.ID)).
			Exec(ctx)
		_, _ = ss.client.SessionEvent.Delete().
			Where(sessionevent.SessionIDEQ(e.ID)).
			Exec(ctx)
		if err := ss.client.Session.DeleteOneID(e.ID).Exec(ctx); err == nil {
			deleted = append(deleted, e.ID)
		}
	}
	return deleted
}

func (ss *personalSessionStore) RedactSession(ctx context.Context, _, _, sessionID string) error {
	if ss.redactFn == nil {
		return nil
	}

	events, err := ss.client.SessionEvent.Query().
		Where(sessionevent.SessionIDEQ(sessionID)).
		Order(sessionevent.ByID()).
		All(ctx)
	if err != nil {
		return fmt.Errorf("redact session: load events: %w", err)
	}

	for _, ev := range events {
		raw, err := json.Marshal(ev.EventData)
		if err != nil {
			continue
		}
		redacted := ss.redactFn(string(raw))
		var newData map[string]interface{}
		if err := json.Unmarshal([]byte(redacted), &newData); err != nil {
			continue
		}
		_ = ss.client.SessionEvent.UpdateOneID(ev.ID).
			SetEventData(newData).
			Exec(ctx)
	}
	return nil
}

func (ss *personalSessionStore) SetRedactFunc(fn func(string) string) {
	ss.redactFn = fn
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (ss *personalSessionStore) appendEventInternal(ctx context.Context, sessionID string, event *adksession.Event) error {
	eventData, err := adkEventToMap(event)
	if err != nil {
		return fmt.Errorf("serialize event: %w", err)
	}

	_, err = ss.client.SessionEvent.Create().
		SetSessionID(sessionID).
		SetEventData(eventData).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}

	// Update message count and updated_at.
	_ = ss.client.Session.UpdateOneID(sessionID).
		AddMessageCount(1).
		SetUpdatedAt(time.Now().UTC()).
		Exec(ctx)

	return nil
}

// personalEntSessionToMeta converts a personal Session entity to store.SessionMeta.
func personalEntSessionToMeta(e *personalent.Session) store.SessionMeta {
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

// trimTempState returns the event with temporary state keys (prefix "temp:")
// removed from StateDelta. This mirrors the ADK's trimTempDeltaState behavior
// so that in-memory events don't carry ephemeral state.
func trimTempState(event *adksession.Event) *adksession.Event {
	if len(event.Actions.StateDelta) == 0 {
		return event
	}
	filtered := make(map[string]any)
	for key, value := range event.Actions.StateDelta {
		if !strings.HasPrefix(key, adksession.KeyPrefixTemp) {
			filtered[key] = value
		}
	}
	// Return a shallow copy with trimmed state to avoid mutating the original.
	cp := *event
	cp.Actions.StateDelta = filtered
	return &cp
}
