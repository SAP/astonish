package common

import (
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestParallelToolCallsConfig(t *testing.T) {
	for _, req := range []*model.LLMRequest{nil, {}, {Config: &genai.GenerateContentConfig{}}, {Config: &genai.GenerateContentConfig{MaxOutputTokens: 42, HTTPOptions: &genai.HTTPOptions{Headers: http.Header{"X-Test": {"kept"}}, ExtraBody: map[string]any{"other": "kept", ParallelToolCallsOption: true}}}}} {
		before, _ := json.Marshal(req)
		out := WithParallelToolCalls(req, false)
		got, err := ParallelToolCalls(out)
		if err != nil || got == nil || *got {
			t.Fatalf("false missing: %v %v", got, err)
		}
		if req != nil && req.Config != nil && req.Config.HTTPOptions != nil {
			if out.Config.MaxOutputTokens != 42 || out.Config.HTTPOptions.Headers.Get("X-Test") != "kept" || out.Config.HTTPOptions.ExtraBody["other"] != "kept" {
				t.Fatal("unrelated config lost")
			}
		}
		stripped := WithoutParallelToolCalls(out)
		if got, err := ParallelToolCalls(stripped); got != nil || err != nil {
			t.Fatalf("native config not stripped: %v %v", got, err)
		}
		if got, _ := ParallelToolCalls(out); got == nil || *got {
			t.Fatal("strip mutated source")
		}
		after, _ := json.Marshal(req)
		if string(before) != string(after) {
			t.Fatal("source mutated")
		}
	}
	for _, value := range []any{nil, "false", 0, map[string]any{}} {
		req := &model.LLMRequest{Config: &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{ExtraBody: map[string]any{ParallelToolCallsOption: value}}}}
		if _, err := ParallelToolCalls(req); err == nil {
			t.Fatalf("accepted malformed value %T", value)
		}
	}
	for _, req := range []*model.LLMRequest{nil, {}, {Config: &genai.GenerateContentConfig{}}, {Config: &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{}}}} {
		if got, err := ParallelToolCalls(req); err != nil || got != nil {
			t.Fatal("absent extension changed defaults")
		}
		if WithoutParallelToolCalls(req) != req {
			t.Fatal("absent extension cloned native request")
		}
	}
}
