package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/sandbox"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// Dispatch settlement is delayed until the next persisted model boundary. The
// adapter installs bot.WithProgressDispatch and bot.WithProgressController before
// factory construction. Ordinary Chat and legacy Bot requests have neither hook.
// The shared dispatch capability owns lease/revision fencing and durable usage.
type botProgressBridge struct {
	mu      sync.Mutex
	active  int
	pending []func(context.Context) error
}

func (p *botProgressBridge) boundary(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != 0 {
		return bot.ErrProgressBoundaryUnsafe
	}
	for len(p.pending) != 0 {
		if err := p.pending[0](ctx); err != nil {
			return err
		}
		p.pending = p.pending[1:]
	}
	if d := bot.ProgressDispatchFromContext(ctx); d != nil {
		return d.Boundary(ctx)
	}
	return nil
}

func (b *botFactoryBinding) beginProgress(ctx context.Context, kind store.BotProgressDispatchKind, operation any, usage store.BotUsage) (func(store.BotProgressDispatchOutcome, store.BotUsage, string), error) {
	if !bot.ProgressExecutionEnabled(b.req) {
		return func(store.BotProgressDispatchOutcome, store.BotUsage, string) {}, nil
	}
	d := bot.ProgressDispatchFromContext(b.owner)
	if d == nil || b.progress == nil {
		return nil, store.ErrBotUnsupported
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	b.progress.mu.Lock()
	b.progress.active++
	b.progress.mu.Unlock()
	receipt, err := d.Begin(ctx, kind, digest, usage)
	if err != nil || receipt == nil {
		b.progress.mu.Lock()
		b.progress.active--
		b.progress.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, store.ErrBotInvalid
	}
	return func(outcome store.BotProgressDispatchOutcome, actual store.BotUsage, evidence string) {
		b.progress.mu.Lock()
		defer b.progress.mu.Unlock()
		b.progress.active--
		b.progress.pending = append(b.progress.pending, func(c context.Context) error { return d.Settle(c, receipt.DispatchID, outcome, actual, evidence) })
	}, nil
}

// All tools, including direct email and MCP branches, enter this wrapper after
// authorization and reservation. Calls remain joined on cancellation: returning
// early from a goroutine would permit evaluation while an effect is still live.
func (b *botFactoryBinding) progressTool(ctx botBoundToolContext, t botRunnableTool, args any, run func(tool.Context, any) (map[string]any, error)) (map[string]any, error) {
	if !bot.ProgressExecutionEnabled(b.req) {
		return run(ctx, args)
	}
	finish, err := b.beginProgress(ctx, store.BotProgressDispatchTool, struct {
		Name string
		Args any
	}{t.Name(), args}, store.BotUsage{ToolCalls: 1})
	if err != nil {
		return nil, err
	}
	call, cancel := context.WithTimeout(ctx, time.Duration(b.req.Version.Budget.RuntimePolicy.ToolCallTimeoutSeconds)*time.Second)
	defer cancel()
	result, callErr := run(preserveBotToolBinding(ctx, botProgressToolContext{Context: ctx, call: call}), args)
	callErr = errors.Join(callErr, call.Err())
	outcome := store.BotProgressDispatchUncertain
	if botProgressPinnedRead(t, args) {
		outcome = store.BotProgressDispatchSettled
	}
	// Native policy classification is trusted; unknown/MCP tools remain
	// conservative. Reads may fail without creating an uncertain external write.
	if classifier := agent.BotPolicyRuntimeFromContext(ctx).Classifier; classifier != nil {
		raw, marshalErr := json.Marshal(args)
		if marshalErr == nil {
			if action, classifyErr := classifier.DescribeAction(ctx, t.Name(), raw); classifyErr == nil && action.ConstraintsVerified && action.Effect == execution.EffectRead {
				outcome = store.BotProgressDispatchSettled
			}
		}
	}
	evidence := ""
	if outcome == store.BotProgressDispatchSettled {
		// Pinned implementation or the trusted classifier proves this dispatch
		// is a read, even if no content receipt was produced (for example a
		// controlled search adapter). This is not evidence of successful facts.
		evidence = "trusted-read-dispatch:" + ctx.FunctionCallID()
	}
	// Only an out-of-band exact trusted receipt can prove a write settled. Do
	// not infer nonexecution from an error or a response's status/success fields.
	if receipt, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok {
		bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), receipt)
		if !receipt.ReceiptConflict && receipt.Nonexecution != nil && receipt.Nonexecution.CallID == ctx.FunctionCallID() && receipt.Nonexecution.ToolName == t.Name() {
			outcome = store.BotProgressDispatchNotExecuted
			evidence = "tool-receipt:" + ctx.FunctionCallID()
		} else if !receipt.ReceiptConflict && receipt.Verified && len(receipt.Unverified) == 0 {
			outcome = store.BotProgressDispatchSettled
			evidence = "tool-receipt:" + ctx.FunctionCallID()
		}
	}
	finish(outcome, store.BotUsage{}, evidence)
	return result, callErr
}

