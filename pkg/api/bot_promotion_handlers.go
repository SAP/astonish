package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http"
)

func registerBotPromotionRoutes(router *mux.Router) {
	registerBotSourceIntakeRoutes(router)
	router.HandleFunc("/api/studio/bots/promote-chat/{sessionID}", BotPromoteChatHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/convert-adaptive/{jobID}", BotReviewAdaptiveHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/convert-adaptive/{jobID}", BotConvertAdaptiveHandler).Methods(http.MethodPost)
}
func botIntakeRequest(w http.ResponseWriter, r *http.Request, key string) (store.BotIntakeStore, uuid.UUID, bool) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(mux.Vars(r)[key])
	if err != nil || id == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return nil, uuid.Nil, false
	}
	intake, ok := repo.(store.BotIntakeStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, uuid.Nil, false
	}
	return intake, id, true
}
func BotPromoteChatHandler(w http.ResponseWriter, r *http.Request) {
	intake, id, ok := botIntakeRequest(w, r, "sessionID")
	if !ok {
		return
	}
	var q store.BotPromotionRequest
	if !botDecodeRequest(w, r, &q) {
		return
	}
	q.SourceSessionID = id
	b, err := intake.PromoteChat(r.Context(), q)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusCreated, b)
}
func BotReviewAdaptiveHandler(w http.ResponseWriter, r *http.Request) {
	intake, id, ok := botIntakeRequest(w, r, "jobID")
	if !ok {
		return
	}
	review, err := intake.ReviewAdaptiveJob(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, review)
}
func BotConvertAdaptiveHandler(w http.ResponseWriter, r *http.Request) {
	intake, id, ok := botIntakeRequest(w, r, "jobID")
	if !ok {
		return
	}
	var q store.BotConversionRequest
	if !botDecodeRequest(w, r, &q) {
		return
	}
	q.SourceJobID = id
	b, err := intake.ConvertAdaptiveJob(r.Context(), q)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusCreated, b)
}
