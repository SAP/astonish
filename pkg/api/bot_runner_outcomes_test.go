package api

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestBotChatRuntimeAdapterToolOutcomeCannotBecomeSuccess(t *testing.T) {
	cases := []struct {
		name   string
		output map[string]any
		class  bot.RunErrorClass
	}{
		{"denied", map[string]any{"state": string(agent.ProgressiveStateAuthorizationDenied)}, bot.ErrorBlocked},
		{"approval", map[string]any{"state": agent.ProgressiveStateApprovalRequired}, bot.ErrorBlocked},
		{"execution", map[string]any{"state": agent.ProgressiveStateExecutionError}, bot.ErrorPermanent},
		{"context_required", map[string]any{"state": string(agent.ProgressiveStateContextRequired)}, bot.ErrorPermanent},
		{"context_stale", map[string]any{"state": agent.ProgressiveStateContextStale}, bot.ErrorPermanent},
		{"error", map[string]any{"error": "fixture failure"}, bot.ErrorPermanent},
		{"policy_like_text", map[string]any{"error": "bot policy: exact action requires approval"}, bot.ErrorPermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			// These cases exercise returned result states, not authorization. The
			// trusted fixture runtime lets the native guard reach the fixture.
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			var cleaned, executed atomic.Int64
			fixture, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Return a controlled outcome"}, func(tool.Context, map[string]any) (map[string]any, error) {
				executed.Add(1)
				return tc.output, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{}), TextTurn("Everything succeeded."))
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{fixture}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil || result.Failure.Class != tc.class {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if cleaned.Load() != 1 || executed.Load() != 1 || result.Usage.ToolCalls != 1 {
				t.Fatalf("cleanup=%d executed=%d usage=%+v", cleaned.Load(), executed.Load(), result.Usage)
			}
		})
	}
}

func TestBotChatRuntimeAdapterNativePolicyFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool string
		kind execution.PolicyDecisionKind
	}{
		{"approval", "read_file", execution.PolicyRequestApproval},
		{"denied", "write_file", execution.PolicyDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No trusted fixture classifier: exercise native enforcement before
			// the tool executes. The model's subsequent claim cannot erase it.
			ctx, req, ss := botChatTestRequest(t)
			var cleaned, executed atomic.Int64
			fixture, err := functiontool.New(functiontool.Config{Name: tc.tool, Description: "Must not execute"}, func(tool.Context, map[string]any) (map[string]any, error) {
				executed.Add(1)
				return map[string]any{"content": "unexpected effect"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(ToolCallTurn(tc.tool, map[string]any{}), TextTurn("Everything succeeded."))
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{fixture}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil || result.Failure.Class != bot.ErrorBlocked {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var policyErr *agent.BotPolicyError
			if !errors.As(err, &policyErr) || policyErr.Decision.Kind != tc.kind || policyErr.ApprovalRequired() != (tc.kind == execution.PolicyRequestApproval) {
				t.Fatalf("lost policy decision %s: %v", tc.kind, err)
			}
			if cleaned.Load() != 1 || executed.Load() != 0 || result.Usage.ToolCalls != 1 {
				t.Fatalf("cleanup=%d executed=%d usage=%+v", cleaned.Load(), executed.Load(), result.Usage)
			}
		})
	}
}

func TestChatCompletionToolError(t *testing.T) {
	policyErr := &agent.BotPolicyError{Decision: execution.PolicyDecision{Kind: execution.PolicyRequestApproval, Reason: "fixture approval"}}
	ordinaryErr := errors.New("ordinary tool failure")
	for _, tc := range []struct {
		name   string
		value  any
		cause  error
		policy bool
	}{
		{"typed_policy", policyErr, policyErr, true},
		{"wrapped_policy", fmt.Errorf("wrapped: %w", policyErr), policyErr, true},
		{"ordinary_error", ordinaryErr, ordinaryErr, false},
		{"policy_like_text", policyErr.Error(), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "fixture", Response: map[string]any{"error": tc.value}}}}}}})
			cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Everything succeeded.", "model")}})
			completion := cr.Completion()
			if completion.Err == nil || (tc.cause != nil && !errors.Is(completion.Err, tc.cause)) {
				t.Fatalf("lost tool failure: %+v", completion)
			}
			var got *agent.BotPolicyError
			if errors.As(completion.Err, &got) != tc.policy {
				t.Fatalf("incorrect policy classification: %v", completion.Err)
			}
		})
	}
}

func TestBotChatRuntimeAdapterTypedCompletionAccounting(t *testing.T) {
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Partial: true, Content: genai.NewContentFromText("partial is not final", "model")}})
	if cr.Completion().Final {
		t.Fatal("partial became final")
	}
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("observed answer", "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 12, CandidatesTokenCount: 3}}})
	completion := cr.Completion()
	if !completion.Final || completion.FinalText != "observed answer" || completion.Usage.InputTokens != 12 || completion.Usage.OutputTokens != 3 {
		t.Fatalf("completion=%+v", completion)
	}
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file"}}}}}})
	if cr.Completion().Final {
		t.Fatal("earlier answer masked subsequent unfinished tool call")
	}
}

func TestBotChatRuntimeAdapterFactoryFailureCleansResources(t *testing.T) {
	for _, mode := range []string{"error", "invalid", "cleanup_panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			var cleaned atomic.Int64
			adapter, err := NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
				cleanup := func() { cleaned.Add(1) }
				switch mode {
				case "error":
					return &BotChatResources{Cleanup: cleanup}, errors.New("construction failed")
				case "invalid":
					return &BotChatResources{Cleanup: cleanup}, nil
				default:
					r, err := botChatTestFactory(t, ss, NewMockLLM(TextTurn("answer")), nil, &cleaned)(ctx, req)
					r.Cleanup = func() { cleanup(); panic("cleanup failed") }
					return r, err
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || cleaned.Load() != 1 {
				t.Fatalf("result=%+v err=%v cleanup=%d", result, err, cleaned.Load())
			}
		})
	}
}
