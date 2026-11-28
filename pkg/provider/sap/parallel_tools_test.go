package sap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestSAPAnthropicParallelToolCallsWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name             string
			value            any
			present, invalid bool
		}{
			{name: "absent"},
			{name: "serial", value: false, present: true},
			{name: "parallel", value: true, present: true},
			{name: "invalid", value: "false", present: true, invalid: true},
			{name: "null", present: true, invalid: true},
		} {
			for _, toolMode := range []string{"functions", "none", "empty"} {
				t.Run(fmt.Sprintf("stream=%v/%s/%s", stream, tc.name, toolMode), func(t *testing.T) {
					var attempts atomic.Int32
					bodies := make(chan map[string]any, 2)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						attempts.Add(1)
						path := "/inference/deployments/test/invoke"
						if stream {
							path += "-with-response-stream"
						}
						if r.Method != http.MethodPost || r.URL.Path != path {
							t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
						}
						if r.Header.Get("AI-Resource-Group") != "test" {
							t.Error("missing resource group")
						}
						var payload map[string]any
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						bodies <- payload
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, sapEvents(sapText, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, sapStop))
						} else {
							_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
						}
					}))
					defer server.Close()
					p := testStreamProvider(server.Client(), server.URL)
					p.modelName = "anthropic--claude-4.6-sonnet"
					ordinary := &model.LLMRequest{
						Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hello"}}}},
						Config: &genai.GenerateContentConfig{
							SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "system"}}},
							HTTPOptions:       &genai.HTTPOptions{ExtraBody: map[string]any{"arbitrary_override": "not forwarded", "tool_choice": map[string]any{"type": "any"}}},
						},
					}
					if toolMode == "functions" {
						ordinary.Config.Tools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", Description: "Look up a value", ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}}}}}}
					} else if toolMode == "empty" {
						ordinary.Config.Tools = []*genai.Tool{{}}
					}
					req := ordinary
					if tc.present {
						req = common.WithParallelToolCalls(ordinary, false)
						req.Config.HTTPOptions.ExtraBody[common.ParallelToolCallsOption] = tc.value
					}
					before, err := json.Marshal(ordinary)
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					failures, finals := 0, 0
					for response, err := range p.GenerateContent(ctx, req, stream) {
						if err != nil {
							failures++
							if !tc.invalid || !strings.Contains(err.Error(), "parallel_tool_calls request option must be a boolean") {
								t.Fatal(err)
							}
						} else if response != nil && !response.Partial {
							finals++
						}
					}
					if tc.invalid {
						if failures != 1 || finals != 0 || attempts.Load() != 0 {
							t.Fatalf("invalid option: failures=%d finals=%d HTTP=%d", failures, finals, attempts.Load())
						}
						return
					}
					if failures != 0 || finals != 1 || attempts.Load() != 1 {
						t.Fatalf("failures=%d finals=%d HTTP=%d", failures, finals, attempts.Load())
					}
					payload := <-bodies
					choice, exists := payload["tool_choice"]
					wantChoice := tc.present && toolMode == "functions"
					if exists != wantChoice {
						t.Errorf("tool_choice present = %v, want %v: %v", exists, wantChoice, payload)
					}
					if wantChoice {
						want := map[string]any{"type": "auto", "disable_parallel_tool_use": !tc.value.(bool)}
						if !reflect.DeepEqual(choice, want) {
							t.Errorf("tool_choice = %v, want %v", choice, want)
						}
					}
					for _, key := range []string{common.ParallelToolCallsOption, "arbitrary_override", "temperature"} {
						if _, ok := payload[key]; ok {
							t.Errorf("unexpected field %s", key)
						}
					}
					if payload["anthropic_version"] != "bedrock-2023-05-31" || payload["system"] != "system" || payload["max_tokens"] != float64(64000) {
						t.Errorf("lost request fields: %v", payload)
					}
					for _, err := range p.GenerateContent(ctx, ordinary, stream) {
						if err != nil {
							t.Fatal(err)
						}
					}
					if attempts.Load() != 2 {
						t.Fatalf("ordinary request HTTP=%d", attempts.Load())
					}
					ordinaryPayload := <-bodies
					if _, exists := ordinaryPayload["tool_choice"]; exists {
						t.Error("serial option leaked into ordinary request")
					}
					delete(payload, "tool_choice")
					if !reflect.DeepEqual(payload, ordinaryPayload) {
						t.Error("other request fields changed")
					}
					after, err := json.Marshal(ordinary)
					if err != nil || string(before) != string(after) {
						t.Fatal("ordinary request mutated")
					}
				})
			}
		}
	}
}
