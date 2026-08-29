package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestUsageMetadataMapsCachedPromptTokens(t *testing.T) {
	usage := usageMetadata(100, 20, 120, &openailib.PromptTokensDetails{CachedTokens: 75})
	if usage.CachedContentTokenCount != 75 {
		t.Fatalf("CachedContentTokenCount = %d, want 75", usage.CachedContentTokenCount)
	}
	if usage.PromptTokenCount != 100 || usage.CandidatesTokenCount != 20 || usage.TotalTokenCount != 120 {
		t.Fatalf("unexpected usage metadata: %+v", usage)
	}
}

func TestMergeConsecutiveSameRole(t *testing.T) {
	tests := []struct {
		name     string
		input    []openailib.ChatCompletionMessage
		expected []openailib.ChatCompletionMessage
	}{
		{
			name:     "empty",
			input:    nil,
			expected: nil,
		},
		{
			name: "single message unchanged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
			},
		},
		{
			name: "alternating roles unchanged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "hi"},
				{Role: openailib.ChatMessageRoleUser, Content: "how are you"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "hi"},
				{Role: openailib.ChatMessageRoleUser, Content: "how are you"},
			},
		},
		{
			name: "consecutive user messages merged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "ready now"},
				{Role: openailib.ChatMessageRoleUser, Content: "continue"},
				{Role: openailib.ChatMessageRoleUser, Content: "please respond"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "ready now\ncontinue\nplease respond"},
			},
		},
		{
			name: "consecutive user messages between assistant responses",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleAssistant, Content: "ok"},
				{Role: openailib.ChatMessageRoleUser, Content: "msg1"},
				{Role: openailib.ChatMessageRoleUser, Content: "msg2"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "got it"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleAssistant, Content: "ok"},
				{Role: openailib.ChatMessageRoleUser, Content: "msg1\nmsg2"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "got it"},
			},
		},
		{
			name: "tool messages not merged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleTool, Content: "result1", ToolCallID: "call_1"},
				{Role: openailib.ChatMessageRoleTool, Content: "result2", ToolCallID: "call_2"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleTool, Content: "result1", ToolCallID: "call_1"},
				{Role: openailib.ChatMessageRoleTool, Content: "result2", ToolCallID: "call_2"},
			},
		},
		{
			name: "assistant tool_calls then text merged (Case 3)",
			input: []openailib.ChatCompletionMessage{
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn1"}},
					},
				},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "done",
				},
			},
			expected: []openailib.ChatCompletionMessage{
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "done",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn1"}},
					},
				},
			},
		},
		{
			name: "assistant text then tool_calls merged (Case 2 - streaming fix)",
			input: []openailib.ChatCompletionMessage{
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "Let me check that for you.",
				},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_abc123", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: `{"command":"free -h"}`}},
					},
				},
			},
			expected: []openailib.ChatCompletionMessage{
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "Let me check that for you.",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_abc123", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: `{"command":"free -h"}`}},
					},
				},
			},
		},
		{
			name: "full streaming tool call round-trip",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleSystem, Content: "You are an assistant."},
				{Role: openailib.ChatMessageRoleUser, Content: "Check memory"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "I'll check the memory usage."},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "",
					ToolCalls: []openailib.ToolCall{
						{ID: "resolve_credential:0", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "resolve_credential", Arguments: `{"name":"proxmox-ssh"}`}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, Content: `{"password":"secret"}`, ToolCallID: "resolve_credential:0"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleSystem, Content: "You are an assistant."},
				{Role: openailib.ChatMessageRoleUser, Content: "Check memory"},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "I'll check the memory usage.",
					ToolCalls: []openailib.ToolCall{
						{ID: "resolve_credential:0", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "resolve_credential", Arguments: `{"name":"proxmox-ssh"}`}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, Content: `{"password":"secret"}`, ToolCallID: "resolve_credential:0"},
			},
		},
		{
			name: "two assistant tool_calls messages not merged",
			input: []openailib.ChatCompletionMessage{
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn1"}},
					},
				},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn2"}},
					},
				},
			},
			expected: []openailib.ChatCompletionMessage{
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn1"}},
					},
				},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn2"}},
					},
				},
			},
		},
		{
			name: "three consecutive assistant: text + text + tool_calls all merged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleAssistant, Content: "Thinking..."},
				{Role: openailib.ChatMessageRoleAssistant, Content: "Let me check."},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn1"}},
					},
				},
			},
			expected: []openailib.ChatCompletionMessage{
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "Thinking...\nLet me check.",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "fn1"}},
					},
				},
			},
		},
		{
			name: "consecutive assistant text messages merged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleAssistant, Content: "thinking..."},
				{Role: openailib.ChatMessageRoleAssistant, Content: "here is my answer"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleAssistant, Content: "thinking...\nhere is my answer"},
			},
		},
		{
			name: "system messages not merged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleSystem, Content: "system1"},
				{Role: openailib.ChatMessageRoleSystem, Content: "system2"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleSystem, Content: "system1"},
				{Role: openailib.ChatMessageRoleSystem, Content: "system2"},
			},
		},
		{
			name: "empty content in consecutive messages",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: ""},
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
			},
			expected: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeConsecutiveSameRole(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d messages, got %d", len(tt.expected), len(result))
				return
			}
			for i := range result {
				if result[i].Role != tt.expected[i].Role {
					t.Errorf("message[%d]: expected role %q, got %q", i, tt.expected[i].Role, result[i].Role)
				}
				if result[i].Content != tt.expected[i].Content {
					t.Errorf("message[%d]: expected content %q, got %q", i, tt.expected[i].Content, result[i].Content)
				}
				if result[i].ToolCallID != tt.expected[i].ToolCallID {
					t.Errorf("message[%d]: expected tool_call_id %q, got %q", i, tt.expected[i].ToolCallID, result[i].ToolCallID)
				}
				if len(result[i].ToolCalls) != len(tt.expected[i].ToolCalls) {
					t.Errorf("message[%d]: expected %d tool_calls, got %d", i, len(tt.expected[i].ToolCalls), len(result[i].ToolCalls))
				}
			}
		})
	}
}

