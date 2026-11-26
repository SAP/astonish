package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Strict prerequisites: this gate must fail, never skip or download a browser.
func replayBrowserPrerequisites(t *testing.T) (string, string) {
	t.Helper()
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Fatal("ASTONISH_BOT_BROWSER_PATH must name an explicit browser executable")
	}
	if st, err := os.Stat(bin); err != nil || st.IsDir() || st.Mode()&0111 == 0 {
		t.Fatalf("browser executable unavailable: %s (%v)", bin, err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build Studio first", err)
	}
	return dist, bin
}
func replayBrowserPage(t *testing.T, bin, url string) *rod.Page {
	t.Helper()
	l := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := l.Launch()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Cleanup)
	browser := rod.New().ControlURL(control).MustConnect()
	t.Cleanup(func() { _ = browser.Close() })
	page := browser.MustPage(url).Timeout(45 * time.Second)
	return page.WithPanic(func(v any) {
		text, _ := page.CancelTimeout().Timeout(time.Second).Eval(`() => document.body.innerText`)
		t.Fatalf("built SPA browser: %v\n%v", v, text)
	})
}

type replayBrowserStreamWriter struct {
	http.ResponseWriter
	savedQuestion *atomic.Bool
	savedReviewID string
}

func (w replayBrowserStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w replayBrowserStreamWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.savedReviewID) && strings.Contains(string(p), `"question"`) {
		w.savedQuestion.Store(true)
	}
	return w.ResponseWriter.Write(p)
}

