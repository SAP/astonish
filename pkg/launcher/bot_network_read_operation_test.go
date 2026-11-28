package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
)

type networkProgressProbe struct {
	progressDispatchProbe
	t         *testing.T
	preflight context.Context
}

func (p *networkProgressProbe) Begin(c context.Context, k store.BotProgressDispatchKind, d string, u store.BotUsage) (*store.BotProgressDispatchReceipt, error) {
	if entstore.WithBotContentReadOperationFromContext(context.Background(), c) == context.Background() {
		p.t.Error("progress reservation lost preflight scope")
	}
	p.preflight = c
	return p.progressDispatchProbe.Begin(c, k, d, u)
}

func TestNativeNetworkFullBoundGuardScopes(t *testing.T) {
	for _, name := range []string{"web_fetch", "http_request"} {
		t.Run(name, func(t *testing.T) {
			f, r, _ := researchFixture(t)
			f.req.Policy, _ = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{name}})
			progress := &networkProgressProbe{t: t}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				if progress.preflight == nil || entstore.WithBotContentReadOperationFromContext(context.Background(), progress.preflight) != context.Background() {
					t.Error("preflight survived network execution")
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("weather clear"))
			}))
			defer server.Close()
			origin, _ := tools.RoutineReadOrigin(server.URL)
			r.m.Constraints[0].Resources = []string{name, origin}
			auth, err := execution.VerifyBotMandateAuthorization(f.ctx, r, r.m.Ref, r.m.Purpose)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate.Authorization = auth
			probe := &nativeSearchReadProbe{researchTestRuntime: r, t: t}
			f.req.Mandate.Runtime = probe
			f.req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			var policies int
			catalog, err := BotMandatedReadTools(f.req.Mandate, tools.RoutineReadPolicy{Authorize: func(c context.Context, _ string) error {
				policies++
				if entstore.WithBotContentReadOperationFromContext(context.Background(), c) == context.Background() {
					t.Error("network authorization missing bounded reads")
				}
				return nil
			}, AllowIP: func(context.Context, string, net.IP) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			owner := store.WithServices(f.ctx, f.services)
			owner = execution.WithBotRunIdentity(owner, f.req.Policy.Identity())
			owner = execution.WithRunPolicy(owner, f.req.Policy)
			owner, _ = bot.WithToolOutcomes(owner)
			owner = bot.WithProgressDispatch(owner, progress)
			owner = agent.WithBotPolicyRuntime(owner, agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: WithBotMandatedReadClassifier(botFactoryTestClassifier{}, catalog)})
			b := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }, progress: &botProgressBridge{}}
			target := &botBoundTool{botRunnableTool: catalog[0].(botRunnableTool), binding: b}
			ctx := mandatedDigestContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: owner}, sid: f.req.Run.SessionID.String()}}
			result, err := target.Run(ctx, map[string]any{"url": server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d result=%v", requests.Load(), result)
			}
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if !ok || len(outcome.SourceAccess) != 1 {
				t.Fatalf("missing receipt: %+v result=%v", outcome, result)
			}
			raw, _ := json.Marshal(result)
			var persisted map[string]any
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(persisted)
			sum := sha256.Sum256(raw)
			if outcome.SourceAccess[0].BodyDigest != hex.EncodeToString(sum[:]) {
				t.Fatal("returned envelope digest changed")
			}
			for _, retained := range probe.retained {
				if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
					t.Error("guard scope survived return")
				}
			}
			if probe.unscoped != 0 {
				t.Fatalf("unscoped guards=%d", probe.unscoped)
			}
			if policies < 3 {
				t.Fatalf("network authority not refreshed: %d", policies)
			}
			if err := b.progress.boundary(owner); err != nil {
				t.Fatal(err)
			}
			if progress.settled != 1 || progress.outcome != store.BotProgressDispatchSettled {
				t.Fatalf("progress=%+v", progress)
			}
			t.Logf("mandate checks=%d fresh network policy checks=%d HTTP dispatches=%d", len(probe.retained), policies, requests.Load())
		})
	}
}