func TestEnsureNotEndingWithTool(t *testing.T) {
	tests := []struct {
		name        string
		input       []openailib.ChatCompletionMessage
		expectLen   int
		expectLast  string // expected role of last message
		expectExtra bool   // whether a synthetic user message was appended
	}{
		{
			name:      "empty messages unchanged",
			input:     nil,
			expectLen: 0,
		},
		{
			name: "ending with user unchanged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
			},
			expectLen:  1,
			expectLast: openailib.ChatMessageRoleUser,
		},
		{
			name: "ending with assistant unchanged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "hi there"},
			},
			expectLen:  2,
			expectLast: openailib.ChatMessageRoleAssistant,
		},
		{
			name: "ending with tool gets user appended",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "do something"},
				{Role: openailib.ChatMessageRoleAssistant, ToolCalls: []openailib.ToolCall{
					{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "my_tool", Arguments: "{}"}},
				}},
				{Role: openailib.ChatMessageRoleTool, Content: "result", ToolCallID: "call_1"},
			},
			expectLen:   4,
			expectLast:  openailib.ChatMessageRoleUser,
			expectExtra: true,
		},
		{
			name: "ending with tool after multiple tool results",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "do two things"},
				{Role: openailib.ChatMessageRoleAssistant, ToolCalls: []openailib.ToolCall{
					{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool_a", Arguments: "{}"}},
					{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool_b", Arguments: "{}"}},
				}},
				{Role: openailib.ChatMessageRoleTool, Content: "result_a", ToolCallID: "call_1"},
				{Role: openailib.ChatMessageRoleTool, Content: "result_b", ToolCallID: "call_2"},
			},
			expectLen:   5,
			expectLast:  openailib.ChatMessageRoleUser,
			expectExtra: true,
		},
		{
			name: "tool followed by user already present - unchanged",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "do something"},
				{Role: openailib.ChatMessageRoleAssistant, ToolCalls: []openailib.ToolCall{
					{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "my_tool", Arguments: "{}"}},
				}},
				{Role: openailib.ChatMessageRoleTool, Content: "result", ToolCallID: "call_1"},
				{Role: openailib.ChatMessageRoleUser, Content: "now what?"},
			},
			expectLen:  4,
			expectLast: openailib.ChatMessageRoleUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureNotEndingWithTool(tt.input)
			if len(result) != tt.expectLen {
				t.Fatalf("expected %d messages, got %d", tt.expectLen, len(result))
			}
			if tt.expectLen > 0 {
				last := result[len(result)-1]
				if last.Role != tt.expectLast {
					t.Errorf("expected last role %q, got %q", tt.expectLast, last.Role)
				}
				if tt.expectExtra && last.Content == "" {
					t.Error("expected synthetic user message to have non-empty content")
				}
			}
		})
	}
}

func TestPatchOrphanedToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		input     []openailib.ChatCompletionMessage
		expectLen int
		// expectOrphanPatched: the tool_call_id that should have a synthetic response
		expectOrphanPatched []string
	}{
		{
			name:      "no messages",
			input:     nil,
			expectLen: 0,
		},
		{
			name: "no orphans - all tool_calls have responses",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "ok"},
			},
			expectLen:           3,
			expectOrphanPatched: nil,
		},
		{
			name: "orphan at end - no tool response",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "browser_snapshot", Arguments: "{}"}},
					},
				},
			},
			expectLen:           3, // original 2 + 1 synthetic tool response
			expectOrphanPatched: []string{"call_1"},
		},
		{
			name: "multiple orphans in one assistant message",
			input: []openailib.ChatCompletionMessage{
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_a", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool1", Arguments: "{}"}},
						{ID: "call_b", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool2", Arguments: "{}"}},
						{ID: "call_c", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool3", Arguments: "{}"}},
					},
				},
			},
			expectLen:           4, // 1 assistant + 3 synthetic
			expectOrphanPatched: []string{"call_a", "call_b", "call_c"},
		},
		{
			name: "partial orphan - one answered one not",
			input: []openailib.ChatCompletionMessage{
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool1", Arguments: "{}"}},
						{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "tool2", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "ok"},
			},
			expectLen:           3, // assistant + synthetic for call_2 + existing tool for call_1
			expectOrphanPatched: []string{"call_2"},
		},
		{
			name: "orphan in middle of conversation",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "do something"},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_old", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "hung_tool", Arguments: "{}"}},
					},
				},
				// No tool response — then user sent another message
				{Role: openailib.ChatMessageRoleUser, Content: "?"},
			},
			expectLen:           4, // user + assistant + synthetic tool + user
			expectOrphanPatched: []string{"call_old"},
		},
		{
			name: "no tool_calls in assistant - untouched",
			input: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "hi there"},
			},
			expectLen:           2,
			expectOrphanPatched: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := patchOrphanedToolCalls(tt.input)
			if len(result) != tt.expectLen {
				t.Fatalf("expected %d messages, got %d", tt.expectLen, len(result))
			}

			// Verify that the expected orphan IDs now have tool responses
			for _, expectedID := range tt.expectOrphanPatched {
				found := false
				for _, msg := range result {
					if msg.Role == openailib.ChatMessageRoleTool && msg.ToolCallID == expectedID {
						found = true
						if msg.Content == "" {
							t.Errorf("synthetic tool response for %q should have content", expectedID)
						}
						break
					}
				}
				if !found {
					t.Errorf("expected synthetic tool response for orphan %q, not found", expectedID)
				}
			}
		})
	}
}

