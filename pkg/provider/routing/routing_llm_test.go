package routing

import (
	"context"
	"iter"
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

func (f *fixedClassifier) Classify(string, ClassifierContext) ComplexityScore {
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
	r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.8}, 0.5)

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
	r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.2}, 0.5)

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
	r := NewRoutingLLM(strong, weak, sc, 0.5)

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

type switchableClassifier struct {
	score ComplexityScore
}

func (s *switchableClassifier) Classify(string, ClassifierContext) ComplexityScore {
	return s.score
}

func TestRoutingLLM_LastRouting(t *testing.T) {
	strong := &mockLLM{name: "strong-model"}
	weak := &mockLLM{name: "weak-model"}
	r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.2}, 0.5)

	drainLLM(r, context.Background())

	name, isStrong := r.Last.Get()
	if name != "weak-model" {
		t.Errorf("Last.Name = %q, want %q", name, "weak-model")
	}
	if isStrong {
		t.Error("Last.IsStrong = true, want false")
	}
}

func TestRoutingLLM_Name(t *testing.T) {
	strong := &mockLLM{name: "claude-sonnet"}
	weak := &mockLLM{name: "gpt-4o-mini"}

	t.Run("no tier", func(t *testing.T) {
		r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.5}, 0.5)
		want := "auto(claude-sonnet|gpt-4o-mini)"
		if got := r.Name(); got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
	})

	t.Run("tier orchestrator", func(t *testing.T) {
		r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.5}, 0.5)
		r.Tier = "orchestrator"
		want := "auto:orchestrator(claude-sonnet|gpt-4o-mini)"
		if got := r.Name(); got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
	})

	t.Run("tier task", func(t *testing.T) {
		r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.5}, 0.5)
		r.Tier = "task"
		want := "auto:task(claude-sonnet|gpt-4o-mini)"
		if got := r.Name(); got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
	})
}

func TestRoutingLLM_DefaultThreshold(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	weak := &mockLLM{name: "weak"}

	// threshold=0 should default to 0.5.
	r := NewRoutingLLM(strong, weak, &fixedClassifier{score: 0.4}, 0)
	drainLLM(r, context.Background())

	// 0.4 < 0.5 threshold → weak should be called.
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

	// multi-part user content: returns the shortest text part (actual user input)
	reqMultiPart := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{
				{Text: "This is a very long injected context with AGENTS.md and session state and all sorts of framework metadata"},
				{Text: "hello"},
			}},
		},
	}
	if got := extractLastUserMessage(reqMultiPart); got != "hello" {
		t.Errorf("multi-part = %q, want %q", got, "hello")
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
