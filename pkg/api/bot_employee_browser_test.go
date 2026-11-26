package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type employeeBrowserModel struct {
	calls          atomic.Int64
	question       atomic.Bool
	laterReport    atomic.Bool
	t              *testing.T
	payload        map[string]any
	captured       chan struct{}
	captureOnce    sync.Once
	release        chan struct{}
	sourceReturned *atomic.Bool
}

func (*employeeBrowserModel) Name() string { return "interactive-http" }
func (m *employeeBrowserModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if req.Config != nil && req.Config.SystemInstruction != nil && len(req.Config.SystemInstruction.Parts) > 0 && strings.HasPrefix(req.Config.SystemInstruction.Parts[0].Text, "BOT_EMPLOYEE_MESSAGE") {
		// Auxiliary narration is not another execution turn. Exercise the real
		// typed fallback, without claiming a model-generated delivery.
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, store.ErrBotUnsupported) }
	}
	if response, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool {
		return w.Candidate.Text == "Your request has been recorded." || (strings.Contains(w.Candidate.Text, "Routine operation ") && strings.Contains(w.Candidate.Text, "is durably queued and pending, not applied.") && len(w.Candidate.Observations) > 0) || (strings.HasPrefix(w.Candidate.Text, "Schedule operation ") && strings.Contains(w.Candidate.Text, "is durably queued and pending. No schedule change is verified by this turn.") && len(w.Candidate.Observations) > 0) || w.Candidate.Text == "Later report: local status is healthy."
	}); ok {
		return response
	}
	if req.Config != nil && req.Config.SystemInstruction != nil && len(req.Config.SystemInstruction.Parts) > 0 && strings.HasPrefix(req.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_PENDING_REVIEW_V1") {
		var work store.BotWorkState
		if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil || work.Candidate == nil {
			m.t.Fatalf("pending assessment input: %v", err)
		}
		current := store.BotWorkReview{CandidateDigest: work.Candidate.Digest}
		deferred := store.BotWorkReview{CandidateDigest: work.Candidate.Digest}
		for _, criterion := range work.Criteria {
			assessment := store.BotCriterionAssessment{CriterionID: criterion.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Controlled model assessed the server-captured pending payload; application is deferred."}
			current.Criteria = append(current.Criteria, assessment)
			assessment.Verdict = store.BotCriterionUnverified
			deferred.Criteria = append(deferred.Criteria, assessment)
		}
		currentRaw, _ := json.Marshal(current)
		currentResponse := &model.LLMResponse{Content: genai.NewContentFromText(string(currentRaw), "model")}
		fixtureHTTPContentCoverage(req, currentResponse)
		var coveredCurrent map[string]any
		_ = json.Unmarshal([]byte(currentResponse.Content.Parts[0].Text), &coveredCurrent)
		raw, _ := json.Marshal(map[string]any{"current_turn": coveredCurrent, "deferred_application": deferred})
		return func(yield func(*model.LLMResponse, error) bool) {
			response := &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}
			fixtureHTTPContentCoverage(req, response)
			yield(response, nil)
		}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.laterReport.Load() {
			m.calls.Add(1)
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Later report: local status is healthy.", "model"), TurnComplete: true}, nil)
			return
		}
		if m.question.Load() {
			m.calls.Add(1)
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "employee-question", Name: "execute_tool", Args: map[string]any{"name": "ask_user", "arguments": map[string]any{"kind": "text", "prompt": "Which employee report should I prepare next?"}}}}}}}, nil)
			return
		}
		if m.calls.Add(1) == 1 {
			call := &genai.FunctionCall{ID: "employee-control", Name: "propose_bot_control", Args: map[string]any{"payload": m.payload}}
			if m.payload["operation"] == store.BotControlScheduleCreate {
				call.Name = "execute_tool"
				call.Args = map[string]any{"name": "schedule_job", "arguments": map[string]any{"name": "Employee report", "schedule": "* * * * *", "timezone": "UTC", "instructions": "Read local status and report", "mode": "adaptive"}}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
			return
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil {
					m.t.Logf("tool result: %+v", part.FunctionResponse)
				}
			}
		}
		m.captureOnce.Do(func() { close(m.captured) })
		select {
		case <-ctx.Done():
			return
		case <-m.release:
		}
		text := "Your request has been recorded."
		if m.payload["operation"] == store.BotControlScheduleCreate || m.payload["operation"] == store.BotControlResponsibility {
			for _, content := range req.Contents {
				for _, part := range content.Parts {
					if part.FunctionResponse != nil {
						if receipt, ok := part.FunctionResponse.Response["message"].(string); ok && (strings.HasPrefix(receipt, "Routine operation ") || strings.HasPrefix(receipt, "Schedule operation ")) && strings.Contains(receipt, "durably queued and pending") {
							text = receipt
						}
					}
				}
			}
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model"), TurnComplete: true}, nil)
		m.sourceReturned.Store(true)
	}
}

// TestBotWorkQualityBrowserAcceptance serves the built Studio against isolated
// production Bot handlers/store/worker. Only model and identity/settings are controlled.
func TestBotWorkQualityBrowserAcceptance(t *testing.T) {
	runEmployeeQualityBrowser(t, []string{"checked", "limitations"})
}

func TestBotBlockedExplanationBrowserAcceptance(t *testing.T) {
	runEmployeeQualityBrowser(t, []string{"blocked"})
}

func runEmployeeQualityBrowser(t *testing.T, modes []string) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after npm run build")
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Fatal("ASTONISH_BOT_BROWSER_PATH is required")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", nil, []string{"chat", "tool:execute"}, store.BotScopePersonal)
			defer f.worker.Stop()
			f.model.qualityMode = "publication"
			if mode == "limitations" {
				f.model.qualityMode = "limits"
			}
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				if req.Config == nil || req.Config.SystemInstruction == nil {
					return nil, false
				}
				prompt := req.Config.SystemInstruction.Parts[0].Text
				var result any
				switch {
				case strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1"):
					result = map[string]any{"kind": store.BotSkillNoSkillNeeded, "skill_id": "", "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Controlled receipt quality fixture"}
				case strings.HasPrefix(prompt, "BOT_EMPLOYEE_REPAIR_V1"):
					text := "Corrected: the receipt says isolated receipt."
					if mode == "limitations" {
						text = "Defective: the receipt says imaginary."
					}
					result = map[string]any{"text": text}
				case strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1"):
					result = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "receipt", SourceIDs: []string{"assignment", "charter"}, Text: "Return the supplied exact receipt"}}}
				case strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1"):
					var work store.BotWorkState
					if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil {
						t.Fatal(err)
					}
					verdict := store.BotCriterionFailed
					if work.Candidate.Text == "Corrected: the receipt says isolated receipt." {
						verdict = store.BotCriterionSatisfied
					}
					result = store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "receipt", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Use the supplied exact receipt"}}}
				default:
					return nil, false
				}
				raw, _ := json.Marshal(result)
				response := &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}
				fixtureHTTPContentCoverage(req, response)
				return response, true
			}
			if mode == "blocked" {
				owner := f.b.OwnerID.String()
				link := &store.UserChannel{ID: uuid.NewString(), UserID: owner, ChannelType: store.BotDeliveryChannelEmail, ExternalID: owner + "@example.test", Enabled: true}
				if err := f.db.UserChannels().Link(f.ctx, link); err != nil {
					t.Fatal(err)
				}
				if err := f.db.UserChannels().Verify(f.ctx, link.ID); err != nil {
					t.Fatal(err)
				}
				llm := &blockedPublicationModel{t: t}
				worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{
					Backend: f.db, Sandbox: f.workerConfig.Sandbox, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 1000000,
					ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 1000, SupportsOutputLimit: true},
					ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
						return llm, nil
					},
					Prepare: f.workerConfig.Prepare,
				})
				if err != nil {
					t.Fatal(err)
				}
				f.worker = worker
				defer worker.Stop()
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
			dist, _ := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
			if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(f.server.URL)
			proxy := httputil.NewSingleHostReverseProxy(u)
			principal, _ := execution.PrincipalFromContext(f.ctx)
			ods, err := f.db.ForOrg(principal.OrgSlug)
			if err != nil {
				t.Fatal(err)
			}
			svc := &store.Services{TenantRouter: f.db, Bots: f.repo, PersonalBots: f.repo, Sessions: ods.ForTeam(principal.TeamSlug).Sessions(), PersonalSessions: ods.ForUser(principal.Subject).Sessions(), BotTurnRegistry: f.registry, BotInteractiveReady: true}
			router := mux.NewRouter()
			// Compatibility probes adapt only the read DTO; successful publication
			// still comes exclusively from the production worker/work state machine.
			var compatibility atomic.Int32
			router.HandleFunc("/api/studio/bots/{botID}/snapshot", func(w http.ResponseWriter, r *http.Request) {
				if compatibility.Load() == 0 {
					api.BotSnapshotHandler(w, r)
					return
				}
				rec := httptest.NewRecorder()
				api.BotSnapshotHandler(rec, r)
				var snap store.BotSnapshot
				if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &snap) != nil {
					t.Error("snapshot read failed")
					http.Error(w, "snapshot", 500)
					return
				}
				if snap.LatestOutcome != nil {
					if compatibility.Load() == 1 {
						snap.LatestOutcome.Report.Quality = nil
					} else {
						snap.LatestOutcome.Report.Quality = &store.BotQualityReport{SchemaVersion: 999}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(snap)
			}).Methods("GET")
			for _, route := range []struct {
				path string
				h    http.HandlerFunc
			}{
				{"", api.BotListHandler}, {"/{botID}/activity", api.BotActivityHandler}, {"/{botID}/read", api.BotReadCursorHandler}, {"/{botID}/versions/{version}", api.BotGetVersionHandler}, {"/{botID}/runs", api.BotRunsHandler}, {"/{botID}/proposals", api.BotControlProposalsHandler}, {"/{botID}/approvals", api.BotApprovalsHandler}, {"/{botID}/goals", api.BotGoalsHandler},
			} {
				router.HandleFunc("/api/studio/bots"+route.path, route.h).Methods("GET")
			}
			router.PathPrefix("/").Handler(proxy)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/studio/bots") {
					ctx, cancel := context.WithCancel(f.ctx)
					defer cancel()
					stop := context.AfterFunc(r.Context(), cancel)
					defer stop()
					router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
					return
				}
				var v any
				switch r.URL.Path {
				case "/api/auth/setup-status":
					v = map[string]any{"initialized": true, "auth_mode": "builtin"}
				case "/api/auth/me":
					v = map[string]any{"user": map[string]any{"id": principal.Subject, "email": "quality@example.test", "display_name": "Owner", "role": "owner", "platform_role": "user"}, "org": map[string]any{"slug": principal.OrgSlug}, "team": principal.TeamSlug}
				case "/api/orgs":
					v = map[string]any{"orgs": []any{}, "active_org": principal.OrgSlug}
				case "/api/teams":
					v = []any{map[string]any{"slug": principal.TeamSlug, "name": "Team"}}
				case "/api/settings/status":
					v = map[string]any{"setupRequired": false}
				case "/api/version":
					v = map[string]any{"version": "dev"}
				case "/api/agents", "/api/tools":
					v = []any{}
				case "/api/settings/config":
					v = map[string]any{}
				case "/api/settings/providers/effective":
					v = map[string]any{"providers": map[string]any{}}
				}
				if v != nil {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(v)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/") {
					proxy.ServeHTTP(w, r)
					return
				}
				path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
				if info, e := os.Stat(path); e == nil && !info.IsDir() {
					http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
					return
				}
				http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			}))
			defer server.Close()
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			page := browser.MustPage(server.URL + "/#/chat/bots/personal/" + f.b.ID.String()).Timeout(40 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`()=>document.body.innerText`)
				t.Fatalf("browser: %v\n%v", v, text)
			})
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			var ids []string
			count := 3
			if mode != "checked" {
				count = 1
			}
			instructions := "Report the supplied text: isolated receipt."
			if mode == "blocked" {
				instructions = blockedPublicationAssignment
			}
			for i := 0; i < count; i++ {
				f.model.productions = 0
				var receipt store.BotMessageReceipt
				if err = json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":%q,"idempotency_key":"browser-quality-%d"}`, f.b.Version, instructions, i), 202), &receipt); err != nil {
					t.Fatal(err)
				}
				// These assertions deliberately exercise the historical assessor's
				// repair/quality DTO contract, not the agent-led default work mode.
				pinLegacyHTTPAttempt(t, f, receipt.Run)
				if err = f.worker.Poll(f.ctx); err != nil {
					t.Fatal(err)
				}
				if mode == "blocked" {
					run, e := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
					if e != nil || run.Status != store.RunPartial || run.Report.Quality == nil || run.Report.Quality.ResponseOutcome != store.BotWorkResponseVerifiedBlocker || run.Report.Quality.Repairs != 0 || run.Report.Summary != blockedPublicationText {
						t.Fatalf("lost verified partial blocker: %+v %v", run, e)
					}
				}
				ids = append(ids, "bot-report:"+receipt.Run.ID.String())
			}
			want := "Requirements checked"
			if mode == "limitations" {
				want = "Requirements remain unmet or unverified"
			}
			homeText := "Corrected: the receipt says isolated receipt."
			if mode == "limitations" {
				homeText = "I couldn't confirm completion of the requested work. The requested result has not been verified. Review the incomplete result before deciding how to continue."
			}
			if mode == "blocked" {
				homeText = blockedPublicationText
			}
			page.MustWait(`want => [...document.querySelectorAll('[data-testid="agent-message-bubble"]')].some(e => e.innerText.trim() === want)`, homeText)
			if mode == "checked" {
				page.MustWait(`()=>document.body.innerText.split('Corrected: the receipt says isolated receipt.').length === 4`)
			}
			assertHome := func() {
				t.Helper()
				text := page.MustEval(`()=>document.body.innerText`).Str()
				for _, internal := range []string{"REVIEWER_ONLY", "transport_unavailable", "Run report", "Quality assessment", "candidate:", "### Unchanged"} {
					if strings.Contains(text, internal) {
						t.Fatalf("Home leaked diagnostic %q: %s", internal, text)
					}
				}
				if strings.Contains(text, "Defective: the receipt says imaginary.") {
					t.Fatal("draft leaked into Home")
				}
				if mode == "checked" && strings.Count(text, "Corrected: the receipt says isolated receipt.") != 3 {
					t.Fatalf("distinct final reports lost/duplicated: %s", text)
				}
				history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				for _, id := range ids {
					if count := canonicalHTTPReportCount(t, history, id); count != 1 {
						t.Fatalf("canonical report identity %s count=%d", id, count)
					}
				}
				if mode != "checked" && page.MustEval(`() => document.querySelectorAll('[aria-label="Bot workspace"] [data-testid="agent-message-bubble"]').length`).Int() != 1 {
					t.Fatal("incomplete/blocked outcome must be exactly one shared employee bubble")
				}
				if page.MustEval(`()=>document.querySelectorAll('[data-review-id]').length`).Int() != 0 {
					t.Fatal("quality introduced owner approval")
				}
			}
			assertHome()
			page.MustReload()
			if mode == "blocked" {
				homeText = blockedPublicationText
			}
			page.MustWait(`want => [...document.querySelectorAll('[data-testid="agent-message-bubble"]')].some(e => e.innerText.trim() === want)`, homeText)
			assertHome()
			if mode == "blocked" {
				t.Log("built Studio verified blocker: exact reason, administrator action and unchanged settings; one ordinary bubble live/reload; private runtime/reviewer diagnostics hidden")
				return
			}
			page.MustElement(`[data-testid="chat-input"]`).MustInput("keep quality draft")
			page.MustEval(`()=>{window.qualityComposer=document.querySelector('[data-testid="chat-input"]'); return true}`)
			page.MustElementR("button", "^Details$").MustClick()
			page.MustElementR("summary", "Runs & activity").MustClick()
			page.MustElementR(`[aria-label="Quality assessment"] summary`, want).MustVisible()
			page.MustWait(`(want, count) => [...document.querySelectorAll('[aria-label="Quality assessment"] summary')].filter(e => e.innerText.includes(want)).length === count + 1`, want, count)
			for _, tc := range []struct {
				state int32
				want  string
			}{{1, "Not assessed"}, {2, "Assessment unavailable"}} {
				compatibility.Store(tc.state)
				page.MustElementR("button", "Refresh Bot").MustClick()
				page.MustElementR(`[aria-label="Quality assessment"] summary`, tc.want).MustVisible()
			}
			page.MustElementR("button", "^Close$").MustClick()
			if !page.MustEval(`()=>window.qualityComposer===document.querySelector('[data-testid="chat-input"]') && window.qualityComposer.value==='keep quality draft'`).Bool() {
				t.Fatal("Details remounted/lost composer")
			}
			t.Logf("built Studio live/reload: %s, canonical reports=%d, no rejected drafts or new approvals; legacy/unknown DTO compatibility and preserved composer", mode, count)
		})
	}
}

