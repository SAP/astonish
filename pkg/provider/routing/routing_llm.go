package routing

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// RoutingStats tracks cumulative routing decisions and token usage (thread-safe via atomics).
type RoutingStats struct {
	strongCalls atomic.Int64
	mediumCalls atomic.Int64
	weakCalls   atomic.Int64

	// Per-tier token accumulators. Populated by the GenerateContent wrapper
	// from UsageMetadata on each response. Zero when the provider does not
	// report token usage.
	strongPromptTokens     atomic.Int64
	strongCompletionTokens atomic.Int64
	mediumPromptTokens     atomic.Int64
	mediumCompletionTokens atomic.Int64
	weakPromptTokens       atomic.Int64
	weakCompletionTokens   atomic.Int64
}

// RecordStrong increments the strong model call counter.
func (s *RoutingStats) RecordStrong() { s.strongCalls.Add(1) }

// RecordMedium increments the medium model call counter.
func (s *RoutingStats) RecordMedium() { s.mediumCalls.Add(1) }

// RecordWeak increments the weak model call counter.
func (s *RoutingStats) RecordWeak() { s.weakCalls.Add(1) }

// RecordTokens adds the prompt and completion token counts for the given tier.
// Called once per response (or once per streaming chunk that carries usage).
func (s *RoutingStats) RecordTokens(tier string, prompt, completion int32) {
	switch tier {
	case "strong":
		s.strongPromptTokens.Add(int64(prompt))
		s.strongCompletionTokens.Add(int64(completion))
	case "medium":
		s.mediumPromptTokens.Add(int64(prompt))
		s.mediumCompletionTokens.Add(int64(completion))
	case "weak":
		s.weakPromptTokens.Add(int64(prompt))
		s.weakCompletionTokens.Add(int64(completion))
	}
}

// Total returns the total number of routing decisions.
func (s *RoutingStats) Total() int64 {
	return s.strongCalls.Load() + s.mediumCalls.Load() + s.weakCalls.Load()
}

// StrongCount returns the number of strong model calls.
func (s *RoutingStats) StrongCount() int64 { return s.strongCalls.Load() }

// MediumCount returns the number of medium model calls.
func (s *RoutingStats) MediumCount() int64 { return s.mediumCalls.Load() }

// WeakCount returns the number of weak model calls.
func (s *RoutingStats) WeakCount() int64 { return s.weakCalls.Load() }

// StrongPromptTokens returns accumulated prompt tokens routed to the strong model.
func (s *RoutingStats) StrongPromptTokens() int64 { return s.strongPromptTokens.Load() }

// StrongCompletionTokens returns accumulated completion tokens from the strong model.
func (s *RoutingStats) StrongCompletionTokens() int64 { return s.strongCompletionTokens.Load() }

// MediumPromptTokens returns accumulated prompt tokens routed to the medium model.
func (s *RoutingStats) MediumPromptTokens() int64 { return s.mediumPromptTokens.Load() }

// MediumCompletionTokens returns accumulated completion tokens from the medium model.
func (s *RoutingStats) MediumCompletionTokens() int64 { return s.mediumCompletionTokens.Load() }

// WeakPromptTokens returns accumulated prompt tokens routed to the weak model.
func (s *RoutingStats) WeakPromptTokens() int64 { return s.weakPromptTokens.Load() }

// WeakCompletionTokens returns accumulated completion tokens from the weak model.
func (s *RoutingStats) WeakCompletionTokens() int64 { return s.weakCompletionTokens.Load() }

// TotalPromptTokens returns the sum of prompt tokens across all tiers.
func (s *RoutingStats) TotalPromptTokens() int64 {
	return s.strongPromptTokens.Load() + s.mediumPromptTokens.Load() + s.weakPromptTokens.Load()
}

// TotalCompletionTokens returns the sum of completion tokens across all tiers.
func (s *RoutingStats) TotalCompletionTokens() int64 {
	return s.strongCompletionTokens.Load() + s.mediumCompletionTokens.Load() + s.weakCompletionTokens.Load()
}

// HasTokenData reports whether any token usage has been recorded.
func (s *RoutingStats) HasTokenData() bool {
	return s.TotalPromptTokens() > 0
}

// StrongPct returns the percentage of calls routed to the strong model (0-100).
func (s *RoutingStats) StrongPct() float64 {
	total := s.Total()
	if total == 0 {
		return 0
	}
	return float64(s.strongCalls.Load()) / float64(total) * 100
}

// MediumPct returns the percentage of calls routed to the medium model (0-100).
func (s *RoutingStats) MediumPct() float64 {
	total := s.Total()
	if total == 0 {
		return 0
	}
	return float64(s.mediumCalls.Load()) / float64(total) * 100
}

