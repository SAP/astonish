package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
)

// Record the final (tool-less) synthesis request too. The shared scripted model
// deliberately treats auxiliary session naming separately from execution.
type configuredSearchResumeModel struct {
	configuredAdmissionModel
	evidenceMu      sync.Mutex
	evidence        []string
	finalToolCounts []int
	finalPrompts    []string
}

func (m *configuredSearchResumeModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.evidenceMu.Lock()
	raw, _ := json.Marshal(r.Contents)
	m.evidence = append(m.evidence, string(raw))
	m.finalToolCounts = append(m.finalToolCounts, len(r.Tools))
	if strings.Contains(string(raw), "configured-search-evidence") {
		prompt, _ := json.Marshal(r.Config)
		m.finalPrompts = append(m.finalPrompts, string(prompt))
	}
	m.evidenceMu.Unlock()
	return m.configuredAdmissionModel.GenerateContent(ctx, r, stream)
}

// This is production scheduler admission, HTTP owner approval, actual worker
// claim/resume and configured provider HTTP, not a fabricated grant or sandbox
// infrastructure claim. All databases, retained bytes and endpoints are owned by
// this test. Ordinary generic tools remain forbidden after the exact action.
func TestBotConfiguredSearchOwnerResumeHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, scenario := range []struct {
		scope store.BotScope
		drift string
	}{
		{store.BotScopePersonal, ""}, {store.BotScopeTeam, ""},
		{store.BotScopePersonal, "model"}, {store.BotScopeTeam, "model"},
		{store.BotScopePersonal, "provider"}, {store.BotScopeTeam, "provider"},
		{store.BotScopePersonal, "credential"}, {store.BotScopeTeam, "credential"},
		{store.BotScopePersonal, "dispatch-credential"}, {store.BotScopeTeam, "dispatch-credential"},
		{store.BotScopePersonal, "extra-tool"}, {store.BotScopeTeam, "extra-tool"},
	} {
		scope := scenario.scope
		name := string(scope)
		if scenario.drift != "" {
			name += "/" + scenario.drift + "-drift"
		}
		t.Run(name, func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "admission", "team", "telegram", "linked")
			org, err := backend.ForOrg("admission")
			if err != nil {
				t.Fatal(err)
			}
			team, personal := org.ForTeam("team"), org.ForUser(owner)
			principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "admission", TeamSlug: "team", Authenticated: true}
			auth, err := execution.WithPrincipal(t.Context(), principal)
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			repo, pin := team.Bots(), team.SetSessionPin
			if scope == store.BotScopePersonal {
				repo, pin = personal.Bots(), personal.SetSessionPin
			}
			disabled := false
			for _, standard := range config.GetStandardServers() {
				if standard.Kind != "model" {
					if err := backend.PlatformMCPServers().Save(auth, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var calls, prepared, cleaned atomic.Int32
			key := "owned-" + string(scope) + "-search-key"
			search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("configured provider credential drift")
				}
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.Model != "sonar-fixture" || len(body.Messages) == 0 || !strings.Contains(body.Messages[len(body.Messages)-1].Content, "controlled public evidence") {
					t.Errorf("configured search input drift: %+v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"search-fixture","object":"chat.completion","model":"sonar-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"configured-search-evidence https://evidence.example/source"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}}`)
			}))
			defer search.Close()
			settings := &store.PlatformSettings{WebSearchTool: "perplexity:perplexity_web_search", PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search-fixture", Model: "sonar-fixture"}, Providers: map[string]store.ProviderConfig{"search-fixture": {"type": "litellm", "base_url": search.URL, "api_key": key}}}
			if err := backend.PlatformSettings().Save(auth, settings); err != nil {
				t.Fatal(err)
			}
			home := testRetainedContentBackend(t, backend)
			catalogBackend := &templateWiringBackend{}
			home.setupHomeBackend.Backend = catalogBackend
			pool := &configuredProtocolPool{backend: catalogBackend}
			bundle := &launcher.ChatCatalogResources{Backend: catalogBackend, Pool: pool}
			shared := productionBotChatResources(&launcher.ChatFactoryResult{SharedResources: bundle, SandboxPool: pool})
			if shared == nil || shared.Pool != pool || shared.Backend != catalogBackend {
				t.Fatal("production shared resource bundle lost")
			}
			llm := &configuredSearchResumeModel{configuredAdmissionModel: configuredAdmissionModel{script: []configuredAdmissionCall{{"perplexity_web_search", map[string]any{"query": "controlled public evidence", "max_results": float64(2)}}}}}
			if scenario.drift == "extra-tool" {
				llm.script = append(llm.script, configuredAdmissionCall{"perplexity_web_search", map[string]any{"query": "replacement post-resume", "max_results": float64(2)}})
			}
			registry := execution.NewBotTurnAuthorizationRegistry(8)
			defer registry.Close()
			svc := &store.Services{Mode: store.ModePlatform, Platform: backend, TenantRouter: backend, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), PlatformMCPServers: backend.PlatformMCPServers(), TeamMCPServers: team.MCPServers(), PlatformSettings: backend.PlatformSettings(), Settings: team.Settings(), PersonalSettings: personal.PersonalSettings(), BotTurnRegistry: registry, BotInteractiveReady: true}
			worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: home, Pool: pool, WorkerID: "search-owner-resume", TurnRegistry: registry, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				prepared.Add(1)
				pool.Alias(q.Run.SessionID.String(), q.Bot.HomeSessionID.String())
				s.Services.BotFilesFactory = func(c context.Context, services *store.Services, requested store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, home, services, requested)
				}
				llm.mu.Lock()
				llm.work = func() (*store.BotWorkState, error) { return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease) }
				llm.mu.Unlock()
				local, err := launcher.BotScopedChatConfig(c, s)
				if err != nil {
					return nil, err
				}
				if scenario.drift == "dispatch-credential" && prepared.Load() == 2 {
					changed := *settings
					changed.Providers = map[string]store.ProviderConfig{"search-fixture": {"type": "litellm", "base_url": search.URL, "api_key": "replacement-key"}}
					if err := backend.PlatformSettings().Save(c, &changed); err != nil {
						return nil, err
					}
				}
				copy := *shared
				return &launcher.BotChatSafeResources{ChatConfig: local, ProviderFactory: provider.GetProvider, Catalog: &copy, WorkspaceDir: "/workspace", PolicyRuntime: productionBotPolicyRuntime(botLiveGuard{repo: repo, request: q}), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { cleaned.Add(1) }}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
			router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler).Methods("POST")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := principal
				if subject := r.Header.Get("X-Test-Subject"); subject != "" {
					p.Subject = subject
				}
				c, e := execution.WithPrincipal(r.Context(), p)
				if e != nil {
					t.Error(e)
					return
				}
				c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
				if e != nil {
					t.Error(e)
					return
				}
				c = store.WithServices(c, svc)
				c = store.WithTenantContext(c, &store.TenantContext{UserID: p.Subject, OrgSlug: "admission", TeamSlug: "team"})
				c = api.WithPlatformUser(c, &api.PlatformUser{ID: p.Subject, OrgSlug: "admission", TeamSlug: "team", Role: "member"})
				router.ServeHTTP(w, r.WithContext(c))
			}))
			defer server.Close()
			server.Client().Timeout = 30 * time.Second
			post := func(path string, payload any, subject string, want int) []byte {
				t.Helper()
				raw, _ := json.Marshal(payload)
				r, e := http.NewRequestWithContext(t.Context(), "POST", server.URL+path, strings.NewReader(string(raw)))
				if e != nil {
					t.Fatal(e)
				}
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Test-Subject", subject)
				resp, e := server.Client().Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				out, e := io.ReadAll(resp.Body)
				if e != nil || resp.StatusCode != want {
					t.Fatalf("POST %s: %d %s %v", path, resp.StatusCode, out, e)
				}
				return out
			}
			v := store.DefaultBotVersion()
			v.Charter = configuredProtocolTask
			v.Autonomy.AllowedTools = []string{"perplexity_web_search", "bot_work_update"}
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Scheduled configured search", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "search-resume"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err := pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			var admitted store.BotRun
			if err := json.Unmarshal(admitConfiguredContinuousSchedule(t, auth, worker, repo, b, configuredProtocolTask), &admitted); err != nil {
				t.Fatal(err)
			}
			poll := func() {
				t.Helper()
				if err := worker.Poll(auth); err != nil {
					t.Fatal(err)
				}
			}
			get := func() *store.BotRun {
				t.Helper()
				r, e := repo.GetRun(auth, b.ID, admitted.ID)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			approvals := func() []store.BotApproval {
				t.Helper()
				a, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				return a
			}
			poll()
			pending := get()
			all := approvals()
			if pending.Status != store.RunWaitingApproval || len(all) != 1 || all[0].Status != store.ApprovalPending || calls.Load() != 0 {
				t.Fatalf("not safely checkpointed: %+v approvals=%+v calls=%d", pending, all, calls.Load())
			}
			stored, _ := json.Marshal(all)
			if strings.Contains(string(stored), key) {
				t.Fatal("provider credential leaked to approval storage")
			}
			approval := all[0]
			checkpoint, err := bot.DecodeApprovalCheckpoint(pending.Checkpoint, *pending)
			if err != nil || !checkpoint.Matches(approval) {
				t.Fatalf("checkpoint mismatch: %v", err)
			}
			if approval.Action.Tool != "perplexity_web_search" || string(approval.Action.Arguments) != `{"max_results":2,"query":"controlled public evidence"}` {
				t.Fatalf("not exact saved arguments: %+v", approval.Action)
			}
			if !strings.HasPrefix(approval.Action.TargetFingerprint, "provider-config-v1:") {
				t.Fatal("configured search approval missing provider/credential target binding")
			}
			if pending.Usage.ToolCalls < 1 || pending.Usage.InputTokens == 0 {
				t.Fatalf("missing used budget: %+v", pending.Usage)
			}
			approvePath := "/bots/" + b.ID.String() + "/approvals/" + approval.ID.String() + "/approve-and-continue?scope=" + string(scope)
			payload := map[string]any{"action_digest": approval.ActionDigest}
			post(approvePath, map[string]any{"action_digest": "altered"}, "", 409)
			post(approvePath, map[string]any{"action_digest": approval.ActionDigest, "arguments": map[string]any{"query": "replacement"}}, "", 400)
			foreign := uuid.NewString()
			if err := backend.Users().Create(t.Context(), &store.User{ID: foreign, Email: foreign + "@test.invalid", DisplayName: "Foreign", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			orgs, err := backend.Organizations().GetBySlug(t.Context(), "admission")
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Organizations().AddMember(t.Context(), foreign, orgs.ID, "member"); err != nil {
				t.Fatal(err)
			}
			teams, err := org.Teams().GetTeamBySlug(t.Context(), "team")
			if err != nil {
				t.Fatal(err)
			}
			if err := org.Teams().AddMember(t.Context(), &store.TeamMembership{UserID: foreign, TeamID: teams.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			post(approvePath, payload, foreign, 403)
			if err := backend.Organizations().RemoveMember(t.Context(), owner, orgs.ID); err != nil {
				t.Fatal(err)
			}
			post(approvePath, payload, "", 403)
			if err := backend.Organizations().AddMember(t.Context(), owner, orgs.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if approvals()[0].Status != store.ApprovalPending || get().Status != store.RunWaitingApproval || calls.Load() != 0 {
				t.Fatal("invalid approval changed pending run")
			}
			for i := 0; i < 2; i++ {
				var receipt store.BotApproveContinueResult
				if err := json.Unmarshal(post(approvePath, payload, "", 202), &receipt); err != nil || receipt.RunID != admitted.ID || receipt.Continuation != store.BotContinuationQueued {
					t.Fatalf("continuation identity: %+v %v", receipt, err)
				}
			}
			// Claim must re-resolve membership even with owner HTTP consent already saved.
			if err := backend.Organizations().RemoveMember(t.Context(), owner, orgs.ID); err != nil {
				t.Fatal(err)
			}
			beforePrepared := prepared.Load()
			poll()
			if calls.Load() != 0 || prepared.Load() != beforePrepared {
				t.Fatal("revoked owner reached execution")
			}
			if err := backend.Organizations().AddMember(t.Context(), owner, orgs.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if approvals()[0].Status != store.ApprovalApproved {
				t.Fatal("denied claim consumed approval")
			}
			queued := get()
			if queued.Usage != pending.Usage {
				t.Fatalf("resume reset cumulative budget: before=%+v queued=%+v", pending.Usage, queued.Usage)
			}
			if scenario.drift == "provider" || scenario.drift == "model" || scenario.drift == "credential" {
				changed := *settings
				changed.Providers = map[string]store.ProviderConfig{}
				for name, values := range settings.Providers {
					copy := store.ProviderConfig{}
					for k, v := range values {
						copy[k] = v
					}
					changed.Providers[name] = copy
				}
				if scenario.drift == "credential" {
					changed.Providers["search-fixture"]["api_key"] = "replacement-key"
				} else {
					searchConfig := *settings.PerplexityWebSearch
					if scenario.drift == "model" {
						searchConfig.Model = "replacement-model"
					} else {
						searchConfig.Provider = "replacement-provider"
						changed.Providers[searchConfig.Provider] = changed.Providers["search-fixture"]
					}
					changed.PerplexityWebSearch = &searchConfig
				}
				if err := backend.PlatformSettings().Save(auth, &changed); err != nil {
					t.Fatal(err)
				}
				poll()
				if calls.Load() != 0 {
					t.Fatal("saved search approval authorized changed provider/credential target")
				}
				if approvals()[0].Status != store.ApprovalApproved {
					t.Fatalf("configuration drift changed exact approval: approval=%s run=%s", approvals()[0].Status, get().Status)
				}
				if err := backend.PlatformSettings().Save(auth, settings); err != nil {
					t.Fatal(err)
				}
				post("/bots/"+b.ID.String()+"/runs/"+admitted.ID.String()+"/resume?scope="+string(scope), map[string]any{"approval_id": approval.ID}, "", 202)
			}
			if scenario.drift == "dispatch-credential" {
				poll()
				if calls.Load() != 0 || approvals()[0].Status == store.ApprovalConsumed {
					t.Fatal("dispatch-time credential drift executed or consumed search")
				}
				if err := backend.PlatformSettings().Save(auth, settings); err != nil {
					t.Fatal(err)
				}
				post("/bots/"+b.ID.String()+"/runs/"+admitted.ID.String()+"/resume?scope="+string(scope), map[string]any{"approval_id": approval.ID}, "", 202)
			}
			poll()
			final := get()
			if final.ID != pending.ID || final.SessionID != pending.SessionID || final.Version != pending.Version || final.ExecutionMode != pending.ExecutionMode || final.Intent != pending.Intent {
				t.Fatal("resume changed logical run identity")
			}
			if calls.Load() != 1 || approvals()[0].Status != store.ApprovalConsumed {
				t.Fatalf("not consumed/executed once: calls=%d approval=%+v report=%+v", calls.Load(), approvals(), final.Report)
			}
			if scenario.drift == "extra-tool" {
				if final.Status != store.RunPartial || calls.Load() != 1 {
					t.Fatal("post-resume action was not denied")
				}
				poll()
				if calls.Load() != 1 {
					t.Fatal("replayed after post-resume denial")
				}
				return
			}
			if final.Status != store.RunSucceeded && !(final.Status == store.RunPartial && final.Report.Failure == nil && len(final.Report.Unverified) > 0) {
				t.Fatalf("resume failed: status=%s report=%+v", final.Status, final.Report)
			}
			if final.Usage.ToolCalls <= pending.Usage.ToolCalls || final.Usage.InputTokens < pending.Usage.InputTokens+12 || final.Usage.OutputTokens < pending.Usage.OutputTokens+9 {
				t.Fatalf("search bypassed cumulative budget: before=%+v after=%+v", pending.Usage, final.Usage)
			}
			llm.evidenceMu.Lock()
			evidence := strings.Join(llm.evidence, "\n")
			counts := append([]int(nil), llm.finalToolCounts...)
			prompts := strings.Join(llm.finalPrompts, "\n")
			llm.evidenceMu.Unlock()
			if len(counts) == 0 {
				t.Fatal("missing final synthesis request")
			}
			if counts[len(counts)-1] != 0 {
				t.Fatal("final report synthesis exposed generic tools")
			}
			if strings.Contains(evidence+prompts, key) {
				t.Fatal("provider credential leaked to model")
			}
			if !strings.Contains(evidence, "configured-search-evidence") || !strings.Contains(evidence, "https://evidence.example/source") {
				t.Fatal("actual provider result did not reach final model")
			}
			post(approvePath, payload, "", 409)
			post("/bots/"+b.ID.String()+"/runs/"+admitted.ID.String()+"/resume?scope="+string(scope), map[string]any{"approval_id": approval.ID}, "", 409)
			poll()
			poll()
			if calls.Load() != 1 {
				t.Fatal("replayed consumed search")
			}
			deadline := time.Now().Add(time.Second)
			for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			wantAttempts := int32(2)
			if scenario.drift != "" {
				wantAttempts = 3
			}
			if prepared.Load() != wantAttempts || cleaned.Load() != wantAttempts {
				t.Fatalf("attempt resources leaked: prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
			}
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if pool.closed || len(pool.operations) != 0 {
				t.Fatal("borrowed pool closed or unrelated node tool executed")
			}
			t.Logf("scheduler -> pending exact search -> owner HTTP approval (duplicate safe) -> membership revocation denied claim -> same logical run -> configured provider once -> final model; cumulative usage %+v -> %+v; cleanup %d/%d", pending.Usage, final.Usage, cleaned.Load(), prepared.Load())
		})
	}
}
