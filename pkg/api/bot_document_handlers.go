package api

import (
	"net/http"
	"strconv"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func registerBotDocumentRoutes(router *mux.Router) {
	router.HandleFunc("/api/studio/bots/{botID}/documents/{document:.*}/ingest", BotDocumentIngestHandler).Methods(http.MethodPost)
	router.HandleFunc("/api/studio/bots/{botID}/documents", BotDocumentsHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/documents/{document:.*}", BotDocumentEditHandler).Methods(http.MethodPut)
	router.HandleFunc("/api/studio/bots/{botID}/memory-health", BotMemoryHealthHandler).Methods(http.MethodGet)
	router.HandleFunc("/api/studio/bots/{botID}/memory-health/consolidate", BotMemoryConsolidateHandler).Methods(http.MethodPost)
}

func BotDocumentsHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var version int64
	if values, exists := r.URL.Query()["version"]; exists {
		if len(values) != 1 {
			botHTTPError(w, store.ErrBotInvalid)
			return
		}
		var err error
		version, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || version < 1 {
			botHTTPError(w, store.ErrBotInvalid)
			return
		}
	}
	documents, err := botdomain.NewDocuments(repo)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	memory, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	projection, err := documents.LoadMapped(r.Context(), id, version, memory)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, projection)
}

type botDocumentEditDTO struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Content          *string `json:"content"`
	Reason           string  `json:"reason"`
	Confirmed        bool    `json:"confirmed"`
}

// Confirmation comes from an authenticated interactive request, never projected
// prose. The domain service owns the allowlist and existing version transaction.
func BotDocumentEditHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q botDocumentEditDTO
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if q.Content == nil {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	documents, err := botdomain.NewDocuments(repo)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	ack, err := documents.Edit(r.Context(), botdomain.DocumentEdit{BotID: id, ExpectedRevision: q.ExpectedRevision, Name: botdomain.DocumentName(mux.Vars(r)["document"]), Content: *q.Content, Reason: q.Reason, Confirmed: q.Confirmed})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, ack)
}

// BotMemoryHealthHandler reuses the existing evaluator only AFTER Bot prefiltering.
// Recommendations are review-only and never grant rights or delete raw evidence.
func BotMemoryHealthHandler(w http.ResponseWriter, r *http.Request) {
	service, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	limit, ok := botMemoryInt(w, r, "limit", 500, 1, 2000)
	if !ok {
		return
	}
	health, err := botdomain.NewMemoryHealth(service)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	snapshot, err := health.Snapshot(r.Context(), limit)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	report := BuildMemoryHealth(BuildMemoryMap(snapshot.Records), false, snapshot.EvaluatedAt)
	scope := string(store.BotScopePersonal)
	if r.URL.Query().Get("scope") != "" {
		scope = r.URL.Query().Get("scope")
	}
	// The generic evaluator can propose promotion and raw-source cleanup. Those
	// are not permitted through the Bot adapter; source rows remain authoritative.
	allowedMemory := map[string]bool{}
	allowedSession := map[string]bool{}
	for _, row := range snapshot.Records {
		allowedMemory[row.ID] = true
		allowedSession[row.SessionID] = true
	}
	for i := range report.Recommendations {
		recommendation := &report.Recommendations[i]
		recommendation.TargetScope = scope
		recommendation.Card.Scope = scope
		recommendation.Card.Status = memory.ScenarioCardStatusDraft
		recommendation.Card.LastVerifiedAt = ""
		recommendation.Card.SourceMemoryIDs = botHealthKnownIDs(recommendation.Card.SourceMemoryIDs, allowedMemory)
		recommendation.Card.SourceSessionIDs = botHealthKnownIDs(recommendation.Card.SourceSessionIDs, allowedSession)
		recommendation.MemoryIDs = botHealthKnownIDs(recommendation.MemoryIDs, allowedMemory)
		recommendation.DuplicateCardIDs = botHealthKnownIDs(recommendation.DuplicateCardIDs, allowedMemory)
		recommendation.Description = "Review this scoped consolidation suggestion. Applying requires a separate explicitly confirmed proposal with source fingerprints; original evidence is retained."
	}
	report.Generated = true
	botWriteJSON(w, http.StatusOK, struct {
		BotID          store.BotID               `json:"bot_id"`
		Scope          string                    `json:"scope"`
		Snapshot       *botdomain.MemorySnapshot `json:"snapshot"`
		ReviewOnly     bool                      `json:"review_only"`
		ApplySupported bool                      `json:"apply_supported"`
		Health         MemoryHealthResponse      `json:"health"`
	}{BotID: mustBotHealthID(r), Scope: scope, Snapshot: snapshot, ReviewOnly: true, ApplySupported: true, Health: report})
}

func botHealthKnownIDs(ids []string, allowed map[string]bool) []string {
	result := []string{}
	for _, id := range ids {
		if allowed[id] {
			result = append(result, id)
		}
	}
	return result
}

func mustBotHealthID(r *http.Request) store.BotID {
	id, _ := uuid.Parse(mux.Vars(r)["botID"])
	return id
}

// Consolidation and document ingest share one explicit, atomic reviewed operation.
// Neither endpoint executes a model recommendation or imports Markdown authority.
func BotMemoryConsolidateHandler(w http.ResponseWriter, r *http.Request) {
	applyBotMemoryReview(w, r, store.BotMemoryReviewConsolidate, "")
}
func BotDocumentIngestHandler(w http.ResponseWriter, r *http.Request) {
	applyBotMemoryReview(w, r, store.BotMemoryReviewDocument, mux.Vars(r)["document"])
}
func applyBotMemoryReview(w http.ResponseWriter, r *http.Request, kind store.BotMemoryReviewKind, document string) {
	service, ok := botRequestMemory(w, r)
	if !ok {
		return
	}
	var q store.BotMemoryReview
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if (q.Kind != "" && q.Kind != kind) || (q.Document != "" && q.Document != document) {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	q.Kind = kind
	q.Document = document
	receipt, err := service.ApplyReviewed(r.Context(), q)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, receipt)
}
