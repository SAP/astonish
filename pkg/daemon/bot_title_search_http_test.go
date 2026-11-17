package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
	"google.golang.org/genai"
)

type titleResearchHTTPModel struct {
	questionAsked bool
	reviews       int
	step          int
	url           string
	missing       bool
}

func (titleResearchHTTPModel) Name() string { return "title-research-fixture" }
func (m *titleResearchHTTPModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if !m.questionAsked {
		m.questionAsked = true
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "title-search-question", Name: "ask_user", Args: map[string]any{
				"prompt": "Which AI news assignment should I carry out?", "kind": "select", "allow_custom_answer": true,
				"options": []any{map[string]any{"id": "brief", "label": "Research current AI news"}, map[string]any{"id": "compare", "label": "Compare AI research announcements"}, map[string]any{"id": "sources", "label": "Find original AI news sources"}},
			}}}}}}, nil)
		}
	}
	if response, ok := m.review(request); ok {
		return response
	}
	if response, ok := workerWorkAssessment(request, nil); ok {
		return response
	}
	m.step++
	name, args := launcher.BotConfiguredSearchToolName, map[string]any{"facet": "overview"}
	if m.step == 2 {
		name, args = "web_fetch", map[string]any{"url": m.url}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.step > 2 || m.missing {
			text := "Decisive AI news source details."
			if m.missing {
				text = "Configured search unavailable; no detail source fetched. Configure scoped search before researching AI News."
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model"), TurnComplete: true}, nil)
		} else {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}}, nil)
		}
	}
}

// review answers the real evidence-bound assessment protocol without advancing
// the producer script. It never treats an execution receipt as a news source.
func (m *titleResearchHTTPModel) review(request *model.LLMRequest) (iter.Seq2[*model.LLMResponse, error], bool) {
	if request.Config == nil || request.Config.SystemInstruction == nil || len(request.Config.SystemInstruction.Parts) == 0 || len(request.Contents) != 1 || !strings.HasPrefix(request.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_REVIEW_V1") {
		return nil, false
	}
	var input struct {
		store.BotWorkState
		workerContentInput
	}
	if json.Unmarshal([]byte(request.Contents[0].Parts[0].Text), &input) != nil || input.Candidate == nil {
		return nil, false
	}
	m.reviews++
	pass := len(input.Items) > 0
	content := []workerContentAssessment{}
	for _, item := range input.Items {
		a := workerContentAssessment{ItemID: item.ID, ClaimType: workerClaimFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: "No decisive captured detail supports this claim.", Sources: []workerContentSource{}, ExecutionSources: []workerContentSource{}}
		if !m.missing && item.Text == "Decisive AI news source details." {
			for ref, body := range input.Sources {
				if strings.HasPrefix(ref, "source_") && strings.Contains(body, item.Text) {
					a.Support = store.BotCriterionSatisfied
					a.Sources = append(a.Sources, workerContentSource{Ref: ref, Quote: item.Text})
					a.Explanation = "Exact AI news detail returned by the authorized HTTP endpoint."
				}
			}
		}
		if m.missing && item.Text == "Configured search unavailable; no detail source fetched. Configure scoped search before researching AI News." && len(input.Sources) == 0 && len(input.Candidate.Bodies) == 0 {
			a.ClaimType, a.Support = workerClaimNonfactual, store.BotCriterionSatisfied
			a.Explanation = "Honest no-result limitation; no news fact or completed research claimed."
		}
		pass = pass && a.Support == store.BotCriterionSatisfied
		content = append(content, a)
	}
	verdict := store.BotCriterionUnverified
	if pass {
		verdict = store.BotCriterionSatisfied
	}
	criteria := []store.BotCriterionAssessment{}
	for _, criterion := range input.Criteria {
		if !criterion.Excluded {
			criteria = append(criteria, store.BotCriterionAssessment{CriterionID: criterion.ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + input.Candidate.Digest}, Explanation: "AI News title scope checked against captured detail, or explicit search-unavailable limitation."})
		}
	}
	raw, _ := json.Marshal(struct {
		store.BotWorkReview
		ContentAssessments []workerContentAssessment `json:"content_assessments"`
	}{store.BotWorkReview{CandidateDigest: input.Candidate.Digest, Criteria: criteria}, content})
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}, true
}

