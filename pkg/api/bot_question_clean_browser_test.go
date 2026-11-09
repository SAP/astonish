package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
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
	routes.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	page := newStreamBrowserPage(t, env, bin, dist).CancelTimeout().Timeout(60 * time.Second)
	page.MustElementR("button", "^New Bot$").MustClick()
	page.MustElement(`[aria-label="Bot name"]`).MustInput("Boston fixture acceptance")
	page.MustElementR("button", "^Start conversation$").MustClick()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	hash := page.MustEval(`() => location.hash`).Str()
	id := uuid.MustParse(strings.Split(hash, "/")[4])
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
	createdApproval, err := repo.RequestApproval(worker, lease, a)
	if err != nil {
		t.Fatal(err)
	}
	a = *createdApproval
	report := store.BotReport{Summary: "Question awaits the explicit Bot owner's typed answer; no execution resources are held. Continuation uses the saved conversation under current tool policy.", NeedsApproval: []uuid.UUID{a.ID}, Unverified: []string{"Tool ask_user returned without independently verified outcome metadata."}}
	checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: id, RunID: a.RunID, SessionID: runs[0].SessionID, Version: b.Version, ActionDigest: a.ActionDigest, Fence: lease.Fence})
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
	if err = env.Services.PersonalSessions.AppendFleetEvent(context.Background(), b.HomeSessionID.String(), &session.Event{ID: uuid.NewString(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Later unrelated assistant message remains visible.", "model"), TurnComplete: true}}); err != nil {
		t.Fatal(err)
	}
	page.MustElementR("p", "Later unrelated assistant message remains visible").MustVisible()
	check()
	page.MustReload()
	page.MustElementR("p", "Later unrelated assistant message remains visible").MustVisible()
	check()
	approvals, err := repo.ListApprovals(owner, id, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalPending {
		t.Fatal("fixture question was consumed", err)
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
	t.Log("PASS exact Boston question marker then waiting report: one canonical question live/reload, no duplicated heading/pending/Deny; Details diagnostics retained; later messages render; no answer consumed")
}
