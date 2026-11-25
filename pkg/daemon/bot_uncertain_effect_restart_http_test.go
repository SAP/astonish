package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
)

// The model, sandbox transport/retained Home, isolation callback and exact-action
// classifier are controlled. Catalog, dispatch, HTTP implementation, live guard,
// worker, HTTP handlers and SQLite stores are real. No tool replacement or positive
// outcome is injected. Restart occurs after terminal persistence, not mid-effect.
type uncertainRestartHTTPModel struct {
	workerTestModel
	args map[string]any
	text string
}

func (m *uncertainRestartHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.args != nil {
			args := m.args
			m.args = nil
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "execute_tool", Args: map[string]any{"name": "http_request", "arguments": args}}}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.text, "model"), TurnComplete: true}, nil)
	}
}

type uncertainRestartHTTPClassifier struct {
	identity             execution.BotRunIdentity
	writeURL, receiptURL string
}

func (c uncertainRestartHTTPClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	identity, ok := execution.BotRunIdentityFromContext(ctx)
	var args tools.HttpRequestArgs
	if !ok || identity != c.identity || name != "http_request" || json.Unmarshal(raw, &args) != nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	effect := execution.EffectSend
	switch {
	case args.Method == http.MethodPost && args.URL == c.writeURL:
	case args.Method == http.MethodGet && args.URL == c.receiptURL:
		effect = execution.EffectRead
	default:
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: effect, ConstraintsVerified: true, TargetFingerprint: args.URL}, nil
}

// The test-owned node transport runs the actual node dispatcher, restricted to
// HTTP against the owned listener. It cannot execute any filesystem/shell tool.
type uncertainRestartHTTPPool struct {
	intentHTTPPool
	writeURL, receiptURL string
	calls                atomic.Int64
	lost                 atomic.Int64
	readResult           []byte
}

func (p *uncertainRestartHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *uncertainRestartHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *uncertainRestartHTTPPool) GetBackend() sandbox.Backend { return nil }
func (p *uncertainRestartHTTPPool) Call(sessionID, name string, args map[string]any) (json.RawMessage, error) {
	return p.CallContext(context.Background(), sessionID, name, args)
}
func (p *uncertainRestartHTTPPool) CallContext(ctx context.Context, sessionID, name string, args map[string]any) (json.RawMessage, error) {
	if sessionID == "" || name != "http_request" || !((args["method"] == http.MethodPost && args["url"] == p.writeURL) || (args["method"] == http.MethodGet && args["url"] == p.receiptURL)) {
		return nil, store.ErrBotForbidden
	}
	p.calls.Add(1)
	result, err := tools.ExecuteTool(ctx, name, args, "uncertain-restart-fixture")
	if err != nil {
		if args["method"] == http.MethodPost {
			p.lost.Add(1)
		}
		return nil, err
	}
	raw, err := json.Marshal(result)
	if args["method"] == http.MethodGet {
		p.readResult = append([]byte(nil), raw...)
	}
	return raw, err
}

type uncertainRestartHTTPReceipt struct {
	Key           string `json:"key"`
	PayloadSHA256 string `json:"payload_sha256"`
	EffectCount   int64  `json:"effect_count"`
}

