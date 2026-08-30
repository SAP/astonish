package google

import (
	"context"
	"encoding/json"
	"iter"
	"math/rand"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type capturingLLM struct {
	requests [][]byte
}

func (m *capturingLLM) Name() string { return "capturing" }

func (m *capturingLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	data, err := json.Marshal(req)
	if err == nil {
		m.requests = append(m.requests, data)
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.LLMResponse{}, nil)
	}
}

func TestProviderCanonicalizesToolsAtSDKBoundary(t *testing.T) {
	wrapped := &capturingLLM{}
	provider := &Provider{model: wrapped}
	rng := rand.New(rand.NewSource(42))
	names := []string{"charlie", "alpha", "bravo"}
	for range 50 {
		var tools []*genai.Tool
		for _, index := range rng.Perm(len(names)) {
			properties := make(map[string]any)
			keys := []string{"zeta", "alpha"}
			for _, propertyIndex := range rng.Perm(len(keys)) {
				properties[keys[propertyIndex]] = map[string]any{"type": "string"}
			}
			tools = append(tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: names[index],
				ParametersJsonSchema: map[string]any{
					"type":       "object",
					"required":   []string{"zeta", "alpha"},
					"properties": properties,
				},
			}}})
		}
		req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: tools}}
		for _, err := range provider.GenerateContent(context.Background(), req, false) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for index := 1; index < len(wrapped.requests); index++ {
		if string(wrapped.requests[index]) != string(wrapped.requests[0]) {
			t.Fatalf("SDK request %d differs\n got: %s\nwant: %s", index, wrapped.requests[index], wrapped.requests[0])
		}
	}
}

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
