package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// This exercises the installed production factory capability, not an injected
// research runner. End-to-end employee review has separate adapter tests.
func TestBotEmployeeProductionResearchFactory(t *testing.T) {
	f, runtime, episode := researchFixture(t)
	ref := runtime.m.Ref
	f.req.Run.MandateRef = &ref
	f.req.Principal = f.req.Mandate.Authorization.Principal(f.req.Principal.Surface)
	f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
	searchModel := &knowledgeModel{text: "Additional closing sources: https://example.com/breadth"}
	searchTool, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return searchModel, nil }, nil, episode)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch", "write_file", "delegate_tasks"}})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy = policy
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		// No external adapter is used in this construction test.
		return &BotChatSafeResources{Research: episode, Tools: []tool.Tool{searchTool}, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer resources.Cleanup()
	if resources.ResearchFactory == nil {
		t.Fatal("production continuation not installed")
	}
	child, err := policy.Narrow([]string{"perplexity_web_search", "web_fetch"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, policy.Identity()), child)
	request := api.BotEmployeeResearchRequest{Request: f.req, PolicyRuntime: *resources.PolicyRuntime}
	request.Request.Policy = child
	request.PolicyRuntime.Approvals = nil
	fresh, err := resources.ResearchFactory(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Cleanup()
	if fresh.Agent == resources.Agent || fresh.SessionService != resources.SessionService || f.wrapped != 1 {
		t.Fatal("producer/model/session recreated or reused incorrectly")
	}
	if fresh.Agent.SubAgentManager != nil {
		t.Fatal("research producer exposes delegates")
	}
	if len(fresh.Agent.Tools) != 1 || fresh.Agent.Tools[0].Name() != BotConfiguredSearchToolName {
		t.Fatalf("unexpected tools: %v", fresh.Agent.Tools)
	}
	// The original and repair share the exact configured episode and total quota.
	original := resources.Agent.Tools[0].(botRunnableTool)
	if _, err = original.Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": "overview"}); err != nil {
		t.Fatal(err)
	}
	continued := fresh.Agent.Tools[0].(botRunnableTool)
	if _, err = continued.Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": "recent"}); err != nil {
		t.Fatal(err)
	}
	if searchModel.calls != 2 || runtime.calls != 2 || episode.State() != BotResearchSearched {
		t.Fatal("repair did not acquire additional configured sources")
	}
	if _, err = continued.Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": "recent"}); err == nil {
		t.Fatal("repair exceeded original quota")
	}
	// Both model and tools bind the narrowed runtime even when invoked with a
	// caller carrying the original full policy.
	llm := fresh.Agent.LLM.(*botFactoryLLM)
	if llm.binding.req.Budget != f.req.Budget || llm.binding.req.Run.ID != f.req.Run.ID || !llm.researchOnly {
		t.Fatal("research lifetime mismatch")
	}
	bound := llm.binding.bind(execution.WithRunPolicy(ctx, child))
	if agent.BotPolicyRuntimeFromContext(bound).Approvals != nil {
		t.Fatal("approval escape")
	}
	for _, name := range []string{"write_file", "delegate_tasks", "schedule_job", "email_send", "web_fetch"} {
		action := execution.ActionDescriptor{Tool: name, Arguments: json.RawMessage(`{}`), Effect: execution.EffectRead, ConstraintsVerified: true}
		decision := execution.EvaluateRunPolicy(bound, action, time.Now())
		if (decision.Kind == execution.PolicyAllow) != (name == "web_fetch") {
			t.Fatalf("ceiling %s: %v", name, decision)
		}
	}
	// Cross-run substitution is refused without creating another producer.
	request.Request.Run.ID = f.req.Bot.ID
	if _, err = resources.ResearchFactory(ctx, request); err == nil {
		t.Fatal("cross-run research admitted")
	}
}

// guidedHTTPModel transports every producer/reviewer request through a raw
// loopback endpoint; no injected continuation or unconditional passing assessor.
type guidedHTTPModel struct{ endpoint string }

func (*guidedHTTPModel) Name() string { return "guided-http" }
func (m *guidedHTTPModel) GenerateContent(ctx context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		raw, _ := json.Marshal(q)
		request, _ := http.NewRequestWithContext(ctx, "POST", m.endpoint, bytes.NewReader(raw))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			yield(nil, err)
			return
		}
		defer response.Body.Close()
		var out model.LLMResponse
		err = json.NewDecoder(response.Body).Decode(&out)
		yield(&out, err)
	}
}

