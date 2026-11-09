package api

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/store"
)

// Goal edits cannot assign completion state, evidence, ownership or file paths.
type botGoalDTO struct {
	ExpectedRevision   int64      `json:"expected_revision"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	CompletionCriteria string     `json:"completion_criteria"`
	DueAt              *time.Time `json:"due_at,omitempty"`
}

// This endpoint records the authenticated user's confirmation, not a claim that
// a worker independently observed evidence. The timestamp is stable on retries.
type botConfirmationDTO struct {
	Summary    string    `json:"summary"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

func (d botConfirmationDTO) evidence(actor uuid.UUID) store.BotEvidence {
	return store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: d.Summary, Source: d.Source, ObservedAt: d.ObservedAt, ActorID: actor}
}

type botGoalCompletionDTO struct {
	ExpectedRevision int64              `json:"expected_revision"`
	Evidence         botConfirmationDTO `json:"evidence"`
}
type botCorrectionDTO struct {
	ID               uuid.UUID          `json:"id"`
	ExpectedRevision int64              `json:"expected_revision"`
	SupersededSource string             `json:"superseded_source"`
	Replacement      string             `json:"replacement"`
	Evidence         botConfirmationDTO `json:"evidence"`
	GoalIDs          []uuid.UUID        `json:"goal_ids"`
}

func registerBotGoalRoutes(router *mux.Router) {
	router.HandleFunc("/api/studio/bots/{botID}/goals", BotGoalsHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/goals", BotSaveGoalHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/goals/{goalID}", BotSaveGoalHandler).Methods(http.MethodPut)
	router.HandleFunc("/api/studio/bots/{botID}/goals/{goalID}/complete", BotCompleteGoalHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/goals/{goalID}/tracked-items", BotTrackedItemsHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/goals/{goalID}/tracked-items/{itemID}", BotSaveTrackedItemHandler).Methods(http.MethodPut)
	router.HandleFunc("/api/studio/bots/{botID}/corrections", BotCorrectionHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/corrections/{correctionID}/reconcile", BotReconcileCorrectionHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/activity", BotActivityHandler).Methods(http.MethodGet)
}
func botGoalPathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)["goalID"])
	if err != nil || id == uuid.Nil {
		http.Error(w, "Invalid goal ID", 400)
		return uuid.Nil, false
	}
	return id, true
}
func BotGoalsHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	goals, err := repo.ListGoals(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if goals == nil {
		goals = []store.BotGoal{}
	}
	botWriteJSON(w, 200, struct {
		Goals []store.BotGoal `json:"goals"`
	}{goals})
}
func BotSaveGoalHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q botGoalDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	goal := store.BotGoal{BotID: id, Title: q.Title, Description: q.Description, CompletionCriteria: q.CompletionCriteria, DueAt: q.DueAt, Status: store.BotGoalActive}
	status := http.StatusCreated
	if r.Method == http.MethodPut {
		goalID, ok := botGoalPathID(w, r)
		if !ok {
			return
		}
		goal.ID = goalID
		status = http.StatusOK
		if q.ExpectedRevision < 1 {
			http.Error(w, "Reviewed goal revision required", 400)
			return
		}
	} else if q.ExpectedRevision != 0 {
		http.Error(w, "New goal revision must be zero", 400)
		return
	}
	g, err := repo.SaveGoal(r.Context(), store.BotGoalUpdate{BotID: id, Goal: goal, ExpectedRevision: q.ExpectedRevision})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, status, g)
}
func BotCompleteGoalHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	goalID, ok := botGoalPathID(w, r)
	if !ok {
		return
	}
	var q botGoalCompletionDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	g, err := repo.CompleteGoal(r.Context(), store.BotGoalCompletion{BotID: id, GoalID: goalID, ExpectedRevision: q.ExpectedRevision, ActorID: actor, Evidence: []store.BotEvidence{q.Evidence.evidence(actor)}})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, 200, g)
}
func BotCorrectionHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q botCorrectionDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	err := repo.Correct(r.Context(), store.BotCorrection{ID: q.ID, BotID: id, ExpectedRevision: q.ExpectedRevision, SupersededSource: q.SupersededSource, Replacement: q.Replacement, Evidence: q.Evidence.evidence(actor), GoalIDs: q.GoalIDs})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	// Accepted means recorded for reconciliation, not applied to external stores.
	botWriteJSON(w, http.StatusAccepted, struct {
		ID     uuid.UUID           `json:"id"`
		Intent store.BotOutboxKind `json:"intent"`
	}{q.ID, store.BotOutboxReconcile})
}

