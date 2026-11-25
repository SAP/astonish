package api_test

import (
	"reflect"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// scriptedHTTPProgressiveResponse adapts scripted producer calls to the catalog
// advertised for this request. It emits a real execute_tool call, never a tool
// response, so authorization, accounting, execution and errors remain real.
func scriptedHTTPProgressiveResponse(req *model.LLMRequest, response *model.LLMResponse) *model.LLMResponse {
	if req == nil || req.Config == nil || response == nil || response.Content == nil {
		return response
	}
	declared := map[string]bool{}
	for _, tools := range req.Config.Tools {
		for _, declaration := range tools.FunctionDeclarations {
			declared[declaration.Name] = true
		}
	}
	if !declared["execute_tool"] {
		return response
	}
	cloned := *response
	content := *response.Content
	content.Parts = append([]*genai.Part(nil), content.Parts...)
	cloned.Content = &content
	for i, part := range content.Parts {
		if part == nil || part.FunctionCall == nil || declared[part.FunctionCall.Name] {
			continue
		}
		call := *part.FunctionCall
		call.Args = map[string]any{"name": call.Name, "arguments": call.Args}
		call.Name = "execute_tool"
		copyPart := *part
		copyPart.FunctionCall = &call
		content.Parts[i] = &copyPart
	}
	return &cloned
}

func TestScriptedHTTPProgressiveResponse(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		tools  []string
		bridge bool
	}{
		{"deferred", []string{"execute_tool", "read_file"}, true},
		{"declared", []string{"execute_tool", "bot_work_update"}, false},
		{"tool-free-review", nil, false},
		{"missing-bridge", []string{"read_file"}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			req := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
			for _, name := range scenario.tools {
				req.Config.Tools = append(req.Config.Tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: name}}})
			}
			args := map[string]any{"expected_revision": 1, "kind": "replace_notes", "notes": "real note"}
			call := &genai.FunctionCall{ID: "stable-call", Name: "bot_work_update", Args: args}
			response := &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}, {Text: "unchanged text"}}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5}}
			got := scriptedHTTPProgressiveResponse(req, response)
			actual := got.Content.Parts[0].FunctionCall
			if actual.ID != call.ID || got.UsageMetadata != response.UsageMetadata || got.Content.Parts[1].Text != "unchanged text" || got.Content.Parts[0].FunctionResponse != nil {
				t.Fatalf("changed call identity, usage, text or fabricated response: %+v", got)
			}
			if scenario.bridge {
				if actual.Name != "execute_tool" || actual.Args["name"] != call.Name || !reflect.DeepEqual(actual.Args["arguments"], args) {
					t.Fatalf("bridge lost target/arguments: %+v", actual)
				}
			} else if !reflect.DeepEqual(actual, call) {
				t.Fatalf("declared/tool-free call changed: %+v", actual)
			}
			if call.Name != "bot_work_update" || !reflect.DeepEqual(call.Args, args) {
				t.Fatal("adapter mutated original script")
			}
			if again := scriptedHTTPProgressiveResponse(req, got); !reflect.DeepEqual(again, got) {
				t.Fatal("adapter nested an existing bridge")
			}
		})
	}
}
