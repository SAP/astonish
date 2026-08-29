package anthropic

import (
	"encoding/json"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestToolSerializationIsCanonical(t *testing.T) {
	provider := NewProvider("test", "test-model")
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "zeta", ParametersJsonSchema: map[string]any{}}}},
		{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "alpha",
			ParametersJsonSchema: map[string]any{
				"type":     "object",
				"required": []string{"z", "a"},
				"properties": map[string]any{
					"nested": map[string]any{"type": "object"},
				},
			},
		}}},
	}}}

	converted, err := provider.toAnthropicRequest(req, false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(converted.Tools)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"alpha","input_schema":{"properties":{"nested":{"properties":{},"type":"object"}},"required":["a","z"],"type":"object"}},{"name":"zeta","input_schema":{"properties":{},"type":"object"}}]`
	if string(data) != want {
		t.Fatalf("serialized tools mismatch\n got: %s\nwant: %s", data, want)
	}
}

func TestPatchOrphanedToolUse_NoOrphans(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: []Content{{Type: "text", Text: "hello"}}},
		{
			Role: "assistant",
			Content: []Content{
				{Type: "tool_use", ID: "t1", Name: "grep"},
			},
		},
		{
			Role: "user",
			Content: []Content{
				{Type: "tool_result", ToolUseID: "t1"},
			},
		},
	}

	result := patchOrphanedToolUse(messages)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
}

func TestPatchOrphanedToolUse_OrphanedWithFollowingUser(t *testing.T) {
	messages := []Message{
		{
			Role: "assistant",
			Content: []Content{
				{Type: "tool_use", ID: "t1", Name: "search"},
			},
		},
		{
			Role: "user",
			Content: []Content{
				{Type: "text", Text: "try something else"},
			},
		},
	}

	result := patchOrphanedToolUse(messages)

	// Synthetic tool_result should be merged into the user message
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (merged), got %d", len(result))
	}

	userContent := result[1].Content
	if len(userContent) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(userContent))
	}
	if userContent[0].Type != "tool_result" {
		t.Fatalf("expected first block 'tool_result', got %q", userContent[0].Type)
	}
	if userContent[0].ToolUseID != "t1" {
		t.Fatalf("expected ToolUseID 't1', got %q", userContent[0].ToolUseID)
	}
	if !userContent[0].IsError {
		t.Fatal("expected IsError to be true")
	}
	if userContent[1].Type != "text" {
		t.Fatalf("expected second block 'text', got %q", userContent[1].Type)
	}
}

func TestPatchOrphanedToolUse_OrphanedNoFollowing(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: []Content{{Type: "text", Text: "hello"}}},
		{
			Role: "assistant",
			Content: []Content{
				{Type: "tool_use", ID: "t1", Name: "exec"},
			},
		},
	}

	result := patchOrphanedToolUse(messages)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	synth := result[2]
	if synth.Role != "user" {
		t.Fatalf("expected 'user', got %q", synth.Role)
	}
	if len(synth.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(synth.Content))
	}
	if synth.Content[0].ToolUseID != "t1" {
		t.Fatalf("expected 't1', got %q", synth.Content[0].ToolUseID)
	}
	if !synth.Content[0].IsError {
		t.Fatal("expected IsError to be true")
	}
}

func TestPatchOrphanedToolUse_PartialOrphans(t *testing.T) {
	messages := []Message{
		{
			Role: "assistant",
			Content: []Content{
				{Type: "tool_use", ID: "t1", Name: "grep"},
				{Type: "tool_use", ID: "t2", Name: "exec"},
			},
		},
		{
			Role: "user",
			Content: []Content{
				{Type: "tool_result", ToolUseID: "t1"},
			},
		},
	}

	result := patchOrphanedToolUse(messages)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	// t2 result should be prepended to user message
	userContent := result[1].Content
	if len(userContent) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(userContent))
	}
	if userContent[0].ToolUseID != "t2" {
		t.Fatalf("expected 't2', got %q", userContent[0].ToolUseID)
	}
	if userContent[1].ToolUseID != "t1" {
		t.Fatalf("expected 't1', got %q", userContent[1].ToolUseID)
	}
}

func TestPatchOrphanedToolUse_TextOnlyAssistant(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: []Content{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []Content{{Type: "text", Text: "hello"}}},
	}

	result := patchOrphanedToolUse(messages)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages unchanged, got %d", len(result))
	}
}

func TestPatchOrphanedToolUse_MultipleOrphanedTools(t *testing.T) {
	messages := []Message{
		{
			Role: "assistant",
			Content: []Content{
				{Type: "tool_use", ID: "t1", Name: "a"},
				{Type: "tool_use", ID: "t2", Name: "b"},
				{Type: "tool_use", ID: "t3", Name: "c"},
			},
		},
		// No following message at all
	}

	result := patchOrphanedToolUse(messages)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	synth := result[1]
	if len(synth.Content) != 3 {
		t.Fatalf("expected 3 synthetic results, got %d", len(synth.Content))
	}
	for i, id := range []string{"t1", "t2", "t3"} {
		if synth.Content[i].ToolUseID != id {
			t.Errorf("block %d: expected %q, got %q", i, id, synth.Content[i].ToolUseID)
		}
	}
}
