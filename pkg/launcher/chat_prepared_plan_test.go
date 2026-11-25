package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// SQLite, production factory and actual file/shell dispatcher; node transport
// and activation readiness are controlled. No physical sandbox claim.
func TestBotPreparedPlanAnnouncementResume(t *testing.T) {
	testBotPreparedPlanLifecycle(t, false)
}

func TestBotPreparedPlanVerifierFailureResume(t *testing.T) {
	testBotPreparedPlanLifecycle(t, true)
}

func testBotPreparedPlanLifecycle(t *testing.T, failVerification bool) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = store.BotScope(scope)
			r := &servicesParityRuntime{f: f}
			m := newLiveMembershipFixture(t, r, scope)
			repo := m.repo.BotStore
			f.services.Bots, f.services.PersonalBots = repo, repo
			db := f.services.Platform.(*entstore.Store)
			db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			b, err := repo.Get(f.ctx, m.repo.probe)
			must(err)
			b, err = repo.Activate(f.ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "prepared plan fixture"})
			must(err)
			f.req.Bot = *b
			v, err := repo.GetVersion(f.ctx, b.ID, b.Version)
			must(err)
			f.req.Version = *v
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			t.Cleanup(registry.Close)
			f.ctx, err = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			must(err)
			f.services.BotTurnRegistry, f.services.BotInteractiveReady = registry, true
			mode := true
			receipt, err := repo.(store.BotMessageStore).AdmitMessage(store.WithBotMessageAdmission(f.ctx, registry), store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "prepared", Instructions: "Announce the controlled plan", PlanMode: &mode})
			must(err)
			run := &receipt.Run
			id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
			worker := execution.WithBotRunIdentity(f.ctx, id)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "prepared", ExpiresAt: time.Now().Add(time.Minute)})
			must(err)
			run, err = repo.GetRun(worker, b.ID, run.ID)
			must(err)
			f.req.Run = *run
			f.req.Authorization, err = registry.Authorizer(id, *run.CallerCeiling, m.current)
			must(err)
			f.req.PlanMode = true
			f.req.Policy = nil
			ods, err := db.ForOrg(f.req.Principal.OrgSlug)
			must(err)
			scoped, err := BotChatServicesForOwner(ods, f.req.Principal, store.BotScope(scope))
			must(err)
			f.sessions = scoped.PersonalSessions
			if scope == chatruntime.ResourceScopeTeam {
				f.sessions = scoped.Sessions
			}
			f.ctx = agent.WithPromptOverrides(bot.WithApprovalStore(worker, repo), &agent.PromptOverrides{PlanMode: true})
			root := t.TempDir()
			node := &servicesParityNode{backend: &productionCapabilityBackend{}, root: root, session: run.SessionID.String()}
			planArgs := servicesParityPlanArgs()
			if failVerification {
				planArgs["steps"].([]any)[0].(map[string]any)["verify"] = "printf services-parity-failed; exit 7"
			}
			llm := model.LLM(&fileOutcomeModel{name: "announce_plan", args: planArgs})
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: productionCapabilityPool{backend: node.backend, client: preparedLifecycleNode{servicesParityNode: node}}, Backend: node.backend}, WorkspaceDir: root, PolicyRuntime: agent.BotPolicyRuntime{Classifier: BotCatalogueClassifier{}, Guard: preparedFixtureGuard{repo: repo}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			must(err)
			adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
				res, err := factory(ctx, req)
				if err != nil {
					return nil, fmt.Errorf("factory: %w", err)
				}
				return res, nil
			})
			must(err)
			sink := func(_ context.Context, e bot.RuntimeEvent) error { t.Logf("%s %s", e.Type, e.Payload); return nil }
			waiting, err := adapter.Start(f.ctx, f.req, sink)
			must(err)
			// Interactive Chat already has owner tool authority; definition consent
			// does not manufacture an exact-effect grant. The supervised autonomous
			// fixtures below exercise the separate mirror/verifier approvals.
			if waiting.Status != store.RunWaitingApproval || !reflect.DeepEqual(node.calls, []string{"write_file"}) {
				t.Fatalf("initial %+v calls=%v", waiting, node.calls)
			}
			result := waiting
			bytes, err := os.ReadFile(filepath.Join(root, "PLAN.md"))
			must(err)
			sess, err := f.sessions.Get(worker, &session.GetRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: run.SessionID.String()})
			must(err)
			value, err := sess.Session.State().Get(agent.PlanSnapshotStateKey)
			must(err)
			raw, err := json.Marshal(value)
			must(err)
			var published agent.PlanSnapshot
			must(json.Unmarshal(raw, &published))
			if published.Document.Lifecycle != "" || published.Document.Results != "" || !strings.Contains(string(bytes), published.Goal) {
				t.Fatal("definition snapshot not persisted before waiting")
			}
			// The announcement mirror approval is not definition consent. Persist
			// the new typed wait and use the actual owner HTTP continuation.
			must(repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Checkpoint: result.Checkpoint, Report: result.Report}))
			var definitionCheckpoint bot.ApprovalCheckpoint
			must(json.Unmarshal(result.Checkpoint, &definitionCheckpoint))
			approvals, err := repo.ListApprovals(worker, b.ID, store.BotPage{Limit: 100})
			must(err)
			var definitionApproval *store.BotApproval
			for i := range approvals {
				if approvals[i].ID == definitionCheckpoint.ApprovalID {
					definitionApproval = &approvals[i]
				}
			}
			if definitionApproval == nil {
				t.Fatal("missing definition approval")
			}
			definition, err := definitionApproval.Interaction.PlanDefinition()
			must(err)
			if definitionApproval.Action.Tool != "ask_user" || definition.SessionID != run.SessionID.String() || definition.RunID != run.ID {
				t.Fatal("definition consent reused mirror grant or changed origin")
			}
			httpCtx := store.WithServices(f.ctx, f.services)
			httpCtx = store.WithTenantContext(httpCtx, &store.TenantContext{UserID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug})
			continueHTTP := func(consent store.BotPlanExecutionConsent, key string, want int) {
				t.Helper()
				body, err := json.Marshal(store.BotContinueInteractiveRequest{ExpectedRevision: b.Revision, IdempotencyKey: key, ApprovedPlanExecution: true, PlanExecution: &consent})
				must(err)
				request := httptest.NewRequest("POST", "/api/studio/bots/"+b.ID.String()+"/runs/"+run.ID.String()+"/continue-interactive?scope="+string(scope), strings.NewReader(string(body))).WithContext(httpCtx)
				request = mux.SetURLVars(request, map[string]string{"botID": b.ID.String(), "runID": run.ID.String()})
				response := httptest.NewRecorder()
				api.BotContinueInteractiveHandler(response, request)
				if response.Code != want {
					t.Fatalf("consent HTTP %d want %d: %s", response.Code, want, response.Body.String())
				}
			}
			staleDefinition := definition
			staleDefinition.Sequence++
			continueHTTP(staleDefinition, "stale-definition", 409)
			m.revoke()
			continueHTTP(definition, "revoked-definition", 403)
			m.restore()
			registry.Cancel(id)
			continueHTTP(definition, "owner-definition", 202)
			continueHTTP(definition, "completed-definition-replay", 409)
			lease, err = repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "owner-consent", ExpiresAt: time.Now().Add(time.Minute)})
			must(err)
			run, err = repo.GetRun(worker, b.ID, run.ID)
			must(err)
			if run.ID != definition.RunID || run.SessionID.String() != definition.SessionID {
				t.Fatal("owner consent queued a new run/session")
			}
			f.req.Run = *run
			f.req.Authorization, err = registry.Authorizer(id, *run.CallerCeiling, m.current)
			must(err)
			f.req.PlanMode = false
			f.ctx = agent.WithPromptOverrides(bot.WithApprovalStore(worker, repo), &agent.PromptOverrides{})
			llm = &preparedLifecycleModel{calls: []preparedLifecycleCall{
				{"update_plan", map[string]any{"step": "verify", "status": "running"}},
				{"update_plan", map[string]any{"step": "verify", "status": "complete"}},
				{"announce_completion", map[string]any{"outcome_observed": "Local verifier executed exactly once", "unverified": "Physical sandbox"}},
			}}
			result, err = adapter.Resume(f.ctx, f.req, sink)
			must(err)
			assertPlan := func(status string, results bool) {
				t.Helper()
				current, err := f.sessions.Get(worker, &session.GetRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: run.SessionID.String()})
				must(err)
				value, err := current.Session.State().Get(agent.PlanSnapshotStateKey)
				must(err)
				raw, err := json.Marshal(value)
				must(err)
				var snapshot agent.PlanSnapshot
				must(json.Unmarshal(raw, &snapshot))
				if snapshot.PlanID != definition.PlanID || snapshot.Steps[0].Status != status || (snapshot.Document.Results != "") != results {
					t.Fatalf("unexpected trusted plan: %+v", snapshot)
				}
			}
			if failVerification {
				assertPlan("failed", false)
				bytes, err = os.ReadFile(filepath.Join(root, "PLAN.md"))
				must(err)
				if !strings.Contains(string(bytes), "exit 7") || strings.Contains(string(bytes), "## Results") {
					t.Fatalf("failure evidence missing or false completion: %s", bytes)
				}
				verifies := 0
				for _, call := range node.calls {
					if call == "shell_command" {
						verifies++
					}
				}
				if verifies != 1 {
					t.Fatalf("failed verifier replayed: %v", node.calls)
				}
				return
			}
			assertPlan("complete", true)
			bytes, err = os.ReadFile(filepath.Join(root, "PLAN.md"))
			must(err)
			if !strings.Contains(string(bytes), "## Results") || !strings.Contains(string(bytes), "services-parity-ok") || !strings.Contains(string(bytes), "Local verifier executed exactly once") {
				t.Fatalf("missing results/evidence: %s", bytes)
			}
			verifies := 0
			for _, call := range node.calls {
				if call == "shell_command" {
					verifies++
				}
			}
			if verifies != 1 {
				t.Fatalf("verifier replay: calls=%v", node.calls)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report}); err != nil {
				t.Fatalf("commit prepared Results: %v work=%+v", err, f.req.Work)
			}
		})
	}
}

