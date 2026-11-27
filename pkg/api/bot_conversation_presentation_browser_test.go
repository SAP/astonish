package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/scheduler"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Controlled immutable receipt and disk object; production HTTP authorization,
// schema validation, preview redaction and download tickets remain exercised.
type mixedPresentationRepo struct {
	browserPublicationRepo
	receipt store.BotPublication
}

func (r mixedPresentationRepo) ReadBotPublication(ctx context.Context, b store.BotID, run store.RunID, seq int64) (*store.BotPublication, error) {
	if run != r.receipt.RunID {
		return r.BotStore.(store.BotPublicationReader).ReadBotPublication(ctx, b, run, seq)
	}
	if _, err := r.ReadFilePublication(ctx, b, run, seq); err != nil {
		return nil, err
	}
	p := r.receipt
	return &p, nil
}

type mixedPresentationFiles struct {
	*browserWorkspaceFiles
	ref store.BotContentRef
}

func (f *mixedPresentationFiles) OpenContent(ctx context.Context, ref store.BotContentRef, br store.BotFileRange) (*store.BotFileStream, error) {
	if ref != f.ref {
		return nil, store.ErrBotForbidden
	}
	stream, err := f.Open(ctx, ref.Scope.BotID, "mixed-reviewed.md", br)
	if err == nil {
		stream.Entry.SHA256 = ref.SHA256
		stream.Entry.Freshness = ref.SHA256
	}
	return stream, err
}

// One resolver distinguishes the real Employee and private Chat by their sealed
// executable views, then verifies the committed result before authoring final text.
type employeePresentationBrowserModel struct {
	t                                   *testing.T
	employeeStarted, captured, verified bool
	finalRead                           func()
	assessment                          func(*model.LLMRequest) (*model.LLMResponse, bool)
}

func (*employeePresentationBrowserModel) Name() string { return "interactive-http" }
func (m *employeePresentationBrowserModel) GenerateContent(_ context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var content *genai.Content
		prompt := ""
		if q.Config != nil && q.Config.SystemInstruction != nil {
			for _, p := range q.Config.SystemInstruction.Parts {
				prompt += p.Text
			}
		}
		if strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
			m.t.Fatal("validated private schedule capture must complete mechanically without a pending-review model call")
			return
		}
		if m.assessment != nil {
			if response, ok := m.assessment(q); ok {
				yield(response, nil)
				return
			}
		}
		if strings.Contains(prompt, "BOT_EMPLOYEE_PRESENTATION_V1") {
			if len(q.Tools) != 0 || len(q.Config.Tools) != 0 {
				m.t.Fatal("final Employee has executable tools")
			}
			var facts struct {
				Digest     string               `json:"source_digest"`
				Operations []store.BotOperation `json:"operations"`
			}
			for _, c := range q.Contents {
				for _, p := range c.Parts {
					_ = json.Unmarshal([]byte(p.Text), &facts)
				}
			}
			if len(facts.Operations) != 1 || facts.Operations[0].Status != store.BotOperationApplied || facts.Operations[0].Receipt == nil {
				m.t.Fatalf("final lacks committed schedule receipt: %+v", facts)
			}
			m.finalRead()
			raw, _ := json.Marshal(map[string]string{"source_digest": facts.Digest, "text": "Your daily report is scheduled at 9am UTC."})
			content = genai.NewContentFromText(string(raw), "model")
		} else if _, ok := q.Tools["employee_chat"]; ok {
			if !m.employeeStarted {
				m.employeeStarted = true
				content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "employee-private", Name: "employee_chat", Args: map[string]any{"message": "Schedule the daily report at 9am UTC using the owner's exact request"}}}}}
			} else {
				content = genai.NewContentFromText("PRIVATE_PENDING pending; application not verified.", "model")
			}
		} else if !m.captured {
			m.captured = true
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "real-schedule", Name: "execute_tool", Args: map[string]any{"name": "schedule_job", "arguments": map[string]any{"name": "Daily report", "schedule": "0 9 * * *", "timezone": "UTC", "mode": "adaptive", "instructions": "Read the daily report", "invocation": "Deliver the daily report"}}}}}}
		} else {
			found := false
			for _, c := range q.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil && p.FunctionResponse.Name == "execute_tool" {
						raw, _ := json.Marshal(p.FunctionResponse.Response)
						found = strings.Contains(string(raw), "operation_id")
					}
				}
			}
			if !found {
				raw, _ := json.Marshal(q.Contents)
				m.t.Fatalf("private worker lacks real schedule capture response: tools=%v contents=%s", q.Tools, raw)
			}
			content = genai.NewContentFromText("PRIVATE_WORKER pending; application not verified.", "model")
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}

