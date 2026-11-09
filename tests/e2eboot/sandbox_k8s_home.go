//go:build e2e

package e2eboot

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
)

// ValidateRetainedHomeKubernetesInputs is a strict release gate, not an opt-in
// skip. Run before Bootstrap, whose ordinary paths may skip missing credentials.
func ValidateRetainedHomeKubernetesInputs(getenv func(string) string) error {
	if err := ValidateSecurityPreflight(getenv); err != nil {
		return err
	}
	if strings.TrimSpace(getenv("ASTONISH_SANDBOX_IMAGE")) == "" {
		return fmt.Errorf("ASTONISH_SANDBOX_IMAGE must identify the current retained-home sandbox image available to cluster nodes")
	}
	homes := getenv("ASTONISH_E2E_HOMES_PVC")
	if homes == "" || homes == "astonish-layers" || homes == "astonish-uppers" {
		return fmt.Errorf("ASTONISH_E2E_HOMES_PVC must name an independent pre-provisioned RWX claim")
	}
	return nil
}

// BootstrapRetainedHomeKubernetes reuses the full platform bootstrap and its
// dedicated namespace safety gate. It provisions no cluster, namespace or PVC.
// The returned opener rebuilds the production backend and durable local registry
// so callers can prove daemon-state reopen without fake Kubernetes clients.
func BootstrapRetainedHomeKubernetes(t *testing.T) (*Harness, func() (sandbox.Backend, *sandbox.SessionRegistry)) {
	t.Helper()
	if err := ValidateRetainedHomeKubernetesInputs(os.Getenv); err != nil {
		t.Fatalf("BLOCKED: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("BLOCKED: kubectl %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if command("config", "current-context") == "" {
		t.Fatal("BLOCKED: kubeconfig has no current-context")
	}
	for _, claim := range []string{"astonish-layers", "astonish-uppers", os.Getenv("ASTONISH_E2E_HOMES_PVC")} {
		state := command("get", "pvc", "-n", SandboxNamespace(), claim, "-o", "jsonpath={.status.phase}:{.spec.accessModes}")
		if !strings.HasPrefix(state, "Bound:") || !strings.Contains(state, "ReadWriteMany") {
			t.Fatalf("BLOCKED: PVC %s requires Bound RWX, got %s", claim, state)
		}
	}
	h := Bootstrap(t)
	root := t.TempDir()
	return h, func() (sandbox.Backend, *sandbox.SessionRegistry) {
		t.Helper()
		st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
		if err != nil {
			t.Fatal(err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindK8s, Sessions: reg,
			K8s: config.SandboxKubernetesConfig{KubeconfigPath: os.Getenv("KUBECONFIG"), Namespace: SandboxNamespace(), ControlPlaneNamespace: os.Getenv("ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"), OverlayMode: "fuse", PrivilegedPods: true, SandboxImage: os.Getenv("ASTONISH_SANDBOX_IMAGE"), LayersPVCName: "astonish-layers", UppersPVCName: "astonish-uppers", HomesPVCName: os.Getenv("ASTONISH_E2E_HOMES_PVC")},
		})
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
}

type retainedHomeNoGeneration struct{}

func (retainedHomeNoGeneration) Name() string { return "retained-home-no-generation" }
func (retainedHomeNoGeneration) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, fmt.Errorf("home-only scenario must not generate model content"))
	}
}

func NewRetainedHomeBotRuntime(t *testing.T, ctx context.Context, h *Harness, backend sandbox.Backend, pool sandbox.ToolNodePool) *RetainedHomeBotRuntime {
	t.Helper()
	seed := Seed(t, h)
	owner := seed.Users[UserAliceEmail]
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.ID, OrgSlug: owner.OrgSlug, TeamSlug: owner.TeamSlug, Authenticated: true}
	auth, err := execution.WithPrincipal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	org, err := h.Store.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	services, err := launcher.BotChatServicesForOwner(org, p, store.BotScopePersonal)
	if err != nil {
		t.Fatal(err)
	}
	r := &RetainedHomeBotRuntime{Context: auth, Principal: p, Bots: services.PersonalBots, pin: org.ForUser(p.Subject).SetSessionPin}
	r.Manager = &launcher.BotHomeManager{Backend: backend.(sandbox.BotHomeBackend), Pool: pool, Bots: r.Bots}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: h.Store, Sandbox: backend, Pool: pool, WorkerID: "retained-home-readiness", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, got execution.Principal, scope store.BotScope, name, modelName string) (model.LLM, error) {
		if got.Subject != p.Subject || got.OrgSlug != p.OrgSlug || got.TeamSlug != p.TeamSlug || scope != store.BotScopePersonal || name != "retained-home" || modelName != "no-generation" {
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
