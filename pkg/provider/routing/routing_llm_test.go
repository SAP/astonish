package routing

import (
	"context"
	"iter"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// mockLLM records whether GenerateContent was called and optionally returns UsageMetadata.
type mockLLM struct {
	name   string
	called atomic.Bool
	usage  *genai.GenerateContentResponseUsageMetadata // optional; returned in every response
}

func (m *mockLLM) Name() string { return m.name }

func (m *mockLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.called.Store(true)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content:       &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "ok"}}},
			UsageMetadata: m.usage,
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

// TestRoutingLLM_TurnTiers_OrderPreserved verifies that GenerateContent appends
// to turnTiers in call order and TurnTiers returns them in the same order.
func TestRoutingLLM_TurnTiers_OrderPreserved(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	medium := &mockLLM{name: "medium"}
	weak := &mockLLM{name: "weak"}
	sc := &switchableClassifier{}
	r := NewRoutingLLM(strong, medium, weak, sc, 0.7, 0.3)

	// strong, weak, weak, medium
	for _, score := range []ComplexityScore{0.8, 0.1, 0.2, 0.5} {
		sc.score = score
		drainLLM(r, context.Background())
	}

	got := r.TurnTiers()
	want := []string{"strong", "weak", "weak", "medium"}
	if len(got) != len(want) {
		t.Fatalf("TurnTiers len = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TurnTiers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRoutingLLM_ResetTurn_ClearsTiersAndStats verifies that ResetTurn
// clears both the ordered tier list and the Stats counters.
func TestRoutingLLM_ResetTurn_ClearsTiersAndStats(t *testing.T) {
	strong := &mockLLM{name: "strong"}
	weak := &mockLLM{name: "weak"}
	sc := &switchableClassifier{score: 0.8}
	r := NewRoutingLLM(strong, nil, weak, sc, 0.7, 0.3)

	drainLLM(r, context.Background())
	drainLLM(r, context.Background())
	if r.Stats.Total() != 2 {
		t.Fatalf("before reset: Stats.Total = %d, want 2", r.Stats.Total())
	}
	if len(r.TurnTiers()) != 2 {
		t.Fatalf("before reset: TurnTiers len = %d, want 2", len(r.TurnTiers()))
	}

	r.ResetTurn()

	if r.Stats.Total() != 0 {
		t.Errorf("after ResetTurn: Stats.Total = %d, want 0", r.Stats.Total())
	}
	if len(r.TurnTiers()) != 0 {
		t.Errorf("after ResetTurn: TurnTiers len = %d, want 0", len(r.TurnTiers()))
	}
}

// TestRoutingLLM_modelNameForTier verifies that each tier maps to the correct
// configured display name.
func TestRoutingLLM_modelNameForTier(t *testing.T) {
	strong := &mockLLM{name: "claude-sonnet"}
	medium := &mockLLM{name: "claude-haiku"}
	weak := &mockLLM{name: "gpt-4o-mini"}
	r := NewRoutingLLM(strong, medium, weak, &fixedClassifier{score: 0.5}, 0.7, 0.3)

	tests := []struct {
		tier string
		want string
	}{
		{"strong", "claude-sonnet"},
		{"medium", "claude-haiku"},
		{"weak", "gpt-4o-mini"},
		{"unknown", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := r.ModelNameForTier(tc.tier)
		if got != tc.want {
			t.Errorf("modelNameForTier(%q) = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestRoutingLLM_SummaryLine_IncludesCostSavings(t *testing.T) {
	r := NewRoutingLLM(&mockLLM{name: "opus"}, nil, &mockLLM{name: "haiku"}, &fixedClassifier{score: 0.1}, 0.7, 0.3)
	r.StrongName = "opus"
	r.WeakName = "haiku"
	r.SetPricing(ModelCost{PromptCost: 0.01}, ModelCost{}, ModelCost{PromptCost: 0.001})
	r.Stats.RecordWeak()
	r.Stats.RecordWeak()

	line := r.SummaryLine()
	if line == "" {
		t.Fatal("expected a summary line")
	}
	if !strings.Contains(line, "Saved") {
		t.Errorf("summary %q should include cost savings", line)
	}
	if !strings.Contains(line, "2 calls") {
		t.Errorf("summary %q should include call count", line)
	}
}

func TestRoutingLLM_SummaryLine_NoPricingOmitsSavings(t *testing.T) {
	r := NewRoutingLLM(&mockLLM{name: "opus"}, nil, &mockLLM{name: "haiku"}, &fixedClassifier{score: 0.1}, 0.7, 0.3)
	r.WeakName = "haiku"
	r.Stats.RecordWeak()

	line := r.SummaryLine()
	if strings.Contains(line, "Saved") {
		t.Errorf("summary %q should not include savings without pricing", line)
	}
}

func TestRoutingLLM_SummaryLine_AllStrongOmitsZeroSavings(t *testing.T) {
	r := NewRoutingLLM(&mockLLM{name: "opus"}, nil, &mockLLM{name: "haiku"}, &fixedClassifier{score: 0.9}, 0.7, 0.3)
	r.StrongName = "opus"
	r.SetPricing(ModelCost{PromptCost: 0.01}, ModelCost{}, ModelCost{PromptCost: 0.001})
	r.Stats.RecordStrong()
	r.Stats.RecordStrong()

	line := r.SummaryLine()
	if !strings.Contains(line, "2 calls") {
		t.Errorf("summary %q should include call count", line)
	}
	if strings.Contains(line, "Saved") {
		t.Errorf("all-strong summary %q should omit zero savings", line)
	}
}

// TestRoutingStats_RecordTokens verifies per-tier token accumulation,
// accessors, HasTokenData, and Reset.
func TestRoutingStats_RecordTokens(t *testing.T) {
	var s RoutingStats

	if s.HasTokenData() {
		t.Fatal("fresh stats should report HasTokenData=false")
	}

	s.RecordTokens("strong", 1000, 200)
	s.RecordTokens("weak", 5000, 1000)
	s.RecordTokens("medium", 300, 60)

	if !s.HasTokenData() {
		t.Fatal("after recording tokens, HasTokenData should be true")
	}
	if s.StrongPromptTokens() != 1000 {
		t.Errorf("StrongPromptTokens = %d; want 1000", s.StrongPromptTokens())
	}
	if s.StrongCompletionTokens() != 200 {
		t.Errorf("StrongCompletionTokens = %d; want 200", s.StrongCompletionTokens())
	}
	if s.MediumPromptTokens() != 300 {
		t.Errorf("MediumPromptTokens = %d; want 300", s.MediumPromptTokens())
	}
	if s.MediumCompletionTokens() != 60 {
		t.Errorf("MediumCompletionTokens = %d; want 60", s.MediumCompletionTokens())
	}
	if s.WeakPromptTokens() != 5000 {
		t.Errorf("WeakPromptTokens = %d; want 5000", s.WeakPromptTokens())
	}
	if s.WeakCompletionTokens() != 1000 {
		t.Errorf("WeakCompletionTokens = %d; want 1000", s.WeakCompletionTokens())
	}
	if s.TotalPromptTokens() != 6300 {
		t.Errorf("TotalPromptTokens = %d; want 6300", s.TotalPromptTokens())
	}
	if s.TotalCompletionTokens() != 1260 {
		t.Errorf("TotalCompletionTokens = %d; want 1260", s.TotalCompletionTokens())
	}

	// Accumulation: record again for the same tier.
	s.RecordTokens("strong", 500, 100)
	if s.StrongPromptTokens() != 1500 {
		t.Errorf("StrongPromptTokens after accumulation = %d; want 1500", s.StrongPromptTokens())
	}

	// Unknown tier should be a no-op.
	s.RecordTokens("unknown", 9999, 9999)
	if s.TotalPromptTokens() != 6800 {
		t.Errorf("TotalPromptTokens after unknown tier = %d; want 6800", s.TotalPromptTokens())
	}

	// Reset should zero everything.
	s.Reset()
	if s.HasTokenData() {
		t.Error("after Reset, HasTokenData should be false")
	}
	if s.TotalPromptTokens() != 0 || s.TotalCompletionTokens() != 0 {
		t.Error("after Reset, all token counters should be zero")
	}
}

// TestRoutingLLM_AccumulatesTokens verifies that GenerateContent wraps the
// provider iterator and accumulates UsageMetadata into the correct tier.
func TestRoutingLLM_AccumulatesTokens(t *testing.T) {
	strongMock := &mockLLM{
		name:  "opus",
		usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 2000, CandidatesTokenCount: 400},
	}
	weakMock := &mockLLM{
		name:  "haiku",
		usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 500, CandidatesTokenCount: 100},
	}

	ctx := context.Background()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "test"}}},
		},
	}

	// Force strong (score >= highThreshold).
	strong := NewRoutingLLM(strongMock, nil, weakMock, &fixedClassifier{score: 0.9}, 0.7, 0.3)
	for resp, err := range strong.GenerateContent(ctx, req, false) {
		_ = resp
		_ = err
	}
	if strong.Stats.StrongPromptTokens() != 2000 {
		t.Errorf("StrongPromptTokens = %d; want 2000", strong.Stats.StrongPromptTokens())
	}
	if strong.Stats.StrongCompletionTokens() != 400 {
		t.Errorf("StrongCompletionTokens = %d; want 400", strong.Stats.StrongCompletionTokens())
	}
	if strong.Stats.WeakPromptTokens() != 0 {
		t.Errorf("WeakPromptTokens should be 0 when no weak calls made")
	}

	// Force weak (score < lowThreshold).
	weak := NewRoutingLLM(strongMock, nil, weakMock, &fixedClassifier{score: 0.1}, 0.7, 0.3)
	for resp, err := range weak.GenerateContent(ctx, req, false) {
		_ = resp
		_ = err
	}
	if weak.Stats.WeakPromptTokens() != 500 {
		t.Errorf("WeakPromptTokens = %d; want 500", weak.Stats.WeakPromptTokens())
	}
	if weak.Stats.WeakCompletionTokens() != 100 {
		t.Errorf("WeakCompletionTokens = %d; want 100", weak.Stats.WeakCompletionTokens())
	}

	// Accumulate a second weak call.
	for resp, err := range weak.GenerateContent(ctx, req, false) {
		_ = resp
		_ = err
	}
	if weak.Stats.WeakPromptTokens() != 1000 {
		t.Errorf("WeakPromptTokens after 2 calls = %d; want 1000", weak.Stats.WeakPromptTokens())
	}
}

