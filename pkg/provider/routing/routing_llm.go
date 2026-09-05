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
	mediumCalls atomic.Int64
	weakCalls   atomic.Int64
}

// RecordStrong increments the strong model call counter.
func (s *RoutingStats) RecordStrong() { s.strongCalls.Add(1) }

// RecordMedium increments the medium model call counter.
func (s *RoutingStats) RecordMedium() { s.mediumCalls.Add(1) }

// RecordWeak increments the weak model call counter.
func (s *RoutingStats) RecordWeak() { s.weakCalls.Add(1) }

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

// Reset zeroes all counters.
func (s *RoutingStats) Reset() {
	s.strongCalls.Store(0)
	s.mediumCalls.Store(0)
	s.weakCalls.Store(0)
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
	var label, tier string
	switch {
	case float64(score) >= r.highThreshold:
		chosen = r.strong
		label = "strong"
		tier = "strong"
		r.Stats.RecordStrong()
		r.Last.Set(r.StrongName, "strong")
	case r.medium != nil && float64(score) >= r.lowThreshold:
		chosen = r.medium
		label = "medium"
		tier = "medium"
		r.Stats.RecordMedium()
		r.Last.Set(r.MediumName, "medium")
	default:
		chosen = r.weak
		label = "weak"
		tier = "weak"
		r.Stats.RecordWeak()
		r.Last.Set(r.WeakName, "weak")
	}

	slog.Debug("[routing] model selected",
		"score", fmt.Sprintf("%.2f", score),
		"high_threshold", fmt.Sprintf("%.2f", r.highThreshold),
		"low_threshold", fmt.Sprintf("%.2f", r.lowThreshold),
		"tier", tier,
		"chosen", label,
		"model", chosen.Name(),
		"prompt_preview", truncateForLog(prompt, 100),
	)

	return chosen.GenerateContent(ctx, req, stream)
}

// StrongModel returns the strong model (for inspection).
func (r *RoutingLLM) StrongModel() model.LLM { return r.strong }

// MediumModel returns the medium model (for inspection), may be nil.
func (r *RoutingLLM) MediumModel() model.LLM { return r.medium }

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