// Only pinned built-in implementations qualify; an MCP name or annotation is
// never proof of read semantics. A failed read settles an operation, not evidence
// that the requested source was retrieved.
func botProgressPinnedRead(t botRunnableTool, args any) bool {
	switch t.(type) {
	case *sandbox.NodeTool, *botSandboxTool:
	default:
		return false
	}
	switch t.Name() {
	case "read_file", "find_files", "grep_search", "file_tree", "web_fetch":
		return true
	case "http_request":
		raw, err := json.Marshal(args)
		if err != nil {
			return false
		}
		var values struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		method := strings.ToUpper(values.Method)
		return method == "" || method == "GET" || method == "HEAD"
	}
	return false
}

type botProgressToolContext struct {
	tool.Context
	call context.Context
}

func (c botProgressToolContext) Deadline() (time.Time, bool) { return c.call.Deadline() }
func (c botProgressToolContext) Done() <-chan struct{}       { return c.call.Done() }
func (c botProgressToolContext) Err() error                  { return c.call.Err() }

type botProgressLLM struct {
	model.LLM
	binding *botFactoryBinding
}

func (l *botProgressLLM) EstimateInputTokens(ctx context.Context, request *model.LLMRequest) (int64, error) {
	return provider.EstimateModelInput(ctx, l.LLM, request)
}

func (l *botProgressLLM) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if l.binding.req.Version.Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 {
			for r, e := range l.LLM.GenerateContent(ctx, request, stream) {
				if !yield(r, e) {
					return
				}
			}
			return
		}
		finish, err := l.binding.beginProgress(ctx, store.BotProgressDispatchModel, request.Contents, store.BotUsage{})
		if err != nil {
			yield(nil, err)
			return
		}
		call := provider.WithGenerationHealthCheck(ctx, l.binding.checkDuringGeneration)
		if policy := l.binding.req.Version.Budget.RuntimePolicy; policy.GenerationHealth == nil {
			var cancel context.CancelFunc
			call, cancel = context.WithTimeout(call, time.Duration(policy.ModelCallTimeoutSeconds)*time.Second)
			defer cancel()
		}
		var usage store.BotUsage
		defer func() { finish(store.BotProgressDispatchSettled, usage, "") }()
		for r, e := range l.LLM.GenerateContent(call, request, stream) {
			if r != nil && r.UsageMetadata != nil {
				input, output := bot.ModelUsageTokens(r.UsageMetadata)
				// Usage snapshots are cumulative, including repeated trailers.
				usage.InputTokens = max(usage.InputTokens, input)
				usage.OutputTokens = max(usage.OutputTokens, output)
			}
			// A consumer may drain a failed generation to observe BudgetLLM's
			// trailing settlement error before authorizing a bounded correction.
			if !yield(r, e) {
				return
			}
		}
		if call.Err() != nil {
			yield(nil, call.Err())
		}
	}
}
