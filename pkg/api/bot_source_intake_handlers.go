package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http"
)

func registerBotSourceIntakeRoutes(router *mux.Router) {
	router.HandleFunc("/api/studio/bots/source-proposals", BotProposeSourceIntakeHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/source-proposals/{proposalID}", BotGetSourceIntakeHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/source-proposals/{proposalID}/decision", BotDecideSourceIntakeHandler).Methods(http.MethodPost)
}
func sourceIntakeRequestStore(w http.ResponseWriter, r *http.Request) (store.BotSourceIntakeStore, store.BotTemplateStore, bool) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return nil, nil, false
	}
	s, ok := repo.(store.BotSourceIntakeStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, nil, false
	}
	var templates store.BotTemplateStore
	if svc := store.FromRequest(r); svc != nil {
		templates = svc.BotTemplates
	}
	return s, templates, true
}
func BotProposeSourceIntakeHandler(w http.ResponseWriter, r *http.Request) {
	s, t, ok := sourceIntakeRequestStore(w, r)
	if !ok {
		return
	}
	var q store.BotSourceIntakeRequest
	if !botDecodeRequest(w, r, &q) {
		return
	}
	p, e := s.ProposeSourceIntake(r.Context(), q, t)
	if e != nil {
		botHTTPError(w, e)
		return
	}
	botWriteJSON(w, http.StatusCreated, p)
}
func sourceProposalPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, e := uuid.Parse(mux.Vars(r)["proposalID"])
	if e != nil || id == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return uuid.Nil, false
	}
	return id, true
}
func BotGetSourceIntakeHandler(w http.ResponseWriter, r *http.Request) {
	s, _, ok := sourceIntakeRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := sourceProposalPath(w, r)
	if !ok {
		return
	}
	p, e := s.GetSourceIntake(r.Context(), id)
	if e != nil {
		botHTTPError(w, e)
		return
	}
	botWriteJSON(w, http.StatusOK, p)
}
func BotDecideSourceIntakeHandler(w http.ResponseWriter, r *http.Request) {
	s, t, ok := sourceIntakeRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := sourceProposalPath(w, r)
	if !ok {
		return
	}
	var q store.BotSourceIntakeDecision
	if !botDecodeRequest(w, r, &q) {
		return
	}
	p, e := s.DecideSourceIntake(r.Context(), id, q, t, store.BotStoreFromContext(r.Context(), store.BotScopePersonal))
	if e != nil {
		botHTTPError(w, e)
		return
	}
	botWriteJSON(w, http.StatusOK, p)
}