// WeakPct returns the percentage of calls routed to the weak model (0-100).
func (s *RoutingStats) WeakPct() float64 {
	total := s.Total()
	if total == 0 {
		return 0
	}
	return float64(s.weakCalls.Load()) / float64(total) * 100
}

// Reset zeroes all call and token counters.
func (s *RoutingStats) Reset() {
	s.strongCalls.Store(0)
	s.mediumCalls.Store(0)
	s.weakCalls.Store(0)
	s.strongPromptTokens.Store(0)
	s.strongCompletionTokens.Store(0)
	s.mediumPromptTokens.Store(0)
	s.mediumCompletionTokens.Store(0)
	s.weakPromptTokens.Store(0)
	s.weakCompletionTokens.Store(0)
}

// LastRouting records the most recent routing decision (mutex-protected).
type LastRouting struct {
	mu        sync.RWMutex
	modelName string
	tier      string
}

// Set records a routing decision.
func (lr *LastRouting) Set(name string, tier string) {
	lr.mu.Lock()
	lr.modelName = name
	lr.tier = tier
	lr.mu.Unlock()
}

// Get returns the most recent routing decision.
func (lr *LastRouting) Get() (name string, tier string) {
	lr.mu.RLock()
	name = lr.modelName
	tier = lr.tier
	lr.mu.RUnlock()
	return
}

// RoutingLLM implements model.LLM and routes each GenerateContent call
// to a strong, medium, or weak LLM based on prompt complexity.
type RoutingLLM struct {
	strong        model.LLM
	medium        model.LLM
	weak          model.LLM
	classifier    ComplexityClassifier
	highThreshold float64
	lowThreshold  float64
	Stats         RoutingStats
	Last          LastRouting
	StrongName    string // display name (e.g. "claude-sonnet")
	MediumName    string // display name (e.g. "claude-haiku"), empty if no medium
	WeakName      string // display name (e.g. "gpt-4o-mini")
	// Pricing fields — set post-construction via SetPricing (guarded by pricingMu).
	pricingMu  sync.RWMutex
	strongCost ModelCost
	mediumCost ModelCost
	weakCost   ModelCost
	hasPricing bool
	// turnTiers is an ordered record of every routing decision this turn,
	// in the exact order they were made. It is the single source of truth:
	// both the summary percentages and the per-bubble badges read from it.
	// Protected by the same goroutine that runs driveTurn (no concurrent
	// GenerateContent calls within one turn).
	turnTiersMu sync.Mutex
	turnTiers   []string // "strong", "medium", or "weak", one entry per call
}

// NewRoutingLLM creates a routing LLM wrapper.
// medium may be nil for a 2-tier (strong/weak) setup.
func NewRoutingLLM(strong, medium, weak model.LLM, classifier ComplexityClassifier, highThreshold, lowThreshold float64) *RoutingLLM {
	if highThreshold <= 0 || highThreshold >= 1 {
		highThreshold = 0.7
	}
	if lowThreshold <= 0 || lowThreshold >= 1 {
		lowThreshold = 0.3
	}
	if lowThreshold >= highThreshold {
		lowThreshold = highThreshold - 0.1
	}
	if lowThreshold < 0 {
		lowThreshold = 0
	}
	mediumName := ""
	if medium != nil {
		mediumName = medium.Name()
	}
	return &RoutingLLM{
		strong:        strong,
		medium:        medium,
		weak:          weak,
		classifier:    classifier,
		highThreshold: highThreshold,
		lowThreshold:  lowThreshold,
		StrongName:    strong.Name(),
		MediumName:    mediumName,
		WeakName:      weak.Name(),
	}
}

// Name implements model.LLM.
func (r *RoutingLLM) Name() string {
	if r.MediumName != "" {
		return fmt.Sprintf("auto(%s|%s|%s)", r.StrongName, r.MediumName, r.WeakName)
	}
	return fmt.Sprintf("auto(%s|%s)", r.StrongName, r.WeakName)
}

