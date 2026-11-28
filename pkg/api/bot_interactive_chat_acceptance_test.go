package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// canonicalHTTPText is the public Chat text payload, not the persisted ADK event.
// Projection metadata may repeat invocationId; it must never count as delivery.
type canonicalHTTPText struct {
	InvocationID string `json:"invocationId"`
	Text         string `json:"text"`
}

func canonicalHTTPTextEvents(t *testing.T, body string) []canonicalHTTPText {
	t.Helper()
	r := bufio.NewReader(strings.NewReader(body))
	var texts []canonicalHTTPText
	for {
		if _, err := r.Peek(1); err == io.EOF {
			return texts
		} else if err != nil {
			t.Fatal(err)
		}
		frame := readBlockedPublicationFrame(t, r)
		if frame.kind != "text" {
			continue
		}
		var text canonicalHTTPText
		if err := json.Unmarshal([]byte(frame.data), &text); err != nil {
			t.Fatalf("invalid canonical text payload: %v", err)
		}
		texts = append(texts, text)
	}
}

func canonicalHTTPReportCount(t *testing.T, body, invocationID string) int {
	t.Helper()
	count := 0
	for _, event := range canonicalHTTPTextEvents(t, body) {
		if event.InvocationID == invocationID {
			count++
		}
	}
	return count
}

func canonicalHTTPTextCount(t *testing.T, body, text string) int {
	t.Helper()
	count := 0
	for _, event := range canonicalHTTPTextEvents(t, body) {
		if event.Text == text {
			count++
		}
	}
	return count
}

func TestCanonicalHTTPPublicationCounts(t *testing.T) {
	body := "event: text\ndata: {\"invocationId\":\"bot-report:run\",\"text\":\"Reviewed answer\",\"_projection\":{\"turn_id\":\"bot-report:run\"}}\n\n" +
		"event: bot_run_state\ndata: {\"invocationId\":\"bot-report:run\",\"text\":\"Reviewed answer\"}\n\n"
	if got := canonicalHTTPReportCount(t, body, "bot-report:run"); got != 1 {
		t.Fatalf("metadata/non-text event counted as delivery: %d", got)
	}
	if got := canonicalHTTPTextCount(t, body, "Reviewed answer"); got != 1 {
		t.Fatalf("non-text event counted as answer: %d", got)
	}
	if got := canonicalHTTPReportCount(t, body+body, "bot-report:run"); got != 2 {
		t.Fatalf("duplicate delivery hidden: %d", got)
	}
	if got := canonicalHTTPReportCount(t, body, "bot-report:other"); got != 0 {
		t.Fatalf("another run counted as delivery: %d", got)
	}
}

type interactiveHTTPModel struct {
	chatOnly    bool
	deliverySet bool
	qualityMode string
	productions int
	reviews     int
	assessment  func(*model.LLMRequest) (*model.LLMResponse, bool)
	work        func() *store.BotWorkState
	name        string
	args        map[string]any
	called      bool
	principal   execution.Principal
	validatePrincipal func(context.Context, *model.LLMRequest, execution.Principal)
	t           *testing.T
}

func (*interactiveHTTPModel) Name() string { return "interactive-http" }
func (m *interactiveHTTPModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Follow the shared Chat catalog: deferred tools use its real bridge,
		// while declared tools and tool-free review requests remain unchanged.
		originalYield := yield
		yield = func(response *model.LLMResponse, err error) bool {
			return originalYield(scriptedHTTPProgressiveResponse(req, response), err)
		}
		p, _ := execution.PrincipalFromContext(ctx)
		if m.validatePrincipal != nil {
			m.validatePrincipal(ctx, req, p)
		} else if p.Subject != m.principal.Subject || p.Authentication != m.principal.Authentication || p.Surface != m.principal.Surface {
			m.t.Errorf("provider lost original principal: %+v", p)
		}
		if response, ok := scriptedEmployeeReview(req); ok {
			m.reviews++
			yield(response, nil)
			return
		}
		if m.assessment != nil {
			if response, ok := m.assessment(req); ok {
				fixtureHTTPContentCoverage(req, response)
				yield(response, nil)
				return
			}
		}
		// Conversational fixtures declare delivery through the same optional
		// model tool as production, rather than bypassing file recovery.
		if m.chatOnly && !m.deliverySet {
			m.deliverySet = true
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "bot_work_update", Args: map[string]any{"expected_revision": m.work().Revision, "kind": "set_delivery", "delivery_intent": string(store.BotDeliveryChatOnly)}}}}}}, nil)
			return
		}
		part := &genai.Part{Text: "Observed scoped result."}
		if m.qualityMode != "" {
			m.productions++
			part.Text = "Defective: the receipt says imaginary."
			if m.productions > 1 && m.qualityMode != "limits" {
				part.Text = "Corrected: the receipt says isolated receipt."
			}
			m.called = true
		}
		if !m.called {
			m.called = true
			args := m.args
			if m.name == "read_file" {
				args = make(map[string]any, len(m.args))
				for k, v := range m.args {
					args[k] = v
				}
				if path, ok := args["path"].(string); ok {
					args["path"] = strings.TrimPrefix(path, "/isolated/")
				}
				if args["path"] == nil {
					args["path"] = "receipt.txt"
				}
			}
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: m.name, Args: args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}

