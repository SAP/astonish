package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Every target is executed through the wired Chat catalogue/bridge, with an
// observable request-scoped adapter call. mcp_controlled_read is a pre-bound
// adapter's provider-safe name, not a lazy MCP server/group registration. This
// verifies the dispatch boundary, not production MCP/flow backend availability.
// No provider, browser or network is contacted.
func TestBotCatalogueGuardedDispatchFamilies(t *testing.T) {
	for _, name := range []string{"shell_command", "browser_navigate", "run_flow", "mcp_controlled_read", "native_controlled"} {
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{name}})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Policy = policy
			effects := 0
			target, err := functiontool.New(functiontool.Config{Name: name, Description: "Controlled scoped adapter"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
				effects++
				if store.UserIDFromContext(ctx) != f.req.Principal.Subject {
					t.Fatal("viewer scope reached adapter")
				}
				return map[string]any{"dispatched": name}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{Tools: []tool.Tool{target}, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			r, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Cleanup()
			ctx := execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, policy.Identity()), policy)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}})
			tc := botFactoryHandoffContext{base: ctx, session: f.req.Run.SessionID.String()}
			var execute tool.Tool
			for _, candidate := range r.Agent.Tools {
				if candidate.Name() == "execute_tool" {
					execute = candidate
				}
			}
			if execute == nil {
				t.Fatal("ordinary Chat discovery bridge missing")
			}
			if _, err := execute.(interface {
				Run(tool.Context, any) (map[string]any, error)
			}).Run(tc, map[string]any{"name": name, "arguments": map[string]any{}}); err != nil {
				t.Fatal(err)
			}
			if effects != 1 {
				t.Fatal("adapter not executed")
			}
			// Live revocation is re-read, even when the original tool remains indexed.
			f.req.Bot.Status = store.BotPaused
			_, err = execute.(interface {
				Run(tool.Context, any) (map[string]any, error)
			}).Run(tc, map[string]any{"name": name, "arguments": map[string]any{}})
			if err == nil || effects != 1 {
				t.Fatal("revoked adapter dispatched")
			}
		})
	}
}
func TestBotCatalogueRejectsMalformedBeforeSandbox(t *testing.T) {
	c := &catalogueClient{}
	all, err := BotCatalogueTools(c, "owner-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range all {
		if target.Name() == "shell_command" {
			_, err := target.(botRunnableTool).Run(catalogueContext{botFactoryToolContext{base: context.Background()}}, map[string]any{})
			if err == nil || c.calls != 0 {
				t.Fatal("malformed command dispatched")
			}
		}
	}
}
func TestBotScopedFlowSearchRefusesGlobalFallback(t *testing.T) {
	target, err := BotSearchFlowsTool()
	if err != nil {
		t.Fatal(err)
	}
	_, err = target.(botRunnableTool).Run(catalogueContext{botFactoryToolContext{base: context.Background()}}, map[string]any{})
	if !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
}

type botScopedSearchStore struct {
	store.FlowStore
	calls int
}

func (s *botScopedSearchStore) ListAllFlows(context.Context) []store.FlowSummary {
	s.calls++
	return []store.FlowSummary{{Name: "owner-flow", Description: "controlled owner flow"}}
}

func TestBotScopedFlowSearchDispatchesOwnerStore(t *testing.T) {
	target, err := BotSearchFlowsTool()
	if err != nil {
		t.Fatal(err)
	}
	s := &botScopedSearchStore{}
	ctx := catalogueContext{botFactoryToolContext{base: store.WithFlowStore(context.Background(), s)}}
	result, err := target.(botRunnableTool).Run(ctx, map[string]any{"query": "owner"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil || s.calls != 1 || !strings.Contains(string(raw), "owner-flow") {
		t.Fatalf("owner store not used: %s, calls=%d, err=%v", raw, s.calls, err)
	}
}

func TestBotBrowserToolsRejectMissingBackend(t *testing.T) {
	if _, cleanup, err := BotBrowserTools(nil, "owner-run"); !errors.Is(err, store.ErrBotUnsupported) || cleanup != nil {
		t.Fatalf("missing backend must not select host browser: %v", err)
	}
}

func TestBotCatalogueKeepsRealSchemas(t *testing.T) {
	all, err := BotCatalogueTools(&catalogueClient{}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range all {
		if target.Name() == "shell_command" {
			raw, err := json.Marshal(target.(botRunnableTool).Declaration())
			if err != nil || len(raw) < 100 {
				t.Fatal("lost real schema")
			}
		}
	}
}