func TestConfigMaxOutputTokensAndTemperatureOverride(t *testing.T) {
	tests := []struct {
		name                   string
		providerMaxTokens      int
		configMaxOutputTokens  int32
		configTemperature      *float32
		expectMaxTokens        int
		expectTemperatureSet   bool
		expectTemperatureValue float32
	}{
		{
			name:                  "per-request MaxOutputTokens overrides provider default",
			providerMaxTokens:     64000,
			configMaxOutputTokens: 100,
			expectMaxTokens:       100,
		},
		{
			name:                   "per-request Temperature is applied",
			providerMaxTokens:      64000,
			configTemperature:      genai.Ptr(float32(0.3)),
			expectMaxTokens:        64000, // unchanged
			expectTemperatureSet:   true,
			expectTemperatureValue: 0.3,
		},
		{
			name:                   "both overrides applied together",
			providerMaxTokens:      64000,
			configMaxOutputTokens:  100,
			configTemperature:      genai.Ptr(float32(0.3)),
			expectMaxTokens:        100,
			expectTemperatureSet:   true,
			expectTemperatureValue: 0.3,
		},
		{
			name:              "zero MaxOutputTokens does not override",
			providerMaxTokens: 64000,
			expectMaxTokens:   64000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody map[string]any

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &capturedBody)

				w.Header().Set("Content-Type", "application/json")
				// Minimal valid non-streaming response
				_, _ = w.Write([]byte(`{
					"id": "test",
					"object": "chat.completion",
					"choices": [{
						"index": 0,
						"message": {"role": "assistant", "content": "hello"},
						"finish_reason": "stop"
					}],
					"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
				}`))
			}))
			defer srv.Close()

			config := openailib.DefaultConfig("test-key")
			config.BaseURL = srv.URL + "/v1"
			client := openailib.NewClientWithConfig(config)

			p := NewProviderWithMaxTokens(client, "test-model", true, tt.providerMaxTokens)

			req := &model.LLMRequest{
				Contents: []*genai.Content{
					genai.NewContentFromText("test", genai.RoleUser),
				},
				Config: &genai.GenerateContentConfig{},
			}
			if tt.configMaxOutputTokens > 0 {
				req.Config.MaxOutputTokens = tt.configMaxOutputTokens
			}
			if tt.configTemperature != nil {
				req.Config.Temperature = tt.configTemperature
			}

			for _, err := range p.GenerateContent(context.Background(), req, false) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			// Verify max_completion_tokens
			if maxTokens, ok := capturedBody["max_completion_tokens"].(float64); ok {
				if int(maxTokens) != tt.expectMaxTokens {
					t.Errorf("max_completion_tokens: got %d, want %d", int(maxTokens), tt.expectMaxTokens)
				}
			} else if tt.expectMaxTokens > 0 {
				t.Errorf("max_completion_tokens not found in request body")
			}

			// Verify temperature
			if tt.expectTemperatureSet {
				if temp, ok := capturedBody["temperature"].(float64); ok {
					if float32(temp) != tt.expectTemperatureValue {
						t.Errorf("temperature: got %f, want %f", temp, tt.expectTemperatureValue)
					}
				} else {
					t.Errorf("temperature not found in request body")
				}
			}
		})
	}
}

func TestToOpenAIMessages_ReasoningContent(t *testing.T) {
	p := &Provider{model: "test"}

	// Build a request with an assistant message containing Thought parts
	// followed by tool calls and their responses. This simulates the
	// conversation history that DeepSeek V4 produces.
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			// User message
			{
				Role:  "user",
				Parts: []*genai.Part{{Text: "Check my memory usage"}},
			},
			// Assistant response with reasoning + tool calls
			{
				Role: "model",
				Parts: []*genai.Part{
					{Text: "Let me think about this...", Thought: true},
					{Text: "I'll check that for you."},
					{FunctionCall: &genai.FunctionCall{
						Name: "shell_command",
						Args: map[string]any{"command": "free -m"},
						ID:   "call_1",
					}},
				},
			},
			// Tool response
			{
				Role: "user",
				Parts: []*genai.Part{
					{FunctionResponse: &genai.FunctionResponse{
						Name:     "shell_command",
						Response: map[string]any{"result": "Mem: 16384 8192 8192"},
						ID:       "call_1",
					}},
				},
			},
		},
		Config: &genai.GenerateContentConfig{},
	}

	messages := p.toOpenAIMessages(req)

	// Find the assistant message
	var assistantMsg *openailib.ChatCompletionMessage
	for i := range messages {
		if messages[i].Role == openailib.ChatMessageRoleAssistant {
			assistantMsg = &messages[i]
			break
		}
	}

	if assistantMsg == nil {
		t.Fatal("expected an assistant message")
	}

	// ReasoningContent should contain the thought text
	if assistantMsg.ReasoningContent != "Let me think about this..." {
		t.Errorf("ReasoningContent: got %q, want %q", assistantMsg.ReasoningContent, "Let me think about this...")
	}

	// Content should contain only the non-thought text
	if assistantMsg.Content != "I'll check that for you." {
		t.Errorf("Content: got %q, want %q", assistantMsg.Content, "I'll check that for you.")
	}

	// Should have the tool call
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistantMsg.ToolCalls))
	}
	if assistantMsg.ToolCalls[0].Function.Name != "shell_command" {
		t.Errorf("tool call name: got %q, want %q", assistantMsg.ToolCalls[0].Function.Name, "shell_command")
	}
}

