package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Controlled model and source; real scoped SQLite, worker, mandate checks,
// quota reservations, progress dispatches and evidence persistence. This measures
// orchestration only, not production network or provider latency.
func TestBotProgressMandateTwelveReadProfile(t *testing.T) {
	for _, scenario := range []struct {
		mandated   bool
		ledgerRows int
	}{{false, 0}, {true, 0}, {true, 2000}} {
		mandated := scenario.mandated
		t.Run(fmt.Sprintf("mandated=%t/ledger=%d", mandated, scenario.ledgerRows), func(t *testing.T) {
			f := newProgressHTTPFixture(t, store.BotScopePersonal)
			payload := strings.Repeat("Independent observation of measured revenue and costs. ", 300)
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, payload) }))
			defer upstream.Close()
			origin, _ := tools.RoutineReadOrigin(upstream.URL)
			policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				limits := execution.BotMandateLimits(config.DefaultBotMandateConfig())
				limits.MaxTokens = 2000000
				limits.MaxTotalTokens = 20000000
				return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy(), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"web_fetch", origin}}}, Limits: limits, TTL: time.Hour}, nil
			}
			*f.policy = policy
			v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
			if err != nil {
				t.Fatal(err)
			}
			v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			raw, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated mandate timing", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err = json.Unmarshal(f.request("PUT", f.path+"/configuration", string(raw), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			*f.policy = nil
			progressResumeFixture(t, f)
			steps := 0
			prior := time.Now()
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				t.Logf("model_entry=%d elapsed_since_previous=%s", steps, time.Since(prior))
				prior = time.Now()
				if steps < 12 {
					steps++
					name := "read_file"
					args := map[string]any{"path": "receipt.txt"}
					if mandated {
						name = "web_fetch"
						args = map[string]any{"url": fmt.Sprintf("%s/%d", upstream.URL, steps)}
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("read-%d", steps), Name: name, Args: args}}}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
				}
				return &model.LLMResponse{Content: genai.NewContentFromText("Compared all twelve observations: measured revenue and costs agree.", "model")}, true
			}
			f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
				if mandated {
					if name != tools.BotPublicFetchOperation {
						return nil, fmt.Errorf("unexpected profile tool %q", name)
					}
					// Shared Chat dispatches the ordinary web_fetch through the node
					// pool, not the legacy replacement in safe.Tools. Exercise its
					// real handler with only this fixture's pinned source permitted.
					raw, err := json.Marshal(args)
					if err != nil {
						return nil, err
					}
					var fetch tools.WebFetchArgs
					if err = json.Unmarshal(raw, &fetch); err != nil {
						return nil, err
					}
					ctx := tools.WithRoutineReadPolicy(f.ctx, tools.RoutineReadPolicy{
						Authorize: func(_ context.Context, requested string) error {
							if requested != origin {
								return store.ErrBotForbidden
							}
							return nil
						},
						AllowIP: func(context.Context, string, net.IP) error { return nil },
					})
					result, err := tools.WebFetch(contentFetchContext{ctx: ctx}, fetch)
					if err != nil {
						return nil, err
					}
					return json.Marshal(tools.BotPublicFetchResult{ReadExecuted: true, Response: result})
				}
				return json.Marshal(map[string]any{"content": payload})
			}
			cfg := f.workerConfig
			var seedElapsed time.Duration
			cfg.ContextWindow = 200000
			var receipt *store.BotMessageReceipt
			if mandated {
				p, _ := execution.PrincipalFromContext(f.ctx)
				p.Authentication = execution.AuthMethodPlatformJWT
				auth, e := execution.WithPrincipal(f.ctx, p)
				if e != nil {
					t.Fatal(e)
				}
				auth, e = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if e != nil {
					t.Fatal(e)
				}
				receipt, err = f.repo.(store.BotMessageStore).AdmitMessage(store.WithBotMandateAdmission(auth, policy), store.BotMessageRequest{BotID: f.b.ID, Version: f.b.Version, Instructions: "Compare twelve independent public observations.", IdempotencyKey: "profile"})
				if err != nil {
					t.Fatal(err)
				}
				if receipt.Run.MandateRef == nil {
					t.Fatal("missing durable mandate")
				}
				cfg.MandatePolicy = policy
				cfg.Prepare = func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.pool.dir}, repo: f.repo, id: r.Bot.ID, scope: f.scope}
					files.install(c, r, s)
					f.model.principal = r.Principal
					seedStart := time.Now()
					for i := 0; i < scenario.ledgerRows; i++ {
						_, e := f.repo.AppendActivity(c, r.Run.Lease, store.BotActivity{BotID: r.Bot.ID, RunID: &r.Run.ID, Kind: store.BotActivityRun, Payload: json.RawMessage(`{"note":"old scoped activity"}`)})
						if e != nil {
							return nil, e
						}
					}
					seedElapsed = time.Since(seedStart)
					prior = time.Now()
					reads, e := launcher.BotMandatedReadTools(r.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
					if e != nil {
						return nil, e
					}
					return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, Tools: reads, PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, f.repo}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
				}
			} else {
				raw, _ = json.Marshal(store.BotMessageRequest{Version: f.b.Version, Instructions: "Compare twelve independent public observations.", IdempotencyKey: "profile"})
				receipt = &store.BotMessageReceipt{}
				err = json.Unmarshal(f.request("POST", f.path+"/messages", string(raw), 202), receipt)
				if err != nil {
					t.Fatal(err)
				}
			}
			worker, err := daemon.NewBotWorker(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			started := time.Now()
			prior = started
			if err = worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(started)
			run, err := f.repo.GetRun(f.ctx, f.b.ID, receipt.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != store.RunSucceeded || run.Usage.ToolCalls != 12 || steps != 12 {
				sourceResponses, _ := json.Marshal(progressPrivateResponses(t, f))
				t.Logf("private source responses: %s", sourceResponses)
				t.Fatalf("status=%s usage=%+v steps=%d report=%+v", run.Status, run.Usage, steps, run.Report)
			}
			if mandated && calls.Load() != 12 {
				t.Fatalf("source calls=%d", calls.Load())
			}
			t.Logf("mandated=%t ledger_rows=%d reads=%d payload_bytes=%d worker_elapsed_without_seeding=%s", mandated, scenario.ledgerRows, steps, len(payload), elapsed-seedElapsed)
		})
	}
}
