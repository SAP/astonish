package memory

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

func newTestHugotEmbedder(run func(context.Context, string) ([]float32, error)) *HugotEmbedder {
	return &HugotEmbedder{
		run:      run,
		gate:     make(chan struct{}, 1),
		cache:    make(map[string][]float32),
		inflight: make(map[string]*embeddingCall),
	}
}

func TestHugotEmbeddingFuncCachesAndCoalesces(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	h := newTestHugotEmbedder(func(context.Context, string) ([]float32, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []float32{1, 2}, nil
	})
	embed := h.EmbeddingFunc()
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			got, err := embed(context.Background(), "same")
			if err != nil || len(got) != 2 {
				t.Errorf("embed = %v, %v", got, err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if _, err := embed(context.Background(), "same"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("pipeline calls = %d, want 1", calls.Load())
	}
}

func TestHugotEmbeddingFuncWaitIsCancelable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := newTestHugotEmbedder(func(context.Context, string) ([]float32, error) {
		close(started)
		<-release
		return []float32{1}, nil
	})
	embed := h.EmbeddingFunc()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = embed(context.Background(), "same")
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := embed(ctx, "same"); !errors.Is(err, context.Canceled) {
		t.Fatalf("embed error = %v, want context canceled", err)
	}
	close(release)
	<-done
}

func skipIfNoModel(t *testing.T) {
	t.Helper()
	modelsDir, err := config.GetModelsDir()
	if err != nil {
		t.Skipf("Cannot resolve models directory: %v", err)
	}
	modelPath := modelsDir + "/sentence-transformers_all-MiniLM-L6-v2/model.onnx"
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		t.Skip("Model not downloaded yet, skipping")
	}
}

func testModelsDir(t *testing.T) string {
	t.Helper()
	modelsDir, err := config.GetModelsDir()
	if err != nil {
		t.Fatalf("Cannot resolve models directory: %v", err)
	}
	return modelsDir
}

// TestHugotEmbedderInit tests session and pipeline creation.
func TestHugotEmbedderInit(t *testing.T) {
	skipIfNoModel(t)

	start := time.Now()
	embedder, err := NewHugotEmbedder(testModelsDir(t), true)
	if err != nil {
		t.Fatalf("NewHugotEmbedder failed: %v", err)
	}
	defer embedder.Close()
	t.Logf("NewHugotEmbedder completed in %v", time.Since(start))
}

// TestHugotEmbedderRunPipeline tests that RunPipeline completes without deadlock.
// Regression test: GoMLX's simplego backend deadlocks in parallel mode when
// runtime.NumCPU() <= 2 due to nested WaitToStart calls in DotGeneral.
func TestHugotEmbedderRunPipeline(t *testing.T) {
	skipIfNoModel(t)
	t.Logf("runtime.NumCPU() = %d, GOMAXPROCS = %d", runtime.NumCPU(), runtime.GOMAXPROCS(0))

	embedder, err := NewHugotEmbedder(testModelsDir(t), true)
	if err != nil {
		t.Fatalf("NewHugotEmbedder failed: %v", err)
	}
	defer embedder.Close()

	done := make(chan struct{})
	var runErr error

	go func() {
		defer close(done)
		_, runErr = embedder.pipeline.RunPipeline(context.Background(), []string{"Hello world"})
	}()

	select {
	case <-done:
		if runErr != nil {
			t.Fatalf("RunPipeline failed: %v", runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunPipeline hung for 30 seconds — deadlock not resolved")
	}
}

// TestHugotEmbeddingFunc tests the embedding function.
func TestHugotEmbeddingFunc(t *testing.T) {
	skipIfNoModel(t)

	embedder, err := NewHugotEmbedder(testModelsDir(t), true)
	if err != nil {
		t.Fatalf("NewHugotEmbedder failed: %v", err)
	}
	defer embedder.Close()

	embFunc := embedder.EmbeddingFunc()

	result, err := embFunc(context.Background(), "test embedding")
	if err != nil {
		t.Fatalf("EmbeddingFunc failed: %v", err)
	}
	if len(result) != 384 {
		t.Fatalf("Expected 384 dimensions, got %d", len(result))
	}

	// Different text must produce different embedding
	result2, err := embFunc(context.Background(), "another text to embed")
	if err != nil {
		t.Fatalf("Second EmbeddingFunc call failed: %v", err)
	}
	same := true
	for i := range result {
		if result[i] != result2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("Two different texts produced identical embeddings")
	}
}

// TestHugotEmbedderMultipleTexts tests embedding multiple texts sequentially.
func TestHugotEmbedderMultipleTexts(t *testing.T) {
	skipIfNoModel(t)

	embedder, err := NewHugotEmbedder(testModelsDir(t), true)
	if err != nil {
		t.Fatalf("NewHugotEmbedder failed: %v", err)
	}
	defer embedder.Close()

	embFunc := embedder.EmbeddingFunc()
	texts := []string{
		"The quick brown fox jumps over the lazy dog",
		"Machine learning models process text data",
		"Go is a statically typed programming language",
	}

	for _, text := range texts {
		result, err := embFunc(context.Background(), text)
		if err != nil {
			t.Fatalf("EmbeddingFunc failed for %q: %v", text, err)
		}
		if len(result) != 384 {
			t.Fatalf("Expected 384 dimensions for %q, got %d", text, len(result))
		}
	}
}
