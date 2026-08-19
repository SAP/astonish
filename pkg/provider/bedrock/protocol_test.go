package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestPatchOrphanedToolUse_NoOrphans(t *testing.T) {
	// Assistant message with tool_use followed by user message with tool_result
	req := &Request{
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ID: "tool_1", Name: "grep"},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "tool_1", Content: `{"output":"found"}`},
				},
			},
		},
	}

	patchOrphanedToolUse(req)

	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}
}

func TestPatchOrphanedToolUse_OrphanedWithFollowingUserText(t *testing.T) {
	// Assistant has tool_use but next user message is plain text (no tool_result)
	req := &Request{
		Messages: []Message{
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ID: "tool_1", Name: "grep"},
				},
			},
			{Role: "user", Content: "do it differently"},
		},
	}

	patchOrphanedToolUse(req)

	// A synthetic tool_result message should be inserted between assistant and user
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages (assistant + synthetic tool_result + user), got %d", len(req.Messages))
	}

	// The synthetic message should be at index 1 (user role with tool_result)
	synth := req.Messages[1]
	if synth.Role != "user" {
		t.Fatalf("expected synthetic message role 'user', got %q", synth.Role)
	}
	blocks, ok := synth.Content.([]ContentBlock)
	if !ok {
		t.Fatal("expected synthetic message content to be []ContentBlock")
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 synthetic block, got %d", len(blocks))
	}
	if blocks[0].Type != "tool_result" {
		t.Fatalf("expected 'tool_result', got %q", blocks[0].Type)
	}
	if blocks[0].ToolUseID != "tool_1" {
		t.Fatalf("expected ToolUseID 'tool_1', got %q", blocks[0].ToolUseID)
	}
}

func TestPatchOrphanedToolUse_OrphanedNoFollowingMessage(t *testing.T) {
	// Assistant has tool_use but it's the last message (no following message at all)
	req := &Request{
		Messages: []Message{
			{Role: "user", Content: "search for something"},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ID: "tool_1", Name: "search"},
				},
			},
		},
	}

	patchOrphanedToolUse(req)

	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}

	synth := req.Messages[2]
	if synth.Role != "user" {
		t.Fatalf("expected 'user', got %q", synth.Role)
	}
	blocks, ok := synth.Content.([]ContentBlock)
	if !ok {
		t.Fatal("expected []ContentBlock")
	}
	if blocks[0].ToolUseID != "tool_1" {
		t.Fatalf("expected 'tool_1', got %q", blocks[0].ToolUseID)
	}
}

func TestPatchOrphanedToolUse_PartialOrphans(t *testing.T) {
	// Assistant has 2 tool_use, but only 1 has a tool_result
	req := &Request{
		Messages: []Message{
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ID: "tool_1", Name: "grep"},
					{Type: "tool_use", ID: "tool_2", Name: "search"},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "tool_1", Content: `{"ok":true}`},
				},
			},
		},
	}

	patchOrphanedToolUse(req)

	// tool_2 is orphaned; synthetic result should be merged into the user message
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages (merged), got %d", len(req.Messages))
	}

	blocks, ok := req.Messages[1].Content.([]ContentBlock)
	if !ok {
		t.Fatal("expected []ContentBlock")
	}
	// Synthetic result for tool_2 should be prepended
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (synthetic + existing), got %d", len(blocks))
	}
	if blocks[0].ToolUseID != "tool_2" {
		t.Fatalf("expected first block ToolUseID 'tool_2', got %q", blocks[0].ToolUseID)
	}
	if blocks[1].ToolUseID != "tool_1" {
		t.Fatalf("expected second block ToolUseID 'tool_1', got %q", blocks[1].ToolUseID)
	}
}

func TestPatchOrphanedToolUse_MultipleAssistantTurns(t *testing.T) {
	// Two assistant turns, first is properly paired, second is orphaned
	req := &Request{
		Messages: []Message{
			{Role: "user", Content: "first request"},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "grep"},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: `{}`},
				},
			},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "ok"}}},
			{Role: "user", Content: "second request"},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ID: "t2", Name: "search"},
				},
			},
			// No tool_result for t2
			{Role: "user", Content: "try again"},
		},
	}

	patchOrphanedToolUse(req)

	// t2 is orphaned; synthetic should be inserted between assistant[5] and user[6]
	if len(req.Messages) != 8 {
		t.Fatalf("expected 8 messages, got %d", len(req.Messages))
	}

	// The synthetic message should be at index 6
	synth := req.Messages[6]
	if synth.Role != "user" {
		t.Fatalf("expected synthetic at index 6 to be 'user', got %q", synth.Role)
	}
	blocks, ok := synth.Content.([]ContentBlock)
	if !ok {
		t.Fatal("expected []ContentBlock")
	}
	if blocks[0].ToolUseID != "t2" {
		t.Fatalf("expected 't2', got %q", blocks[0].ToolUseID)
	}
}

func TestPatchOrphanedToolUse_PlainTextAssistant(t *testing.T) {
	// Assistant message with only text (no tool_use) -- should be left alone
	req := &Request{
		Messages: []Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}

	patchOrphanedToolUse(req)

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
}

func TestPatchOrphanedToolUse_StringContentAssistant(t *testing.T) {
	// Assistant message with string content (not []ContentBlock) -- should be skipped
	req := &Request{
		Messages: []Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "just text"},
		},
	}

	patchOrphanedToolUse(req)

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
}

func TestConvertRequest_TemperatureIncluded(t *testing.T) {
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  "user",
				Parts: []*genai.Part{{Text: "hello"}},
			},
		},
	}

	bedrockReq, err := ConvertRequest(req, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(bedrockReq)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"temperature"`) {
		t.Fatalf("expected temperature field in JSON, got: %s", jsonStr)
	}

	if bedrockReq.Temperature == nil {
		t.Fatal("expected Temperature to be non-nil")
	}
	if *bedrockReq.Temperature != 0.7 {
		t.Fatalf("expected temperature 0.7, got %f", *bedrockReq.Temperature)
	}
}

func TestConvertRequest_TemperatureOmitted(t *testing.T) {
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role:  "user",
				Parts: []*genai.Part{{Text: "hello"}},
			},
		},
	}

	bedrockReq, err := ConvertRequest(req, 0, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(bedrockReq)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, `"temperature"`) {
		t.Fatalf("expected temperature field to be absent in JSON, got: %s", jsonStr)
	}

	if bedrockReq.Temperature != nil {
		t.Fatal("expected Temperature to be nil")
	}
}
