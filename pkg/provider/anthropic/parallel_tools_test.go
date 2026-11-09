package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type parallelCase struct {
	name             string
	value            any
	present, invalid bool
}

func parallelCases() []parallelCase {
	return []parallelCase{
		{name: "absent"},
		{name: "serial", value: false, present: true},
		{name: "parallel", value: true, present: true},
		{name: "invalid", value: "false", present: true, invalid: true},
		{name: "null", present: true, invalid: true},
	}
}

func parallelRequest(toolMode string) *model.LLMRequest {
	req := &model.LLMRequest{
		Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hello"}}}},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "system"}}},
			HTTPOptions:       &genai.HTTPOptions{ExtraBody: map[string]any{"arbitrary_override": "not forwarded", "tool_choice": map[string]any{"type": "any"}}},
		},
	}
	if toolMode == "functions" {
		req.Config.Tools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", Description: "Look up a value", ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}}}}}}
	} else if toolMode == "empty" {
		req.Config.Tools = []*genai.Tool{{}}
	}
	return req
}

func parallelJSON(t *testing.T, req any) map[string]any {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertParallelChoice(t *testing.T, payload map[string]any, tc parallelCase, toolMode string) {
	t.Helper()
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
	for _, key := range []string{common.ParallelToolCallsOption, "arbitrary_override"} {
		if _, ok := payload[key]; ok {
			t.Errorf("forwarded unsupported field %s", key)
		}
	}
}

func TestToAnthropicRequestParallelToolCalls(t *testing.T) {
	p := NewProvider("fixture", "claude-sonnet-4-6")
	for _, stream := range []bool{false, true} {
		for _, tc := range parallelCases() {
			for _, toolMode := range []string{"functions", "none", "empty"} {
				t.Run(fmt.Sprintf("stream=%v/%s/%s", stream, tc.name, toolMode), func(t *testing.T) {
					req := parallelRequest(toolMode)
					baseline, err := p.toAnthropicRequest(req, stream)
					if err != nil {
						t.Fatal(err)
					}
					if tc.present {
						req.Config.HTTPOptions.ExtraBody[common.ParallelToolCallsOption] = tc.value
					}
					before := parallelJSON(t, req)
					converted, err := p.toAnthropicRequest(req, stream)
					if !reflect.DeepEqual(before, parallelJSON(t, req)) {
						t.Fatal("source request mutated")
					}
					if tc.invalid {
						if err == nil || !strings.Contains(err.Error(), "parallel_tool_calls request option must be a boolean") {
							t.Fatalf("error = %v, want invalid-option error", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					payload := parallelJSON(t, converted)
					assertParallelChoice(t, payload, tc, toolMode)
					delete(payload, "tool_choice")
					if !reflect.DeepEqual(payload, parallelJSON(t, baseline)) {
						t.Fatalf("other request fields changed: %v", payload)
					}
					if payload["system"] != "system" || payload["max_tokens"] != float64(64000) {
						t.Fatal("lost request configuration")
					}
				})
			}
		}
	}
}

func TestAnthropicParallelToolCallsWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range parallelCases() {
			for _, toolMode := range []string{"functions", "none", "empty"} {
				t.Run(fmt.Sprintf("stream=%v/%s/%s", stream, tc.name, toolMode), func(t *testing.T) {
					var attempts atomic.Int32
					bodies := make(chan map[string]any, 2)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						attempts.Add(1)
						if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
							t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
						}
						var payload map[string]any
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						bodies <- payload
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, safetySSE(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, safetyStop))
						} else {
							_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":1}}`)
						}
					}))
					defer server.Close()
					target, err := url.Parse(server.URL)
					if err != nil {
						t.Fatal(err)
					}
					p := NewProvider("fixture", "claude-sonnet-4-6")
					p.client = &http.Client{Transport: safetyTransport{target: target, base: server.Client().Transport}}
					ordinary := parallelRequest(toolMode)
					req := ordinary
					if tc.present {
						req = common.WithParallelToolCalls(ordinary, false)
						req.Config.HTTPOptions.ExtraBody[common.ParallelToolCallsOption] = tc.value
					}
					before := parallelJSON(t, ordinary)
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
					assertParallelChoice(t, payload, tc, toolMode)
					if payload["model"] != p.model || payload["system"] != "system" || (payload["stream"] == true) != stream {
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
					assertParallelChoice(t, <-bodies, parallelCase{}, toolMode)
					if !reflect.DeepEqual(before, parallelJSON(t, ordinary)) {
						t.Fatal("ordinary request mutated")
					}
				})
			}
		}
	}
}
