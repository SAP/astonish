package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"net/http"
)

func registerBotUnfinishedIntakeRoutes(router *mux.Router) {
	router.HandleFunc("/api/studio/bots/intake/{botID}", BotUnfinishedIntakeHandler).Methods(http.MethodGet, http.MethodPut, http.MethodDelete)
}

// BotUnfinishedIntakeHandler only stores inert user input. It never creates a Bot.
func BotUnfinishedIntakeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	provider, ok := repo.(store.BotUnfinishedIntakeProvider)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	intake := provider.UnfinishedIntakes()
	switch r.Method {
	case http.MethodGet:
		b, err := intake.Get(r.Context(), id)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusOK, b)
	case http.MethodPut:
		var q store.BotUnfinishedPut
		r.Body = http.MaxBytesReader(w, r.Body, store.BotIntakeMaxBytes)
		if !botDecodeRequest(w, r, &q) {
			return
		}
		b, err := intake.Put(r.Context(), id, q)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusOK, b)
	case http.MethodDelete:
		var q store.BotUnfinishedDelete
		r.Body = http.MaxBytesReader(w, r.Body, store.BotIntakeMaxBytes)
		if !botDecodeRequest(w, r, &q) {
			return
		}
		if err := intake.Delete(r.Context(), id, q); err != nil {
			botHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
