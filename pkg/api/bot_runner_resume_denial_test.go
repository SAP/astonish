package api

import (
	"bytes"
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

type ambiguousResumeConsumptionStore struct {
	resumeTestStore
}

func (s *ambiguousResumeConsumptionStore) ConsumeApproval(ctx context.Context, q store.BotApprovalConsumption) error {
	if err := s.resumeTestStore.ConsumeApproval(ctx, q); err != nil {
		return err
	}
	return errors.New("consumption response lost")
}

func TestBotResumeAmbiguousConsumptionNeverRetryable(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	s := &ambiguousResumeConsumptionStore{}
	ctx = bot.WithApprovalStore(ctx, s)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: resumeTestGuard{}})
	var executed, cleaned atomic.Int64
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "exact fixture"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) { executed.Add(1); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	sink := func(context.Context, bot.RuntimeEvent) error { return nil }
	adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(ToolCallTurn("read_file", map[string]any{"body": "exact"})), []tool.Tool{action}, &cleaned))
	waiting, err := adapter.Start(ctx, req, sink)
	if err != nil {
		t.Fatal(err)
	}
	req.Run.Checkpoint = waiting.Checkpoint
	req.Run.Lease.Fence++
	s.a.Status = store.ApprovalApproved
	adapter, _ = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(TextTurn("not an effect receipt")), []tool.Tool{action}, &cleaned))
	result, err := adapter.Resume(ctx, req, sink)
	if err == nil || result.Status != store.RunPartial || result.Failure == nil || !result.Failure.UncertainEffect || s.consumed != 1 || executed.Load() != 0 {
		t.Fatalf("result=%+v err=%v consumed=%d executed=%d", result, err, s.consumed, executed.Load())
	}
	if _, err := adapter.Resume(ctx, req, sink); err == nil || s.consumed != 1 || executed.Load() != 0 {
		t.Fatal("ambiguous consumption replayed")
	}
}

type resumeDriftClassifier struct{ drift bool }

func (c resumeDriftClassifier) DescribeAction(ctx context.Context, n string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	a, err := (resumeTestClassifier{}).DescribeAction(ctx, n, raw)
	if c.drift {
		a.TargetFingerprint = "changed-target"
	}
	return a, err
}

func TestBotResumeUnusedTargetDenialRestoresExactAction(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	s := &resumeTestStore{}
	ctx = bot.WithApprovalStore(ctx, s)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: resumeTestGuard{}})
	var executed, cleaned atomic.Int64
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "exact fixture"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		executed.Add(1)
		return map[string]any{"receipt": "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := func(context.Context, bot.RuntimeEvent) error { return nil }
	adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(ToolCallTurn("read_file", map[string]any{"body": "exact"})), []tool.Tool{action}, &cleaned))
	waiting, err := adapter.Start(ctx, req, sink)
	if err != nil || waiting.Status != store.RunWaitingApproval {
		t.Fatal(waiting, err)
	}
	req.Run.Checkpoint = waiting.Checkpoint
	req.Run.Lease.Fence++
	s.a.Status = store.ApprovalApproved
	original := *s.a
	driftCtx := agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeDriftClassifier{drift: true}, Guard: resumeTestGuard{}})
	final := NewMockLLM(TextTurn("Exact receipt observed."))
	adapter, _ = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, final, []tool.Tool{action}, &cleaned))
	denied, err := adapter.Resume(driftCtx, req, sink)
	if err == nil || denied.Status != store.RunWaitingApproval || !bytes.Equal(denied.Checkpoint, waiting.Checkpoint) || s.a.Status != store.ApprovalApproved || s.a.ID != original.ID || s.consumed != 0 || executed.Load() != 0 || len(final.Calls) != 0 {
		t.Fatalf("denied=%+v err=%v approval=%+v consumed=%d executed=%d model=%d", denied, err, s.a, s.consumed, executed.Load(), len(final.Calls))
	}
	adapter, _ = NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, final, []tool.Tool{action}, &cleaned))
	_, err = adapter.Resume(ctx, req, sink)
	if err != nil || executed.Load() != 1 || s.consumed != 1 {
		t.Fatalf("restore err=%v executed=%d consumed=%d", err, executed.Load(), s.consumed)
	}
	if _, err = adapter.Resume(ctx, req, sink); err == nil || executed.Load() != 1 {
		t.Fatal("consumed action replayed")
	}
}
