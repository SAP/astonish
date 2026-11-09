package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/store"
)

func registerBotTemplateRoutes(router *mux.Router) {
	// Fixed template routes precede the Bot-ID route.
	router.HandleFunc("/api/studio/bots/templates", BotTemplatesHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/templates", BotPublishTemplateHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/templates/{templateID}", BotGetTemplateHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/templates/{templateID}/fork", BotForkTemplateHandler).Methods(http.MethodPost)
}
func botTemplateRequestStore(w http.ResponseWriter, r *http.Request) (store.BotTemplateStore, bool) {
	if _, _, ok := botRequestStore(w, r); !ok {
		return nil, false
	}
	if r.URL.Query().Get("scope") != string(store.BotScopeTeam) {
		http.Error(w, "Templates require explicit team scope", 400)
		return nil, false
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.BotTemplates == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, false
	}
	return svc.BotTemplates, true
}
func botTemplatePathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["templateID"])
	if err != nil || id == uuid.Nil {
		http.Error(w, "Invalid template ID", 400)
		return uuid.Nil, false
	}
	return id, true
}
func BotTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	repo, ok := botTemplateRequestStore(w, r)
	if !ok {
		return
	}
	page, ok := botPageRequest(w, r)
	if !ok {
		return
	}
	templates, err := repo.List(r.Context(), page)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if templates == nil {
		templates = []store.BotTemplate{}
	}
	botWriteJSON(w, 200, struct {
		Templates []store.BotTemplate `json:"templates"`
	}{templates})
}
func BotGetTemplateHandler(w http.ResponseWriter, r *http.Request) {
	repo, ok := botTemplateRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botTemplatePathID(w, r)
	if !ok {
		return
	}
	template, err := repo.Get(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, 200, template)
}
func BotPublishTemplateHandler(w http.ResponseWriter, r *http.Request) {
	repo, ok := botTemplateRequestStore(w, r)
	if !ok {
		return
	}
	var q struct {
		ExpectedRevision int64                       `json:"expected_revision"`
		ReviewedForTeam  bool                        `json:"reviewed_for_team"`
		Definition       store.BotTemplateDefinition `json:"definition"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	// The admin supplies an explicitly reviewed/redacted copy, never a source Bot
	// ID. This acknowledgment is not a claim of automatic free-text sanitization.
	if !q.ReviewedForTeam {
		http.Error(w, "Review and redact the definition for team publication", 400)
		return
	}
	template, err := repo.Publish(r.Context(), q.Definition, q.ExpectedRevision)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	status := http.StatusOK
	if q.ExpectedRevision == 0 {
		status = http.StatusCreated
	}
	botWriteJSON(w, status, template)
}
func BotForkTemplateHandler(w http.ResponseWriter, r *http.Request) {
	repo, ok := botTemplateRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botTemplatePathID(w, r)
	if !ok {
		return
	}
	var q struct {
		TemplateRevision int64  `json:"template_revision"`
		Name             string `json:"name"`
		IdempotencyKey   string `json:"idempotency_key"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	dest := store.BotStoreFromContext(r.Context(), store.BotScopePersonal)
	if dest == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	b, err := repo.ForkToPrivate(r.Context(), dest, store.BotTemplateForkRequest{TemplateID: id, TemplateRevision: q.TemplateRevision, Name: q.Name, IdempotencyKey: q.IdempotencyKey})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusCreated, b)
}
