package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"net/http"
)

func registerBotSnapshotRoutes(router *mux.Router) {
	router.HandleFunc("/api/studio/bots/{botID}/snapshot", BotSnapshotHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/read", BotReadCursorHandler).Methods(http.MethodGet)
}
func botSnapshotRead(w http.ResponseWriter, r *http.Request, cursorOnly bool) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	reader, ok := repo.(store.BotSnapshotReader)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	if cursorOnly {
		result, err := reader.ReadCursor(r.Context(), id)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusOK, result)
	} else {
		result, err := reader.Snapshot(r.Context(), id)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusOK, result)
	}
}
func BotSnapshotHandler(w http.ResponseWriter, r *http.Request)   { botSnapshotRead(w, r, false) }
func BotReadCursorHandler(w http.ResponseWriter, r *http.Request) { botSnapshotRead(w, r, true) }
