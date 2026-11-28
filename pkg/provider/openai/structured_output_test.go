package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestExplicitResponseJSONSchemaMapping(t *testing.T) {
	for _, mode := range []string{"schema", "mime-only", "ordinary", "tools", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &genai.GenerateContentConfig{ResponseMIMEType: "application/json", ResponseJsonSchema: map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false}, MaxOutputTokens: 23}
			switch mode {
			case "mime-only":
				cfg.ResponseJsonSchema = nil
			case "ordinary":
				cfg.ResponseJsonSchema = nil
				cfg.ResponseMIMEType = ""
			case "tools":
				cfg.Tools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "test_tool"}}}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var got openailib.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				switch mode {
				case "schema":
					if got.ResponseFormat == nil || got.ResponseFormat.Type != openailib.ChatCompletionResponseFormatTypeJSONSchema || got.ResponseFormat.JSONSchema == nil || !got.ResponseFormat.JSONSchema.Strict {
						t.Errorf("schema not mapped: %+v", got.ResponseFormat)
					}
				case "mime-only":
					if got.ResponseFormat == nil || got.ResponseFormat.Type != openailib.ChatCompletionResponseFormatTypeJSONObject {
						t.Errorf("MIME behavior changed: %+v", got.ResponseFormat)
					}
				default:
					if got.ResponseFormat != nil {
						t.Errorf("ordinary/tool/unsupported behavior changed: %+v", got.ResponseFormat)
					}
				}
				if got.MaxCompletionTokens != 23 {
					t.Errorf("limit changed: %d", got.MaxCompletionTokens)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			cc := openailib.DefaultConfig("test-only")
			cc.BaseURL = server.URL
			p := NewProvider(openailib.NewClientWithConfig(cc), "test", mode != "unsupported")
			req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("JSON", "user")}, Config: cfg}
			for _, err := range p.GenerateContent(context.Background(), req, false) {
				if err != nil {
					t.Fatal(err)
				}
			}
			// Responses uses the same explicit opt-in, independently of MIME-only mode.
			if mode != "tools" && mode != "unsupported" {
				got := toResponsesRequest(req, "gpt-6", nil, nil, 23)
				if mode == "schema" && (got.Text == nil || got.Text.Format.Type != "json_schema" || !got.Text.Format.Strict || got.Text.Format.Schema == nil) {
					t.Fatalf("Responses lost schema: %+v", got.Text)
				}
				if mode == "mime-only" && (got.Text == nil || got.Text.Format.Type != "json_object") {
					t.Fatalf("Responses MIME changed: %+v", got.Text)
				}
				if mode == "ordinary" && got.Text != nil {
					t.Fatalf("Responses ordinary changed: %+v", got.Text)
				}
			}
		})
	}
}