func TestBotResponsibilityConfirmedOnlyBrowserAcceptance(t *testing.T) {
	runEmployeeBrowser(t, "responsibility")
}

func TestBotScheduleConfirmedOnlyBrowserAcceptance(t *testing.T) { runEmployeeBrowser(t, "routine") }
func TestBotEmployeeBrowserAcceptance(t *testing.T)              { runEmployeeBrowser(t, "routine") }
func TestBotEmployeeEscalationBrowserAcceptance(t *testing.T) {
	for _, decision := range []string{"approve", "deny", "serverexpiry", "stalerace"} {
		t.Run(decision, func(t *testing.T) { runEmployeeBrowser(t, decision) })
	}
}

// All Bot API responses, operations and transcript events come from production
// handlers and a scoped SQLite store. Only identity/settings, model and physical
// home provisioning are controlled; no browser fetch interception is used.
func runEmployeeBrowser(t *testing.T, scenario string) {
	responsibilityProbe := scenario == "responsibility"
	revisionProbe := scenario == "revision"
	if revisionProbe || responsibilityProbe {
		scenario = "routine"
	}
	probe := newComposerRevisionProbe()
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after npm run build")
	}
	scope := store.BotScopePersonal
	if scenario != "routine" {
		scope = store.BotScopeTeam
	}
	f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
	defer f.worker.Stop()
	principal, _ := execution.PrincipalFromContext(f.ctx)
	principal.Authentication = execution.AuthMethodPlatformJWT
	auth, err := execution.WithPrincipal(f.ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	op := store.BotControlGoalCreate
	payload := map[string]any{"operation": op, "reason": "Owner requested a report goal", "goal": map[string]any{"goal": map[string]any{"title": "Ship employee report", "completion_criteria": "Report delivered", "status": "active"}}}
	if scenario == "routine" {
		op = store.BotControlScheduleCreate
		payload = map[string]any{"operation": op, "reason": "Owner requested every minute", "schedule": map[string]any{"trigger": map[string]any{"kind": "cron", "cron": "* * * * *", "timezone": "UTC", "instructions": "Read local status and report"}}}
	}
	responsibility := "Read local status and report concisely; after 16:00 America/New_York report the next day's outlook."
	confirmation := "I've created the schedule."
	acknowledgmentKind := store.BotReportAcknowledgmentSchedulePending
	if responsibilityProbe {
		op = store.BotControlResponsibility
		payload = map[string]any{"operation": op, "reason": "Owner requested a responsibility update with an explicit timezone", "responsibility": responsibility}
		confirmation = "I've updated my responsibilities."
		acknowledgmentKind = store.BotReportAcknowledgmentControlPending
	}
	applyReached, applyRelease := make(chan struct{}), make(chan struct{})
	var applyOnce sync.Once
	var unblockOnce sync.Once
	unblockApply := func() { unblockOnce.Do(func() { close(applyRelease) }) }
	defer unblockApply()
	var sourceReturned atomic.Bool
	policy := func(ctx context.Context, _ store.BotMandateRef) (store.BotMandatePolicy, error) {
		resources := []string{"propose_bot_control", "ask_user"}
		if scenario == "routine" {
			resources = append(resources, "schedule_job")
		}
		if responsibilityProbe {
			resources = append(resources, entstore.RoutineControlResource(store.BotControlResponsibility))
		}
		ttl := time.Hour
		if scenario == "serverexpiry" {
			ttl = 10 * time.Second
		}
		return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: resources}}, ExceptionConstraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{entstore.RoutineControlResource(store.BotControlGoalCreate)}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: ttl}, nil
	}
	m := &employeeBrowserModel{sourceReturned: &sourceReturned, t: t, payload: payload, captured: make(chan struct{}), release: make(chan struct{})}
	files := newComposedHTTPFiles(t, f.dataDir, f.repo, f.b, nil)
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, MandatePolicy: policy, TurnRegistry: f.registry, Sandbox: composedBrowserBackend{synthesisHTTPBackend{files: files.disk}}, Pool: f.pool, WorkerID: "employee-browser", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(ctx context.Context, _ execution.Principal, _ store.BotScope, _, _ string) (model.LLM, error) {
		// Candidate readiness runs after CommitRun; a model return or policy callback
		// alone is not evidence of a committed source outcome.
		if scenario == "routine" && sourceReturned.Load() {
			runs, readErr := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
			if readErr != nil {
				return nil, readErr
			}
			if len(runs) == 1 && runs[0].FinishedAt != nil {
				applyOnce.Do(func() { close(applyReached) })
				select {
				case <-applyRelease:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return m, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		files.install(c, q, scope)
		workflow, e := launcher.BotWorkflowTools(q, scope)
		if e != nil {
			return nil, e
		}
		return &launcher.BotChatSafeResources{Tools: workflow, PolicyRuntime: agent.BotPolicyRuntime{Guard: employeeBrowserGuard{repo: f.repo, req: q}, Classifier: launcher.BotCatalogueClassifier{}}, WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	daemon.ConfigureBotScheduler(&scheduler.Executor{}, nil, worker.SchedulerCallbacks())
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(m.release) }) }
	defer release()
	ods, err := f.db.ForOrg(principal.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Services{Bots: f.repo, Sessions: ods.ForTeam(principal.TeamSlug).Sessions(), TenantRouter: f.db, PersonalBots: f.repo, PersonalSessions: ods.ForUser(principal.Subject).Sessions(), BotMandatePolicy: policy, BotTurnRegistry: f.registry, BotInteractiveReady: true}
	svc.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
		return worker.AuthorizedOperations(f.repo)
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
		t.Fatal(err)
	}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	routes := []struct {
		path, method string
		h            http.HandlerFunc
	}{
		{root, "GET", api.BotListHandler}, {root + "/{botID}", "GET", api.BotGetHandler}, {root + "/{botID}/snapshot", "GET", api.BotSnapshotHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler}, {root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler}, {root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler}, {root + "/{botID}/messages", "POST", api.BotMessageHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler}, {root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/goals", "GET", api.BotGoalsHandler}, {root + "/{botID}/operations/{operationID}/decision", "POST", api.BotOperationDecisionHandler}, {root + "/{botID}/sessions/{sessionID}/stream", "GET", api.BotHistoryStreamHandler}, {root + "/{botID}/sessions/{sessionID}/events", "GET", api.BotEventsHandler}}
	var decisions atomic.Int64
	var readOnlyIdentity atomic.Bool
	viewer := principal
	viewer.Subject = uuid.NewString()
	viewerAuth, err := execution.WithPrincipal(auth, viewer)
	if err != nil {
		t.Fatal(err)
	}
	viewerAuth = store.WithTenantContext(viewerAuth, &store.TenantContext{UserID: viewer.Subject, OrgSlug: viewer.OrgSlug, TeamSlug: viewer.TeamSlug})
	if scope == store.BotScopeTeam {
		org, e := f.db.Organizations().GetBySlug(auth, principal.OrgSlug)
		if e != nil {
			t.Fatal(e)
		}
		team, e := ods.Teams().GetTeamBySlug(auth, principal.TeamSlug)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.db.Users().Create(auth, &store.User{ID: viewer.Subject, Email: viewer.Subject + "@example.test", DisplayName: "Viewer", Status: "active"}); e != nil {
			t.Fatal(e)
		}
		if e = f.db.Organizations().AddMember(auth, viewer.Subject, org.ID, "member"); e != nil {
			t.Fatal(e)
		}
		if e = ods.Teams().AddMember(auth, &store.TeamMembership{UserID: viewer.Subject, TeamID: team.ID, Role: "viewer"}); e != nil {
			t.Fatal(e)
		}
	}
	var raceTransition func()
	var responseStatus atomic.Int64
	var decisionBody struct {
		PayloadDigest    string `json:"payload_digest"`
		ExpectedRevision int64  `json:"expected_revision"`
		Approve          bool   `json:"approve"`
	}
	for _, r := range routes {
		router.HandleFunc(r.path, r.h).Methods(r.method)
	}
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fixture any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			fixture = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			userID := principal.Subject
			if readOnlyIdentity.Load() {
				userID = viewer.Subject
			}
			role := "owner"
			if readOnlyIdentity.Load() {
				role = "viewer"
			}
			fixture = map[string]any{"user": map[string]any{"id": userID, "email": "employee@example.test", "display_name": "Owner", "role": role, "platform_role": "user"}, "org": map[string]any{"slug": principal.OrgSlug}, "team": principal.TeamSlug}
		case "/api/orgs":
			fixture = map[string]any{"orgs": []any{}, "active_org": principal.OrgSlug}
		case "/api/teams":
			fixture = []any{map[string]any{"slug": principal.TeamSlug, "name": "Team"}}
		case "/api/settings/status":
			fixture = map[string]any{"setupRequired": false}
		case "/api/version":
			fixture = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			fixture = []any{}
		case "/api/settings/config":
			fixture = map[string]any{}
		case "/api/settings/providers/effective":
			fixture = map[string]any{"providers": map[string]any{}}
		}
		if fixture != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(fixture)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if info, e := os.Stat(path); e == nil && !info.IsDir() {
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/") && r.Method == "POST" {
			decisions.Add(1)
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if raceTransition != nil {
				raceTransition()
			}
			encoded, _ := json.Marshal(body)
			json.Unmarshal(encoded, &decisionBody)
			r.Body = http.NoBody
			r.Body = ioNopEmployee(string(encoded))
			if r.URL.Query().Get("scope") != string(scope) {
				t.Error("decision lost scope")
			}
		}
		requestAuth := auth
		if readOnlyIdentity.Load() {
			requestAuth = viewerAuth
		}
		requestCtx, cancel := context.WithCancel(requestAuth)
		defer cancel()
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		if strings.Contains(r.URL.Path, "/operations/") && r.Method == "POST" {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, r.WithContext(store.WithServices(requestCtx, svc)))
			responseStatus.Store(int64(recorder.Code))
			for key, values := range recorder.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		if revisionProbe && strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodPost {
			probe.serve(t, w, r.WithContext(store.WithServices(requestCtx, svc)), router, f.repo, auth, f.b.ID)
			return
		}
		router.ServeHTTP(w, r.WithContext(store.WithServices(requestCtx, svc)))
	}))
	defer server.Close()
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		var ok bool
		bin, ok = browserlauncher.LookPath()
		if !ok {
			t.Fatal("browser prerequisite unavailable")
		}
	}
	launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(35 * time.Second)
	page = page.WithPanic(func(v any) {
		text, _ := page.CancelTimeout().Timeout(3 * time.Second).Eval(`()=>JSON.stringify({home:document.body.innerText,scheduleReplay:window.scheduleReplay,scheduleReadyState:window.scheduleStream?.readyState,scheduleEvents:window.scheduleEvents})`)
		t.Fatalf("browser: %v\n%v", v, text)
	})
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	if page.MustEval(`()=>document.querySelectorAll('[data-operation-id]').length`).Int() != 0 {
		t.Fatal("nonempty initial operation state")
	}
	message := "Create a goal to ship employee report"
	if scenario == "routine" {
		message = "Schedule every minute to read local status and report"
	}
	if responsibilityProbe {
		message = "Update your responsibility: " + responsibility
	}
	page.MustElement(`[data-testid="chat-input"]`).MustInput(message)
	page.MustElement(`[data-testid="send-button"]`).MustClick()
	page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
		if e != nil {
			t.Fatal(e)
		}
		if len(runs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("UI message not admitted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	poll := make(chan error, 1)
	go func() { poll <- worker.Poll(context.Background()) }()
	select {
	case <-m.captured:
	case e := <-poll:
		t.Fatalf("worker did not capture: %v", e)
	case <-time.After(15 * time.Second):
		t.Fatal("model capture timeout")
	}
	assertNoSchedulePending := func() {
		t.Helper()
		if !page.MustEval(`()=>{const w=document.querySelector('[aria-label="Bot workspace"]');return !w.querySelector('[data-operation-id]') && !/durably queued|Change queued|Applying change|No schedule change is verified|Work is still pending|Your request has been recorded/.test(w.innerText)}`).Bool() {
			t.Fatalf("pending schedule leaked into Home: %s", page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText`).Str())
		}
	}
	if scenario == "routine" {
		assertNoSchedulePending()
		if page.MustEval(`()=>document.querySelectorAll('[data-review-id]').length`).Int() != 0 {
			t.Fatal("routine demanded review")
		}
	} else {
		page.MustElement(`[data-operation-id]`).MustVisible()
	}
	if responsibilityProbe {
		var pending store.BotSnapshot
		if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &pending); err != nil {
			t.Fatal(err)
		}
		if len(pending.Operations) != 1 || pending.Operations[0].Operation != store.BotControlResponsibility || pending.Operations[0].Presentation != store.BotOperationPresentationInternal || pending.Operations[0].SourceRunID == uuid.Nil || pending.Operations[0].SourceUserEventID == "" {
			t.Fatalf("responsibility pending snapshot lost internal source identity: %+v", pending.Operations)
		}
		// Metadata commits do not run schedule candidate-readiness. Hold the
		// controlled model after real capture to exercise pending live/reload.
		page.MustReload()
		page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
		assertNoSchedulePending()
		page.MustEval(`url=>{window.scheduleEvents=[];window.scheduleReplay=false;window.scheduleStream=new EventSource(url);for(const type of ['bot_operation','text'])window.scheduleStream.addEventListener(type,e=>window.scheduleEvents.push({type,data:JSON.parse(e.data)}));window.scheduleStream.addEventListener('replay_complete',()=>window.scheduleReplay=true)}`, f.path+"/sessions/"+f.b.HomeSessionID.String()+"/events?scope="+string(scope))
		page.MustWait(`()=>window.scheduleReplay`)
	}
	release()
	if scenario == "routine" && !responsibilityProbe {
		select {
		case <-applyReached:
		case e := <-poll:
			t.Fatalf("source did not reach terminal pending apply: %v", e)
		case <-time.After(10 * time.Second):
			t.Fatal("pending apply timeout")
		}
		committedRuns, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
		if e != nil || len(committedRuns) != 1 {
			t.Fatalf("source readback: %v %v", committedRuns, e)
		}
		source, e := f.repo.GetRun(auth, f.b.ID, committedRuns[0].ID)
		if e != nil || source.Status != store.RunSucceeded || source.FinishedAt == nil {
			t.Fatalf("source must be durably terminal before apply: %+v %v", source, e)
		}
		reportJSON, _ := json.Marshal(source.Report)
		if len(source.Report.Unverified) != 0 || len(source.Report.Changed) != 0 || len(source.Report.Unchanged) != 1 || source.Report.Acknowledgment == nil || source.Report.Acknowledgment.Kind != acknowledgmentKind || len(source.Report.Acknowledgment.OperationIDs) != 1 {
			t.Fatalf("expected pure server-marked schedule acknowledgment, not an applied change: %s", reportJSON)
		}
		var pending store.BotSnapshot
		if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &pending); err != nil {
			t.Fatal(err)
		}
		if len(pending.Operations) != 1 || pending.Operations[0].Presentation != store.BotOperationPresentationInternal || pending.Operations[0].OperationID != source.Report.Acknowledgment.OperationIDs[0] || pending.Operations[0].SourceRunID != source.ID || pending.Operations[0].SourceUserEventID == "" {
			t.Fatalf("pending snapshot lost internal identity: %+v", pending.Operations)
		}
		assertNoSchedulePending()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
		page.MustReload()
		page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
		assertNoSchedulePending()
		// Observe the same production SSE endpoint as Studio, without intercepting
		// or replacing fetch. Keep it open across the actual worker application.
		page.MustEval(`url=>{window.scheduleEvents=[];window.scheduleReplay=false;window.scheduleStream=new EventSource(url);for(const type of ['bot_operation','text'])window.scheduleStream.addEventListener(type,e=>window.scheduleEvents.push({type,data:JSON.parse(e.data)}));window.scheduleStream.addEventListener('replay_complete',()=>window.scheduleReplay=true)}`, f.path+"/sessions/"+f.b.HomeSessionID.String()+"/events?scope="+string(scope))
		page.MustWait(`()=>window.scheduleReplay`)
		t.Logf("pure acknowledgment committed status=%s; pending snapshot retains operation=%s/source=%s; live and reload show working with zero pending cards/prose", source.Status, pending.Operations[0].OperationID, source.ID)
		unblockApply()
	}
	if err = <-poll; err != nil {
		t.Fatal(err)
	}
	runs, err := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatal("unexpected runs", err)
	}
	if responsibilityProbe {
		source, e := f.repo.GetRun(auth, f.b.ID, runs[0].ID)
		if e != nil || source.Status != store.RunSucceeded || source.Report.Acknowledgment == nil || source.Report.Acknowledgment.Kind != acknowledgmentKind || len(source.Report.Acknowledgment.OperationIDs) != 1 {
			t.Fatalf("responsibility source must retain pure control_pending acknowledgment: %+v %v", source, e)
		}
	}
	rt, err := worker.AuthorizedOperations(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := rt.ListAuthorizedOperations(auth, *runs[0].MandateRef)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations %+v %v", ops, err)
	}
	assertFinalMetadata := func(status store.BotOperationStatus) {
		t.Helper()
		var snapshot store.BotSnapshot
		if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Operations) != 1 {
			t.Fatalf("expected one persisted metadata outcome: %+v", snapshot.Operations)
		}
		final := snapshot.Operations[0]
		if final.OperationID != ops[0].ID || final.SourceRunID != runs[0].ID || final.SourceUserEventID == "" || final.Presentation != store.BotOperationPresentationConversation || final.Status != status || final.Summary == "" {
			t.Fatalf("final metadata outcome lost conversation identity: %+v", final)
		}
		if status == store.BotOperationApplied && final.Summary != "I've added the goal." {
			t.Fatalf("unexpected approved goal confirmation: %q", final.Summary)
		}
		if status == store.BotOperationCancelled && final.Summary != "I couldn't confirm completion of the requested work. You cancelled the pending work. Review the request before starting another run." {
			t.Fatalf("unexpected cancelled goal confirmation: %q", final.Summary)
		}
		page.MustWait(`summary=>{const w=document.querySelector('[aria-label="Bot workspace"]');const ops=w?.querySelectorAll('[data-operation-id]');const bubbles=w?.querySelectorAll('[data-operation-id] [data-testid="agent-message-bubble"]');return ops?.length===1 && bubbles?.length===1 && bubbles[0].innerText.includes(summary)}`, final.Summary)
		if !page.MustEval(`summary=>{const w=document.querySelector('[aria-label="Bot workspace"]');return !w.querySelector('[data-operation-id] h3') && ![...w.querySelectorAll('[data-operation-id] button')].some(b=>/^(Approve |Deny$)/.test(b.innerText)) && w.innerText.split(summary).length===2}`, final.Summary).Bool() {
			t.Fatal("final metadata outcome must be exactly one ordinary bubble without a technical card or decision controls")
		}
		for _, id := range []string{ops[0].ID.String(), ops[0].Provenance.SourceRunID.String(), ops[0].Provenance.PayloadDigest} {
			if page.MustEval(`id=>document.querySelector('[aria-label="Bot workspace"]').innerText.includes(id)`, id).Bool() {
				t.Fatal("final metadata outcome leaked authority identity")
			}
		}
	}
	if scenario != "routine" {
		goals, e := f.repo.(store.BotGoalStore).ListGoals(auth, f.b.ID)
		if e != nil || len(goals) != 0 {
			t.Fatalf("goal mutated before owner decision: %+v %v", goals, e)
		}
		page.MustElementR(`[data-operation-id] h3`, "Blocked").MustVisible()
		page.MustElementR(`[data-operation-id] dd`, "Ship employee report").MustVisible()
		exceptionCard := page.MustElement(`[data-operation-id="` + ops[0].ID.String() + `"]`)
		if ops[0].Exception == nil {
			t.Fatal("missing bounded exception")
		}
		if expiry := exceptionCard.MustElement("time"); expiry.MustText() != ops[0].Exception.ExpiresAt.UTC().Format(time.RFC3339Nano) {
			t.Fatal("exact exception expiry not visible")
		}
		if !strings.Contains(exceptionCard.MustText(), "I’m asking for permission for this saved action only.") {
			t.Fatal("exception lifetime scope missing")
		}
		if page.MustEval(`()=>document.querySelector('[data-operation-id]').innerText.includes('payload_digest')`).Bool() {
			t.Fatal("raw authority leaked")
		}
		if scenario == "approve" {
			readOnlyIdentity.Store(true)
			page.MustReload()
			page.MustElementR(`[data-operation-id] h3`, "Blocked").MustVisible()
			page.MustElementR(`[data-operation-id] dd`, "Ship employee report").MustVisible()
			if page.MustEval(`()=>document.querySelectorAll('[data-operation-id] button').length`).Int() != 0 {
				t.Fatal("viewer retained mutation controls")
			}
			body, _ := json.Marshal(map[string]any{"payload_digest": ops[0].Provenance.PayloadDigest, "expected_revision": ops[0].Revision, "approve": false})
			status := page.MustEval(`async (url, body)=>(await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body})).status`, f.path+"/operations/"+ops[0].ID.String()+"/decision?scope=team", string(body)).Int()
			if status != http.StatusForbidden {
				t.Fatalf("viewer decision status %d", status)
			}
			saved, e := rt.ReadAuthorizedOperation(auth, ops[0].MandateRef, ops[0].ID)
			if e != nil || !reflect.DeepEqual(saved, &ops[0]) {
				t.Fatalf("viewer mutated operation: %+v %v", saved, e)
			}
			readOnlyIdentity.Store(false)
			decisions.Store(0)
			page.MustReload()
			t.Log("authenticated same-tenant viewer read real blocked card without mutation controls; direct decision forbidden; durable operation unchanged")
		}
		if scenario == "serverexpiry" || scenario == "stalerace" {
			before := ops[0]
			if scenario == "serverexpiry" {
				if before.Exception == nil || !time.Now().Before(before.Exception.ExpiresAt) {
					t.Fatal("expiry fixture must expose a live exception before submission")
				}
				// Hold the submitted request until the real persisted expiry. The
				// browser submits valid coordinates while the card is still live;
				// only ordinary server time advances, not private clocks or grants.
				raceTransition = func() {
					wait := time.Until(before.Exception.ExpiresAt) + time.Millisecond
					if wait > 10*time.Second {
						t.Error("expiry wait exceeds fixture TTL")
						return
					}
					if wait > 0 {
						time.Sleep(wait)
					}
				}
			}
			if scenario == "stalerace" {
				raceTransition = func() {
					saved, e := rt.(store.BotRoutineExceptionStore).DecideRoutineException(auth, store.BotRoutineExceptionDecision{MandateRef: before.MandateRef, OperationID: before.ID, PayloadDigest: before.Provenance.PayloadDigest, ExpectedRevision: before.Revision, Approve: false})
					if e != nil {
						t.Errorf("concurrent deny: %v", e)
						return
					}
					before = *saved
				}
			}
			page.MustElementR(`[data-operation-id] button`, "^Approve create goal$").MustClick()
			if scenario == "serverexpiry" {
				page.MustWait(`() => document.querySelector('[data-operation-id] [role="alert"]')?.innerText.trim() === 'I can’t access this Bot with your current authorization. Please check your access and refresh. I can’t confirm the outcome of your work.'`)
			} else {
				// SSE may replace the stale authorization card with the concurrent
				// denial before the rejected request can render its local alert.
				page.MustWait(`()=>!!document.querySelector('[data-operation-id] [role="alert"], [data-operation-id] [data-testid="agent-message-bubble"]')`)
			}
			// The saved exception shares the mandate expiry; verify rejects the
			// expired mandate as forbidden before reaching the grant-expiry branch.
			wantStatus := http.StatusForbidden
			if scenario == "stalerace" {
				wantStatus = http.StatusConflict
			}
			responseDeadline := time.Now().Add(12 * time.Second)
			for responseStatus.Load() == 0 && time.Now().Before(responseDeadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if decisions.Load() != 1 || !decisionBody.Approve || int(responseStatus.Load()) != wantStatus || decisionBody.ExpectedRevision != ops[0].Revision || decisionBody.ExpectedRevision <= 0 || decisionBody.PayloadDigest != ops[0].Provenance.PayloadDigest {
				t.Fatalf("negative decision status=%d coordinates=%+v", responseStatus.Load(), decisionBody)
			}
			saved, e := rt.ReadAuthorizedOperation(auth, before.MandateRef, before.ID)
			if e != nil || !reflect.DeepEqual(saved, &before) {
				t.Fatalf("rejected request changed durable operation: %+v %v", saved, e)
			}
			goals, e := f.repo.(store.BotGoalStore).ListGoals(auth, f.b.ID)
			if e != nil || len(goals) != 0 {
				t.Fatalf("negative goals %v %v", goals, e)
			}
			page.MustElement(`[data-operation-id]`).MustVisible()
			if !page.MustEval(`()=>[...document.querySelectorAll('[data-operation-id] button')].filter(b=>/^(Approve |Deny$)/.test(b.innerText)).every(b=>b.disabled)`).Bool() {
				t.Fatal("error allows blind retry")
			}
			if page.MustEval(`() => !!document.querySelector('[data-operation-id] [role="alert"]')`).Bool() {
				want := "I can’t access this Bot with your current authorization. Please check your access and refresh. I can’t confirm the outcome of your work."
				if scenario == "stalerace" {
					want = "I need you to refresh and review the current saved action before trying again. I can’t confirm this request’s outcome."
				}
				if !page.MustEval(`want => document.querySelector('[data-operation-id] [role="alert"]').innerText.trim() === want`, want).Bool() {
					t.Fatal("rejected decision must show the exact typed employee explanation, not raw server diagnostics")
				}
			}
			t.Logf("%s: HTTP %d, valid revision %d, persisted revision %d status %s unchanged by rejected request; zero goals", scenario, wantStatus, decisionBody.ExpectedRevision, before.Revision, before.Status)
			if scenario == "stalerace" {
				// Regardless of whether SSE already replaced the error card,
				// restore the canonical concurrent denial from persisted history.
				calls := m.calls.Load()
				page.MustReload()
				assertFinalMetadata(store.BotOperationCancelled)
				page.MustReload()
				assertFinalMetadata(store.BotOperationCancelled)
				if m.calls.Load() != calls {
					t.Fatal("stale decision reload replayed model")
				}
				t.Log("concurrent denial restored exactly one ordinary cancelled bubble on repeated reload, without replay")
			}
			return
		}
		button := "^Deny$"
		if scenario == "approve" {
			button = "^Approve create goal$"
		}
		page.MustElementR(`[data-operation-id] button`, button).MustClick()
		page.MustWait(`()=>![...document.querySelectorAll('[data-operation-id] button')].some(b=>/^(Approve |Deny$)/.test(b.innerText))`)
		if decisions.Load() != 1 || decisionBody.PayloadDigest != ops[0].Provenance.PayloadDigest || decisionBody.ExpectedRevision != ops[0].Revision || decisionBody.Approve != (scenario == "approve") {
			t.Fatalf("decision coordinates incorrect: %+v", decisionBody)
		}
		if err = worker.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	expected := store.BotOperationApplied
	if scenario == "deny" {
		expected = store.BotOperationCancelled
	}
	assertFinalOperation := func() {
		t.Helper()
		if scenario != "routine" {
			assertFinalMetadata(expected)
			return
		}
		wantConfirmation := confirmation
		if !responsibilityProbe {
			wantConfirmation += " It runs every minute. Next run:"
		}
		page.MustElementR(`[data-operation-id] [data-testid="agent-message-bubble"]`, wantConfirmation).MustVisible()
		if !page.MustEval(`confirmation=>{const w=document.querySelector('[aria-label="Bot workspace"]'); return w.querySelectorAll('[data-operation-id]').length===1 && w.querySelectorAll('[data-operation-id] [data-testid="agent-message-bubble"]').length===1 && !w.querySelector('[data-operation-id] h3') && !/durably queued|Change queued|Applying change|No schedule change is verified|Work is still pending|Your request has been recorded/.test(w.innerText) && w.innerText.split(confirmation).length===2}`, confirmation).Bool() {
			t.Fatal("schedule must appear exactly once as friendly confirmed prose, without pending acknowledgment or technical card")
		}
		for _, id := range []string{ops[0].ID.String(), ops[0].Provenance.SourceRunID.String(), ops[0].Provenance.PayloadDigest} {
			if page.MustEval(`id=>document.querySelector('[aria-label="Bot workspace"]').innerText.includes(id)`, id).Bool() {
				t.Fatalf("internal identity leaked into visible schedule: %s", id)
			}
		}
	}
	assertFinalOperation()
	if scenario == "routine" {
		var applied store.BotSnapshot
		if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &applied); err != nil {
			t.Fatal(err)
		}
		if len(applied.Operations) != 1 || applied.Operations[0].Presentation != store.BotOperationPresentationConversation || applied.Operations[0].Status != store.BotOperationApplied || applied.Operations[0].OperationID != ops[0].ID || applied.Operations[0].SourceRunID != runs[0].ID || (!responsibilityProbe && applied.Operations[0].VerifiedSchedule == nil) || (responsibilityProbe && applied.Operations[0].VerifiedSchedule != nil) {
			t.Fatalf("applied snapshot lost confirmed identity: %+v", applied.Operations)
		}
		waitScheduleSSE := func(predicate string) {
			t.Helper()
			if err := page.Timeout(8 * time.Second).Wait(rod.Eval(predicate)); err != nil {
				diagnostic, diagnosticErr := page.CancelTimeout().Timeout(3 * time.Second).Eval(`()=>JSON.stringify({readyState:window.scheduleStream?.readyState,replay:window.scheduleReplay,events:window.scheduleEvents,home:document.querySelector('[aria-label="Bot workspace"]')?.innerText})`)
				t.Fatalf("schedule SSE wait: %v; diagnostic=%v diagnosticError=%v", err, diagnostic, diagnosticErr)
			}
		}
		waitScheduleSSE(`()=>window.scheduleEvents.some(e=>e.type==='bot_operation' && e.data.presentation==='conversation')`)
		if !page.MustEval(`(id, source)=>{const rows=window.scheduleEvents;const ops=rows.filter(e=>e.type==='bot_operation').map(e=>e.data);return ops.some(o=>o.operation_id===id && o.presentation==='internal' && o.source_run_id===source) && ops.filter(o=>o.operation_id===id && o.presentation==='conversation' && o.status==='applied').length===1 && !rows.some(e=>e.type==='text' && /durably queued|Your request has been recorded/.test(e.data.text||''))}`, ops[0].ID.String(), runs[0].ID.String()).Bool() {
			t.Fatalf("SSE lost internal heads, duplicated confirmation, or published pending prose: %s", page.MustEval(`()=>JSON.stringify(window.scheduleEvents)`).Str())
		}
		page.MustEval(`()=>window.scheduleStream.close()`)
		t.Log("production SSE retained internal pending identity and emitted exactly one applied conversation event, with no pending prose")
		jobs := ods.ForUser(principal.Subject).ScheduledJobs().List(auth)
		if responsibilityProbe {
			current, e := f.repo.Get(auth, f.b.ID)
			if e != nil {
				t.Fatal(e)
			}
			version, e := f.repo.GetVersion(auth, f.b.ID, current.Version)
			if e != nil || version.Charter != responsibility || len(jobs) != 0 {
				t.Fatalf("responsibility readback must preserve exact charter without creating schedules: %+v jobs=%+v err=%v", version, jobs, e)
			}
		} else if len(jobs) != 1 || !jobs[0].Enabled || jobs[0].NextRun == nil {
			t.Fatalf("schedule readback %+v", jobs)
		}
	} else {
		goals, e := f.repo.(store.BotGoalStore).ListGoals(auth, f.b.ID)
		want := 0
		if scenario == "approve" {
			want = 1
		}
		if e != nil || len(goals) != want {
			t.Fatalf("goals %v %v", goals, e)
		}
		if scenario == "approve" && (goals[0].Title != "Ship employee report" || goals[0].CompletionCriteria != "Report delivered") {
			t.Fatalf("approval mutated a different goal: %+v", goals)
		}
	}
	if revisionProbe {
		m.laterReport.Store(true)
		probe.check(t, page, f.repo, auth, f.b, func() error { return worker.Poll(auth) })
		return
	}
	before := m.calls.Load()
	page.MustReload()
	assertFinalOperation()
	if page.MustEval(`()=>document.querySelectorAll('[data-operation-id]').length`).Int() != 1 || m.calls.Load() != before {
		t.Fatal("reload duplicated operation or replayed model")
	}
	if page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText.includes('Changes made')`).Bool() {
		t.Fatal("synthetic report rendered")
	}
	for _, width := range []int{1280, 390} {
		for _, dark := range []bool{false, true} {
			page.MustSetViewport(width, 844, 1, false)
			page.MustEval(`dark=>document.documentElement.classList.toggle('dark',dark)`, dark)
			page.MustElement(`[data-operation-id]`).MustVisible()
			if !page.MustEval(`()=>{const c=document.querySelector('[data-operation-id]'); const r=c.getBoundingClientRect(); const title=c.querySelector('h3, [data-testid="agent-message-bubble"]'); return r.width>100 && !!title && getComputedStyle(title).fontSize !== '0px' && c.scrollWidth<=c.clientWidth+2}`).Bool() {
				t.Log("screenshot readability diagnostic: operation card geometry requires manual review")
			}
			dir := "../../.tmp/employee-browser-artifacts"
			if err = os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			shot, e := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
			if e != nil {
				t.Fatal(e)
			}
			artifactScenario := scenario
			if responsibilityProbe {
				artifactScenario = "responsibility"
			}
			if e = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d-dark-%t.png", artifactScenario, width, dark)), shot, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	if responsibilityProbe {
		m.laterReport.Store(true)
		page.MustElement(`[data-testid="chat-input"]`).MustInput("Give me the current local status report")
		page.MustElement(`[data-testid="send-button"]`).MustClick()
		deadline := time.Now().Add(10 * time.Second)
		for {
			runs, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if len(runs) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("later report turn not admitted")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if e := worker.Poll(context.Background()); e != nil {
			t.Fatal(e)
		}
		assertLater := func() {
			t.Helper()
			page.MustElementR(`[data-testid="agent-message-bubble"]`, "Later report: local status is healthy.").MustVisible()
			assertFinalOperation()
			if !page.MustEval(`() => {
				const operation=document.querySelector('[data-operation-id]');
				const report=[...document.querySelectorAll('[data-testid="agent-message-bubble"]')].find(e=>e.innerText.includes('Later report: local status is healthy.'));
				return !!report && !operation.contains(report) && !!(operation.compareDocumentPosition(report) & Node.DOCUMENT_POSITION_FOLLOWING);
			}`).Bool() {
				t.Fatal("later report moved or restamped responsibility confirmation")
			}
		}
		assertLater()
		beforeReload := m.calls.Load()
		page.MustReload()
		assertLater()
		if m.calls.Load() != beforeReload {
			t.Fatal("later report reload replayed model")
		}
		t.Log("responsibility: exact charter committed; one ordinary confirmation live/reload and after later report live/reload; zero pending prose/cards and no replay")
		return
	}
	if scenario == "routine" {
		m.question.Store(true)
		page.MustElement(`[data-testid="chat-input"]`).MustInput("Ask me which employee report to prepare next")
		page.MustElement(`[data-testid="send-button"]`).MustClick()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
		deadline := time.Now().Add(10 * time.Second)
		for {
			runs, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if len(runs) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("question turn not admitted")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if e := worker.Poll(context.Background()); e != nil {
			t.Fatal(e)
		}
		page.MustElementR(`[data-review-id]`, "Which employee report should I prepare next").MustVisible()
		assertLast := func() {
			if !page.MustEval(`()=>{const cards=[...document.querySelectorAll('[data-operation-id],[data-review-id]')].filter(e=>e.checkVisibility());return cards.length===2 && cards.at(-1).hasAttribute('data-review-id')}`).Bool() {
				t.Fatalf("pending question must remain after the single applied operation: %s", page.MustEval(`()=>JSON.stringify([...document.querySelectorAll('[data-operation-id],[data-review-id]')].map(e=>({tag:e.tagName,text:e.innerText,html:e.outerHTML.slice(0,400)})))`).Str())
			}
		}
		assertLast()
		page.MustReload()
		page.MustElementR(`[data-review-id]`, "Which employee report should I prepare next").MustVisible()
		assertLast()
	}
	t.Log("built Studio + real HTTP + persisted worker operation; canonical reload and four viewport/theme screenshots")
}

type employeeBrowserGuard struct {
	repo store.BotStore
	req  bot.RunRequest
}

func (g employeeBrowserGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if g.req.Mandate == nil {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	if e := g.req.Mandate.Check(ctx); e != nil {
		return execution.PolicyDecision{}, e
	}
	if e := g.req.Budget.Check(ctx); e != nil {
		return execution.PolicyDecision{}, e
	}
	b, e := g.repo.Get(ctx, g.req.Bot.ID)
	if e != nil {
		return execution.PolicyDecision{}, e
	}
	r, e := g.repo.GetRun(ctx, g.req.Bot.ID, g.req.Run.ID)
	if e != nil {
		return execution.PolicyDecision{}, e
	}
	if !g.req.Run.Intent.AllowsStatus(b.Status) || b.Version != g.req.Version.Version || b.OwnerID != g.req.Bot.OwnerID || r.Status != store.RunRunning || r.Lease.Fence != g.req.Run.Lease.Fence || r.Lease.WorkerID != g.req.Run.Lease.WorkerID || !time.Now().Before(r.Lease.ExpiresAt) {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		grant, e := g.req.Mandate.AuthorizeAction(ctx, a)
		if e != nil || grant.ActionDigest != d.ActionDigest {
			return d, store.ErrBotForbidden
		}
		d.Kind = execution.PolicyAllow
	}
	return d, nil
}

// TestBotEmployeeObjectiveBrowser uses the production scoped worker and configured
// search route. Browser reads never synthesize the employee transcript.
func TestBotEmployeeObjectiveBrowser(t *testing.T) { runBotEmployeeObjectiveSurface(t, false) }

// This uses the same physical Studio/shared Chat journey, with database-only
// runtime records and the Stock Market News clarification used in acceptance.
func TestBotDatabaseEmployeeBrowser(t *testing.T) {
	runBotEmployeeObjectiveSurface(t, false, true)
}

// Invoked by the cmd package's native PTY acceptance with its Execute test binary.
// All Bot HTTP/history/model/search/worker setup is identical to the browser journey.
func TestBotEmployeeObjectivePTYFixture(t *testing.T) {
	if os.Getenv("ASTONISH_EMPLOYEE_PTY_BINARY") == "" {
		t.Skip("owned CLI subprocess required; run TestBotEmployeeObjectivePTY")
	}
	runBotEmployeeObjectiveSurface(t, true)
}

// Database acceptance is strict: a missing native CLI harness is a failure.
func TestBotDatabaseEmployeePTYFixture(t *testing.T) {
	if os.Getenv("ASTONISH_EMPLOYEE_PTY_BINARY") == "" && os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit database employee acceptance opt-in required")
	}
	if os.Getenv("ASTONISH_EMPLOYEE_PTY_BINARY") == "" {
		t.Fatal("ASTONISH_EMPLOYEE_PTY_BINARY is required; run TestBotDatabaseEmployeePTY")
	}
	runBotEmployeeObjectiveSurface(t, true, true)
}

func runBotEmployeeObjectiveSurface(t *testing.T, nativePTY bool, databaseMode ...bool) {
	database := len(databaseMode) > 0 && databaseMode[0]
	if !nativePTY && os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building Studio")
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if !nativePTY && bin == "" {
		t.Fatal("explicit physical browser required")
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
	if !nativePTY {
		if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "ask_user", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			p, _ := execution.PrincipalFromContext(f.ctx)
			p.Authentication = execution.AuthMethodPlatformJWT
			auth, err := execution.WithPrincipal(f.ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			org, err := f.db.ForOrg(p.OrgSlug)
			if err != nil {
				t.Fatal(err)
			}
			team := org.ForTeam(p.TeamSlug)
			if err = f.db.PlatformSettings().Save(auth, &store.PlatformSettings{PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "selected-search", Model: "sonar"}}); err != nil {
				t.Fatal(err)
			}
			if err = team.Settings().Save(auth, &store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search", Providers: map[string]store.ProviderConfig{"selected-search": {"api_key": "owned-fixture-only"}}}); err != nil {
				t.Fatal(err)
			}
			ss := org.ForUser(p.Subject).Sessions()
			if scope == store.BotScopeTeam {
				ss = team.Sessions()
			}
			route, err := launcher.BotEffectiveConfiguredSearchRoute(auth, &store.Services{PlatformSettings: f.db.PlatformSettings(), OrgSettings: f.db.OrgSettings(p.OrgSlug), Settings: team.Settings()})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user", launcher.BotConfiguredSearchToolName}}}, PublicResearch: &execution.BotPublicResearchScope{Version: 1, Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent, execution.BotResearchPrimary, execution.BotResearchContradictions}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 12}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
			}
			svc := &store.Services{TenantRouter: f.db, Bots: f.repo, PersonalBots: f.repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: policy, BotInteractiveReady: true}
			router := mux.NewRouter()
			root := "/api/studio/bots"
			for _, r := range []struct {
				path, method string
				h            http.HandlerFunc
			}{
				{root, "POST", api.BotCreateHandler}, {root, "GET", api.BotListHandler}, {root + "/{botID}", "GET", api.BotGetHandler}, {root + "/{botID}/snapshot", "GET", api.BotSnapshotHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler}, {root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler}, {root + "/{botID}/goals", "GET", api.BotGoalsHandler}, {root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler}, {root + "/{botID}/approvals/{approvalID}/approve-and-continue", "POST", api.BotApproveContinueHandler}, {root + "/{botID}/sessions/{sessionID}/stream", "GET", api.BotHistoryStreamHandler}, {root + "/{botID}/sessions/{sessionID}/events", "GET", api.BotEventsHandler},
			} {
				router.HandleFunc(r.path, r.h).Methods(r.method)
			}
			var watches atomic.Int32
			var watchMu sync.Mutex
			watchCancels := make(map[int32]context.CancelFunc)
			disconnectWatches := func() {
				watchMu.Lock()
				defer watchMu.Unlock()
				for _, cancel := range watchCancels {
					cancel()
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, root) {
					ctx, cancel := context.WithCancel(auth)
					defer cancel()
					if strings.HasSuffix(r.URL.Path, "/events") {
						watchID := watches.Add(1)
						watchMu.Lock()
						watchCancels[watchID] = cancel
						watchMu.Unlock()
						defer func() {
							watchMu.Lock()
							delete(watchCancels, watchID)
							watchMu.Unlock()
						}()
					}
					stop := context.AfterFunc(r.Context(), cancel)
					defer stop()
					router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
					return
				}
				var value any
				switch r.URL.Path {
				case "/api/auth/setup-status":
					value = map[string]any{"initialized": true, "auth_mode": "builtin"}
				case "/api/auth/me":
					value = map[string]any{"user": map[string]any{"id": p.Subject, "email": "employee@example.test", "display_name": "Owner", "role": "owner", "platform_role": "user"}, "org": map[string]any{"slug": p.OrgSlug}, "team": p.TeamSlug}
				case "/api/orgs":
					value = map[string]any{"orgs": []any{}, "active_org": p.OrgSlug}
				case "/api/teams":
					value = []any{map[string]any{"slug": p.TeamSlug, "name": "Team"}}
				case "/api/settings/status":
					value = map[string]any{"setupRequired": false}
				case "/api/version":
					value = map[string]any{"version": "dev"}
				case "/api/agents", "/api/tools":
					value = []any{}
				case "/api/settings/config":
					value = map[string]any{}
				case "/api/settings/providers/effective":
					value = map[string]any{"providers": map[string]any{}}
				}
				if value != nil {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(value)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/") {
					http.NotFound(w, r)
					return
				}
				http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			}))
			defer server.Close()
			request := func(method, path, body string, status int) []byte {
				t.Helper()
				q, e := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				q.Header.Set("Content-Type", "application/json")
				res, e := server.Client().Do(q)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				b, _ := io.ReadAll(res.Body)
				if res.StatusCode != status {
					t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
				}
				return b
			}
			var created titleHTTPReceipt
			title := "Apple News"
			refinement := "Apple Inc. product launches and material market developments."
			acknowledgement := "I'll investigate Apple Inc. launches and market developments."
			final := "Apple Inc. launched a new device and market revenue rose 5%. Sources: https://fixture.test/product and https://fixture.test/market."
			questionPrompt := "Which Apple news?"
			if database {
				title = "Stock Market News"
				refinement = "U.S. markets and major indexes"
				acknowledgement = "I'll investigate U.S. markets and major indexes."
				questionPrompt = "Which stock markets and indexes should I cover?"
				final = "U.S. markets: S&P 500, Nasdaq Composite and Dow Jones major indexes were checked. Source: https://fixture.test/market."
			}
			createBody, _ := json.Marshal(map[string]any{"name": title, "start_from_title": true, "idempotency_key": "employee-browser-objective"})
			if err = json.Unmarshal(request("POST", root, string(createBody), 201), &created); err != nil || created.InitialTurn == nil {
				t.Fatal("create", err)
			}
			f.b = created.Bot
			if f.b.Status != store.BotActive {
				t.Fatalf("title-created employee must be Active, got %s", f.b.Status)
			}
			f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
			f.registry.Close()
			var runtimeBefore map[string]string
			if database {
				runtimeBefore = databaseRuntimeFileSnapshot(t, f.dataDir)
			}
			var confirmed atomic.Bool
			var queries atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			mdl := &interactiveHTTPModel{t: t, principal: execution.Principal{Subject: p.Subject, Authentication: execution.AuthMethodBotMandate, Surface: execution.Surface("daemon")}}
			mdl.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				if response, ok := f.model.assessment(req); ok {
					t.Error("unexpected mandatory review")
					return response, true
				}
				call := func(name string, args map[string]any) *model.LLMResponse {
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}}
				}
				if !confirmed.Load() {
					return call("ask_user", map[string]any{"kind": "select", "prompt": questionPrompt, "allow_custom_answer": true, "options": []map[string]any{{"id": "company", "label": refinement}, {"id": "fruit", "label": "Fruit harvests"}, {"id": "services", "label": "Apple services"}}}), true
				}
				var evidence strings.Builder
				for _, c := range req.Contents {
					for _, part := range c.Parts {
						if part.FunctionResponse != nil {
							b, _ := json.Marshal(part.FunctionResponse.Response)
							evidence.Write(b)
						}
					}
				}
				seen := evidence.String()
				if database {
					if !strings.Contains(fmt.Sprint(req.Contents), refinement) {
						raw, _ := json.Marshal(req.Contents)
						if !strings.Contains(string(raw), refinement) {
							t.Error("shared producer lost original accepted market answer")
						}
					}
					if strings.Contains(seen, "major indexes were checked") {
						return &model.LLMResponse{Content: genai.NewContentFromText(final, "model")}, true
					}
					response := call("perplexity_web_search", map[string]any{"query": refinement, "max_results": 5, "recency": "week"})
					response.Content.Parts = append([]*genai.Part{{Text: acknowledgement}}, response.Content.Parts...)
					return response, true
				}
				query := "Apple News"
				switch {
				case strings.Contains(seen, "market revenue rose 5%"):
					return &model.LLMResponse{Content: genai.NewContentFromText(final, "model")}, true
				case strings.Contains(seen, "Market developments not covered"):
					query = "Apple Inc. material market revenue developments"
				case strings.Contains(seen, "fruit harvest"):
					query = "Apple Inc. product launches"
				}
				response := call("perplexity_web_search", map[string]any{"query": query, "max_results": 5, "recency": "week"})
				if query == "Apple News" {
					response.Content.Parts = append([]*genai.Part{{Text: acknowledgement}}, response.Content.Parts...)
				}
				return response, true
			}
			search := &employeeConfiguredSearchFixture{run: func(ctx context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
				raw, _ := json.Marshal(req.Contents)
				text := string(raw)
				answer := "Apple fruit harvest increases. Source: https://fixture.test/fruit"
				n := queries.Add(1)
				if n == 1 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if strings.Contains(text, "Apple Inc. material market revenue developments") {
					answer = "Apple Inc. market revenue rose 5%. Source: https://fixture.test/market"
				} else if strings.Contains(text, "Apple Inc. product launches") {
					answer = "Apple Inc. launched a new device. Market developments not covered. Source: https://fixture.test/product"
				}
				if database {
					answer = final
				}
				return &model.LLMResponse{Content: genai.NewContentFromText(answer, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil
			}}
			files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.pool.dir}, repo: f.repo, id: f.b.ID, scope: scope}
			worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: synthesisHTTPBackend{files: files.disk}, Pool: f.pool, MandatePolicy: policy, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mdl, nil
			}, Prepare: func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				runFiles := *files
				runFiles.id = r.Bot.ID
				runFiles.install(c, r, s)
				local, e := launcher.BotScopedChatConfig(c, s)
				if e != nil {
					return nil, e
				}
				episode, e := launcher.NewBotResearchEpisode(r.Run.ID, r.Run.SessionID.String(), launcher.BotResearchOpenWeb, nil)
				if e != nil {
					return nil, e
				}
				return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: local, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, Research: episode, ProviderFactory: func(c context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
					if !provider.IsConfinedProviderSecrets(c) || p != "selected-search" || m != "sonar" || cfg.Providers[p]["api_key"] != "owned-fixture-only" {
						t.Error("lost scoped selected search")
					}
					return search, nil
				}, PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, f.repo}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				once.Do(func() { close(release) })
				worker.Stop()
			}()
			if err = worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			registerHome := func() {
				t.Helper()
				current, e := f.repo.Get(auth, f.b.ID)
				if e != nil {
					t.Fatal(e)
				}
				internal, e := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
				if e != nil {
					t.Fatal(e)
				}
				if e = entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: current.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); e != nil {
					t.Fatal(e)
				}
			}
			if nativePTY {
				runEmployeeNativePTY(t, server, scope, f.b.ID, questionPrompt, refinement, acknowledgement, final, &watches, disconnectWatches, func() []byte {
					return request("GET", root+"/"+f.b.ID.String()+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200)
				}, func() {
					registerHome()
					confirmed.Store(true)
					go func() {
						if e := worker.Poll(context.Background()); e != nil {
							t.Error(e)
						}
					}()
					select {
					case <-started:
					case <-time.After(20 * time.Second):
						t.Fatal("native PTY search did not start")
					}
				}, func() { once.Do(func() { close(release) }) })
				wantQueries := int32(3)
				if database {
					wantQueries = 1
					if after := databaseRuntimeFileSnapshot(t, f.dataDir); !reflect.DeepEqual(runtimeBefore, after) {
						t.Fatal("native conversation operations wrote runtime content files")
					}
					assertDatabaseRuntimeComplete(t, f)
				}
				run, e := f.repo.GetRun(auth, f.b.ID, created.InitialTurn.Run.ID)
				if e != nil || run.Status != store.RunSucceeded || queries.Load() != wantQueries || mdl.reviews != 0 || len(run.Report.Artifacts) != 0 || run.Report.EmployeeMessage == nil || run.Report.EmployeeMessage.Text != final {
					t.Fatalf("native shared-worker result: %+v searches=%d reviews=%d err=%v", run, queries.Load(), mdl.reviews, e)
				}
				return
			}
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(30 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`()=>document.body.innerText`)
				t.Fatalf("employee browser: %v\n%v", v, text)
			})
			finalNeedle := "market revenue rose 5%"
			finalBody := "Apple Inc. launched a new device and market revenue rose 5%."
			wantQueries := int32(3)
			if database {
				finalNeedle, finalBody, wantQueries = "major indexes were checked", final, 1
			}
			page.MustElementR(`[role="radio"]`, refinement).MustClick()
			page.MustWait(`() => !document.querySelector('[role="radio"]')`)
			page.MustWait(`want=>document.querySelector('[data-testid="message-area"]').innerText.includes(want)`, refinement)
			current, err := f.repo.Get(auth, f.b.ID)
			if err != nil {
				t.Fatal(err)
			}
			internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err = entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: current.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			confirmed.Store(true)
			done := make(chan error, 1)
			go func() { done <- worker.Poll(context.Background()) }()
			select {
			case <-started:
			case err = <-done:
				t.Fatal("search did not start", err)
			case <-time.After(20 * time.Second):
				t.Fatal("search timeout")
			}
			page.MustWait(`want=>document.querySelector('[data-testid="message-area"]').innerText.includes(want)`, acknowledgement)
			before := watches.Load()
			server.CloseClientConnections()
			deadline := time.Now().Add(15 * time.Second)
			for watches.Load() <= before && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if watches.Load() <= before {
				t.Fatal("browser did not reconnect before source completion")
			}
			once.Do(func() { close(release) })
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			page.MustWait(`want=>document.querySelector('[data-testid="message-area"]').innerText.includes(want)`, finalNeedle)
			assertAnswer := func(mode string) {
				t.Helper()
				text := page.MustElement(`[data-testid="message-area"]`).MustText()
				if !page.MustEval(`want => Array.from(document.querySelectorAll('.chat-bubble-user')).filter(n => n.innerText === want).length === 1`, refinement).Bool() {
					t.Fatalf("%s expected one accepted owner bubble: %s", mode, text)
				}
				for _, s := range []string{acknowledgement, finalBody} {
					if strings.Count(text, s) != 1 {
						t.Fatalf("%s expected once %q: %s", mode, s, text)
					}
				}
				if strings.Index(text, refinement) >= strings.Index(text, acknowledgement) || strings.Index(text, acknowledgement) >= strings.Index(text, finalBody) {
					t.Fatalf("%s reordered committed owner/acknowledgement/final: %s", mode, text)
				}
				for _, s := range []string{"Review candidate", "Approve work", "Download", "Open in Files panel"} {
					if strings.Contains(text, s) {
						t.Fatalf("needless workflow %q: %s", s, text)
					}
				}
				for _, source := range []string{"https://fixture.test/product", "https://fixture.test/market"} {
					if database && strings.HasSuffix(source, "/product") {
						continue
					}
					if !strings.Contains(text, source) {
						t.Fatalf("missing source %s: %s", source, text)
					}
				}
			}
			assertAnswer("live/reconnect")
			homePath := root + "/" + f.b.ID.String() + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
			committedHistory := request("GET", homePath, "", 200)
			page.MustReload()
			page.MustWait(`want=>document.querySelector('[data-testid="message-area"]')?.innerText.includes(want)`, finalNeedle)
			assertAnswer("reload")
			if restored := request("GET", homePath, "", 200); !reflect.DeepEqual(committedHistory, restored) {
				t.Fatal("reload changed ordered durable SSE IDs or bodies")
			}
			if database {
				if after := databaseRuntimeFileSnapshot(t, f.dataDir); !reflect.DeepEqual(runtimeBefore, after) {
					t.Fatalf("conversation operations wrote runtime content files: before=%v after=%v", runtimeBefore, after)
				}
				assertDatabaseRuntimeComplete(t, f)
			}
			run, err := f.repo.GetRun(auth, f.b.ID, created.InitialTurn.Run.ID)
			if err != nil || run.Status != store.RunSucceeded || queries.Load() != wantQueries || mdl.reviews != 0 || len(run.Report.Artifacts) != 0 || run.Report.EmployeeMessage == nil || run.Report.EmployeeMessage.Text != final {
				t.Fatalf("configured loop: %+v searches=%d reviews=%d err=%v", run, queries.Load(), mdl.reviews, err)
			}
			if watches.Load() <= before {
				t.Fatal("no physical reconnect/reload watch")
			}
			t.Logf("built Studio %s live/reconnect/reload actual scoped configured search=%d reviewer=0 artifact=0 database=%t", scope, wantQueries, database)
		})
	}
}