func TestToOpenAIMessages_ThoughtPartsOnlyForAssistant(t *testing.T) {
	p := &Provider{model: "test"}

	// User messages with Thought=true should NOT be split into ReasoningContent
	// (only assistant messages use reasoning_content in the DeepSeek API)
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					{Text: "Some thought", Thought: true},
					{Text: "Regular text"},
				},
			},
		},
		Config: &genai.GenerateContentConfig{},
	}

	messages := p.toOpenAIMessages(req)

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	// For user messages, all text should go into Content (Thought flag ignored)
	if messages[0].Content != "Some thoughtRegular text" {
		t.Errorf("Content: got %q, want %q", messages[0].Content, "Some thoughtRegular text")
	}
	if messages[0].ReasoningContent != "" {
		t.Errorf("ReasoningContent should be empty for user messages, got %q", messages[0].ReasoningContent)
	}
}

func TestToLLMResponse_ReasoningContent(t *testing.T) {
	p := &Provider{model: "test"}

	resp := openailib.ChatCompletionResponse{
		Choices: []openailib.ChatCompletionChoice{
			{
				Message: openailib.ChatCompletionMessage{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "I need to check memory usage first.",
					Content:          "Let me check your memory usage.",
				},
			},
		},
		Usage: openailib.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	llmResp := p.toLLMResponse(resp)

	if llmResp.Content == nil {
		t.Fatal("expected non-nil content")
	}

	// Should have 2 parts: thought + regular text
	if len(llmResp.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(llmResp.Content.Parts))
	}

	// First part: reasoning as Thought
	if !llmResp.Content.Parts[0].Thought {
		t.Error("first part should be Thought=true")
	}
	if llmResp.Content.Parts[0].Text != "I need to check memory usage first." {
		t.Errorf("reasoning text: got %q, want %q", llmResp.Content.Parts[0].Text, "I need to check memory usage first.")
	}

	// Second part: regular content
	if llmResp.Content.Parts[1].Thought {
		t.Error("second part should be Thought=false")
	}
	if llmResp.Content.Parts[1].Text != "Let me check your memory usage." {
		t.Errorf("content text: got %q, want %q", llmResp.Content.Parts[1].Text, "Let me check your memory usage.")
	}
}

func TestMergeConsecutiveSameRole_ReasoningContent(t *testing.T) {
	tests := []struct {
		name            string
		input           []openailib.ChatCompletionMessage
		expectLen       int
		expectReasoning string // expected ReasoningContent on first assistant message
		expectContent   string // expected Content on first assistant message
	}{
		{
			name: "merge consecutive assistant messages preserves reasoning",
			input: []openailib.ChatCompletionMessage{
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "thinking part 1",
					Content:          "text part 1",
				},
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "thinking part 2",
					Content:          "text part 2",
				},
			},
			expectLen:       1,
			expectReasoning: "thinking part 1\nthinking part 2",
			expectContent:   "text part 1\ntext part 2",
		},
		{
			name: "merge with tool_calls preserves reasoning (case 2)",
			input: []openailib.ChatCompletionMessage{
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "I should use a tool",
					Content:          "Let me check",
				},
				{
					Role: openailib.ChatMessageRoleAssistant,
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "test", Arguments: "{}"}},
					},
				},
			},
			expectLen:       1,
			expectReasoning: "I should use a tool",
			expectContent:   "Let me check",
		},
		{
			name: "reasoning only on second message",
			input: []openailib.ChatCompletionMessage{
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "first",
				},
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "added reasoning",
					Content:          "second",
				},
			},
			expectLen:       1,
			expectReasoning: "added reasoning",
			expectContent:   "first\nsecond",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeConsecutiveSameRole(tt.input)
			if len(result) != tt.expectLen {
				t.Fatalf("expected %d messages, got %d", tt.expectLen, len(result))
			}
			if result[0].ReasoningContent != tt.expectReasoning {
				t.Errorf("ReasoningContent: got %q, want %q", result[0].ReasoningContent, tt.expectReasoning)
			}
			if result[0].Content != tt.expectContent {
				t.Errorf("Content: got %q, want %q", result[0].Content, tt.expectContent)
			}
		})
	}
}

