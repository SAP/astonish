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
)

type mcpResumeTargetClassifier struct{ fingerprint string }

func (c *mcpResumeTargetClassifier) DescribeAction(_ context.Context, n string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: n, Arguments: args, Effect: execution.EffectUnknown, TargetFingerprint: c.fingerprint}, nil
}

func TestBotMCPApprovalCheckpointTargetBinding(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-target-once", true: "changed-target-blocked"}[changed], func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			const name = "mcp:controlled/call"
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{name}})
			if err != nil {
				t.Fatal(err)
			}
			req.Policy = policy
			ctx = execution.WithRunPolicy(ctx, policy)
			s := &resumeTestStore{}
			classifier := &mcpResumeTargetClassifier{fingerprint: "mcp-config-v1:old"}
			ctx = bot.WithApprovalStore(ctx, s)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: classifier, Guard: resumeTestGuard{}})
			var effects, cleaned atomic.Int64
			target, err := functiontool.New(functiontool.Config{Name: name, Description: "controlled MCP effect"}, func(tool.Context, map[string]any) (map[string]any, error) {
				effects.Add(1)
				return map[string]any{"receipt": "ok"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			first := NewMockLLM(ToolCallTurn(name, map[string]any{"arg": "exact"}))
			adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, first, []tool.Tool{target}, &cleaned))
			sink := func(context.Context, bot.RuntimeEvent) error { return nil }
			waiting, err := adapter.Start(ctx, req, sink)
			if err != nil || waiting.Status != store.RunWaitingApproval || effects.Load() != 0 {
				t.Fatalf("checkpoint: %+v %v", waiting, err)
			}
			// Reconstruct the persisted approval as a restart would, retaining fingerprint.
			wire, err := json.Marshal(s.a)
			if err != nil {
				t.Fatal(err)
			}
			var saved store.BotApproval
			if err := json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			s.a = &saved
			s.a.Status = store.ApprovalApproved
			req.Run.Checkpoint = waiting.Checkpoint
			req.Run.Lease.Fence++
			if changed {
				classifier.fingerprint = "mcp-config-v1:new"
			}
			final := NewMockLLM(TextTurn("receipt observed"))
			adapter, _ = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, final, []tool.Tool{target}, &cleaned))
			_, err = adapter.Resume(ctx, req, sink)
			if changed {
				if err == nil || effects.Load() != 0 || s.consumed != 0 {
					t.Fatalf("changed target executed: err=%v effects=%d consumed=%d", err, effects.Load(), s.consumed)
				}
			} else {
				if err != nil || effects.Load() != 1 || s.consumed != 1 {
					t.Fatalf("same target failed: err=%v effects=%d consumed=%d", err, effects.Load(), s.consumed)
				}
				if _, err := adapter.Resume(ctx, req, sink); err == nil || effects.Load() != 1 {
					t.Fatal("approval replay")
				}
			}
		})
	}
}
