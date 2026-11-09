package api

import (
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"net/http"
)

// BotPermanentDeleteHandler acknowledges completion only after the exact target
// purge commits. A missing target is 404, never fabricated physical proof.
func BotPermanentDeleteHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q struct {
		ExpectedRevision int64 `json:"expected_revision"`
		Confirm          bool  `json:"confirm"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	deletion, ok := repo.(store.BotPermanentDeleteStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	b, err := deletion.BeginPermanentDelete(r.Context(), store.BotPermanentDeleteRequest{BotID: id, ActorID: actor, ExpectedRevision: q.ExpectedRevision, Confirm: q.Confirm})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	err = deletion.PurgeDeleted(r.Context(), id, b.Revision)
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A worker may consume the authorized intent concurrently. Only our own
	// successful transaction earns 204; otherwise report acceptance, not proof.
	if !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotNotFound) {
		botHTTPError(w, err)
		return
	}
	w.Header().Set("Retry-After", "2")
	botWriteJSON(w, http.StatusAccepted, struct {
		Bot    *store.Bot `json:"bot"`
		Status string     `json:"status"`
	}{b, "pending"})
}
