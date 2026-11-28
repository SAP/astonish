package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// BudgetLLMOptions describes the effective model, not global pooled state.
// EstimateInput must conservatively include instructions, tools and media.
// When nil, a byte bound is refined only for an identical, previously measured
// prompt prefix; appended messages retain their byte bound (budget_input.go).
// Price returns nil when the effective model's cost is unknown. It must return
// a conservative upper bound when pricing a reservation. Callbacks must be safe
// for concurrent use. Hidden provider retries cannot be individually metered;
// billable retries must call this decorator again rather than retry beneath it.
type BudgetLLMOptions struct {
	MaxOutputTokens     int32
	SupportsOutputLimit bool
	EstimateInput       func(context.Context, *model.LLMRequest) (int64, error)
	Price               func(inputTokens, outputTokens int64) (*int64, error)
}

// BudgetLLM is a per-run decorator. Never store it in the shared provider Pool.
// Parent and child decorators may wrap different models with the same budget.
type BudgetLLM struct {
	inner        model.LLM
	budget       execution.RunBudget
	options      BudgetLLMOptions
	inputHistory budgetInputHistory
}

func NewBudgetLLM(inner model.LLM, budget execution.RunBudget, options BudgetLLMOptions) (*BudgetLLM, error) {
	if inner == nil || budget == nil || options.MaxOutputTokens <= 0 {
		return nil, errors.New("budget model requires model, budget and positive output bound")
	}
	// Without per-request support MaxOutputTokens must be the verified maximum
	// configured on the underlying provider. A smaller budget will fail closed.
	return &BudgetLLM{inner: inner, budget: budget, options: options}, nil
}

func (b *BudgetLLM) Name() string { return b.inner.Name() }

func (b *BudgetLLM) price(input, output int64) (*int64, error) {
	if b.options.Price == nil {
		return nil, nil
	}
	cost, err := b.options.Price(input, output)
	if err != nil {
		return nil, err
	}
	if cost != nil {
		if *cost < 0 {
			return nil, errors.New("negative model price")
		}
		n := *cost
		return &n, nil
	}
	return nil, nil
}

