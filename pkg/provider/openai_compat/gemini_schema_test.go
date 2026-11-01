package openai_compat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	compat "github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestGeminiAnnouncePlanNullableSchema(t *testing.T) {
	for _, modelName := range []string{"gemini-2.5-flash", "gemini-3.8-flash", "gpt-4o"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", modelName, streaming), func(t *testing.T) {
				tool, err := tools.NewAnnouncePlanTool()
				if err != nil {
					t.Fatal(err)
				}
				fd := tool.(interface {
					Declaration() *genai.FunctionDeclaration
				}).Declaration()
				before, _ := json.Marshal(fd.ParametersJsonSchema)
				var captured map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/litellm/v1/chat/completions" {
						t.Errorf("unexpected path: %s", r.URL.Path)
					}
					if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
						t.Error(err)
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
					}
				}))
				defer server.Close()
				llm := compat.NewProvider(context.Background(), "fixture", server.URL+"/litellm/v1", modelName, false)
				req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{fd}}}}}
				for _, err := range llm.GenerateContent(context.Background(), req, streaming) {
					if err != nil {
						t.Fatal(err)
					}
				}
				schema := captured["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
				steps := schema["properties"].(map[string]any)["steps"].(map[string]any)
				files := steps["items"].(map[string]any)["properties"].(map[string]any)["files"].(map[string]any)
				for name, field := range map[string]map[string]any{"steps": steps, "files": files} {
					if modelName == "gpt-4o" {
						if _, ok := field["type"].([]any); !ok {
							t.Fatalf("non-Gemini %s type changed: %v", name, field["type"])
						}
					} else {
						if field["type"] != "array" || field["nullable"] != true {
							t.Fatalf("Gemini %s needs scalar array + nullable, got type=%v nullable=%v", name, field["type"], field["nullable"])
						}
					}
					if field["items"] == nil || field["description"] == nil {
						t.Fatalf("%s schema lost structure", name)
					}
				}
				after, _ := json.Marshal(fd.ParametersJsonSchema)
				if string(before) != string(after) {
					t.Fatal("shared declaration mutated")
				}
			})
		}
	}
}
