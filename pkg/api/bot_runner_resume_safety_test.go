package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestBotResumeNoReplayAfterEffect(t *testing.T) {
	for _, failure := range []string{"model", "extra-tool", "tool-error"} {
		t.Run(failure, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			s := &resumeTestStore{}
			ctx = bot.WithApprovalStore(ctx, s)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: resumeTestGuard{}})
			var executed, cleaned atomic.Int64
			action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Exact fixture"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
				executed.Add(1)
				if failure == "tool-error" {
					return nil, errors.New("action outcome uncertain")
				}
				return map[string]any{"receipt": "ok"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(ToolCallTurn("read_file", map[string]any{"body": "exact"})), []tool.Tool{action}, &cleaned))
			sink := func(context.Context, bot.RuntimeEvent) error { return nil }
			waiting, err := adapter.Start(ctx, req, sink)
			if err != nil || waiting.Status != store.RunWaitingApproval {
				t.Fatal(waiting, err)
			}
			req.Run.Checkpoint = waiting.Checkpoint
			req.Run.Lease.Fence++
			s.a.Status = store.ApprovalApproved
			next := ErrorTurn("failed", "report failed after effect")
			if failure == "extra-tool" {
				next = ToolCallTurn("read_file", map[string]any{"body": "exact"})
			}
			adapter, _ = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(next), []tool.Tool{action}, &cleaned))
			result, err := adapter.Resume(ctx, req, sink)
			if err == nil || result.Status != store.RunPartial || result.Failure == nil || !result.Failure.UncertainEffect || executed.Load() != 1 || s.consumed != 1 {
				t.Fatalf("%+v %v executed=%d consumed=%d", result, err, executed.Load(), s.consumed)
			}
			if err := result.Report.ValidateEmployeeMessageIntegrity(result.Status); err != nil {
				t.Fatalf("final exact-action outcome has a stale employee envelope: %v", err)
			}
			if string(result.Checkpoint) != string(waiting.Checkpoint) {
				t.Fatal("uncertain action lost its reconciliation checkpoint")
			}
			adapter, _ = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(TextTurn("must not run")), []tool.Tool{action}, &cleaned))
			if _, err := adapter.Resume(ctx, req, sink); err == nil || executed.Load() != 1 {
				t.Fatal("uncertain action replayed")
			}
		})
	}
}

func TestBotResumeModelPreservesExactNumbers(t *testing.T) {
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"id":9007199254740993}`)}
	m := &botResumeModel{action: action}
	for response, err := range m.GenerateContent(context.Background(), nil, false) {
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(response.Content.Parts[0].FunctionCall.Args)
		if err != nil {
			t.Fatal(err)
		}
		actual := action
		actual.Arguments = raw
		want, _ := action.Digest()
		got, _ := actual.Digest()
		if want != got {
			t.Fatalf("rounded action: %s", raw)
		}
	}
}