// assertBotSidebarChrome measures the built UI in Chrome and uses physical clicks
// for collapse/expand; API reads remain production scoped handlers.
func assertBotSidebarChrome(t *testing.T, page *rod.Page, scope store.BotScope, id string) {
	t.Helper()
	page.MustElement(`section[aria-label="Bots"] a[aria-current="page"]`).MustVisible()
	page.MustElement(`[title="Delete conversation"]`).MustVisible()
	page.MustWait(`() => !!document.querySelector('section[aria-label="Bots"] button[aria-label^="Delete "]')`)
	checkExpanded := func() {
		t.Helper()
		result := page.MustEval(`async (scope, id) => {
			const errors = [], check = (ok, message) => { if (!ok) errors.push(message) }
			const bots = document.querySelector('section[aria-label="Bots"]'), sidebar = bots.parentElement
			const botHeading = bots.querySelector('h2'), chatHeading = [...sidebar.querySelectorAll('span')].find(e => e.textContent === 'Chats')
			check(Math.abs(botHeading.getBoundingClientRect().x - chatHeading.getBoundingClientRect().x) < 0.5, 'Bots/Chats heading x alignment')
			check(getComputedStyle(botHeading).fontSize === getComputedStyle(chatHeading).fontSize && getComputedStyle(botHeading).fontWeight === getComputedStyle(chatHeading).fontWeight, 'heading typography parity')
			const hide = sidebar.querySelector('[aria-label="Hide sidebar"]')
			check(hide.closest('section') === bots && hide.parentElement.parentElement === botHeading.parentElement, 'Hide must belong to top Bots heading')
			for (const label of ['New Bot', 'Hide sidebar', 'New conversation', 'Start fleet session']) {
				const button = sidebar.querySelector('[aria-label="' + label + '"]')
				if (!button || !button.querySelector('svg')) { check(false, label + ' icon action missing; rebuild Studio dist'); continue }
				const box = button.getBoundingClientRect(), icon = button.querySelector('svg').getBoundingClientRect()
				check(box.width === 28 && box.height === 28 && icon.width === 16 && icon.height === 16, label + ' action/icon dimensions')
				check(getComputedStyle(button).backgroundColor === 'rgba(0, 0, 0, 0)', label + ' ghost presentation')
			}
			const row = bots.querySelector('a[aria-current="page"]'), metadata = row.querySelector('svg').parentElement
			const chatMetadata = sidebar.querySelector('[title="Delete conversation"]').parentElement.querySelector('svg.lucide-clock').parentElement
			for (const key of ['fontSize', 'color', 'gap', 'marginTop']) check(getComputedStyle(metadata)[key] === getComputedStyle(chatMetadata)[key], 'metadata parity: ' + key)
			const botTrash = bots.querySelector('button[aria-label^="Delete "] svg'), chatTrash = sidebar.querySelector('[title="Delete conversation"] svg')
			check(botTrash.getBoundingClientRect().width === 12 && chatTrash.getBoundingClientRect().width === 12, 'trash icons 12px parity')
			const response = await fetch('/api/studio/bots?scope=' + scope), data = await response.json(), bot = data.bots.find(b => b.id === id)
			check(response.ok && !!bot, 'production scoped Bot list')
			check(bot.message_count !== undefined && metadata.textContent.includes(bot.message_count + ' msg' + (bot.message_count === 1 ? '' : 's')), 'actual API message count rendered')
			check(!!metadata.querySelector('svg.lucide-clock') && !!bot.home_updated_at && metadata.querySelectorAll('span')[0].textContent.length > 0, 'durable Home timestamp rendered')
			check(row.getAttribute('href') === '#/chat/bots/' + scope + '/' + id, 'active scoped Bot route')
			return errors
		}`, string(scope), id)
		if result.String() != "[]" {
			t.Fatalf("expanded sidebar Chrome assertions: %s", result.String())
		}
	}
	checkExpanded()
	artifactDir, err := filepath.Abs("../../.tmp/sidebar-browser")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifactDir, 0700); err != nil {
		t.Fatal(err)
	}
	page.MustScreenshot(filepath.Join(artifactDir, string(scope)+"-expanded.png"))
	route := page.MustEval(`() => location.hash`).String()
	page.MustElement(`[aria-label="Hide sidebar"]`).MustClick()
	page.MustElement(`[aria-label="Show sidebar"]`).MustVisible()
	if !page.MustEval(`() => {
		const rail = document.querySelector('[aria-label="Show sidebar"]').parentElement
		return rail.querySelectorAll('button').length === 1 && rail.querySelectorAll('svg').length === 1 && rail.querySelectorAll('a,input,section').length === 0 && !document.querySelector('section[aria-label="Bots"]') && rail.textContent.trim() === ''
	}`).Bool() {
		t.Fatal("collapsed sidebar must contain only the expand control, no Bot/Chat actions or links")
	}
	page.MustScreenshot(filepath.Join(artifactDir, string(scope)+"-collapsed.png"))
	page.MustElement(`[aria-label="Show sidebar"]`).MustClick()
	page.MustElement(`section[aria-label="Bots"] a[aria-current="page"]`).MustVisible()
	page.MustWait(`() => !!document.querySelector('section[aria-label="Bots"] button[aria-label^="Delete "]')`)
	checkExpanded()
	if page.MustEval(`() => location.hash`).String() != route {
		t.Fatal("sidebar toggle changed active route")
	}
	t.Logf("Chrome sidebar parity: aligned headings, ghost 28px actions/16px icons, 12px trash, API metadata, expand-only rail and unchanged route (%s); screenshots %s", scope, artifactDir)
}

