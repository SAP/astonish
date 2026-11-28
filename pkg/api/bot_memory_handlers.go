package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func registerBotMemoryRoutes(router *mux.Router) {
	const root = "/api/studio/bots/{botID}/memory"
	router.HandleFunc(root, BotMemoryListHandler).Methods(http.MethodGet)
	router.HandleFunc(root, BotMemoryCreateHandler).Methods(http.MethodPost)
	router.HandleFunc(root+"/search", BotMemorySearchHandler).Methods(http.MethodGet)
	router.HandleFunc(root+"/{memoryID}", BotMemoryEntryHandler).Methods(http.MethodGet, http.MethodPut, http.MethodDelete)
}

// Compose only from the authenticated Bot scope and its tenant router. The
// generic Chat memory/search services may span scopes and must never be used.
func botRequestMemory(w http.ResponseWriter, r *http.Request) (*store.BotMemoryService, bool) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return nil, false
	}
	id, ok := botPathID(w, r)
	if !ok {
		return nil, false
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return nil, false
	}
	if b == nil {
		botHTTPError(w, store.ErrBotNotFound)
		return nil, false
	}
	scope := store.BotScopePersonal
	if r.URL.Query().Get("scope") != "" {
		scope = store.BotScope(r.URL.Query().Get("scope"))
	}
	if b.ID != id || b.Scope != scope {
		botHTTPError(w, store.ErrBotForbidden)
		return nil, false
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.TenantRouter == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, false
	}
	tc := store.TenantContextFrom(r.Context())
	org, err := svc.TenantRouter.ForOrg(tc.OrgSlug)
	if err != nil {
		botHTTPError(w, err)
		return nil, false
	}
	if org == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, false
	}
	var memory store.MemoryStore
	switch scope {
	case store.BotScopePersonal:
		if personal := org.ForUser(actor.String()); personal != nil {
			memory = personal.Memories()
		}
	case store.BotScopeTeam:
		if tc.TeamSlug != "" {
			if team := org.ForTeam(tc.TeamSlug); team != nil {
				memory = team.Memories()
			}
		}
	}
	service, err := store.NewBotMemoryService(repo, memory, id, scope)
	if err != nil {
		botHTTPError(w, err)
		return nil, false
	}
	return service, true
}

func botMemoryInt(w http.ResponseWriter, r *http.Request, key string, fallback, min, max int) (int, bool) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return fallback, true
	}
	if len(values) == 1 {
		n, err := strconv.Atoi(values[0])
		if err == nil && n >= min && n <= max {
			return n, true
		}
	}
	botHTTPError(w, store.ErrBotInvalid)
	return 0, false
}
func botMemoryRows(w http.ResponseWriter, rows []store.MemorySearchResult, err error) {
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if rows == nil {
		rows = []store.MemorySearchResult{}
	}
	botWriteJSON(w, http.StatusOK, struct {
		Memories []store.MemorySearchResult `json:"memories"`
	}{rows})
}
func BotMemoryListHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	limit, ok := botMemoryInt(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	offset, ok := botMemoryInt(w, r, "offset", 0, 0, 1000000)
	if !ok {
		return
	}
	rows, err := service.List(r.Context(), r.URL.Query().Get("category"), limit, offset)
	botMemoryRows(w, rows, err)
}
func BotMemorySearchHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || len(query) > 4096 {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	limit, ok := botMemoryInt(w, r, "limit", 10, 1, 200)
	if !ok {
		return
	}
	rows, err := service.SearchByCategory(r.Context(), query, limit, 0, r.URL.Query().Get("category"))
	botMemoryRows(w, rows, err)
}

type botMemoryCreateDTO struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Category  string `json:"category"`
}

func BotMemoryCreateHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	var q botMemoryCreateDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	session, err := uuid.Parse(q.SessionID)
	if err != nil || session == uuid.Nil || strings.TrimSpace(q.Content) == "" {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	if err := service.Add(r.Context(), store.MemoryEntry{SessionID: session.String(), Content: q.Content, Category: q.Category}); err != nil {
		botHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
func BotMemoryEntryHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(mux.Vars(r)["memoryID"])
	if err != nil || id == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	switch r.Method {
	case http.MethodGet:
		row, err := service.Get(r.Context(), id.String())
		if err != nil {
			botHTTPError(w, err)
			return
		}
		if row == nil {
			botHTTPError(w, store.ErrBotNotFound)
			return
		}
		botWriteJSON(w, http.StatusOK, row)
	case http.MethodPut:
		var q struct {
			Content  string `json:"content"`
			Category string `json:"category"`
		}
		if !botDecodeRequest(w, r, &q) {
			return
		}
		if strings.TrimSpace(q.Content) == "" {
			botHTTPError(w, store.ErrBotInvalid)
			return
		}
		if err := service.Update(r.Context(), id.String(), q.Content, q.Category); err != nil {
			botHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := service.Delete(r.Context(), id.String()); err != nil {
			botHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
