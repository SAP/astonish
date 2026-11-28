package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type browserIntentModel struct {
	context  *atomic.Value
	calls    *atomic.Int64
	proposal bool
	emitted  bool
}

func (*browserIntentModel) Name() string { return "journey-model" }
func (m *browserIntentModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.calls.Add(1)
	if m.context != nil && req.Config != nil && req.Config.SystemInstruction != nil {
		for _, part := range req.Config.SystemInstruction.Parts {
			if part != nil && part.Text != "" {
				m.context.Store(part.Text)
			}
		}
	}
	if response, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool {
		return w.Candidate.Text == "Draft test browser reply." || w.Candidate.Text == "Active browser conversational reply." || w.Candidate.Text == "Paused browser conversational reply." || w.Candidate.Text == "Responsibility proposed, not applied. Please confirm the saved review."
	}); ok {
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.proposal && !m.emitted {
			m.emitted = true
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "I will propose the responsibility for your review."}, {FunctionCall: &genai.FunctionCall{ID: "browser-responsibility", Name: "propose_bot_control", Args: map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "Owner requested public notices", "responsibility": "Track reviewed browser notices"}}}}}}}, nil)
			return
		}
		text := "Draft test browser reply."
		if m.proposal {
			text = "Responsibility proposed, not applied. Please confirm the saved review."
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if strings.Contains(part.Text, "Active browser owner question") {
					text = "Active browser conversational reply."
				}
				if strings.Contains(part.Text, "Paused browser owner question") {
					text = "Paused browser conversational reply."
				}
			}
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}

// TestBotScheduledWorkspaceBrowserAcceptance joins the production scheduler,
// scoped daemon worker, ChatRunner, SQLite, HTTP/SSE and built Studio SPA. Only
// identity/settings, model, isolated local tool and home binding are controlled.
// No run, approval, checkpoint or transcript result is manufactured by the test.
// This is not physical sandbox storage or external delivery acceptance.
func TestBotScheduledWorkspaceBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "scheduled") }
func TestBotChatFirstWorkspaceBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "workspace") }
func TestBotChatFirstOrderedConversationBrowserAcceptance(t *testing.T) {
	runBotBrowserJourney(t, "ordered")
}