func runEmployeeNativePTY(t *testing.T, server *httptest.Server, scope store.BotScope, id uuid.UUID, questionPrompt, refinement, acknowledgement, final string, watches *atomic.Int32, disconnect func(), history func() []byte, startWork, release func()) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	tokens, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = tokens.Save(&client.Tokens{AccessToken: "owned-employee-token"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Getenv("ASTONISH_EMPLOYEE_PTY_BINARY"), "-test.run=^TestBotCLIProcess$", "--", "chat", "--bot", id.String(), "--bot-scope", string(scope))
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 60, Cols: 180})
	if err != nil {
		t.Fatal(err)
	}
	defer pt.Close()
	defer func() { _ = cmd.Process.Kill() }()
	var mu sync.Mutex
	var output strings.Builder
	go func() {
		var pending string
		queries := []struct{ request, response string }{{"\x1b]10;?\x1b\\", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"}, {"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"}, {"\x1b]10;?\a", "\x1b]10;rgb:ffff/ffff/ffff\a"}, {"\x1b]11;?\a", "\x1b]11;rgb:0000/0000/0000\a"}, {"\x1b[6n", "\x1b[1;1R"}, {"\x1b[c", "\x1b[?1;2c"}, {"\x1b[0c", "\x1b[?1;2c"}}
		buf := make([]byte, 8192)
		for {
			n, e := pt.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				mu.Unlock()
				pending += string(buf[:n])
				for len(pending) > 0 {
					matched, partial := false, false
					for _, q := range queries {
						if strings.HasPrefix(pending, q.request) {
							_, _ = io.WriteString(pt, q.response)
							pending = pending[len(q.request):]
							matched = true
							break
						}
						partial = partial || strings.HasPrefix(q.request, pending)
					}
					if matched {
						continue
					}
					if partial {
						break
					}
					pending = pending[1:]
				}
			}
			if e != nil {
				return
			}
		}
	}()
	read := func() string { mu.Lock(); defer mu.Unlock(); return ansi.Strip(output.String()) }
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(read(), want) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("native PTY missing %q: %s", want, read())
	}
	send := func(s string) {
		t.Helper()
		if _, e := io.WriteString(pt, s); e != nil {
			t.Fatal(e)
		}
		time.Sleep(180 * time.Millisecond)
	}
	// Decode the complete stream at fixed dimensions. A resize repaint contains
	// terminal deltas, not an independently decodable conversation snapshot.
	frame := func() (string, error) {
		t.Helper()
		mu.Lock()
		raw := []byte(output.String())
		mu.Unlock()
		return employeePTYScreen(raw)
	}
	waitScreen := func(want string, after int) string {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		var text string
		var decodeErr error
		for time.Now().Before(deadline) {
			mu.Lock()
			changed := output.Len() > after
			mu.Unlock()
			if changed {
				// A read may end midway through a UTF-8 glyph or escape. Require
				// a complete valid screen before asserting; persistent decoder
				// errors still fail at the deadline, never via ANSI stripping.
				text, decodeErr = frame()
				if decodeErr == nil && strings.Contains(text, want) {
					return text
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("native PTY screen missing %q: %s; decode error: %v", want, text, decodeErr)
		return ""
	}
	wait("Waiting for answer or review")
	send("/bots approvals\r")
	wait("Question: " + questionPrompt)
	send("y")
	wait("Enter reviews")
	send("\r")
	wait(`Confirm answer "company"`)
	send("\r")
	waitScreen("[approved]", 0)
	send("\x1b")
	startWork()
	wait(acknowledgement)
	before := watches.Load()
	disconnect()
	deadline := time.Now().Add(15 * time.Second)
	for watches.Load() <= before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if watches.Load() <= before {
		t.Fatal("native PTY did not reconnect while source held")
	}
	release()
	finalBody := strings.TrimSuffix(strings.Split(final, " Source")[0], " Sources:")
	wait(finalBody)
	assert := func(text string) {
		t.Helper()
		ownerLines := 0
		for _, line := range strings.Split(text, "\n") {
			if strings.Trim(strings.TrimSpace(line), "│ ") == refinement {
				ownerLines++
			}
		}
		if ownerLines != 1 {
			t.Fatalf("native PTY expected one exact accepted owner row: %s", text)
		}
		for _, want := range []string{acknowledgement, finalBody, "https://fixture.test/market"} {
			if strings.Count(text, want) != 1 {
				t.Fatalf("native PTY want once %q: %s", want, text)
			}
		}
		if strings.Index(text, refinement) >= strings.Index(text, acknowledgement) || strings.Index(text, acknowledgement) >= strings.Index(text, finalBody) {
			t.Fatal("native PTY chronology reordered")
		}
		for _, forbidden := range []string{"Review candidate", "Approve work", "Open in Files panel"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("needless workflow: %s", text)
			}
		}
	}
	assert(waitScreen(finalBody, 0))
	committedHistory := history()
	mu.Lock()
	restoreOffset := output.Len()
	mu.Unlock()
	send("/bots home\r")
	assert(waitScreen(finalBody, restoreOffset))
	if restored := history(); !reflect.DeepEqual(committedHistory, restored) {
		t.Fatal("native reload changed ordered durable SSE IDs or bodies")
	}
	send("/bots detach\r")
	send("/exit\r")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("native PTY did not exit")
	}
	t.Logf("native PTY %s live/reconnect/restore actual shared-worker reviewer=0 artifact=0", scope)
}

func employeePTYScreen(raw []byte) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-c", `import base64,re,sys,unicodedata
sys.path.insert(0, '../../tests/e2eboot')
from agent_terminal_contract import screen
raw=sys.stdin.buffer.read()
# Existing decoder admits only single-cell glyphs. The native queued badge
# uses a two-cell hourglass; map only that non-content glyph to two cells.
# Preserve all control sequences and every conversation byte unchanged.
raw=raw.replace('⏳'.encode(),b'..')
try:
 print(screen({'encoding':'base64','data':base64.b64encode(raw).decode(),'rows':60,'cols':180}))
except RuntimeError:
 print('Observed controls:', sorted(set(re.findall(rb'\x1b(?:\[[^A-Za-z]*[A-Za-z]|[^[])',raw)),key=repr),file=sys.stderr)
 print('Unsupported glyphs:', [(repr(c),hex(ord(c)),unicodedata.name(c,'unknown')) for c in set(raw.decode('utf-8','replace')) if (not c.isprintable() and c not in '\x1b\r\n\t\b\a') or unicodedata.combining(c) or unicodedata.east_asian_width(c) in ('W','F')],file=sys.stderr)
 raise`)
	cmd.Stdin = strings.NewReader(string(raw))
	text, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("terminal decoder: %w: %s", err, text)
	}
	return string(text), nil
}

func TestBotEmployeePTYScreenQueuedBadgeKeepsColumns(t *testing.T) {
	text, err := employeePTYScreen([]byte("\x1b[2J\x1b[H⏳ Queued\x1b[2;1HU.S. markets and major indexes"))
	if err != nil || !strings.Contains(text, ".. Queued") || strings.Count(text, "U.S. markets and major indexes") != 1 {
		t.Fatalf("queued badge changed conversation columns: %q, %v", text, err)
	}
}

func TestBotEmployeePTYScreenRetainsBodyAcrossDelta(t *testing.T) {
	text, err := employeePTYScreen([]byte("\x1b[2J\x1b[HApple Inc. answer\x1b[2;1HRunning\x1b[2;1H\x1b[2KCompleted"))
	if err != nil || strings.Count(text, "Apple Inc. answer") != 1 || !strings.Contains(text, "Completed") || strings.Contains(text, "Running") {
		t.Fatalf("current terminal screen did not retain unchanged body: %q, %v", text, err)
	}
}

func TestBotDetailsWithoutSkillsBrowserAcceptance(t *testing.T) { runBotAgentWorkBrowser(t) }

func TestBotAgentWorkBrowserAcceptance(t *testing.T) { runBotAgentWorkBrowser(t) }

func runBotAgentWorkBrowser(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit browser opt-in required")
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		var ok bool
		bin, ok = browserlauncher.LookPath()
		if !ok {
			t.Fatal("native browser unavailable")
		}
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
	dist, _ := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	id, home, runID, owner := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	b := store.Bot{ID: id, HomeSessionID: home, OwnerID: owner, Scope: store.BotScopePersonal, Status: store.BotActive, Name: "Agent surface", Version: 1, Revision: 1}
	var mode atomic.Int32
	var writes atomic.Int32
	var skillFetches atomic.Int32
	var lifecycle atomic.Int32
	var teamScope, readOnly atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "read only fixture", 405)
			return
		}
		if strings.Contains(r.URL.Path, "/skills") && r.URL.Query().Get("limit") != "100" {
			skillFetches.Add(1)
		}
		visibleBot := b
		visibleBot.Status = []store.BotStatus{store.BotActive, store.BotActive, store.BotPaused, store.BotStopped, store.BotDeleted}[lifecycle.Load()]
		if teamScope.Load() {
			visibleBot.Scope = store.BotScopeTeam
		}
		if readOnly.Load() {
			visibleBot.OwnerID = runID
		}
		var value any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			value = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			value = map[string]any{"user": map[string]any{"id": owner.String(), "email": "agent@example.test", "display_name": "Owner", "role": "owner", "platform_role": "user"}, "org": map[string]any{"slug": "test"}, "team": "test"}
		case "/api/orgs":
			value = map[string]any{"orgs": []any{}, "active_org": "test"}
		case "/api/teams":
			value = []any{map[string]any{"slug": "test", "name": "Test"}}
		case "/api/settings/status":
			value = map[string]any{"setupRequired": false}
		case "/api/version":
			value = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			value = []any{}
		case "/api/studio/bots":
			value = map[string]any{"bots": []store.Bot{visibleBot}}
		default:
			if strings.Contains(r.URL.Path, "/versions/") {
				value = store.DefaultBotVersion()
				break
			}
			if strings.HasSuffix(r.URL.Path, "/events") {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: replay_complete\nid: %s:1\ndata: {}\n\n", home)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			if strings.HasSuffix(r.URL.Path, "/snapshot") {
				if mode.Load() == 4 {
					http.Error(w, "unavailable", 503)
					return
				}
				work := &store.BotWorkProjection{SchemaVersion: 1, Protocol: store.BotWorkProtocolAgentLedV1, BotID: id, RunID: runID, WorkRevision: 1, Phase: store.BotWorkFinished, Disposition: store.AgentWorkAnswered}
				if mode.Load() == 1 {
					work.Disposition = store.AgentWorkPartial
					work.Remaining = []store.BotWorkObligation{{ID: uuid.New(), SourceRunID: runID, Summary: "Compare missing source", Reason: "Source unavailable", Continuation: store.BotWorkContinueOwnerRequest}}
					work.Checks = []store.BotWorkCheckReceipt{{ID: uuid.New(), Kind: store.BotWorkCheckModelFeedback, Status: store.BotWorkCheckReturned, Summary: "Narrow the conclusion"}}
				}
				if mode.Load() == 2 {
					work.SchemaVersion = 99
					work.Items = []store.BotWorkItem{{Summary: "UNSUPPORTED PRIVATE CONTENT"}}
				}
				if mode.Load() == 3 {
					work = nil
				}
				value = store.BotSnapshot{Bot: visibleBot, Operations: []store.BotOperationView{}, PendingProposals: []store.BotControlProposal{}, PendingApprovals: []store.BotApproval{}, ReadCursor: store.BotReadCursor{ViewerID: owner}, LatestOutcome: &store.BotRun{ID: runID, BotID: id, Status: store.RunSucceeded, Work: work}, RunStates: []store.BotRunState{{SchemaVersion: 1, BotID: id, RunID: runID, SessionID: uuid.New(), HomeSessionID: home, Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunSucceeded, StateSequence: int64(mode.Load() + 2), Work: work}}}
			} else if strings.HasPrefix(r.URL.Path, "/api/") {
				switch {
				case strings.HasSuffix(r.URL.Path, "/runs"), strings.HasSuffix(r.URL.Path, "/goals"), strings.HasSuffix(r.URL.Path, "/approvals"), strings.HasSuffix(r.URL.Path, "/proposals"), strings.HasSuffix(r.URL.Path, "/activity"), strings.HasSuffix(r.URL.Path, "/skills"), strings.HasSuffix(r.URL.Path, "/sessions"):
					value = map[string]any{"runs": []any{}, "goals": []any{}, "approvals": []any{}, "proposals": []any{}, "activity": []any{}, "skills": []any{}, "sessions": []any{}}
				default:
					http.NotFound(w, r)
					return
				}
			}
		}
		if value != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(value)
			return
		}
		path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if info, e := os.Stat(path); e == nil && !info.IsDir() {
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	}))
	defer server.Close()
	launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage().Timeout(30 * time.Second)
	page.MustEvalOnNewDocument(`window.__agentErrors=[]; window.addEventListener('error', e=>window.__agentErrors.push(e.error?.stack || e.message)); window.addEventListener('unhandledrejection', e=>window.__agentErrors.push(String(e.reason)))`)
	page.MustNavigate(server.URL + "/#/chat/bots/personal/" + id.String())
	page = page.WithPanic(func(v any) {
		text, _ := page.CancelTimeout().Eval(`()=>JSON.stringify({body:document.body.innerText, errors:window.__agentErrors, url:location.href})`)
		t.Fatalf("agent browser: %v\n%v", v, text)
	})
	assertUnclutteredChat := func() {
		t.Helper()
		if !page.MustEval(`() => !document.querySelector('[aria-label="Bot skills"]') && !Array.from(document.querySelectorAll('[role="dialog"] summary')).some(n => /Skills & history|File browser|Tasks/.test(n.textContent))`).Bool() {
			t.Fatal("removed Details navigation mounted")
		}
		if !page.MustEval(`() => { const workspace = document.querySelector('[aria-label="Bot workspace"]'); return workspace && !workspace.querySelector('[aria-label^="Work for run"]') && !workspace.innerText.includes('Bot transcript:') && !workspace.querySelector('[data-testid="message-area"]').innerText.match(/Work in progress|Answer provided|Partial answer|Work details unavailable/); }`).Bool() {
			t.Fatal("standalone work or transport diagnostics appeared in chat")
		}
	}
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Keep this draft")
	assertUnclutteredChat()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Runs & activity$").MustClick()
	page.MustElementR("summary", "^Answer provided$").MustVisible()
	t.Log("AGENT_SURFACE browser/answer PASS")
	for _, tc := range []struct {
		mode int32
		want string
	}{{1, "Partial answer"}, {2, "Work details unavailable"}, {3, "No work result yet"}} {
		mode.Store(tc.mode)
		page.MustElementR("button", "^Refresh Bot$").MustClick()
		page.MustWait(`want => document.querySelector('[role="dialog"]').innerText.includes(want)`, tc.want)
		assertUnclutteredChat()
		if tc.mode == 1 {
			page.MustElementR(`[role="dialog"] summary`, "^Partial answer$").MustClick()
			page.MustElementR("p", "Feedback received: Narrow the conclusion").MustVisible()
			page.MustElementR("p", "no automatic continuation").MustVisible()
		}
		if tc.mode == 2 && strings.Contains(page.MustElement("body").MustText(), "UNSUPPORTED PRIVATE CONTENT") {
			t.Fatal("unknown work schema interpreted")
		}
	}
	t.Log("AGENT_SURFACE browser/work-states PASS")
	mode.Store(4)
	page.MustElementR("button", "^Refresh Bot$").MustClick()
	page.MustElement(`[aria-label="Bot workspace"] [role="alert"]`).MustVisible()
	mode.Store(0)
	page.MustElementR("button", "^Refresh Bot$").MustClick()
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	if page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustProperty("value").Str() != "Keep this draft" {
		t.Fatal("composer lost")
	}
	page.MustReload()
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustVisible()
	assertUnclutteredChat()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Runs & activity$").MustClick()
	page.MustElementR("summary", "^Answer provided$").MustVisible()
	if writes.Load() != 0 {
		t.Fatalf("inspection wrote %d requests", writes.Load())
	}
	assertUnclutteredChat()
	if skillFetches.Load() != 0 {
		t.Fatalf("removed Skills fetched %d times", skillFetches.Load())
	}
	for _, scope := range []string{"personal", "team"} {
		teamScope.Store(scope == "team")
		for status := int32(0); status < 5; status++ {
			lifecycle.Store(status)
			for _, viewerReadOnly := range []bool{false, true} {
				readOnly.Store(viewerReadOnly)
				page.MustNavigate(server.URL + "/#/chat/bots/" + scope + "/" + id.String())
				page.MustReload()
				page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
				page.MustElementR("button", "^Details$").MustClick()
				assertUnclutteredChat()
				for _, name := range []string{"Goals", "Memory & charter", "Schedule & triggers", "Runs & activity", "Review inbox"} {
					page.MustElementR(`[role="dialog"] summary`, "^"+name+"$").MustVisible()
				}
				page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
				assertUnclutteredChat()
			}
		}
	}
	if skillFetches.Load() != 0 || writes.Load() != 0 {
		t.Fatal("Details inspection fetched removed inspector or mutated state")
	}
	t.Log("SKILL_SURFACE browser/no-skills PASS")
	t.Log("SKILL_SURFACE browser/no-skill-fetch PASS")
	t.Log("SKILL_SURFACE browser/home-readonly PASS")
	t.Log("AGENT_SURFACE browser/restore-composer PASS")
}

// databaseRuntimeFileSnapshot observes only the test-owned internal repository.
// Workspace standing documents remain permitted file IO. Runtime evidence and
// checkpoint objects must not appear or change during shared Chat execution.
func databaseRuntimeFileSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	objects := map[string]string{}
	err := filepath.WalkDir(filepath.Join(dir, "bot-content"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasPrefix(entry.Name(), "evidence-") && !strings.HasPrefix(entry.Name(), "checkpoint-") {
			return nil
		}
		//nolint:gosec // G122: test-only walk over a t.TempDir() tree; no untrusted symlinks.
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Knowledge-extraction inputs belong to memory's unchanged file lifecycle,
		// not conversation/session/activity/approval/run runtime representation.
		var extraction struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &extraction) == nil && len(extraction.Input) > 0 {
			return nil
		}
		objects[path] = fmt.Sprintf("%x", sha256.Sum256(raw))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

// Inspect only this disposable tenant's SQLite representation using read-only SQL.
// This is test evidence, never an authorization or production persistence path.
func assertDatabaseRuntimeComplete(t *testing.T, f *interactiveHTTPFixture) {
	t.Helper()
	path := filepath.Join(f.dataDir, "orgs", "interactive", "personal", f.b.OwnerID.String()+".db")
	if f.scope == store.BotScopeTeam {
		path = filepath.Join(f.dataDir, "orgs", "interactive", "teams", "team.db")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, family := range []struct{ name, query string }{
		{"session_event", `SELECT event_data FROM session_events WHERE session_id IN (SELECT id FROM sessions WHERE bot_id = ?)`},
		{"run", `SELECT data FROM bot_runs WHERE bot_id = ?`},
		{"approval", `SELECT data FROM bot_approvals WHERE bot_id = ?`},
		{"activity", `SELECT payload FROM bot_outbox WHERE bot_id = ?`},
	} {
		rows, err := db.QueryContext(f.ctx, family.query, f.b.ID.String())
		if err != nil {
			t.Fatal(family.name, err)
		}
		count := 0
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]any
			if err = json.Unmarshal([]byte(raw), &envelope); err != nil {
				t.Fatal(err)
			}
			if record, ok := envelope["bot_record_v2"].(string); ok {
				var header struct {
					Inline    []byte          `json:"inline"`
					SizeBytes int64           `json:"size_bytes"`
					SHA256    string          `json:"sha256"`
					Content   json.RawMessage `json:"content"`
				}
				if err := json.Unmarshal([]byte(record), &header); err != nil {
					t.Fatalf("%s invalid runtime envelope: %v", family.name, err)
				}
				// Current unreleased runtime records are inline database bytes;
				// no obsolete development format marker is part of the contract.
				if len(header.Inline) == 0 || header.SizeBytes != int64(len(header.Inline)) || header.Content != nil || header.SHA256 != fmt.Sprintf("%x", sha256.Sum256(header.Inline)) || !json.Valid(header.Inline) {
					t.Fatalf("%s non-database or corrupt runtime envelope", family.name)
				}
				count++
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if count == 0 {
			t.Fatalf("%s had no proven inline database records", family.name)
		}
	}
}

type employeeReadCloser struct{ *strings.Reader }

func (employeeReadCloser) Close() error         { return nil }
func ioNopEmployee(s string) employeeReadCloser { return employeeReadCloser{strings.NewReader(s)} }