func (b *BudgetLLM) GenerateContent(parentCtx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		ctx := parentCtx
		if req == nil {
			EndModelPreflight(ctx)
			yield(nil, errors.New("nil budget model request"))
			return
		}
		if timeout, ok := b.budget.(interface{ ModelCallTimeout() time.Duration }); ok && timeout.ModelCallTimeout() > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout.ModelCallTimeout())
			defer cancel()
		}
		if deadline, ok := b.budget.(interface{ Deadline() time.Time }); ok && !deadline.Deadline().IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline.Deadline())
			defer cancel()
		}
		var input, output int64
		var snapshot *budgetInputSnapshot
		// The optional scope covers synchronous checks/reservation only. The
		// closure closes it before any provider call or consumer yield (even errors).
		r, err := func() (execution.BudgetReservation, error) {
			defer EndModelPreflight(ctx)
			preflight := ctx
			if scoped, ok := b.budget.(interface {
				BeginModelPreflight(context.Context) (context.Context, func())
			}); ok {
				var end func()
				preflight, end = scoped.BeginModelPreflight(ctx)
				defer end()
			}
			if err := b.budget.Check(preflight); err != nil {
				return execution.BudgetReservation{}, err
			}
			var err error
			input, snapshot, err = b.estimateInput(preflight, req)
			if err != nil {
				return execution.BudgetReservation{}, err
			}
			output = int64(b.options.MaxOutputTokens)
			if b.options.SupportsOutputLimit && req.Config != nil && req.Config.MaxOutputTokens > 0 {
				output = min(output, int64(req.Config.MaxOutputTokens))
			}
			if input < 0 || input > math.MaxInt64-output {
				return execution.BudgetReservation{}, errors.New("invalid input token estimate")
			}
			cost, err := b.price(input, output)
			if err != nil {
				return execution.BudgetReservation{}, err
			}
			return b.budget.ReserveModel(preflight, execution.BudgetReservation{InputTokens: input, OutputTokens: output, CostMicros: cost})
		}()
		if err != nil {
			yield(nil, err)
			return
		}
		// Settle even if the consumer stops, cancellation occurs, or the provider
		// panics. No usage is not proof of no charge. Keep the entire reservation.
		usage := execution.BudgetUsage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens}
		settled := false
		settle := func() error {
			if settled {
				return nil
			}
			settled = true
			return b.budget.SettleModel(context.WithoutCancel(ctx), r.ID, usage)
		}
		defer func() { _ = settle() }()
		if r.OutputTokens <= 0 || r.OutputTokens > output || r.InputTokens != input || r.OutputTokens > math.MaxInt32 {
			yield(nil, errors.New("invalid model budget allowance"))
			return
		}
		if !b.options.SupportsOutputLimit && r.OutputTokens != output {
			// No provider call happened: release the complete hold, including price.
			zero := int64(0)
			usage = execution.BudgetUsage{CostMicros: &zero}
			yield(nil, errors.Join(errors.New("provider cannot enforce reduced output allowance"), settle()))
			return
		}
		cloned := *req
		config := genai.GenerateContentConfig{}
		if req.Config != nil {
			config = *req.Config
		}
		if b.options.SupportsOutputLimit {
			config.MaxOutputTokens = int32(r.OutputTokens)
		}
		cloned.Config = &config
		var stopHealth func() error
		if health, ok := b.budget.(interface {
			GenerationHealth() *execution.BotGenerationHealthPolicy
		}); ok {
			if policy := health.GenerationHealth(); policy != nil {
				check, _ := ctx.Value(generationHealthCheckKey{}).(func(context.Context) error)
				ctx, stopHealth = streaming.StartHealthMonitor(ctx, streaming.HealthOptions{
					IdleTimeout:   time.Duration(policy.IdleTimeoutSeconds) * time.Second,
					CheckInterval: time.Duration(policy.CheckIntervalSeconds) * time.Second,
					Check: func(c context.Context) error {
						if err := CheckGenerationBudget(c, b.budget); err != nil {
							return err
						}
						if check != nil {
							return check(c)
						}
						return nil
					},
				})
				// Runs after the provider iterator unwinds, including early consumer exit.
				defer func() { _ = stopHealth() }()
			}
		}
		observed := execution.BudgetUsage{}
		seen, finalUsage, failed := false, false, false
		finalPrompt := false
		for response, callErr := range b.inner.GenerateContent(ctx, &cloned, stream) {
			if response != nil && response.UsageMetadata != nil {
				m := response.UsageMetadata
				in := max(int64(m.PromptTokenCount), 0) + max(int64(m.ToolUsePromptTokenCount), 0)
				out := max(int64(m.CandidatesTokenCount), 0) + max(int64(m.ThoughtsTokenCount), 0)
				out = max(out, int64(m.TotalTokenCount)-in)
				// Provider adapters report cumulative usage, including repeated final
				// metadata. Take maxima; never sum repeated chunks.
				observed.InputTokens = max(observed.InputTokens, in)
				observed.OutputTokens = max(observed.OutputTokens, out)
				// Keep conservative accounting even if the consumer stops here.
				usage.InputTokens = max(usage.InputTokens, observed.InputTokens)
				usage.OutputTokens = max(usage.OutputTokens, observed.OutputTokens)
				seen = seen || in+out > 0
				finalUsage = finalUsage || (!response.Partial && in+out > 0)
				finalPrompt = finalPrompt || (!response.Partial && m.PromptTokenCount > 0)
			}
			if callErr != nil || response != nil && (response.ErrorCode != "" || response.Interrupted) {
				failed = true
			}
			// A continuing consumer may need trailing failure evidence before
			// deciding whether recovery is safe. Drain after errors as requested,
			// retaining conservative settlement and any later usage metadata.
			if !yield(response, callErr) {
				return
			}
		}
		callContextErr := ctx.Err()
		if stopHealth != nil {
			// Normal stop cancels the monitor context; only its result classifies
			// generation failure. In particular final usage must still reconcile.
			callContextErr = stopHealth()
		}
		if seen && finalUsage && !failed && callContextErr == nil {
			usage = observed
			if usage.InputTokens == 0 {
				// Output-only usage does not prove the prompt was free.
				usage.InputTokens = r.InputTokens
			}
			usage.CostMicros, err = b.price(usage.InputTokens, usage.OutputTokens)
			if err != nil {
				usage.CostMicros = nil
			}
		} else {
			usage.InputTokens = max(usage.InputTokens, observed.InputTokens)
			usage.OutputTokens = max(usage.OutputTokens, observed.OutputTokens)
		}
		if err = errors.Join(err, callContextErr, settle()); err != nil {
			yield(nil, err)
		} else if finalPrompt && !failed {
			b.inputHistory.observe(snapshot, observed.InputTokens)
		}
	}
}

// A byte-count bound deliberately overestimates text rather than using a
// chars/4 heuristic. Remote/cached/inline media have no safe textual estimate;
// callers must provide a model-aware estimator for those requests.
func estimateBudgetInput(_ context.Context, req *model.LLMRequest) (int64, error) {
	contents := append([]*genai.Content(nil), req.Contents...)
	if req.Config != nil {
		if req.Config.CachedContent != "" {
			return 0, errors.New("cached content requires model-aware budget estimator")
		}
		contents = append(contents, req.Config.SystemInstruction)
	}
	for _, c := range contents {
		if c != nil {
			for _, p := range c.Parts {
				if p != nil && (p.InlineData != nil || p.FileData != nil) {
					return 0, errors.New("media requires model-aware budget estimator")
				}
			}
		}
	}
	data, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("estimate model request: %w", err)
	}
	return int64(len(data)) + 64, nil
}

var _ model.LLM = (*BudgetLLM)(nil)
