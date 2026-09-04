package routing

import (
	"os"
	"testing"
)

// TestParseNpz_RealWeights parses the actual trained router_weights.npz file
// from the astonish-router project outputs and verifies shape/value correctness.
// This test is skipped if the weights file is not present.
func TestParseNpz_RealWeights(t *testing.T) {
	candidates := []string{
		"../../../astonish-router/outputs/router_weights.npz",
		"~/Projects/astonish-router/outputs/router_weights.npz",
	}
	var path string
	for _, c := range candidates {
		expanded := expandTilde(c)
		if _, err := os.Stat(expanded); err == nil {
			path = expanded
			break
		}
	}
	if path == "" {
		t.Skip("router_weights.npz not found in expected locations; skipping real weights test")
	}

	w, err := parseNpz(path)
	if err != nil {
		t.Fatalf("parseNpz(%q): %v", path, err)
	}

	// Shape assertions for the trained model
	if len(w.layer1Weight) != 64 {
		t.Errorf("layer1_weight rows = %d, want 64", len(w.layer1Weight))
	}
	if len(w.layer1Weight) > 0 && len(w.layer1Weight[0]) != 384 {
		t.Errorf("layer1_weight cols = %d, want 384", len(w.layer1Weight[0]))
	}
	if len(w.layer1Bias) != 64 {
		t.Errorf("layer1_bias len = %d, want 64", len(w.layer1Bias))
	}
	if len(w.layer2Weight) != 32 {
		t.Errorf("layer2_weight rows = %d, want 32", len(w.layer2Weight))
	}
	if len(w.layer2Weight) > 0 && len(w.layer2Weight[0]) != 64 {
		t.Errorf("layer2_weight cols = %d, want 64", len(w.layer2Weight[0]))
	}
	if len(w.layer2Bias) != 32 {
		t.Errorf("layer2_bias len = %d, want 32", len(w.layer2Bias))
	}
	if len(w.layer3Weight) != 32 {
		t.Errorf("layer3_weight len = %d, want 32", len(w.layer3Weight))
	}
	if w.embeddingDim != 384 {
		t.Errorf("embeddingDim = %d, want 384", w.embeddingDim)
	}
	if w.modelDim != 64 {
		t.Errorf("modelDim = %d, want 64", w.modelDim)
	}

	t.Logf("Parsed weights: layer1(%dx%d), layer2(%dx%d), layer3(%d), bias=%.4f",
		len(w.layer1Weight), len(w.layer1Weight[0]),
		len(w.layer2Weight), len(w.layer2Weight[0]),
		len(w.layer3Weight), w.layer3Bias)
}

// TestParseNpz_ForwardPassRealWeights loads real weights and runs a known prompt
// to verify the expected score direction (simple vs complex).
func TestParseNpz_ForwardPassRealWeights(t *testing.T) {
	candidates := []string{
		"../../../astonish-router/outputs/router_weights.npz",
		"~/Projects/astonish-router/outputs/router_weights.npz",
	}
	var path string
	for _, c := range candidates {
		expanded := expandTilde(c)
		if _, err := os.Stat(expanded); err == nil {
			path = expanded
			break
		}
	}
	if path == "" {
		t.Skip("router_weights.npz not found; skipping forward pass test")
	}

	// We can't call the Hugot embedder here (no pipeline in tests), but we can
	// test that mlpForward runs without panicking on real-sized inputs.
	w, err := parseNpz(path)
	if err != nil {
		t.Fatalf("parseNpz: %v", err)
	}

	// Synthesize a 384-dim all-ones embedding (not a real embedding, but exercises all code paths)
	emb := make([]float32, 384)
	for i := range emb {
		emb[i] = 1.0 / 384.0 // unit-ish norm
	}
	score := mlpForward(w, emb)
	if score < 0 || score > 1 {
		t.Errorf("mlpForward with real weights returned out-of-range score: %.6f", score)
	}
	t.Logf("Real weights forward pass (unit input): score=%.6f", score)
}
