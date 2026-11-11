package api

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type resumeTranscript struct{}

func (resumeTranscript) AppendBotRunEvent(context.Context, store.BotLease, uuid.UUID, *session.Event) error {
	return nil
}

type resumeTestStore struct {
	store.BotStore
	a        *store.BotApproval
	consumed int
}

func (s *resumeTestStore) RequestApproval(_ context.Context, l store.BotLease, a store.BotApproval) (*store.BotApproval, error) {
	a.ID = uuid.New()
	a.Status = store.ApprovalPending
	s.a = &a
	return s.a, nil
}
func (s *resumeTestStore) LoadResumeApproval(context.Context, store.BotLease) (*store.BotApproval, error) {
	if s.a.Status != store.ApprovalApproved {
		return nil, store.ErrBotConflict
	}
	return s.a, nil
}
func (s *resumeTestStore) RequeueApprovedRun(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *resumeTestStore) ConsumeApproval(_ context.Context, q store.BotApprovalConsumption) error {
	if s.a.Status != store.ApprovalApproved || q.ActionDigest != s.a.ActionDigest {
		return store.ErrBotConflict
	}
	s.a.Status = store.ApprovalConsumed
	s.consumed++
	return nil
}

type resumeTestClassifier struct{}

func (resumeTestClassifier) DescribeAction(_ context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: n, Effect: execution.EffectSend, Arguments: a, ConstraintsVerified: true}, nil
}

type resumeTestGuard struct{}

func (resumeTestGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		d.Kind = execution.PolicyAllow
	}
	return d, nil
}

func TestBotChatExactApprovalContinuation(t *testing.T) {
	for _, intent := range []store.BotExecutionIntent{store.BotIntentRegular, store.BotIntentDraftTest} {
		t.Run(string(intent), func(t *testing.T) {
			testBotChatExactApprovalContinuation(t, intent)
		})
	}
}

// The durable personal/team approval transaction is covered separately by
// TestBotExecutionIntentsDurableLease. Here we exercise the actual Chat runtime
// and executable exact-call callback, including draft intent on both attempts.
func testBotChatExactApprovalContinuation(t *testing.T, intent store.BotExecutionIntent) {
	ctx, req, ss := botChatTestRequest(t)
	req.Run.Intent = intent
	req.Bot.Status = intent.RequiredStatus()
	factory := func(llm model.LLM, targets []tool.Tool, cleaned *atomic.Int64) BotChatAgentFactory {
		inner := botChatTestFactory(t, ss, llm, targets, cleaned)
		return func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
			r, err := inner(ctx, req)
			if r != nil {
				r.Transcript = resumeTranscript{}
			}
			return r, err
		}
	}
	s := &resumeTestStore{}
	ctx = bot.WithApprovalStore(ctx, s)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: resumeTestGuard{}})
	var executed, cleaned atomic.Int64
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled exact action"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		if args["body"] != "exact" {
			t.Error("changed args")
		}
		executed.Add(1)
		return map[string]any{"receipt": "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first := NewMockLLM(ToolCallTurn("read_file", map[string]any{"body": "exact"}))
	a, _ := NewBotChatRuntimeAdapter(factory(first, []tool.Tool{read}, &cleaned))
	sink := func(context.Context, bot.RuntimeEvent) error { return nil }
	waiting, err := a.Start(ctx, req, sink)
	if err != nil || waiting.Status != store.RunWaitingApproval || executed.Load() != 0 {
		t.Fatalf("wait %+v %v executions %d", waiting, err, executed.Load())
	}
	req.Run.Checkpoint = waiting.Checkpoint
	req.Run.Lease.Fence++
	s.a.Status = store.ApprovalApproved
	final := NewMockLLM(TextTurn("Exact action receipt observed."))
	a, _ = NewBotChatRuntimeAdapter(factory(final, []tool.Tool{read}, &cleaned))
	result, err := a.Resume(ctx, req, sink)
	if err != nil || executed.Load() != 1 || s.consumed != 1 || len(final.Calls) != 1 || result.Report.Summary != "Exact action receipt observed." {
		t.Fatalf("resume %+v %v executed %d consumed %d model %d", result, err, executed.Load(), s.consumed, len(final.Calls))
	}
	if err := result.Report.ValidateEmployeeMessageIntegrity(result.Status); err != nil {
		t.Fatalf("final exact-action report has a stale employee envelope: %v", err)
	}
	if _, err := a.Resume(ctx, req, sink); err == nil {
		t.Fatal("consumed action replayed")
	}
}
