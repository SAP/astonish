package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type restartMandateGuard struct {
	request bot.RunRequest
	repo    store.BotStore
}

func (g restartMandateGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if err := g.request.Mandate.Check(ctx); err != nil {
		return execution.PolicyDecision{}, err
	}
	r, err := g.repo.GetRun(ctx, g.request.Bot.ID, g.request.Run.ID)
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	if r.Status != store.RunRunning || r.Lease.Fence != g.request.Run.Lease.Fence || !time.Now().Before(r.Lease.ExpiresAt) {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	if _, err := g.request.Mandate.AuthorizeAction(ctx, a); err != nil {
		return execution.PolicyDecision{}, err
	}
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	d.Kind = execution.PolicyAllow
	return d, nil
}

func testMandateWorkerRestartHTTP(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "web_fetch", nil, []string{"chat", "tool:execute"})
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, "restart observation") }))
	defer upstream.Close()
	origin, _ := tools.RoutineReadOrigin(upstream.URL)
	policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"web_fetch", origin}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
	}
	p, _ := execution.PrincipalFromContext(f.ctx)
	p.Authentication = execution.AuthMethodPlatformJWT
	ctx, err := execution.WithPrincipal(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, &store.Services{Bots: f.repo, BotMandatePolicy: policy})))
	}))
	response, err := http.Post(server.URL+"/bots/"+f.b.ID.String()+"/messages?scope=team", "application/json", strings.NewReader(fmt.Sprintf(`{"version":%d,"instructions":"Read %s","idempotency_key":"restart"}`, f.b.Version, upstream.URL)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	server.Close()
	if response.StatusCode != 202 {
		t.Fatalf("admission %d %s", response.StatusCode, raw)
	}
	var receipt store.BotMessageReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	persisted, err := f.repo.GetRun(ctx, f.b.ID, receipt.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Run = *persisted
	if receipt.Run.MandateRef == nil {
		t.Fatalf("missing mandate: %s", raw)
	}
	f.registry.Close()
	f.server.Close()
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, db, err := entstore.NewPlatformServices(context.Background(), entstore.Config{DSN: filepath.Join(f.pool.dir, "platform.db"), DataDir: f.pool.dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, err := db.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForTeam(p.TeamSlug).Bots()
	// The verifier, quota, leases and factory are real; only the model and home backend are controlled.
	mdl := &interactiveHTTPModel{name: "web_fetch", args: map[string]any{"url": upstream.URL}, principal: execution.Principal{Subject: p.Subject, Authentication: execution.AuthMethodBotMandate, Surface: execution.Surface("daemon")}, t: t}
	mdl.assessment = f.model.assessment
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: journeyHomeBackend{}, Pool: f.pool, MandatePolicy: policy, WorkerID: "restart-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}, Prepare: func(c context.Context, r bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		reads, err := launcher.BotMandatedReadTools(r.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
		if err != nil {
			return nil, err
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", Tools: reads, PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, repo}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, f.b.ID, receipt.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunSucceeded || calls != 1 || run.Usage.ToolCalls != 1 || run.Usage.InputTokens == 0 || run.Usage.CostMicros != nil {
		t.Fatalf("restart execution calls=%d run=%+v", calls, run)
	}
}
