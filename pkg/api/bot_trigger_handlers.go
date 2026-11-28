package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"net/http"
)

func registerBotTriggerRoutes(r *mux.Router) {
	root := "/api/studio/bots/{botID}/triggers/{triggerID}"
	r.HandleFunc(root, BotTriggerSaveHandler).Methods(http.MethodPut)
	r.HandleFunc(root, botTriggerActionHandler(store.BotTriggerRemove)).Methods(http.MethodDelete)
	r.HandleFunc(root+"/enable", botTriggerActionHandler(store.BotTriggerEnable)).Methods(http.MethodPost)
	r.HandleFunc(root+"/disable", botTriggerActionHandler(store.BotTriggerDisable)).Methods(http.MethodPost)
	r.HandleFunc(root+"/dry-run", botTriggerActionHandler(store.BotTriggerDryRun)).Methods(http.MethodPost)
	r.HandleFunc(root+"/events", BotTriggerEventHandler).Methods(http.MethodPost)
}

type botTriggerDTO struct {
	ExpectedRevision int64                   `json:"expected_revision"`
	Reason           string                  `json:"reason"`
	Definition       store.TriggerSpec       `json:"definition"`
	Proof            *store.BotExternalEvent `json:"proof,omitempty"`
}

func BotTriggerSaveHandler(w http.ResponseWriter, r *http.Request) {
	botTriggerActionHandler(store.BotTriggerSave)(w, r)
}
func botTriggerActionHandler(action store.BotTriggerAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repo, _, ok := botRequestStore(w, r)
		if !ok {
			return
		}
		id, ok := botPathID(w, r)
		if !ok {
			return
		}
		tid, ok := botRuntimePathID(w, r, "triggerID")
		if !ok {
			return
		}
		var q botTriggerDTO
		if !botDecodeRequest(w, r, &q) {
			return
		}
		triggers, ok := repo.(store.BotTriggerStore)
		if !ok {
			botHTTPError(w, store.ErrBotUnsupported)
			return
		}
		b, e := triggers.ChangeTrigger(r.Context(), store.BotTriggerChange{BotID: id, TriggerID: tid, ExpectedRevision: q.ExpectedRevision, Action: action, Reason: q.Reason, Definition: q.Definition, Proof: q.Proof})
		if e != nil {
			botHTTPError(w, e)
			return
		}
		botWriteJSON(w, http.StatusOK, b)
	}
}

// Signed events additionally require the existing authenticated, scoped user
// transport. Possession of a signing secret never mints an owner principal.
func BotTriggerEventHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	tid, ok := botRuntimePathID(w, r, "triggerID")
	if !ok {
		return
	}
	var q struct {
		Version   int64  `json:"version"`
		EventID   string `json:"event_id"`
		Timestamp int64  `json:"timestamp"`
		Payload   []byte `json:"payload,omitempty"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	triggers, ok := repo.(store.BotTriggerStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	run, e := triggers.AdmitExternalRun(r.Context(), store.BotExternalEvent{BotID: id, TriggerID: tid, Version: q.Version, EventID: q.EventID, Timestamp: q.Timestamp, Payload: q.Payload, Signature: r.Header.Get("X-Astonish-Signature")})
	if e != nil {
		botHTTPError(w, e)
		return
	}
	botWriteJSON(w, http.StatusAccepted, run)
}
