package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

var errBindingBudgetExhausted = errors.New("tool budget exhausted")

type bindingTimeoutBudget struct {
	botFactoryTestBudget
	timeout time.Duration
	calls   int
	reject  error
}

func (b *bindingTimeoutBudget) ToolCallTimeout() time.Duration { return b.timeout }
func (b *bindingTimeoutBudget) ReserveTool(ctx context.Context, _ string) error {
	b.calls++
	if b.reject != nil {
		return b.reject
	}
	return ctx.Err()
}

func bindingQuestionFixture(t *testing.T, progress bool, timeout time.Duration) (*botFactoryBinding, *botBoundTool, *bindingTimeoutBudget, *progressDispatchProbe) {
	t.Helper()
	f := newBotFactoryFixture(t)
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"ask_user"}})
	if err != nil {
		t.Fatal(err)
	}
	budget := &bindingTimeoutBudget{timeout: timeout}
	f.req.Budget = budget
	resources, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resources.Cleanup)
	binding := botFactoryModelForTest(t, resources.Agent.LLM).binding
	binding.owner = bot.WithInteractionOwner(binding.owner)
	probe := &progressDispatchProbe{}
	if progress {
		binding.req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
		binding.owner = bot.WithProgressDispatch(binding.owner, probe)
		binding.installProgressBridge()
	}
	raw, err := botWorkflowQuestionTool(binding.req, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := newBotBoundTool(raw, binding)
	if err != nil {
		t.Fatal(err)
	}
	return binding, wrapped.(*botBoundTool), budget, probe
}

func bindingQuestionContext(b *botFactoryBinding, base context.Context) botFactoryHandoffContext {
	return botFactoryHandoffContext{base: base, session: b.req.Run.SessionID.String(), functionCallID: "bound-question"}
}

type bindingObservedTool struct {
	botRunnableTool
	observe func(tool.Context)
}

func (t bindingObservedTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	t.observe(ctx)
	return t.botRunnableTool.Run(ctx, args)
}

func TestBotWorkflowBindingThroughProgressAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name     string
		progress bool
		timeout  time.Duration
		narrow   bool
	}{
		{"baseline", false, 0, false},
		{"progress", true, 0, false},
		{"timeout", false, time.Second, false},
		{"composed", true, time.Second, false},
		{"composed_read_deadline", true, time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, target, budget, probe := bindingQuestionFixture(t, tc.progress, tc.timeout)
			if tc.narrow {
				meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				b.synthesisReserve = newBotSynthesisReserve(meter, store.BudgetSpec{})
				b.owner = agent.WithBotPolicyRuntime(b.owner, agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: BotCatalogueClassifier{}})
			}
			result, err := target.Run(bindingQuestionContext(b, b.owner), map[string]any{"kind": "text", "prompt": "Which destination?"})
			if err != nil {
				t.Fatalf("real ask_user through bound dispatch: %v", err)
			}
			questionID, _ := result["questionId"].(string)
			if result["status"] != "ok" || result["prompt"] != "Which destination?" || questionID == "" {
				t.Fatalf("missing genuine question result: %#v", result)
			}
			if budget.calls != 1 {
				t.Fatalf("tool reservations = %d", budget.calls)
			}
			if tc.progress {
				if probe.began != 1 || probe.settled != 0 {
					t.Fatalf("dispatch accounting: %+v", probe)
				}
				if err := b.progress.boundary(b.owner); err != nil {
					t.Fatal(err)
				}
				if probe.settled != 1 {
					t.Fatalf("settlements = %d", probe.settled)
				}
				if probe.outcome != store.BotProgressDispatchSettled {
					t.Fatalf("genuine question classified as uncertain external effect: %+v", probe)
				}
			}
		})
	}
}

func TestBotWorkflowQuestionProgressWithoutAutonomousClassifier(t *testing.T) {
	b, target, _, probe := bindingQuestionFixture(t, true, time.Second)
	// Interactive Chat is authorized by the caller ceiling, not an autonomous classifier.
	b.owner = agent.WithBotPolicyRuntime(b.owner, agent.BotPolicyRuntime{})
	ctx := botBoundToolContext{Context: bindingQuestionContext(b, b.owner), owner: b.owner}
	result, err := b.progressTool(ctx, target.botRunnableTool, map[string]any{"kind": "text", "prompt": "Which destination?"}, func(c tool.Context, args any) (map[string]any, error) {
		return b.runSynthesisTool(c, target.botRunnableTool, args)
	})
	if err != nil || result["status"] != "ok" {
		t.Fatalf("genuine question: %v %v", result, err)
	}
	if err := b.progress.boundary(b.owner); err != nil {
		t.Fatal(err)
	}
	if probe.outcome != store.BotProgressDispatchSettled {
		t.Fatalf("genuine question classified as uncertain external effect: %+v", probe)
	}
	if botProgressPinnedRead(progressNamedTool{name: "ask_user"}, nil) {
		t.Fatal("untrusted question name acquired pinned read semantics")
	}
}

