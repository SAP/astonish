package google

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type usageLLM struct{}

func (usageLLM) Name() string { return "usage" }

func (usageLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:        100,
				CandidatesTokenCount:    20,
				TotalTokenCount:         120,
				CachedContentTokenCount: 75,
			},
		}, nil)
	}
}

func TestProviderPreservesWrappedUsageMetadata(t *testing.T) {
	provider := &Provider{model: usageLLM{}}
	var got *model.LLMResponse
	for resp, err := range provider.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
		got = resp
	}
	if got == nil || got.UsageMetadata == nil {
		t.Fatal("wrapped response has no usage metadata")
	}
	if got.UsageMetadata.CachedContentTokenCount != 75 {
		t.Fatalf("CachedContentTokenCount = %d, want 75", got.UsageMetadata.CachedContentTokenCount)
	}
}
