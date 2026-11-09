package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/session"
)

// BotEventSubscription names a tenant-bound wakeup topic, not read authority.
// Subscribers receive no event data; the HTTP handler reauthorizes stored reads.
type BotEventSubscription struct {
	OrgSlug, TeamSlug string
	BotID, SessionID  uuid.UUID
}
type BotEventSubscriber interface {
	SubscribeBotEvents(BotEventSubscription) (<-chan struct{}, func())
}

var botEventSource struct {
	sync.RWMutex
	source BotEventSubscriber
}

// SetBotEventSubscriber installs the daemon's wakeup accelerator. Persistence and
// periodic catchup remain authoritative, including across daemon processes.
func SetBotEventSubscriber(source BotEventSubscriber) {
	botEventSource.Lock()
	defer botEventSource.Unlock()
	botEventSource.source = source
}

// marshalBotTranscriptEvent validates known state metadata without translating it
// into generic Chat events. Unknown schema versions remain raw, forward-compatible
// transcript rows, which clients must ignore for state rendering.
func marshalBotTranscriptEvent(event *session.Event, botID, sessionID uuid.UUID) ([]byte, error) {
	if value, ok := event.Actions.StateDelta[store.BotOperationKey]; ok {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var view store.BotOperationView
		if err := json.Unmarshal(raw, &view); err != nil {
			return nil, err
		}
		if view.Validate() != nil || view.BotID != botID || view.HomeSessionID != sessionID || event.Content != nil || event.Author != "bot_runtime" {
			return nil, store.ErrBotInvalid
		}
		// Re-encode the public allowlist: never forward unknown authority fields.
		clean := *event
		clean.Actions = session.EventActions{StateDelta: map[string]any{store.BotOperationKey: view}}
		return json.Marshal(&clean)
	}
	if value, ok := event.Actions.StateDelta[store.BotRunStateKey]; ok {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var version struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal(raw, &version); err != nil {
			return nil, err
		}
		if version.SchemaVersion == store.BotRunStateSchemaVersion {
			var state store.BotRunState
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, err
			}
			if state.BotID != botID || state.RunID == uuid.Nil || state.SessionID == uuid.Nil || state.HomeSessionID == uuid.Nil || state.StateSequence < 1 || event.Content != nil || (sessionID != state.SessionID && (sessionID != state.HomeSessionID || state.Presentation != store.BotRunPresentationHomeInteractive)) {
				return nil, store.ErrBotInvalid
			}
			switch state.Status {
			case store.RunQueued, store.RunRunning, store.RunWaitingApproval, store.RunInterrupted, store.RunSucceeded, store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled:
			default:
				return nil, store.ErrBotInvalid
			}
		}
	}
	return json.Marshal(event)
}

const botReplayComplete = "replay_complete"
const botEventPageSize = 100

func botEventCursor(sid uuid.UUID, cursor int64) string {
	return sid.String() + ":" + strconv.FormatInt(cursor, 10)
}
func parseBotEventCursor(sid uuid.UUID, raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	prefix := sid.String() + ":"
	if !strings.HasPrefix(raw, prefix) {
		return 0, store.ErrBotInvalid
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(raw, prefix), 10, 64)
	if err != nil || n < 0 {
		return 0, store.ErrBotInvalid
	}
	return n, nil
}

// BotEventsHandler is an additive read-only snapshot+delta SSE endpoint. It never
// attaches a ChatRunner or resolves credentials. Every payload comes from the
// ordered transcript; coalesced wakes cannot lose events or duplicate delivery.
func BotEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	sid, err := uuid.Parse(mux.Vars(r)["sessionID"])
	if err != nil || sid == uuid.Nil {
		http.Error(w, "Invalid session ID", 400)
		return
	}
	svc := store.FromRequest(r)
	if svc == nil {
		http.Error(w, "Bot storage unavailable", 503)
		return
	}
	scope := store.BotScopePersonal
	ss := svc.PersonalSessions
	if r.URL.Query().Get("scope") == string(store.BotScopeTeam) {
		scope = store.BotScopeTeam
		ss = svc.Sessions
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil || b == nil || b.ID != id || b.Scope != scope || ss == nil {
		http.Error(w, "Bot session not found", 404)
		return
	}
	// Re-read metadata as well as membership: cached linkage never grants access.
	allowed := func() bool {
		if r.Context().Err() != nil {
			return false
		}
		meta, err := ss.GetSessionMeta(r.Context(), sid.String())
		return err == nil && meta != nil && meta.ID == sid.String() && meta.BotID == id.String() && botSessionReadAllowed(r, svc, meta, scope)
	}
	if !allowed() {
		http.Error(w, "Bot session not found", 404)
		return
	}
	reader, ok := ss.(store.BotTranscriptReader)
	if !ok {
		http.Error(w, "Bot transcript unavailable", 503)
		return
	}
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	cursor, err := parseBotEventCursor(sid, raw)
	if err != nil {
		http.Error(w, "Invalid Bot event cursor", 400)
		return
	}
	p, _ := execution.PrincipalFromContext(r.Context())
	botEventSource.RLock()
	source := botEventSource.source
	botEventSource.RUnlock()
	var wake <-chan struct{}
	if source != nil {
		var cancel func()
		wake, cancel = source.SubscribeBotEvents(BotEventSubscription{OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, BotID: id, SessionID: sid})
		defer cancel()
	}
	// Subscribe before the first read so commits during replay are not missed.
	if !allowed() {
		http.Error(w, "Bot session not found", 404)
		return
	}
	rows, err := reader.ReadBotTranscript(r.Context(), sid, cursor, botEventPageSize)
	if err != nil {
		http.Error(w, "Bot transcript unavailable", 500)
		return
	}
	if !allowed() {
		http.Error(w, "Bot session not found", 404)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(event string, payload []byte, position int64) bool {
		if !allowed() {
			return false
		}
		// A slow socket is disconnected, not buffered; its next request catches up.
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", botEventCursor(sid, position), event, payload); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	initial := true
	for {
		approvals, reviewErr := repo.ListApprovals(r.Context(), id, store.BotPage{Limit: 200})
		if reviewErr != nil {
			return
		}
		for _, row := range rows {
			if row.Cursor <= cursor || row.Event == nil {
				return
			}
			payload, err := marshalBotTranscriptEvent(projectBotQuestionTranscript(row.Event, approvals), id, sid)
			if err != nil || !write(botTranscriptEvent, payload, row.Cursor) {
				return
			}
			cursor = row.Cursor
		}
		if len(rows) == botEventPageSize {
			if !allowed() {
				return
			}
			rows, err = reader.ReadBotTranscript(r.Context(), sid, cursor, botEventPageSize)
			if err != nil {
				return
			}
			continue
		}
		if initial {
			if !write(botReplayComplete, []byte(`{"persisted":true}`), cursor) {
				return
			}
			initial = false
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-ticker.C:
		}
		if !allowed() {
			return
		}
		rows, err = reader.ReadBotTranscript(r.Context(), sid, cursor, botEventPageSize)
		if err != nil {
			return
		}
	}
}
