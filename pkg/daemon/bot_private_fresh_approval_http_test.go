package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type privateFreshUnknownClassifier struct{}

func (privateFreshUnknownClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown}, nil
}

// Real scoped HTTP admission, production worker/factory/private guard and durable
// approval/continuation store. Only the model and harmless effect are fixtures.
func TestPrivateInteractiveFreshUnknownHTTPExactOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "private-fresh", "team", "telegram", "linked")
			org, err := backend.ForOrg("private-fresh")
			if err != nil {
				t.Fatal(err)
			}
			team, personal := org.ForTeam("team"), org.ForUser(owner)
			repo, pin := team.Bots(), team.SetSessionPin
			if scope == store.BotScopePersonal {
				repo, pin = personal.Bots(), personal.SetSessionPin
			}
			p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "private-fresh", TeamSlug: "team", Authenticated: true}
			auth, err := execution.WithPrincipal(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(8)
			defer registry.Close()
			svc := &store.Services{Mode: store.ModePlatform, Platform: backend, TenantRouter: backend, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), BotTurnRegistry: registry, BotInteractiveReady: true}
			home := testRetainedContentBackend(t, backend)
			llm := &configuredAdmissionModel{script: []configuredAdmissionCall{{"fixture_send", map[string]any{"body": "exact"}}}}
			var effects atomic.Int32
			target, err := functiontool.New(functiontool.Config{Name: "fixture_send", Description: "Count an exact harmless effect"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				if args["body"] != "exact" {
					return nil, fmt.Errorf("changed action %v", args)
				}
				if effects.Add(1) != 1 {
					return nil, fmt.Errorf("replayed effect")
				}
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Exact counter receipt", Source: "fixture://counter", ObservedAt: time.Now(), ActorID: uuid.MustParse(owner)}}})
				return map[string]any{"receipt": "exact counter"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: home, Pool: workerTestPool{}, WorkerID: "private-fresh", TurnRegistry: registry, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				llm.mu.Lock()
				llm.work = func() (*store.BotWorkState, error) { return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease) }
				llm.mu.Unlock()
				s.Services.BotFilesFactory = func(c context.Context, s *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, home, s, scope)
				}
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: &intentHTTPPool{}, Groups: map[string]*agent.ToolGroup{"fixture": {Name: "fixture", Description: "Exact counter", Tools: []tool.Tool{target}}}}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: privateFreshUnknownClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			v := store.DefaultBotVersion()
			v.Charter = "Perform the exact counted action"
			v.Autonomy.AllowedTools = []string{"fixture_send", "bot_work_update"}
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Private fresh", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "fresh"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler)
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := p
				if r.Header.Get("X-Foreign") != "" {
					current.Subject = uuid.NewString()
				}
				c, e := execution.WithPrincipal(r.Context(), current)
				if e != nil {
					t.Error(e)
					return
				}
				c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: current.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if e != nil {
					t.Error(e)
					return
				}
				c = store.WithServices(c, svc)
				c = store.WithTenantContext(c, &store.TenantContext{UserID: current.Subject, OrgSlug: current.OrgSlug, TeamSlug: current.TeamSlug})
				c = api.WithPlatformUser(c, &api.PlatformUser{ID: current.Subject, OrgSlug: current.OrgSlug, TeamSlug: current.TeamSlug, Role: "member"})
				router.ServeHTTP(w, r.WithContext(c))
			}))
			defer server.Close()
			server.Client().Timeout = 30 * time.Second
			post := func(path string, payload any, foreign bool, want int) []byte {
				t.Helper()
				raw, e := json.Marshal(payload)
				if e != nil {
					t.Fatal(e)
				}
				r, e := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/bots/"+b.ID.String()+"/"+path+"?scope="+string(scope), strings.NewReader(string(raw)))
				if e != nil {
					t.Fatal(e)
				}
				r.Header.Set("Content-Type", "application/json")
				if foreign {
					r.Header.Set("X-Foreign", "1")
				}
				resp, e := server.Client().Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				raw, e = io.ReadAll(resp.Body)
				if e != nil {
					t.Fatal(e)
				}
				if resp.StatusCode != want {
					t.Fatalf("POST %s: %d want %d %s", path, resp.StatusCode, want, raw)
				}
				return raw
			}
			var receipt store.BotMessageReceipt
			raw := post("messages", map[string]any{"version": b.Version, "idempotency_key": "fresh-message", "instructions": "Perform fixture_send with body exact"}, false, 202)
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			if err = worker.Poll(auth); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(auth, b.ID, receipt.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != store.RunWaitingApproval || effects.Load() != 0 {
				t.Fatalf("fresh unknown did not checkpoint: %+v effects=%d", saved, effects.Load())
			}
			all, err := repo.ListApprovals(auth, b.ID, store.BotPage{})
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 1 || all[0].Status != store.ApprovalPending {
				t.Fatalf("approvals %+v", all)
			}
			approval := all[0]
			checkpoint, err := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
			if err != nil || !checkpoint.Matches(approval) || approval.Action.Tool != "fixture_send" || string(approval.Action.Arguments) != `{"body":"exact"}` || approval.Action.Effect != execution.EffectUnknown {
				t.Fatalf("non-exact approval: %+v %v", approval, err)
			}
			path := "approvals/" + approval.ID.String() + "/approve-and-continue"
			body := map[string]any{"action_digest": approval.ActionDigest}
			post(path, body, true, 403)
			if effects.Load() != 0 {
				t.Fatal("foreign approval ran effect")
			}
			post(path, body, false, 202)
			if err = worker.Poll(auth); err != nil {
				t.Fatal(err)
			}
			if effects.Load() != 1 {
				saved, _ = repo.GetRun(auth, b.ID, receipt.Run.ID)
				t.Fatalf("approved effect=%d run=%+v", effects.Load(), saved)
			}
			// A settled interactive continuation is rejected rather than requeued.
			post(path, body, false, 409)
			if err = worker.Poll(auth); err != nil {
				t.Fatal(err)
			}
			if effects.Load() != 1 {
				t.Fatal("continuation replayed effect")
			}
		})
	}
}
