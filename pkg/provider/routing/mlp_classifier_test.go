package routing

import (
	"context"
	"math"
	"testing"
)

// makeTestWeights builds a small npzWeights with known values for deterministic testing.
func makeTestWeights() *npzWeights {
	// Layer 1: 2×3 (modelDim=2, embDim=3) — identity-ish
	l1w := [][]float64{
		{1.0, 0.0, 0.0},
		{0.0, 1.0, 0.0},
	}
	l1b := []float64{0.0, 0.0}

	// Layer 2: 1×2 (outDim=1, hidDim=2)
	l2w := [][]float64{
		{1.0, 1.0},
	}
	l2b := []float64{0.0}

	// Layer 3: scalar projection
	l3w := []float64{1.0}
	l3b := 0.0

	return &npzWeights{
		layer1Weight: l1w,
		layer1Bias:   l1b,
		layer2Weight: l2w,
		layer2Bias:   l2b,
		layer3Weight: l3w,
		layer3Bias:   l3b,
		embeddingDim: 3,
		modelDim:     2,
	}
}

func TestMLPForward_KnownValues(t *testing.T) {
	w := makeTestWeights()

	// Input: [2.0, 3.0, 0.0]
	// h1 = ReLU([2.0, 3.0]) = [2.0, 3.0]
	// h2 = ReLU([2.0+3.0]) = ReLU([5.0]) = [5.0]
	// logit = 1.0*5.0 + 0.0 = 5.0
	// sigmoid(5.0) ≈ 0.9933
	emb := []float32{2.0, 3.0, 0.0}
	got := mlpForward(w, emb)
	want := 1.0 / (1.0 + math.Exp(-5.0))
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("mlpForward([2,3,0]) = %.6f, want %.6f", got, want)
	}
}

func TestMLPForward_NegativeInputsClamped(t *testing.T) {
	w := makeTestWeights()
	// Negative inputs get ReLU'd to 0, so h1=[0,0], h2=[0], logit=0, sigmoid(0)=0.5
	emb := []float32{-1.0, -2.0, -3.0}
	got := mlpForward(w, emb)
	want := 0.5
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("mlpForward(all-negative) = %.6f, want %.6f", got, want)
	}
}

func TestMLPClassifier_EmptyPrompt(t *testing.T) {
	c := NewMLPClassifier(makeTestWeights(), nil)
	score := float64(c.Classify("", ClassifierContext{}))
	if score != 0.1 {
		t.Errorf("empty prompt score = %.3f, want 0.1", score)
	}
}

func TestMLPClassifier_PlanModeAlwaysStrong(t *testing.T) {
	c := NewMLPClassifier(makeTestWeights(), nil)
	score := float64(c.Classify("ok", ClassifierContext{HasPlanMode: true}))
	if score != 0.9 {
		t.Errorf("plan mode score = %.3f, want 0.9", score)
	}
}

func TestMLPClassifier_NilEmbedNeutral(t *testing.T) {
	c := NewMLPClassifier(makeTestWeights(), nil)
	score := float64(c.Classify("hello", ClassifierContext{}))
	if score != 0.5 {
		t.Errorf("nil embed score = %.3f, want 0.5", score)
	}
}

func TestMLPClassifier_EmbedErrorNeutral(t *testing.T) {
	errEmbed := EmbedFunc(func(ctx context.Context, text string) ([]float32, error) {
		return nil, context.Canceled
	})
	c := NewMLPClassifier(makeTestWeights(), errEmbed)
	score := float64(c.Classify("hello", ClassifierContext{}))
	if score != 0.5 {
		t.Errorf("embed error score = %.3f, want 0.5", score)
	}
}

func TestMLPClassifier_EmbedDimMismatch(t *testing.T) {
	// Return a 5-dim embedding when weights expect 3-dim
	badDimEmbed := EmbedFunc(func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1.0, 2.0, 3.0, 4.0, 5.0}, nil
	})
	c := NewMLPClassifier(makeTestWeights(), badDimEmbed)
	score := float64(c.Classify("hello", ClassifierContext{}))
	if score != 0.5 {
		t.Errorf("dim mismatch score = %.3f, want 0.5", score)
	}
}

func TestMLPClassifier_DeterministicOutput(t *testing.T) {
	fixedEmbed := EmbedFunc(func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1.0, 0.5, 0.0}, nil
	})
	c := NewMLPClassifier(makeTestWeights(), fixedEmbed)

	score1 := float64(c.Classify("test prompt", ClassifierContext{}))
	score2 := float64(c.Classify("test prompt", ClassifierContext{}))
	if score1 != score2 {
		t.Errorf("scores not deterministic: %.6f != %.6f", score1, score2)
	}
	// Both should be the same actual value
	if score1 < 0 || score1 > 1 {
		t.Errorf("score out of range: %.6f", score1)
	}
}

func TestMLPClassifier_ImplementsInterface(t *testing.T) {
	// Compile-time check that MLPClassifier satisfies ComplexityClassifier.
	var _ ComplexityClassifier = (*MLPClassifier)(nil)
}
