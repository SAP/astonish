//go:build e2e

package e2eboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"io"
	"iter"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// BootstrapRetainedHomeKubernetes validates dedicated infrastructure before any
// network action, then opens a real no-model SQL/auth harness. No generic sweep,
// provider config, cluster provisioning or PVC deletion is performed.
func BootstrapRetainedHomeKubernetes(t *testing.T) (*Harness, func() (sandbox.Backend, *sandbox.SessionRegistry)) {
	t.Helper()
	in, err := retainedHomeKubernetesInputs(os.Getenv)
	if err != nil {
		t.Fatalf("BLOCKED before network: %v", err)
	}
	client, err := retainedHomeDedicatedClient(in)
	if err != nil {
		t.Fatalf("BLOCKED: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := checkRetainedHomeCluster(ctx, in, client); err != nil {
		t.Fatalf("BLOCKED before SQL provisioning: %v", err)
	}
	h := bootstrapRetainedHomeNoModel(t, in)
	root := t.TempDir()
	return h, func() (sandbox.Backend, *sandbox.SessionRegistry) {
		t.Helper()
		st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
		if err != nil {
			t.Fatal(err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindK8s, Sessions: reg, K8s: in.Kubernetes})
		if err != nil {
			t.Fatal(err)
		}
		return b, reg
	}
}

// RetainedHomeBotRuntime uses bootstrap's authenticated directory and scoped store.
// The worker installs real readiness but is not started: this scenario runs no LLM.
type RetainedHomeBotRuntime struct {
	Context   context.Context
	Principal execution.Principal
	Bots      store.BotStore
	Manager   *launcher.BotHomeManager
	pin       func(context.Context, string, string, string) error
	Scope     store.BotScope
	Sessions  store.SessionStore
	Org       store.OrgDataStore
}

type retainedHomeNoGeneration struct{}

func (retainedHomeNoGeneration) Name() string { return "retained-home-no-generation" }
func (retainedHomeNoGeneration) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, fmt.Errorf("home-only scenario must not generate model content"))
	}
}

func NewRetainedHomeBotRuntime(t *testing.T, ctx context.Context, h *Harness, backend sandbox.Backend, pool sandbox.ToolNodePool) *RetainedHomeBotRuntime {
	return NewScopedRetainedHomeBotRuntime(t, ctx, h, backend, pool, store.BotScopePersonal)
}

// NewScopedRetainedHomeBotRuntime never aliases a team repository to a personal one.
func NewScopedRetainedHomeBotRuntime(t *testing.T, ctx context.Context, h *Harness, backend sandbox.Backend, pool sandbox.ToolNodePool, scope store.BotScope) *RetainedHomeBotRuntime {
	t.Helper()
	if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
		t.Fatal("invalid retained Home scope")
	}
	owner := seedRetainedHomeOwner(t, h)
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.ID, OrgSlug: owner.OrgSlug, TeamSlug: owner.TeamSlug, Authenticated: true}
	auth, err := execution.WithPrincipal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	org, err := h.Store.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	auth = store.WithTenantContext(auth, &store.TenantContext{OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, UserID: p.Subject})
	r := &RetainedHomeBotRuntime{Context: auth, Principal: p, Scope: scope, Org: org, Bots: org.ForUser(p.Subject).Bots(), Sessions: org.ForUser(p.Subject).Sessions(), pin: org.ForUser(p.Subject).SetSessionPin}
	if scope == store.BotScopeTeam {
		r.Bots, r.Sessions, r.pin = org.ForTeam(p.TeamSlug).Bots(), org.ForTeam(p.TeamSlug).Sessions(), org.ForTeam(p.TeamSlug).SetSessionPin
	}
	r.Manager = &launcher.BotHomeManager{Backend: backend.(sandbox.BotHomeBackend), Pool: pool, Bots: r.Bots, Scope: scope}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: h.Store, Sandbox: backend, Pool: pool, WorkerID: "retained-home-readiness", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, got execution.Principal, scope store.BotScope, name, modelName string) (model.LLM, error) {
		if got.Subject != p.Subject || got.OrgSlug != p.OrgSlug || got.TeamSlug != p.TeamSlug || scope != r.Scope || name != "retained-home" || modelName != "no-generation" {
			return nil, store.ErrBotForbidden
		}
		return retainedHomeNoGeneration{}, nil
	}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		return nil, fmt.Errorf("home-only scenario must not start model execution")
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Stop)
	return r
}

