package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// This is a stopping policy, not a second allowance. All generation still goes
// through the original model reservation/settlement and live authorization.
// One shared instance follows parent, child and auxiliary calls for this run.
type botSynthesisReserve struct {
	mu     sync.Mutex
	budget execution.RunBudget
	meter  interface {
		Deadline() time.Time
		Usage() store.BotUsage
	}
	spec        store.BudgetSpec
	reserveTime time.Duration
	final       bool
	now         func() time.Time
}

func newBotSynthesisReserve(b execution.RunBudget, spec store.BudgetSpec) *botSynthesisReserve {
	meter, ok := b.(interface {
		Deadline() time.Time
		Usage() store.BotUsage
	})
	if !ok {
		return nil
	} // Test/legacy budgets retain their original enforcement.
	// A 300-second run keeps 60 seconds for composition, never extra runtime.
	if effective, ok := b.(interface{ TokenLimit() *int64 }); ok {
		spec.MaxTokens = effective.TokenLimit()
	}
	reserve := min(max(time.Duration(0), time.Until(meter.Deadline()))/5, 60*time.Second)
	return &botSynthesisReserve{budget: b, meter: meter, spec: spec, reserveTime: reserve, now: time.Now}
}

func (r *botSynthesisReserve) synthesis() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.now().Before(r.meter.Deadline().Add(-r.reserveTime)) {
		r.final = true
	}
	return r.final
}

func (r *botSynthesisReserve) prepare(ctx context.Context, req *model.LLMRequest) (*model.LLMRequest, bool, error) {
	if r == nil || req == nil {
		return req, false, nil
	}
	if err := r.budget.Check(ctx); err != nil {
		return nil, false, err
	}
	cloned := *req
	config := genai.GenerateContentConfig{}
	if req.Config != nil {
		config = *req.Config
	}
	cloned.Config = &config
	usage := r.meter.Usage()
	remaining := "unlimited"
	// Include the *whole* next prompt, not just output, in the stopping decision.
	// JSON bytes are deliberately conservative, not a learned bytes/token ratio.
	// Actual reservations (including unknown costs) remain the provider's job.
	if r.spec.MaxTokens != nil {
		left := max(int64(0), *r.spec.MaxTokens-usage.InputTokens-usage.OutputTokens)
		remaining = fmt.Sprint(left)
		raw, err := json.Marshal(struct {
			Contents []*genai.Content
			Config   *genai.GenerateContentConfig
		}{req.Contents, req.Config})
		if err != nil {
			return nil, false, err
		}
		input := int64(len(raw)) + 1024
		output := int64(4096)
		if config.MaxOutputTokens > 0 {
			output = min(output, int64(config.MaxOutputTokens))
		}
		r.mu.Lock()
		if usage.ToolCalls > 0 && left <= 2*(input+output) {
			r.final = true
		}
		r.mu.Unlock()
	}
	final := r.synthesis()
	guidance := fmt.Sprintf("Run budget remaining: %s tokens (input and output together), %.0f seconds until the original hard deadline. Keep %.0f seconds for synthesis. These are remaining limits, not new allowances. Refine the requested scope rather than adding optional research; deliver promptly from adequate evidence.", remaining, max(0, r.meter.Deadline().Sub(r.now()).Seconds()), r.reserveTime.Seconds())
	if final {
		config.Tools = nil
		config.ToolConfig = nil
		cloned.Tools = nil
		guidance += " Synthesis reserve has begun. Make the final answer now using only evidence already retained. No tools, new research, delegation or external calls. Self-check while composing: qualify or omit unsupported claims, disclose material gaps and unfinished obligations honestly, and never imply uncertain effects succeeded or retry them. Do not claim completeness merely because time is short. No mandatory review or finish ritual."
	}
	// Request-local user context preserves a stable system prefix for the existing
	// measured-input estimator; it is never persisted as a new owner assignment.
	cloned.Contents = append(append([]*genai.Content(nil), req.Contents...), genai.NewContentFromText(guidance, "user"))
	return &cloned, final, nil
}

var errBotSynthesisToolCall = errors.New("model requested a tool during final synthesis")

func botResponseHasToolCall(r *model.LLMResponse) bool {
	if r == nil || r.Content == nil {
		return false
	}
	for _, p := range r.Content.Parts {
		if p != nil && p.FunctionCall != nil {
			return true
		}
	}
	return false
}

func botSynthesisStoppedResult() map[string]any {
	return map[string]any{"status": "not_executed", "reason": "synthesis reserve reached; compose the final answer from retained evidence without further tools"}
}

// Deadline narrowing is only safe to recover as a source limitation for a
// mechanically verified read. Never turn cancellation of an unknown/write
// effect into success, or replay it. The original parent context remains live.
func (b *botFactoryBinding) synthesisReadContext(ctx tool.Context, name string, args any) (tool.Context, context.CancelFunc, bool) {
	noop := func() {}
	if b.synthesisReserve == nil {
		return ctx, noop, false
	}
	classifier := agent.BotPolicyRuntimeFromContext(b.owner).Classifier
	if classifier == nil {
		return ctx, noop, false
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ctx, noop, false
	}
	action, err := classifier.DescribeAction(ctx, name, raw)
	if err != nil || action.Effect != execution.EffectRead || !action.ConstraintsVerified {
		return ctx, noop, false
	}
	deadline := b.synthesisReserve.meter.Deadline().Add(-b.synthesisReserve.reserveTime)
	narrowed, cancel := context.WithDeadline(ctx, deadline)
	lifetime := botSynthesisToolContext{Context: ctx, lifetime: narrowed}
	if bound, ok := ctx.(botBoundToolContext); ok {
		bound.Context = lifetime
		return bound, cancel, true
	}
	return lifetime, cancel, true
}

type botSynthesisToolContext struct {
	tool.Context
	lifetime context.Context
}

func (c botSynthesisToolContext) Deadline() (time.Time, bool) { return c.lifetime.Deadline() }
func (c botSynthesisToolContext) Done() <-chan struct{}       { return c.lifetime.Done() }
func (c botSynthesisToolContext) Err() error                  { return c.lifetime.Err() }

func (b *botFactoryBinding) runSynthesisTool(ctx tool.Context, target botRunnableTool, args any) (map[string]any, error) {
	if b.synthesisReserve.synthesis() {
		return botSynthesisStoppedResult(), nil
	}
	readCtx, cancel, read := b.synthesisReadContext(ctx, target.Name(), args)
	defer cancel()
	result, err := target.Run(readCtx, args)
	if read && errors.Is(readCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil && b.synthesisReserve.synthesis() {
		// Recheck revocation, lease and hard budget before recovering the read timeout.
		if checkErr := b.check(ctx); checkErr != nil {
			return result, checkErr
		}
		limited := map[string]any{"status": "source_unavailable", "reason": "read interrupted at synthesis reserve; no source result established"}
		if result != nil {
			// Preserve the adapter's receipt verbatim; never relabel it as success.
			limited["interrupted_result"] = result
		}
		return limited, nil
	}
	return result, err
}
