package api

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func registerBotSkillRoutes(router *mux.Router) {
	root := "/api/studio/bots/{botID}/skills"
	router.HandleFunc(root, BotSkillsHandler).Methods(http.MethodGet)
	router.HandleFunc(root+"/{skillID}", BotSkillHandler).Methods(http.MethodGet)
	router.HandleFunc(root+"/{skillID}/head", BotSkillHeadHandler).Methods(http.MethodGet)
	router.HandleFunc(root+"/{skillID}/revisions", BotSkillRevisionsHandler).Methods(http.MethodGet)
	router.HandleFunc(root+"/{skillID}/revisions/{revision}", BotSkillRevisionHandler).Methods(http.MethodGet)
	router.HandleFunc(root+"/{skillID}/runs", BotSkillRunsHandler).Methods(http.MethodGet)
}

// Resolve the optional capability only from the authenticated scoped Bot store.
// These handlers never orient, convert legacy records, or otherwise mutate on GET.
func botRequestSkills(w http.ResponseWriter, r *http.Request) (store.BotSkillStore, uuid.UUID, bool) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}
	id, ok := botPathID(w, r)
	if !ok {
		return nil, uuid.Nil, false
	}
	skills, ok := repo.(store.BotSkillStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return nil, uuid.Nil, false
	}
	return skills, id, true
}

func botSkillID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["skillID"])
	if err != nil || id == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return uuid.Nil, false
	}
	return id, true
}

func botSkillQuery(w http.ResponseWriter, r *http.Request, paged, archived, revisionCursor bool) (store.BotSkillPage, bool) {
	page := store.BotSkillPage{Limit: 50}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		botHTTPError(w, store.ErrBotInvalid)
		return page, false
	}
	for key, values := range query {
		if len(values) != 1 {
			botHTTPError(w, store.ErrBotInvalid)
			return page, false
		}
		switch key {
		case "scope": // botRequestStore owns scope validation and authorization.
		case "limit":
			if !paged {
				botHTTPError(w, store.ErrBotInvalid)
				return page, false
			}
			page.Limit, err = strconv.Atoi(values[0])
			if err != nil || page.Limit < 1 || page.Limit > store.BotSkillMaxPageSize {
				botHTTPError(w, store.ErrBotInvalid)
				return page, false
			}
		case "after":
			if !paged || values[0] == "" {
				botHTTPError(w, store.ErrBotInvalid)
				return page, false
			}
			page.After = values[0]
			if revisionCursor {
				n, err := strconv.ParseInt(page.After, 10, 64)
				if err != nil || n < 1 || n > store.BotSkillMaxRevisions || strconv.FormatInt(n, 10) != page.After {
					botHTTPError(w, store.ErrBotInvalid)
					return page, false
				}
			} else if id, err := uuid.Parse(page.After); err != nil || id == uuid.Nil || id.String() != page.After {
				botHTTPError(w, store.ErrBotInvalid)
				return page, false
			}
		case "include_archived":
			if !archived || (values[0] != "true" && values[0] != "false") {
				botHTTPError(w, store.ErrBotInvalid)
				return page, false
			}
			page.IncludeArchived = values[0] == "true"
		default:
			botHTTPError(w, store.ErrBotInvalid)
			return page, false
		}
	}
	if err := page.Validate(); err != nil {
		botHTTPError(w, err)
		return page, false
	}
	return page, true
}

func BotSkillsHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	page, ok := botSkillQuery(w, r, true, true, false)
	if !ok {
		return
	}
	result, err := repo.ListSkills(r.Context(), id, page)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if result == nil {
		result = &store.BotSkillList{}
	}
	if result.Skills == nil {
		result.Skills = []store.BotSkill{}
	}
	botWriteJSON(w, http.StatusOK, result)
}
func BotSkillHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	skill, ok := botSkillID(w, r)
	if !ok {
		return
	}
	if _, ok := botSkillQuery(w, r, false, false, false); !ok {
		return
	}
	result, err := repo.GetSkill(r.Context(), id, skill)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, result)
}
func BotSkillHeadHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	skill, ok := botSkillID(w, r)
	if !ok {
		return
	}
	if _, ok := botSkillQuery(w, r, false, false, false); !ok {
		return
	}
	head, err := repo.GetSkill(r.Context(), id, skill)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if head == nil {
		botHTTPError(w, store.ErrBotNotFound)
		return
	}
	result, err := repo.GetSkillRevision(r.Context(), id, skill, head.HeadRevision)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, result)
}
func BotSkillRevisionHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	skill, ok := botSkillID(w, r)
	if !ok {
		return
	}
	if _, ok := botSkillQuery(w, r, false, false, false); !ok {
		return
	}
	raw := mux.Vars(r)["revision"]
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 || revision > store.BotSkillMaxRevisions || strconv.FormatInt(revision, 10) != raw {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	result, err := repo.GetSkillRevision(r.Context(), id, skill, revision)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, result)
}
func BotSkillRevisionsHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	skill, ok := botSkillID(w, r)
	if !ok {
		return
	}
	page, ok := botSkillQuery(w, r, true, false, true)
	if !ok {
		return
	}
	result, err := repo.ListSkillRevisions(r.Context(), id, skill, page)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if result == nil {
		result = &store.BotSkillRevisionList{}
	}
	if result.Revisions == nil {
		result.Revisions = []store.BotSkillRevision{}
	}
	botWriteJSON(w, http.StatusOK, result)
}
func BotSkillRunsHandler(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := botRequestSkills(w, r)
	if !ok {
		return
	}
	skill, ok := botSkillID(w, r)
	if !ok {
		return
	}
	page, ok := botSkillQuery(w, r, true, false, false)
	if !ok {
		return
	}
	result, err := repo.ListSkillRuns(r.Context(), id, skill, page)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if result == nil {
		result = &store.BotSkillRunList{}
	}
	if result.Runs == nil {
		result.Runs = []store.BotRun{}
	}
	botWriteJSON(w, http.StatusOK, result)
}
