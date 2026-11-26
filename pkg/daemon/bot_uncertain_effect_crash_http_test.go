package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
)

// The subprocess receives only disposable fixture coordinates, never daemon or
// provider configuration. Its only external tool is the shared HTTP dispatcher.
type uncertainCrashCoordinates struct {
	Dir, Home, Endpoint, Key, Body, Org, Team string
	Owner                                     uuid.UUID
	Scope                                     store.BotScope
}

type uncertainCrashFixture struct {
	t       *testing.T
	q       uncertainCrashCoordinates
	db      *entstore.Store
	repo    store.BotStore
	svc     *store.Services
	auth    context.Context
	worker  *BotWorker
	backend *regressionHomeBackend
	llm     *uncertainRestartHTTPModel
	pool    *uncertainRestartHTTPPool
	server  *httptest.Server
}

func openUncertainCrashFixture(t *testing.T, q uncertainCrashCoordinates, child bool) *uncertainCrashFixture {
	t.Helper()
	ctx := context.Background()
	_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(q.Dir, "platform.db"), DataDir: q.Dir})
	if err != nil {
		t.Fatal(err)
	}
	f := &uncertainCrashFixture{t: t, q: q, db: db, backend: &regressionHomeBackend{fileFirstHTTPBackend{setupHomeFilesBackend: setupHomeFilesBackend{documentsRoot: q.Home}}}, llm: &uncertainRestartHTTPModel{text: "Do not replay the write; independently reconcile the provider receipt."}, pool: &uncertainRestartHTTPPool{writeURL: q.Endpoint + "/effects", receiptURL: q.Endpoint + "/receipts/" + q.Key}}
	if err := db.InstallBotContentHomeBackend(f.backend); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(q.Org)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = ods.ForTeam(q.Team).Bots()
	f.svc = &store.Services{Bots: f.repo, Sessions: ods.ForTeam(q.Team).Sessions()}
	if q.Scope == store.BotScopePersonal {
		f.repo = ods.ForUser(q.Owner.String()).Bots()
		f.svc.PersonalBots, f.svc.PersonalSessions = f.repo, ods.ForUser(q.Owner.String()).Sessions()
	}
	var pool sandbox.ToolNodePool = f.pool
	if child {
		pool = &uncertainCrashPool{uncertainRestartHTTPPool: f.pool}
	}
	f.worker, err = NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: f.backend, Pool: workerTestPool{}, WorkerID: fmt.Sprintf("owned-crash-%d", os.Getpid()), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return f.llm, nil
	}, Prepare: func(_ context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
			return productionBotFiles(c, f.backend, svc, scope)
		}
		return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: f.repo, request: q}, Classifier: uncertainRestartHTTPClassifier{q.Policy.Identity(), f.pool.writeURL, f.pool.receiptURL}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.auth, _, err = f.worker.authenticateOwner(ctx, BotSchedulerScope{q.Org, q.Team, q.Owner.String()})
	if err != nil {
		t.Fatal(err)
	}
	httpCtx := store.WithServices(store.WithTenantContext(f.auth, &store.TenantContext{UserID: q.Owner.String(), OrgSlug: q.Org, TeamSlug: q.Team}), f.svc)
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}/test-run", api.BotTestRunHandler)
	router.HandleFunc("/bots/{botID}/runs/{runID}", api.BotRunHandler)
	router.HandleFunc("/bots/{botID}/approvals", api.BotApprovalsHandler)
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler)
	router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
	return f
}
func (f *uncertainCrashFixture) close() {
	f.server.Close()
	f.worker.Stop()
	if err := f.db.Close(); err != nil {
		f.t.Error(err)
	}
}
func (f *uncertainCrashFixture) poll() {
	f.t.Helper()
	if err := f.worker.Poll(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}
func (f *uncertainCrashFixture) request(id uuid.UUID, method, path, payload string, want int) []byte {
	f.t.Helper()
	req, err := http.NewRequest(method, f.server.URL+"/bots/"+id.String()+"/"+path+"?scope="+string(f.q.Scope), strings.NewReader(payload))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := f.server.Client()
	client.Timeout = 10 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if resp.StatusCode != want {
		f.t.Fatalf("%s %s status=%d want=%d: %s", method, path, resp.StatusCode, want, raw)
	}
	return raw
}
func (f *uncertainCrashFixture) run(id, run uuid.UUID) store.BotRun {
	f.t.Helper()
	var r store.BotRun
	if err := json.Unmarshal(f.request(id, "GET", "runs/"+run.String(), "", 200), &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}
func (f *uncertainCrashFixture) approvals(id uuid.UUID) []store.BotApproval {
	f.t.Helper()
	var r struct {
		Approvals []store.BotApproval `json:"approvals"`
	}
	if err := json.Unmarshal(f.request(id, "GET", "approvals", "", 200), &r); err != nil {
		f.t.Fatal(err)
	}
	return r.Approvals
}

// This seam only delays returning the actual HTTP error to Chat. It neither
// consumes approval nor persists a dispatch/result marker. The parent instructs
// exit after independently checking the effect and nonterminal durable state.
type uncertainCrashPool struct{ *uncertainRestartHTTPPool }

func (p *uncertainCrashPool) GetOrCreate(string) sandbox.ToolNodeClient                     { return p }
func (p *uncertainCrashPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient { return p }
func (p *uncertainCrashPool) Call(session, name string, args map[string]any) (json.RawMessage, error) {
	return p.CallContext(context.Background(), session, name, args)
}
func (p *uncertainCrashPool) CallContext(ctx context.Context, session, name string, args map[string]any) (json.RawMessage, error) {
	raw, err := p.uncertainRestartHTTPPool.CallContext(ctx, session, name, args)
	if args["method"] == http.MethodPost && err != nil && p.lost.Load() == 1 {
		fmt.Fprintln(os.Stdout, "ASTONISH_OWNED_EFFECT_LOST_RESPONSE")
		var command [1]byte
		if _, readErr := io.ReadFull(os.Stdin, command[:]); readErr == nil && command[0] == 'X' {
			os.Exit(73)
		}
		os.Exit(74)
	}
	return raw, err
}

func uncertainCrashTransport(t *testing.T, endpoint string) {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != u.Host {
			return nil, fmt.Errorf("non-fixture dial rejected")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}}
	http.DefaultTransport = transport
	tools.SetAllowPrivateNetworks(true)
	t.Cleanup(func() {
		http.DefaultTransport = original
		transport.CloseIdleConnections()
		tools.SetAllowPrivateNetworks(false)
	})
}

func TestBotUncertainEffectCrashHTTPWorker(t *testing.T) {
	raw := os.Getenv("ASTONISH_OWNED_CRASH_FIXTURE")
	if raw == "" {
		return
	}
	var q uncertainCrashCoordinates
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatal(err)
	}
	uncertainCrashTransport(t, q.Endpoint)
	f := openUncertainCrashFixture(t, q, true)
	defer f.close()
	f.poll()
	t.Fatal("owned worker returned without the mid-effect crash handshake")
}

func TestBotUncertainEffectCrashHTTPNoReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			q := uncertainCrashCoordinates{Dir: dir, Home: filepath.Join(dir, "home"), Key: uuid.NewString(), Body: "one non-idempotent crash effect", Org: "owned-crash", Team: "team", Owner: uuid.New(), Scope: scope}
			sum := sha256.Sum256([]byte(q.Body))
			want := uncertainRestartHTTPReceipt{q.Key, hex.EncodeToString(sum[:]), 1}
			receiptPath := filepath.Join(dir, "receipt.json")
			var posts, gets atomic.Int64
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/effects":
					body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
					if err != nil || string(body) != q.Body || r.Header.Get("Idempotency-Key") != q.Key {
						http.Error(w, "not approved", 403)
						return
					}
					receipt := want
					receipt.EffectCount = posts.Add(1) // Deliberately NO deduplication.
					raw, err := json.Marshal(receipt)
					if err == nil {
						var file *os.File
						file, err = os.OpenFile(receiptPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
						if err == nil {
							_, err = file.Write(raw)
							if err == nil {
								err = file.Sync()
							}
							closeErr := file.Close()
							if err == nil {
								err = closeErr
							}
						}
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
					_ = conn.Close()
				case r.Method == http.MethodGet && r.URL.Path == "/receipts/"+q.Key:
					gets.Add(1)
					raw, err := os.ReadFile(receiptPath)
					if err != nil {
						http.Error(w, "missing", 404)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				default:
					http.Error(w, "not owned", 403)
				}
			}))
			defer sink.Close()
			q.Endpoint = sink.URL
			uncertainCrashTransport(t, q.Endpoint)
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			org := &store.Organization{ID: uuid.NewString(), Slug: q.Org, Name: "Owned crash", Status: "active"}
			if err := db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(q.Org)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: q.Team, Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			if err := db.Users().Create(ctx, &store.User{ID: q.Owner.String(), Email: q.Owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, q.Owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: q.Owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			f := openUncertainCrashFixture(t, q, false)
			defer func() {
				if f != nil {
					f.close()
				}
			}()
			v := store.DefaultBotVersion()
			v.Charter = "Write only after exact owner approval. Never replay uncertain effects; read receipts only when independently authorized."
			v.Autonomy.AllowedTools = []string{"http_request"}
			b, err := f.repo.Create(f.auth, store.BotCreateRequest{Name: "Crash no replay", OwnerID: q.Owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = f.worker.provisionHome(f.auth, f.repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			ods, err = f.db.ForOrg(q.Org)
			if err != nil {
				t.Fatal(err)
			}
			pin := ods.ForTeam(q.Team).SetSessionPin
			if scope == store.BotScopePersonal {
				pin = ods.ForUser(q.Owner.String()).SetSessionPin
			}
			if err := pin(f.auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			f.llm.args = map[string]any{"url": f.pool.writeURL, "method": http.MethodPost, "body": q.Body, "headers": map[string]any{"Idempotency-Key": q.Key}, "timeout": 2, "max_bytes": 1024}
			payload := fmt.Sprintf(`{"version":%d,"idempotency_key":"crash-write","instructions":"Perform the exact write only after approval"}`, b.Version)
			var run store.BotRun
			if err := json.Unmarshal(f.request(b.ID, "POST", "test-run", payload, 202), &run); err != nil {
				t.Fatal(err)
			}
			f.poll()
			waiting := f.run(b.ID, run.ID)
			all := f.approvals(b.ID)
			if waiting.Status != store.RunWaitingApproval || len(all) != 1 || all[0].Status != store.ApprovalPending || posts.Load() != 0 {
				t.Fatalf("approval barrier: %+v %+v", waiting, all)
			}
			approval := all[0]
			cp, err := bot.DecodeApprovalCheckpoint(waiting.Checkpoint, waiting)
			if err != nil || !cp.Matches(approval) {
				t.Fatalf("checkpoint: %+v %v", cp, err)
			}
			decisionPath := "approvals/" + approval.ID.String() + "/decision"
			decision := fmt.Sprintf(`{"action_digest":%q,"status":"approved"}`, approval.ActionDigest)
			resumePath := "runs/" + run.ID.String() + "/resume"
			resume := fmt.Sprintf(`{"approval_id":%q}`, approval.ID)
			f.request(b.ID, "POST", decisionPath, decision, 200)
			f.poll()
			if posts.Load() != 0 {
				t.Fatal("decision executed write")
			}
			f.request(b.ID, "POST", resumePath, resume, 202)
			f.close()
			f = nil // No parent worker/store survives the effect-producing process.
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			command := exec.CommandContext(childCtx, binary, "-test.run=^TestBotUncertainEffectCrashHTTPWorker$", "-test.v")
			raw, err := json.Marshal(q)
			if err != nil {
				t.Fatal(err)
			}
			command.Env = append(os.Environ(), "ASTONISH_OWNED_CRASH_FIXTURE="+string(raw))
			var childLog bytes.Buffer
			command.Stderr = &childLog
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					cancel()
					_ = command.Wait()
				}
			}()
			scanner := bufio.NewScanner(stdout)
			handshake := false
			for scanner.Scan() {
				if scanner.Text() == "ASTONISH_OWNED_EFFECT_LOST_RESPONSE" {
					handshake = true
					break
				}
			}
			if !handshake {
				_ = command.Wait()
				waited = true
				t.Fatalf("missing lost-response handshake: %s", childLog.String())
			}
			f = openUncertainCrashFixture(t, q, false)
			unfinished := f.run(b.ID, run.ID)
			if unfinished.Status != store.RunRunning || unfinished.FinishedAt != nil || unfinished.Report.EffectState == store.BotReportEffectsUncertain {
				t.Fatalf("child already persisted terminal report: %+v", unfinished)
			}
			all = f.approvals(b.ID)
			if len(all) != 1 || all[0].Status != store.ApprovalConsumed || posts.Load() != 1 || gets.Load() != 0 {
				t.Fatalf("pre-crash durable evidence: approvals=%+v posts=%d gets=%d", all, posts.Load(), gets.Load())
			}
			raw, err = os.ReadFile(receiptPath)
			var receipt uncertainRestartHTTPReceipt
			if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt != want {
				t.Fatalf("effect receipt: %s %v", raw, err)
			}
			if _, err := stdin.Write([]byte("X")); err != nil {
				t.Fatal(err)
			}
			_ = stdin.Close()
			err = command.Wait()
			waited = true
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 73 {
				t.Fatalf("wrong owned process exit: %v %s", err, childLog.String())
			}
			f.close()
			f = openUncertainCrashFixture(t, q, false)
			unfinished = f.run(b.ID, run.ID)
			if unfinished.Status != store.RunRunning || unfinished.FinishedAt != nil {
				t.Fatalf("terminal persistence survived killed worker: %+v", unfinished)
			}
			// Wait for the authoritative persisted lease; never forge an expired lease or
			// edit database timestamps. Recovery itself is the production worker Poll.
			delay := time.Until(unfinished.Lease.ExpiresAt) + 20*time.Millisecond
			if delay > 5*time.Minute+time.Second {
				t.Fatalf("unexpected fixture lease: %s", delay)
			}
			if delay > 0 {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				<-timer.C
			}
			f.poll()
			saved := f.run(b.ID, run.ID)
			if saved.Status != store.RunInterrupted || saved.Report.EffectState != store.BotReportEffectsUncertain || len(saved.Report.Unverified) == 0 || len(saved.Report.Changed) != 0 {
				t.Errorf("mid-effect recovery lost typed uncertainty: status=%s finished=%t effect=%q unverified=%v changed=%v", saved.Status, saved.FinishedAt != nil, saved.Report.EffectState, saved.Report.Unverified, saved.Report.Changed)
			}
			original := saved.Report
			for range 2 {
				f.request(b.ID, "POST", resumePath, resume, 409)
				f.request(b.ID, "POST", decisionPath, decision, 409)
				f.request(b.ID, "POST", "test-run", payload, 202)
				f.poll()
				if !reflect.DeepEqual(f.run(b.ID, run.ID).Report, original) {
					t.Fatal("admission altered crash report")
				}
			}
			if posts.Load() != 1 || gets.Load() != 0 || f.pool.calls.Load() != 0 || f.llm.calls != 0 {
				t.Fatal("recovery replayed or silently reconciled")
			}
			f.llm.args = map[string]any{"url": f.pool.receiptURL, "method": http.MethodGet, "timeout": 2, "max_bytes": 1024}
			readPayload := fmt.Sprintf(`{"version":%d,"idempotency_key":"separate-receipt-read","instructions":"Read the provider receipt only; never retry the write"}`, b.Version)
			var readRun store.BotRun
			if err := json.Unmarshal(f.request(b.ID, "POST", "test-run", readPayload, 202), &readRun); err != nil {
				t.Fatal(err)
			}
			if readRun.ID == run.ID {
				t.Fatal("read reused write identity")
			}
			if err := f.db.Organizations().RemoveMember(ctx, q.Owner.String(), org.ID); err != nil {
				t.Fatal(err)
			}
			f.poll()
			if gets.Load() != 0 || f.llm.calls != 0 {
				t.Fatal("revoked owner reconciled")
			}
			if err := f.db.Organizations().AddMember(ctx, q.Owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			f.poll()
			var result tools.HttpRequestResult
			if err := json.Unmarshal(f.pool.readResult, &result); err != nil || result.StatusCode != 200 || json.Unmarshal([]byte(result.Body), &receipt) != nil || receipt != want {
				t.Fatalf("receipt read: %s %v", f.pool.readResult, err)
			}
			if f.run(b.ID, readRun.ID).Status != store.RunSucceeded || posts.Load() != 1 || gets.Load() != 1 || f.pool.calls.Load() != 1 {
				t.Fatal("independent receipt did not finish without replay")
			}
			if !reflect.DeepEqual(f.run(b.ID, run.ID).Report, original) {
				t.Fatal("reconciliation rewrote original uncertainty")
			}
			all = f.approvals(b.ID)
			if len(all) != 1 || all[0].Status != store.ApprovalConsumed {
				t.Fatalf("reconciliation changed authority: %+v", all)
			}
			t.Logf("scope=%s owned_child_exit=73 before_terminal=true consumed_before_dispatch=true effects=1 lost_response=1 reopened_sqlite=true fresh_worker_recovery=true rejected_resumes=2 independent_receipt_gets=1", scope)
		})
	}
}