// Physical presentation proof: built Studio, isolated Chrome profile, real scoped
// SQLite and production transcript/history HTTP handlers. Controlled public events
// are persisted, not intercepted in browser fetch or injected into React state.
func TestEmployeeOnlyConversationBrowserAcceptance(t *testing.T) {
	runEmployeeConversationBrowserAcceptance(t, false)
}

// Sidebar-only proof reuses the real scoped HTTP/SPA fixture without submitting
// a message, polling the worker, or performing any model work.
func TestBotSidebarChromeAcceptance(t *testing.T) {
	runEmployeeConversationBrowserAcceptance(t, true)
}

func runEmployeeConversationBrowserAcceptance(t *testing.T, sidebarOnly bool) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building Studio")
	}
	bin := os.Getenv("ASTONISH_MEMORY_BROWSER_PATH")
	if bin == "" {
		bin = os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	}
	if bin == "" {
		t.Fatal("explicit browser executable required")
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
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			files := &mixedPresentationFiles{browserWorkspaceFiles: &browserWorkspaceFiles{root: t.TempDir()}}
			reviewed := []byte("# Reviewed mixed deliverable\nPublic mixed receipt verified.\n")
			if err := os.WriteFile(filepath.Join(files.root, "mixed-reviewed.md"), reviewed, 0600); err != nil {
				t.Fatal(err)
			}
			runID := uuid.New()
			publication := store.BotFilePublication{SchemaVersion: 1, BotID: f.b.ID, RunID: runID, HomeSessionID: f.b.HomeSessionID, HomeID: f.b.Home.ID, ReportSequence: 1, EventID: store.BotPublicationEventID(runID, 1), Summary: "Reviewed mixed delivery.", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: f.b.Home.ID, RelativePath: "mixed-reviewed.md", SHA256: fmt.Sprintf("%x", sha256.Sum256(reviewed)), SizeBytes: int64(len(reviewed)), MediaType: "text/markdown", Preview: store.BotFilePreviewText}}}
			files.ref = store.BotContentRef{Format: 1, Scope: store.BotContentScope{OrgID: "interactive", TeamID: "team", OwnerID: f.b.OwnerID.String(), BotID: f.b.ID}, Kind: store.BotContentArtifact, Key: "mixed-object", SHA256: publication.Artifacts[0].SHA256, SizeBytes: int64(len(reviewed)), Revision: 1}
			receipt := store.BotPublication{BotID: f.b.ID, RunID: runID, HomeSessionID: f.b.HomeSessionID, ReportSequence: 1, EventID: publication.EventID, EmployeeSpeech: true, Text: publication.Summary, Artifacts: []store.BotArtifact{{ID: publication.Artifacts[0].ID, HomeID: f.b.Home.ID, RelativePath: "mixed-reviewed.md", SHA256: files.ref.SHA256, SizeBytes: files.ref.SizeBytes, ContentRef: &files.ref}}}
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			personal := org.ForUser(f.b.OwnerID.String())
			sessions := personal.Sessions()
			if scope == store.BotScopeTeam {
				sessions = org.ForTeam("team").Sessions()
			}
			ordinaryID := uuid.New()
			ordinary, err := personal.Sessions().Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.b.OwnerID.String(), SessionID: ordinaryID.String()})
			if err != nil {
				t.Fatal(err)
			}
			// Ordinary Chat retains its established text/tool renderer; these rows
			// are never used to mint Employee speech in Home.
			for _, row := range clientStateRows(t, ordinaryID, uuid.New()) {
				if err := personal.Sessions().AppendEvent(f.ctx, ordinary.Session, row); err != nil {
					t.Fatal(err)
				}
			}
			gate := &clientStateGate{closed: make(chan struct{}, 1)}
			var unavailable atomic.Bool
			f.worker.Stop()
			principal, _ := execution.PrincipalFromContext(f.ctx)
			principal.Authentication = execution.AuthMethodPlatformJWT
			f.ctx, err = execution.WithPrincipal(f.ctx, principal)
			if err != nil {
				t.Fatal(err)
			}
			f.ctx, err = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"schedule_job"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
			}
			mdl := &employeePresentationBrowserModel{t: t, assessment: f.model.assessment}
			workerConfig := f.workerConfig
			workerConfig.MandatePolicy = policy
			workerConfig.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mdl, nil
			}
			workerConfig.Prepare = func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				retained := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.pool.dir}, repo: f.repo, id: f.b.ID, scope: scope}
				retained.install(c, q, s)
				return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, PolicyRuntime: agent.BotPolicyRuntime{Mandate: q.Mandate, Guard: restartMandateGuard{q, f.repo}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
			}
			f.worker, err = daemon.NewBotWorker(workerConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer f.worker.Stop()
			mts := daemon.NewMultiTenantScheduler(f.db, &scheduler.Executor{}, nil, nil)
			mts.SetBotSchedulerCallbacks(f.worker.SchedulerCallbacks())
			mdl.finalRead = func() {
				jobs := personal.ScheduledJobs()
				if scope == store.BotScopeTeam {
					jobs = org.ForTeam("team").ScheduledJobs()
				}
				rows := jobs.List(f.ctx)
				if len(rows) != 1 || !rows[0].Enabled || rows[0].Schedule.Cron != "0 9 * * *" || rows[0].Schedule.Timezone != "UTC" || rows[0].NextRun == nil {
					t.Fatalf("final Employee ran before independent enabled scheduler read: %+v", rows)
				}
				mdl.verified = true
			}
			target, _ := url.Parse(f.server.URL)
			proxy := httputil.NewSingleHostReverseProxy(target)
			routes := mux.NewRouter()
			root := "/api/studio/bots"
			for _, r := range []struct {
				path, method string
				handler      http.HandlerFunc
			}{
				{root, "GET", api.BotListHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler},
				{root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler},
				{root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler},
				{root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler},
				{"/api/studio/sessions/{id}", "GET", api.StudioSessionHandler}, {"/api/studio/sessions", "GET", api.StudioSessionsHandler},
			} {
				routes.HandleFunc(r.path, r.handler).Methods(r.method)
			}
			routes.NotFoundHandler = proxy
			// Admission uses the same authenticated production handler as Studio.
			routes.HandleFunc(root+"/{botID}/messages", func(w http.ResponseWriter, r *http.Request) {
				actual := store.Services{TenantRouter: f.db, Bots: org.ForTeam("team").Bots(), PersonalBots: personal.Bots(), Sessions: org.ForTeam("team").Sessions(), PersonalSessions: personal.Sessions(), BotInteractiveReady: !unavailable.Load(), BotTurnRegistry: f.registry, BotMandatePolicy: policy}
				actual.Bots = org.ForTeam("team").Bots()
				actual.PersonalBots = personal.Bots()
				api.BotMessageHandler(w, r.WithContext(store.WithServices(r.Context(), &actual)))
			}).Methods("POST")
			svc := &store.Services{TenantRouter: f.db, Bots: org.ForTeam("team").Bots(), Sessions: org.ForTeam("team").Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: f.registry, BotMandatePolicy: policy}
			if scope == store.BotScopeTeam {
				svc.Bots = mixedPresentationRepo{browserPublicationRepo{svc.Bots, publication}, receipt}
			} else {
				svc.PersonalBots = mixedPresentationRepo{browserPublicationRepo{svc.PersonalBots, publication}, receipt}
			}
			svc.Credentials = org.ForTeam("team").Credentials()
			svc.PersonalCredentials = personal.Credentials()
			artifactRoot := root + "/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}"
			routes.HandleFunc(artifactRoot+"/preview", api.BotFilePreviewHandler).Methods("GET")
			routes.HandleFunc(artifactRoot+"/download-ticket", api.BotFileDownloadTicketHandler).Methods("POST")
			routes.HandleFunc(artifactRoot+"/download", api.BotFileDownloadHandler).Methods("GET", "HEAD")
			svc.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }
			routes.HandleFunc(root+"/{botID}/sessions/{sessionID}/events", func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				gate.mu.Lock()
				gate.cancel = cancel
				gate.cursors = append(gate.cursors, r.Header.Get("Last-Event-ID")+r.URL.Query().Get("after"))
				gate.mu.Unlock()
				if gate.fail.Load() {
					http.Error(w, "fixture transport unavailable", http.StatusServiceUnavailable)
					return
				}
				defer func() {
					select {
					case gate.closed <- struct{}{}:
					default:
					}
				}()
				api.BotEventsHandler(w, r.WithContext(ctx))
			}).Methods("GET")
			routes.HandleFunc("/api/studio/sessions/{id}/status", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"running": false})
			}).Methods("GET")
			routes.HandleFunc(root+"/{botID}/files/preview", api.BotFilePreviewHandler).Methods("GET")
			routes.HandleFunc(root+"/{botID}/files/download-ticket", api.BotFileDownloadTicketHandler).Methods("POST")
			routes.HandleFunc(root+"/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET", "HEAD")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, root) || strings.HasPrefix(r.URL.Path, "/api/studio/sessions") {
					ctx, cancel := context.WithCancel(f.ctx)
					stop := context.AfterFunc(r.Context(), cancel)
					defer func() { stop(); cancel() }()
					routes.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
					return
				}
				var value any
				switch r.URL.Path {
				case "/api/auth/setup-status":
					value = map[string]any{"initialized": true, "auth_mode": "builtin"}
				case "/api/auth/me":
					value = map[string]any{"user": map[string]any{"id": f.b.OwnerID.String(), "email": "mixed@example.invalid", "display_name": "Mixed acceptance", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": "interactive", "name": "Interactive"}, "team": "team"}
				case "/api/orgs":
					value = map[string]any{"orgs": []any{}, "active_org": "interactive"}
				case "/api/teams":
					value = []any{map[string]any{"slug": "team", "name": "Team"}}
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
				path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
				if st, e := os.Stat(path); e == nil && !st.IsDir() {
					http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
					return
				}
				http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			}))
			defer func() { server.CloseClientConnections(); server.Close() }()
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			defer launch.Kill()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			downloads := t.TempDir()
			if err := (proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorAllow, DownloadPath: downloads}).Call(browser); err != nil {
				t.Fatal(err)
			}
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(30 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
				t.Fatalf("browser failure: %v\n%v", v, text)
			})
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			if sidebarOnly {
				assertBotSidebarChrome(t, page, scope, f.b.ID.String())
				rows, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
				if err != nil || len(rows) != 0 || mdl.employeeStarted || mdl.captured {
					t.Fatalf("sidebar navigation admitted model work: runs=%+v err=%v model=%+v", rows, err, mdl)
				}
				current, err := f.repo.Get(f.ctx, f.b.ID)
				if err != nil || current.Status != f.b.Status || current.Version != f.b.Version {
					t.Fatalf("sidebar toggle mutated Bot: before=%+v after=%+v err=%v", f.b, current, err)
				}
				return
			}
			input := page.MustElement(`[data-testid="chat-input"]`)
			input.MustInput("Schedule the daily report at 9am UTC")
			page.MustElement(`[aria-label="Send message"]`).MustClick()
			var actualRun *store.BotRun
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
				rows, e := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				if len(rows) == 1 {
					actualRun = &rows[0]
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if actualRun == nil {
				t.Fatal("browser did not admit owner message")
			}
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			actualRun, err = f.repo.GetRun(f.ctx, f.b.ID, actualRun.ID)
			if err != nil || !mdl.verified || !mdl.captured || !mdl.employeeStarted {
				t.Fatalf("actual scheduling incomplete: %+v err=%v model=%+v", actualRun, err, mdl)
			}
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			pub, err := f.repo.(store.BotPublicationStore).PublishReport(f.ctx, store.BotPublicationRequest{BotID: f.b.ID, RunID: actualRun.ID})
			if err != nil || pub.Text != "Your daily report is scheduled at 9am UTC." {
				t.Fatalf("persisted Employee publication: %+v %v", pub, err)
			}
			input.MustInput("Retain mixed draft during reconnect")
			page.MustElementR("p", "Your daily report is scheduled at 9am UTC.").MustVisible()
			gate.fail.Store(true)
			gate.disconnect(t)
			page.MustWait(`() => [...document.querySelectorAll('[role="alert"]')].some(e => e.textContent.includes('Updates unavailable')) || document.body.innerText.includes('I can’t confirm the latest outcome right now.')`)
			if page.MustEval(`() => document.body.innerText.includes('I can’t confirm the latest outcome right now.')`).Bool() {
				t.Fatal("built Studio promotes reconnect failure to canned Agent speech; current source requires neutral alert (rebuild dist in parent gate)")
			}
			page.MustElementR(`[role="alert"]`, "Updates unavailable").MustVisible()
			if page.MustEval(`() => [...document.querySelectorAll('[data-message-index]')].some(e => e.textContent.includes('Updates unavailable'))`).Bool() {
				t.Fatal("reconnect error impersonates Employee speech")
			}
			gate.fail.Store(false)
			for i, kind := range []string{"text", "tool_call", "tool_result", "error", "turn_complete"} {
				payload, _ := json.Marshal(map[string]any{"text": "PRIVATE_MIXED_CHECKPOINT", "message": "PRIVATE_MIXED_CHECKPOINT", "name": "read_file", "result": "PRIVATE_MIXED_CHECKPOINT"})
				row := &session.Event{ID: fmt.Sprintf("private-%d", i), Author: "bot_runtime", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: f.b.HomeSessionID, RunID: runID, TurnID: runID, Sequence: int64(i + 1), Type: kind, Payload: payload}}}}
				if err := sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), row); err != nil {
					t.Fatal(err)
				}
			}
			// Persist a legitimate user attachment through the existing projector and
			// image serializer; no browser fetch/state interception is involved.
			attachmentPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aMioAAAAASUVORK5CYII=")
			if err != nil {
				t.Fatal(err)
			}
			attachmentRow := &session.Event{ID: "user-attachment", Author: "user", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{Text: "Review my attached image"}, {InlineData: &genai.Blob{MIMEType: "image/png", Data: attachmentPNG}}}}}}
			if err := sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), attachmentRow); err != nil {
				t.Fatal(err)
			}
			publishedRow := &session.Event{ID: publication.EventID, InvocationID: "bot-report:" + publication.RunID.String(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(publication.Summary, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: publication}}}
			if err := sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), publishedRow); err != nil {
				t.Fatal(err)
			}
			page.MustElement(`[data-publication-id="` + publication.EventID + `"]`).MustVisible()
			page.MustElementR("p", "Your daily report is scheduled at 9am UTC.").MustVisible()
			assertMixed := func(mode string) {
				t.Helper()
				text := page.MustElement(`[aria-label="Bot workspace"]`).MustText()
				for _, marker := range []string{"Schedule the daily report at 9am UTC", "Your daily report is scheduled at 9am UTC.", publication.Summary, "Review my attached image"} {
					if !strings.Contains(text, marker) {
						t.Errorf("%s missing public presentation %q: %s", mode, marker, text)
					}
				}
				if strings.Contains(text, "PRIVATE_") || strings.Contains(text, "pending; application") || strings.Contains(text, "I can’t confirm") {
					t.Fatal("private payload leaked")
				}
				for _, marker := range []string{"Your daily report is scheduled at 9am UTC.", publication.Summary, "Review my attached image"} {
					if strings.Count(text, marker) != 1 {
						t.Errorf("%s missing/duplicated public text %q: %s", mode, marker, text)
					}
				}
				if len(page.MustElements(`[data-artifact-id="`+publication.Artifacts[0].ID.String()+`"]`)) != 1 {
					t.Errorf("%s reviewed artifact not shown once", mode)
				}
				imageSelector := `img[src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(attachmentPNG) + `"]`
				if len(page.MustElements(imageSelector)) != 1 {
					t.Errorf("%s user attachment image not shown once", mode)
				} else {
					page.MustElement(imageSelector).MustVisible()
				}
			}
			assertMixed("live/reconnect")
			gate.mu.Lock()
			cursors := append([]string(nil), gate.cursors...)
			gate.mu.Unlock()
			if len(cursors) < 2 || cursors[len(cursors)-1] == "" {
				t.Fatalf("reconnect did not use persisted SSE cursor: %v", cursors)
			}
			if page.MustEval(`() => [...document.querySelectorAll('[data-message-index]')].some(e => e.textContent.includes('Updates unavailable'))`).Bool() {
				t.Fatal("transport status impersonates Employee speech")
			}
			liveDelivery := page.MustElement(`[data-publication-id="` + publication.EventID + `"]`)
			liveDelivery.MustElement(`[aria-label="Open in Files panel"]`).MustClick()
			page.MustElementR("h1", "^Reviewed mixed deliverable$").MustVisible()
			page.MustElementR("button", "^Close preview$").MustClick()
			if got := input.MustProperty("value").Str(); got != "Retain mixed draft during reconnect" {
				t.Fatalf("event stream replaced composer draft: %q", got)
			}
			page.MustReload()
			page.MustElementR("p", "Your daily report is scheduled at 9am UTC.").MustVisible()
			assertMixed("restore")
			delivery := page.MustElement(`[data-publication-id="` + publication.EventID + `"]`)
			if len(page.MustElements(`[data-artifact-id="`+publication.Artifacts[0].ID.String()+`"]`)) != 1 {
				t.Fatal("reviewed delivery duplicated on restore")
			}
			delivery.MustElement(`[aria-label="Open in Files panel"]`).MustClick()
			page.MustElementR("h1", "^Reviewed mixed deliverable$").MustVisible()
			delivery.MustElementR("button", "^Download$").MustClick()
			page.MustElementR("button", "^Download as Markdown$").MustClick()
			var downloaded []byte
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				downloaded, err = os.ReadFile(filepath.Join(downloads, "mixed-reviewed.md"))
				if err == nil && bytes.Equal(downloaded, reviewed) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !bytes.Equal(downloaded, reviewed) {
				t.Fatalf("reviewed native download mismatch: %d bytes %v", len(downloaded), err)
			}
			t.Logf("reviewed mixed preview and native download bytes=%d sha256=%x", len(downloaded), sha256.Sum256(downloaded))
			page.MustElementR("button", "^Close preview$").MustClick()
			unavailable.Store(true)
			failedDraft := page.MustElement(`[data-testid="chat-input"]`)
			failedDraft.MustInput("Keep rejected request draft")
			page.MustElement(`[aria-label="Send message"]`).MustClick()
			page.MustElementR(`[role="alert"]`, "Updates unavailable").MustVisible()
			if page.MustEval(`() => [...document.querySelectorAll('[data-message-index]')].some(e => e.textContent.includes('Updates unavailable'))`).Bool() {
				t.Fatal("failed admission manufactured Employee speech")
			}
			if got := failedDraft.MustProperty("value").Str(); got != "Keep rejected request draft" {
				t.Fatalf("failed admission lost draft: %q", got)
			}
			assertMixed("failed admission")
			unavailable.Store(false)
			ordinaryPage := browser.MustPage(server.URL + "/#/chat/" + ordinaryID.String()).Timeout(20 * time.Second)
			ordinaryPage = ordinaryPage.WithPanic(func(v any) {
				text, _ := ordinaryPage.CancelTimeout().Eval(`() => document.body.innerText`)
				t.Fatalf("ordinary Chat browser failure: %v\n%v", v, text)
			})
			ordinaryPage.MustElementR("p", "Shared presentation complete").MustVisible()
			ordinaryPage.MustElementR("button", "^Explored ").MustClick()
			ordinaryPage.MustElementR("p", "Shared public acknowledgement").MustVisible()
			ordinaryPage.MustReload()
			ordinaryPage.MustElementR("p", "Shared presentation complete").MustVisible()
			ordinaryPage.MustElementR("button", "^Explored ").MustVisible()
			ordinaryPage.MustClose()
			t.Logf("real Employee/private Chat/synchronous scheduler publication; live/reload/reconnect cursor=%v; neutral transport/admission errors; ordinary Chat text/tools retained", cursors)
			artifactDir := "../../.tmp/studio-conversation-artifacts"
			if err := os.MkdirAll(artifactDir, 0755); err != nil {
				t.Fatal(err)
			}
			page.MustElement(`[aria-label="Bot workspace"]`).MustScreenshot(filepath.Join(artifactDir, string(scope)+".png"))
			rows, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
			if err != nil || len(rows) != 1 {
				t.Fatalf("view/reload admitted work: %+v %v", rows, err)
			}
			current, err := f.repo.Get(f.ctx, f.b.ID)
			if err != nil || current.Status != store.BotActive {
				t.Fatalf("view/reload changed lifecycle: %+v %v", current, err)
			}
			t.Logf("built Studio scope=%s endpoint=%s isolated_profile=true live/restore mixed public frames; no run admission", scope, server.URL)
		})
	}
}