func TestTitleResearchReviewRequiresCapturedDetail(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		sources    map[string]string
		want       store.BotCriterionVerdict
	}{
		{"captured-detail", "Decisive AI news source details.", map[string]string{"source_1": "Decisive AI news source details."}, store.BotCriterionSatisfied},
		{"missing-detail", "Decisive AI news source details.", nil, store.BotCriterionUnverified},
		{"invented-result", "AI shares rose 20%.", map[string]string{"source_1": "Decisive AI news source details."}, store.BotCriterionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &titleResearchHTTPModel{}
			input := struct {
				store.BotWorkState
				workerContentInput
			}{
				store.BotWorkState{Criteria: []store.BotWorkCriterion{{ID: "employee-assignment"}}, Candidate: &store.BotWorkCandidate{Digest: "fixture-digest", Text: tc.text}},
				workerContentInput{Items: []workerContentItem{{ID: "body:0:line:0", Text: tc.text}}, Sources: tc.sources},
			}
			raw, _ := json.Marshal(input)
			request := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("BOT_WORK_REVIEW_V1", "system")}, Contents: []*genai.Content{genai.NewContentFromText(string(raw), "user")}}
			responses, ok := m.review(request)
			if !ok {
				t.Fatal("review protocol not recognized")
			}
			for response, err := range responses {
				if err != nil {
					t.Fatal(err)
				}
				var out struct {
					store.BotWorkReview
					ContentAssessments []workerContentAssessment `json:"content_assessments"`
				}
				if err := json.Unmarshal([]byte(response.Content.Parts[0].Text), &out); err != nil {
					t.Fatal(err)
				}
				if len(out.Criteria) != 1 || out.Criteria[0].CriterionID != "employee-assignment" || out.Criteria[0].Verdict != tc.want || len(out.ContentAssessments) != 1 || out.ContentAssessments[0].Support != tc.want {
					t.Fatalf("unsupported or uncovered review: %+v", out)
				}
			}
			if m.step != 0 || m.reviews != 1 {
				t.Fatalf("review consumed producer turn: %+v", m)
			}
		})
	}
}

