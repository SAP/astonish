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

// RoutingStats tracks cumulative routing decisions (thread-safe via atomics).
type RoutingStats struct {
	strongCalls atomic.Int64
	weakCalls   atomic.Int64
}

// RecordStrong increments the strong model call counter.
func (s *RoutingStats) RecordStrong() { s.strongCalls.Add(1) }

// RecordWeak increments the weak model call counter.
func (s *RoutingStats) RecordWeak() { s.weakCalls.Add(1) }

// Total returns the total number of routing decisions.
func (s *RoutingStats) Total() int64 { return s.strongCalls.Load() + s.weakCalls.Load() }

// StrongCount returns the number of strong model calls.
func (s *RoutingStats) StrongCount() int64 { return s.strongCalls.Load() }

// WeakCount returns the number of weak model calls.
func (s *RoutingStats) WeakCount() int64 { return s.weakCalls.Load() }

// StrongPct returns the percentage of calls routed to the strong model (0-100).
func (s *RoutingStats) StrongPct() float64 {
	total := s.Total()
	if total == 0 {
		return 0
	}
	return float64(s.strongCalls.Load()) / float64(total) * 100
}

// WeakPct returns the percentage of calls routed to the weak model (0-100).
func (s *RoutingStats) WeakPct() float64 {
	total := s.Total()
	if total == 0 {
		return 0
	}
	return float64(s.weakCalls.Load()) / float64(total) * 100
}

// Reset zeroes both counters.
func (s *RoutingStats) Reset() {
	s.strongCalls.Store(0)
	s.weakCalls.Store(0)
}

// LastRouting records the most recent routing decision (mutex-protected).
type LastRouting struct {
	mu        sync.RWMutex
	modelName string
	isStrong  bool
}

// Set records a routing decision.
func (lr *LastRouting) Set(name string, strong bool) {
	lr.mu.Lock()
	lr.modelName = name
	lr.isStrong = strong
	lr.mu.Unlock()
}

// Get returns the most recent routing decision.
func (lr *LastRouting) Get() (name string, isStrong bool) {
	lr.mu.RLock()
	name = lr.modelName
	isStrong = lr.isStrong
	lr.mu.RUnlock()
	return
}

// RoutingLLM implements model.LLM and routes each GenerateContent call
// to either a strong or weak LLM based on prompt complexity.
type RoutingLLM struct {
	strong     model.LLM
	weak       model.LLM
	classifier ComplexityClassifier
	threshold  float64
	Stats      RoutingStats
	Last       LastRouting
	StrongName string // display name (e.g. "claude-sonnet")
	WeakName   string // display name (e.g. "gpt-4o-mini")
	Tier       string // "orchestrator" or "task" (empty for legacy 2-tier)
}

// NewRoutingLLM creates a routing LLM wrapper.
func NewRoutingLLM(strong, weak model.LLM, classifier ComplexityClassifier, threshold float64) *RoutingLLM {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.5
	}
	return &RoutingLLM{
		strong:     strong,
		weak:       weak,
		classifier: classifier,
		threshold:  threshold,
		StrongName: strong.Name(),
		WeakName:   weak.Name(),
	}
}

// Name implements model.LLM.
func (r *RoutingLLM) Name() string {
	if r.Tier != "" {
		return fmt.Sprintf("auto:%s(%s|%s)", r.Tier, r.StrongName, r.WeakName)
	}
	return fmt.Sprintf("auto(%s|%s)", r.StrongName, r.WeakName)
}

// GenerateContent implements model.LLM.
func (r *RoutingLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	prompt := extractLastUserMessage(req)
	classCtx := ClassifierContextFromContext(ctx)

	score := r.classifier.Classify(ctx, prompt, classCtx)

	var chosen model.LLM
	var label string
	isStrong := float64(score) >= r.threshold
	if isStrong {
		chosen = r.strong
		label = "strong"
		r.Stats.RecordStrong()
		r.Last.Set(r.StrongName, true)
	} else {
		chosen = r.weak
		label = "weak"
		r.Stats.RecordWeak()
		r.Last.Set(r.WeakName, false)
	}

	slog.Debug("[routing] model selected",
		"score", fmt.Sprintf("%.2f", score),
		"threshold", fmt.Sprintf("%.2f", r.threshold),
		"chosen", label,
		"model", chosen.Name(),
		"prompt_preview", truncateForLog(prompt, 100),
	)

	return chosen.GenerateContent(ctx, req, stream)
}

// StrongModel returns the strong model (for inspection).
func (r *RoutingLLM) StrongModel() model.LLM { return r.strong }

// WeakModel returns the weak model (for inspection).
func (r *RoutingLLM) WeakModel() model.LLM { return r.weak }

// Verify RoutingLLM implements model.LLM at compile time.
var _ model.LLM = (*RoutingLLM)(nil)

// --- Context key helpers ---

type routingContextKey struct{}

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
