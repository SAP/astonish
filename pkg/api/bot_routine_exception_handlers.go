package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/http"
	"time"
)

type botOperationViewer interface {
	ReadOperationForViewer(context.Context, uuid.UUID, uuid.UUID) (*store.BotOperation, *store.BotOperationView, error)
}
type botOperationDecisionRequest struct {
	PayloadDigest    string    `json:"payload_digest"`
	ExpectedRevision int64     `json:"expected_revision"`
	ExpiresAt        time.Time `json:"expires_at"`
	Approve          bool      `json:"approve"`
}

// BotOperationDecisionHandler accepts consent coordinates, never authority.
func BotOperationDecisionHandler(w http.ResponseWriter, r *http.Request) {
	var q botOperationDecisionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&q) != nil {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	operationID, ok := botRuntimePathID(w, r, "operationID")
	if !ok {
		return
	}
	decideBotOperation(w, r, operationID, q)
}

// Legacy compatibility: ignore client-supplied authority and resolve it afresh.
func botRoutineExceptionDecision(w http.ResponseWriter, r *http.Request, q *store.BotRoutineExceptionDecision) {
	operationID, ok := botRuntimePathID(w, r, "approvalID")
	if !ok {
		return
	}
	if operationID != q.OperationID {
		botHTTPError(w, store.ErrBotForbidden)
		return
	}
	decideBotOperation(w, r, operationID, botOperationDecisionRequest{q.PayloadDigest, q.ExpectedRevision, q.ExpiresAt, q.Approve})
}
func decideBotOperation(w http.ResponseWriter, r *http.Request, operationID uuid.UUID, q botOperationDecisionRequest) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if b.OwnerID != actor {
		botHTTPError(w, store.ErrBotForbidden)
		return
	}
	reader, ok := repo.(botOperationViewer)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	operation, _, err := reader.ReadOperationForViewer(r.Context(), id, operationID)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	svc := store.FromContext(r.Context())
	if svc == nil || svc.BotAuthorizedOperationFactory == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	scope := store.BotScopePersonal
	if r.URL.Query().Get("scope") != "" {
		scope = store.BotScope(r.URL.Query().Get("scope"))
	}
	runtime, err := svc.BotAuthorizedOperationFactory(r.Context(), svc, scope)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	decisions, ok := runtime.(store.BotRoutineExceptionStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	_, err = decisions.DecideRoutineException(r.Context(), store.BotRoutineExceptionDecision{MandateRef: operation.MandateRef, OperationID: operationID, PayloadDigest: q.PayloadDigest, ExpectedRevision: q.ExpectedRevision, ExpiresAt: q.ExpiresAt, Approve: q.Approve})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	_, view, err := reader.ReadOperationForViewer(r.Context(), id, operationID)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, view)
}
