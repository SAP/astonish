package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

const researchDepthURL = "https://www.nyse.com/markets/hours-calendars"
const researchDepthSearch = `Search results retrieved 2026-11-27. NYSE official Hours & Calendars (https://www.nyse.com/markets/hours-calendars), 2026 holiday calendar: Thanksgiving Day Thursday November 26, 2026, markets closed. Friday November 27, 2026 is the next trading session, opening 9:30 a.m. Eastern Time and closing early at 1:00 p.m. Eastern Time. NYSE core trading normally runs 9:30 a.m. to 4:00 p.m. ET. Independent corroboration: https://www.nasdaq.com/market-activity/stock-market-holiday-schedule lists November 26, 2026 as closed and November 27, 2026 as an early close at 1 p.m. ET. These dated results directly answer the requested holiday/next-session dates and hours. Search supplies a paraphrase, not an exact verbatim quotation of the NYSE page.`

// Controlled service content, not a claim that this test contacted the NYSE.
// Preserve the complete multi-year early-close sentence rather than shortening
// it to the requested year and inviting a reconstructed quotation audit.
const researchDepthQuote = "Each market will close early at 1:00 p.m. (1:15 p.m. for eligible options) on Friday, November 27, 2026, Friday, November 26, 2027, and Friday, November 24, 2028 (the day after Thanksgiving)."
const researchDepthPage = `NYSE | Hours & Calendars
Holidays & Trading Hours
Holiday | 2026 | 2027 | 2028
Thanksgiving Day | Thursday, November 26 | Thursday, November 25 | Thursday, November 23
` + researchDepthQuote + `
Crossing Session orders will be accepted beginning at 1:00 p.m. for continuous executions until 1:30 p.m. on these dates, and NYSE American Equities, NYSE Arca Equities, NYSE National, and NYSE Texas late trading sessions will close at 5:00 p.m. All times are Eastern Time.
NYSE
Core Trading Session: 9:30 a.m. to 4:00 p.m. ET.
On the early closing day the core session opens at 9:30 a.m. ET and ends at 1:00 p.m. ET.
Source: https://www.nyse.com/markets/hours-calendars`

// Service fixture only. This substitutes the search service, never the Employee,
// producer, assessment or presentation model in the paid SAP acceptance.
type researchDepthSearchService struct{ calls *int }

func (researchDepthSearchService) Name() string { return "controlled-search-service" }
func (m researchDepthSearchService) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		*m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(researchDepthSearch, "model"), TurnComplete: true}, nil)
	}
}

type researchDepthPool struct {
	titleProductionPool
	fetches    int
	operations []string
}

