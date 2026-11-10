package api

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type approvalIdentityToolContext struct {
	context.Context
	id string
}

func (c approvalIdentityToolContext) FunctionCallID() string { return c.id }

func TestBotApprovalIdentityEvidence(t *testing.T) {
	for _, scenario := range []string{"exact", "other-id", "other-name", "different-args", "different-context-id", "failed-ledger", "spoof-only", "duplicate-response"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx, _ = bot.WithToolOutcomes(ctx)
			capture := &botApprovalCapture{store: &resumeTestStore{}, req: req, invocation: &genai.FunctionCall{ID: "blocked", Name: "read_file", Args: map[string]any{"phase": "approval"}}}
			action := execution.ActionDescriptor{Tool: "read_file", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"phase":"approval"}`), ConstraintsVerified: true}
			if scenario == "different-args" {
				action.Arguments = json.RawMessage(`{"phase":"other"}`)
			}
			digest, _ := action.Digest()
			requestCtx := approvalIdentityToolContext{Context: ctx, id: "blocked"}
			if scenario == "different-context-id" {
				requestCtx.id = "other"
			}
			if scenario != "spoof-only" {
				if _, err := capture.RequestApproval(requestCtx, req.Policy.Identity(), action, digest); err != nil {
					t.Fatal(err)
				}
			}
			ctx = context.WithValue(ctx, botApprovalCaptureKey{}, capture)
			cr := &ChatRunner{ctx: ctx}
			response := &genai.FunctionResponse{ID: "blocked", Name: "read_file", Response: map[string]any{"approval_required": true, "executed": false}}
			if scenario == "other-id" {
				response.ID = "earlier"
			}
			if scenario == "other-name" {
				response.Name = "other_tool"
			}
			if scenario == "failed-ledger" {
				bot.RecordToolOutcome(ctx, "blocked", bot.ToolOutcome{Verified: false, Unverified: []string{"genuine failed effect"}})
			}
			cr.observeBotToolOutcome("tool", response)
			if scenario == "duplicate-response" {
				cr.observeBotToolOutcome("tool", response)
			}
			want := 1
			if scenario == "exact" {
				want = 0
			}
			if scenario == "failed-ledger" {
				want = 2
			}
			if len(cr.completion.Report.Unverified) != want {
				t.Fatalf("want %d warnings, got %v", want, cr.completion.Report.Unverified)
			}
		})
	}
}

type approvalIdentityClassifier struct{}

func (approvalIdentityClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	effect := execution.EffectSend
	var values map[string]any
	_ = json.Unmarshal(args, &values)
	if values["phase"] == "prior" {
		effect = execution.EffectRead
	}
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: effect, ConstraintsVerified: true}, nil
}

func TestBotApprovalNonexecutionIdentity(t *testing.T) {
	for _, prior := range []bool{false, true} {
		name := "only_waiting"
		if prior {
			name = "prior_same_name_uncertain"
		}
		t.Run(name, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			ctx = bot.WithApprovalStore(ctx, &resumeTestStore{})
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: approvalIdentityClassifier{}, Guard: resumeTestGuard{}})
			var executed, cleaned atomic.Int64
			target, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled invocation"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
				executed.Add(1)
				return map[string]any{"content": "unverified prior result"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			turns := []*MockTurn{}
			if prior {
				turns = append(turns, ToolCallTurn("read_file", map[string]any{"phase": "prior"}))
			}
			turns = append(turns, ToolCallTurn("read_file", map[string]any{"phase": "approval"}))
			adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(turns...), []tool.Tool{target}, &cleaned))
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			want := 0
			if prior {
				want = 1
			}
			if err != nil || result.Status != store.RunWaitingApproval || executed.Load() != int64(want) {
				t.Fatalf("result=%+v err=%v executions=%d", result, err, executed.Load())
			}
			if len(result.Report.Unverified) != want {
				t.Fatalf("want %d genuine uncertainties, got %v", want, result.Report.Unverified)
			}
		})
	}
}
