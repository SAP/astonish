package bedrock

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestConvertRequestToolSerializationIsCanonical(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	names := []string{"charlie", "alpha", "bravo"}
	var want []byte
	for iteration := 0; iteration < 50; iteration++ {
		var tools []*genai.Tool
		for _, index := range rng.Perm(len(names)) {
			properties := make(map[string]any)
			keys := []string{"zeta", "alpha"}
			for _, propertyIndex := range rng.Perm(len(keys)) {
				properties[keys[propertyIndex]] = map[string]any{"type": "string"}
			}
			tools = append(tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: names[index],
				ParametersJsonSchema: map[string]any{
					"type":       "object",
					"required":   []string{"zeta", "alpha"},
					"properties": properties,
				},
			}}})
		}
		converted, err := ConvertRequest(&model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: tools}}, 100, false)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(converted)
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 {
			want = data
		} else if string(data) != string(want) {
			t.Fatalf("converted request %d differs\n got: %s\nwant: %s", iteration, data, want)
		}
	}
}

func TestParseResponseMapsAnthropicCacheUsage(t *testing.T) {
	body := []byte(`{"content":[],"usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":70}}`)
	resp, err := ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	usage := resp.UsageMetadata
	if usage == nil {
		t.Fatal("UsageMetadata is nil")
	}
	if usage.PromptTokenCount != 200 || usage.CandidatesTokenCount != 20 || usage.TotalTokenCount != 220 || usage.CachedContentTokenCount != 70 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestParseResponseMapsSAPBedrockCacheUsage(t *testing.T) {
	body := []byte(`{"content":[],"usage":{"input_tokens":100,"output_tokens":20,"cacheWriteInputTokens":30,"cacheReadInputTokens":70}}`)
	resp, err := ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	usage := resp.UsageMetadata
	if usage == nil {
		t.Fatal("UsageMetadata is nil")
	}
	if usage.PromptTokenCount != 200 || usage.CandidatesTokenCount != 20 || usage.TotalTokenCount != 220 || usage.CachedContentTokenCount != 70 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestParseResponseDistinguishesAbsentAndZeroCacheUsage(t *testing.T) {
	absent, err := ParseResponse([]byte(`{"content":[],"usage":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if absent.UsageMetadata != nil {
		t.Fatalf("absent usage = %#v, want nil", absent.UsageMetadata)
	}

	present, err := ParseResponse([]byte(`{"content":[],"usage":{"cacheReadInputTokens":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if present.UsageMetadata == nil {
		t.Fatal("explicit zero cache usage should produce metadata")
	}
}

func TestParseStreamMapsSAPBedrockCacheUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0,"cacheWriteInputTokens":3,"cacheReadInputTokens":7}}}`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":2}}`,
		"",
	}, "\n")
	var final *model.LLMResponse
	for resp, err := range ParseStream(strings.NewReader(stream)) {
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Partial {
			final = resp
		}
	}
	if final == nil || final.UsageMetadata == nil {
		t.Fatal("final response has no usage metadata")
	}
	usage := final.UsageMetadata
	if usage.PromptTokenCount != 20 || usage.CandidatesTokenCount != 2 || usage.TotalTokenCount != 22 || usage.CachedContentTokenCount != 7 {
		t.Fatalf("usage = %#v", usage)
	}
}

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