// BotReconcileCorrectionHandler applies only the repository's saved intent.
// This bodyless action cannot replace its actor, evidence, source or replacement.
func BotReconcileCorrectionHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	correctionID, err := uuid.Parse(mux.Vars(r)["correctionID"])
	if err != nil || correctionID == uuid.Nil {
		http.Error(w, "Invalid correction ID", http.StatusBadRequest)
		return
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			http.Error(w, "Reconciliation does not accept a request body", http.StatusBadRequest)
			return
		}
	}
	b, err := repo.Get(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	reconciler, ok := repo.(store.BotCorrectionReconciler)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	svc := store.FromRequest(r)
	if svc == nil || svc.TenantRouter == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	tc := store.TenantContextFrom(r.Context())
	org, err := svc.TenantRouter.ForOrg(tc.OrgSlug)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if org == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	var memories store.MemoryStore
	switch b.Scope {
	case store.BotScopePersonal:
		if personal := org.ForUser(actor.String()); personal != nil {
			memories = personal.Memories()
		}
	case store.BotScopeTeam:
		if team := org.ForTeam(tc.TeamSlug); team != nil {
			memories = team.Memories()
		}
	}
	if memories == nil {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	// The repository rechecks current write permission, fetches the saved
	// correction and verifies same-database memory/session attribution even on retry.
	if err := reconciler.ReconcileCorrection(r.Context(), id, correctionID, memories); err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, struct {
		ID            uuid.UUID `json:"id"`
		MemoryApplied bool      `json:"memory_applied"`
	}{correctionID, true})
}

type botTrackedItemDTO struct {
	ExpectedGoalRevision int64                  `json:"expected_goal_revision"`
	Title                string                 `json:"title"`
	Status               store.BotTrackedStatus `json:"status"`
	Reason               string                 `json:"reason"`
	DueAt                *time.Time             `json:"due_at,omitempty"`
	Evidence             []botConfirmationDTO   `json:"evidence"`
}

func BotTrackedItemsHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	goalID, ok := botGoalPathID(w, r)
	if !ok {
		return
	}
	items, err := repo.ListTrackedItems(r.Context(), id, goalID)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if items == nil {
		items = []store.BotTrackedItem{}
	}
	botWriteJSON(w, 200, struct {
		Items []store.BotTrackedItem `json:"items"`
	}{items})
}
func BotSaveTrackedItemHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	goalID, ok := botGoalPathID(w, r)
	if !ok {
		return
	}
	itemID, err := uuid.Parse(mux.Vars(r)["itemID"])
	if err != nil || itemID == uuid.Nil {
		http.Error(w, "Invalid tracked item ID", 400)
		return
	}
	var q botTrackedItemDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	item := store.BotTrackedItem{ID: itemID, GoalID: goalID, Title: q.Title, Status: q.Status, Reason: q.Reason, DueAt: q.DueAt}
	for _, e := range q.Evidence {
		item.Evidence = append(item.Evidence, e.evidence(actor))
	}
	if err := repo.SaveTrackedItem(r.Context(), id, item, q.ExpectedGoalRevision); err != nil {
		botHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func BotActivityHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	after := int64(0)
	limit := 50
	if raw := r.URL.Query().Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "Invalid activity cursor", 400)
			return
		}
		after = n
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			http.Error(w, "Invalid activity limit", 400)
			return
		}
		limit = n
	}
	events, err := repo.ListActivity(r.Context(), id, after, limit)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if events == nil {
		events = []store.BotActivity{}
	}
	botWriteJSON(w, 200, struct {
		Activity []store.BotActivity `json:"activity"`
	}{events})
}
