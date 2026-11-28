package daemon

import (
	"context"
	"encoding/json"
	"google.golang.org/adk/session"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"iter"
	"sync/atomic"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

// Transport-only ceiling: real Chat write/shell implementations may touch only
// this test's PLAN.md and run one literal command. This is not physical isolation.
type preparedAdmissionHTTPPool struct {
	intentHTTPPool
	mu       sync.Mutex
	root     string
	sessions map[string]bool
	calls    []string
}

func (p *preparedAdmissionHTTPPool) GetBackend() sandbox.Backend               { return nil }
func (p *preparedAdmissionHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *preparedAdmissionHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *preparedAdmissionHTTPPool) Call(s, n string, a map[string]any) (json.RawMessage, error) {
	return p.CallContext(context.Background(), s, n, a)
}
func (p *preparedAdmissionHTTPPool) CallContext(c context.Context, s, n string, a map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.sessions[s] {
		return nil, store.ErrBotForbidden
	}
	switch n {
	case "write_file":
		if a["path"] != filepath.Join(p.root, "PLAN.md") {
			return nil, store.ErrBotForbidden
		}
	case "shell_command":
		if a["command"] != "printf prepared-http-ok" || a["working_dir"] != p.root {
			return nil, store.ErrBotForbidden
		}
	default:
		return nil, store.ErrBotForbidden
	}
	p.calls = append(p.calls, n)
	out, e := tools.ExecuteTool(c, n, a, s)
	if e != nil {
		return nil, e
	}
	return json.Marshal(out)
}
func (p *preparedAdmissionHTTPPool) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type preparedAdmissionHTTPModel struct {
	configuredAdmissionModel
	generations atomic.Int32
}

func (m *preparedAdmissionHTTPModel) GenerateContent(c context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.generations.Add(1)
	return m.configuredAdmissionModel.GenerateContent(c, r, stream)
}

// Real owner HTTP admission -> production worker -> private prepared engine ->
// owner HTTP exact approval -> fresh worker lease -> actual ordinary write tool.
// No fabricated checkpoint/grant, direct adapter invocation, or paid provider.
// Initial Plan mode is admitted and persisted through the HTTP request, not
// injected by the constructor or worker. Admission, decisions, resume,
// membership checks and all continuations are real HTTP/worker paths. The
// companion owner-consent case executes the complete
// definition -> verifier -> Results lifecycle without injected approval.
func TestBotPreparedPlanAdmissionHTTP(t *testing.T) { testBotPreparedPlanAdmissionHTTP(t, false) }

func TestBotOwnerPlanConsentHTTP(t *testing.T) { testBotPreparedPlanAdmissionHTTP(t, true) }

func TestPrivateWorkerPlanModeHTTP(t *testing.T) {
	testBotPreparedPlanAdmissionHTTPInitial(t, false, true)
}

func testBotPreparedPlanAdmissionHTTP(t *testing.T, interactive bool) {
	testBotPreparedPlanAdmissionHTTPInitial(t, interactive, false)
}

func TestPrivateWorkerNormalModeHTTP(t *testing.T) {
	testBotPreparedPlanAdmissionHTTPMode(t, false, true, false)
}

func testBotPreparedPlanAdmissionHTTPInitial(t *testing.T, interactive, initialOnly bool) {
	testBotPreparedPlanAdmissionHTTPMode(t, interactive, initialOnly, true)
}

func testBotPreparedPlanAdmissionHTTPMode(t *testing.T, interactive, initialOnly, planning bool) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			db, owner := setupChannelResolverFixture(t, "admission", "team", "telegram", "linked")
			org, e := db.ForOrg("admission")
			must(e)
			team, personal := org.ForTeam("team"), org.ForUser(owner)
			repo, ss, pin := team.Bots(), team.Sessions(), team.SetSessionPin
			if scope == store.BotScopePersonal {
				repo, ss, pin = personal.Bots(), personal.Sessions(), personal.SetSessionPin
			}
			principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "admission", TeamSlug: "team", Authenticated: true}
			auth, e := execution.WithPrincipal(t.Context(), principal)
			must(e)
			auth, e = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			must(e)
			disabled := false
			for _, standard := range config.GetStandardServers() {
				if standard.Kind != "model" {
					must(db.PlatformMCPServers().Save(auth, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}))
				}
			}
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			home := testRetainedContentBackend(t, db)
			pool := &preparedAdmissionHTTPPool{root: t.TempDir(), sessions: map[string]bool{}}
			announce := map[string]any{"goal": "HTTP exact prepared plan", "context": "Test-owned prepared plan", "what_not_to_do": "No unrelated commands or files", "verification": "Observe prepared-http-ok", "steps": []any{map[string]any{"name": "verify", "description": "Verify scope", "details": "Execute the one bounded verifier", "summary": "Observe actual command output", "outcome": "prepared-http-ok", "files": []any{map[string]any{"path": "PLAN.md", "kind": "new"}}, "verify": "printf prepared-http-ok", "verify_kind": "behavior"}}}
			llm := &preparedAdmissionHTTPModel{configuredAdmissionModel: configuredAdmissionModel{script: []configuredAdmissionCall{{"announce_plan", announce}}}}
			worker, e := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: home, Pool: workerTestPool{}, WorkerID: "prepared-http", TurnRegistry: registry, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mediateDaemonModel(llm), nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				pool.mu.Lock()
				pool.sessions[q.Run.SessionID.String()] = true
				pool.mu.Unlock()
				s.Services.BotFilesFactory = func(c context.Context, s *store.Services, sc store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, home, s, sc)
				}
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, WorkspaceDir: pool.root, PolicyRuntime: productionBotPolicyRuntime(botLiveGuard{repo: repo, request: q}), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			must(e)
			defer worker.Stop()
			v := store.DefaultBotVersion()
			v.Charter = "Announce a plan only; request exact owner approval before writing it"
			v.Autonomy.AllowedTools = []string{"announce_plan", "update_plan", "announce_completion", "write_file", "shell_command"}
			v.Budget.MaxTokens = nil
			b, e := repo.Create(auth, store.BotCreateRequest{Name: "Prepared HTTP", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "prepared-http"})
			must(e)
			b, e = worker.provisionHome(auth, repo, b.ID, b.Revision)
			must(e)
			must(pin(auth, b.HomeSessionID.String(), "controlled", "model"))
			if interactive {
				b, e = repo.Get(auth, b.ID)
				must(e)
			}
			must(pin(auth, b.HomeSessionID.String(), "controlled", "model"))
			svc := &store.Services{Mode: store.ModePlatform, Platform: db, TenantRouter: db, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), BotTurnRegistry: registry, BotInteractiveReady: true}
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/runs/{runID}/continue-interactive", api.BotContinueInteractiveHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/test-run", api.BotTestRunHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler).Methods("POST")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c := auth
				subject := owner
				if r.Header.Get("X-Foreign") != "" {
					subject = uuid.NewString()
					foreign := principal
					foreign.Subject = subject
					var err error
					c, err = execution.WithPrincipal(c, foreign)
					if err != nil {
						http.Error(w, "identity", 500)
						return
					}
				}
				c = store.WithServices(c, svc)
				c = store.WithTenantContext(c, &store.TenantContext{UserID: subject, OrgSlug: "admission", TeamSlug: "team"})
				router.ServeHTTP(w, r.WithContext(c))
			}))
			defer server.Close()
			server.Client().Timeout = 30 * time.Second
			postAs := func(path string, body any, want int, foreign bool) []byte {
				t.Helper()
				raw, e := json.Marshal(body)
				must(e)
				request, e := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/bots/"+b.ID.String()+"/"+path+"?scope="+string(scope), strings.NewReader(string(raw)))
				must(e)
				request.Header.Set("Content-Type", "application/json")
				if foreign {
					request.Header.Set("X-Foreign", "1")
				}
				response, e := server.Client().Do(request)
				must(e)
				defer response.Body.Close()
				out, e := io.ReadAll(response.Body)
				must(e)
				if response.StatusCode != want {
					t.Fatalf("%s: %d want %d: %s", path, response.StatusCode, want, out)
				}
				return out
			}
			post := func(path string, body any, want int) []byte { return postAs(path, body, want, false) }
			payload := map[string]any{"version": b.Version, "idempotency_key": "announce-http", "instructions": v.Charter, "planMode": planning}
			var admitted store.BotRun
			endpoint := "test-run"
			if interactive {
				endpoint = "messages"
				var receipt store.BotMessageReceipt
				must(json.Unmarshal(post(endpoint, payload, 202), &receipt))
				admitted = receipt.Run
			} else {
				must(json.Unmarshal(post(endpoint, payload, 202), &admitted))
			}
			poll := func() {
				t.Helper()
				must(worker.Poll(auth))
			}
			get := func() *store.BotRun { t.Helper(); r, e := repo.GetRun(auth, b.ID, admitted.ID); must(e); return r }
			poll() // mode comes only from the durable HTTP request, never worker context
			pending := get()
			if !planning {
				approvals, err := repo.ListApprovals(auth, b.ID, store.BotPage{})
				must(err)
				if pending.Status == store.RunWaitingApproval || len(approvals) != 0 || pool.count() != 0 {
					t.Fatalf("Normal mode admitted announce_plan: run=%+v approvals=%+v calls=%d", pending, approvals, pool.count())
				}
				llm.mu.Lock()
				responses := strings.Join(llm.responses, "\n")
				llm.mu.Unlock()
				if !strings.Contains(responses, "blocked_announce_plan_not_in_plan_mode") {
					t.Fatalf("Normal mode did not report the runtime mode gate: %s", responses)
				}
				return
			}
			approvals, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
			must(e)
			if pending.Status != store.RunWaitingApproval || len(approvals) != 1 || (!interactive && pool.count() != 0) {
				t.Fatalf("unsafe initial: run=%+v approvals=%+v calls=%d responses=%v", pending, approvals, pool.count(), llm.responses)
			}
			if initialOnly {
				if approvals[0].Action.Tool != "write_file" || pool.count() != 0 {
					t.Fatal("Plan mode did not privately prepare the exact mirror")
				}
				return
			}
			if interactive {
				testOwnerDefinitionConsentHTTP(t, auth, repo, ss, b, admitted, pool, llm, post, poll, func(path string, body any) { postAs(path, body, 403, true) }, func() {
					registry.Cancel(execution.BotRunIdentity{BotID: b.ID, RunID: admitted.ID, Version: b.Version, ActorID: owner, OrgSlug: "admission", TeamSlug: "team"})
				})
				return
			}
			approval := approvals[0]
			cp, e := bot.DecodeApprovalCheckpoint(pending.Checkpoint, *pending)
			must(e)
			if !cp.Matches(approval) || approval.Action.Tool != "write_file" {
				t.Fatal("not exact prepared mirror checkpoint")
			}
			if _, e := os.Stat(filepath.Join(pool.root, "PLAN.md")); !os.IsNotExist(e) {
				t.Fatalf("unapproved mirror exists: %v", e)
			}
			path := "approvals/" + approval.ID.String() + "/approve-and-continue"
			post(path, map[string]any{"action_digest": "altered"}, 409)
			post(path, map[string]any{"action_digest": approval.ActionDigest, "arguments": map[string]any{"content": "replacement"}}, 400)
			// Decision alone cannot initiate a worker continuation.
			post("approvals/"+approval.ID.String()+"/decision", map[string]any{"action_digest": approval.ActionDigest, "status": "approved"}, 200)
			poll()
			if get().Status != store.RunWaitingApproval || pool.count() != 0 {
				t.Fatal("decision alone executed")
			}
			for i := 0; i < 2; i++ {
				var receipt store.BotApproveContinueResult
				must(json.Unmarshal(post(path, map[string]any{"action_digest": approval.ActionDigest}, 202), &receipt))
				if receipt.RunID != admitted.ID {
					t.Fatal("new logical run on approval")
				}
			}
			orgRow, e := db.Organizations().GetBySlug(t.Context(), "admission")
			must(e)
			must(db.Organizations().RemoveMember(t.Context(), owner, orgRow.ID))
			poll()
			if pool.count() != 0 {
				t.Fatal("revoked approved owner executed")
			}
			must(db.Organizations().AddMember(t.Context(), owner, orgRow.ID, "member"))
			beforeGenerations := llm.generations.Load()
			llm.mu.Lock()
			before := llm.step
			llm.mu.Unlock()
			poll()
			final := get()
			if final.Status != store.RunPartial && final.Status != store.RunSucceeded {
				t.Fatalf("continuation failed: %+v failure=%+v calls=%d", final, final.Report.Failure, pool.count())
			}
			if final.ID != pending.ID || final.SessionID != pending.SessionID || final.Version != pending.Version || final.Intent != pending.Intent || final.Lease.Fence <= pending.Lease.Fence || pool.count() != 1 {
				t.Fatalf("identity/fence/replay: %+v calls=%d", final, pool.count())
			}
			llm.mu.Lock()
			after := llm.step
			llm.mu.Unlock()
			if after != before || llm.generations.Load() != beforeGenerations {
				t.Fatal("prepared continuation invoked script/model tools")
			}
			var args map[string]any
			must(json.Unmarshal(approval.Action.Arguments, &args))
			data, e := os.ReadFile(filepath.Join(pool.root, "PLAN.md"))
			must(e)
			if string(data) != args["content"] {
				t.Fatal("mirror differs from approved bytes")
			}
			sess, e := ss.Get(auth, &session.GetRequest{AppName: "astonish", UserID: owner, SessionID: final.SessionID.String()})
			must(e)
			value, e := sess.Session.State().Get(agent.PlanSnapshotStateKey)
			must(e)
			raw, e := json.Marshal(value)
			must(e)
			var snap agent.PlanSnapshot
			must(json.Unmarshal(raw, &snap))
			if snap.PlanID == "" || snap.Document.Results != "" || snap.Steps[0].Status != "pending" {
				t.Fatalf("unsafe announcement state: %+v", snap)
			}
			post(path, map[string]any{"action_digest": approval.ActionDigest}, 409)
			post("runs/"+admitted.ID.String()+"/resume", map[string]any{"approval_id": approval.ID}, 409)
			post("test-run", payload, 202)
			poll()
			poll()
			if pool.count() != 1 {
				t.Fatal("consumed prepared mirror replayed")
			}
			t.Logf("HTTP admission/decision/approve-and-continue -> worker fence %d -> %d -> exact PLAN.md once (%d bytes); no invented Results", pending.Lease.Fence, final.Lease.Fence, len(data))
		})
	}
}
