package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"net/http"
)

// botEnsureExecutionHome is only called after explicit execution consent. Legacy
// embeddings without the setup capability keep their existing readiness contract.
func botEnsureExecutionHome(r *http.Request, repo store.BotStore, b *store.Bot) (*store.Bot, error) {
	svc := store.FromRequest(r)
	if svc == nil || svc.BotHomeSetup == nil {
		return b, nil
	}
	if _, err := entstore.AuthorizeBotHomeSetup(r.Context(), repo, b.ID, b.Revision); err != nil {
		return nil, err
	}
	return svc.BotHomeSetup(r.Context(), repo, b.ID, b.Revision)
}

// BotHomeSetupHandler accepts intent only; backend, owner, storage identity and
// registration authority are supplied exclusively by trusted daemon composition.
func BotHomeSetupHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if _, err := entstore.AuthorizeBotHomeSetup(r.Context(), repo, id, q.ExpectedRevision); err != nil {
		botHTTPError(w, err)
		return
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.BotHomeSetup == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	b, err := svc.BotHomeSetup(r.Context(), repo, id, q.ExpectedRevision)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, b)
}