// Unchanged owner title -> authenticated HTTP admission -> production policy and
// tool assembly -> exact selected search -> real HTTP detail. No mandate pins
// are forged. The missing-engine fixture reports the blocker and independently
// checks that the production episode rejects detail retrieval.
func TestBotTitleConfiguredSearchHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing=%t", scope, missing), func(t *testing.T) {
				backend, owner := setupChannelResolverFixture(t, "title-search", "team", "telegram", "linked")
				org, err := backend.ForOrg("title-search")
				if err != nil {
					t.Fatal(err)
				}
				team := org.ForTeam("team")
				repo, sessions, pin := team.Bots(), team.Sessions(), team.SetSessionPin
				if scope == store.BotScopePersonal {
					personal := org.ForUser(owner)
					repo, sessions, pin = personal.Bots(), personal.Sessions(), personal.SetSessionPin
				}
				auth, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "title-search", TeamSlug: "team", Authenticated: true})
				if err != nil {
					t.Fatal(err)
				}
				auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				auth = store.WithTenantContext(auth, &store.TenantContext{UserID: owner, OrgSlug: "title-search", TeamSlug: "team"})
				searchCalls, detailCalls := 0, 0
				detail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					detailCalls++
					if searchCalls != 1 {
						t.Error("detail before selected search")
					}
					io.WriteString(w, "Decisive AI news source details.")
				}))
				defer detail.Close()
				endpoint, _ := url.Parse(detail.URL)
				port, _ := strconv.Atoi(endpoint.Port())
				if err := team.NetworkPolicies().Save(auth, &store.NetworkPolicyRule{Host: endpoint.Hostname(), Port: uint32(port), Action: store.NetworkPolicyAllow}); err != nil {
					t.Fatal(err)
				}
				if !missing {
					if err := backend.PlatformSettings().Save(auth, &store.PlatformSettings{PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "selected-search", Model: "sonar"}}); err != nil {
						t.Fatal(err)
					}
					if err := team.Settings().Save(auth, &store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search", Providers: map[string]store.ProviderConfig{"selected-search": {"api_key": "owned-fixture-only"}}}); err != nil {
						t.Fatal(err)
					}
				}
				policy := newProductionBotMandatePolicy(backend, &config.AppConfig{})
				registry := execution.NewBotTurnAuthorizationRegistry(16)
				defer registry.Close()
				mdl := &titleResearchHTTPModel{url: detail.URL, missing: missing}
				worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, TurnRegistry: registry, MandatePolicy: policy, Sandbox: lifecycleHTTPBackend{}, Pool: workerTestPool{}, WorkerID: "title-search", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return mdl, nil
				}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					// Production scope contains these same stores; no permissive test read policy.
					s.Services.PlatformNetworkPolicies = backend.(interface {
						PlatformNetworkPolicies() store.NetworkPolicyStore
					}).PlatformNetworkPolicies()
					s.Services.NetworkPolicies = org.OrgNetworkPolicies()
					s.Services.TeamNetworkPolicies = team.NetworkPolicies()
					reads, episode, e := productionBotMandatedTools(c, q, s, nil, func(c context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
						if !provider.IsConfinedProviderSecrets(c) || p != "selected-search" || m != "sonar" {
							t.Fatalf("wrong selected search: %s/%s", p, m)
						}
						if q.Mandate.Authorization.Snapshot().PublicResearch.Subject != "AI News" {
							t.Fatal("title subject changed")
						}
						return configuredSearchEngineFixture{calls: &searchCalls, url: "https://example.com/ai-news"}, nil
					})
					if e != nil {
						return nil, e
					}
					hasAsk := false
					for _, candidate := range reads {
						if candidate.Name() == "ask_user" {
							hasAsk = true
						}
					}
					if !hasAsk {
						t.Fatal("authorized mandated branch lacks actual ask_user adapter")
					}
					if q.Run.TitleConfirmation == nil || q.Run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed {
						t.Fatal("research preparation before title confirmation")
					}
					if episode == nil {
						t.Fatal("confirmed title missing search-before-detail guard")
					}
					if missing {
						if _, err := launcher.BotEffectiveConfiguredSearchRoute(c, s.Services); err == nil {
							t.Fatal("missing selection fell back")
						}
						if err := episode.AllowDetail(c, q.Run.ID, q.Run.SessionID.String(), detail.URL); err == nil || err.Error() != "configured search must return sources before detail retrieval; retry an admitted facet or report search unavailable" {
							t.Fatal("missing engine allowed detail")
						}
					}
					return &launcher.BotChatSafeResources{Tools: reads, Research: episode, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
				services := &store.Services{Bots: repo, PersonalBots: repo, Sessions: sessions, PersonalSessions: sessions, BotMandatePolicy: policy, BotTurnRegistry: registry, BotInteractiveReady: true}
				router := mux.NewRouter()
				router.HandleFunc("/", api.BotCreateHandler).Methods(http.MethodPost)
				router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods(http.MethodPost)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					router.ServeHTTP(w, r.WithContext(store.WithServices(auth, services)))
				}))
				defer server.Close()
				response, err := http.Post(server.URL+"?scope="+string(scope), "application/json", strings.NewReader(`{"name":"AI News","start_from_title":true,"idempotency_key":"unchanged-title"}`))
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != 201 {
					t.Fatalf("create %d %s", response.StatusCode, raw)
				}
				var created struct {
					store.Bot
					InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
				}
				if err := json.Unmarshal(raw, &created); err != nil || created.InitialTurn == nil {
					t.Fatalf("receipt %s %v", raw, err)
				}
				version, err := repo.GetVersion(auth, created.ID, created.Version)
				if err != nil || version.Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 || version.Budget.RuntimePolicy.MaxActiveSeconds != nil {
					t.Fatalf("new title did not select default progress policy: %+v %v", version, err)
				}
				if err := pin(auth, created.HomeSessionID.String(), "controlled", "model"); err != nil {
					t.Fatal(err)
				}
				if err := worker.execute(auth, repo, sessions, created.ID, created.InitialTurn.Run.ID); err != nil {
					t.Fatalf("generate title question: %v", err)
				}
				waiting, err := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
				if err != nil || waiting.Status != store.RunWaitingApproval || waiting.TitleConfirmation == nil || waiting.TitleConfirmation.State != store.BotTitleConfirmationWaiting || searchCalls != 0 || detailCalls != 0 || mdl.step != 0 {
					t.Fatalf("research occurred before confirmation: run=%+v search=%d detail=%d step=%d err=%v", waiting, searchCalls, detailCalls, mdl.step, err)
				}
				approvals, err := repo.ListApprovals(auth, created.ID, store.BotPage{})
				if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
					t.Fatalf("missing title question: %+v %v", approvals, err)
				}
				approval := approvals[0]
				answerBody, err := json.Marshal(map[string]any{"action_digest": approval.ActionDigest, "response": store.BotInteractionResponse{QuestionID: approval.Interaction.QuestionID, Answer: "brief"}})
				if err != nil {
					t.Fatal(err)
				}
				answerURL := fmt.Sprintf("%s/bots/%s/approvals/%s/approve-and-continue?scope=%s", server.URL, created.ID, approval.ID, scope)
				answerResponse, err := server.Client().Post(answerURL, "application/json", strings.NewReader(string(answerBody)))
				if err != nil {
					t.Fatal(err)
				}
				answerRaw, err := io.ReadAll(answerResponse.Body)
				answerResponse.Body.Close()
				if err != nil || answerResponse.StatusCode != http.StatusAccepted {
					t.Fatalf("answer title: status=%d body=%s err=%v", answerResponse.StatusCode, answerRaw, err)
				}
				if err := worker.execute(auth, repo, sessions, created.ID, created.InitialTurn.Run.ID); err != nil {
					t.Fatalf("execute confirmed title research: %v", err)
				}
				saved, err := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !missing && (mdl.reviews != 0 || !store.BotReportIsReadOnlyResult(saved.Report) || saved.Report.Work == nil || saved.Report.Work.CandidateDigest == "" || saved.Report.Work.RunID != saved.ID) {
					t.Fatalf("missing fresh read-only result or unexpected reviewer: calls=%d report=%+v", mdl.reviews, saved.Report)
				}
				if missing {
					if searchCalls != 0 || detailCalls != 0 || saved.Report.Summary != "Configured search unavailable; no detail source fetched. Configure scoped search before researching AI News." {
						t.Fatalf("untruthful blocker: %+v search=%d fetch=%d", saved, searchCalls, detailCalls)
					}
				} else if saved.Status != store.RunSucceeded || searchCalls != 1 || detailCalls != 1 {
					t.Fatalf("research failed: %+v search=%d fetch=%d", saved, searchCalls, detailCalls)
				}
			})
		}
	}
}
