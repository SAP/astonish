package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Uses the recorded Boston payload, real scoped HTTP/SSE, SQLite, and built SPA.
// Only disposable acceptance data is written; the owner's Boston approval is untouched.
func TestBostonQuestionCleanBrowserAcceptance(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	env := setupMemoryHTTPAcceptance(t, &MockLLM{}, nil)
	routes := mux.NewRouter()
	registry := execution.NewBotTurnAuthorizationRegistry(100)
	defer registry.Close()
	var answerRequests, authorizationRetries atomic.Int64
	var denyAnswer atomic.Bool
	routes.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/continue-interactive") {
				authorizationRetries.Add(1)
			}
			if strings.HasSuffix(r.URL.Path, "/approve-and-continue") {
				answerRequests.Add(1)
			}
			// Exercise production tenant/identity denial, not a stubbed HTTP error.
			if denyAnswer.Load() && strings.HasSuffix(r.URL.Path, "/approve-and-continue") {
				p, _ := execution.PrincipalFromContext(r.Context())
				p.OrgSlug = "revoked-membership"
				ctx, err := execution.WithPrincipal(r.Context(), p)
				if err != nil {
					t.Fatal(err)
				}
				r = r.WithContext(ctx)
			}
			svc := *store.FromContext(r.Context())
			svc.BotInteractiveReady = true
			svc.BotTurnRegistry = registry
			ctx, err := execution.WithAuthenticationEvidence(r.Context(), execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(store.WithServices(ctx, &svc)))
		})
	})
	registerBotRoutes(routes)
	env.Router.PathPrefix("/api/studio/bots").Handler(entstore.TenantMiddleware(env.Store)(routes))
	// The recorded text question is an ordinary conversation question, not the
	// newer title-intake select question. Use ordinary draft/message admission.
	created := env.Request(t, http.MethodPost, "/api/studio/bots", map[string]any{"name": "Boston fixture acceptance", "idempotency_key": "boston-question-fixture"}, memoryIdentityAdmin)
	defer created.Body.Close()
	var draft store.Bot
	if created.StatusCode != http.StatusCreated || json.NewDecoder(created.Body).Decode(&draft) != nil {
		t.Fatal("could not create ordinary Boston question fixture")
	}
	message := env.Request(t, http.MethodPost, "/api/studio/bots/"+draft.ID.String()+"/messages", map[string]any{"version": draft.Version, "instructions": "Discuss Boston weather update recipients.", "idempotency_key": "boston-question-message"}, memoryIdentityAdmin)
	defer message.Body.Close()
	if message.StatusCode != http.StatusAccepted {
		t.Fatal("could not admit ordinary Boston question fixture")
	}
	page := newStreamBrowserPage(t, env, bin, dist).CancelTimeout().Timeout(60 * time.Second)
	page.MustNavigate(env.URL + "/#/chat/bots/personal/" + draft.ID.String())
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	id := draft.ID
	org, err := env.Store.ForOrg(env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForUser(env.UserID).Bots()
	owner, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.Get(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := repo.ListRuns(owner, id, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: id, RunID: runs[0].ID, Version: b.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug})
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: id, RunID: runs[0].ID, WorkerID: "question-browser", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/boston-question-approvals.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Approvals []store.BotApproval `json:"approvals"`
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	original := saved.Approvals[0]
	a := original
	a.ID = uuid.Nil
	a.BotID = id
	a.RunID = runs[0].ID
	a.Version = b.Version
	a.ExpiresAt = time.Now().Add(time.Hour)
	// Canonicalize the historical envelope through the current descriptor shape.
	var questionPayload map[string]any
	if err = json.Unmarshal(a.Interaction.Payload, &questionPayload); err != nil {
		t.Fatal(err)
	}
	a.Interaction.Payload, err = json.Marshal(questionPayload)
	if err != nil {
		t.Fatal(err)
	}
	a.Action.Arguments = append(json.RawMessage(nil), a.Interaction.Payload...)
	// Historical action hashes predate the current descriptor envelope.
	a.ActionDigest, err = a.Action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	createdApproval, err := repo.RequestApproval(worker, lease, a)
	if err != nil {
		t.Fatal(err)
	}
	a = *createdApproval
	// The home marker is presentation only. Production answer acceptance also
	// requires the exact correlated ask_user call/response in the private run.
	privateCallID := "boston-fixture-ask-user"
	privateInvocation := "boston-fixture-question"
	privateResponse := make(map[string]any, len(questionPayload)+1)
	for key, value := range questionPayload {
		privateResponse[key] = value
	}
	privateResponse["status"] = "ok"
	for _, event := range []*session.Event{
		{ID: uuid.NewString(), Author: "agent", InvocationID: privateInvocation, Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: privateCallID, Name: "ask_user", Args: questionPayload}}}}}},
		{ID: uuid.NewString(), Author: "agent", InvocationID: privateInvocation, Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: privateCallID, Name: "ask_user", Response: privateResponse}}}}}},
	} {
		if err = repo.(store.BotTranscriptStore).AppendBotRunEvent(worker, lease, runs[0].SessionID, event); err != nil {
			t.Fatal(err)
		}
	}
	report := store.BotReport{Summary: "Question awaits the explicit Bot owner's typed answer; no execution resources are held. Continuation uses the saved conversation under current tool policy.", NeedsApproval: []uuid.UUID{a.ID}, Unverified: []string{"Tool ask_user returned without independently verified outcome metadata."}}
	work, err := repo.(store.BotWorkStore).LoadWork(worker, lease)
	if err != nil {
		t.Fatal(err)
	}
	report.Work = store.BuildBotWorkProjection(work)
	checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: id, RunID: a.RunID, SessionID: runs[0].SessionID, Version: b.Version, ActionDigest: a.ActionDigest, Fence: lease.Fence})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: report}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("testdata/boston-question-history.sse")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var e session.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) != nil || e.Content == nil || len(e.Content.Parts) != 1 {
			continue
		}
		if e.Author == "model" && tryParseChatQuestionMessage(e.Content.Parts[0].Text) != nil || e.InvocationID == "bot-report:"+original.RunID.String() {
			if e.Author == "assistant" {
				e.InvocationID = "bot-report:" + a.RunID.String()
				e.Content = genai.NewContentFromText(bot.RenderReport(store.BotRun{ID: a.RunID, Status: store.RunWaitingApproval, Report: report}), "model")
			}
			e.Timestamp = time.Now()
			if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), b.HomeSessionID.String(), &e); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	if count != 2 {
		t.Fatalf("fixture markers=%d", count)
	}
	var q struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(a.Interaction.Payload, &q)
	check := func() {
		page.MustElement(`[data-review-id="` + a.ID.String() + `"]`).MustVisible()
		text := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		if strings.Count(text, q.Prompt) != 1 || strings.Contains(text, "Question awaits the explicit") || strings.Contains(text, "independently verified outcome") {
			t.Fatalf("clutter: %s", text)
		}
		card := page.MustElement(`[data-review-id="` + a.ID.String() + `"]`)
		if card.MustEval(`() => this.querySelector('h3') !== null || [...this.querySelectorAll('p,button')].some(e => /^(pending|Deny)$/.test(e.textContent))`).Bool() {
			t.Fatal("duplicate heading or permission chrome")
		}
	}
	check()
	page.MustReload()
	check()
	if err = os.MkdirAll("../../.tmp/bot-question-artifacts", 0755); err != nil {
		t.Fatal(err)
	}
	shot, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("../../.tmp/bot-question-artifacts/boston-restored.png", shot, 0600); err != nil {
		t.Fatal(err)
	}
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Runs & activity$").MustClick()
	page.MustElementR(`[role="dialog"] p`, "Unverified: Tool ask_user returned without independently verified outcome metadata").MustVisible()
	page.MustElementR("button", "^Close$").MustClick()
	// An arbitrary assistant event is a negative control, not Employee authority.
	if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), b.HomeSessionID.String(), &session.Event{ID: uuid.NewString(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("PRIVATE_UNATTESTED_ASSISTANT", "model"), TurnComplete: true}}); err != nil {
		t.Fatal(err)
	}
	check()
	page.MustReload()
	check()
	approvals, err := repo.ListApprovals(owner, id, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalPending {
		t.Fatal("fixture question was consumed", err)
	}
	// One click reaches the production answer endpoint. Denial must not mint a
	// replacement continuation, and the accepted answer must survive restoration.
	denyAnswer.Store(true)
	page.MustElement(`[data-review-id="` + a.ID.String() + `"] textarea`).MustInput("just me via Telegram")
	page.MustElementR(`[data-review-id="`+a.ID.String()+`"] button`, "^Submit answer$").MustClick()
	page.MustWait(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.includes('Bot access unavailable with the current authorization')`)
	if answerRequests.Load() != 1 || authorizationRetries.Load() != 0 {
		t.Fatal("permission denial retried with more authority")
	}
	pending, err := repo.GetRun(owner, id, a.RunID)
	if err != nil || pending.Status != store.RunWaitingApproval {
		t.Fatalf("denial advanced run: %+v %v", pending, err)
	}
	decodedCheckpoint, checkpointErr := bot.DecodeApprovalCheckpoint(pending.Checkpoint, *pending)
	if checkpointErr != nil || !decodedCheckpoint.Matches(a) {
		t.Fatalf("historical checkpoint binding invalid: checkpoint=%+v approval=%+v err=%v", decodedCheckpoint, a, checkpointErr)
	}
	denyAnswer.Store(false)
	// Lose the original process-local entry before answering, as after a restart.
	registry.Cancel(execution.BotRunIdentity{BotID: id, RunID: a.RunID, Version: b.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug})
	page.MustElementR(`[data-review-id="`+a.ID.String()+`"] button`, "^Submit answer$").MustClick()
	checkAnswered := func() {
		t.Helper()
		page.MustWait(`() => { const card = document.querySelector('[data-review-id="` + a.ID.String() + `"]'); return card && card.innerText.includes('Your answer: just me via Telegram') }`)
		text := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		if strings.Contains(text, "Continue interactive") || strings.Contains(text, "Confirm exact change") {
			t.Fatalf("question demanded second consent: %s", text)
		}
		card := page.MustElement(`[data-review-id="` + a.ID.String() + `"]`)
		if card.MustEval(`() => [...this.querySelectorAll('button')].some(e => /Submit answer/.test(e.textContent))`).Bool() {
			t.Fatal("saved question retained active submit")
		}
	}
	checkAnswered()
	queued, err := repo.GetRun(owner, id, a.RunID)
	approved, approvalErr := repo.ListApprovals(owner, id, store.BotPage{})
	if err != nil || approvalErr != nil || queued.Status != store.RunQueued || queued.ID != pending.ID || queued.SessionID != pending.SessionID || len(approved) != 1 || approved[0].Status != store.ApprovalApproved || approved[0].Response == nil || approved[0].Response.QuestionID != a.Interaction.QuestionID || approved[0].Response.Answer != "just me via Telegram" {
		t.Fatalf("single answer not durable: run=%+v approvals=%+v err=%v/%v", queued, approved, err, approvalErr)
	}
	if answerRequests.Load() != 2 || authorizationRetries.Load() != 0 {
		t.Fatal("accepted answer required another authorization request")
	}
	page.MustReload()
	checkAnswered()

	// Finish the admitted continuation through real work validation and publication.
	// The scripted candidate is not a forged speech receipt: CommitRun owns attestation.
	lease, err = repo.ClaimRun(worker, store.BotClaimRequest{BotID: id, RunID: a.RunID, WorkerID: "answered-question-browser", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: b.Version, ActionDigest: a.ActionDigest}); err != nil {
		t.Fatal(err)
	}
	workStore := repo.(store.BotWorkStore)
	work, err = workStore.LoadWork(worker, lease)
	if err != nil {
		t.Fatal(err)
	}
	next := work.Clone()
	next.Phase = store.BotWorkFinished
	next.AgentWork.Disposition = store.AgentWorkAnswered
	const laterSpeech = "I will use your Telegram destination for the Boston update."
	next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: laterSpeech}
	next.Candidate.Digest = next.Candidate.ContentDigest()
	if next.EmployeeReviewPolicy == store.BotEmployeeReviewEvidenceBoundV1 {
		next.Criteria = []store.BotWorkCriterion{{ID: "destination", SourceIDs: []string{"assignment", "charter"}, Text: "Acknowledge the supplied destination"}}
		next.Review = &store.BotWorkReview{CandidateDigest: next.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "destination", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + next.Candidate.Digest}, Explanation: "Scripted provider assessment"}}}
	}
	work, err = workStore.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: work.Revision, State: *next})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: laterSpeech, Work: store.BuildBotWorkProjection(work)}}); err != nil {
		t.Fatal(err)
	}
	publication, err := repo.(store.BotPublicationStore).PublishReport(owner, store.BotPublicationRequest{BotID: id, RunID: a.RunID})
	if err != nil || publication == nil || !publication.EmployeeSpeech || publication.Text != laterSpeech {
		t.Fatalf("legitimate Employee publication: %+v %v", publication, err)
	}
	checkOrder := func() {
		t.Helper()
		page.MustElementR("p", "I will use your Telegram destination").MustVisible()
		checkAnswered()
		text := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		if strings.Count(text, q.Prompt) != 1 || strings.Count(text, laterSpeech) != 1 || strings.Index(text, q.Prompt) > strings.Index(text, "Your answer: just me via Telegram") || strings.Index(text, "Your answer: just me via Telegram") > strings.Index(text, laterSpeech) || strings.Contains(text, "PRIVATE_UNATTESTED_ASSISTANT") {
			t.Fatalf("clarification/answer/Employee order: %s", text)
		}
	}
	checkOrder()
	// A later duplicate of the original question must not restamp its first anchor.
	if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), b.HomeSessionID.String(), &session.Event{ID: uuid.NewString(), Author: "model", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("[chat_question]"+string(a.Interaction.Payload), "model")}}); err != nil {
		t.Fatal(err)
	}
	checkOrder()
	page.MustReload()
	checkOrder()
	t.Log("PASS canonical question stays before accepted answer and legitimately committed Employee reply, including later duplicate question and reload")

	// A partial run with no eligible candidate publishes neutral state, never prose.
	partialMessage := env.Request(t, http.MethodPost, "/api/studio/bots/"+id.String()+"/messages", map[string]any{"version": b.Version, "idempotency_key": "neutral-browser", "instructions": "Unverified follow-up"}, memoryIdentityAdmin)
	var partialReceipt store.BotMessageReceipt
	if partialMessage.StatusCode != http.StatusAccepted || json.NewDecoder(partialMessage.Body).Decode(&partialReceipt) != nil {
		partialMessage.Body.Close()
		t.Fatal("neutral fixture interactive admission")
	}
	partialMessage.Body.Close()
	partialRun := partialReceipt.Run
	partialWorker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: id, RunID: partialRun.ID, Version: b.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug})
	partialLease, err := repo.ClaimRun(partialWorker, store.BotClaimRequest{BotID: id, RunID: partialRun.ID, WorkerID: "neutral-browser", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	partialWork, err := workStore.LoadWork(partialWorker, partialLease)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CommitRun(partialWorker, store.BotRunCommit{Lease: partialLease, Status: store.RunPartial, Report: store.BotReport{Summary: "PRIVATE_NO_ELIGIBLE_ANSWER", Work: store.BuildBotWorkProjection(partialWork)}}); err != nil {
		t.Fatal(err)
	}
	neutral, err := repo.(store.BotPublicationStore).PublishReport(owner, store.BotPublicationRequest{BotID: id, RunID: partialRun.ID})
	if err != nil || neutral == nil || neutral.EmployeeSpeech {
		t.Fatalf("neutral publication: %+v %v", neutral, err)
	}
	checkNeutral := func() {
		t.Helper()
		page.MustElementR(`[role="status"]`, "Partially completed — no Employee answer available").MustVisible()
		text := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		if strings.Count(text, "Partially completed — no Employee answer available") != 1 || strings.Contains(text, "PRIVATE_NO_ELIGIBLE_ANSWER") {
			t.Fatalf("neutral status became speech: %s", text)
		}
	}
	checkNeutral()
	page.MustReload()
	checkNeutral()
	t.Log("PASS real partial publication renders one neutral status live/reload without an assistant bubble")
	t.Log("PASS actual HTTP permission denial has no escalation; one accepted answer queues same run and shows saved answer live/reload without additional consent")

	// Restore real revoked/expired repository questions; neither may offer recovery.
	for _, state := range []string{"revoked", "expired"} {
		t.Run(state, func(t *testing.T) {
			created := env.Request(t, http.MethodPost, "/api/studio/bots", map[string]any{"name": "Inactive question " + state, "idempotency_key": "inactive-" + state}, memoryIdentityAdmin)
			var fixture store.Bot
			if created.StatusCode != http.StatusCreated || json.NewDecoder(created.Body).Decode(&fixture) != nil {
				t.Fatal("inactive fixture creation")
			}
			created.Body.Close()
			message := env.Request(t, http.MethodPost, "/api/studio/bots/"+fixture.ID.String()+"/messages", map[string]any{"version": fixture.Version, "instructions": "Discuss recipients", "idempotency_key": "inactive-message"}, memoryIdentityAdmin)
			var receipt store.BotMessageReceipt
			if message.StatusCode != http.StatusAccepted || json.NewDecoder(message.Body).Decode(&receipt) != nil {
				t.Fatal("inactive fixture admission")
			}
			message.Body.Close()
			worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: fixture.ID, RunID: receipt.Run.ID, Version: fixture.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: fixture.ID, RunID: receipt.Run.ID, WorkerID: "inactive-question", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			question := original
			question.ID, question.BotID, question.RunID, question.Version = uuid.Nil, fixture.ID, receipt.Run.ID, fixture.Version
			question.ExpiresAt = time.Now().Add(time.Hour)
			if state == "expired" {
				question.ExpiresAt = time.Now().Add(300 * time.Millisecond)
			}
			question.ActionDigest, err = question.Action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.RequestApproval(worker, lease, question)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: fixture.ID, RunID: receipt.Run.ID, SessionID: receipt.Run.SessionID, Version: fixture.Version, ActionDigest: a.ActionDigest, Fence: lease.Fence})
			if err != nil {
				t.Fatal(err)
			}
			work, err := repo.(store.BotWorkStore).LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting for answer", NeedsApproval: []uuid.UUID{a.ID}, Work: store.BuildBotWorkProjection(work)}}); err != nil {
				t.Fatal(err)
			}
			if state == "revoked" {
				if _, err = repo.(store.BotRunCancellationStore).CancelRun(owner, fixture.ID, receipt.Run.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				time.Sleep(350 * time.Millisecond)
			}
			if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), fixture.HomeSessionID.String(), &session.Event{ID: uuid.NewString(), Author: "model", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("[chat_question]"+string(a.Interaction.Payload), "model")}}); err != nil {
				t.Fatal(err)
			}
			page.MustNavigate(env.URL + "/#/chat/bots/personal/" + fixture.ID.String())
			checkInactive := func() {
				t.Helper()
				page.MustElement(`[data-review-id="` + a.ID.String() + `"]`).MustVisible()
				text := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
				if strings.Contains(text, "Continue interactive") || strings.Contains(text, "Confirm exact change") || strings.Contains(text, "Submit answer") {
					t.Fatalf("inactive %s offers consent/recovery: %s", state, text)
				}
			}
			checkInactive()
			page.MustReload()
			checkInactive()
		})
	}
	// Ordinary Chat must not inherit Bot-only projection, even for quoted markers.
	sourceID := uuid.NewString()
	if err = env.Services.PersonalSessions.AddSessionMeta(context.Background(), store.SessionMeta{ID: sourceID, AppName: studioChatAppName, UserID: env.UserID, Title: "Quoted marker", Purpose: store.SessionPurposeChat, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), sourceID, &session.Event{ID: uuid.NewString(), Author: "agent", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("A legitimate quoted marker: `[chat_question]` is documentation, not a pending question.", "model"), TurnComplete: true}}); err != nil {
		t.Fatal(err)
	}
	page.MustNavigate(env.URL + "/#/chat/" + sourceID)
	page.MustWait(`() => document.body.innerText.includes('A legitimate quoted marker: [chat_question] is documentation')`)
	page.MustReload()
	page.MustWait(`() => document.body.innerText.includes('A legitimate quoted marker: [chat_question] is documentation')`)
	t.Log("PASS ordinary Chat quoted [chat_question] preserved live navigation and reload")
	t.Log("PASS exact Boston question marker then waiting report: one canonical question live/reload, no duplicated heading/pending/Deny; Details diagnostics retained; later messages render; original clean projection preserved before answer")
}