// Create provisions storage before the registry attestation, then performs reviewed
// configuration and production worker readiness/CAS activation. No SQL status seed.
func (r *RetainedHomeBotRuntime) Create(t *testing.T, name string) (*store.Bot, *sandbox.BotHomeBinding) {
	t.Helper()
	v := store.DefaultBotVersion()
	v.Charter = "Verify isolated retained home across leased physical sessions"
	b, err := r.Bots.CreateDraft(r.Context, store.BotCreateRequest{Name: name, OwnerID: uuid.MustParse(r.Principal.Subject), Version: v, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	return r.RegisterAndActivate(t, b, v)
}

func (r *RetainedHomeBotRuntime) RegisterAndActivate(t *testing.T, b *store.Bot, v store.BotVersion) (*store.Bot, *sandbox.BotHomeBinding) {
	t.Helper()
	scope := sandbox.BotHomeScope{OrgID: r.Principal.OrgSlug, TeamID: r.Principal.TeamSlug, UserID: r.Principal.Subject, BotID: b.ID.String()}
	home, err := r.Manager.Backend.EnsureBotHome(r.Context, scope)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := execution.WithPrincipal(r.Context, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceCode, ClientID: "retained-home-registry", OrgSlug: r.Principal.OrgSlug, TeamSlug: r.Principal.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	registered := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: b.Scope, Backend: string(home.Backend), StorageKey: home.Ref, Revision: 1}
	q := entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: registered}
	err = entstore.RegisterBotHomeBinding(internal, r.Bots, q, func(_ context.Context, got entstore.BotHomeRegistration) error {
		// Attest the already-provisioned result, never provision inside the callback.
		if got != q || home.Validate() != nil || home.Scope != scope || got.Home.StorageKey != home.Ref || got.Home.Backend != string(home.Backend) {
			return store.ErrBotConflict
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err = r.Bots.Get(r.Context, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err = r.Bots.(store.BotConfigurationStore).ConfigureReviewed(r.Context, store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Reviewed home-only release scenario", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.pin(r.Context, b.HomeSessionID.String(), "retained-home", "no-generation"); err != nil {
		t.Fatal(err)
	}
	b, err = r.Bots.Activate(r.Context, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Reviewed retained-home readiness"})
	if err != nil || b == nil || b.Status != store.BotActive {
		t.Fatalf("reviewed activation: %+v %v", b, err)
	}
	return b, home
}

func (r *RetainedHomeBotRuntime) Claim(t *testing.T, id store.BotID) (context.Context, *store.BotRun) {
	t.Helper()
	b, err := r.Bots.Get(r.Context, id)
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.Bots.RequestRun(r.Context, store.BotRunRequest{BotID: id, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: uuid.NewString(), Instructions: "Exercise retained home only"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := execution.WithBotRunIdentity(r.Context, execution.BotRunIdentity{BotID: id, RunID: run.ID, Version: b.Version, ActorID: r.Principal.Subject, OrgSlug: r.Principal.OrgSlug, TeamSlug: r.Principal.TeamSlug})
	lease, err := r.Bots.ClaimRun(ctx, store.BotClaimRequest{BotID: id, RunID: run.ID, WorkerID: "retained-home-release", ExpiresAt: time.Now().Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	run, err = r.Bots.GetRun(ctx, id, run.ID)
	if err != nil || run == nil || run.Status != store.RunRunning || run.SessionID == uuid.Nil || run.Lease.Fence != lease.Fence || run.Lease.WorkerID != lease.WorkerID || !time.Now().Before(run.Lease.ExpiresAt) {
		t.Fatalf("claimed run: %+v %v", run, err)
	}
	return ctx, run
}

// RetainedHomeFixtureBytes matches the backend-neutral Docker acceptance fixture.
const RetainedHomeFixtureBytes = "LOCAL_SEARCH_RESULT_42: OpenShell sidecars separate temporary compute from retained Home storage. This is a deterministic local fixture, not a fetched public source."
const retainedHomeFixturePath = "outputs/local-search-results.txt"

// RetainedHomeLifecycle drives production HTTP handlers, worker, shared catalog,
// scheduler admission and genuine child execution. Only model output is scripted.
type RetainedHomeLifecycle struct {
	platform      *entstore.Store
	Runtime       *RetainedHomeBotRuntime
	Backend       sandbox.Backend
	Pool          sandbox.ToolNodePool
	Worker        *daemon.BotWorker
	Client        kubernetes.Interface
	Inputs        RetainedHomeKubernetesInputs
	Server        *httptest.Server
	Bot           *store.Bot
	Home          *sandbox.BotHomeBinding
	model         *retainedHomeLifecycleModel
	mandatePolicy store.BotMandatePolicyResolver
}

type retainedHomeLifecycleModel struct {
	mu      sync.Mutex
	calls   int
	childID string
	title   bool
}

func (*retainedHomeLifecycleModel) Name() string { return "controlled-retained-home" }
func (m *retainedHomeLifecycleModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if r.Config != nil && r.Config.ResponseMIMEType == "application/json" {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"schema_version":1,"kind":"no_change","reason":"retained bytes unchanged","proposals":[]}`, "model"), TurnComplete: true}, nil)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.title {
			m.title = false
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "title-question", Name: "ask_user", Args: map[string]any{"prompt": "Which retained-file deliverable should I prepare?", "kind": "text"}}}}}}, nil)
			return
		}
		m.calls++
		call := m.calls
		if call == 2 || call == 4 || call == 6 || call == 9 {
			found := false
			for _, c := range r.Contents {
				if c == nil {
					continue
				}
				for _, p := range c.Parts {
					if p != nil && p.FunctionResponse != nil && p.FunctionResponse.ID == fmt.Sprintf("retained-read-%d", call-1) {
						if strings.Contains(fmt.Sprint(p.FunctionResponse.Response), RetainedHomeFixtureBytes) {
							found = true
						}
					}
				}
			}
			if !found {
				yield(nil, fmt.Errorf("generation %d missing complete retained bytes", call))
				return
			}
		}
		if call == 9 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(RetainedHomeFixtureBytes, "model"), TurnComplete: true}, nil)
			return
		}
		if call == 10 {
			for _, c := range r.Contents {
				if c == nil {
					continue
				}
				for _, p := range c.Parts {
					if p == nil || p.FunctionResponse == nil || p.FunctionResponse.ID != "retained-delegate" {
						continue
					}
					raw, _ := json.Marshal(p.FunctionResponse.Response)
					var out struct {
						Results []struct {
							Status string `json:"status"`
							Child  string `json:"child_session_id"`
							Result string `json:"result"`
						} `json:"results"`
					}
					if json.Unmarshal(raw, &out) == nil && len(out.Results) == 1 && out.Results[0].Status == "success" && strings.Contains(out.Results[0].Result, RetainedHomeFixtureBytes) {
						m.childID = out.Results[0].Child
					}
				}
			}
			if m.childID == "" {
				yield(nil, fmt.Errorf("actual joined child result missing"))
				return
			}
		}
		var fc *genai.FunctionCall
		switch call {
		case 1, 3, 5, 8:
			fc = &genai.FunctionCall{ID: fmt.Sprintf("retained-read-%d", call), Name: "read_file", Args: map[string]any{"path": retainedHomeFixturePath, "force": true}}
		case 2, 4, 6, 10:
			fc = &genai.FunctionCall{ID: fmt.Sprintf("retained-question-%d", call), Name: "ask_user", Args: map[string]any{"prompt": fmt.Sprintf("Read retained bytes again after step %d?", call), "kind": "text"}}
		case 7:
			fc = &genai.FunctionCall{ID: "retained-delegate", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "retained-reader", "task": "Read outputs/local-search-results.txt using read_file and return its complete exact content.", "tools": []string{"read_file"}}}}}
		default:
			yield(nil, fmt.Errorf("unexpected generation %d", call))
			return
		}
		if r.Tools[fc.Name] == nil {
			fc = &genai.FunctionCall{ID: fc.ID, Name: "execute_tool", Args: map[string]any{"name": fc.Name, "arguments": fc.Args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: fc}}}}, nil)
	}
}

// NewRetainedHomeLifecycle uses only explicit backend/client configuration. GETs
// use the read-only scoped Home manager and cannot create runs or new storage.
func NewRetainedHomeLifecycle(t *testing.T, scope store.BotScope) *RetainedHomeLifecycle {
	t.Helper()
	h, open := BootstrapRetainedHomeKubernetes(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	b, reg := open()
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	r := NewScopedRetainedHomeBotRuntime(t, ctx, h, b, pool, scope)
	in, err := retainedHomeKubernetesInputs(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := retainedHomeDedicatedClient(in)
	if err != nil {
		t.Fatal(err)
	}
	f := &RetainedHomeLifecycle{platform: h.Store, Runtime: r, Backend: b, Pool: pool, Client: kc, Inputs: in, model: &retainedHomeLifecycleModel{title: true}}
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	t.Cleanup(registry.Close)
	mandatePolicy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		if ref.OwnerID != r.Principal.Subject || ref.OrgSlug != r.Principal.OrgSlug || ref.TeamSlug != r.Principal.TeamSlug {
			return store.BotMandatePolicy{}, store.ErrBotForbidden
		}
		if _, err := f.currentOwner(c); err != nil {
			return store.BotMandatePolicy{}, err
		}
		policy, err := launcher.NewBotRoutineReadMandatePolicy(&store.Services{NetworkPolicies: r.Org.OrgNetworkPolicies(), TeamNetworkPolicies: r.Org.ForTeam(r.Principal.TeamSlug).NetworkPolicies()}, config.DefaultBotMandateConfig())(c, ref)
		if err != nil {
			return policy, err
		}
		for i := range policy.Constraints {
			if policy.Constraints[i].Action == execution.CapabilityToolExecute {
				policy.Constraints[i].Resources = append(policy.Constraints[i].Resources, "read_file", "ask_user")
			}
		}
		return policy, nil
	}
	f.mandatePolicy = mandatePolicy
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{MandatePolicy: mandatePolicy, Backend: h.Store, Sandbox: b, Pool: pool, WorkerID: "retained-lifecycle-" + uuid.NewString(), TurnRegistry: registry, ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true}, ResolveModel: func(c context.Context, p execution.Principal, s store.BotScope, name, modelName string) (model.LLM, error) {
		if p.Subject != r.Principal.Subject || p.OrgSlug != r.Principal.OrgSlug || p.TeamSlug != r.Principal.TeamSlug || s != scope || name != "retained-home" || modelName != "no-generation" {
			return nil, store.ErrBotForbidden
		}
		return f.model, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if f.Bot == nil || q.Bot.ID != f.Bot.ID || (f.Home == nil && q.Bot.Status != store.BotDraft) {
			return nil, store.ErrBotConflict
		}
		id := q.Bot.HomeSessionID.String()
		if f.Home != nil {
			if binder, ok := b.(sandbox.BotHomeSessionBinder); ok {
				if err := binder.BindBotHomeSession(f.Home, id); err != nil {
					return nil, err
				}
			}
		}
		pool.SetSessionScope(id, q.Principal.OrgSlug, q.Principal.TeamSlug)
		if err := sandbox.BindBotHome(pool, id, f.Home); err != nil {
			return nil, err
		}
		pool.GetOrCreateWithTemplate(id, q.Version.SandboxTemplate)
		pool.Alias(q.Run.SessionID.String(), id)
		guard := retainedHomeLifecycleGuard{repo: r.Bots, request: q}
		local, err := launcher.BotScopedChatConfig(c, s)
		if err != nil {
			return nil, err
		}
		res := &launcher.BotChatSafeResources{ChatConfig: local, ProviderFactory: func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return f.model, nil }, Catalog: &launcher.ChatCatalogResources{Backend: b, Pool: pool, SessionRegistry: reg}, WorkspaceDir: "/root", PolicyRuntime: agent.BotPolicyRuntime{Guard: guard, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: guard.check, Cleanup: func() {}}
		if q.Mandate != nil {
			res.PolicyRuntime.Mandate = q.Mandate
		}
		return res, res.VerifyIsolation(c)
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.Worker = worker
	t.Cleanup(worker.Stop)
	// Install the public scheduler bridge so real activation readiness sees it.
	daemon.ConfigureBotScheduler(&scheduler.Executor{}, nil, worker.SchedulerCallbacks())
	svc, err := launcher.BotChatServicesForOwner(r.Org, r.Principal, scope)
	if err != nil {
		t.Fatal(err)
	}
	svc.BotTurnRegistry = registry
	svc.BotInteractiveReady = true
	svc.BotFilesFactory = func(c context.Context, s *store.Services, requested store.BotScope) (store.BotFiles, error) {
		if requested != scope {
			return nil, store.ErrBotForbidden
		}
		return &launcher.BotHomeManager{Backend: b.(sandbox.BotHomeBackend), Bots: r.Bots, Scope: scope}, nil
	}
	router := mux.NewRouter()
	router.HandleFunc("/bots", api.BotCreateHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/pause", api.BotPauseHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/files", api.BotFilesHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		router.ServeHTTP(w, req.WithContext(store.WithServices(r.Context, svc)))
	}))
	t.Cleanup(f.Server.Close)
	// Cleanup only the exact test Home session. Physical/fence uncertainty retains
	// its storage; never sweep org pods, delete PVCs or remove namespace roots.
	t.Cleanup(func() {
		worker.Stop()
		if f.Bot == nil || f.Home == nil {
			return
		}
		clean, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		if err := destroyLiveBotSession(clean, b, f.Bot.HomeSessionID.String()); err != nil {
			t.Errorf("owned physical cleanup: %v", err)
			return
		}
		pool.Remove(f.Bot.HomeSessionID.String())
		if err := b.(sandbox.BotHomeDeleteBackend).DeleteBotHome(clean, f.Home); err != nil {
			t.Errorf("owned Home cleanup retained fence: %v", err)
		}
	})
	return f
}

type retainedHomeLifecycleGuard struct {
	repo    store.BotStore
	request bot.RunRequest
}

func (g retainedHomeLifecycleGuard) check(c context.Context) error {
	q := g.request
	if q.Budget == nil {
		return store.ErrBotForbidden
	}
	if err := q.Budget.Check(c); err != nil {
		return err
	}
	if q.Authorization != nil {
		if err := q.Authorization.Authorize(c, execution.CapabilityChat); err != nil {
			return err
		}
	}
	if q.Mandate != nil {
		if err := q.Mandate.Check(c); err != nil {
			return err
		}
	}
	b, err := g.repo.Get(c, q.Bot.ID)
	if err != nil {
		return err
	}
	if b == nil || !q.Run.Intent.AllowsStatus(b.Status) || b.Version != q.Version.Version || b.OwnerID != q.Bot.OwnerID {
		return store.ErrBotForbidden
	}
	run, err := g.repo.GetRun(c, b.ID, q.Run.ID)
	if err != nil {
		return err
	}
	if run == nil || run.Status != store.RunRunning || run.SessionID != q.Run.SessionID || run.Lease.Fence != q.Run.Lease.Fence || run.Lease.WorkerID != q.Run.Lease.WorkerID || !time.Now().Before(run.Lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	return nil
}
func (g retainedHomeLifecycleGuard) Authorize(c context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if err := g.check(c); err != nil {
		return execution.PolicyDecision{}, err
	}
	d := execution.EvaluateRunPolicy(c, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant && g.request.Mandate != nil {
		grant, err := g.request.Mandate.AuthorizeAction(c, a)
		if err != nil || grant.ActionDigest != d.ActionDigest {
			return d, store.ErrBotForbidden
		}
		d.Kind = execution.PolicyAllow
	}
	if d.Kind != execution.PolicyAllow {
		return d, store.ErrBotForbidden
	}
	return d, nil
}

func (f *RetainedHomeLifecycle) HTTP(t *testing.T, method, path string, body any, status int) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(f.Runtime.Context, method, f.Server.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := f.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != status {
		t.Fatalf("%s %s status=%d want=%d body=%s err=%v", method, path, res.StatusCode, status, out, err)
	}
	return out
}
func (f *RetainedHomeLifecycle) path(suffix string) string {
	return "/bots/" + f.Bot.ID.String() + suffix + "?scope=" + string(f.Runtime.Scope)
}
func (f *RetainedHomeLifecycle) pollWaiting(t *testing.T, id uuid.UUID, wantCalls int) *store.BotRun {
	t.Helper()
	if err := f.Worker.Poll(f.Runtime.Context); err != nil {
		t.Fatal(err)
	}
	run, err := f.Runtime.Bots.GetRun(f.Runtime.Context, f.Bot.ID, id)
	f.model.mu.Lock()
	calls := f.model.calls
	f.model.mu.Unlock()
	if err != nil || run == nil || run.Status != store.RunWaitingApproval || calls != wantCalls {
		t.Fatalf("worker waiting run=%+v err=%v calls=%d want=%d", run, err, calls, wantCalls)
	}
	return run
}
func (f *RetainedHomeLifecycle) ownerMessage(t *testing.T, instructions string, wantCalls int) *store.BotRun {
	t.Helper()
	current, err := f.Runtime.Bots.Get(f.Runtime.Context, f.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := f.HTTP(t, http.MethodPost, f.path("/messages"), map[string]any{"version": current.Version, "instructions": instructions, "idempotency_key": uuid.NewString()}, 202)
	var receipt store.BotMessageReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	return f.pollWaiting(t, receipt.Run.ID, wantCalls)
}

// Run proves title-only laziness, interactive/scheduled/continuation reuse,
// UID-fenced compute eviction, real restricted delegation and exact downloads.
func (f *RetainedHomeLifecycle) Run(t *testing.T, noPython bool) {
	t.Helper()
	r := f.Runtime
	ctx := r.Context
	raw := f.HTTP(t, http.MethodPost, "/bots?scope="+string(r.Scope), map[string]any{"name": "Astonish New Proxy", "start_from_title": true, "idempotency_key": uuid.NewString()}, 201)
	var created struct {
		store.Bot
		InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.InitialTurn == nil {
		t.Fatalf("title create: %s %v", raw, err)
	}
	f.Bot = &created.Bot
	if err := r.pin(ctx, f.Bot.HomeSessionID.String(), "retained-home", "no-generation"); err != nil {
		t.Fatal(err)
	}
	f.pollWaiting(t, created.InitialTurn.Run.ID, 0)
	f.AssertCompute(t, false, noPython)
	v := store.DefaultBotVersion()
	v.Autonomy.AllowedTools = []string{"read_file", "ask_user", "delegate_tasks"}
	v.Autonomy.AllowedPaths = []string{"outputs"}
	var err error
	f.Bot, f.Home = r.RegisterAndActivate(t, f.Bot, v)
	if f.Bot == nil {
		t.Fatal("retained lifecycle activation returned no Bot")
	}
	// Seed via the real retained backend outside SQL, never fabricate a tool result.
	files := f.Backend.(sandbox.BotHomeFilesBackend)
	_, err = files.WriteBotHomeFile(ctx, f.Home, store.BotFileWrite{Path: retainedHomeFixturePath, SizeBytes: int64(len(RetainedHomeFixtureBytes)), Body: strings.NewReader(RetainedHomeFixtureBytes), CreateOnly: true})
	if err != nil {
		t.Fatalf("seed retained fixture: %v", err)
	}
	if err := entstore.SelectBotHomeConfiguration(ctx, r.Bots, *f.Home, f.Backend.(sandbox.BotHomeFilesBackend)); err != nil {
		t.Fatal(err)
	}
	f.ownerMessage(t, "Read outputs/local-search-results.txt and ask whether to continue.", 2)
	first := f.AssertCompute(t, true, noPython)
	f.AssertDownload(t)
	// Use the public scheduled occurrence store and production scheduler admission
	// wrapper, reauthenticating owner membership instead of trusting request hints.
	tid := uuid.New()
	current, err := r.Bots.Get(ctx, f.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	triggers := r.Bots.(store.BotTriggerStore)
	current, err = triggers.ChangeTrigger(ctx, store.BotTriggerChange{BotID: f.Bot.ID, TriggerID: tid, ExpectedRevision: current.Revision, Action: store.BotTriggerSave, Reason: "Dedicated retained read", Definition: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "Read outputs/local-search-results.txt and ask whether to continue."}})
	if err != nil {
		t.Fatal(err)
	}
	current, err = triggers.ChangeTrigger(ctx, store.BotTriggerChange{BotID: f.Bot.ID, TriggerID: tid, ExpectedRevision: current.Revision, Action: store.BotTriggerEnable, Reason: "Dedicated retained schedule"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := r.Org.ForTeam(r.Principal.TeamSlug).ScheduledJobs()
	if r.Scope == store.BotScopePersonal {
		jobs = r.Org.ForUser(r.Principal.Subject).ScheduledJobs()
	}
	list := jobs.List(ctx)
	if len(list) != 1 {
		t.Fatalf("expected one owned schedule: %+v", list)
	}
	job := list[0]
	admission := daemon.BotSchedulerCallbacks{Runtime: f.Worker.SchedulerCallbacks().Runtime, Resolve: func(c context.Context, q scheduler.BotTriggerRequest) (context.Context, store.BotScheduledRunStore, error) {
		owner, err := f.currentOwner(c)
		if err != nil {
			return nil, nil, err
		}
		if q.BotID != f.Bot.ID {
			return nil, nil, store.ErrBotForbidden
		}
		return store.WithBotMandateAdmission(owner, f.mandatePolicy), r.Bots.(store.BotScheduledRunStore), nil
	}}.Admission()
	receipt, err := admission(context.WithoutCancel(ctx), scheduler.BotTriggerRequest{JobID: job.ID, BotID: f.Bot.ID, Version: current.Version, TriggerID: tid, OccurrenceAt: time.Now().UTC(), IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	scheduled := f.pollWaiting(t, receipt.RunID, 4)
	if scheduled.IsInteractiveChat() || scheduled.Trigger != store.TriggerCron {
		t.Fatal("scheduled occurrence used interactive authority")
	}
	if pod := f.AssertCompute(t, true, noPython); pod.UID != first.UID {
		t.Fatal("scheduler replaced original physical compute")
	}
	approvals, err := r.Bots.ListApprovals(ctx, f.Bot.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	var question *store.BotApproval
	for i := range approvals {
		a := &approvals[i]
		if a.RunID == scheduled.ID && a.Interaction != nil && a.Status == store.ApprovalPending {
			question = a
		}
	}
	if question == nil {
		t.Fatal("scheduled typed question missing")
	}
	f.Evict(t)
	f.AssertCompute(t, false, noPython)
	f.AssertDownload(t)
	f.HTTP(t, http.MethodPost, f.path("/approvals/"+question.ID.String()+"/approve-and-continue"), map[string]any{"action_digest": question.ActionDigest, "response": store.BotInteractionResponse{QuestionID: question.Interaction.QuestionID, Answer: "Read the same exact bytes again."}}, 202)
	f.pollWaiting(t, scheduled.ID, 6)
	recovered := f.AssertCompute(t, true, noPython)
	if recovered.UID == first.UID {
		t.Fatal("eviction did not create new physical UID")
	}
	parent := f.ownerMessage(t, "Delegate the exact retained output to a read-only reader child.", 10)
	if pod := f.AssertCompute(t, true, noPython); pod.UID != recovered.UID {
		t.Fatal("delegate replaced recovered original compute")
	}
	f.model.mu.Lock()
	childID := f.model.childID
	f.model.mu.Unlock()
	child, err := r.Sessions.GetSessionMeta(ctx, childID)
	if err != nil || child == nil || child.ParentID != parent.SessionID.String() || child.UserID != r.Principal.Subject {
		t.Fatalf("child parent/owner context: %+v %v", child, err)
	}
	events, err := r.Sessions.ReadTranscriptEvents(ctx, child.AppName, child.UserID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e == nil || e.Content == nil {
			continue
		}
		for _, p := range e.Content.Parts {
			if p != nil && p.FunctionResponse != nil && p.FunctionResponse.ID == "retained-read-8" {
				found = found || strings.Contains(fmt.Sprint(p.FunctionResponse.Response), RetainedHomeFixtureBytes)
			}
		}
	}
	if !found {
		t.Fatal("genuine child transcript lacks exact shared read_file response")
	}
	f.AssertDownload(t)
	t.Logf("K8S_ALL_MODE scope=%s original_session=%s initial_uid=%s recovered_uid=%s scheduled=%s child=%s exact_bytes=%d no_python=%t", r.Scope, f.Bot.HomeSessionID, first.UID, recovered.UID, scheduled.ID, childID, len(RetainedHomeFixtureBytes), noPython)
}

func (f *RetainedHomeLifecycle) currentOwner(c context.Context) (context.Context, error) {
	r := f.Runtime
	p := r.Principal
	u, err := r.Org.Teams().IsTeamMember(c, p.Subject, p.TeamSlug)
	if err != nil || !u {
		return nil, store.ErrBotForbidden
	}
	owner, err := execution.WithPrincipal(c, p)
	if err != nil {
		return nil, err
	}
	return store.WithTenantContext(owner, &store.TenantContext{OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, UserID: p.Subject}), nil
}

// retainedHomeOwnedPod validates the exact creation recipe before UID-fenced
// eviction. Label selectors alone never authorize deletion of arbitrary compute.
func retainedHomeOwnedPod(p *corev1.Pod, sessionID, namespace string, home *sandbox.BotHomeBinding) error {
	if p == nil || home == nil || home.Validate() != nil || p.Namespace != namespace || p.Name != DerivePodName(sessionID) || p.UID == "" {
		return store.ErrBotForbidden
	}
	mounted := false
	for _, c := range p.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.MountPath == "/mnt/astonish-bot-home" && m.SubPath == home.Scope.Key()+"/data" && m.SubPathExpr == "" && !m.ReadOnly {
				mounted = true
			}
		}
	}
	if !mounted {
		return store.ErrBotForbidden
	}
	return nil
}
func deleteRetainedHomeOwnedPod(ctx context.Context, client kubernetes.Interface, p *corev1.Pod, sessionID, namespace string, home *sandbox.BotHomeBinding) error {
	if client == nil || retainedHomeOwnedPod(p, sessionID, namespace, home) != nil {
		return store.ErrBotForbidden
	}
	uid := p.UID
	return client.CoreV1().Pods(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
}

func (f *RetainedHomeLifecycle) AssertCompute(t *testing.T, present, noPython bool) *corev1.Pod {
	t.Helper()
	pods, err := f.Client.CoreV1().Pods(f.Inputs.Kubernetes.Namespace).List(f.Runtime.Context, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var owned []*corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Name == DerivePodName(f.Bot.HomeSessionID.String()) || p.Labels["astonish.io/org"] == f.Runtime.Principal.OrgSlug {
			owned = append(owned, p)
		}
	}
	if !present {
		if len(owned) != 0 {
			t.Fatalf("read/question started workload compute: %+v", owned)
		}
		return nil
	}
	if len(owned) != 1 {
		t.Fatalf("expected sole retained original workload, got %d", len(owned))
	}
	p := owned[0]
	if err := retainedHomeOwnedPod(p, f.Bot.HomeSessionID.String(), f.Inputs.Kubernetes.Namespace, f.Home); err != nil {
		t.Fatal(err)
	}
	if noPython {
		out, err := f.Backend.Exec(f.Runtime.Context, f.Bot.HomeSessionID.String(), sandbox.ExecSpec{Command: []string{"/bin/sh", "-ec", `test -d /usr && test ! -x /usr/bin/python3 && ! command -v python3 && ! command -v python`}})
		if err != nil || out == nil || out.ExitCode != 0 {
			t.Fatalf("actual recovered tool overlay contains Python or probe failed: %+v %v", out, err)
		}
	}
	return p
}
func (f *RetainedHomeLifecycle) Evict(t *testing.T) {
	t.Helper()
	p := f.AssertCompute(t, true, false)
	err := deleteRetainedHomeOwnedPod(f.Runtime.Context, f.Client, p, f.Bot.HomeSessionID.String(), f.Inputs.Kubernetes.Namespace, f.Home)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := f.Client.CoreV1().Pods(f.Inputs.Kubernetes.Namespace).Get(f.Runtime.Context, p.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-f.Runtime.Context.Done():
			t.Fatal(f.Runtime.Context.Err())
		case <-time.After(time.Second):
		}
	}
}
func (f *RetainedHomeLifecycle) AssertDownload(t *testing.T) {
	t.Helper()
	before, err := f.Runtime.Bots.ListRuns(f.Runtime.Context, f.Bot.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	raw := f.HTTP(t, http.MethodGet, f.path("/files")+"&path=outputs", nil, 200)
	var page store.BotFilePage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range page.Entries {
		if entry.Path == retainedHomeFixturePath && entry.SizeBytes == int64(len(RetainedHomeFixtureBytes)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("exact listing missing: %s", raw)
	}
	data := f.HTTP(t, http.MethodGet, f.path("/files/download")+"&path="+retainedHomeFixturePath, nil, 200)
	if string(data) != RetainedHomeFixtureBytes {
		t.Fatalf("download bytes changed: %q", data)
	}
	after, err := f.Runtime.Bots.ListRuns(f.Runtime.Context, f.Bot.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	z, _ := json.Marshal(after)
	if string(a) != string(z) {
		t.Fatal("GET changed run inventory")
	}
}

// StopAndBrowse proves stopped cold HTTP reads without admitting model work.
func (f *RetainedHomeLifecycle) StopAndBrowse(t *testing.T) {
	t.Helper()
	current, err := f.Runtime.Bots.Get(f.Runtime.Context, f.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Runtime.Bots.Stop(f.Runtime.Context, store.BotLifecycleRequest{BotID: f.Bot.ID, ExpectedRevision: current.Revision, ActorID: current.OwnerID, Reason: "Dedicated cold read acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	if err := destroyLiveBotSession(f.Runtime.Context, f.Backend, f.Bot.HomeSessionID.String()); err != nil {
		t.Fatal(err)
	}
	f.Pool.Remove(f.Bot.HomeSessionID.String())
	f.model.mu.Lock()
	callsBefore := f.model.calls
	f.model.mu.Unlock()
	f.AssertCompute(t, false, false)
	f.AssertDownload(t)
	f.AssertCompute(t, false, false)
	f.model.mu.Lock()
	callsAfter := f.model.calls
	f.model.mu.Unlock()
	if callsAfter != callsBefore {
		t.Fatal("stopped browse/download generated model work")
	}
}

// ColdBackupRestore exercises the actual detached stream transport before asking
// the production platform importer to select it. Unsupported import is a failure,
// never a SQL-only fallback or an unconditional stub pretending to be evidence.
func (f *RetainedHomeLifecycle) ColdBackupRestore(t *testing.T) {
	t.Helper()
	ctx := f.Runtime.Context
	current, err := f.Runtime.Bots.Get(ctx, f.Bot.ID)
	if err != nil || current == nil || current.Status != store.BotStopped {
		t.Fatalf("cold backup requires stopped Bot: %+v %v", current, err)
	}
	f.Worker.Stop()
	f.model.mu.Lock()
	modelCallsBefore := f.model.calls
	f.model.mu.Unlock()
	f.AssertCompute(t, false, false)
	backend, ok := f.Backend.(entstore.BotRestoreBackend)
	if !ok {
		t.Fatal("BLOCKED: dedicated Kubernetes backend lacks detached stream/fresh-generation restore")
	}
	source := f.Home.Clone()
	barrier := func(c context.Context, exact *sandbox.BotHomeBinding, op func(context.Context) error) error {
		if !source.Equal(exact) {
			return store.ErrBotForbidden
		}
		return entstore.WithBotWorkspaceArchiveFence(c, f.Runtime.Bots, func() error {
			live, err := f.Runtime.Bots.Get(c, f.Bot.ID)
			if err != nil || live.Status != store.BotStopped || live.Home != current.Home {
				return store.ErrBotConflict
			}
			return op(c)
		})
	}
	var stream bytes.Buffer
	manifest, err := backend.ExportBotHomeArchive(ctx, source, &stream, barrier)
	if err != nil {
		t.Fatalf("detached Kubernetes stream export: %v", err)
	}
	if err := retainedHomeArchiveFixture(manifest); err != nil {
		t.Fatal(err)
	}
	target, err := backend.CreateBotHomeRestoreTarget(ctx, source.Scope)
	if err != nil {
		t.Fatalf("fresh Kubernetes generation: %v", err)
	}
	if target == nil || target.Equal(source) || target.Scope != source.Scope {
		t.Fatal("restore adopted source generation")
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := f.Backend.(sandbox.BotHomeDeleteBackend).DeleteBotHome(c, target); err != nil {
			t.Errorf("exact restored generation cleanup retained fence: %v", err)
		}
	})
	restoreBarrier := func(c context.Context, exact *sandbox.BotHomeBinding, op func(context.Context) error) error {
		if !target.Equal(exact) {
			return store.ErrBotForbidden
		}
		return barrier(c, source, op)
	}
	if err := backend.RestoreBotHomeArchive(ctx, target, manifest, bytes.NewReader(stream.Bytes()), restoreBarrier); err != nil {
		t.Fatalf("detached Kubernetes stream restore: %v", err)
	}
	// Read back all ordinary/private objects while SQL still selects the source.
	var readback bytes.Buffer
	restoredManifest, err := backend.ExportBotHomeArchive(ctx, target, &readback, restoreBarrier)
	if err != nil {
		t.Fatalf("restored generation readback before SQL: %v", err)
	}
	if err := retainedHomeArchiveFixture(restoredManifest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stream.Bytes(), readback.Bytes()) {
		t.Fatal("ordinary/private restored bytes differ before SQL selection")
	}
	f.AssertDownload(t)
	f.AssertCompute(t, false, false)
	archive := filepath.Join(t.TempDir(), "cold-backup")
	if err := f.platform.ExportPlatformBackup(ctx, archive, entstore.PlatformBackupExportOptions{OrgSlug: f.Runtime.Principal.OrgSlug, FileBackend: func(context.Context) (entstore.BotBackupBackend, error) { return backend, nil }}); err != nil {
		t.Fatalf("BLOCKED: production file-first platform export: %v", err)
	}
	// The public importer selects fresh file and SQL generations on an independent
	// platform. A dry-run alone is not cold-restore evidence.
	owned := &retainedHomeRestoreOwner{BotRestoreBackend: backend, source: source, targets: map[string]*sandbox.BotHomeBinding{}}
	sourceURL, err := url.Parse(f.Inputs.DSN)
	if err != nil {
		t.Fatal(err)
	}
	var sourceDatabase string
	if err := f.platform.PlatformDB().QueryRowContext(ctx, "SELECT current_database()").Scan(&sourceDatabase); err != nil {
		t.Fatal(err)
	}
	sourceURL.Path = "/" + sourceDatabase
	sqlTarget, targetConfig := newRetainedHomeRestoreSQL(t, f.Inputs, sourceURL.String(), owned, archive)
	opts := entstore.PlatformRestoreOptions{FileBackend: func(context.Context) (entstore.BotRestoreBackend, error) { return owned, nil }}
	if err := retainedHomePublicRestore(ctx, sqlTarget, archive, opts); err != nil {
		t.Fatalf("production PostgreSQL file-first restore: %v", err)
	}
	if err := sqlTarget.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened, err := entstore.NewPlatformServices(ctx, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	originalRoute, err := f.platform.Organizations().GetBySlug(ctx, f.Runtime.Principal.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	route, err := reopened.Organizations().GetBySlug(ctx, f.Runtime.Principal.OrgSlug)
	if err != nil || route == nil || originalRoute == nil || route.DBName == originalRoute.DBName {
		t.Fatalf("restore adopted source SQL routing: %+v %v", route, err)
	}
	if err := validateRetainedHomeRestoredRoute(ctx, f.Inputs, targetConfig, route.DBName); err != nil {
		t.Fatalf("restored routing receipt: %v", err)
	}
	org, err := reopened.ForOrg(f.Runtime.Principal.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := org.Close(); err != nil {
			t.Error(err)
		}
	})
	member, err := org.Teams().IsTeamMember(ctx, f.Runtime.Principal.Subject, f.Runtime.Principal.TeamSlug)
	if err != nil || !member {
		t.Fatal("restored current owner membership absent")
	}
	svc, err := launcher.BotChatServicesForOwner(org, f.Runtime.Principal, f.Runtime.Scope)
	if err != nil {
		t.Fatal(err)
	}
	restoredBots := svc.Bots
	if f.Runtime.Scope == store.BotScopePersonal {
		restoredBots = svc.PersonalBots
	}
	restored, err := restoredBots.Get(ctx, f.Bot.ID)
	if err != nil || restored == nil || restored.Status != store.BotPaused || restored.OwnerID != current.OwnerID || restored.Scope != current.Scope || restored.HomeSessionID != current.HomeSessionID {
		t.Fatalf("restored paused scoped identity: %+v %v", restored, err)
	}
	newHome := owned.targets[restored.Home.StorageKey]
	if newHome == nil || newHome.Equal(source) || newHome.Ref == source.Ref || newHome.Scope != source.Scope || restored.Home.Backend != string(newHome.Backend) {
		t.Fatal("SQL selected an unowned or source Home")
	}
	sourceJobs := f.Runtime.Org.ForUser(f.Runtime.Principal.Subject).ScheduledJobs()
	if f.Runtime.Scope == store.BotScopeTeam {
		sourceJobs = f.Runtime.Org.ForTeam(f.Runtime.Principal.TeamSlug).ScheduledJobs()
	}
	jobs := svc.Scheduler
	if f.Runtime.Scope == store.BotScopePersonal {
		jobs = svc.PersonalScheduler
	}
	if err := retainedHomePausedSchedules(sourceJobs.List(ctx), jobs.List(ctx)); err != nil {
		t.Fatal(err)
	}
	// Exact ordinary/private byte readback under a stopped source fence. No Home
	// manager Ensure/Start, worker Poll, model execution or new run occurs.
	coldBarrier := func(c context.Context, exact *sandbox.BotHomeBinding, op func(context.Context) error) error {
		if !newHome.Equal(exact) {
			return store.ErrBotForbidden
		}
		live, err := restoredBots.Get(c, restored.ID)
		if err != nil || live.Home != restored.Home || live.Status != store.BotPaused {
			return store.ErrBotConflict
		}
		return barrier(c, source, op)
	}
	var cold bytes.Buffer
	coldManifest, err := backend.ExportBotHomeArchive(ctx, newHome, &cold, coldBarrier)
	if err != nil || coldManifest == nil || !reflect.DeepEqual(manifest.Entries, coldManifest.Entries) || !bytes.Equal(stream.Bytes(), cold.Bytes()) {
		t.Fatalf("cold ordinary/private bytes differ: %v", err)
	}
	svc.BotFilesFactory = func(c context.Context, s *store.Services, scope store.BotScope) (store.BotFiles, error) {
		if scope != f.Runtime.Scope {
			return nil, store.ErrBotForbidden
		}
		return &launcher.BotHomeManager{Backend: f.Backend.(sandbox.BotHomeBackend), Bots: restoredBots, Scope: scope}, nil
	}
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}/files", api.BotFilesHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET")
	coldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		router.ServeHTTP(w, req.WithContext(store.WithServices(ctx, svc)))
	}))
	t.Cleanup(coldServer.Close)
	runsBefore, err := restoredBots.ListRuns(ctx, restored.ID, store.BotPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	coldRuntime := *f.Runtime
	coldRuntime.Bots, coldRuntime.Org = restoredBots, org
	coldFixture := &RetainedHomeLifecycle{Runtime: &coldRuntime, Bot: restored, Server: coldServer}
	coldFixture.AssertDownload(t)
	runsAfter, err := restoredBots.ListRuns(ctx, restored.ID, store.BotPage{Limit: 100})
	if err != nil || !reflect.DeepEqual(runsBefore, runsAfter) {
		t.Fatal("cold HTTP reads changed runs")
	}
	f.AssertCompute(t, false, false)
	liveSource, err := f.Runtime.Bots.Get(ctx, current.ID)
	if err != nil || !reflect.DeepEqual(current, liveSource) {
		t.Fatal("restore modified source Bot")
	}
	f.AssertDownload(t)
	f.model.mu.Lock()
	modelCallsAfter := f.model.calls
	f.model.mu.Unlock()
	if modelCallsAfter != modelCallsBefore {
		t.Fatal("cold restore generated model work")
	}
	f.AssertCompute(t, false, false)
	t.Logf("K8S_COLD_POSTGRES_RESTORE scope=%s fresh_sql=%s fresh_home=%s exact_bytes=%d paused_jobs=%d", f.Runtime.Scope, route.DBName, newHome.Ref, cold.Len(), len(jobs.List(ctx)))
}

func retainedHomeArchiveFixture(m *sandbox.BotHomeArchiveManifest) error {
	if m == nil || m.Validate() != nil {
		return errors.New("invalid retained Home archive manifest")
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(RetainedHomeFixtureBytes)))
	for _, e := range m.Entries {
		if e.Root == sandbox.BotHomeArchiveHome && e.Path == retainedHomeFixturePath && !e.Directory && e.Size == int64(len(RetainedHomeFixtureBytes)) && e.SHA256 == want {
			return nil
		}
	}
	return errors.New("retained Home archive lacks exact complete fixture digest")
}

func (f *RetainedHomeLifecycle) RuntimePlatform() *entstore.Store { return f.platform }
