package routing

import (
	"context"
	"iter"
	"math"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// mockLLM records whether GenerateContent was called.
type mockLLM struct {
	name   string
	called atomic.Bool
}

func (m *mockLLM) Name() string { return m.name }

func (m *mockLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.called.Store(true)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "ok"}}},
		}, nil)
	}
}

// fixedClassifier always returns the same score.
type fixedClassifier struct {
	score ComplexityScore
}

func (f *fixedClassifier) Classify(context.Context, string, ClassifierContext) ComplexityScore {
	return f.score
}

func drainLLM(r *RoutingLLM, ctx context.Context) {
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "test"}}},
		},
	}
	for resp, err := range r.GenerateContent(ctx, req, false) {
		_ = resp
		_ = err
	}
}

func TestRoutingLLM_RoutesToStrong(t *testing.T) {
	strong := &mockLLM{name: "strong-model"}
	weak := &mockLLM{name: "weak-model"}
	r := NewRoutingLLM(strong, nil, weak, &fixedClassifier{score: 0.8}, 0.7, 0.3)

	drainLLM(r, context.Background())

	if !strong.called.Load() {
		t.Error("expected strong model to be called")
	}
	if weak.called.Load() {
		t.Error("expected weak model NOT to be called")
	}
}

func TestRoutingLLM_RoutesToWeak(t *testing.T) {
	strong := &mockLLM{name: "strong-model"}
	weak := &mockLLM{name: "weak-model"}
	r := NewRoutingLLM(strong, nil, weak, &fixedClassifier{score: 0.2}, 0.7, 0.3)

	drainLLM(r, context.Background())

	if strong.called.Load() {
		t.Error("expected strong model NOT to be called")
	}
	if !weak.called.Load() {
		t.Error("expected weak model to be called")
	}
}

func TestRoutingLLM_RoutesToMedium(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	medium := &mockLLM{name: "medium"}
	weak := &mockLLM{name: "weak"}
	// score=0.5: below high=0.7 but >= low=0.3 → medium
	r := NewRoutingLLM(strong, medium, weak, &fixedClassifier{score: 0.5}, 0.7, 0.3)

	drainLLM(r, context.Background())

	if strong.called.Load() {
		t.Error("expected strong model NOT to be called")
	}
	if !medium.called.Load() {
		t.Error("expected medium model to be called")
	}
	if weak.called.Load() {
		t.Error("expected weak model NOT to be called")
	}
}

func TestRoutingLLM_NilMediumFallsBackToWeak(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	weak := &mockLLM{name: "weak"}
	// medium=nil, score=0.5: not >= high=0.7, medium is nil → falls to weak
	r := NewRoutingLLM(strong, nil, weak, &fixedClassifier{score: 0.5}, 0.7, 0.3)

	drainLLM(r, context.Background())

	if strong.called.Load() {
		t.Error("expected strong model NOT to be called")
	}
	if !weak.called.Load() {
		t.Error("expected weak model to be called")
	}
}

func TestRoutingLLM_StatsTracking(t *testing.T) {
	strong := &mockLLM{name: "strong-model"}
	weak := &mockLLM{name: "weak-model"}

	// Build one RoutingLLM with a switchable classifier.
	sc := &switchableClassifier{score: 0.8}
	r := NewRoutingLLM(strong, nil, weak, sc, 0.7, 0.3)

	for i := 0; i < 3; i++ {
		drainLLM(r, context.Background())
	}
	sc.score = 0.2
	for i := 0; i < 2; i++ {
		drainLLM(r, context.Background())
	}

	if r.Stats.Total() != 5 {
		t.Errorf("Total = %d, want 5", r.Stats.Total())
	}
	if r.Stats.StrongCount() != 3 {
		t.Errorf("StrongCount = %d, want 3", r.Stats.StrongCount())
	}
	if r.Stats.WeakCount() != 2 {
		t.Errorf("WeakCount = %d, want 2", r.Stats.WeakCount())
	}
	if pct := r.Stats.StrongPct(); pct != 60 {
		t.Errorf("StrongPct = %f, want 60", pct)
	}
	if pct := r.Stats.WeakPct(); pct != 40 {
		t.Errorf("WeakPct = %f, want 40", pct)
	}
}

func TestRoutingLLM_Stats_3Tier(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	medium := &mockLLM{name: "medium"}
	weak := &mockLLM{name: "weak"}

	sc := &switchableClassifier{score: 0.8}
	r := NewRoutingLLM(strong, medium, weak, sc, 0.7, 0.3)

	// 1 strong call (score >= 0.7)
	sc.score = 0.8
	drainLLM(r, context.Background())

	// 1 medium call (0.3 <= score < 0.7)
	sc.score = 0.5
	drainLLM(r, context.Background())

	// 1 weak call (score < 0.3)
	sc.score = 0.1
	drainLLM(r, context.Background())

	if r.Stats.Total() != 3 {
		t.Errorf("Total = %d, want 3", r.Stats.Total())
	}
	if r.Stats.StrongCount() != 1 {
		t.Errorf("StrongCount = %d, want 1", r.Stats.StrongCount())
	}
	if r.Stats.MediumCount() != 1 {
		t.Errorf("MediumCount = %d, want 1", r.Stats.MediumCount())
	}
	if r.Stats.WeakCount() != 1 {
		t.Errorf("WeakCount = %d, want 1", r.Stats.WeakCount())
	}
	if pct := r.Stats.MediumPct(); math.Abs(pct-33.333333333333336) > 0.001 {
		t.Errorf("MediumPct = %f, want ~33.3", pct)
	}
}