// TestRoutingLLM_SummaryLine_TokenWeightedSavings verifies that when providers
// return UsageMetadata, the savings percentage reflects actual token cost
// rather than the naive call-count ratio.
func TestRoutingLLM_SummaryLine_TokenWeightedSavings(t *testing.T) {
	// strong: expensive model; weak: 10× cheaper per token
	// Arrange: 1 strong call (100 prompt tokens) + 1 weak call (10000 prompt tokens)
	// Naive (call-count ratio, 50/50): savings = 45%
	// Token-weighted: actual = 100*0.01 + 10000*0.001 = 1+10 = 11
	//                 all-strong = 10100*0.01 = 101
	//                 savings ≈ (1-11/101)*100 ≈ 89%
	strongMock := &mockLLM{
		name:  "opus",
		usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 20},
	}
	weakMock := &mockLLM{
		name:  "haiku",
		usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10000, CandidatesTokenCount: 2000},
	}

	ctx := context.Background()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "test"}}},
		},
	}

	// First call → strong (score 0.9 >= 0.7)
	r := NewRoutingLLM(strongMock, nil, weakMock, &fixedClassifier{score: 0.9}, 0.7, 0.3)
	for resp, err := range r.GenerateContent(ctx, req, false) {
		_ = resp
		_ = err
	}
	r.Stats.RecordStrong() // stats already recorded inside GenerateContent; this is the call counter

	// Swap classifier to weak for second call.
	r2 := NewRoutingLLM(strongMock, nil, weakMock, &fixedClassifier{score: 0.1}, 0.7, 0.3)
	// Copy token state from first routing run.
	r2.Stats.RecordTokens("strong", 100, 20)

	for resp, err := range r2.GenerateContent(ctx, req, false) {
		_ = resp
		_ = err
	}
	// r2.Stats now has: strong 100/20 tokens (manually), weak 10000/2000 tokens (from iterator)
	// and call counts: 1 weak from GenerateContent.

	r2.SetPricing(
		ModelCost{PromptCost: 0.01, CompletionCost: 0.03},
		ModelCost{},
		ModelCost{PromptCost: 0.001, CompletionCost: 0.003},
	)
	// Manually record the strong call count to match the token state.
	r2.Stats.RecordStrong()

	line := r2.SummaryLine()
	if !strings.Contains(line, "Saved") {
		t.Errorf("summary %q should include cost savings", line)
	}

	// The token-weighted savings (~89%) should be much higher than the naive
	// call-count savings (~45%). Verify it is above 70% (clearly above naive).
	savings := r2.CostSavingsPct()
	if savings < 70 {
		t.Errorf("token-weighted savings = %.1f%%; want > 70%% (naive call-count would give ~45%%)", savings)
	}
}

// TestRoutingLLM_SummaryLine_IncludesCostSavings verifies the fallback path:
// when mockLLM returns no UsageMetadata, the call-count ratio is used.
func TestRoutingLLM_SummaryLine_IncludesCostSavingsFallback(t *testing.T) {
	// mockLLM with no usage field set → UsageMetadata is nil → fallback to call count.
	r := NewRoutingLLM(&mockLLM{name: "opus"}, nil, &mockLLM{name: "haiku"}, &fixedClassifier{score: 0.1}, 0.7, 0.3)
	r.StrongName = "opus"
	r.WeakName = "haiku"
	r.SetPricing(ModelCost{PromptCost: 0.01}, ModelCost{}, ModelCost{PromptCost: 0.001})
	r.Stats.RecordWeak()
	r.Stats.RecordWeak()

	line := r.SummaryLine()
	if line == "" {
		t.Fatal("expected a summary line")
	}
	if !strings.Contains(line, "Saved") {
		t.Errorf("summary %q should include cost savings (fallback path)", line)
	}
	if !strings.Contains(line, "2 calls") {
		t.Errorf("summary %q should include call count", line)
	}
}
