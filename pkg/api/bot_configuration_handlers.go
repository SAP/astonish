package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http"
)

func registerBotConfigurationRoutes(router *mux.Router) {
	router.HandleFunc("/api/studio/bots/{botID}/configuration", BotConfigurationHandler).Methods(http.MethodPut)
	router.HandleFunc("/api/studio/bots/{botID}/proposals", BotControlProposalsHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/proposals/{proposalID}/decision", BotControlProposalDecisionHandler).Methods(http.MethodPost)
	registerBotPromotionRoutes(router)
}

func BotConfigurationHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q store.BotConfigurationRequest
	if !botDecodeRequest(w, r, &q) {
		return
	}
	q.BotID = id
	reviewed, ok := repo.(store.BotConfigurationStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	b, err := reviewed.ConfigureReviewed(r.Context(), q)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, b)
}

func botProposalRequest(w http.ResponseWriter, r *http.Request) (store.BotControlProposalStore, store.BotID, bool) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}
	id, ok := botPathID(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}
	proposals, ok := repo.(store.BotControlProposalStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, uuid.Nil, false
	}
	return proposals, id, true
}
func BotControlProposalsHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botProposalRequest(w, r)
	if !ok {
		return
	}
	rows, err := repo.ListControlProposals(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if rows == nil {
		rows = []store.BotControlProposal{}
	}
	botWriteJSON(w, http.StatusOK, map[string]any{"proposals": rows})
}

type botProposalDecision string

const (
	botProposalConfirm botProposalDecision = "confirm"
	botProposalReject  botProposalDecision = "reject"
)

// Decisions accept only the digest of the saved payload and explicit consent.
// Actor, edited payload, revision and lease never come from the HTTP body.
func BotControlProposalDecisionHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botProposalRequest(w, r)
	if !ok {
		return
	}
	proposalID, err := uuid.Parse(mux.Vars(r)["proposalID"])
	if err != nil || proposalID == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	var body struct {
		Digest   string              `json:"digest"`
		Decision botProposalDecision `json:"decision"`
	}
	if !botDecodeRequest(w, r, &body) {
		return
	}
	if body.Digest == "" || (body.Decision != botProposalConfirm && body.Decision != botProposalReject) {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	q := store.BotControlProposalDecision{BotID: id, ProposalID: proposalID, Digest: body.Digest}
	var receipt *store.BotControlProposal
	if body.Decision == botProposalConfirm {
		receipt, err = repo.ConfirmControlProposal(r.Context(), q)
	} else {
		receipt, err = repo.RejectControlProposal(r.Context(), q)
	}
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, receipt)
}
