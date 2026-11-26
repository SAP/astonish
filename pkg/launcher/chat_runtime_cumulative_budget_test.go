package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// These tests exercise real runtime composition, provider accounting, the local
// configured-search HTTP protocol, and actual delegate execution. The file ledger
// round trip is explicit: it is NOT SQL/controller resume or HTTP admission proof.
// Only model responses and the owner-pinned node transport are test fixtures.
type cumulativeBudgetModel struct {
	t                    *testing.T
	mode, path, expected string
	calls                atomic.Int32
	inspect              func(context.Context)
}

func (*cumulativeBudgetModel) Name() string { return "offline-cumulative-budget" }
func (m *cumulativeBudgetModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.inspect(ctx)
		n := m.calls.Add(1)
		response := &model.LLMResponse{Content: genai.NewContentFromText("cumulative parent/continuation", "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5, TotalTokenCount: 15}}
		if n == 2 {
			response.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "budget-child-read", Name: "read_file", Args: map[string]any{"path": m.path, "force": true}}}}}
			response.TurnComplete = false
		}
		if n == 3 {
			found := false
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil && p.FunctionResponse.Response["content"] == m.expected {
						found = true
					}
				}
			}
			if !found {
				m.t.Error("actual child did not receive exact scoped file bytes")
			}
			response.Content = genai.NewContentFromText("verified cumulative child: "+m.expected, "model")
		}
		yield(response, nil)
	}
}

func cumulativeGenerate(llm model.LLM, ctx context.Context) error {
	for _, err := range llm.GenerateContent(ctx, &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("continue cumulative task", "user")}}, false) {
		if err != nil {
			return err
		}
	}
	return nil
}