func TestNonStreamingReasoningRoundTrip(t *testing.T) {
	// End-to-end test: verify that reasoning_content from a non-streaming
	// response is captured and sent back in subsequent requests.
	var capturedBodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)
		capturedBodies = append(capturedBodies, reqBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "test",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"reasoning_content": "Let me think about the title",
					"content": "Memory Usage Check"
				},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	}))
	defer srv.Close()

	config := openailib.DefaultConfig("test-key")
	config.BaseURL = srv.URL + "/v1"
	client := openailib.NewClientWithConfig(config)
	p := NewProvider(client, "test-model", true)

	// First request: get a response with reasoning_content
	req1 := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("test", genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{},
	}

	var firstResp *model.LLMResponse
	for resp, err := range p.GenerateContent(context.Background(), req1, false) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		firstResp = resp
	}

	// Verify the response has a Thought part
	if firstResp == nil || firstResp.Content == nil {
		t.Fatal("expected non-nil response with content")
	}
	if len(firstResp.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts (thought + text), got %d", len(firstResp.Content.Parts))
	}
	if !firstResp.Content.Parts[0].Thought {
		t.Error("first part should be Thought=true")
	}

	// Second request: send the response back as conversation history
	req2 := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("test", genai.RoleUser),
			firstResp.Content, // assistant response with Thought part
			genai.NewContentFromText("follow up", genai.RoleUser),
		},
		Config: &genai.GenerateContentConfig{},
	}

	for _, err := range p.GenerateContent(context.Background(), req2, false) {
		if err != nil {
			t.Fatalf("unexpected error on second request: %v", err)
		}
	}

	// Verify the second request includes reasoning_content
	if len(capturedBodies) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(capturedBodies))
	}

	messages, ok := capturedBodies[1]["messages"].([]any)
	if !ok {
		t.Fatal("expected messages array in second request")
	}

	// Find the assistant message in the second request
	var assistantFound bool
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "assistant" {
			assistantFound = true
			if m["reasoning_content"] != "Let me think about the title" {
				t.Errorf("reasoning_content: got %q, want %q", m["reasoning_content"], "Let me think about the title")
			}
			if m["content"] != "Memory Usage Check" {
				t.Errorf("content: got %q, want %q", m["content"], "Memory Usage Check")
			}
		}
	}
	if !assistantFound {
		t.Error("assistant message not found in second request")
	}
}

