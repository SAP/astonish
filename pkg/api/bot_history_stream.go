package api

import (
	"fmt"
	"net/http"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

const (
	botTranscriptEvent = "transcript"
	botTranscriptDone  = "done"
)

// BotHistoryStreamHandler replays a finite snapshot of persisted events. It does
// not subscribe to ChatRunner, resolve viewer credentials, or resume execution.
// Clients reconnect explicitly for a fresh snapshot; done terminates even empty
// history. SSE IDs are snapshot positions, not resumable live-run cursors.
func BotHistoryStreamHandler(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil || b == nil || b.ID != id {
		http.Error(w, "Bot session not found", http.StatusNotFound)
		return
	}
	svc := store.FromRequest(r)
	if svc == nil {
		http.Error(w, "Bot storage unavailable", http.StatusServiceUnavailable)
		return
	}
	scope := store.BotScopePersonal
	if r.URL.Query().Get("scope") == string(store.BotScopeTeam) {
		scope = store.BotScopeTeam
	}
	var ss store.SessionStore
	switch scope {
	case store.BotScopePersonal:
		ss = svc.PersonalSessions
	case store.BotScopeTeam:
		ss = svc.Sessions
	}
	if b.Scope != scope || ss == nil {
		http.Error(w, "Bot session not found", http.StatusNotFound)
		return
	}
	meta, err := ss.GetSessionMeta(r.Context(), sid.String())
	if err != nil || meta == nil || meta.ID != sid.String() || meta.BotID != id.String() || !botSessionReadAllowed(r, svc, meta, scope) {
		http.Error(w, "Bot session not found", http.StatusNotFound)
		return
	}
	// Both endpoints read the same durable positions, never synthetic offsets.
	reader, ok := ss.(store.BotTranscriptReader)
	if !ok {
		http.Error(w, "Bot transcript unavailable", http.StatusServiceUnavailable)
		return
	}
	allowed := func() bool {
		if r.Context().Err() != nil {
			return false
		}
		current, err := ss.GetSessionMeta(r.Context(), sid.String())
		return err == nil && current != nil && current.ID == sid.String() && current.BotID == id.String() && botSessionReadAllowed(r, svc, current, scope)
	}
	var rows []store.BotTranscriptEvent
	var readCursor int64
	for {
		if !allowed() {
			http.Error(w, "Bot session not found", http.StatusNotFound)
			return
		}
		page, err := reader.ReadBotTranscript(r.Context(), sid, readCursor, botEventPageSize)
		if err != nil {
			http.Error(w, "Bot transcript unavailable", http.StatusInternalServerError)
			return
		}
		for _, row := range page {
			if row.Cursor <= readCursor || row.Event == nil {
				http.Error(w, "Bot transcript unavailable", http.StatusInternalServerError)
				return
			}
			readCursor = row.Cursor
			rows = append(rows, row)
		}
		if len(page) < botEventPageSize {
			break
		}
	}
	approvals, err := listBotPresentationApprovals(r.Context(), repo, id)
	if err != nil {
		http.Error(w, "Bot reviews unavailable", http.StatusInternalServerError)
		return
	}
	// Encode before committing SSE headers so malformed storage fails atomically.
	var frames []BotPresentationFrame
	var cursor int64
	projector := &SessionEventProjector{BotID: id, HomeSessionID: b.HomeSessionID, SessionID: sid, Repository: repo}
	for _, row := range rows {
		projection, err := projector.ProjectBot(r.Context(), row, approvals)
		if err != nil {
			http.Error(w, "Bot transcript unavailable", http.StatusInternalServerError)
			return
		}
		parts, err := projection.BotFrames(cursor)
		if err != nil {
			http.Error(w, "Bot transcript unavailable", http.StatusInternalServerError)
			return
		}
		frames = append(frames, parts...)
		cursor = row.Cursor
	}
	if !allowed() {
		http.Error(w, "Bot session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for _, frame := range frames {
		if !allowed() {
			return
		}
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", botEventCursor(sid, frame.Cursor), frame.Type, frame.Data); err != nil {
			return
		}
		if controller.Flush() != nil {
			return
		}
	}
	if !allowed() {
		return
	}
	_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: {\"persisted\":true,\"count\":%d}\n\n", botEventCursor(sid, cursor), botTranscriptDone, len(frames))
	_ = controller.Flush()
}