func TestBotEmployeeGuidedResearchProductionJourney(t *testing.T) {
	for _, scenario := range []struct {
		formatOnly        bool
		continuationError bool
		sourceFailure     bool
	}{{}, {formatOnly: true}, {continuationError: true}, {sourceFailure: true}} {
		formatOnly, continuationError := scenario.formatOnly, scenario.continuationError
		t.Run(fmt.Sprintf("format=%v/continuation-error=%v/source-failure=%v", formatOnly, continuationError, scenario.sourceFailure), func(t *testing.T) {
			f, repo := scheduleFixture(t, store.BotScopePersonal)
			knowledgeSearchSettings(f)
			route, err := BotEffectiveConfiguredSearchRoute(f.ctx, f.services)
			if err != nil {
				t.Fatal(err)
			}
			order := []string{}
			const index = "The broad index rose 2%."
			const breadth = "Advancers outnumbered decliners three to one."
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, r.URL.Path)
				w.Header().Set("Content-Type", "text/plain")
				if r.URL.Path == "/blocked" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, "Source unavailable")
				} else if r.URL.Path == "/thin" {
					fmt.Fprint(w, "Privacy policy | Copyright")
				} else if r.URL.Path == "/index" {
					fmt.Fprint(w, index)
				} else {
					fmt.Fprint(w, breadth)
				}
			}))
			defer source.Close()
			origin, _ := tools.RoutineReadOrigin(source.URL)
			public := &execution.BotPublicResearchScope{Version: 1, Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 2}
			policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{BotConfiguredSearchToolName, "web_fetch", origin}}}, Limits: store.BotMandateLimits(config.DefaultBotMandateConfig()), PublicResearch: public}, nil
			}
			owner, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			owner = store.WithBotMandateAdmission(owner, policy)
			instruction := "Public research subject: Stock Market News\nReport the broad index move and market breadth."
			admission, err := repo.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: f.req.Bot.ID, Version: 1, Instructions: instruction, IdempotencyKey: "guided"})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := repo.Get(owner, f.req.Bot.ID)
			v, _ := repo.GetVersion(owner, b.ID, b.Version)
			id := execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: 1, ActorID: b.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
			worker := execution.WithBotRunIdentity(owner, id)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: id.RunID, WorkerID: "guided", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := entstore.NewBotMandateRuntime(repo, policy)
			if err != nil {
				t.Fatal(err)
			}
			auth, err := execution.VerifyBotMandateAuthorization(worker, rt, *admission.Run.MandateRef, execution.BotMandatePurposeTask)
			if err != nil {
				t.Fatal(err)
			}
			if err := rt.ReserveBotMandateRun(worker, *admission.Run.MandateRef, id.RunID); err != nil {
				t.Fatal(err)
			}
			f.req.Bot = *b
			f.req.Version = *v
			f.req.Run = admission.Run
			f.req.Run.Status = store.RunRunning
			f.req.Run.Lease = lease
			f.req.Instructions = instruction
			f.req.Principal = auth.Principal(execution.SurfaceStudio)
			f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
			f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: rt, Identity: id}
			f.req.Policy, _ = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyScoped, AllowedTools: []string{BotConfiguredSearchToolName, "web_fetch", "write_file", "delegate_tasks", "email_send", "schedule_job"}})
			f.req.Budget, _ = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			f.req.Work, err = repo.(store.BotWorkStore).LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			f.services.PersonalBots = repo
			_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: f.req.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: b.HomeSessionID.String(), State: map[string]any{"execution_owner": "bot"}})
			if err != nil {
				t.Fatal(err)
			}
			episode, _ := NewBotResearchEpisode(id.RunID, f.req.Run.SessionID.String(), BotResearchOpenWeb, nil)
			producerCalls, reviews, searchCalls := 0, 0, 0
			final := index + "\n" + breadth
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var q model.LLMRequest
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
					return
				}
				system := providerSystem(&q)
				var text string
				for _, c := range q.Contents {
					for _, p := range c.Parts {
						text += p.Text
					}
				}
				out := model.LLMResponse{TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}
				if strings.Contains(system, "EMPLOYEE_REPAIR_PROTOCOL_V1") {
					reviews++
					var state store.BotWorkState
					var input continuityContentInput
					_ = json.Unmarshal([]byte(text), &state)
					_ = json.Unmarshal([]byte(text), &input)
					adequate := state.Candidate != nil && state.Candidate.Text == final
					if !strings.Contains(state.Contract.Instructions, "index move and market breadth") {
						t.Error("original assignment lost")
					}
					expectedEvidence := 4
					if scenario.sourceFailure {
						expectedEvidence = 6
					}
					if reviews == 2 && (!strings.Contains(fmt.Sprint(state.Candidate.Bodies), breadth) || len(state.AgentWork.Evidence) != expectedEvidence) {
						t.Error("additional durable journal references not rehydrated")
					}
					assessment := func(items continuityContentInput) map[string]any {
						verdict := store.BotCriterionUnverified
						if adequate {
							verdict = store.BotCriterionSatisfied
						}
						criteria := []store.BotCriterionAssessment{{CriterionID: "employee-assignment", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + state.Candidate.Digest}, Explanation: "Breadth requires decisive closing statistics, not only index coverage."}}
						content := []continuityContentAssessment{}
						for _, item := range items.Items {
							claim := strings.Trim(item.Text, "*")
							a := continuityContentAssessment{ItemID: item.ID, ClaimType: continuityFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: "Exact decisive source passage required"}
							for ref, body := range input.Sources {
								if strings.HasPrefix(ref, "source_") && (claim == index || claim == breadth) && strings.Contains(body, claim) {
									a.Support = store.BotCriterionSatisfied
									a.Sources = []continuityCitation{{Ref: ref, Quote: claim}}
									break
								}
							}
							content = append(content, a)
						}
						return map[string]any{"candidate_digest": state.Candidate.Digest, "criteria": criteria, "content_assessments": content}
					}
					response := assessment(input)
					if formatOnly {
						if !adequate {
							t.Error("format candidate not adequate")
						}
						response["repair_kind"] = "formatting"
						response["formatted_text"] = "**" + index + "**\n**" + breadth + "**"
						response["formatted_review"] = assessment(input)
					} else if !adequate {
						response["repair_kind"] = "research"
						response["missing_dimensions"] = []string{"market breadth"}
						response["source_needs"] = []string{"primary closing statistics"}
						response["public_queries"] = []string{"Stock Market News market breadth"}
					} else {
						response["repair_kind"] = "none"
					}
					raw, _ := json.Marshal(response)
					out.Content = genai.NewContentFromText(string(raw), "model")
				} else {
					producerCalls++
					if reviews > 0 {
						for name := range q.Tools {
							if name != "perplexity_web_search" && name != "web_fetch" {
								t.Errorf("continuation offered %s", name)
							}
						}
						if q.Config != nil {
							for _, configured := range q.Config.Tools {
								for _, declaration := range configured.FunctionDeclarations {
									if declaration.Name != BotConfiguredSearchToolName && declaration.Name != "web_fetch" {
										t.Errorf("continuation declaration offered %s", declaration.Name)
									}
								}
							}
						}
						if !strings.Contains(text, "market breadth") || !strings.Contains(text, "primary closing statistics") || !strings.Contains(text, "Stock Market News market breadth") {
							t.Error("exact guidance lost")
						}
					}
					round := producerCalls
					if formatOnly {
						round = producerCalls
					}
					var name string
					var args map[string]any
					switch round {
					case 1:
						name = BotConfiguredSearchToolName
						args = map[string]any{"facet": "overview"}
					case 2:
						name = "web_fetch"
						args = map[string]any{"url": source.URL + "/index"}
					case 3:
						if formatOnly {
							name = BotConfiguredSearchToolName
							args = map[string]any{"facet": "recent"}
						}
					case 4:
						if formatOnly {
							name = "web_fetch"
							args = map[string]any{"url": source.URL + "/breadth"}
						} else {
							name = BotConfiguredSearchToolName
							args = map[string]any{"facet": "recent"}
						}
					case 5:
						if !formatOnly {
							name = "web_fetch"
							args = map[string]any{"url": source.URL + "/breadth"}
						}
					}
					if scenario.sourceFailure {
						switch round {
						case 5:
							name, args = "web_fetch", map[string]any{"url": source.URL + "/blocked"}
						case 6:
							name, args = "web_fetch", map[string]any{"url": source.URL + "/thin"}
						case 7:
							name, args = "web_fetch", map[string]any{"url": source.URL + "/breadth"}
						}
					}
					if name != "" {
						out.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("guided-%d", round), Name: name, Args: args}}}}
					} else {
						answer := final
						if !formatOnly && round == 3 {
							answer = index
						}
						out.Content = genai.NewContentFromText(answer, "model")
						if continuationError && round == 6 {
							// The revised answer and the additional tool captures survive,
							// but the producer reports an operational completion failure.
							out.ErrorCode = "continuation_failed"
							out.ErrorMessage = "controlled continuation failure"
						}
					}
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			llm := &guidedHTTPModel{endpoint: server.URL}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				search, err := BotConfiguredResearchTool(ctx, req, s, func(context.Context, string, string, *config.AppConfig) (model.LLM, error) {
					searchCalls++
					order = append(order, "search")
					url := source.URL + "/index"
					if searchCalls == 2 {
						url = source.URL + "/breadth"
					}
					return &knowledgeModel{text: "Sources: " + url}, nil
				}, nil, episode)
				if err != nil {
					return nil, err
				}
				reads, err := BotMandatedReadTools(req.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
				if err != nil {
					return nil, err
				}
				return &BotChatSafeResources{Research: episode, Tools: append([]tool.Tool{search}, reads...), WorkspaceDir: t.TempDir(), PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
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
			if continuationError {
				state, loadErr := repo.(store.BotWorkStore).LoadWork(worker, lease)
				if loadErr != nil || result.Status != store.RunPartial || state.Phase != store.BotWorkBlocked || state.HasAcceptedEmployeeReview() || len(state.AgentWork.Evidence) != 4 || reviews != 1 || producerCalls != 6 {
					t.Fatalf("failed continuation lost fenced limitation/evidence: status=%s state=%+v load=%v reviews=%d producer=%d", result.Status, state, loadErr, reviews, producerCalls)
				}
				if result.Report.Work.WorkRevision != state.Revision || result.Report.ReviewLimitation == nil {
					t.Fatal("limitation is not bound to latest continuation revision")
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: result.Usage}); err != nil {
					t.Fatal(err)
				}
				committed, commitErr := repo.GetRun(owner, b.ID, id.RunID)
				if commitErr != nil {
					t.Fatal(commitErr)
				}
				message := committed.Report.EmployeeMessage
				if committed.Status != store.RunPartial || message == nil || message.Reason != store.BotEmployeeOutcomeReason(result.Status, result.Report) || strings.TrimSpace(message.Text) == "" || message.Text == final || message.Text == state.Candidate.Text {
					t.Fatalf("failed continuation did not publish its safe committed outcome: %+v", committed)
				}
				return
			}
			expected := final
			if formatOnly {
				expected = "**" + index + "**\n**" + breadth + "**"
			}
			state, loadErr := repo.(store.BotWorkStore).LoadWork(worker, lease)
			if result.Status != store.RunSucceeded || result.Report.Summary != expected || len(result.Report.Unverified) != 0 || loadErr != nil || !state.HasAcceptedEmployeeReview() {
				t.Fatalf("journey not accepted: result=%+v work=%+v err=%v", result, state, loadErr)
			}
			wantOrder, wantTools := "[search /index search /breadth]", int64(4)
			if scenario.sourceFailure {
				wantOrder, wantTools = "[search /index search /blocked /thin /breadth]", 6
				if len(state.AgentWork.SourceAccess) != 4 {
					t.Fatal("source access facts missing")
				}
				facts := map[string]store.BotSourceAccessFact{}
				for _, fact := range state.AgentWork.SourceAccess {
					facts[fact.CallID] = fact
					if fact.BodyDigest == "" || fact.EvidenceRef != "capture:"+fact.CallID {
						t.Fatal("source failure is not invocation-bound")
					}
				}
				if facts["guided-5"].Status != store.BotSourceAccessFailed || facts["guided-5"].HTTPStatus != 403 || facts["guided-6"].Status != store.BotSourceAccessReturned || facts["guided-6"].HTTPStatus != 200 {
					t.Fatalf("negative/thin sources lost: %+v", facts)
				}
				if !strings.Contains(fmt.Sprint(state.Candidate.Bodies), "Source unavailable") || !strings.Contains(fmt.Sprint(state.Candidate.Bodies), "Privacy policy | Copyright") {
					t.Fatal("negative/thin source bodies not retained for second review")
				}
			}
			if fmt.Sprint(order) != wantOrder || searchCalls != 2 || result.Usage.ToolCalls != wantTools {
				t.Errorf("research order/usage: %v %+v", order, result.Usage)
			}
			wantReviews := 2
			wantProducer := 6
			if scenario.sourceFailure {
				wantProducer = 8
			}
			if formatOnly {
				wantReviews = 1
				wantProducer = 5
			}
			if reviews != wantReviews || producerCalls != wantProducer {
				t.Fatalf("replayed rounds: producer=%d reviews=%d", producerCalls, reviews)
			}
			if !strings.Contains(fmt.Sprint(state.Candidate.Bodies), breadth) {
				t.Fatal("additional persisted evidence not rehydrated")
			}
			quotaErr := rt.(interface {
				ReserveBotPublicResearch(context.Context, execution.BotMandateRef, string) error
			}).ReserveBotPublicResearch(worker, *admission.Run.MandateRef, f.req.Run.SessionID.String())
			if quotaErr == nil {
				t.Error("continuation escaped same durable two-call research quota")
			}
			if got := f.req.Budget.(*bot.SharedRunBudget).Usage(); got.InputTokens != 6 || got.OutputTokens != 10 || got.ToolCalls != wantTools {
				t.Errorf("shared research accounting mismatch: %+v", got)
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: result.Usage}); err != nil {
				t.Fatal(err)
			}
			committed, err := repo.GetRun(owner, b.ID, id.RunID)
			if err != nil || committed.Report.Summary != expected || committed.Report.EmployeeMessage == nil || committed.Report.EmployeeMessage.Text != expected {
				t.Fatalf("committed final missing: %+v %v", committed, err)
			}
		})
	}
}
