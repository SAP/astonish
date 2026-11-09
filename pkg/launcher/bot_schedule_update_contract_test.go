package launcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/provider/litellm"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise the actual LiteLLM/OpenAI transport, not just a Go struct reflection.
func TestBotScheduleUpdateProviderContract(t *testing.T) {
	f := newBotFactoryFixture(t)
	ts, err := BotScheduleTools(f.req, &BotChatScope{Services: f.services})
	if err != nil {
		t.Fatal(err)
	}
	var selected botRunnableTool
	for _, candidate := range ts {
		if candidate.Name() == "update_scheduled_job" {
			selected = candidate.(botRunnableTool)
		}
	}
	if selected == nil {
		t.Fatal("update tool missing")
	}
	declaration := selected.Declaration()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Parameters  struct {
						Properties map[string]json.RawMessage `json:"properties"`
						AnyOf      []json.RawMessage          `json:"anyOf"`
					} `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Tools) != 1 {
			t.Errorf("tools=%d", len(request.Tools))
			w.WriteHeader(400)
			return
		}
		fn := request.Tools[0].Function
		for _, key := range []string{"job_id", "enabled", "schedule", "timezone"} {
			if fn.Parameters.Properties[key] == nil {
				t.Errorf("provider missing %s", key)
			}
		}
		if len(fn.Parameters.Properties) != 4 {
			t.Errorf("Bot exposes unsupported patch fields: %v", fn.Parameters.Properties)
		}
		if len(fn.Parameters.AnyOf) != 3 {
			t.Errorf("schema accepts job_id-only update: %+v", fn.Parameters)
		}
		for _, want := range []string{"at least one", "top-level", "does not change task instructions", "memory"} {
			if !strings.Contains(fn.Description, want) {
				t.Errorf("description missing %q", want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		// Unknown/nested fields must survive provider decoding; they are rejected by
		// the executable schema, not silently discarded by the provider.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-update","type":"function","function":{"name":"update_scheduled_job","arguments":"{\"job_id\":\"known\",\"patch\":{\"instructions\":\"Celsius\"}}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer server.Close()
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{declaration}}}}, Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "Remember my temperature preference."}}}}}
	count := 0
	for response, err := range litellm.NewProvider("test", server.URL, "claude-4.8-opus").GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		if response.Content == nil {
			continue
		}
		for _, part := range response.Content.Parts {
			if part.FunctionCall != nil {
				count++
				if part.FunctionCall.Args["job_id"] != "known" || part.FunctionCall.Args["patch"].(map[string]any)["instructions"] != "Celsius" {
					t.Fatalf("provider discarded arguments: %v", part.FunctionCall.Args)
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("calls=%d", count)
	}
}
