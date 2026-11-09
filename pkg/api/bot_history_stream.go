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
	// Authorization uses the viewer, but transcript attribution is the recorded
	// execution owner. Never install that owner as the request principal.
	events, err := ss.ReadTranscriptEvents(r.Context(), studioChatAppName, meta.UserID, meta.ID)
	if err != nil {
		http.Error(w, "Bot transcript unavailable", http.StatusInternalServerError)
		return
	}
	approvals, err := repo.ListApprovals(r.Context(), id, store.BotPage{Limit: 200})
	if err != nil {
		http.Error(w, "Bot reviews unavailable", http.StatusInternalServerError)
		return
	}
	// Encode before committing SSE headers so malformed storage fails atomically.
	payloads := make([][]byte, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		payload, err := marshalBotTranscriptEvent(projectBotQuestionTranscript(event, approvals), id, sid)
		if err != nil {
			http.Error(w, "Bot transcript unavailable", http.StatusInternalServerError)
			return
		}
		payloads = append(payloads, payload)
	}
	if !botSessionReadAllowed(r, svc, meta, scope) {
		http.Error(w, "Bot session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	for i, payload := range payloads {
		if r.Context().Err() != nil || !botSessionReadAllowed(r, svc, meta, scope) {
			return
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", i+1, botTranscriptEvent, payload); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	if r.Context().Err() != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: {\"persisted\":true,\"count\":%d}\n\n", botTranscriptDone, len(payloads))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