type interactiveHTTPPool struct {
	sandbox.ToolNodePool
	dir          string
	calls        int
	callOverride func(string, map[string]interface{}) (json.RawMessage, error)
}

// Controlled transport has no credential push backend; never dispatch through
// the nil embedded pool when a network tool checks this optional capability.
func (p *interactiveHTTPPool) GetBackend() sandbox.Backend                      { return nil }
func (p *interactiveHTTPPool) SetBotHome(string, *sandbox.BotHomeBinding) error { return nil }
func (p *interactiveHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient        { return p }
func (p *interactiveHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *interactiveHTTPPool) SetSessionScope(string, string, string) {}
func (p *interactiveHTTPPool) BindSession(string)                     {}
func (p *interactiveHTTPPool) EnsureReady(string) error               { return nil }
func (p *interactiveHTTPPool) Call(_ string, name string, args map[string]interface{}) (json.RawMessage, error) {
	if p.callOverride != nil {
		p.calls++
		return p.callOverride(name, args)
	}
	if name != "read_file" {
		return nil, fmt.Errorf("unexpected node call %s", name)
	}
	p.calls++
	body, err := os.ReadFile(filepath.Join(p.dir, "receipt.txt"))
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": string(body)})
}

type interactiveHTTPFixture struct {
	policy        *store.BotMandatePolicyResolver
	db            *entstore.Store
	dataDir       string // owned temporary SQLite fixture, never a live data directory
	server        *httptest.Server
	scope         store.BotScope
	t             *testing.T
	ctx           context.Context
	request       func(string, string, string, int) []byte
	worker        *daemon.BotWorker
	workerConfig  daemon.BotWorkerConfig
	repo          store.BotStore
	b             store.Bot
	path          string
	pool          *interactiveHTTPPool
	registry      *execution.BotTurnAuthorizationRegistry
	requireReview bool
	model         *interactiveHTTPModel
}

func newInteractiveHTTPFixture(t *testing.T, toolName string, args map[string]any, scopes []string, requestedScope ...store.BotScope) *interactiveHTTPFixture {
	scope := store.BotScopeTeam
	if len(requestedScope) > 0 {
		scope = requestedScope[0]
	}
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: "interactive", Name: "Interactive", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
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
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: scopes}
	auth := func(c context.Context) context.Context {
		c, e := execution.WithPrincipal(c, principal)
		if e != nil {
			t.Fatal(e)
		}
		c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		return store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	}
	ctx = auth(ctx)
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	t.Cleanup(registry.Close)
	personal := ods.ForUser(owner)
	repo := ods.ForTeam(team.Slug).Bots()
	if scope == store.BotScopePersonal {
		repo = personal.Bots()
	}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	router.HandleFunc(root, api.BotCreateHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/snapshot", api.BotSnapshotHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/sessions/{sessionID}/events", api.BotEventsHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}", api.BotGetHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/runs", api.BotRequestRunHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/runs/{runID}", api.BotRunHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/configuration", api.BotConfigurationHandler).Methods("PUT")
	router.HandleFunc(root+"/{botID}/resume", api.BotResumeHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/pause", api.BotPauseHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/runs/{runID}/continue-interactive", api.BotContinueInteractiveHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/runs/{runID}/cancel", api.BotCancelRunHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/permanent", api.BotPermanentDeleteHandler).Methods("DELETE")
	router.HandleFunc(root+"/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions/{id}/model", api.PatchSessionModelHandler).Methods("PATCH")
	var policy store.BotMandatePolicyResolver
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policy != nil {
			r = r.WithContext(store.WithBotMandateAdmission(r.Context(), policy))
		}
		svc := &store.Services{TenantRouter: db, Bots: repo, Sessions: ods.ForTeam(team.Slug).Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: registry}
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth(r.Context()), svc)))
	}))
	t.Cleanup(server.Close)
	request := func(method, path, body string, status int) []byte {
		t.Helper()
		path += "?scope=" + string(scope)
		r, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, raw)
		}
		return raw
	}
	var b store.Bot
	if scope == store.BotScopeTeam {
		// Seed retained team data without reopening public team creation.
		v, e := store.NewBotVersionForCreation(ctx, uuid.MustParse(owner))
		if e != nil {
			t.Fatal(e)
		}
		v.Charter = "Take responsibility for the owner's goals and deliver useful, verified outcomes. Perform routine work within your authorized mandate and existing permissions without extra review. Escalate genuine ambiguity or actions outside that scope for clarification or authorization. A request or this charter does not itself grant permission for external side effects."
		seeded, e := repo.Create(ctx, store.BotCreateRequest{Name: "Interactive", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "create"})
		if e != nil {
			t.Fatal(e)
		}
		b = *seeded
	} else if err = json.Unmarshal(request("POST", root, `{"name":"Interactive","idempotency_key":"create"}`, 201), &b); err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}
	serviceCtx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, Actor: "registry", OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	if err = entstore.RegisterBotHomeBinding(serviceCtx, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	request("PATCH", "/api/studio/sessions/"+b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
	pool := &interactiveHTTPPool{dir: dir}
	if err = os.WriteFile(filepath.Join(dir, "receipt.txt"), []byte("isolated receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	mdl := &interactiveHTTPModel{name: toolName, args: args, principal: principal, t: t}
	mdl.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		if req.Config == nil || req.Config.SystemInstruction == nil {
			return nil, false
		}
		prompt := req.Config.SystemInstruction.Parts[0].Text
		var output any
		if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
			output = map[string]any{"kind": "no_skill_needed", "skill_id": "", "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Controlled non-skill fixture"}
		} else if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
			output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "scoped-result", SourceIDs: []string{"assignment", "charter"}, Text: "Use the requested scoped tool and truthfully report its result."}}}
		} else if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
			var work store.BotWorkState
			if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil {
				t.Fatal(err)
			}
			verdict := store.BotCriterionUnverified
			if work.Candidate.Text == "Observed scoped result." && len(work.Candidate.Observations) > 0 {
				verdict = store.BotCriterionSatisfied
			}
			if toolName == "ask_user" && work.Candidate.Text == "Observed scoped result." {
				for key, body := range work.Candidate.Bodies {
					if strings.HasPrefix(key, "tool-response:owner-answer:") && strings.Contains(body, "Quarterly report") {
						verdict = store.BotCriterionSatisfied
					}
				}
			}
			assessments := []store.BotCriterionAssessment{}
			for _, criterion := range work.Criteria {
				if !criterion.Excluded {
					assessments = append(assessments, store.BotCriterionAssessment{CriterionID: criterion.ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Inspected scoped result against observed tool response."})
				}
			}
			output = store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: assessments}
		} else {
			return nil, false
		}
		raw, _ := json.Marshal(output)
		return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
	}
	// Controlled retained Home files satisfy the same mandatory factory and
	// post-run memory IO capabilities used by the synthesis HTTP fixture.
	files := newComposedHTTPFiles(t, dir, repo, b, func(name string, args map[string]interface{}) (json.RawMessage, error) {
		return pool.Call("", name, args)
	})
	mediatedModel := mediateHTTPModel(mdl)
	workerConfig := daemon.BotWorkerConfig{Backend: db, Sandbox: synthesisHTTPBackend{files: files.disk}, Pool: pool, TurnRegistry: registry, WorkerID: uuid.NewString(), ContextWindow: 8192, LeaseTTL: 4 * time.Second, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mediatedModel, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		// Title-start fixtures may create another Bot in this same tenant.
		runFiles := *files
		runFiles.id = q.Bot.ID
		runFiles.install(c, q, s)
		// Capabilities come only from the owner-reviewed HTTP configuration.
		configured, err := repo.GetVersion(c, q.Bot.ID, q.Version.Version)
		if err != nil || !slices.Equal(configured.Autonomy.AllowedTools, q.Version.Autonomy.AllowedTools) {
			return nil, fmt.Errorf("worker did not use persisted capability policy: %v", err)
		}
		if q.Principal.Subject != owner || q.Run.IsInteractiveChat() && q.Principal.Authentication != principal.Authentication || !q.Run.IsInteractiveChat() && q.Principal.Authentication != execution.AuthMethod("daemon_owner") {
			t.Errorf("lost original caller %+v", q.Principal)
		}
		mdl.work = func() *store.BotWorkState {
			work, err := repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease)
			if err != nil {
				t.Fatal(err)
			}
			return work
		}
		resources := &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}
		resources.PolicyRuntime = agent.BotPolicyRuntime{Classifier: composedWeatherClassifier{}, Guard: interactiveFixtureGuard{repo: repo, req: q}}
		if !q.Run.IsInteractiveChat() {
			resources.Tools, err = launcher.BotCatalogueTools(pool, q.Run.SessionID.String())
			if err != nil {
				return nil, err
			}
			mdl.principal = q.Principal
			resources.PolicyRuntime = agent.BotPolicyRuntime{Classifier: composedWeatherClassifier{}, Guard: journeyGuard{repo: repo, req: q}}
		}
		return resources, nil
	}}
	worker, err := daemon.NewBotWorker(workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	// Model selection updates the Bot revision; return the current CAS token.
	updated, err = repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	return &interactiveHTTPFixture{workerConfig: workerConfig, policy: &policy, db: db, dataDir: dir, server: server, scope: scope, t: t, ctx: ctx, request: request, worker: worker, repo: repo, b: b, path: root + "/" + b.ID.String(), pool: pool, registry: registry, model: mdl}
}
func (f *interactiveHTTPFixture) turn() *store.BotRun {
	f.t.Helper()
	var receipt store.BotMessageReceipt
	if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Use the requested tool","idempotency_key":"turn"}`, f.b.Version), 202), &receipt); err != nil {
		f.t.Fatal(err)
	}
	if !receipt.Run.IsInteractiveChat() {
		f.t.Fatal("not interactive admission")
	}
	if f.requireReview {
		pinReviewedHTTPAttempt(f.t, f, receipt.Run)
	}
	if err := f.worker.Poll(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	b, err := f.repo.Get(f.ctx, f.b.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if b.Status != f.b.Status {
		f.t.Fatalf("implicit lifecycle transition %s -> %s", f.b.Status, b.Status)
	}
	return run
}

type capabilityHTTPMode string

const (
	capabilityInteractive capabilityHTTPMode = "interactive"
	capabilityAutonomous  capabilityHTTPMode = "autonomous"
)

// capabilityTurn admits autonomous work only after explicit owner-reviewed grants.
// Interactive runs retain the original caller's authority and do not use these grants.
func (f *interactiveHTTPFixture) capabilityTurn(mode capabilityHTTPMode, tools []string) *store.BotRun {
	f.t.Helper()
	if mode == capabilityInteractive {
		return f.turn()
	}
	v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = json.Unmarshal(f.request("POST", f.path+"/pause", fmt.Sprintf(`{"expected_revision":%d,"reason":"pause for reviewed capability change"}`, f.b.Revision), 200), &f.b); err != nil {
		f.t.Fatal(err)
	}
	v.Autonomy.AllowedTools = tools
	body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated acceptance owner-reviewed capabilities", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
	if err != nil {
		f.t.Fatal(err)
	}
	if err = json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
		f.t.Fatal(err)
	}
	if err = json.Unmarshal(f.request("POST", f.path+"/resume", fmt.Sprintf(`{"expected_revision":%d,"reason":"resume reviewed capability change"}`, f.b.Revision), 200), &f.b); err != nil {
		f.t.Fatal(err)
	}
	var admitted store.BotRun
	if err = json.Unmarshal(f.request("POST", f.path+"/runs", fmt.Sprintf(`{"version":%d,"instructions":"Use the requested tool","idempotency_key":"capabilities"}`, f.b.Version), 202), &admitted); err != nil {
		f.t.Fatal(err)
	}
	if admitted.IsInteractiveChat() {
		f.t.Fatal("autonomous admission unexpectedly interactive")
	}
	if f.requireReview {
		pinReviewedHTTPAttempt(f.t, f, admitted)
	}
	if err = f.worker.Poll(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	run, err := f.repo.GetRun(f.ctx, f.b.ID, admitted.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

func capabilityHTTPCall(mode capabilityHTTPMode, id, name string, args map[string]any) *genai.Part {
	if name == "read_file" {
		copyArgs := make(map[string]any, len(args))
		for key, value := range args {
			copyArgs[key] = value
		}
		if path, ok := copyArgs["path"].(string); ok {
			copyArgs["path"] = strings.TrimPrefix(path, "/isolated/")
		}
		args = copyArgs
	}
	if mode == capabilityAutonomous && (name == "read_file" || name == "web_fetch") {
		args = map[string]any{"name": name, "arguments": args}
		name = "execute_tool"
	}
	return &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}
}

func TestBotAgentProgressHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
			t.Run(string(scope)+"/"+string(mode), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
				defer f.worker.Stop()
				step := 0
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					if req.Config != nil && req.Config.SystemInstruction != nil && strings.HasPrefix(req.Config.SystemInstruction.Parts[0].Text, "BOT_") {
						t.Fatal("unexpected mandatory model call")
					}
					step++
					part := &genai.Part{Text: "Checking the alternate source remains a progress note, not a verified result."}
					switch step {
					case 1:
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "progress-note", Name: "bot_work_update", Args: map[string]any{"expected_revision": 1, "kind": "replace_notes", "notes": "Checking the alternate source"}}}
					case 2:
						raw, _ := json.Marshal(req.Contents)
						if !strings.Contains(string(raw), `"status":"ok"`) {
							t.Fatalf("progress update failed: %s", raw)
						}
						part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "read-progress", Name: "bot_work_read", Args: map[string]any{}}}
					case 3:
						raw, _ := json.Marshal(req.Contents)
						if !strings.Contains(string(raw), "Checking the alternate source") {
							t.Fatal("committed notes not returned")
						}
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
				}
				f.requireReview = true
				run := f.capabilityTurn(mode, []string{"bot_work_update", "bot_work_read"})
				if run.Usage.InputTokens != 20 || run.Usage.OutputTokens != 12 || run.Usage.ToolCalls != 2 {
					t.Fatalf("progress usage not charged: %+v", run.Usage)
				}
				if run.Status != store.RunSucceeded || run.Report.Work == nil || run.Report.Work.WorkRevision < 3 || step != 3 || f.model.reviews != 1 {
					t.Fatalf("progress lost at final capture: %+v steps=%d", run, step)
				}
				history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
				if canonicalHTTPReportCount(t, history, "bot-report:"+run.ID.String()) != 1 {
					t.Fatal("missing/duplicate final publication")
				}
				progressHistory := string(f.request("GET", f.path+"/sessions/"+run.SessionID.String()+"/stream", "", 200))
				if !strings.Contains(progressHistory, `"work_revision":2`) || !strings.Contains(progressHistory, fmt.Sprintf(`"work_revision":%d`, run.Report.Work.WorkRevision)) {
					t.Fatalf("progress and final revisions missing in run history: %s", progressHistory)
				}
			})
		}
	}
}

func TestBotAgentCheckHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
			for _, broken := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/broken=%t", scope, mode, broken), func(t *testing.T) {
					f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
					defer f.worker.Stop()
					step, checks := 0, 0
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						prompt := ""
						if req.Config != nil && req.Config.SystemInstruction != nil {
							prompt = req.Config.SystemInstruction.Parts[0].Text
						}
						if strings.HasPrefix(prompt, "BOT_OPTIONAL_CHECK_V1") {
							checks++
							raw, _ := json.Marshal(req.Contents)
							if !strings.Contains(string(raw), "isolated receipt") || !strings.Contains(string(raw), "capture:source-read") {
								t.Fatal("check missing captured source")
							}
							part := &genai.Part{Text: "The source says isolated receipt, not imaginary; correct the draft."}
							if broken {
								part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "forbidden-check-action", Name: "read_file", Args: map[string]any{"path": "/not-allowed"}}}
							}
							return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 11, CandidatesTokenCount: 7, TotalTokenCount: 18}}, true
						}
						step++
						part := &genai.Part{Text: "The source says isolated receipt."}
						switch step {
						case 1:
							part = capabilityHTTPCall(mode, "source-read", "read_file", map[string]any{"path": "receipt.txt"})
						case 2:
							part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "inspect-source", Name: "bot_evidence_read", Args: map[string]any{"call_id": "source-read"}}}
						case 3:
							raw, _ := json.Marshal(req.Contents)
							if !strings.Contains(string(raw), `"total_bytes"`) {
								t.Fatalf("evidence read did not return content: %s", raw)
							}
							part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "optional-check", Name: "bot_check_work", Args: map[string]any{"question": "Does the draft match the source?", "draft_text": "The receipt says imaginary.", "evidence_refs": []string{"capture:source-read"}}}}
						case 4:
							raw, _ := json.Marshal(req.Contents)
							want := "correct the draft"
							if broken {
								want = "Check unavailable"
							}
							if !strings.Contains(string(raw), want) {
								t.Fatalf("check feedback missing: %s", raw)
							}
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
					}
					run := f.capabilityTurn(mode, []string{"read_file", "bot_evidence_read", "bot_check_work"})
					// Four producer turns, one private Chat action/result pair and the
					// explicitly requested optional check share the run ledger.
					if run.Usage.InputTokens != 41 || run.Usage.OutputTokens != 25 || run.Usage.ToolCalls != 3 {
						t.Fatalf("optional check did not share producer budget: %+v", run.Usage)
					}
					if run.Status != store.RunSucceeded || step != 4 || checks != 1 || f.pool.calls != 1 || run.Report.Work == nil || len(run.Report.Work.Checks) != 1 {
						t.Fatalf("check blocked/replayed work: %+v steps=%d checks=%d fetches=%d", run, step, checks, f.pool.calls)
					}
					receipt := run.Report.Work.Checks[0]
					want := store.BotWorkCheckReturned
					if broken {
						want = store.BotWorkCheckFailed
					}
					if receipt.Status != want || receipt.Kind != store.BotWorkCheckModelFeedback || receipt.Judgment != store.BotWorkCheckUnverified {
						t.Fatalf("fabricated certification: %+v", receipt)
					}
				})
			}
		}
	}
}

func TestBotAgentSourceFallbackHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []capabilityHTTPMode{capabilityInteractive, capabilityAutonomous} {
			for _, sourceCase := range []string{"rate-limited", "footer-only"} {
				t.Run(fmt.Sprintf("%s/%s/%s", scope, mode, sourceCase), func(t *testing.T) {
					f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
					defer f.worker.Stop()
					requests := map[string]int{}
					// This replaces only the sandbox node's network transport. Real HTTP,
					// built-in schema/observation, owner policy and journal remain exercised.
					source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests[r.URL.Path]++
						w.Header().Set("Content-Type", "text/plain")
						if r.URL.Path == "/primary" {
							if sourceCase == "rate-limited" {
								w.Header().Set("Retry-After", "3600")
								w.WriteHeader(http.StatusTooManyRequests)
							}
							_, _ = io.WriteString(w, "Copyright and navigation only. No article here.")
							return
						}
						if r.URL.Path != "/alternative" {
							t.Errorf("unexpected source request: %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
							return
						}
						_, _ = io.WriteString(w, "The controlled source reports 42 units; not 99 units.")
					}))
					defer source.Close()
					f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
						url, _ := args["url"].(string)
						if name != tools.BotPublicFetchOperation || (url != source.URL+"/primary" && url != source.URL+"/alternative") {
							return nil, fmt.Errorf("unexpected scoped transport request: %s %v", name, args)
						}
						resp, err := source.Client().Get(url)
						if err != nil {
							return nil, err
						}
						defer resp.Body.Close()
						body, err := io.ReadAll(resp.Body)
						if err != nil {
							return nil, err
						}
						return json.Marshal(map[string]any{"read_executed": true, "response": map[string]any{"url": url, "final_url": url, "status": resp.StatusCode, "content": string(body), "content_type": "text/plain", "truncated": false}})
					}
					step, checks := 0, 0
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						prompt := ""
						if req.Config != nil && req.Config.SystemInstruction != nil {
							prompt = req.Config.SystemInstruction.Parts[0].Text
						}
						raw, _ := json.Marshal(req.Contents)
						if strings.HasPrefix(prompt, "BOT_OPTIONAL_CHECK_V1") {
							checks++
							if !strings.Contains(string(raw), "capture:alternative-source") || !strings.Contains(string(raw), "42 units") {
								t.Fatalf("check lost selected evidence: %s", raw)
							}
							return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "check-must-not-fetch", Name: "web_fetch", Args: map[string]any{"url": source.URL + "/primary"}}}}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 11, CandidatesTokenCount: 7, TotalTokenCount: 18}}, true
						}
						if strings.HasPrefix(prompt, "BOT_") {
							t.Fatal("unexpected mandatory model gate")
						}
						step++
						part := &genai.Part{Text: "The alternative source reports 42 units. The primary source had no usable article; the optional check was unavailable."}
						call := func(id, name string, args map[string]any) {
							part = capabilityHTTPCall(mode, id, name, args)
						}
						switch step {
						case 1:
							call("primary-source", "web_fetch", map[string]any{"url": source.URL + "/primary"})
						case 2:
							if !strings.Contains(string(raw), "Copyright and navigation only") {
								t.Fatalf("primary access not returned: %s", raw)
							}
							call("fallback-note", "bot_work_update", map[string]any{"expected_revision": 1, "kind": "replace_notes", "notes": "Primary source unusable; use a different source without retrying the blocked page."})
						case 3:
							call("alternative-source", "web_fetch", map[string]any{"url": source.URL + "/alternative"})
						case 4:
							call("inspect-alternative", "bot_evidence_read", map[string]any{"call_id": "alternative-source"})
						case 5:
							if !strings.Contains(string(raw), `"total_bytes"`) || !strings.Contains(string(raw), "42 units") {
								t.Fatalf("evidence read lost source: %s", raw)
							}
							call("failed-check", "bot_check_work", map[string]any{"question": "Does the value match?", "draft_text": "99 units", "evidence_refs": []string{"capture:alternative-source"}})
						case 6:
							if !strings.Contains(string(raw), "Check unavailable") {
								t.Fatalf("failed check not returned to producer: %s", raw)
							}
							work := f.model.work()
							if work.AgentWork == nil || len(work.AgentWork.SourceAccess) != 2 {
								t.Fatalf("transport access facts not persisted: %+v", work.AgentWork)
							}
							for _, fact := range work.AgentWork.SourceAccess {
								wantURL, wantStatus, wantHTTP := source.URL+"/alternative", store.BotSourceAccessReturned, http.StatusOK
								if fact.CallID == "primary-source" {
									wantURL = source.URL + "/primary"
									if sourceCase == "rate-limited" {
										wantStatus, wantHTTP = store.BotSourceAccessFailed, http.StatusTooManyRequests
									}
								} else if fact.CallID != "alternative-source" {
									t.Fatalf("unexpected access fact: %+v", fact)
								}
								if fact.OriginalURL != wantURL || fact.FinalURL != wantURL || fact.Status != wantStatus || fact.HTTPStatus != wantHTTP || fact.ObservedAt.IsZero() || len(fact.BodyDigest) != 64 || fact.EvidenceRef != "capture:"+fact.CallID {
									t.Fatalf("incorrect observed access fact: %+v", fact)
								}
								found := false
								for _, ref := range work.AgentWork.Evidence {
									if ref.Ref == fact.EvidenceRef && ref.Digest == fact.BodyDigest {
										found = true
									}
								}
								if !found {
									t.Fatalf("access fact detached from captured evidence: %+v", fact)
								}
							}
						}
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
					}
					f.requireReview = true
					run := f.capabilityTurn(mode, []string{"web_fetch", "bot_work_update", "bot_evidence_read", "bot_check_work"})
					if run.Status != store.RunSucceeded || step != 6 || checks != 1 || f.pool.calls != 2 || requests["/primary"] != 1 || requests["/alternative"] != 1 {
						t.Fatalf("fallback blocked/replayed: run=%+v step=%d checks=%d requests=%v", run, step, checks, requests)
					}
					if run.Usage.InputTokens != 66 || run.Usage.OutputTokens != 40 || run.Usage.ToolCalls != 5 {
						t.Fatalf("fallback/check budget not shared: %+v", run.Usage)
					}
					if run.Report.Work == nil || len(run.Report.Work.Checks) != 1 || run.Report.Work.Checks[0].Status != store.BotWorkCheckFailed || run.Report.Work.Checks[0].Judgment != store.BotWorkCheckUnverified {
						t.Fatalf("failed check certified work: %+v", run.Report.Work)
					}
					if !strings.Contains(run.Report.Summary, "42 units") || strings.Contains(run.Report.Summary, "99 units") {
						t.Fatalf("incorrect final answer: %s", run.Report.Summary)
					}
					path := f.path + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
					history := string(f.request("GET", path, "", 200))
					if canonicalHTTPReportCount(t, history, "bot-report:"+run.ID.String()) != 1 {
						t.Fatal("fallback final not published exactly once")
					}
					if err := f.worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
					if step != 6 || checks != 1 || f.pool.calls != 2 || requests["/primary"] != 1 || requests["/alternative"] != 1 || canonicalHTTPReportCount(t, string(f.request("GET", path, "", 200)), "bot-report:"+run.ID.String()) != 1 {
						t.Fatal("reconciliation replayed completed reads/check/publication")
					}
				})
			}
		}
	}
}

func TestBotAgentWorkHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			for _, mode := range []string{"interactive", "autonomous"} {
				t.Run(mode, func(t *testing.T) {
					f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"}, scope)
					defer f.worker.Stop()
					f.model.called = true // no optional tools are prerequisites to answering
					calls := 0
					f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
						calls++
						if req.Config != nil && req.Config.SystemInstruction != nil {
							prompt := req.Config.SystemInstruction.Parts[0].Text
							for _, gate := range []string{"BOT_WORK_PREPARE", "BOT_WORK_REVIEW", "BOT_SKILL_ORIENT", "BOT_SKILL_LEARN", "BOT_EMPLOYEE_MESSAGE"} {
								if strings.HasPrefix(prompt, gate) {
									t.Fatalf("mandatory model gate invoked: %s", gate)
								}
							}
						}
						return &model.LLMResponse{Content: genai.NewContentFromText("No stock market sources were retrieved; current market news remains unverified.", "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
					}
					var receipt store.BotMessageReceipt
					body := fmt.Sprintf(`{"version":%d,"instructions":"Stock Market News","idempotency_key":"agent-work"}`, f.b.Version)
					if mode == "autonomous" {
						if err := json.Unmarshal(f.request("POST", f.path+"/runs", body, 202), &receipt.Run); err != nil {
							t.Fatal(err)
						}
						if receipt.Run.IsInteractiveChat() {
							t.Fatal("autonomous admission used interactive mode")
						}
					} else if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
						t.Fatal(err)
					}
					if err := f.worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
					run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if run.Status != store.RunSucceeded || calls != 1 || f.pool.calls != 0 || run.Report.Quality != nil || run.Report.Work == nil || run.Report.Work.Disposition != store.AgentWorkAnswered || run.Report.EmployeeMessage == nil || run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || run.Report.Summary != "No stock market sources were retrieved; current market news remains unverified." {
						t.Fatalf("agent execution: calls=%d tools=%d run=%+v", calls, f.pool.calls, run)
					}
					path := f.path + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
					home := string(f.request("GET", path, "", 200))
					if canonicalHTTPReportCount(t, home, "bot-report:"+run.ID.String()) != 1 || canonicalHTTPTextCount(t, home, run.Report.Summary) != 1 {
						t.Fatal("final answer not published exactly once")
					}
					if err := f.worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
					if calls != 1 || f.pool.calls != 0 || canonicalHTTPReportCount(t, string(f.request("GET", path, "", 200)), "bot-report:"+run.ID.String()) != 1 {
						t.Fatal("reconciliation replayed task or publication")
					}
				})
			}
		})
	}
}

