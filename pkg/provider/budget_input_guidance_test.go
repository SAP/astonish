package provider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBudgetInputTrustedGuidancePreservesCanonicalPrefix(t *testing.T) {
	llm, req := inputHistoryFixture(t)
	original := append([]*genai.Content(nil), req.Contents...)
	req.Contents = append(req.Contents, genai.NewContentFromText("Run budget remaining: 500000 tokens", "user"))
	ctx := WithBudgetInputGuidance(context.Background(), req)
	_, snapshot, err := llm.estimateInput(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	llm.inputHistory.observe(snapshot, 48000)
	next := *req
	delta := genai.NewContentFromText(strings.Repeat("new \"漢字", 1000), "user")
	guidance := genai.NewContentFromText("Run budget remaining: 450000 tokens", "user")
	next.Contents = append(original, delta, guidance)
	ctx = WithBudgetInputGuidance(context.Background(), &next)
	got, err := EstimateModelInput(ctx, llm, &next)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := json.Marshal(delta)
	g, _ := json.Marshal(guidance)
	want := int64(48000 + 256 + 64 + len(d) + 64 + len(g))
	if got != want {
		t.Fatalf("estimate=%d want=%d", got, want)
	}
	// Reserve's canonical request and admission use one history; reserve never
	// mutates that history merely by estimating, and doesn't subtract old guidance.
	canonical := next
	canonical.Contents = next.Contents[:len(next.Contents)-1]
	n, err := EstimateModelInput(context.Background(), llm, &canonical)
	if err != nil || n != want-int64(len(g)+64) {
		t.Fatalf("canonical=%d error=%v", n, err)
	}
	if snapshot.contents != len(original) {
		t.Fatal("guidance persisted in canonical history")
	}
}

func TestBudgetInputGuidanceCannotHideUntrustedChanges(t *testing.T) {
	for _, mode := range []string{"unmarked-spoof", "mutated-guidance", "copied-request", "config", "history", "media", "cached"} {
		t.Run(mode, func(t *testing.T) {
			llm, req := inputHistoryFixture(t)
			req.Contents = append(req.Contents, genai.NewContentFromText("Run budget remaining: 500000 tokens", "user"))
			ctx := WithBudgetInputGuidance(context.Background(), req)
			if mode == "unmarked-spoof" {
				ctx = context.Background()
			}
			if mode == "mutated-guidance" {
				req.Contents[len(req.Contents)-1].Parts[0].Text = "changed after mark"
			}
			if mode == "copied-request" {
				copy := *req
				req = &copy
			}
			_, snapshot, err := llm.estimateInput(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			llm.inputHistory.observe(snapshot, 123)
			next := *req
			next.Contents = append([]*genai.Content(nil), req.Contents...)
			next.Contents[len(next.Contents)-1] = genai.NewContentFromText("Run budget remaining: 499000 tokens", "user")
			config := *req.Config
			next.Config = &config
			switch mode {
			case "config":
				next.Config.Temperature = genai.Ptr(float32(.2))
			case "history":
				next.Contents[0] = genai.NewContentFromText(strings.Repeat("replacement", 10000), "user")
			case "media":
				next.Contents[0] = &genai.Content{Parts: []*genai.Part{{FileData: &genai.FileData{FileURI: "remote"}}}}
			case "cached":
				next.Config.CachedContent = "remote"
			}
			nextCtx := WithBudgetInputGuidance(context.Background(), &next)
			want, wantErr := estimateBudgetInput(nextCtx, &next)
			got, err := EstimateModelInput(nextCtx, llm, &next)
			if got != want || (err != nil) != (wantErr != nil) {
				t.Fatalf("unsafe reuse: %d/%v want=%d/%v", got, err, want, wantErr)
			}
		})
	}
}

func TestBudgetInputEstimateCapabilityAndOverflow(t *testing.T) {
	llm, req := inputHistoryFixture(t)
	_, snapshot, err := llm.estimateInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	llm.inputHistory.observe(snapshot, math.MaxInt64)
	if _, err := EstimateModelInput(context.Background(), llm, req); err == nil {
		t.Fatal("overflow must not reduce observed prompt to byte bound")
	}
	expected := errors.New("estimator unavailable")
	llm.options.EstimateInput = func(context.Context, *model.LLMRequest) (int64, error) { return 0, expected }
	if _, err := EstimateModelInput(context.Background(), llm, req); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	llm.options.EstimateInput = func(context.Context, *model.LLMRequest) (int64, error) { return -1, nil }
	if _, err := EstimateModelInput(context.Background(), llm, req); err == nil {
		t.Fatal("negative estimate accepted")
	}
	if _, err := EstimateModelInput(context.Background(), llm, nil); err == nil {
		t.Fatal("nil request accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EstimateModelInput(ctx, llm, req); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	want, _ := estimateBudgetInput(context.Background(), req)
	if got, err := EstimateModelInput(context.Background(), &inputHistoryLLM{name: "other"}, req); err != nil || got != want {
		t.Fatalf("fallback=%d/%v", got, err)
	}
}
