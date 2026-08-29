package openai

import (
	"context"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestOutboundToolSerializationIsCanonical(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	config := openailib.DefaultConfig("test")
	config.BaseURL = server.URL
	provider := NewProvider(openailib.NewClientWithConfig(config), "test-model", false)
	rng := rand.New(rand.NewSource(42))
	names := []string{"charlie", "alpha", "bravo"}
	for iteration := 0; iteration < 50; iteration++ {
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
		req := &model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("test", genai.RoleUser)},
			Config:   &genai.GenerateContentConfig{Tools: tools},
		}
		for _, err := range provider.GenerateContent(context.Background(), req, false) {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for index := 1; index < len(bodies); index++ {
		if string(bodies[index]) != string(bodies[0]) {
			t.Fatalf("outbound request %d differs\n got: %s\nwant: %s", index, bodies[index], bodies[0])
		}
	}
}

func TestOutboundToolCanonicalizationFailureStopsRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	config := openailib.DefaultConfig("test")
	config.BaseURL = server.URL
	provider := NewProvider(openailib.NewClientWithConfig(config), "test-model", false)
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "invalid", ParametersJsonSchema: map[string]any{"bad": make(chan int)},
		}},
	}}}}
	var gotErr error
	for _, err := range provider.GenerateContent(context.Background(), req, false) {
		gotErr = err
	}
	if gotErr == nil {
		t.Fatal("expected canonicalization error")
	}
	if requests != 0 {
		t.Fatalf("sent %d requests after canonicalization failure", requests)
	}
}
