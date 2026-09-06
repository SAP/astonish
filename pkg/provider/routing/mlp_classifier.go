package routing

import (
	"context"
	"log/slog"
	"math"
)

// EmbedFunc is a function that returns a 384-dim L2-normalized embedding vector
// for a text string. This matches the signature of memory.EmbeddingFunc
// (func(context.Context, string) ([]float32, error)), allowing the routing
// package to embed prompts via the existing Hugot all-MiniLM-L6-v2 pipeline
// without importing the memory package (avoiding circular dependencies).
type EmbedFunc func(ctx context.Context, text string) ([]float32, error)

// MLPClassifier implements ComplexityClassifier using the pre-trained 3-layer MLP.
//
// Architecture (26,753 parameters, 98 KB):
//
//	Input:  all-MiniLM-L6-v2 embedding (384-dim, L2-normalized)
//	Layer 1: Linear(384→64) + ReLU
//	Layer 2: Linear(64→32)  + ReLU
//	Layer 3: Linear(32→1)   + sigmoid
//	Output: P(needs_strong) ∈ [0, 1]
//
// It is model-agnostic: the same weights work regardless of which strong/weak
// model pair is configured in Astonish.
type MLPClassifier struct {
	weights *npzWeights
	embed   EmbedFunc
}

// NewMLPClassifier creates an MLPClassifier from pre-loaded weights and an embed function.
func NewMLPClassifier(weights *npzWeights, embed EmbedFunc) *MLPClassifier {
	return &MLPClassifier{weights: weights, embed: embed}
}

// LoadMLPClassifier loads weights from a .npz file and returns a ready classifier.
// The embed function is used to convert prompts to 384-dim embeddings.
func LoadMLPClassifier(weightsPath string, embed EmbedFunc) (*MLPClassifier, error) {
	weights, err := parseNpz(weightsPath)
	if err != nil {
		return nil, err
	}
	return NewMLPClassifier(weights, embed), nil
}

// Classify implements ComplexityClassifier.
//
// Returns ComplexityScore in [0, 1]:
//   - < 0.5: route to weak model (simple prompt)
//   - ≥ 0.5: route to strong model (complex prompt)
//
// Falls back gracefully:
//   - nil embed or embed error → 0.5 (neutral)
//   - empty prompt → 0.1 (simple)
//   - HasPlanMode → 0.9 (always strong for planning tasks)
func (c *MLPClassifier) Classify(ctx context.Context, prompt string, cc ClassifierContext) ComplexityScore {
	if prompt == "" {
		return ComplexityScore(0.1)
	}
	// HasPlanMode is a strong out-of-band signal: planning tasks need the strong model.
	if cc.HasPlanMode {
		return ComplexityScore(0.9)
	}
	if c.embed == nil {
		return ComplexityScore(0.5)
	}

	emb, err := c.embed(ctx, prompt)
	if err != nil {
		slog.Debug("[routing] embed failed, returning neutral score", "error", err)
		return ComplexityScore(0.5)
	}
	if len(emb) != c.weights.embeddingDim {
		slog.Debug("[routing] embed dim mismatch, returning neutral score",
			"got", len(emb), "want", c.weights.embeddingDim)
		return ComplexityScore(0.5)
	}

	score := mlpForward(c.weights, emb)
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return ComplexityScore(score)
}

// mlpForward runs the 3-layer MLP forward pass in pure float64 arithmetic.
// This replicates the Python MFRouterInference.predict() method exactly.
func mlpForward(w *npzWeights, emb []float32) float64 {
	// Layer 1: h1 = ReLU(emb @ W1^T + b1)
	h1 := make([]float64, len(w.layer1Bias))
	for i, row := range w.layer1Weight {
		sum := w.layer1Bias[i]
		for j, wv := range row {
			sum += wv * float64(emb[j])
		}
		if sum < 0 {
			sum = 0 // ReLU
		}
		h1[i] = sum
	}

	// Layer 2: h2 = ReLU(h1 @ W2^T + b2)
	h2 := make([]float64, len(w.layer2Bias))
	for i, row := range w.layer2Weight {
		sum := w.layer2Bias[i]
		for j, wv := range row {
			sum += wv * h1[j]
		}
		if sum < 0 {
			sum = 0 // ReLU
		}
		h2[i] = sum
	}

	// Layer 3: logit = w3 · h2 + b3
	logit := w.layer3Bias
	for i, wv := range w.layer3Weight {
		logit += wv * h2[i]
	}

	// Clamp to avoid exp overflow, then sigmoid
	if logit > 20 {
		logit = 20
	}
	if logit < -20 {
		logit = -20
	}
	return 1.0 / (1.0 + math.Exp(-logit))
}
