package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/store"
)

// DeckPublishRequest is the payload for publishing a personal deck to a team.
type DeckPublishRequest struct {
	Slug string `json:"slug"`
}

// DeckForkRequest is the payload for forking a team deck to personal.
type DeckForkRequest struct {
	Slug   string `json:"slug"`
	Source string `json:"source"` // "team"
}

// SlidesPublishToTeamHandler copies a personal deck (manifest + all slides)
// into the team docs scope, re-keying every id via slides.Service.CopyDeckTo.
//
//	POST /api/docs/slides/publish
//
// Platform mode only. Mirrors AppPublishToTeamHandler.
func SlidesPublishToTeamHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	if pu := RequireAuth(w, r); pu == nil {
		return
	}

	var req DeckPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Slug == "" {
		respondError(w, http.StatusBadRequest, "slug is required")
		return
	}

	if svc.PersonalDocs == nil {
		respondError(w, http.StatusServiceUnavailable, "personal docs store not available")
		return
	}
	if svc.Docs == nil {
		respondError(w, http.StatusServiceUnavailable, "team docs store not available")
		return
	}

	src := slides.Service{Store: svc.PersonalDocs}
	dst := slides.Service{Store: svc.Docs}
	newDeck, err := src.CopyDeckTo(r.Context(), dst, req.Slug)
	if err != nil {
		respondDeckCopyError(w, err, "publish deck to team")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"published": true,
		"slug":      newDeck.Slug,
		"scope":     "team",
		"message":   fmt.Sprintf("Deck '%s' published to team", newDeck.Slug),
	})
}

// SlidesForkToPersonalHandler copies a team deck back into the user's personal
// docs scope, re-keying every id.
//
//	POST /api/docs/slides/fork
//
// Platform mode only. Mirrors AppForkToPersonalHandler (team→personal only).
func SlidesForkToPersonalHandler(w http.ResponseWriter, r *http.Request) {
	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	if pu := RequireAuth(w, r); pu == nil {
		return
	}

	var req DeckForkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Slug == "" {
		respondError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if req.Source != "team" {
		respondError(w, http.StatusBadRequest, "source must be 'team'")
		return
	}

	if svc.Docs == nil {
		respondError(w, http.StatusServiceUnavailable, "team docs store not available")
		return
	}
	if svc.PersonalDocs == nil {
		respondError(w, http.StatusServiceUnavailable, "personal docs store not available")
		return
	}

	src := slides.Service{Store: svc.Docs}
	dst := slides.Service{Store: svc.PersonalDocs}
	newDeck, err := src.CopyDeckTo(r.Context(), dst, req.Slug)
	if err != nil {
		respondDeckCopyError(w, err, "fork deck to personal")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"forked":  true,
		"slug":    newDeck.Slug,
		"scope":   "personal",
		"source":  req.Source,
		"message": fmt.Sprintf("Deck '%s' forked from team to personal", newDeck.Slug),
	})
}

// respondDeckCopyError maps CopyDeckTo failures to HTTP status codes: a missing
// source deck is 404, an existing destination deck is 409, everything else 500.
func respondDeckCopyError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, store.ErrDocsNotFound):
		respondError(w, http.StatusNotFound, "source deck not found")
	case errors.Is(err, slides.ErrDeckExists):
		respondError(w, http.StatusConflict, "a deck with that slug already exists in the destination scope")
	default:
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to %s: %v", op, err))
	}
}
