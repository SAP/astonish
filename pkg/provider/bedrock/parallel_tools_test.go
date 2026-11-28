package bedrock

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/common"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestConvertRequestParallelToolCalls(t *testing.T) {
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
			t.Run(tc.name+"/"+toolMode, func(t *testing.T) {
				extra := map[string]any{"arbitrary_override": "not forwarded", "tool_choice": map[string]any{"type": "any"}}
				req := &model.LLMRequest{
					Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "hello"}}}},
					Config: &genai.GenerateContentConfig{
						SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "system"}}},
						HTTPOptions:       &genai.HTTPOptions{ExtraBody: extra},
					},
				}
				if toolMode == "functions" {
					req.Config.Tools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", Description: "Look up a value", ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}}}}}}
				} else if toolMode == "empty" {
					req.Config.Tools = []*genai.Tool{{}}
				}
				baseline, err := ConvertRequest(req, 64000, true)
				if err != nil {
					t.Fatal(err)
				}
				if tc.present {
					extra[common.ParallelToolCallsOption] = tc.value
				}
				before, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				converted, err := ConvertRequest(req, 64000, true)
				after, marshalErr := json.Marshal(req)
				if marshalErr != nil || string(before) != string(after) {
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
				payload := parallelRequestJSON(t, converted)
				wantChoice := tc.present && toolMode == "functions"
				choice, exists := payload["tool_choice"]
				if exists != wantChoice {
					t.Fatalf("tool_choice present = %v, want %v: %v", exists, wantChoice, payload)
				}
				if wantChoice {
					want := map[string]any{"type": "auto", "disable_parallel_tool_use": !tc.value.(bool)}
					if !reflect.DeepEqual(choice, want) {
						t.Fatalf("tool_choice = %v, want %v", choice, want)
					}
				}
				for _, key := range []string{common.ParallelToolCallsOption, "arbitrary_override"} {
					if _, ok := payload[key]; ok {
						t.Fatalf("forwarded unsupported field %s", key)
					}
				}
				delete(payload, "tool_choice")
				if !reflect.DeepEqual(payload, parallelRequestJSON(t, baseline)) {
					t.Fatalf("other request fields changed: %v", payload)
				}
				if payload["system"] != "system" || payload["max_tokens"] != float64(64000) {
					t.Fatal("lost request configuration")
				}
				if _, ok := payload["temperature"]; ok {
					t.Fatal("temperature added to no-temperature request")
				}
			})
		}
	}
}

func parallelRequestJSON(t *testing.T, req any) map[string]any {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(fmt.Errorf("decode request: %w", err))
	}
	return out
}
