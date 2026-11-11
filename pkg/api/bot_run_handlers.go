package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/store"
)

// Runtime controls mutate durable admission state only. They never execute on
// an HTTP viewer context or accept owner, lease, checkpoint, clock or trigger
// authority from JSON. The daemon consumes the same production store queue.
func registerBotRunRoutes(r *mux.Router) {
	r.HandleFunc("/api/studio/bots/{botID}/messages", BotMessageHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/test-run", BotTestRunHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/conversation", BotConversationHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/activate", BotActivateHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/runs", BotRequestRunHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/runs", BotRunsHandler).Methods(http.MethodGet)
	r.HandleFunc("/api/studio/bots/{botID}/runs/{runID}", BotRunHandler).Methods(http.MethodGet)
	r.HandleFunc("/api/studio/bots/{botID}/approvals", BotApprovalsHandler).Methods(http.MethodGet)
	r.HandleFunc("/api/studio/bots/{botID}/approvals/{approvalID}/approve-and-continue", BotApproveContinueHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/operations/{operationID}/decision", BotOperationDecisionHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/approvals/{approvalID}/decision", BotApprovalDecisionHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/runs/{runID}/cancel", BotCancelRunHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/runs/{runID}/continue-interactive", BotContinueInteractiveHandler).Methods(http.MethodPost)
	r.HandleFunc("/api/studio/bots/{botID}/runs/{runID}/resume", BotResumeRunHandler).Methods(http.MethodPost)
}

// BotContinueInteractiveHandler requires fresh authentication separately from answering.
func BotContinueInteractiveHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	runID, ok := botRuntimePathID(w, r, "runID")
	if !ok {
		return
	}
	continuation, ok := repo.(store.BotInteractiveContinuationStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	ctx, err := botInteractiveAdmissionContext(r)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	var q store.BotContinueInteractiveRequest
	if !botDecodeRequest(w, r, &q) {
		return
	}
	q.BotID = id
	q.RunID = runID
	run, err := continuation.ContinueInteractive(ctx, q)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusAccepted, run)
}

func BotActivateHandler(w http.ResponseWriter, r *http.Request) {
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
	// The public activate action is explicit consent to activate a draft or resume
	// a paused/stopped Bot. Read through the scoped repository; each mutation
	// rechecks authority, revision and lifecycle state transactionally.
	current, err := repo.Get(r.Context(), id)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if current.Status == store.BotDraft && current.Home.ID == uuid.Nil && current.Revision == q.ExpectedRevision {
		current, err = botEnsureExecutionHome(r, repo, current)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		q.ExpectedRevision = current.Revision
	}
	request := store.BotLifecycleRequest{BotID: id, ActorID: actor, ExpectedRevision: q.ExpectedRevision, Reason: q.Reason}
	var b *store.Bot
	if current.Status == store.BotPaused || current.Status == store.BotStopped {
		resumer, ok := repo.(interface {
			Resume(context.Context, store.BotLifecycleRequest) (*store.Bot, error)
		})
		if !ok {
			botHTTPError(w, store.ErrBotUnsupported)
			return
		}
		b, err = resumer.Resume(r.Context(), request)
	} else {
		b, err = repo.Activate(r.Context(), request)
	}
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, b)
}
func BotMessageHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	if _, err := repo.Get(r.Context(), id); err != nil {
		botHTTPError(w, err)
		return
	}
	messages, ok := repo.(store.BotMessageStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	var q struct {
		Version        int64            `json:"version"`
		IdempotencyKey string           `json:"idempotency_key"`
		Instructions   string           `json:"instructions"`
		Attachments    []ChatAttachment `json:"attachments,omitempty"`
	}
	if !decodeBotAttachmentRequest(w, r, &q) {
		return
	}
	files, err := validateBotAttachments(q.Attachments)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if len(files) > 0 && q.Instructions == "" {
		q.Instructions = "Please review the attached files."
	}
	ctx := r.Context()
	// Until full startup composition opts in, retain the legacy message route.
	// Once enabled, missing evidence/registry fails closed; never downgrade.
	if svc := store.FromContext(ctx); svc != nil && (svc.BotInteractiveReady || svc.BotMandatePolicy != nil) {
		ctx, err = botInteractiveAdmissionContext(r)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		// Physical setup belongs to the shared post-admission worker preflight.
	}
	receipt, err := messages.AdmitMessage(ctx, store.BotMessageRequest{BotID: id, Version: q.Version, IdempotencyKey: q.IdempotencyKey, Instructions: q.Instructions, Attachments: files})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusAccepted, receipt)
}