func (p *researchDepthPool) GetOrCreate(string) sandbox.ToolNodeClient                     { return p }
func (p *researchDepthPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient { return p }
func (p *researchDepthPool) Call(_ string, name string, args map[string]interface{}) (json.RawMessage, error) {
	if (name != "web_fetch" && name != "_bot_public_fetch") || args["url"] != researchDepthURL {
		return nil, fmt.Errorf("controlled page service rejects unexpected tool or URL: %s", name)
	}
	p.fetches++
	p.operations = append(p.operations, name)
	fmt.Printf("RESEARCH_DEPTH_TRANSPORT operation=%s args=%s\n", name, mustRealScheduleJSON(args))
	response := tools.WebFetchResult{URL: researchDepthURL, FinalURL: researchDepthURL, Title: "NYSE | Hours & Calendars", Content: researchDepthPage, ContentType: "text/plain", Status: 200, Length: len(researchDepthPage)}
	if name == tools.BotPublicFetchOperation {
		return json.Marshal(tools.BotPublicFetchResult{ReadExecuted: true, Response: response})
	}
	return json.Marshal(response)
}

type researchDepthObserver struct {
	model.LLM
	t                        *testing.T
	mu                       sync.Mutex
	calls, employees, finals int
	tools                    []string
}

func (m *researchDepthObserver) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	prompt := ""
	if r.Config != nil && r.Config.SystemInstruction != nil {
		for _, p := range r.Config.SystemInstruction.Parts {
			prompt += p.Text
		}
	}
	if strings.Contains(prompt, "BOT_EMPLOYEE_PRESENTATION_V1") {
		m.finals++
	}
	for _, content := range r.Contents {
		if content != nil {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil && (part.FunctionResponse.Name == "execute_tool" || part.FunctionResponse.Name == "perplexity_web_search" || part.FunctionResponse.Name == "web_fetch" || part.FunctionResponse.Name == "employee_chat" || part.FunctionResponse.Name == "bot_evidence_read") {
					m.t.Logf("RESEARCH_DEPTH_SERVICE_RESPONSE name=%s payload=%s", part.FunctionResponse.Name, mustRealScheduleJSON(part.FunctionResponse.Response))
				}
			}
		}
	}
	m.t.Logf("RESEARCH_DEPTH_MODEL call=%d marker=%s tools=%d", call, realSchedulePromptMarker(prompt), len(r.Tools))
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range m.LLM.GenerateContent(ctx, r, stream) {
			if response != nil && response.Content != nil {
				for _, p := range response.Content.Parts {
					if c := p.FunctionCall; c != nil {
						name := c.Name
						if name == "execute_tool" {
							name, _ = c.Args["name"].(string)
						}
						m.tools = append(m.tools, name)
						if name == "employee_chat" {
							m.employees++
							m.t.Logf("RESEARCH_DEPTH_EMPLOYEE args=%s", mustRealScheduleJSON(c.Args))
						}
						m.t.Logf("RESEARCH_DEPTH_TOOL name=%s", name)
					}
				}
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

// Paid model acceptance with deterministic external services and retained-home
// adapters. This proves production composition, NOT a real Docker process or
// real Perplexity/NYSE availability. Opt-in prerequisites fail rather than skip.
func TestBotSAPResearchDepthHTTP(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_SAP_RESEARCH_DEPTH_LIVE") != "1" {
		t.Skip("explicit paid SAP research-depth opt-in required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	app, err := config.LoadAppConfig()
	if err != nil {
		t.Fatal("configuration load failed")
	}
	ps, osx, ts, selected := sapWorkflowReadSettings(t, ctx, app)
	for _, tc := range []struct {
		name, instructions string
		verify             bool
	}{
		{"ordinary", "Give me a short report here: was the NYSE closed for Thanksgiving 2026, and what are the date and core-session hours of the next trading session? Include sources and links.", false},
		{"primary-verification", "Use configured search to locate the NYSE Hours & Calendars primary-source page, then verify directly against that page whether the NYSE was closed for Thanksgiving 2026 and the date and core-session hours of the next trading session. Include the exact NYSE sentence about the November 27 early close as a verbatim quote and link the primary source in your report here.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slug := "research-depth-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
			db, owner := setupChannelResolverFixture(t, slug, "team", "telegram", "isolated-research-depth")
			org, err := db.ForOrg(slug)
			if err != nil {
				t.Fatal(err)
			}
			personal := org.ForUser(owner)
			repo, ss := personal.Bots(), personal.Sessions()
			if err = db.PlatformSettings().Save(ctx, ps); err != nil {
				t.Fatal(err)
			}
			if err = db.OrgSettings(slug).Save(ctx, osx); err != nil {
				t.Fatal(err)
			}
			if err = org.ForTeam("team").Settings().Save(ctx, ts); err != nil {
				t.Fatal(err)
			}
			auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: slug, TeamSlug: "team", Roles: []string{"member"}, Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityMemoryRead), string(execution.CapabilityMemoryWrite), string(execution.CapabilityIntegration), string(execution.CapabilityToolExecute)}, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			auth = store.WithTenantContext(auth, &store.TenantContext{OrgSlug: slug, TeamSlug: "team", UserID: owner})
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			home := &titleProductionHomeBackend{newRegressionHomeBackend(t)}
			pool := &researchDepthPool{}
			worker, release, err := newProductionBotWorker(db, app, &launcher.ChatCatalogResources{Backend: home, Pool: pool}, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			defer worker.Stop()
			observer := &researchDepthObserver{t: t}
			resolve := worker.config.ResolveModel
			worker.config.ResolveModel = func(c context.Context, p execution.Principal, s store.BotScope, n, m string) (model.LLM, error) {
				if n != selected || m != sapWorkflowModel {
					return nil, fmt.Errorf("unexpected model selection")
				}
				llm, e := resolve(c, p, s, n, m)
				if e != nil {
					return nil, e
				}
				configured, ok := llm.(*botGenerationModel)
				if !ok {
					return nil, fmt.Errorf("missing production model wrapper")
				}
				copy := *configured
				observer.LLM = configured.LLM
				copy.LLM = observer
				return &copy, nil
			}
			searchCalls := 0
			prepare := worker.config.Prepare
			worker.config.Prepare = func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				resources, e := prepare(c, r, s)
				if e != nil {
					return resources, e
				}
				resources.PolicyRuntime.Classifier = titleSkillReadClassifier{url: researchDepthURL}
				resources.ProviderFactory = func(c context.Context, n, m string, cfg *config.AppConfig) (model.LLM, error) {
					if ps.PerplexityWebSearch != nil && n == ps.PerplexityWebSearch.Provider && m == ps.PerplexityWebSearch.Model {
						return researchDepthSearchService{&searchCalls}, nil
					}
					return provider.GetProvider(c, n, m, cfg)
				}
				return resources, nil
			}
			svc := &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: worker.config.MandatePolicy, BotTurnRegistry: registry, BotInteractiveReady: true, BotHomeSetup: worker.provisionHome, BotFilesFactory: worker.filesForRequest}
			router := mux.NewRouter()
			router.HandleFunc("/", api.BotCreateHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
			}))
			defer server.Close()
			post := func(path string, v any, status int) []byte {
				t.Helper()
				raw, _ := json.Marshal(v)
				req, e := http.NewRequestWithContext(ctx, "POST", server.URL+path, strings.NewReader(string(raw)))
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Content-Type", "application/json")
				res, e := server.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				raw, e = io.ReadAll(res.Body)
				if e != nil || res.StatusCode != status {
					t.Fatalf("HTTP=%d expected=%d body=%s", res.StatusCode, status, raw)
				}
				return raw
			}
			var created store.Bot
			if json.Unmarshal(post("/?scope=personal", map[string]any{"name": "NYSE Trading Calendar", "idempotency_key": "create"}, 201), &created) != nil {
				t.Fatal("invalid create")
			}
			if err = personal.SetSessionPin(auth, created.HomeSessionID.String(), selected, sapWorkflowModel); err != nil {
				t.Fatal(err)
			}
			var receipt store.BotMessageReceipt
			if json.Unmarshal(post(fmt.Sprintf("/bots/%s/messages?scope=personal", created.ID), map[string]any{"version": created.Version, "instructions": tc.instructions, "idempotency_key": "report"}, 202), &receipt) != nil {
				t.Fatal("invalid admission")
			}
			err = worker.execute(auth, repo, ss, created.ID, receipt.Run.ID)
			run, readErr := repo.GetRun(auth, created.ID, receipt.Run.ID)
			t.Logf("RESEARCH_DEPTH_EVIDENCE scenario=%s model=%s searches=%d fetches=%d employee_calls=%d final_calls=%d tools=%v", tc.name, sapWorkflowModel, searchCalls, pool.fetches, observer.employees, observer.finals, observer.tools)
			if err != nil {
				t.Fatal("real SAP execution failed", err)
			}
			if readErr != nil || run == nil || run.Status != store.RunSucceeded {
				t.Fatalf("unsuccessful run: %+v %v", run, readErr)
			}
			pub, e := sapWorkflowPublication(t, auth, repo, created.ID, run.ID)
			if e != nil || pub == nil || !pub.EmployeeSpeech || pub.Suppressed || run.Report.EmployeeMessage == nil || run.Report.EmployeeMessage.Origin == store.BotEmployeeMessageFallback {
				t.Fatal("actual Employee publication absent")
			}
			// Interactive Employee speech is already reviewed and committed with the
			// run; the separate terminal presentation stage is for operation reports.
			if run.Report.ValidateEmployeeMessageIntegrity(run.Status) != nil || (run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent && run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAssessed) {
				t.Fatal("reviewed interactive Employee message absent")
			}
			text := run.Report.EmployeeMessage.Text
			t.Logf("RESEARCH_DEPTH_FINAL scenario=%s text=%q", tc.name, text)
			if searchCalls < 1 || !strings.Contains(text, "2026") || !strings.Contains(text, "27") || !strings.Contains(text, researchDepthURL) || (!strings.Contains(text, "1:00") && !strings.Contains(text, "13:00") && !strings.Contains(text, "1 p.m.")) {
				t.Fatal("dated sourced answer missing")
			}
			if !tc.verify && searchCalls != 1 {
				t.Fatalf("ordinary answer repeated configured search: %d", searchCalls)
			}
			if !tc.verify && observer.employees != 1 {
				t.Fatalf("ordinary answer repeated private-worker dispatch: %d", observer.employees)
			}
			if !tc.verify && pool.fetches != 0 {
				t.Fatalf("search-sufficient ordinary answer unnecessarily fetched %d pages", pool.fetches)
			}
			if tc.verify {
				for _, operation := range pool.operations {
					if operation != tools.BotPublicFetchOperation {
						t.Fatalf("primary fetch bypassed concrete execution-attestation adapter: %s", operation)
					}
				}
			}
			if tc.verify && (pool.fetches < 1 || !strings.Contains(text, researchDepthQuote)) {
				t.Fatal("explicit primary verification lacks fetched exact quotation")
			}
			t.Logf("RESEARCH_DEPTH_ACCEPTED scenario=%s searches=%d fetches=%d status=%s", tc.name, searchCalls, pool.fetches, run.Status)
		})
	}
}

