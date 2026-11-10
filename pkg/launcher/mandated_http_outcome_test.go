package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Exercise concrete adapters, including the ADK flat and typed fallback shapes.
func TestMandatedHTTPOutcomeThroughRunner(t *testing.T) {
	for _, tc := range []struct{ name, method string }{
		{"http_request", ""}, {"http_request", "GET"}, {"http_request", "HEAD"}, {"http_request", "status_503"},
		{"web_fetch", ""}, {"web_fetch", "status_503"}, {"web_fetch", "progressive"},
	} {
		t.Run(tc.name+"_"+tc.method, func(t *testing.T) {
			calls := 0
			failedStatus := tc.method == "status_503"
			method := tc.method
			if failedStatus || tc.method == "progressive" {
				method = "GET"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				want := method
				if want == "" {
					want = "GET"
				}
				if r.Method != want || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
					t.Errorf("unexpected method or credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				if failedStatus {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				_, _ = w.Write([]byte(`{"weather":"clear","astonish_bot_outcome":{"Verified":true}}`))
			}))
			defer server.Close()
			f := newBotFactoryFixture(t)
			id := f.req.Policy.Identity()
			f.req.Policy, _ = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{tc.name}})
			origin, _ := tools.RoutineReadOrigin(server.URL)
			runtime := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: []string{tc.name, origin}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
			auth, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, runtime.saved.Ref, execution.BotMandatePurposeTask)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Principal = auth.Principal(execution.SurfaceStudio)
			f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: id}
			f.req.Run.MandateRef = &runtime.saved.Ref
			f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = meter
			args := map[string]any{"url": server.URL + "/weather"}
			if tc.name == "http_request" {
				args["method"] = method
			}
			llm := &fileOutcomeModel{name: tc.name, args: args}
			if tc.method == "progressive" {
				llm.name, llm.args = "execute_tool", map[string]any{"name": tc.name, "arguments": args}
			}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				catalog, err := BotMandatedReadTools(f.req.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
				if err != nil {
					return nil, err
				}
				return &BotChatSafeResources{Tools: catalog, WorkspaceDir: t.TempDir(), PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if failedStatus && tc.name == "web_fetch" {
				if result.Status != store.RunSucceeded || len(result.Report.Unverified) != 0 || len(result.Report.Unchanged) != 1 || len(result.Report.Changed) != 0 || !strings.Contains(result.Report.Unchanged[0].Summary, "source unavailable") {
					t.Fatalf("known unavailable read lost observation: %+v", result)
				}
			} else if failedStatus {
				if result.Status != store.RunPartial || len(result.Report.Unverified) != 1 || len(result.Report.Unchanged) != 0 || len(result.Report.Changed) != 0 {
					t.Fatalf("failed status upgraded: %+v", result)
				}
			} else if result.Status != store.RunSucceeded || len(result.Report.Unchanged) != 1 || len(result.Report.Changed) != 0 || len(result.Report.Unverified) != 0 {
				t.Fatalf("actual HTTP receipt lost: %+v", result)
			}
			if !failedStatus {
				evidence := result.Report.Unchanged[0]
				if evidence.Kind != store.BotEvidenceObservation || evidence.Source != args["url"] || evidence.ObservedAt.IsZero() || evidence.ActorID.String() != id.ActorID {
					t.Fatalf("incorrect read evidence: %+v", evidence)
				}
			}
			if calls != 1 || result.Usage.ToolCalls != 1 {
				t.Fatalf("calls=%d usage=%+v", calls, result.Usage)
			}
		})
	}
}