func TestBotWorkflowBindingWrappersRejectUnboundAndForeign(t *testing.T) {
	b, target, _, _ := bindingQuestionFixture(t, true, time.Second)
	raw := target.botRunnableTool
	args := map[string]any{"kind": "text", "prompt": "Which destination?"}
	unbound := bindingQuestionContext(b, b.owner)
	// Matching identity values alone must not grant the private binding marker.
	if _, err := b.runSynthesisTool(unbound, raw, args); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("unbound context accepted: %v", err)
	}
	foreignID := b.req.Policy.Identity()
	foreignID.RunID = uuid.New()
	for _, owner := range []context.Context{
		execution.WithBotRunIdentity(b.owner, foreignID),
		store.WithSessionID(b.owner, uuid.NewString()),
	} {
		bound := botBoundToolContext{Context: unbound, owner: owner}
		_, err := b.progressTool(bound, raw, args, func(ctx tool.Context, args any) (map[string]any, error) {
			return b.runSynthesisTool(ctx, raw, args)
		})
		if !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("foreign context accepted: %v", err)
		}
	}
	// An arbitrary tool.Context embedding the bound context is not trusted.
	foreignWrapper := struct{ tool.Context }{botBoundToolContext{Context: unbound, owner: b.owner}}
	if _, err := b.runSynthesisTool(foreignWrapper, raw, args); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("arbitrary wrapper accepted: %v", err)
	}
}

func TestBotWorkflowBindingComposedLifetime(t *testing.T) {
	for _, mode := range []string{"parent_cancel", "parent_deadline", "tool_timeout", "progress_timeout"} {
		t.Run(mode, func(t *testing.T) {
			b, target, budget, probe := bindingQuestionFixture(t, true, 30*time.Millisecond)
			base, cancel := context.WithCancel(b.owner)
			defer cancel()
			var expectedDeadline time.Time
			if mode == "parent_deadline" {
				expectedDeadline = time.Now().Add(10 * time.Millisecond)
				var deadlineCancel context.CancelFunc
				base, deadlineCancel = context.WithDeadline(base, expectedDeadline)
				defer deadlineCancel()
			}
			if mode == "progress_timeout" {
				// An immediately expired progress timeout proves that the second
				// wrapper cannot extend the dispatch deadline.
				b.req.Version.Budget.RuntimePolicy.ToolCallTimeoutSeconds = 0
			}
			started := time.Now()
			var seen tool.Context
			target.botRunnableTool = bindingObservedTool{botRunnableTool: target.botRunnableTool, observe: func(ctx tool.Context) {
				seen = ctx
				if _, ok := ctx.(botBoundToolContext); !ok {
					t.Errorf("lost trusted binding: %T", ctx)
				}
				if ctx.FunctionCallID() != "bound-question" || ctx.SessionID() != b.req.Run.SessionID.String() {
					t.Error("lost ADK metadata")
				}
				id, ok := execution.BotRunIdentityFromContext(ctx)
				if !ok || id != b.req.Policy.Identity() {
					t.Error("lost owner identity")
				}
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(time.Now().Add(budget.timeout)) || deadline.Before(started) {
					t.Errorf("wrong deadline: %v", deadline)
				}
				if !expectedDeadline.IsZero() && !deadline.Equal(expectedDeadline) {
					t.Error("parent deadline extended")
				}
				if mode == "parent_cancel" {
					cancel()
				}
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("composed context did not cancel")
				}
			}}
			_, err := target.Run(bindingQuestionContext(b, base), map[string]any{"kind": "text", "prompt": "Which destination?"})
			want := error(context.DeadlineExceeded)
			if mode == "parent_cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("error = %v want %v", err, want)
			}
			if seen == nil || !errors.Is(seen.Err(), want) || budget.calls != 1 || probe.began != 1 {
				t.Fatal("lost cancellation or accounting")
			}
			if probe.settled != 0 {
				t.Fatal("settled before boundary")
			}
			if err := b.progress.boundary(b.owner); err != nil {
				t.Fatal(err)
			}
			if probe.settled != 1 {
				t.Fatal("dispatch not settled at boundary")
			}
		})
	}
}

func TestBotWorkflowBindingPreservesBudgetAndCancellation(t *testing.T) {
	for _, cancelCall := range []bool{false, true} {
		b, target, budget, probe := bindingQuestionFixture(t, true, time.Second)
		base, cancel := context.WithCancel(b.owner)
		if cancelCall {
			cancel()
		} else {
			budget.reject = errBindingBudgetExhausted
		}
		_, err := target.Run(bindingQuestionContext(b, base), map[string]any{"kind": "text", "prompt": "Which destination?"})
		cancel()
		want := error(errBindingBudgetExhausted)
		if cancelCall {
			want = context.Canceled
		}
		if !errors.Is(err, want) {
			t.Fatalf("cancel=%v error=%v want=%v", cancelCall, err, want)
		}
		if probe.began != 0 {
			t.Fatalf("rejected call dispatched: %+v", probe)
		}
	}
}