type switchableClassifier struct {
	score ComplexityScore
}

func (s *switchableClassifier) Classify(context.Context, string, ClassifierContext) ComplexityScore {
	return s.score
}

func TestRoutingLLM_LastRouting(t *testing.T) {
	strong := &mockLLM{name: "strong-model"}
	weak := &mockLLM{name: "weak-model"}
	r := NewRoutingLLM(strong, nil, weak, &fixedClassifier{score: 0.2}, 0.7, 0.3)

	drainLLM(r, context.Background())

	name, tier := r.Last.Get()
	if name != "weak-model" {
		t.Errorf("Last.Name = %q, want %q", name, "weak-model")
	}
	if tier != "weak" {
		t.Errorf("Last.tier = %q, want %q", tier, "weak")
	}
}

func TestRoutingLLM_Name_2Models(t *testing.T) {
	strong := &mockLLM{name: "claude-sonnet"}
	weak := &mockLLM{name: "gpt-4o-mini"}

	r := NewRoutingLLM(strong, nil, weak, &fixedClassifier{score: 0.5}, 0.7, 0.3)
	want := "auto(claude-sonnet|gpt-4o-mini)"
	if got := r.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestRoutingLLM_Name_3Models(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	medium := &mockLLM{name: "medium"}
	weak := &mockLLM{name: "weak"}

	r := NewRoutingLLM(strong, medium, weak, &fixedClassifier{score: 0.5}, 0.7, 0.3)
	want := "auto(strong|medium|weak)"
	if got := r.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestRoutingLLM_DefaultThreshold(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	weak := &mockLLM{name: "weak"}

	// highThreshold=0 should default to 0.7; score=0.4 < 0.7 → weak.
	r := NewRoutingLLM(strong, nil, weak, &fixedClassifier{score: 0.4}, 0, 0.3)
	drainLLM(r, context.Background())

	if strong.called.Load() {
		t.Error("expected strong NOT called with default threshold")
	}
	if !weak.called.Load() {
		t.Error("expected weak called with default threshold")
	}
}

func TestRoutingLLM_ContextKey(t *testing.T) {
	cc := ClassifierContext{ToolNames: []string{"announce_plan"}, ConversationTurns: 5, HasPlanMode: true}
	ctx := WithClassifierContext(context.Background(), cc)
	got := ClassifierContextFromContext(ctx)

	if len(got.ToolNames) != 1 || got.ToolNames[0] != "announce_plan" {
		t.Errorf("ToolNames = %v, want [announce_plan]", got.ToolNames)
	}
	if got.ConversationTurns != 5 {
		t.Errorf("ConversationTurns = %d, want 5", got.ConversationTurns)
	}
	if !got.HasPlanMode {
		t.Error("HasPlanMode = false, want true")
	}
}

func TestExtractLastUserMessage(t *testing.T) {
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "first"}}},
			{Role: "model", Parts: []*genai.Part{{Text: "response"}}},
			{Role: "user", Parts: []*genai.Part{{Text: "second"}}},
		},
	}
	if got := extractLastUserMessage(req); got != "second" {
		t.Errorf("extractLastUserMessage = %q, want %q", got, "second")
	}

	// nil request
	if got := extractLastUserMessage(nil); got != "" {
		t.Errorf("nil request = %q, want empty", got)
	}

	// no user content
	reqNoUser := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "model", Parts: []*genai.Part{{Text: "response"}}},
		},
	}
	if got := extractLastUserMessage(reqNoUser); got != "" {
		t.Errorf("no user = %q, want empty", got)
	}

	// Skips per-turn context and returns the real user message before it.
	reqWithCtx := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "hello world"}}},
			{Role: "user", Parts: []*genai.Part{{Text: "[Astonish Per-Turn Context — not user-authored]\n\n## Available Skills\n..."}}},
		},
	}
	if got := extractLastUserMessage(reqWithCtx); got != "hello world" {
		t.Errorf("with per-turn context = %q, want %q", got, "hello world")
	}

	// Multi-part user content: returns the shortest part (the actual user input).
	reqMultiPart := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{
				{Text: "This is a very long AGENTS.md file that the framework injected as context for the agent to follow..."},
				{Text: "fix the bug"},
			}},
		},
	}
	if got := extractLastUserMessage(reqMultiPart); got != "fix the bug" {
		t.Errorf("multi-part = %q, want %q", got, "fix the bug")
	}
}

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("short", 100); got != "short" {
		t.Errorf("short = %q", got)
	}
	long := "abcdefghijklmnopqrstuvwxyz"
	got := truncateForLog(long, 5)
	if got != "abcde…" {
		t.Errorf("truncated = %q, want %q", got, "abcde…")
	}
}