func TestBotInteractiveChatHTTPAcceptance(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprint("paused=", paused), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"})
			if paused {
				if err := json.Unmarshal(f.request("POST", f.path+"/pause", fmt.Sprintf(`{"expected_revision":%d,"reason":"pause before chat"}`, f.b.Revision), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				f.model.called = true // Paused conversations must not dispatch private Chat work.
			}
			f.model.chatOnly = true
			run := f.turn()
			if !f.model.deliverySet || run.Report.Summary != "Observed scoped result." || len(run.Report.Artifacts) != 0 {
				t.Fatalf("short conversation changed: %+v", run)
			}
			wantCalls, wantEvidence := 1, 3
			if paused {
				wantCalls, wantEvidence = 0, 1
			}
			if f.pool.calls != wantCalls || run.Status != store.RunSucceeded || len(run.Report.Unverified) != 0 || len(run.Report.Unchanged) != wantEvidence || run.Usage.ToolCalls != int64(wantCalls+1) {
				t.Fatalf("tool runtime calls=%d run=%+v", f.pool.calls, run)
			}
		})
	}
}
func TestBotInteractivePersonalHTTPAcceptance(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	f.model.chatOnly = true
	run := f.turn()
	if !f.model.deliverySet || run.Report.Summary != "Observed scoped result." || len(run.Report.Artifacts) != 0 {
		t.Fatalf("short conversation changed: %+v", run)
	}
	if f.pool.calls != 1 || run.Status != store.RunSucceeded || len(run.Report.Unverified) != 0 || len(run.Report.Unchanged) != 3 || run.Usage.ToolCalls != 2 {
		t.Fatalf("personal runtime calls=%d run=%+v", f.pool.calls, run)
	}
}

func TestBotInteractiveMissingRegistryHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"}, scope)
			var receipt store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read","idempotency_key":"expired"}`, f.b.Version), 202), &receipt); err != nil {
				t.Fatal(err)
			}
			f.registry.Close()
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != store.RunInterrupted || run.ContinuationReason != store.BotContinuationRequired || run.Lease.Fence != 0 || f.model.called {
				t.Fatalf("unsafe expired turn: %+v", run)
			}
		})
	}
}

func TestBotInteractiveAuthorityHTTPAcceptance(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat"})
	run := f.turn()
	if !f.model.called {
		t.Fatalf("authority acceptance never reached provider dispatch: %+v", run)
	}
	if f.pool.calls != 0 {
		t.Fatalf("chat-only caller executed tool: %+v", run)
	}
}
func TestBotInteractiveQuestionHTTPAcceptance(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "ask_user", map[string]any{"kind": "text", "prompt": "Which report?"}, []string{"chat", "tool:execute"})
	run := f.turn()
	if run.Status != store.RunWaitingApproval {
		t.Fatalf("question not durable: %+v", run)
	}
	approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Interaction == nil {
		t.Fatalf("missing typed question: %+v", approvals)
	}
	a := approvals[0]
	body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": "Quarterly report"}})
	f.request("POST", f.path+"/approvals/"+a.ID.String()+"/approve-and-continue", string(body), 202)
	if err = f.worker.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err = f.repo.GetRun(f.ctx, f.b.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunSucceeded || len(run.Report.Unverified) != 0 || len(run.Checkpoint) != 0 {
		t.Fatalf("answer continuation failed: %+v", run)
	}
	approvals, err = f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed {
		t.Fatalf("answer not consumed: %+v %v", approvals, err)
	}
}