func TestBotUncertainEffectRestartHTTPNoReplay(t *testing.T) {
	// HttpRequest clones DefaultTransport. This nonparallel fixture pins all
	// shared-tool dials to its own loopback listener, with no proxies or DNS.
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			key, body := uuid.NewString(), "one disposable external effect"
			sum := sha256.Sum256([]byte(body))
			wantReceipt := uncertainRestartHTTPReceipt{key, hex.EncodeToString(sum[:]), 1}
			receiptPath := filepath.Join(dir, "receipt.json")
			var posts, gets, effects atomic.Int64
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/effects":
					posts.Add(1)
					raw, err := io.ReadAll(io.LimitReader(r.Body, 1024))
					if err != nil || string(raw) != body || r.Header.Get("Idempotency-Key") != key {
						http.Error(w, "wrong exact action", http.StatusForbidden)
						return
					}
					// Deliberately not idempotent: any replay increments effects and
					// fails the test even if the caller reused its provider key.
					receipt := wantReceipt
					receipt.EffectCount = effects.Add(1)
					raw, err = json.Marshal(receipt)
					if err == nil {
						err = os.WriteFile(receiptPath, raw, 0600)
					}
					if err != nil {
						t.Error(err)
						http.Error(w, "receipt failed", 500)
						return
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close() // No response after committing the effect.
				case r.Method == http.MethodGet && r.URL.Path == "/receipts/"+key:
					gets.Add(1)
					raw, err := os.ReadFile(receiptPath)
					if err != nil {
						http.Error(w, "missing receipt", 404)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				default:
					http.Error(w, "not owned", http.StatusForbidden)
				}
			}))
			defer sink.Close()
			endpoint, err := url.Parse(sink.URL)
			if err != nil {
				t.Fatal(err)
			}
			original := http.DefaultTransport
			transport := &http.Transport{DialContext: func(c context.Context, network, address string) (net.Conn, error) {
				if address != endpoint.Host {
					return nil, fmt.Errorf("non-fixture dial rejected")
				}
				return (&net.Dialer{Timeout: time.Second}).DialContext(c, network, address)
			}}
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = original; transport.CloseIdleConnections() }()

			dbConfig := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
			_, db, err := entstore.NewPlatformServices(ctx, dbConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			org := &store.Organization{ID: uuid.NewString(), Slug: "uncertain-restart", Name: "Uncertain restart", Status: "active"}
			if err := db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			var repo store.BotStore
			var svc *store.Services
			bindStores := func() {
				t.Helper()
				ods, err = db.ForOrg(org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				repo = ods.ForTeam(team.Slug).Bots()
				svc = &store.Services{Bots: repo, Sessions: ods.ForTeam(team.Slug).Sessions()}
				if scope == store.BotScopePersonal {
					repo = ods.ForUser(owner.String()).Bots()
					svc.PersonalBots, svc.PersonalSessions = repo, ods.ForUser(owner.String()).Sessions()
				}
			}
			bindStores()
			backend := newRegressionHomeBackend(t)
			llm := &uncertainRestartHTTPModel{args: map[string]any{"url": sink.URL + "/effects", "method": http.MethodPost, "body": body, "headers": map[string]any{"Idempotency-Key": key}, "timeout": 2, "max_bytes": 1024}, text: "The write outcome is uncertain. Do not replay; reconcile provider key " + key}
			pool := &uncertainRestartHTTPPool{writeURL: sink.URL + "/effects", receiptURL: sink.URL + "/receipts/" + key}
			newWorker := func(id string) *BotWorker {
				t.Helper()
				if err := db.InstallBotContentHomeBackend(backend); err != nil {
					t.Fatal(err)
				}
				w, err := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: backend, Pool: workerTestPool{}, WorkerID: id, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return llm, nil
				}, Prepare: func(_ context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
						return productionBotFiles(c, backend, svc, scope)
					}
					return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: uncertainRestartHTTPClassifier{q.Policy.Identity(), pool.writeURL, pool.receiptURL}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				return w
			}
			worker := newWorker("uncertain-before-restart")
			defer func() { worker.Stop() }()
			auth, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			version := store.DefaultBotVersion()
			version.Charter = "Perform one exact owner-approved write; never replay an uncertain write. Reconciliation requires a separate owner-requested read."
			version.Autonomy.AllowedTools = []string{"http_request"}
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Uncertain restart", OwnerID: owner, Version: version, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			pin := ods.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				pin = ods.ForUser(owner.String()).SetSessionPin
			}
			if err := pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			newServer := func() *httptest.Server {
				httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
				router := mux.NewRouter()
				router.HandleFunc("/bots/{botID}/test-run", api.BotTestRunHandler)
				router.HandleFunc("/bots/{botID}/runs/{runID}", api.BotRunHandler)
				router.HandleFunc("/bots/{botID}/approvals", api.BotApprovalsHandler)
				router.HandleFunc("/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler)
				router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler)
				router.HandleFunc("/bots/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler)
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			}
			server := newServer()
			defer func() { server.Close() }()
			request := func(method, path, payload string, want int) []byte {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+"/bots/"+b.ID.String()+"/"+path+"?scope="+string(scope), strings.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				client := server.Client()
				client.Timeout = 10 * time.Second
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.StatusCode, want, raw)
				}
				return raw
			}
			getRun := func(id store.RunID) store.BotRun {
				t.Helper()
				var r store.BotRun
				if err := json.Unmarshal(request("GET", "runs/"+id.String(), "", 200), &r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			approvals := func() []store.BotApproval {
				t.Helper()
				var result struct {
					Approvals []store.BotApproval `json:"approvals"`
				}
				if err := json.Unmarshal(request("GET", "approvals", "", 200), &result); err != nil {
					t.Fatal(err)
				}
				return result.Approvals
			}
			poll := func() {
				t.Helper()
				if err := worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
			}
			payload := fmt.Sprintf(`{"version":%d,"idempotency_key":"uncertain-write","instructions":"Perform the exact write once after owner approval"}`, b.Version)
			var run store.BotRun
			if err := json.Unmarshal(request("POST", "test-run", payload, 202), &run); err != nil {
				t.Fatal(err)
			}
			poll()
			saved := getRun(run.ID)
			all := approvals()
			if saved.Status != store.RunWaitingApproval || len(all) != 1 || all[0].Status != store.ApprovalPending || posts.Load() != 0 || pool.calls.Load() != 0 {
				t.Fatalf("approval barrier: run=%+v approvals=%+v posts=%d", saved, all, posts.Load())
			}
			approval := all[0]
			checkpoint, err := bot.DecodeApprovalCheckpoint(saved.Checkpoint, saved)
			if err != nil || !checkpoint.Matches(approval) {
				t.Fatalf("exact checkpoint: %+v %v", checkpoint, err)
			}
			var approvedArgs tools.HttpRequestArgs
			if err := json.Unmarshal(approval.Action.Arguments, &approvedArgs); err != nil || approval.Action.Tool != "http_request" || approvedArgs.Body != body || approvedArgs.Headers["Idempotency-Key"] != key || approvedArgs.URL != pool.writeURL {
				t.Fatalf("wrong approved action: %+v %v", approval.Action, err)
			}
			decisionPath := "approvals/" + approval.ID.String() + "/decision"
			decision := fmt.Sprintf(`{"action_digest":%q,"status":"approved"}`, approval.ActionDigest)
			resumePath := "runs/" + run.ID.String() + "/resume"
			resume := fmt.Sprintf(`{"approval_id":%q}`, approval.ID)
			request("POST", decisionPath, decision, 200)
			poll()
			if posts.Load() != 0 || pool.calls.Load() != 0 {
				t.Fatal("decision alone executed write")
			}
			request("POST", resumePath, resume, 202)
			poll()
			saved = getRun(run.ID)
			if posts.Load() != 1 || effects.Load() != 1 || pool.lost.Load() != 1 {
				t.Fatalf("real lost-response effect: posts=%d effects=%d lost=%d run=%+v", posts.Load(), effects.Load(), pool.lost.Load(), saved)
			}
			// Keep this assertion strict: an attempted external send with no
			// response must persist typed uncertainty, not merely generic failure
			// prose. Continue the scenario after failure to exercise no-replay.
			if saved.Status != store.RunPartial || saved.FinishedAt == nil || saved.Report.EffectState != store.BotReportEffectsUncertain || len(saved.Report.Unverified) == 0 || len(saved.Report.Changed) != 0 {
				t.Errorf("lost-response report: status=%s finished=%t effect_state=%q want=%q summary=%q unverified=%v changed=%v", saved.Status, saved.FinishedAt != nil, saved.Report.EffectState, store.BotReportEffectsUncertain, saved.Report.Summary, saved.Report.Unverified, saved.Report.Changed)
			}
			all = approvals()
			if len(all) != 1 || all[0].Status != store.ApprovalConsumed {
				t.Fatalf("approval not durably consumed: %+v", all)
			}
			originalReport := saved.Report
			raw, err := os.ReadFile(receiptPath)
			var receipt uncertainRestartHTTPReceipt
			if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt != wantReceipt {
				t.Fatalf("independent receipt=%s err=%v", raw, err)
			}

			// Reconstruct all storage clients and the worker (not merely its runtime).
			// The retained Home fixture stands in for persistent sandbox disk only.
			server.Close()
			worker.Stop()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			_, db, err = entstore.NewPlatformServices(ctx, dbConfig)
			if err != nil {
				t.Fatal(err)
			}
			bindStores()
			llm = &uncertainRestartHTTPModel{text: "Unexpected replay"}
			pool = &uncertainRestartHTTPPool{writeURL: pool.writeURL, receiptURL: pool.receiptURL}
			worker = newWorker("uncertain-after-restart")
			auth, _, err = worker.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			server = newServer()
			for range 2 {
				request("POST", resumePath, resume, 409)
				request("POST", decisionPath, decision, 409)
				request("POST", "test-run", payload, 202)
				poll()
				restored := getRun(run.ID)
				if restored.Status != saved.Status || !reflect.DeepEqual(restored.Report, originalReport) {
					t.Fatalf("restart changed uncertain run: %+v", restored)
				}
				history := request("GET", "sessions/"+run.SessionID.String()+"/stream", "", 200)
				if !strings.Contains(string(history), "event: done") || !strings.Contains(string(history), "http_request") {
					t.Fatalf("missing durable tool history: %s", history)
				}
			}
			if llm.calls != 0 || pool.calls.Load() != 0 || posts.Load() != 1 || effects.Load() != 1 || gets.Load() != 0 {
				t.Fatal("restart/read/poll/resume replayed or silently reconciled effect")
			}
			all = approvals()
			if len(all) != 1 || all[0].ID != approval.ID || all[0].Status != store.ApprovalConsumed {
				t.Fatalf("restart lost consumed approval: %+v", all)
			}

			// A separate HTTP owner request has a fresh run identity and read-only
			// classifier action. It cannot reuse/repair the consumed write grant.
			llm.args = map[string]any{"url": pool.receiptURL, "method": http.MethodGet, "timeout": 2, "max_bytes": 1024}
			llm.text = "Read-only provider receipt inspected; the original write must not be replayed."
			readPayload := fmt.Sprintf(`{"version":%d,"idempotency_key":"independent-reconciliation","instructions":"Read only the provider receipt for key %s; do not write or retry"}`, b.Version, key)
			var readRun store.BotRun
			if err := json.Unmarshal(request("POST", "test-run", readPayload, 202), &readRun); err != nil {
				t.Fatal(err)
			}
			if readRun.ID == run.ID {
				t.Fatal("reconciliation reused write run")
			}
			// Even a read-only reconciliation must reauthenticate its owner at
			// claim time; the old consumed write approval grants no new authority.
			if err := db.Organizations().RemoveMember(ctx, owner.String(), org.ID); err != nil {
				t.Fatal(err)
			}
			poll()
			if llm.calls != 0 || pool.calls.Load() != 0 || gets.Load() != 0 {
				t.Fatal("revoked reconciliation owner reached model or receipt")
			}
			if err := db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			poll()
			var result tools.HttpRequestResult
			if err := json.Unmarshal(pool.readResult, &result); err != nil || result.StatusCode != 200 || json.Unmarshal([]byte(result.Body), &receipt) != nil || receipt != wantReceipt {
				t.Fatalf("shared-tool receipt read: %s err=%v", pool.readResult, err)
			}
			reconciled := getRun(readRun.ID)
			if reconciled.Status != store.RunSucceeded || len(reconciled.Report.Unverified) != 0 {
				t.Errorf("independent read did not finish: %+v", reconciled)
			}
			if posts.Load() != 1 || effects.Load() != 1 || gets.Load() != 1 || pool.calls.Load() != 1 {
				t.Fatal("reconciliation replayed write")
			}
			if !reflect.DeepEqual(getRun(run.ID).Report, originalReport) {
				t.Fatal("read-only reconciliation rewrote uncertain report")
			}
			all = approvals()
			if len(all) != 1 || all[0].Status != store.ApprovalConsumed {
				t.Fatalf("read altered write authority: %+v", all)
			}
			t.Logf("scope=%s effects=1 write_attempts=1 lost_response=1 reopened_sqlite=true reconstructed_worker=true rejected_resumes=2 reconciliation_gets=1; actual shared catalog and node HTTP dispatcher, controlled sandbox transport", scope)
		})
	}
}
