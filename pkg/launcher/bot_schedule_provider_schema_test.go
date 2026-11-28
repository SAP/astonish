package launcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/provider/litellm"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Models validate every advertised tool before work, even when not scheduling.
func TestBotScheduleSchemasAcceptedBeforeOrdinaryAnswer(t *testing.T) {
	f := newBotFactoryFixture(t)
	tools, err := BotScheduleTools(f.req, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	declarations := []*genai.FunctionDeclaration{}
	for _, candidate := range tools {
		declarations = append(declarations, candidate.(botRunnableTool).Declaration())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name       string                     `json:"name"`
					Parameters map[string]json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		for _, tool := range request.Tools {
			for _, forbidden := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
				if _, exists := tool.Function.Parameters[forbidden]; exists {
					t.Errorf("%s has prohibited top-level %s", tool.Function.Name, forbidden)
					http.Error(w, "Invalid tool schema", 400)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Ready to research."},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	request := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: declarations}}}, Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Anthropic News"}}}}}
	answered := false
	for response, err := range litellm.NewProvider("test", server.URL, "gpt-5.6-terra").GenerateContent(context.Background(), request, false) {
		if err != nil {
			t.Fatal(err)
		}
		if response.Content != nil {
			for _, part := range response.Content.Parts {
				if part.Text == "Ready to research." {
					answered = true
				}
			}
		}
	}
	if !answered {
		t.Fatal("ordinary answer blocked by advertised tool schemas")
	}
}