// Independent databases, sockets, browser profiles and workers for each journey.
func runBotBrowserJourney(t *testing.T, journey string) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building web")
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
		t.Fatalf("build Studio SPA first: %v", err)
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		var found bool
		bin, found = browserlauncher.LookPath()
		if !found {
			t.Fatal("browser prerequisite missing; set ASTONISH_BOT_BROWSER_PATH")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "scheduled-browser", Name: "Scheduled browser", Status: "active"}
	if err = db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "journey", Name: "Journey", SchemaName: "team_journey"}
	if err = ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	auth := func(c context.Context) context.Context {
		// Every journey uses the same fresh owner Chat admission. This grants
		// conversation/continuation, not authority for the captured exact action.
		scopes := []string{"chat"}
		if strings.HasPrefix(journey, "title") {
			scopes = append(scopes, "tool:execute")
		}
		p, e := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: scopes})
		if e != nil {
			panic(e)
		}
		p, e = execution.WithAuthenticationEvidence(p, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			panic(e)
		}
		return store.WithTenantContext(p, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	}
	ownerCtx := auth(ctx)
	personal := ods.ForUser(owner)
	repo := personal.Bots()
	teamJourney := strings.HasPrefix(journey, "title-team")
	if teamJourney {
		repo = ods.ForTeam(team.Slug).Bots()
	}
	registry := execution.NewBotTurnAuthorizationRegistry(100)
	svc := &store.Services{TenantRouter: db, Bots: ods.ForTeam(team.Slug).Bots(), Sessions: ods.ForTeam(team.Slug).Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: strings.HasPrefix(journey, "title"), BotTurnRegistry: registry}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	// Mount exported production handlers, not browser fetch or response mocks.
	for _, route := range []struct {
		path, method string
		handler      http.HandlerFunc
	}{
		{root, "POST", api.BotCreateHandler}, {root, "GET", api.BotListHandler},
		{root + "/{botID}", "GET", api.BotGetHandler},
		{root + "/{botID}/snapshot", "GET", api.BotSnapshotHandler},
		{root + "/{botID}/activity", "GET", api.BotActivityHandler},
		{root + "/{botID}/read", "GET", api.BotReadCursorHandler},
		{root + "/{botID}/read", "POST", api.BotMarkReadHandler},
		{root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler},
		{root + "/{botID}/configuration", "PUT", api.BotConfigurationHandler},
		{root + "/{botID}/resume", "POST", api.BotResumeHandler},
		{root + "/{botID}/pause", "POST", api.BotPauseHandler},
		{root + "/{botID}/runs", "GET", api.BotRunsHandler},
		{root + "/{botID}/runs", "POST", api.BotRequestRunHandler},
		{root + "/{botID}/test-run", "POST", api.BotTestRunHandler},
		{root + "/{botID}/messages", "POST", api.BotMessageHandler},
		{root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler},
		{root + "/{botID}/proposals/{proposalID}/decision", "POST", api.BotControlProposalDecisionHandler},
		{root + "/{botID}/approvals/{approvalID}/approve-and-continue", "POST", api.BotApproveContinueHandler},
		{root + "/{botID}/conversation", "POST", api.BotConversationHandler},
		{root + "/{botID}/runs/{runID}", "GET", api.BotRunHandler},
		{root + "/{botID}/runs/{runID}/resume", "POST", api.BotResumeRunHandler},
		{root + "/{botID}/approvals", "GET", api.BotApprovalsHandler},
		{root + "/{botID}/approvals/{approvalID}/decision", "POST", api.BotApprovalDecisionHandler},
		{root + "/{botID}/goals", "GET", api.BotGoalsHandler},
		{root + "/{botID}/sessions/{sessionID}/events", "GET", api.BotEventsHandler},
		{root + "/{botID}/sessions/{sessionID}/stream", "GET", api.BotHistoryStreamHandler},
		{root + "/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}/preview", "GET", api.BotFilePreviewHandler},
		{root + "/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}/download", "GET", api.BotFileDownloadHandler},
		{root + "/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}/download-ticket", "POST", api.BotFileDownloadTicketHandler},
	} {
		router.HandleFunc(route.path, route.handler).Methods(route.method)
	}
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fixture any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			fixture = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			fixture = map[string]any{"user": map[string]any{"id": owner, "email": "scheduled@example.invalid", "display_name": "Scheduled acceptance", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": org.Slug, "name": org.Name}, "team": team.Slug}
		case "/api/orgs":
			fixture = map[string]any{"orgs": []any{}, "active_org": org.Slug}
		case "/api/teams":
			fixture = []any{map[string]any{"slug": team.Slug, "name": "Journey"}}
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
			_ = json.NewEncoder(w).Encode(fixture)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "fixture endpoint unavailable", 404)
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
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth(r.Context()), svc)))
	}))
	defer server.Close()
	request := func(method, path, body string, status int) []byte {
		t.Helper()
		r, e := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		data, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, status, data)
		}
		return data
	}
	launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatalf("launch isolated browser: %v", err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage(server.URL + "/#/chat").Timeout(45 * time.Second)
	page = page.WithPanic(func(v any) {
		text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
		if text != nil {
			t.Fatalf("browser failure: %v\nDOM: %s", v, text.Value.Str())
		}
		t.Fatalf("browser failure: %v (DOM unavailable)", v)
	})
	var b store.Bot
	if teamJourney {
		page.MustElementR("button", "^New Bot$").MustVisible()
		page.MustEval(`() => localStorage.setItem('astonish_active_team', 'journey')`)
		// Seed retained team title admission without reopening HTTP creation;
		// the built team workspace still uses real HTTP/SSE handlers.
		request("POST", root+"?scope=team", `{"name":"Summarize release notes","idempotency_key":"team-title","start_from_title":true}`, http.StatusForbidden)
		version, e := store.NewBotVersionForCreation(ownerCtx, uuid.MustParse(owner))
		if e != nil {
			t.Fatal(e)
		}
		version.Charter = "Summarize release notes\n\nTake responsibility for the owner's goals and deliver useful, verified outcomes. Perform routine work within your authorized mandate and existing permissions without extra review. Escalate genuine ambiguity or actions outside that scope for clarification or authorization. A request or this charter does not itself grant permission for external side effects."
		receipt, e := repo.(store.BotTitleStartStore).CreateAndStart(store.WithBotMessageAdmission(ownerCtx, registry), store.BotCreateRequest{Name: "Summarize release notes", OwnerID: uuid.MustParse(owner), Version: version, IdempotencyKey: "team-title"})
		if e != nil {
			t.Fatal(e)
		}
		b = receipt.Bot
		if b.Scope != store.BotScopeTeam {
			t.Fatal("team admission used personal store")
		}
		page.MustNavigate(server.URL + "/#/chat/bots/team/" + b.ID.String())
		page.MustReload()
		page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	} else if strings.HasPrefix(journey, "title") {
		page.MustElementR("button", "^New Bot$").MustClick()
		title := "Scheduled browser watcher"
		if journey == "title-question" {
			title = "Help me"
		}
		page.MustElement(`[aria-label="Bot name"]`).MustInput(title)
		page.MustElementR("button", "^Start conversation$").MustClick()
		page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		botID := strings.Split(page.MustEval(`() => location.hash`).Str(), "/")[4]
		if err = json.Unmarshal(request("GET", root+"/"+botID, "", 200), &b); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Log("scheduled fixture: waiting for initial Studio shell")
		page.MustElementR("button", "^New Bot$").MustVisible()
		// Ordinary creation is Active without admitting work. Pause explicitly
		// before installing the reviewed capability for this controlled journey.
		if err = json.Unmarshal(request("POST", root, `{"name":"Scheduled browser watcher","idempotency_key":"scheduled-fixture"}`, 201), &b); err != nil {
			t.Fatal(err)
		}
		page.MustNavigate(server.URL + "/#/chat/bots/personal/" + b.ID.String())
		page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	}
	if b.Status != store.BotActive {
		t.Fatal("minimal creation did not produce an Active Bot")
	}
	path := root + "/" + b.ID.String()
	v, err := repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(journey, "title") {
		var pinErr error
		if teamJourney {
			pinErr = ods.ForTeam(team.Slug).SetSessionPin(ownerCtx, b.HomeSessionID.String(), "controlled", "journey-model")
		} else {
			pinErr = personal.SetSessionPin(ownerCtx, b.HomeSessionID.String(), "controlled", "journey-model")
		}
		if pinErr != nil {
			t.Fatal(pinErr)
		}
		runTitleBrowserAssertions(t, ctx, page, repo, ownerCtx, db, registry, b, journey)
		return
	}
	// Sensitive configuration requires an explicitly Paused Bot and the new
	// lifecycle revision; creation itself must not be a configuration bypass.
	if err = json.Unmarshal(request("POST", path+"/pause", fmt.Sprintf(`{"expected_revision":%d,"reason":"Pause for reviewed isolated capability"}`, b.Revision), 200), &b); err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotPaused {
		t.Fatal("configuration fixture did not pause Bot")
	}
	v.Autonomy.AllowedTools = []string{"journey_exact_action"}
	configuration, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Reviewed isolated local capability", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(request("PUT", path+"/configuration", string(configuration), 200), &b); err != nil {
		t.Fatal(err)
	}
	registryCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "browser-registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, Actor: "browser-registry", OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: store.BotScopePersonal, Backend: "controlled", StorageKey: "journey-home", Revision: 1}
	if err = entstore.RegisterBotHomeBinding(registryCtx, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(c context.Context, q entstore.BotHomeRegistration) error {
		actual, e := (journeyHomeBackend{}).EnsureBotHome(c, sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner, BotID: b.ID.String()})
		if e != nil {
			return e
		}
		if actual.Ref != q.Home.StorageKey || string(actual.Backend) != q.Home.Backend {
			return store.ErrBotConflict
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Get(ownerCtx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	triggerID := uuid.New()
	updated, err = repo.(store.BotTriggerStore).ChangeTrigger(ownerCtx, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "Reviewed scheduled browser task", Definition: store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "UTC", Instructions: "Execute saved exact action then report."}})
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	if err = personal.SetSessionPin(ownerCtx, b.HomeSessionID.String(), "controlled", "journey-model"); err != nil {
		t.Fatal(err)
	}
	var modelContext atomic.Value
	var calls, effects atomic.Int64
	var textOnly, propose atomic.Bool
	textOnly.Store(true)
	files := newComposedHTTPFiles(t, dir, repo, b, nil)
	// Offline HTTP delivery reads the same retained snapshots as scoped Chat,
	// without starting a worker or borrowing a viewer/host file service.
	ownerPrincipal, _ := execution.PrincipalFromContext(ownerCtx)
	deliveryFiles := &journeyReceiptFiles{composedHTTPFiles: files, req: bot.RunRequest{Bot: b, Principal: ownerPrincipal}}
	svc.PersonalCredentials = personal.Credentials()
	svc.BotFilesFactory = func(c context.Context, scoped *store.Services, scope store.BotScope) (store.BotFiles, error) {
		if scoped != svc || scope != store.BotScopePersonal {
			return nil, store.ErrBotForbidden
		}
		if _, err := scoped.PersonalBots.Get(c, b.ID); err != nil {
			return nil, err
		}
		return deliveryFiles, nil
	}
	pool := &interactiveHTTPPool{dir: dir}
	makeWorker := func(report bool) *daemon.BotWorker {
		w, e := daemon.NewBotWorker(daemon.BotWorkerConfig{TurnRegistry: registry, Backend: db, Sandbox: journeyFilesBackend{synthesisHTTPBackend{files: files.disk}}, Pool: pool, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, p execution.Principal, s store.BotScope, pn, mn string) (model.LLM, error) {
			if p.Subject != owner || s != store.BotScopePersonal || pn != "controlled" || mn != "journey-model" {
				return nil, store.ErrBotForbidden
			}
			if textOnly.Load() {
				return &browserIntentModel{context: &modelContext, calls: &calls, proposal: propose.Load()}, nil
			}
			return &journeyModel{root: dir, action: "journey_exact_action", report: report, calls: &calls}, nil
		}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
			files.repo = s.Services.PersonalBots
			receiptFiles := &journeyReceiptFiles{composedHTTPFiles: files, req: q}
			s.Services.BotFilesFactory = func(ctx context.Context, _ *store.Services, scope store.BotScope) (store.BotFiles, error) {
				if scope != q.Bot.Scope {
					return nil, store.ErrBotForbidden
				}
				if _, err := files.repo.Get(ctx, q.Bot.ID); err != nil {
					return nil, err
				}
				return receiptFiles, nil
			}
			publication, receipts, err := launcher.BotPublicationToolsWithReceipts(q, s)
			if err != nil {
				return nil, err
			}
			work, e := os.MkdirTemp(dir, "isolated-local-tool-")
			if e != nil {
				return nil, e
			}
			action, e := functiontool.New(functiontool.Config{Name: "journey_exact_action", Description: "Controlled local receipt action"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				if args["body"] != "exact" {
					return nil, store.ErrBotForbidden
				}
				receiptPath := filepath.Join(work, "receipt")
				if e := os.WriteFile(receiptPath, []byte("exact"), 0600); e != nil {
					return nil, e
				}
				got, e := os.ReadFile(receiptPath)
				if e != nil || string(got) != "exact" {
					return nil, fmt.Errorf("receipt readback: %v", e)
				}
				effects.Add(1)
				body := "Exact approved action receipt observed."
				// Persist real report bytes and publish through the production accumulator.
				reportDir := filepath.Join(dir, "home", "reports")
				if err := os.MkdirAll(reportDir, 0700); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(reportDir, "receipt.md"), []byte(body), 0600); err != nil {
					return nil, err
				}
				publisher := publication[0].(interface {
					Run(tool.Context, any) (map[string]any, error)
				})
				publicationContext := journeyPublicationContext{c}
				if _, err := publisher.Run(publicationContext, map[string]any{"path": "outputs/receipt.md", "title": "Receipt", "purpose": "Exact action receipt", "memory_summary": body}); err != nil {
					return nil, err
				}
				published, ok := bot.TakeToolOutcome(c, publicationContext.FunctionCallID())
				if !ok || !published.Verified || published.ReceiptConflict || len(receipts.Artifacts(q)) != 1 {
					return nil, store.ErrBotConflict
				}
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Scheduled browser receipt read back", Source: receiptPath, ObservedAt: time.Now().UTC(), ActorID: q.Bot.OwnerID}}})
				return map[string]any{"receipt": string(got), "report_path": "outputs/receipt.md"}, nil
			})
			if e != nil {
				return nil, e
			}
			return &launcher.BotChatSafeResources{FileReceipts: receipts, ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool, Groups: map[string]*agent.ToolGroup{"journey": {Name: "journey", Tools: []tool.Tool{action}}}}, WorkspaceDir: work, PolicyRuntime: agent.BotPolicyRuntime{Classifier: journeyClassifier{}, Guard: journeyGuard{repo: s.Services.PersonalBots, req: q}}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		return w
	}
	worker := makeWorker(false)
	// Install readiness only after the scoped shared Chat worker was constructed;
	// HTTP approval retains the very same live turn registry as that worker.
	svc.BotInteractiveReady = true
	defer func() { worker.Stop() }()
	// Resume only after the real worker installs readiness. This must neither
	// admit work nor enable the separately reviewed (but still disabled) schedule.
	if err = json.Unmarshal(request("POST", path+"/resume", fmt.Sprintf(`{"expected_revision":%d,"reason":"Resume reviewed conversation fixture"}`, b.Revision), 200), &b); err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotActive || calls.Load() != 0 || effects.Load() != 0 {
		t.Fatal("fixture resume performed work or did not become Active")
	}
	resumedVersion, err := repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range resumedVersion.Triggers {
		if trigger.Enabled {
			t.Fatal("fixture resume enabled an unauthorized schedule")
		}
		if job := personal.ScheduledJobs().Get(ownerCtx, trigger.ScheduleID.String()); job != nil && job.Enabled {
			t.Fatal("fixture resume enabled an unauthorized scheduled job")
		}
	}
	homeURL := server.URL + "/#/chat/bots/personal/" + b.ID.String()
	page.MustReload()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	// Shared Studio Chat no longer renders a Bot-only transcript status paragraph.
	// The restored Home must instead expose the actual enabled conversation composer.
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustVisible()
	page.MustWait(`() => !document.querySelector('[aria-label="Bot workspace"] [data-testid="chat-input"]').disabled`)
	page = page.CancelTimeout().Timeout(3 * time.Minute)
	if journey != "scheduled" {
		propose.Store(true)
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Please propose tracking public notices")
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
		deadline := time.Now().Add(15 * time.Second)
		for {
			rows, e := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if len(rows) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("message not admitted")
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err = worker.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		observedRuns, observedErr := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
		t.Logf("proposal worker runs=%+v err=%v", observedRuns, observedErr)
		page.MustElementR(`[aria-label="Bot workspace"] h3`, "^Review responsibility$").MustVisible()
		page.MustElementR(`[aria-label="Bot workspace"] p`, "^Responsibility proposed, not applied").MustVisible()
		original, e := repo.GetVersion(ownerCtx, b.ID, b.Version)
		if e != nil || original.Charter == "Track reviewed browser notices" {
			t.Fatal("proposal applied without consent", e)
		}
		assertOrder := func() {
			t.Helper()
			if page.MustInfo().URL != homeURL {
				t.Fatal("message left stable home route")
			}
			if !page.MustEval(`() => { const root = document.querySelector('[aria-label="Bot workspace"]'); const text = root.innerText; return text.indexOf('Please propose tracking public notices') < text.indexOf('Review responsibility') && text.indexOf('Review responsibility') < text.indexOf('Responsibility proposed, not applied'); }`).Bool() {
				t.Fatal("user/review/following narrative out of order")
			}
			if got := page.MustEval(`() => document.querySelectorAll('[aria-label="Bot workspace"] [data-review-id]').length`).Int(); got != 1 {
				t.Fatalf("review painted %d times", got)
			}
		}
		assertOrder()
		before := calls.Load()
		page.MustEval(`() => { window.__botComposer = document.querySelector('[aria-label="Bot workspace"] [data-testid="chat-input"]') }`)
		page.MustElementR("button", "^Details$").MustClick()
		page.MustElementR("button", "^Close$").MustClick()
		if !page.MustEval(`() => window.__botComposer === document.querySelector('[aria-label="Bot workspace"] [data-testid="chat-input"]')`).Bool() {
			t.Fatal("Details remounted home conversation")
		}
		assertOrder()
		page.MustReload()
		page.MustElementR(`[aria-label="Bot workspace"] p`, "^Responsibility proposed, not applied").MustVisible()
		assertOrder()
		if calls.Load() != before {
			t.Fatal("reload replayed conversation")
		}
		page.MustElementR(`[aria-label="Bot workspace"] button`, "^Confirm exact change$").MustClick()
		page.MustWait(`() => { const root = document.querySelector('[aria-label="Bot workspace"]'); return [...root.querySelectorAll('[data-review-id] p')].some(p => p.textContent === 'applied') || !!root.querySelector('[role="alert"]'); }`)
		if alert := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"] [role="alert"]')?.textContent || ''`).Str(); alert != "" {
			proposals := string(request("GET", path+"/proposals", "", 200))
			current := string(request("GET", path, "", 200))
			t.Fatalf("exact proposal confirmation failed: %s; proposals=%s; Bot=%s", alert, proposals, current)
		}
		page.MustElementR(`[aria-label="Bot workspace"] [data-review-id] p`, "^applied$").MustVisible()
		updated, err = repo.Get(ownerCtx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		b = *updated
		confirmed, e := repo.GetVersion(ownerCtx, b.ID, b.Version)
		if e != nil || confirmed.Charter != "Track reviewed browser notices" || b.Status != store.BotActive {
			t.Fatalf("confirmed responsibility not saved safely: %+v %v", confirmed, e)
		}
		if calls.Load() != before || effects.Load() != 0 {
			t.Fatal("confirmation/reload executed model or tool")
		}
		t.Log("minimal Active creation -> explicit pause/configure/resume without schedule revival -> real worker proposal -> ordered canonical review and following narrative -> Details preserves mounted composer -> reload once -> exact confirm persisted")
		if journey == "ordered" {
			return
		}
		propose.Store(false)
	}
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Execution setup$").MustClick()
	baselineRuns, err := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	page.MustElementR("button", "^Test once$").MustClick()
	page.MustElement(`[aria-label="Test instructions"]`).MustInput("Draft browser one-shot")
	if !page.MustElementR("button", "^Confirm test once$").MustProperty("disabled").Bool() {
		t.Fatal("test runs without consent")
	}
	page.MustElementR("button", "^Cancel test$").MustClick()
	runs, err := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(runs) != len(baselineRuns) {
		t.Fatalf("cancel admitted a run: %+v %v", runs, err)
	}
	page.MustElementR("button", "^Test once$").MustClick()
	page.MustElement(`[aria-label="Review test once"] input[type="checkbox"]`).MustClick()
	page.MustElementR("button", "^Confirm test once$").MustClick()
	page.MustWait(`() => location.hash.includes('/runs/')`)
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	runs, err = repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	baselineIDs := make(map[store.RunID]bool, len(baselineRuns))
	for _, previous := range baselineRuns {
		baselineIDs[previous.ID] = true
	}
	var testRun *store.BotRun
	for i := range runs {
		if !baselineIDs[runs[i].ID] {
			testRun = &runs[i]
		}
	}
	if err != nil || len(runs) != len(baselineRuns)+1 || testRun == nil || testRun.Intent != store.BotIntentRegular || testRun.Status != store.RunSucceeded {
		t.Fatalf("draft test: %+v %v", runs, err)
	}
	page.MustReload()
	page.MustElement(`[aria-label="Selected run"]`).MustVisible()
	page.MustElementR(`[aria-label="Selected run"] p`, "Draft test browser reply").MustVisible()
	page.MustNavigate(homeURL)
	page.MustElementR("p", "draft · Private").MustVisible()
	// Read production-persisted Home rows: the filter must not delete model
	// context from durable history, and absence in the DOM must not be vacuous.
	reader := personal.Sessions().(store.BotTranscriptReader)
	var homeCursor int64
	contextRows, genuineInputs := 0, 0
	for {
		rows, e := reader.ReadBotTranscript(ownerCtx, b.HomeSessionID, homeCursor, 200)
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			homeCursor = row.Cursor
			if prompt, _ := row.Event.Actions.StateDelta["_astonish_system_prompt"].(string); prompt != "" {
				// Shared Chat keeps Bot execution hints in the hidden system prompt,
				// not a manufactured user-role turn-context event.
				contextRows++
			}
			if row.Event.Content == nil {
				continue
			}
			marked, _ := row.Event.Actions.StateDelta["_astonish_turn_context"].(bool)
			for _, part := range row.Event.Content.Parts {
				if part == nil {
					continue
				}
				if marked && strings.Contains(part.Text, "[Astonish Per-Turn Context") {
					contextRows++
				}
				if !marked && row.Event.Author == "user" && strings.Contains(part.Text, "Draft browser one-shot") {
					genuineInputs++
				}
			}
		}
	}
	// The shared runtime deliberately does not persist Bot prompt instructions
	// as transcript content. Prove a real model-facing prompt existed instead.
	prompt, _ := modelContext.Load().(string)
	if prompt == "" || genuineInputs != 1 {
		t.Fatalf("Transcript provenance missing: model prompt=%t genuine inputs=%d", prompt != "", genuineInputs)
	}
	assertHomeContextHidden := func() {
		t.Helper()
		// Shared Studio Chat renders the restored ordered messages, not a Bot-only
		// cursor/status paragraph. Require both durable messages before inspection.
		page.MustElementR(`[aria-label="Bot workspace"] p`, "Draft browser one-shot").MustVisible()
		page.MustElementR(`[aria-label="Bot workspace"] p`, "Draft test browser reply").MustVisible()
		text := page.MustElement(`[aria-label="Bot workspace"]`).MustText()
		if strings.Count(text, "Draft browser one-shot") != 1 || strings.Contains(text, "[Astonish Per-Turn Context") || strings.Contains(text, "Relevant Tools For This Request") || strings.Contains(text, "Bot execution runtime:") {
			t.Fatalf("Home leaked internal context or lost/duplicated real input:\n%s", text)
		}
	}
	assertHomeContextHidden()
	beforeReload := calls.Load()
	page.MustReload()
	assertHomeContextHidden()
	if calls.Load() != beforeReload {
		t.Fatal("Home context-filter reload reran model")
	}
	t.Logf("Home and reload: %d hidden context rows, model-facing prompt observed, genuine input shown once, internal context absent, cursor=%s:%d", contextRows, b.HomeSessionID, homeCursor)
	assertDisabled := func(status store.BotStatus) {
		t.Helper()
		current, e := repo.Get(ownerCtx, b.ID)
		if e != nil || current.Status != status {
			t.Fatalf("intent changed lifecycle: %+v %v", current, e)
		}
		version, e := repo.GetVersion(ownerCtx, b.ID, current.Version)
		if e != nil {
			t.Fatal(e)
		}
		for _, trigger := range version.Triggers {
			if status == store.BotActive && trigger.Enabled {
				t.Fatal("draft intent enabled trigger")
			}
			if job := personal.ScheduledJobs().Get(ownerCtx, trigger.ScheduleID.String()); job != nil && job.Enabled {
				t.Errorf("intent enabled scheduled job")
			}
		}
	}
	assertDisabled(store.BotActive)
	if effects.Load() != 0 {
		t.Fatal("text-only draft test invoked tool")
	}
	t.Log("draft Test once: cancel admitted nothing; consent ran real worker; succeeded reply restored on reload; draft and disabled schedule retained")
	calls.Store(0) // Preserve original activation/scheduled journey counters.
	textOnly.Store(false)
	// Activation uses readiness installed by the real production worker.
	if err = json.Unmarshal(request("POST", path+"/resume", fmt.Sprintf(`{"expected_revision":%d,"reason":"Reviewed scheduled acceptance"}`, b.Revision), 200), &b); err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotActive || calls.Load() != 0 || effects.Load() != 0 {
		t.Fatal("activation performed work or did not activate")
	}
	if journey == "workspace" {
		textOnly.Store(true)
		page.MustReload()
		page.MustElementR("p", "active · Private").MustVisible()
		previous, e := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
		if e != nil {
			t.Fatal(e)
		}
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Active browser owner question")
		page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
		deadline := time.Now().Add(15 * time.Second)
		for {
			rows, e := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if len(rows) == len(previous)+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("active message not admitted")
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err = worker.Poll(ctx); err != nil {
			t.Fatal(err)
		}
		page.MustElementR(`[aria-label="Bot workspace"] p`, "^Active browser conversational reply\\.$").MustVisible()
		if page.MustInfo().URL != homeURL || effects.Load() != 0 {
			t.Fatal("active conversation changed route or executed external effect")
		}
		count := calls.Load()
		page.MustReload()
		page.MustElementR(`[aria-label="Bot workspace"] p`, "^Active browser conversational reply\\.$").MustVisible()
		if calls.Load() != count {
			t.Fatal("active response reload reran model")
		}
		calls.Store(0)
		textOnly.Store(false)
	}
	updated, err = repo.(store.BotTriggerStore).ChangeTrigger(ownerCtx, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "Enable reviewed schedule"})
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	v, err = repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	jobs := personal.ScheduledJobs()
	job := jobs.Get(ownerCtx, v.Triggers[0].ScheduleID.String())
	if job == nil || !job.Enabled {
		t.Fatal("reviewed schedule missing")
	}
	due := time.Now().Add(-time.Minute).UTC()
	job.NextRun = &due
	if err = jobs.Update(ownerCtx, job); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan scheduler.BotAdmission, 4)
	executor := &scheduler.Executor{BotTrigger: func(c context.Context, q scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
		a, e := repo.(store.BotScheduledRunStore).AdmitScheduledRun(auth(c), store.BotScheduledOccurrence{JobID: q.JobID, BotID: q.BotID, Version: q.Version, TriggerID: q.TriggerID, OccurrenceAt: q.OccurrenceAt, IdempotencyKey: q.IdempotencyKey})
		if e != nil {
			return scheduler.BotAdmission{}, e
		}
		status := scheduler.BotAdmissionAdmitted
		if a.Duplicate {
			status = scheduler.BotAdmissionDuplicate
		}
		receipt := scheduler.BotAdmission{Status: status, RunID: a.Run.ID}
		ticks <- receipt
		return receipt, nil
	}}
	adapter := &journeyJobs{ctx: ownerCtx, store: jobs}
	t.Logf("scheduled fixture durable job=%+v adapter=%+v", jobs.Get(ownerCtx, job.ID), adapter.Get(job.ID))
	for _, listed := range adapter.List() {
		t.Logf("scheduled fixture listed job=%+v", listed)
	}
	adapter.drop.Store(true)
	engine := scheduler.New(adapter, executor.Execute, scheduler.WithoutBotTriggerDelivery(nil), nil)
	engine.Start(ctx)
	defer engine.Stop()
	var runID uuid.UUID
	select {
	case receipt := <-ticks:
		if receipt.Status != scheduler.BotAdmissionAdmitted {
			t.Fatal(receipt)
		}
		runID = receipt.RunID
	case <-time.After(65 * time.Second):
		t.Fatalf("real scheduler tick missing: durable=%+v adapter=%+v", jobs.Get(ownerCtx, job.ID), adapter.Get(job.ID))
	case <-ctx.Done():
		t.Fatal("real scheduler tick missing", ctx.Err())
	}
	engine.Stop()
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ownerCtx, b.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunWaitingApproval || effects.Load() != 0 || len(run.Checkpoint) == 0 {
		t.Fatalf("not safely waiting: %+v effects=%d", run, effects.Load())
	}
	sessionID, fence := run.SessionID, run.Lease.Fence
	pending, err := repo.ListApprovals(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(pending) != 1 || pending[0].RunID != runID || pending[0].Status != store.ApprovalPending {
		t.Fatalf("worker approval missing: %+v %v", pending, err)
	}
	digest := pending[0].ActionDigest
	assertHome := func(want int) {
		t.Helper()
		events, e := personal.Sessions().ReadTranscriptEvents(ownerCtx, "astonish", owner, b.HomeSessionID.String())
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for _, event := range events {
			if event.InvocationID == "bot-report:"+runID.String() {
				count++
			}
		}
		if count != want {
			t.Fatalf("home reports=%d want=%d", count, want)
		}
	}
	assertHome(1)
	// Observe the actual public employee narrative via the open HTTP/SSE stream,
	// not the private diagnostic summary retained on the worker report.
	if run.Report.EmployeeMessage == nil || run.Report.EmployeeMessage.Text == "" {
		t.Fatal("worker produced no public waiting message")
	}
	page.MustElementR(`[aria-label="Bot workspace"] p`, regexp.QuoteMeta(run.Report.EmployeeMessage.Text)).MustVisible()
	page.MustElementR(`[aria-label="Bot workspace"] [aria-label="Bot review"] pre`, "journey_exact_action").MustVisible()
	page.MustElementR(`[aria-label="Bot workspace"] [aria-label="Bot review"] pre`, "exact").MustVisible()
	page.MustReload()
	page.MustElementR(`[aria-label="Bot workspace"] [aria-label="Bot review"] pre`, "journey_exact_action").MustVisible()
	page.MustElementR(`[aria-label="Bot workspace"] [aria-label="Bot review"] button`, "^Approve & continue$").MustClick()
	page.MustWait(`() => { const root = document.querySelector('[aria-label="Bot workspace"]'); return root.innerText.includes('I saved your approval and queued') || !!root.querySelector('[role="alert"]'); }`)
	if alert := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"] [role="alert"]')?.textContent || ''`).Str(); alert != "" {
		t.Fatalf("exact approve-and-continue failed: %s", alert)
	}
	page.MustElementR(`[aria-label="Bot workspace"] [aria-label="Bot review"] p`, "I saved your approval and queued").MustVisible()
	approvals, err := repo.ListApprovals(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalApproved || approvals[0].ActionDigest != digest {
		t.Fatalf("approval did not persist: %+v %v", approvals, err)
	}
	if effects.Load() != 0 {
		t.Fatal("HTTP approval executed tool outside worker")
	}
	t.Logf("real scheduled run %s: waiting home report live; one exact approve-and-continue gesture", runID)
	deadline := time.Now().Add(15 * time.Second)
	for {
		queued, e := repo.GetRun(ownerCtx, b.ID, runID)
		if e != nil {
			t.Fatal(e)
		}
		if queued.Status == store.RunQueued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser resume did not queue run: %+v", queued)
		}
		time.Sleep(25 * time.Millisecond)
	}
	worker.Stop()
	worker = makeWorker(true)
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	run, err = repo.GetRun(ownerCtx, b.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunSucceeded || run.SessionID != sessionID || run.Lease.Fence <= fence || effects.Load() != 1 || len(run.Report.Changed) != 1 || len(run.Report.Artifacts) != 1 || !strings.Contains(run.Report.Summary, "receipt observed") {
		t.Fatalf("resume failed: %+v effects=%d", run, effects.Load())
	}
	assertHome(2)
	// Resolve only the committed, reviewed publication; mutable workspace bytes
	// cannot substitute for the exact approved deliverable on this HTTP route.
	var reportSequence int64
	for cursor := int64(0); ; {
		activities, err := repo.ListActivity(ownerCtx, b.ID, cursor, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, activity := range activities {
			cursor = activity.Sequence
			if activity.Kind == store.BotActivityReport && activity.RunID != nil && *activity.RunID == runID {
				var outcome struct {
					Intent store.BotOutboxKind `json:"intent"`
					Status store.RunStatus     `json:"status"`
				}
				if err := json.Unmarshal(activity.Payload, &outcome); err != nil {
					t.Fatal(err)
				}
				if outcome.Intent == store.BotOutboxPublish && outcome.Status == store.RunSucceeded {
					reportSequence = activity.Sequence
				}
			}
		}
		if len(activities) < 200 {
			break
		}
	}
	if reportSequence == 0 {
		t.Fatal("successful committed publication sequence missing")
	}
	artifactURL := fmt.Sprintf("%s/runs/%s/reports/%d/artifacts/%s", path, runID, reportSequence, run.Report.Artifacts[0].ID)
	var preview struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(request("GET", artifactURL+"/preview", "", http.StatusOK), &preview); err != nil || preview.Truncated || preview.Content != "Exact approved action receipt observed." {
		t.Fatalf("reviewed artifact preview: %+v %v", preview, err)
	}
	if downloaded := request("GET", artifactURL+"/download", "", http.StatusOK); string(downloaded) != preview.Content {
		t.Fatalf("exact artifact download differs: %q", downloaded)
	}
	t.Log("reviewed retained artifact: production preview and exact downloadable receipt bytes observed")
	page.MustElementR(`[aria-label="Bot workspace"] p`, "Exact approved action receipt observed").MustVisible()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Runs & activity$").MustClick()
	page.MustElementR("a", "succeeded · cron").MustClick()
	page.MustElement(`[aria-label="Selected run"]`).MustVisible()
	page.MustNavigate(homeURL)
	page.MustElementR(`[aria-label="Bot workspace"] p`, "Exact approved action receipt observed").MustVisible()
	before := calls.Load()
	page.MustReload()
	page.MustElementR(`[aria-label="Bot workspace"] p`, "Exact approved action receipt observed").MustVisible()
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Runs & activity$").MustClick()
	page.MustElementR("a", "succeeded · cron").MustVisible()
	page.MustElementR("p", "Changed: Scheduled browser receipt read back").MustVisible()
	if calls.Load() != before || effects.Load() != 1 {
		t.Fatal("history reload reran model/tool")
	}
	page.MustElementR("button", "^Pause$").MustClick()
	page.MustElement(`[aria-label="Lifecycle reason"]`).MustInput("Pause after scheduled browser acceptance")
	page.MustElementR("button", "^Confirm pause$").MustClick()
	page.MustElementR("p", "paused · Private").MustVisible()
	page.MustReload()
	page.MustElementR("p", "paused · Private").MustVisible()
	current, err := repo.Get(ownerCtx, b.ID)
	if err != nil || current.Status != store.BotPaused {
		t.Fatalf("pause not persisted: %+v %v", current, err)
	}
	if downloaded := request("GET", artifactURL+"/download", "", http.StatusOK); string(downloaded) != preview.Content {
		t.Fatalf("paused offline artifact download differs: %q", downloaded)
	}
	request("POST", path+"/runs", fmt.Sprintf(`{"version":%d,"instructions":"must not run","idempotency_key":"paused-browser"}`, current.Version), http.StatusForbidden)
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before || effects.Load() != 1 {
		t.Fatal("pause allowed additional execution")
	}
	// Paused text stays on the home watch and is mirrored by the production runtime.
	textOnly.Store(true)
	if !page.MustElement(`[aria-label="Bot workspace"] button[title="Attach files (images, PDFs, text files)"]`).MustProperty("disabled").Bool() {
		t.Error("paused conversation allows attachments")
	}
	pausedBaseline, err := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	priorRunIDs := make(map[uuid.UUID]bool, len(pausedBaseline))
	for _, prior := range pausedBaseline {
		priorRunIDs[prior.ID] = true
	}
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput("Paused browser owner question")
	page.MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	deadline = time.Now().Add(15 * time.Second)
	var conversation store.BotRun
	for {
		admitted, e := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
		if e != nil {
			t.Fatal(e)
		}
		for _, candidate := range admitted {
			if candidate.Intent == store.BotIntentConversation && !priorRunIDs[candidate.ID] {
				conversation = candidate
			}
		}
		if conversation.ID != uuid.Nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("paused browser send did not admit conversation")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	conversationResult, err := repo.GetRun(ownerCtx, b.ID, conversation.ID)
	if err != nil || conversationResult.Status != store.RunSucceeded {
		t.Fatalf("conversation: %+v %v", conversationResult, err)
	}
	page.MustElementR(`[aria-label="Bot workspace"] .markdown-body p`, "^Paused browser conversational reply\\.$").MustVisible()
	assertConversation := func() {
		t.Helper()
		if got := page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] .markdown-body p')].filter(p => p.textContent === 'Paused browser conversational reply.').length`).Int(); got != 1 {
			t.Errorf("conversation reply painted %d times; DOM=%s", got, page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] .markdown-body p')].filter(p => p.textContent === 'Paused browser conversational reply.').map(p => p.parentElement.outerHTML).join('\n')`).Str())
		}
		if strings.Contains(page.MustEval(`() => location.hash`).Str(), "/runs/") {
			t.Fatal("conversation navigated off home")
		}
		assertDisabled(store.BotPaused)
		events, e := personal.Sessions().ReadTranscriptEvents(ownerCtx, "astonish", owner, b.HomeSessionID.String())
		if e != nil {
			t.Fatal(e)
		}
		var users, replies int
		for _, event := range events {
			if event.Content != nil {
				for _, part := range event.Content.Parts {
					if part.Text == "Paused browser owner question" {
						users++
					}
					if part.Text == "Paused browser conversational reply." {
						replies++
					}
				}
			}
		}
		if users != 1 || replies != 1 {
			t.Fatalf("durable conversation duplicates: user=%d reply=%d", users, replies)
		}
		if effects.Load() != 1 {
			t.Fatal("conversation executed a tool")
		}
	}
	assertConversation()
	conversationCalls := calls.Load()
	page.MustReload()
	page.MustElementR(`[aria-label="Bot workspace"] .markdown-body p`, "^Paused browser conversational reply\\.$").MustVisible()
	assertConversation()
	if calls.Load() != conversationCalls {
		t.Fatal("conversation reload reran model")
	}
	if t.Failed() {
		return // Keep live/reload diagnostics without printing success on a failed gate.
	}
	t.Log("paused owner text: real worker/Chat reply live and exactly once after reload; durable user/reply each once; remained paused with disabled schedule; zero additional tool calls")
	t.Logf("PASS real scheduler → scoped daemon worker/ChatRunner → live Studio home approval report → reload → exact UI approve-and-continue → same-session higher-fence success → home report and cron run evidence → reload without model/tool activity → UI pause/reload and denied admission; run=%s effects=%d calls=%d (controlled model/local tool/home binding; no physical sandbox or external delivery claim)", runID, effects.Load(), calls.Load())
}