func BotTestRunHandler(w http.ResponseWriter, r *http.Request) {
	botRequestRun(w, r, store.BotIntentDraftTest)
}
func BotConversationHandler(w http.ResponseWriter, r *http.Request) {
	botRequestRun(w, r, store.BotIntentConversation)
}
func BotRequestRunHandler(w http.ResponseWriter, r *http.Request) {
	botRequestRun(w, r, store.BotIntentRegular)
}
func botRequestRun(w http.ResponseWriter, r *http.Request, intent store.BotExecutionIntent) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	var q struct {
		Version        int64            `json:"version"`
		IdempotencyKey string           `json:"idempotency_key"`
		Instructions   string           `json:"instructions"`
		Attachments    []ChatAttachment `json:"attachments,omitempty"`
	}
	// Resolve scope access before decoding attachments; admission rechecks write authority.
	if _, err := repo.Get(r.Context(), id); err != nil {
		botHTTPError(w, err)
		return
	}
	if !decodeBotAttachmentRequest(w, r, &q) {
		return
	}
	files, err := validateBotAttachments(q.Attachments)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if len(files) > 0 && q.Instructions == "" {
		q.Instructions = "Please review the attached files."
	}
	if intent == store.BotIntentDraftTest {
		current, err := repo.Get(r.Context(), id)
		if err != nil {
			botHTTPError(w, err)
			return
		}
		if current.Status == store.BotDraft && current.Version == q.Version && current.Home.ID == uuid.Nil {
			if _, err := botEnsureExecutionHome(r, repo, current); err != nil {
				botHTTPError(w, err)
				return
			}
		}
	}
	run, err := repo.RequestRun(r.Context(), store.BotRunRequest{Intent: intent, BotID: id, Version: q.Version, Trigger: store.TriggerManual, IdempotencyKey: q.IdempotencyKey, Instructions: q.Instructions, Attachments: files})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusAccepted, run)
}
func BotRunsHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	page, ok := botPageRequest(w, r)
	if !ok {
		return
	}
	runs, err := repo.ListRuns(r.Context(), id, page)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if runs == nil {
		runs = []store.BotRun{}
	}
	botWriteJSON(w, http.StatusOK, struct {
		Runs []store.BotRun `json:"runs"`
	}{runs})
}
func botRuntimePathID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	id, err := uuid.Parse(mux.Vars(r)[key])
	if err != nil || id == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return uuid.Nil, false
	}
	return id, true
}
func BotRunHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	runID, ok := botRuntimePathID(w, r, "runID")
	if !ok {
		return
	}
	run, err := repo.GetRun(r.Context(), id, runID)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, run)
}
func BotApprovalsHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	page, ok := botPageRequest(w, r)
	if !ok {
		return
	}
	approvals, err := repo.ListApprovals(r.Context(), id, page)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if approvals == nil {
		approvals = []store.BotApproval{}
	}
	botWriteJSON(w, http.StatusOK, struct {
		Approvals []store.BotApproval `json:"approvals"`
	}{approvals})
}

// BotApproveContinueHandler records one exact saved decision and its continuation
// atomically. A paused result is intentionally not activation consent.
func BotApproveContinueHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	approvalID, ok := botRuntimePathID(w, r, "approvalID")
	if !ok {
		return
	}
	var q struct {
		ActionDigest string                        `json:"action_digest"`
		Response     *store.BotInteractionResponse `json:"response,omitempty"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	capability, ok := repo.(store.BotApproveContinueStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	result, err := capability.DecideAndRequeueApprovedRun(r.Context(), store.BotApproveContinueRequest{BotID: id, ApprovalID: approvalID, ActorID: actor, ActionDigest: q.ActionDigest, Response: q.Response})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	status := http.StatusAccepted
	if result.Continuation == store.BotContinuationBlockedByPause {
		status = http.StatusOK
	}
	botWriteJSON(w, status, result)
}

func BotApprovalDecisionHandler(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	approvalID, ok := botRuntimePathID(w, r, "approvalID")
	if !ok {
		return
	}
	var q struct {
		RoutineException *store.BotRoutineExceptionDecision `json:"routine_exception,omitempty"`
		ActionDigest     string                             `json:"action_digest"`
		Status           store.ApprovalStatus               `json:"status"`
		Response         *store.BotInteractionResponse      `json:"response,omitempty"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if q.RoutineException != nil {
		if q.Status != "" || q.ActionDigest != "" || q.Response != nil {
			botHTTPError(w, store.ErrBotInvalid)
			return
		}
		botRoutineExceptionDecision(w, r, q.RoutineException)
		return
	}
	if q.Status != store.ApprovalApproved && q.Status != store.ApprovalDenied {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	approval, err := repo.DecideApproval(r.Context(), store.BotApprovalDecision{BotID: id, ApprovalID: approvalID, ActorID: actor, ActionDigest: q.ActionDigest, Status: q.Status, Response: q.Response})
	if err != nil {
		botHTTPError(w, err)
		return
	}
	botWriteJSON(w, http.StatusOK, approval)
}
func BotResumeRunHandler(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := botRequestStore(w, r)
	if !ok {
		return
	}
	id, ok := botPathID(w, r)
	if !ok {
		return
	}
	runID, ok := botRuntimePathID(w, r, "runID")
	if !ok {
		return
	}
	var q struct {
		ApprovalID uuid.UUID `json:"approval_id"`
	}
	if !botDecodeRequest(w, r, &q) {
		return
	}
	if q.ApprovalID == uuid.Nil {
		botHTTPError(w, store.ErrBotInvalid)
		return
	}
	// Reject terminal/non-resumable runs as state conflicts before checkpoint
	// decoding. The repository still rechecks atomically when requeuing.
	run, err := repo.GetRun(r.Context(), id, runID)
	if err != nil {
		botHTTPError(w, err)
		return
	}
	if run.Status != store.RunWaitingApproval && run.Status != store.RunQueued {
		botHTTPError(w, store.ErrBotConflict)
		return
	}
	resume, ok := repo.(store.BotApprovalResumeStore)
	if !ok {
		botHTTPError(w, store.ErrBotUnsupported)
		return
	}
	if err := resume.RequeueApprovedRun(r.Context(), id, runID, q.ApprovalID); err != nil {
		botHTTPError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