func TestPatchMissingReasoningContent(t *testing.T) {
	tests := []struct {
		name              string
		messages          []openailib.ChatCompletionMessage
		providerThinking  bool   // simulates p.thinkingMode.Load()
		expectLen         int    // message count should never change
		expectToolCalls   int    // total tool_calls remaining
		expectPatched     int    // number of messages that get a synthetic reasoning
		expectReasoning0  string // reasoning on first assistant message with tool_calls (empty = don't check)
	}{
		{
			name: "no thinking mode (provider=false, history=none) — unchanged",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "Let me check",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "ok"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "Done"},
			},
			providerThinking: false,
			expectLen:         4,
			expectToolCalls:   1,
			expectPatched:     0,
		},
		{
			name: "thinking mode with valid reasoning — unchanged",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "check stocks"},
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "I need to use the shell to check stock prices...",
					Content:          "Let me check",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "AAPL: 185"},
				{Role: openailib.ChatMessageRoleAssistant, ReasoningContent: "Got the data", Content: "AAPL is at 185"},
			},
			providerThinking: true,
			expectLen:         4,
			expectToolCalls:   1,
			expectPatched:     0,
		},
		{
			name: "missing reasoning detected via history fallback",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "check stocks"},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "Let me check",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "AAPL: 185"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "AAPL is at 185"},
				{Role: openailib.ChatMessageRoleUser, Content: "what about GOOG?"},
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "Now checking GOOG...",
					Content:          "Let me check GOOG",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_2", Content: "GOOG: 175"},
				{Role: openailib.ChatMessageRoleAssistant, ReasoningContent: "Got it", Content: "GOOG is at 175"},
			},
			providerThinking: false, // provider flag not set, but history has reasoning
			expectLen:         8,
			expectToolCalls:   2,
			expectPatched:     1,
			expectReasoning0:  ".",
		},
		{
			name: "first-turn: no reasoning in history but provider flag set",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "List memory on proxmox"},
				// Model called tools but reasoning was lost (preamble not persisted)
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "memory_search", Arguments: "{}"}},
						{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "resolve_credential", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "memory results"},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_2", Content: "credential resolved"},
			},
			providerThinking: true, // provider saw reasoning during first streaming
			expectLen:         4,
			expectToolCalls:   2,
			expectPatched:     1,
			expectReasoning0:  ".",
		},
		{
			name: "first-turn: no reasoning anywhere and provider=false — no patch",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hello"},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "shell_command", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "ok"},
			},
			providerThinking: false,
			expectLen:         3,
			expectToolCalls:   1,
			expectPatched:     0,
		},
		{
			name: "multiple missing reasoning turns with provider flag",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "turn 1"},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "checking...",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_1", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "t1", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_1", Content: "r1"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "done 1"},
				{Role: openailib.ChatMessageRoleUser, Content: "turn 2"},
				{
					Role:    openailib.ChatMessageRoleAssistant,
					Content: "checking again...",
					ToolCalls: []openailib.ToolCall{
						{ID: "call_2", Type: openailib.ToolTypeFunction, Function: openailib.FunctionCall{Name: "t2", Arguments: "{}"}},
					},
				},
				{Role: openailib.ChatMessageRoleTool, ToolCallID: "call_2", Content: "r2"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "all done"},
			},
			providerThinking: true,
			expectLen:         8,
			expectToolCalls:   2,
			expectPatched:     2,
		},
		{
			name: "assistant without tool_calls and no reasoning — not patched",
			messages: []openailib.ChatCompletionMessage{
				{Role: openailib.ChatMessageRoleUser, Content: "hi"},
				{Role: openailib.ChatMessageRoleAssistant, Content: "hello"},
				{Role: openailib.ChatMessageRoleUser, Content: "check something"},
				{
					Role:             openailib.ChatMessageRoleAssistant,
					ReasoningContent: "thinking...",
					Content:          "result",
				},
			},
			providerThinking: true,
			expectLen:         4,
			expectToolCalls:   0,
			expectPatched:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := make([]openailib.ChatCompletionMessage, len(tt.messages))
			copy(msgs, tt.messages)

			result := patchMissingReasoningContent(msgs, tt.providerThinking)
			if len(result) != tt.expectLen {
				t.Errorf("expected %d messages, got %d", tt.expectLen, len(result))
				for i, m := range result {
					t.Logf("  [%d] role=%s toolCalls=%d reasoning=%q", i, m.Role, len(m.ToolCalls), m.ReasoningContent)
				}
			}
			totalToolCalls := 0
			patched := 0
			for _, m := range result {
				totalToolCalls += len(m.ToolCalls)
				if m.ReasoningContent == "." {
					patched++
				}
			}
			if totalToolCalls != tt.expectToolCalls {
				t.Errorf("expected %d total tool_calls, got %d", tt.expectToolCalls, totalToolCalls)
			}
			if patched != tt.expectPatched {
				t.Errorf("expected %d patched messages, got %d", tt.expectPatched, patched)
			}
			if tt.expectReasoning0 != "" {
				for _, m := range result {
					if m.Role == openailib.ChatMessageRoleAssistant && len(m.ToolCalls) > 0 {
						if m.ReasoningContent != tt.expectReasoning0 {
							t.Errorf("first tool_calls assistant reasoning: got %q, want %q", m.ReasoningContent, tt.expectReasoning0)
						}
						break
					}
				}
			}
		})
	}
}
