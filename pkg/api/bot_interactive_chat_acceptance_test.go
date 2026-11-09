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
	"strings"
	"testing"
	"time"

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
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type interactiveHTTPModel struct {
	name      string
	args      map[string]any
	called    bool
	principal execution.Principal
	t         *testing.T
}

func (*interactiveHTTPModel) Name() string { return "interactive-http" }
func (m *interactiveHTTPModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		p, _ := execution.PrincipalFromContext(ctx)
		if p.Subject != m.principal.Subject || p.Authentication != m.principal.Authentication || p.Surface != m.principal.Surface {
			m.t.Errorf("provider lost original principal: %+v", p)
		}
		part := &genai.Part{Text: "Observed scoped result."}
		if !m.called {
			m.called = true
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: m.name, Args: m.args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}

type interactiveHTTPPool struct {
	sandbox.ToolNodePool
	dir   string
	calls int
}

func (p *interactiveHTTPPool) SetBotHome(string, *sandbox.BotHomeBinding) error { return nil }
func (p *interactiveHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient        { return p }
func (p *interactiveHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *interactiveHTTPPool) SetSessionScope(string, string, string) {}
func (p *interactiveHTTPPool) BindSession(string)                     {}
func (p *interactiveHTTPPool) EnsureReady(string) error               { return nil }
func (p *interactiveHTTPPool) Call(_ string, name string, args map[string]interface{}) (json.RawMessage, error) {
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
	db       *entstore.Store
	server   *httptest.Server
	scope    store.BotScope
	t        *testing.T
	ctx      context.Context
	request  func(string, string, string, int) []byte
	worker   *daemon.BotWorker
	repo     store.BotStore
	b        store.Bot
	path     string
	pool     *interactiveHTTPPool
	registry *execution.BotTurnAuthorizationRegistry
	model    *interactiveHTTPModel
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
	router.HandleFunc(root+"/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/activate", api.BotActivateHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/pause", api.BotPauseHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	router.HandleFunc("/api/studio/sessions/{id}/model", api.PatchSessionModelHandler).Methods("PATCH")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if err = json.Unmarshal(request("POST", root, `{"name":"Interactive","idempotency_key":"create"}`, 201), &b); err != nil {
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
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: journeyHomeBackend{}, Pool: pool, TurnRegistry: registry, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if len(q.Version.Autonomy.AllowedTools) != 0 {
			t.Error("test must retain empty autonomy")
		}
		if q.Principal.Authentication != principal.Authentication || q.Principal.Subject != owner {
			t.Errorf("lost original caller %+v", q.Principal)
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &interactiveHTTPFixture{db: db, server: server, scope: scope, t: t, ctx: ctx, request: request, worker: worker, repo: repo, b: b, path: root + "/" + b.ID.String(), pool: pool, registry: registry, model: mdl}
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
func TestBotInteractiveChatHTTPAcceptance(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprint("paused=", paused), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"})
			if paused {
				if err := json.Unmarshal(f.request("POST", f.path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"explicit setup"}`, f.b.Revision), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(f.request("POST", f.path+"/pause", fmt.Sprintf(`{"expected_revision":%d,"reason":"pause before chat"}`, f.b.Revision), 200), &f.b); err != nil {
					t.Fatal(err)
				}
			}
			run := f.turn()
			if f.pool.calls != 1 || run.Status != store.RunSucceeded || len(run.Report.Unverified) != 0 || len(run.Report.Unchanged) != 1 || run.Usage.ToolCalls != 1 {
				t.Fatalf("tool runtime calls=%d run=%+v", f.pool.calls, run)
			}
		})
	}
}
func TestBotInteractivePersonalHTTPAcceptance(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	run := f.turn()
	if f.pool.calls != 1 || run.Status != store.RunSucceeded || len(run.Report.Unverified) != 0 || len(run.Report.Unchanged) != 1 || run.Usage.ToolCalls != 1 {
		t.Fatalf("personal runtime calls=%d run=%+v", f.pool.calls, run)
	}
}

func TestBotInteractiveMissingRegistryHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, scope)
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
	f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat"})
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