// Real UUID-sorted SQLite approvals, production history/delta HTTP, and an
// isolated built SPA. No response replacement, React state injection or provider.
func TestBotApprovalReplayBrowserAcceptance(t *testing.T) {
	dist, bin := replayBrowserPrerequisites(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			run, err := f.repo.RequestRun(f.ctx, store.BotRunRequest{BotID: f.b.ID, Version: f.b.Version, Trigger: store.TriggerManual, Instructions: "Replay fixture", IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(f.ctx, execution.BotRunIdentity{BotID: f.b.ID, RunID: run.ID, Version: f.b.Version, ActorID: f.b.OwnerID.String(), OrgSlug: "interactive", TeamSlug: "team"})
			lease, err := f.repo.ClaimRun(worker, store.BotClaimRequest{BotID: f.b.ID, RunID: run.ID, WorkerID: "browser-replay", ExpiresAt: time.Now().Add(5 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			approvals := make([]store.BotApproval, 0, 201)
			for i := 0; i < 201; i++ {
				qid := fmt.Sprintf("replay-question-%03d", i)
				payload, _ := json.Marshal(map[string]any{"questionId": qid, "kind": "text", "prompt": fmt.Sprintf("Replay question %03d?", i)})
				action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload}
				digest, err := action.Digest()
				if err != nil {
					t.Fatal(err)
				}
				a, err := f.repo.RequestApproval(worker, lease, store.BotApproval{BotID: f.b.ID, RunID: run.ID, Version: f.b.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: qid, Payload: payload}})
				if err != nil {
					t.Fatalf("approval %d: %v", i, err)
				}
				approvals = append(approvals, *a)
			}
			sort.Slice(approvals, func(i, j int) bool { return approvals[i].ID.String() < approvals[j].ID.String() })
			saved, live := approvals[200], approvals[198] // beyond both the old 200 and eight-row boundaries
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			runSessions := org.ForTeam("team").Sessions()
			if scope == store.BotScopePersonal {
				runSessions = org.ForUser(f.b.OwnerID.String()).Sessions()
			}
			var args map[string]any
			if err = json.Unmarshal(saved.Interaction.Payload, &args); err != nil {
				t.Fatal(err)
			}
			response := make(map[string]any)
			for k, v := range args {
				response[k] = v
			}
			response["status"] = "ok"
			for _, content := range []*genai.Content{{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "saved-call", Name: "ask_user", Args: args}}}}, {Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "saved-call", Name: "ask_user", Response: response}}}}} {
				if err = runSessions.AppendFleetEvent(f.ctx, run.SessionID.String(), &adksession.Event{ID: uuid.NewString(), InvocationID: "saved-call-invocation", Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: content}}); err != nil {
					t.Fatal(err)
				}
			}
			saved, err = func() (store.BotApproval, error) {
				a, e := f.repo.DecideApproval(f.ctx, store.BotApprovalDecision{BotID: f.b.ID, ApprovalID: saved.ID, ActorID: f.b.OwnerID, ActionDigest: saved.ActionDigest, Status: store.ApprovalApproved, Response: &store.BotInteractionResponse{QuestionID: saved.Interaction.QuestionID, Answer: "PERSISTED-BEYOND-PAGE-ANSWER"}})
				if e != nil {
					return store.BotApproval{}, e
				}
				return *a, nil
			}()
			if err != nil {
				t.Fatal(err)
			}
			if err = f.repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: lease, ApprovalID: saved.ID, Version: f.b.Version, ActionDigest: saved.ActionDigest}); err != nil {
				t.Fatal(err)
			}
			// One waiting run gives the report suppression gate all three real signals.
			report := store.BotReport{Summary: "Question awaits the explicit Bot owner's typed answer; no execution resources are held. Continuation uses the saved conversation under current tool policy.", NeedsApproval: []uuid.UUID{live.ID}, Unverified: []string{"Tool ask_user returned without independently verified outcome metadata."}}
			work, err := f.repo.(store.BotWorkStore).LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			report.Work = store.BuildBotWorkProjection(work)
			checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.InteractionCheckpointFormat, ApprovalID: live.ID, BotID: f.b.ID, RunID: run.ID, SessionID: run.SessionID, Version: f.b.Version, ActionDigest: live.ActionDigest, Fence: lease.Fence})
			if err = f.repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: report}); err != nil {
				t.Fatal(err)
			}
			var sessions store.SessionStore
			var eventConnections atomic.Int32
			var streamSavedAnswer atomic.Bool
			disconnect := make(chan context.CancelFunc, 1)
			reconnectCursor := make(chan string, 4)
			server := generationHealthBrowserServer(t, f, dist, func(routes *mux.Router, svc *store.Services) {
				sessions = svc.PersonalSessions
				if scope == store.BotScopeTeam {
					sessions = svc.Sessions
				}
				routes.HandleFunc("/api/studio/bots/{botID}/sessions/{sessionID}/events", func(w http.ResponseWriter, r *http.Request) {
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					if eventConnections.Add(1) == 1 {
						disconnect <- cancel
					} else {
						cursor := r.URL.Query().Get("after")
						if cursor == "" {
							cursor = r.Header.Get("Last-Event-ID")
						}
						select {
						case reconnectCursor <- cursor:
						default:
						}
					}
					api.BotEventsHandler(replayBrowserStreamWriter{ResponseWriter: w, savedQuestion: &streamSavedAnswer, savedReviewID: saved.ID.String()}, r.WithContext(ctx))
				}).Methods(http.MethodGet)
			})
			appendText := func(id, inv, text string) {
				t.Helper()
				if err := sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), &adksession.Event{ID: id, InvocationID: inv, Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(text, "model"), TurnComplete: true}}); err != nil {
					t.Fatal(err)
				}
			}
			appendReview := func(a store.BotApproval) {
				identity := uuid.NewSHA1(a.ID, []byte("bot-review:approval"))
				payload, _ := json.Marshal(map[string]any{"kind": "approval", "review_id": a.ID})
				event := &adksession.Event{ID: identity.String(), InvocationID: identity.String(), Author: "bot_runtime", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: f.b.HomeSessionID, RunID: run.ID, TurnID: identity, Sequence: 1, Type: "bot_review", Payload: payload}}}}
				if err := sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), event); err != nil {
					t.Fatal(err)
				}
			}
			appendReview(saved)
			appendText(uuid.NewString(), "later-message", "LATER-AFTER-SAVED-QUESTION")
			page := replayBrowserPage(t, bin, server.URL+"/#/chat/bots/"+string(scope)+"/"+f.b.ID.String())
			checkSaved := func() {
				t.Helper()
				page.MustElement(`[aria-label="Answered question"][data-review-id="` + saved.ID.String() + `"]`).MustVisible()
				page.MustWait(`() => document.body.innerText.includes('LATER-AFTER-SAVED-QUESTION')`)
				result := page.MustEval(`id => {const cards=[...document.querySelectorAll('[aria-label="Answered question"][data-review-id="'+id+'"]')];const c=cards[0];const p=[...document.querySelectorAll('p')].find(e=>e.textContent==='LATER-AFTER-SAVED-QUESTION');return cards.length===1 && c.innerText.includes('PERSISTED-BEYOND-PAGE-ANSWER') && !c.querySelector('button,input,textarea') && !!(c.compareDocumentPosition(p)&Node.DOCUMENT_POSITION_FOLLOWING)}`, saved.ID.String()).Bool()
				if !result {
					t.Fatalf("saved answer duplicated, actionable, missing, or reordered: %s", page.MustEval(`id => JSON.stringify({cards:[...document.querySelectorAll('[data-review-id="'+id+'"]')].map(e=>e.outerHTML),ps:[...document.querySelectorAll('p')].filter(e=>e.textContent.includes('LATER-AFTER')).map(e=>e.outerHTML)})`, saved.ID.String()).Str())
				}
			}
			checkSaved()
			if !streamSavedAnswer.Load() {
				t.Fatal("production SSE did not enrich the consumed approval beyond page 200; UI fallback must not mask server replay truncation")
			}
			page.MustElementR("button", "^Details$").MustClick()
			page.MustElementR("summary", "^Runs & activity$").MustClick()
			page.MustElementR("p", "200 decisions waiting").MustVisible()
			page.MustElementR("button", "^Close$").MustClick()
			// Live marker and report arrive after the UI mounted, without a reload.
			appendReview(live)
			appendText(uuid.NewString(), "bot-report:"+run.ID.String(), bot.RenderReport(store.BotRun{ID: run.ID, Status: store.RunWaitingApproval, Report: report}))
			page.MustElement(`[data-review-id="` + live.ID.String() + `"] textarea`).MustVisible()
			for i := 0; i < 12; i++ {
				appendText(uuid.NewString(), fmt.Sprintf("tail-%02d", i), fmt.Sprintf("RECONNECT-TAIL-%02d", i))
			}
			page.MustWait(`() => document.body.innerText.includes('RECONNECT-TAIL-11')`)
			check := func() {
				t.Helper()
				checkSaved()
				if page.MustEval(`() => {const w=document.querySelector('[aria-label="Bot workspace"]');return [...w.querySelectorAll('p')].filter(e=>!e.closest('[role="dialog"]')).some(e=>e.textContent.includes('Question awaits the explicit')||e.textContent.includes('independently verified outcome'))}`).Bool() {
					t.Fatal("waiting report leaked into Chat")
				}
			}
			check()
			// Close the real production SSE request; require the SPA to resume with a
			// nonzero durable cursor, rather than assuming CDP offline killed a socket.
			select {
			case cancel := <-disconnect:
				cancel()
			case <-time.After(5 * time.Second):
				t.Fatal("production events connection never attached")
			}
			reconnected := approvals[199]
			appendReview(reconnected)
			appendText(uuid.NewString(), "reconnect-final", "RECONNECTED-FINAL")
			select {
			case cursor := <-reconnectCursor:
				if !strings.HasPrefix(cursor, f.b.HomeSessionID.String()+":") || strings.HasSuffix(cursor, ":0") {
					t.Fatalf("SPA did not reconnect at its durable cursor: %q", cursor)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("SPA never reconnected the production event stream")
			}
			page.MustElement(`[data-review-id="` + reconnected.ID.String() + `"] textarea`).MustVisible()
			page.MustWait(`() => document.body.innerText.includes('RECONNECTED-FINAL')`)
			check()
			before := page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] [data-review-id]')].filter(e=>!e.closest('[role="dialog"]')).map(e=>e.dataset.reviewId).join('|')`).Str()
			want := strings.Join([]string{saved.ID.String(), live.ID.String(), reconnected.ID.String()}, "|")
			selected := strings.Split(before, "|")
			selected = slices.DeleteFunc(selected, func(id string) bool {
				return id != saved.ID.String() && id != live.ID.String() && id != reconnected.ID.String()
			})
			if strings.Join(selected, "|") != want {
				t.Fatalf("review identities/order not exact: got %q want %q", selected, want)
			}
			tailOrder := page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] p')].map(e=>e.textContent).filter(s=>s.startsWith('RECONNECT-TAIL-')||s==='RECONNECTED-FINAL').join('|')`).Str()
			page.MustReload()
			page.MustWait(`() => document.body.innerText.includes('RECONNECTED-FINAL')`)
			check()
			after := page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] [data-review-id]')].filter(e=>!e.closest('[role="dialog"]')).map(e=>e.dataset.reviewId).join('|')`).Str()
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("reload identities/order differ: %v => %v", before, after)
			}
			if afterTail := page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] p')].map(e=>e.textContent).filter(s=>s.startsWith('RECONNECT-TAIL-')||s==='RECONNECTED-FINAL').join('|')`).Str(); afterTail != tailOrder {
				t.Fatalf("reload tail chronology differs: %q => %q", tailOrder, afterTail)
			}
			text := page.MustElement(`[aria-label="Bot workspace"]`).MustText()
			if strings.Count(text, "RECONNECT-TAIL-11") != 1 {
				t.Fatal("duplicate replay tail")
			}
			if f.model.productions != 0 || f.pool.calls != 0 {
				t.Fatal("read acceptance ran a provider or tool")
			}
			t.Logf("built SPA %s: 201 UUID-ordered approvals, consumed index 200 saved once before later text; 200 pending; live index198, reconnect beyond8 and reload exact review identities/order", scope)
		})
	}
}