// GenerateContent implements model.LLM.
func (r *RoutingLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	prompt := extractLastUserMessage(req)
	classCtx := ClassifierContextFromContext(ctx)
	score := r.classifier.Classify(ctx, prompt, classCtx)

	var chosen model.LLM
	var tier string
	switch {
	case float64(score) >= r.highThreshold:
		chosen = r.strong
		tier = "strong"
		r.Stats.RecordStrong()
		r.Last.Set(r.StrongName, "strong")
	case r.medium != nil && float64(score) >= r.lowThreshold:
		chosen = r.medium
		tier = "medium"
		r.Stats.RecordMedium()
		r.Last.Set(r.MediumName, "medium")
	default:
		chosen = r.weak
		tier = "weak"
		r.Stats.RecordWeak()
		r.Last.Set(r.WeakName, "weak")
	}

	// Record this call's tier in the ordered per-turn list — unless this is
	// a stats-only call (e.g. title generation) which counts in the summary
	// but has no corresponding chat bubble and must not shift badge indices.
	if !isStatsOnlyRouting(ctx) {
		r.turnTiersMu.Lock()
		r.turnTiers = append(r.turnTiers, tier)
		r.turnTiersMu.Unlock()
	}

	slog.Debug("[routing] model selected",
		"score", fmt.Sprintf("%.2f", score),
		"high_threshold", fmt.Sprintf("%.2f", r.highThreshold),
		"low_threshold", fmt.Sprintf("%.2f", r.lowThreshold),
		"tier", tier,
		"model", chosen.Name(),
		"prompt_preview", truncateForLog(prompt, 100),
	)

	// Wrap the provider's iterator to intercept UsageMetadata so we can
	// record actual token counts per tier for accurate cost savings.
	// Many streaming providers emit cumulative UsageMetadata on every chunk,
	// so we track only the last chunk's values and record once after the
	// stream completes to avoid double-counting.
	// NOTE: This assumes cumulative (not incremental) usage reporting.
	// Providers using incremental per-chunk reporting would be under-counted.
	inner := chosen.GenerateContent(ctx, req, stream)
	return func(yield func(*model.LLMResponse, error) bool) {
		var lastPrompt, lastCompletion int32
		for resp, err := range inner {
			if resp != nil && resp.UsageMetadata != nil {
				lastPrompt = resp.UsageMetadata.PromptTokenCount
				lastCompletion = resp.UsageMetadata.CandidatesTokenCount
			}
			if !yield(resp, err) {
				break
			}
		}
		if lastPrompt > 0 || lastCompletion > 0 {
			r.Stats.RecordTokens(tier, lastPrompt, lastCompletion)
		}
	}
}

// StrongModel returns the strong model (for inspection).
func (r *RoutingLLM) StrongModel() model.LLM { return r.strong }

// MediumModel returns the medium model (for inspection), may be nil.
func (r *RoutingLLM) MediumModel() model.LLM { return r.medium }

// WeakModel returns the weak model (for inspection).
func (r *RoutingLLM) WeakModel() model.LLM { return r.weak }

// TurnTiers returns a copy of the ordered routing decisions recorded this turn.
// Index 0 is the first call, index 1 the second, etc. Both the summary
// percentages (via Stats) and the per-bubble badges read from this same list,
// ensuring they always agree.
func (r *RoutingLLM) TurnTiers() []string {
	r.turnTiersMu.Lock()
	defer r.turnTiersMu.Unlock()
	out := make([]string, len(r.turnTiers))
	copy(out, r.turnTiers)
	return out
}

// ResetTurn clears the per-turn tier list and Stats counters, ready for the
// next turn. Called at the start of each new user turn.
func (r *RoutingLLM) ResetTurn() {
	r.turnTiersMu.Lock()
	r.turnTiers = r.turnTiers[:0]
	r.turnTiersMu.Unlock()
	r.Stats.Reset()
}

// ModelNameForTier returns the configured display name for a routing tier.
// Centralises the tier→name mapping used by both the live loop and reload so
// there is only one place to change if names change.
func (r *RoutingLLM) ModelNameForTier(tier string) string {
	switch tier {
	case "strong":
		return r.StrongName
	case "medium":
		return r.MediumName
	case "weak":
		return r.WeakName
	}
	return ""
}

// --- Pricing support ---

// SetPricing injects per-model cost data (USD per token) for cost-savings
// computation. Safe to call from a goroutine after construction.
func (r *RoutingLLM) SetPricing(strong, medium, weak ModelCost) {
	r.pricingMu.Lock()
	r.strongCost = strong
	r.mediumCost = medium
	r.weakCost = weak
	r.hasPricing = strong.PromptCost > 0 || strong.CompletionCost > 0
	r.pricingMu.Unlock()
}

// HasPricing reports whether pricing data has been injected.
func (r *RoutingLLM) HasPricing() bool {
	r.pricingMu.RLock()
	v := r.hasPricing
	r.pricingMu.RUnlock()
	return v
}

// CostSavingsPct returns the estimated percentage saved vs routing all calls
// to the strong model. Returns 0 when pricing data is unavailable.
// Uses actual token counts when available; falls back to call-count ratio.
func (r *RoutingLLM) CostSavingsPct() float64 {
	r.pricingMu.RLock()
	if !r.hasPricing {
		r.pricingMu.RUnlock()
		return 0
	}
	strong, medium, weak := r.strongCost, r.mediumCost, r.weakCost
	r.pricingMu.RUnlock()
	return CostSavingsPct(
		strong, medium, weak,
		r.Stats.StrongCount(), r.Stats.MediumCount(), r.Stats.WeakCount(),
		r.Stats.StrongPromptTokens(), r.Stats.StrongCompletionTokens(),
		r.Stats.MediumPromptTokens(), r.Stats.MediumCompletionTokens(),
		r.Stats.WeakPromptTokens(), r.Stats.WeakCompletionTokens(),
	)
}

