package api

import (
	"github.com/SAP/astonish/pkg/store"
	"net/http"
)

// BotCancelRunHandler requests durable cancellation, not physical teardown.
// It accepts no client-supplied lease, scope, actor or cleanup attestation.
func BotCancelRunHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	rid, ok := botRuntimePathID(w, r, "runID")
	if !ok {
		return
	}
	cancel, ok := repo.(store.BotRunCancellationStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	run, err := cancel.CancelRun(r.Context(), id, rid)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusAccepted, run)
}
