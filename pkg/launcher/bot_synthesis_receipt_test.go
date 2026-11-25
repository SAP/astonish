package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

func TestBotSynthesisStoppedReceiptExactInvocation(t *testing.T) {
	for _, path := range []string{"autonomous", "interactive", "dispatch_boundary"} {
		t.Run(path, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			binding := botFactoryModelForTest(t, got.Agent.LLM).binding
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			binding.synthesisReserve = newBotSynthesisReserve(meter, store.BudgetSpec{})
			binding.synthesisReserve.final = true
			base := binding.owner // adapter installs its own trusted per-attempt ledger
			ctx := botFactoryHandoffContext{base: base, session: f.req.Run.SessionID.String(), functionCallID: "rejected-exact"}
			target := &synthesisBlockingRead{}
			var result map[string]any
			switch path {
			case "autonomous":
				result, err = (&botBoundTool{botRunnableTool: target, binding: binding}).Run(ctx, map[string]any{})
			case "interactive":
				result, err = (&interactiveBotTool{botRunnableTool: target, binding: binding}).Run(ctx, map[string]any{})
			case "dispatch_boundary":
				result, err = binding.runSynthesisTool(ctx, target, map[string]any{})
			}
			if err != nil || result["status"] != "not_executed" || target.calls != 0 {
				t.Fatalf("result=%v calls=%d err=%v", result, target.calls, err)
			}
			outcome, ok := bot.TakeToolOutcome(base, ctx.FunctionCallID())
			if !ok || !outcome.NonexecutionFor(base, ctx.FunctionCallID(), target.Name(), map[string]any{}) || outcome.Nonexecution.Disposition != bot.ToolFeedbackTerminal {
				t.Fatalf("missing exact nonexecution receipt: %+v", outcome)
			}
			if !outcome.Verified || len(outcome.Changed) != 0 || len(outcome.Unchanged) != 0 || outcome.ReceiptConflict {
				t.Fatalf("rejection falsely certified effects: %+v", outcome)
			}
			if _, ok := bot.TakeToolOutcome(base, ctx.FunctionCallID()); ok {
				t.Fatal("receipt consumed twice")
			}
		})
	}
}

func TestBotSynthesisStoppedReceiptDuplicateCannotEraseUncertainty(t *testing.T) {
	base, _ := bot.WithToolOutcomes(context.Background())
	ctx := botFactoryHandoffContext{base: base, functionCallID: "duplicate"}
	bot.RecordToolOutcome(base, ctx.FunctionCallID(), bot.ToolOutcome{Unverified: []string{"started effect unresolved"}})
	binding := &botFactoryBinding{synthesisReserve: &botSynthesisReserve{final: true, meter: synthesisReceiptMeter{}}}
	if _, err := binding.runSynthesisTool(ctx, &synthesisBlockingRead{}, nil); err != nil {
		t.Fatal(err)
	}
	outcome, ok := bot.TakeToolOutcome(base, ctx.FunctionCallID())
	if !ok || !outcome.ReceiptConflict || outcome.Nonexecution != nil || len(outcome.Unverified) != 1 {
		t.Fatalf("duplicate erased prior uncertainty: %+v", outcome)
	}
}

type synthesisReceiptMeter struct{}

func (synthesisReceiptMeter) Deadline() time.Time   { return time.Time{} }
func (synthesisReceiptMeter) Usage() store.BotUsage { return store.BotUsage{} }

type synthesisStartedTimeout struct {
	botRunnableTool
	calls int
}

func (*synthesisStartedTimeout) Name() string { return "unknown_write" }
func (s *synthesisStartedTimeout) Run(ctx tool.Context, _ any) (map[string]any, error) {
	s.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBotSynthesisStartedTimeoutHasNoNonexecutionReceipt(t *testing.T) {
	base, _ := bot.WithToolOutcomes(context.Background())
	lifetime, cancel := context.WithTimeout(base, time.Millisecond)
	defer cancel()
	ctx := botFactoryHandoffContext{base: lifetime, functionCallID: "started"}
	binding := &botFactoryBinding{}
	target := &synthesisStartedTimeout{}
	_, err := binding.runSynthesisTool(ctx, target, nil)
	if !errors.Is(err, context.DeadlineExceeded) || target.calls != 1 {
		t.Fatalf("started timeout calls=%d err=%v", target.calls, err)
	}
	if outcome, ok := bot.TakeToolOutcome(base, ctx.FunctionCallID()); ok {
		t.Fatalf("started operation minted receipt: %+v", outcome)
	}
}
