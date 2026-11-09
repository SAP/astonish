package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func TestInteractiveChildSharedModelBudget(t *testing.T) {
	ctx, registry, id := interactivePolicyContext(t, []string{"chat", "tool:execute"})
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	probe := &botAuxProbe{}
	metered, err := provider.NewBudgetLLM(probe, budget, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 2, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithBotAuxiliaryLLM(ctx, metered)
	mgr := NewSubAgentManager(SubAgentConfig{})
	child := mgr.effectiveTaskLLM(ctx)
	for _, llm := range []model.LLM{auxiliaryLLM(ctx, nil), child} {
		for _, err := range llm.GenerateContent(ctx, &model.LLMRequest{}, false) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	usage := budget.Usage()
	if probe.calls != 2 || usage.InputTokens != 4 || usage.OutputTokens != 6 {
		t.Fatalf("calls=%d usage=%+v", probe.calls, usage)
	}
	registry.Cancel(id)
	for _, err := range child.GenerateContent(ctx, &model.LLMRequest{}, false) {
		if err == nil {
			t.Fatal("revoked child model ran")
		}
	}
	if probe.calls != 2 {
		t.Fatal("provider called after revocation")
	}
}
func TestInteractiveChildAuthorityAndScope(t *testing.T) {
	for _, route := range []string{"native", "progressive", "preferred", "outside-scope", "revoked", "missing-authorizer", "mixed-policy", "missing-model"} {
		t.Run(route, func(t *testing.T) {
			ctx, registry, id := interactivePolicyContext(t, []string{"chat", "tool:execute"})
			auth, _ := execution.BotTurnAuthorizerFromContext(ctx)
			var calls atomic.Int32
			target := botCountingTool(t, &calls)
			mgr := NewSubAgentManager(SubAgentConfig{MaxRetries: 1})
			mgr.AppName, mgr.UserID = "test", "user"
			mgr.SessionService = session.InMemoryService()
			mgr.ToolGroups = map[string]*ToolGroup{"core": {Name: "core", Tools: []tool.Tool{target}}}
			untrusted := &botAuxProbe{}
			mgr.LLM = untrusted
			mgr.SetTaskLLM(untrusted)
			ctx = WithLLM(ctx, untrusted)
			name, args := "read_file", map[string]any{"path": "notes"}
			if route == "progressive" || route == "outside-scope" {
				targetName := name
				if route == "outside-scope" {
					targetName = "write_file"
				}
				name, args = "execute_tool", map[string]any{"name": targetName, "arguments": args}
			}
			modelCalls := 0
			trusted := &memoryCaptureModel{respond: func(childCtx context.Context, i int, _ *model.LLMRequest) (*model.LLMResponse, error) {
				modelCalls++
				gotID, ok := execution.BotRunIdentityFromContext(childCtx)
				gotAuth, _ := execution.BotTurnAuthorizerFromContext(childCtx)
				if !ok || gotID != id || gotAuth != auth || execution.RunPolicyFromContext(childCtx) != nil {
					t.Error("child changed trusted authority")
				}
				if i == 0 {
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "child-read", Name: name, Args: args}}}}, TurnComplete: true}, nil
				}
				return &model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel), TurnComplete: true}, nil
			}}
			if route != "missing-model" {
				ctx = WithBotAuxiliaryLLM(ctx, trusted)
			}
			switch route {
			case "revoked":
				registry.Cancel(id)
			case "missing-authorizer":
				ctx = execution.WithBotTurnAuthorizer(ctx, nil)
			case "mixed-policy":
				policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised})
				if err != nil {
					t.Fatal(err)
				}
				ctx = execution.WithRunPolicy(ctx, policy)
			}
			task := SubAgentTask{Name: "reader", Description: "read", ToolFilter: []string{"read_file"}, OverrideTools: []tool.Tool{target}}
			if route == "preferred" {
				task.ToolSelectionMode = ChildToolSelectionPreferred
			}
			result := mgr.RunTask(ctx, task)
			allowed := route == "native" || route == "progressive" || route == "preferred"
			if allowed && (calls.Load() != 1 || result.Status != "success") {
				t.Fatalf("calls=%d result=%+v", calls.Load(), result)
			}
			if !allowed && calls.Load() != 0 {
				t.Fatalf("unauthorized dispatch: %d", calls.Load())
			}
			if route == "revoked" || route == "missing-authorizer" || route == "mixed-policy" || route == "missing-model" {
				if modelCalls != 0 || result.Error == "" {
					t.Fatalf("failed-open child: calls=%d result=%+v", modelCalls, result)
				}
			}
			if route == "missing-authorizer" && !strings.Contains(result.Error, "missing parent policy") {
				t.Fatalf("unexpected denial: %+v", result)
			}
			if untrusted.calls != 0 {
				t.Fatal("untrusted model override used")
			}
		})
	}
}
