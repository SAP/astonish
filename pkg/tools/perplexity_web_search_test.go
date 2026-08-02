package tools

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type mockPerplexityLLM struct {
	request *model.LLMRequest
	resp    *model.LLMResponse
}

func (m *mockPerplexityLLM) Name() string { return "mock-sonar" }

func (m *mockPerplexityLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.request = req
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(m.resp, nil)
	}
}

func TestRunPerplexityWebSearchUsesConfiguredProviderModel(t *testing.T) {
	mock := &mockPerplexityLLM{resp: &model.LLMResponse{
		Content: &genai.Content{Parts: []*genai.Part{{Text: "Sonar answer. Sources: https://example.com/a"}}},
		GroundingMetadata: &genai.GroundingMetadata{GroundingChunks: []*genai.GroundingChunk{{
			Web: &genai.GroundingChunkWeb{Title: "Example", URI: "https://example.com/a"},
		}}},
	}}
	var gotProvider, gotModel string
	factory := func(_ context.Context, providerName, modelName string, _ *config.AppConfig) (model.LLM, error) {
		gotProvider = providerName
		gotModel = modelName
		return mock, nil
	}

	cfg := &config.AppConfig{PerplexityWebSearch: config.PerplexityWebSearchConfig{
		Provider:          "SAP AI Core",
		Model:             "perplexity/sonar-pro",
		SearchContextSize: "high",
		MaxResults:        7,
	}}

	result, err := RunPerplexityWebSearch(nil, PerplexityWebSearchArgs{Query: "latest Go release", Recency: "week", Domains: []string{"go.dev"}}, cfg, factory)
	if err != nil {
		t.Fatalf("RunPerplexityWebSearch returned error: %v", err)
	}
	if gotProvider != "SAP AI Core" || gotModel != "perplexity/sonar-pro" {
		t.Fatalf("factory called with provider/model %q/%q", gotProvider, gotModel)
	}
	if result.Provider != gotProvider || result.Model != gotModel || result.Query != "latest Go release" {
		t.Fatalf("unexpected result identity: %+v", result)
	}
	if len(result.Citations) != 1 || result.Citations[0] != "https://example.com/a" {
		t.Fatalf("unexpected citations: %#v", result.Citations)
	}
	if mock.request == nil || len(mock.request.Contents) != 1 || mock.request.Model != "perplexity/sonar-pro" {
		t.Fatalf("unexpected model request: %#v", mock.request)
	}
	prompt := mock.request.Contents[0].Parts[0].Text
	for _, want := range []string{"latest Go release", "go.dev", "week", "Maximum sources: 7", "Search context size: high"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestRunPerplexityWebSearchRequiresConfiguration(t *testing.T) {
	_, err := RunPerplexityWebSearch(nil, PerplexityWebSearchArgs{Query: "x"}, &config.AppConfig{}, nil)
	if err == nil {
		t.Fatal("expected missing configuration error")
	}
}