// The negative verifier uses the same dispatcher, bounded to one literal command
// in the test-owned directory; never return fabricated exit evidence.
type preparedLifecycleNode struct{ *servicesParityNode }

func (n preparedLifecycleNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	if name != "shell_command" || args["command"] != "printf services-parity-failed; exit 7" {
		return n.servicesParityNode.Call(id, name, args)
	}
	if id != n.session || args["working_dir"] != n.root {
		return nil, store.ErrBotForbidden
	}
	n.calls = append(n.calls, name)
	result, err := tools.ExecuteTool(context.Background(), name, args, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// Like the production guard, verify the saved approval without consuming it:
// the prepared store owns atomic consumption with dispatch intent.
type preparedFixtureGuard struct{ repo store.BotStore }

func (g preparedFixtureGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		s := g.repo
		id, _ := execution.BotRunIdentityFromContext(ctx)
		run, err := s.GetRun(ctx, id.BotID, id.RunID)
		if err != nil {
			return d, err
		}
		approval, err := s.(store.BotApprovalResumeStore).LoadResumeApproval(ctx, run.Lease)
		if err != nil {
			return d, err
		}
		if approval.ID != d.GrantID || approval.ActionDigest != d.ActionDigest {
			return d, store.ErrBotForbidden
		}
		d.Kind = execution.PolicyAllow
	}
	return d, nil
}

// Keep the complete owner-approved implementation in one genuine Chat turn;
// ending a producer after each tool would incorrectly finalize its work state.
type preparedLifecycleCall struct {
	name string
	args map[string]any
}
type preparedLifecycleModel struct {
	model.LLM
	calls []preparedLifecycleCall
	step  int
}

func (*preparedLifecycleModel) Name() string { return "prepared-lifecycle" }
func (m *preparedLifecycleModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		content := genai.NewContentFromText("Observed verifier and plan state.", "model")
		if m.step < len(m.calls) {
			c := m.calls[m.step]
			m.step++
			name, args := c.name, c.args
			if req.Tools[name] == nil && req.Tools["execute_tool"] != nil {
				name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
			}
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("prepared-%d", m.step), Name: name, Args: args}}}}
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}
