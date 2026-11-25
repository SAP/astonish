package api

import (
	"google.golang.org/genai"
	"strings"
	"testing"
)

func TestBotFileFirstHistoryReferencesOnly(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		body := map[string]any{"content": "OLD_DELIVERABLE_BODY", "entry": map[string]any{"path": "outputs/design.md", "sha256": strings.Repeat("a", 64)}, "offset": 0, "next_offset": 20}
		root := body
		if wrapped {
			root = map[string]any{"output": body}
		}
		got := projectBotHomeReadResponse(&genai.FunctionResponse{Name: "read_file", Response: root})
		if got == nil {
			t.Fatal("missing reference projection")
		}
		result := got
		if wrapped {
			result = got["output"].(map[string]any)
		}
		if _, ok := result["content"]; ok {
			t.Fatal("historical body retained")
		}
		if result["entry"] == nil || result["_file_history_reference"] == nil {
			t.Fatal("lost file reference")
		}
		if body["content"] != "OLD_DELIVERABLE_BODY" {
			t.Fatal("mutated persisted transcript")
		}
	}
	if got := projectBotHomeReadResponse(&genai.FunctionResponse{Name: "read_file", Response: map[string]any{"content": "diagnostic", "error": "failed"}}); got != nil {
		t.Fatal("projected error")
	}
	if got := projectBotHomeReadResponse(&genai.FunctionResponse{Name: "read_file", Response: map[string]any{"content": "unknown output"}}); got != nil {
		t.Fatal("guessed unknown schema")
	}
}