func TestChatRuntimeCumulativeBudgetContinuity(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				r, membership, pool, oldModel, oldCleanup := newDelegateExecutionRuntime(t, scope, mode)
				oldCleanup()
				m := &cumulativeBudgetModel{t: t, mode: mode, path: oldModel.path, expected: oldModel.expected}
				limit, tokens := int64(4), int64(100000)
				spec := store.BudgetSpec{MaxRuntimeSeconds: 60, MaxToolCalls: &limit, MaxTokens: &tokens}
				meter, err := bot.NewRunBudget(spec, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if mode != "ordinary-chat" {
					r.f.req.Budget = meter
				}
				searchArgs := map[string]any{"query": "cumulative public evidence", "max_results": float64(2)}
				delegateArgs := map[string]any{"tasks": []any{map[string]any{"name": "reader", "task": "Read the scoped file", "tools": []string{"read_file"}}}}
				shell := oldModel.shell
				unusedShell := map[string]any{"command": shell["command"], "working_dir": shell["working_dir"], "background": false}
				unusedSearch := map[string]any{"query": "unused cumulative evidence"}
				var searches atomic.Int32
				key := "cumulative-" + string(scope) + "-fixture"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					searches.Add(1)
					if req.URL.Path != "/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer "+key {
						t.Error("search escaped configured endpoint or credential")
					}
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					raw, _ := json.Marshal(body)
					if body["model"] != "sonar-fixture" || !strings.Contains(string(raw), "cumulative public evidence") {
						t.Errorf("unexpected search request: %s", raw)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"cumulative-search","object":"chat.completion","model":"sonar-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Cumulative sourced answer https://evidence.example/source"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}}`)
				}))
				defer server.Close()
				cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"scoped-search": {"type": "litellm", "api_key": key, "base_url": server.URL}}}
				cfg.General.WebSearchTool = "perplexity:perplexity_web_search"
				cfg.PerplexityWebSearch.Provider, cfg.PerplexityWebSearch.Model = "scoped-search", "sonar-fixture"
				r.f.services.PlatformSettings = botKnowledgePlatformSettings{row: &store.PlatformSettings{WebSearchTool: cfg.General.WebSearchTool, PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "scoped-search", Model: "sonar-fixture"}, Providers: map[string]store.ProviderConfig{"scoped-search": {"type": "litellm", "api_key": key, "base_url": server.URL}}}}
				r.f.services.OrgSettings = botKnowledgeOrgSettings{row: &store.OrgSettings{}}
				r.f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{}}
				if mode == "autonomous-bot" {
					scopedSearchGrants(t, r.f, cfg, map[string][]map[string]any{"perplexity_web_search": {searchArgs, unusedSearch}, "delegate_tasks": {delegateArgs}, "read_file": nil, "shell_command": {shell, unusedShell}})
				}
				resources := ChatCatalogResources{Pool: pool, Backend: pool.node.backend}
				factory := func(ctx context.Context, p, name string, selected *config.AppConfig) (model.LLM, error) {
					owner, ok := execution.PrincipalFromContext(ctx)
					if !ok || owner.Subject != r.f.req.Principal.Subject || owner.OrgSlug != r.f.req.Principal.OrgSlug || owner.TeamSlug != r.f.req.Principal.TeamSlug {
						t.Error("search lost owner scope")
					}
					if p != "scoped-search" || name != "sonar-fixture" || selected.Providers[p]["base_url"] != server.URL {
						return nil, fmt.Errorf("foreign provider configuration")
					}
					return provider.GetProvider(ctx, p, name, selected)
				}
				prepare := r.f.cfg.Prepare
				r.f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
					safe, err := prepare(ctx, req, scope)
					if err == nil {
						safe.ChatConfig, safe.Catalog, safe.ProviderFactory = cfg, &resources, factory
					}
					return safe, err
				}
				r.f.cfg.WrapModel = func(_ context.Context, _ model.LLM, budget execution.RunBudget) (model.LLM, error) {
					if budget != meter {
						t.Error("factory replaced cumulative shared meter")
					}
					return provider.NewBudgetLLM(m, budget, provider.BudgetLLMOptions{MaxOutputTokens: 5, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 10, nil }})
				}
				m.inspect = func(ctx context.Context) {
					p, ok := execution.PrincipalFromContext(ctx)
					if !ok || p.Subject != r.f.req.Principal.Subject || p.OrgSlug != r.f.req.Principal.OrgSlug || p.TeamSlug != r.f.req.Principal.TeamSlug {
						t.Error("model lost owner/tenant scope")
					}
					n := m.calls.Load() + 1
					// Ordinary parent model calls need identity, not tool credentials;
					// scoped credentials are installed at the child/tool binding boundary.
					if mode != "ordinary-chat" || n == 2 || n == 3 {
						cs := store.CredentialStoreFromContext(ctx)
						if cs == nil || cs.Get(ctx, "mail") == nil || cs.Get(ctx, "mail").Username != "owner" {
							t.Error("model resolved foreign scoped credentials")
						}
					}
					identity, hasIdentity := execution.BotRunIdentityFromContext(ctx)
					policy := execution.RunPolicyFromContext(ctx)
					_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
					switch mode {
					case "ordinary-chat":
						if hasIdentity || policy != nil || interactive || api.BotChatRunBudgetFromContext(ctx) != nil {
							t.Error("ordinary Chat fabricated Bot authority/budget")
						}
					case "interactive-bot":
						if !hasIdentity || policy != nil || !interactive {
							t.Error("interactive model lost turn authority")
						}
					case "autonomous-bot":
						if !hasIdentity || identity != r.f.req.Policy.Identity() || policy == nil || interactive {
							t.Error("autonomous model lost run authority")
						}
						if n := m.calls.Load() + 1; (n == 2 || n == 3) && policy == r.f.req.Policy {
							t.Error("child inherited unrestricted parent policy")
						}
					}
				}
				var cleanup func()
				build := func() {
					t.Helper()
					if mode == "ordinary-chat" {
						inputs, e := chatruntime.Build(r.f.ctx, chatruntime.BuildOptions{Principal: r.f.req.Principal, Services: r.f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, ProviderFactory: factory, FallbackPolicy: chatruntime.FallbackForbidden})
						if e != nil {
							t.Fatal(e)
						}
						t.Cleanup(func() { _ = inputs.Close() })
						runtime, e := NewScopedChatRuntime(r.f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: r.node.root}, Resources: &resources, LLM: m, Sessions: r.f.sessions, SessionID: r.node.session, UseScopedEngines: true}, ChatRuntimeHooks{})
						if e != nil {
							t.Fatal(e)
						}
						r.chat, cleanup = runtime.ChatAgent, runtime.Cleanup
					} else {
						runtime, e := r.f.construct(t)
						if e != nil {
							t.Fatal(e)
						}
						r.chat, cleanup = runtime.Agent, runtime.Cleanup
					}
					t.Cleanup(cleanup)
				}
				build()
				assertUsage := func(in, out, tools int64) {
					t.Helper()
					u := meter.Usage()
					if u.InputTokens != in || u.OutputTokens != out || u.ToolCalls != tools {
						t.Fatalf("cumulative ledger=%+v want input=%d output=%d tools=%d", u, in, out, tools)
					}
				}
				if err := cumulativeGenerate(r.chat.LLM, r.f.ctx); err != nil {
					t.Fatal(err)
				}
				if mode != "ordinary-chat" {
					assertUsage(10, 5, 0)
				}
				result, err := r.invoke(t, r.f.ctx, "perplexity_web_search", searchArgs)
				raw, _ := json.Marshal(result)
				if err != nil || searches.Load() != 1 || !strings.Contains(string(raw), "Cumulative sourced answer") {
					t.Fatalf("configured search: %s %v calls=%d", raw, err, searches.Load())
				}
				if mode != "ordinary-chat" {
					assertUsage(22, 14, 1)
				}
				ctx, cancel := context.WithTimeout(r.f.ctx, 10*time.Second)
				defer cancel()
				result, err = r.invoke(t, ctx, "delegate_tasks", delegateArgs)
				raw, _ = json.Marshal(result)
				var delegated tools.DelegateTasksResult
				if e := json.Unmarshal(raw, &delegated); e != nil {
					t.Fatal(e)
				}
				if err != nil || len(delegated.Results) != 1 || delegated.Results[0].Status != "success" || delegated.Results[0].ChildSessionID == r.node.session || delegated.Results[0].ChildSessionID == "" || !strings.Contains(delegated.Results[0].Result, m.expected) || m.calls.Load() != 3 {
					t.Fatalf("actual delegate: %s %v models=%d", raw, err, m.calls.Load())
				}
				if len(pool.node.calls) != 1 || pool.node.logicalIDs[0] != delegated.Results[0].ChildSessionID {
					t.Fatalf("missing actual child alias dispatch: %v %v", pool.node.calls, pool.node.logicalIDs)
				}
				if mode != "ordinary-chat" {
					assertUsage(42, 24, 3)
				}
				cleanup()
				// Simulate a stopped attempt's durable ledger boundary without inventing
				// consumption or replacing any meter operation with a stub.
				if mode != "ordinary-chat" {
					before := meter.Usage()
					ledger := filepath.Join(t.TempDir(), "usage.json")
					bytes, e := json.Marshal(before)
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(ledger, bytes, 0600); e != nil {
						t.Fatal(e)
					}
					bytes, e = os.ReadFile(ledger)
					if e != nil {
						t.Fatal(e)
					}
					var persisted store.BotUsage
					if e = json.Unmarshal(bytes, &persisted); e != nil {
						t.Fatal(e)
					}
					meter, e = bot.RestoreRunBudget(spec, time.Now(), persisted)
					if e != nil {
						t.Fatal(e)
					}
					r.f.req.Budget = meter
					if meter.Usage().RuntimeNanos < before.RuntimeNanos || meter.Usage().CostMicros != nil {
						t.Fatal("continuation reset runtime or fabricated known cost")
					}
					assertUsage(42, 24, 3)
				}
				build()
				membership.revoke()
				if err := cumulativeGenerate(r.chat.LLM, r.f.ctx); err == nil {
					t.Error("continuation model used revoked membership")
				}
				if _, err := r.invoke(t, r.f.ctx, "shell_command", shell); err == nil {
					t.Error("continuation tool used revoked membership")
				}
				membership.restore()
				if m.calls.Load() != 3 || len(pool.node.calls) != 1 {
					t.Fatal("revoked continuation dispatched")
				}
				if mode != "ordinary-chat" {
					lease := r.f.req.Run.Lease
					r.f.req.Run.Lease.Fence++
					if err := cumulativeGenerate(r.chat.LLM, r.f.ctx); err == nil {
						t.Error("continuation model used replaced lease")
					}
					if _, err := r.invoke(t, r.f.ctx, "shell_command", shell); err == nil {
						t.Error("continuation tool used replaced lease")
					}
					r.f.req.Run.Lease = lease
					assertUsage(42, 24, 3)
				}
				if err := cumulativeGenerate(r.chat.LLM, r.f.ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := r.invoke(t, r.f.ctx, "shell_command", shell); err != nil {
					t.Fatal(err)
				}
				if m.calls.Load() != 4 || len(pool.node.calls) != 2 || pool.node.outputs[1]["stdout"] != "services-parity-ok" {
					t.Fatal("positive continuation did not execute")
				}
				if mode == "ordinary-chat" {
					assertUsage(0, 0, 0)
					return
				}
				assertUsage(52, 29, 4)
				consumed := len(r.guard.consumed)
				if mode == "autonomous-bot" && consumed != 3 {
					t.Fatalf("wrong exact grant consumption %d", consumed)
				}
				if err := cumulativeGenerate(r.chat.LLM, r.f.ctx); !errors.Is(err, bot.ErrBudgetExceeded) {
					t.Fatalf("exhausted model: %v", err)
				}
				for _, call := range []struct {
					name string
					args map[string]any
				}{{"perplexity_web_search", unusedSearch}, {"shell_command", unusedShell}, {"delegate_tasks", delegateArgs}} {
					if _, err := r.invoke(t, r.f.ctx, call.name, call.args); !errors.Is(err, bot.ErrBudgetExceeded) {
						t.Errorf("exhausted %s: %v", call.name, err)
					}
				}
				if searches.Load() != 1 || m.calls.Load() != 4 || len(pool.node.calls) != 2 || len(r.guard.consumed) != consumed {
					t.Fatal("unused exact approval bought budget or denied operation dispatched")
				}
				assertUsage(52, 29, 4)
			})
		}
	}
}
