package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// Definition DTOs intentionally omit ownership, grants, active triggers and storage
// bindings. Those need independent reviewed service operations, not mass assignment.
type botDefinitionDTO struct {
	Charter string            `json:"charter"`
	Persona *store.BotPersona `json:"persona,omitempty"`
}
type botCreateDTO struct {
	Name           string           `json:"name"`
	IdempotencyKey string           `json:"idempotency_key"`
	Definition     botDefinitionDTO `json:"definition"`
	StartFromTitle bool             `json:"start_from_title"`
}
type botVersionDTO struct {
	ExpectedRevision int64            `json:"expected_revision"`
	ChangeReason     string           `json:"change_reason"`
	Definition       botDefinitionDTO `json:"definition"`
}

func registerBotRoutes(router *mux.Router) {
	router.Use(botContentAccess)
	router.HandleFunc("/api/studio/bots/{botID}/home", BotHomeSetupHandler).Methods(http.MethodPost)
	registerBotFileRoutes(router)
	registerBotSnapshotRoutes(router)
	registerBotUnfinishedIntakeRoutes(router)
	registerBotConfigurationRoutes(router)
	registerBotRunRoutes(router)
	registerBotTriggerRoutes(router)
	router.HandleFunc("/api/studio/bots/{botID}/sessions/{sessionID}/stream", BotHistoryStreamHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/sessions/{sessionID}/events", BotEventsHandler).Methods(http.MethodGet)
	registerBotTemplateRoutes(router)
	registerBotGoalRoutes(router)
	registerBotMemoryRoutes(router)
	registerBotDocumentRoutes(router)
	registerBotSkillRoutes(router)
	router.HandleFunc("/api/studio/bots/{botID}/skills/import-legacy", BotLegacySkillImportHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/permanent-delete", BotDeletionStatusHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/permanent-delete", BotPermanentDeleteHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/pause", BotPauseHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/stop", botLifecycleHandler(botdomain.LifecycleStop)).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/restore", botLifecycleHandler(botdomain.LifecycleRestore)).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}", botLifecycleHandler(botdomain.LifecycleDelete)).Methods(http.MethodDelete)
	router.HandleFunc("/api/studio/bots/{botID}/read", BotMarkReadHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots", BotCreateHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots", BotListHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}", BotGetHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/versions", BotVersionHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/versions/{version}", BotGetVersionHandler).Methods(http.MethodGet)
}

type botLifecycleDTO struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

// The route supplies a typed event; request bodies cannot request activation or
// supply a clock, actor, retention deadline or cleanup confirmation.
func botLifecycleHandler(event botdomain.LifecycleEvent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repo, actor, ok := botRequestStore(w, r)
		if !ok {
			return
		}
		id, ok := botPathID(w, r)
		if !ok {
			return
		}
		var q botLifecycleDTO
		if !botDecodeRequest(w, r, &q) {
			return
		}
		var apply func(context.Context, store.BotLifecycleRequest) (*store.Bot, error)
		switch event {
		case botdomain.LifecyclePause:
			apply = repo.Pause
		case botdomain.LifecycleStop:
			apply = repo.Stop
		case botdomain.LifecycleDelete:
			apply = repo.BeginDelete
		case botdomain.LifecycleRestore:
			apply = repo.Restore
		default:
			botHTTPError(w, store.ErrBotUnsupported)
			return
		}
		result, err := apply(r.Context(), store.BotLifecycleRequest{BotID: id, ActorID: actor, ExpectedRevision: q.ExpectedRevision, Reason: q.Reason})
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusOK, result)
	}
}
func BotPauseHandler(w http.ResponseWriter, r *http.Request) {
	botLifecycleHandler(botdomain.LifecyclePause)(w, r)
}

func BotMarkReadHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q struct {
		Sequence *int64 `json:"sequence"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if q.Sequence == nil {
		http.Error(w, "Read sequence required", http.StatusBadRequest)
		return
	}
	if err := repo.MarkRead(r.Context(), store.BotReadCursor{BotID: id, ViewerID: actor, Sequence: *q.Sequence}); err != nil {
		botHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func BotCreateHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	var q botCreateDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	v, err := store.NewBotVersionForCreation(r.Context(), actor)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	v.Charter = q.Definition.Charter
	if strings.TrimSpace(v.Charter) == "" {
		v.Charter = "Take responsibility for the owner's goals and deliver useful, verified outcomes. Perform routine work within your authorized mandate and existing permissions without extra review. Escalate genuine ambiguity or actions outside that scope for clarification or authorization. A request or this charter does not itself grant permission for external side effects."
		if q.StartFromTitle {
			v.Charter = strings.TrimSpace(q.Name) + "\n\n" + v.Charter
		}
	}
	if q.Definition.Persona != nil {
		v.Persona = *q.Definition.Persona
	}
	request := store.BotCreateRequest{Name: q.Name, OwnerID: actor, Version: v, IdempotencyKey: q.IdempotencyKey}
	if q.StartFromTitle {
		// Use the already-authorized scoped repository; never fall back to a
		// different tenant or to inert creation after an explicit start request.
		titles, ok := repo.(store.BotTitleStartStore)
		if !ok {
			botHTTPError(w, store.ErrBotUnsupported)
			return
		}
		ctx, err := botInteractiveAdmissionContext(r)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		receipt, err := titles.CreateAndStart(ctx, request)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		botWriteJSON(w, http.StatusCreated, struct {
			store.Bot
			InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
		}{receipt.Bot, &store.BotMessageReceipt{Run: receipt.Run, HomeSessionID: receipt.HomeSessionID, UserEventID: receipt.UserEventID, HistoryCursor: receipt.HistoryCursor}})
		return
	}
	b, err := repo.Create(r.Context(), request)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusCreated, b)
}

// botListDTO adds read-only Home conversation metadata only to the list endpoint.
type botListDTO struct {
	store.Bot
	MessageCount  *int       `json:"message_count,omitempty"`
	HomeUpdatedAt *time.Time `json:"home_updated_at,omitempty"`
}

func botListHomeMetadata(r *http.Request, b store.Bot) botListDTO {
	row := botListDTO{Bot: b}
	scope := store.BotScopePersonal
	if value := r.URL.Query().Get("scope"); value != "" {
		scope = store.BotScope(value)
	}
	svc := store.FromContext(r.Context())
	if svc == nil || b.Scope != scope || b.ID == (store.BotID{}) || b.HomeSessionID == (store.BotID{}) {
		return row
	}
	sessions := svc.PersonalSessions
	if scope == store.BotScopeTeam {
		sessions = svc.Sessions
	}
	if sessions == nil {
		return row
	}
	meta, err := sessions.GetSessionMeta(r.Context(), b.HomeSessionID.String())
	if err != nil || meta == nil || meta.ID != b.HomeSessionID.String() || meta.BotID != b.ID.String() || meta.UserID != b.OwnerID.String() || meta.Purpose != store.SessionPurposeBotHome || meta.Retention != store.SessionRetentionBot || meta.BotRunID != "" || meta.ParentID != "" {
		return row
	}
	if meta.MessageCount >= 0 {
		row.MessageCount = &meta.MessageCount
	}
	if !meta.UpdatedAt.IsZero() {
		row.HomeUpdatedAt = &meta.UpdatedAt
	}
	return row
}

func BotListHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	page, ok := botPageRequest(w, r)
	if !ok {
		return
	}
	bots := []store.Bot{}
	for len(bots) < page.Limit {
		remaining := page.Limit - len(bots)
		batch, err := repo.List(r.Context(), store.BotPage{Limit: remaining, After: page.After})
		if err != nil {
			botHTTPError(w, err)
			return
		}
		for _, b := range batch {
			if !b.PermanentDelete {
				bots = append(bots, b)
			}
		}
		if len(batch) < remaining {
			break
		}
		next := batch[len(batch)-1].ID.String()
		if next == page.After {
			botHTTPError(w, store.ErrBotConflict)
			return
		}
		page.After = next
	}
	rows := make([]botListDTO, 0, len(bots))
	for _, b := range bots {
		rows = append(rows, botListHomeMetadata(r, b))
	}
	botWriteJSON(w, http.StatusOK, struct {
		Bots []botListDTO `json:"bots"`
	}{rows})
}
func BotGetHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
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
	botWriteJSON(w, http.StatusOK, b)
}
func BotGetVersionHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(mux.Vars(r)["version"], 10, 64)
	if err != nil || version < 1 {
		http.Error(w, "Invalid Bot version", 400)
		return
	}
	v, err := repo.GetVersion(r.Context(), id, version)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, v)
}
func BotVersionHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q botVersionDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if q.ExpectedRevision < 1 || strings.TrimSpace(q.ChangeReason) == "" {
		http.Error(w, "Reviewed revision and change reason required", 400)
		return
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	v, err := repo.GetVersion(r.Context(), id, b.Version)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	v.Charter = q.Definition.Charter
	v.ChangeReason = q.ChangeReason
	if q.Definition.Persona != nil {
		v.Persona = *q.Definition.Persona
	}
	updated, err := repo.ApplyVersion(r.Context(), store.BotVersionRequest{BotID: id, ExpectedRevision: q.ExpectedRevision, Version: *v})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, updated)
}