// SummaryLine is the end-of-turn Auto routing system message. It includes an
// estimated cost-savings clause when pricing is available and savings exceed
// 0.5% versus routing every call to the strong model.
func (r *RoutingLLM) SummaryLine() string {
	total := r.Stats.Total()
	if total < 1 {
		return ""
	}
	parts := make([]string, 0, 3)
	if r.Stats.StrongCount() > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% strong %s", r.Stats.StrongPct(), r.StrongName))
	}
	if r.Stats.MediumCount() > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% medium %s", r.Stats.MediumPct(), r.MediumName))
	}
	if r.Stats.WeakCount() > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% weak %s", r.Stats.WeakPct(), r.WeakName))
	}
	summary := fmt.Sprintf("Auto routing \u2014 %d calls (%s)", total, strings.Join(parts, ", "))
	if r.HasPricing() {
		if savings := r.CostSavingsPct(); savings > 0.5 {
			summary += fmt.Sprintf(" \u00b7 Saved ~%.0f%% vs all-strong", savings)
		}
	}
	return summary
}

// Verify RoutingLLM implements model.LLM at compile time.
var _ model.LLM = (*RoutingLLM)(nil)

// --- Context key helpers ---

type routingContextKey struct{}

// statsOnlyContextKey marks a routing call as stats-only: it is counted in
// Stats (for the summary) but NOT appended to turnTiers (no badge).
// Used for background utility calls (e.g. title generation) that should
// appear in the turn summary count but have no corresponding chat bubble.
type statsOnlyContextKey struct{}

// WithStatsOnlyRouting marks ctx so routing decisions are counted in Stats
// but not appended to the ordered turnTiers badge list.
func WithStatsOnlyRouting(ctx context.Context) context.Context {
	return context.WithValue(ctx, statsOnlyContextKey{}, true)
}

// isStatsOnlyRouting returns true when the call should be stats-only.
func isStatsOnlyRouting(ctx context.Context) bool {
	v, _ := ctx.Value(statsOnlyContextKey{}).(bool)
	return v
}

// WithClassifierContext attaches classifier context to a Go context.
func WithClassifierContext(ctx context.Context, cc ClassifierContext) context.Context {
	return context.WithValue(ctx, routingContextKey{}, cc)
}

// ClassifierContextFromContext retrieves classifier context.
func ClassifierContextFromContext(ctx context.Context) ClassifierContext {
	cc, _ := ctx.Value(routingContextKey{}).(ClassifierContext)
	return cc
}

// --- Helpers ---

const perTurnContextPrefix = "[Astonish Per-Turn Context"

// extractLastUserMessage walks req.Contents in reverse, finds the last
// user-authored content (skipping framework-injected per-turn context), and
// returns only the shortest text part — which is the actual user-typed input.
// Longer parts are typically framework-injected context (AGENTS.md, session
// state, timestamps) that would artificially inflate the complexity score.
func extractLastUserMessage(req *model.LLMRequest) string {
	if req == nil {
		return ""
	}
	for i := len(req.Contents) - 1; i >= 0; i-- {
		c := req.Contents[i]
		if c == nil || c.Role != "user" {
			continue
		}
		// Skip the framework-injected per-turn context block entirely.
		// It is always a separate Content with role=user, injected after the
		// actual human message, and its text starts with the well-known prefix.
		if isPerTurnContext(c) {
			continue
		}
		// In code mode, user content often has multiple text parts: the actual
		// user input (short) and injected context (long). Pick the shortest
		// non-empty text part as the best proxy for what the user actually typed.
		var shortest string
		for _, p := range c.Parts {
			if p != nil && p.Text != "" {
				if shortest == "" || len(p.Text) < len(shortest) {
					shortest = p.Text
				}
			}
		}
		if shortest != "" {
			return shortest
		}
	}
	return ""
}

// isPerTurnContext returns true if the Content is a framework-injected
// per-turn context block (skills, tools, session metadata) rather than
// actual user input. These always start with "[Astonish Per-Turn Context".
func isPerTurnContext(c *genai.Content) bool {
	for _, p := range c.Parts {
		if p != nil && strings.HasPrefix(p.Text, perTurnContextPrefix) {
			return true
		}
	}
	return false
}

// truncateForLog truncates a string for debug logging.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