func TestResearchDepthServiceFixtureContrast(t *testing.T) {
	pool := &researchDepthPool{}
	raw, err := pool.Call("fixture", "_bot_public_fetch", map[string]any{"url": researchDepthURL})
	var result struct {
		ReadExecuted bool           `json:"read_executed"`
		Response     map[string]any `json:"response"`
	}
	if err != nil || json.Unmarshal(raw, &result) != nil || !result.ReadExecuted || result.Response["content"] != researchDepthPage || pool.fetches != 1 {
		t.Fatal("page fixture does not implement concrete public-fetch transport contract")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 2 {
		t.Fatal("fetch execution envelope must contain exactly read_executed and response")
	}
	var page tools.WebFetchResult
	if json.Unmarshal(envelope["response"], &page) != nil || page.URL != researchDepthURL || page.FinalURL != researchDepthURL || page.Title != "NYSE | Hours & Calendars" || page.Status != 200 || page.Truncated || page.ContentType != "text/plain" || page.Content != researchDepthPage || page.Length != len(researchDepthPage) {
		t.Fatal("page fixture does not match concrete WebFetchResult metadata")
	}
	if _, err := pool.Call("fixture", "_bot_public_fetch", map[string]any{"url": "https://unrelated.example/"}); err == nil {
		t.Fatal("page fixture accepts unrelated source")
	}
	if len(pool.operations) != 1 || pool.operations[0] != tools.BotPublicFetchOperation {
		t.Fatal("fixture did not execute the first-party public-fetch operation")
	}
	if strings.Contains(researchDepthSearch, researchDepthQuote) {
		t.Fatal("search accidentally provides exact primary quote")
	}
	if !strings.Contains(researchDepthPage, researchDepthQuote) {
		t.Fatal("primary page lacks required quote")
	}
	for _, detail := range []string{"Friday, November 27, 2026", "Friday, November 26, 2027", "Friday, November 24, 2028", "(the day after Thanksgiving)", "(1:15 p.m. for eligible options)"} {
		if !strings.Contains(researchDepthQuote, detail) {
			t.Fatalf("primary quote omits part of the complete early-close sentence: %s", detail)
		}
	}
	if !strings.Contains(researchDepthPage, "Thanksgiving Day | Thursday, November 26 | Thursday, November 25 | Thursday, November 23") {
		t.Fatal("primary fixture lacks the aligned multi-year holiday row")
	}
	for _, detail := range []string{"2026", "November 26", "November 27", "9:30", "1:00", researchDepthURL} {
		if !strings.Contains(researchDepthSearch, detail) {
			t.Fatalf("initial search inadequate: %s", detail)
		}
	}
}
