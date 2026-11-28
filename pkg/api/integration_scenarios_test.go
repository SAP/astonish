package api

import (
	"strings"
	"testing"

	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// TestIntegration_X1_SimpleTextResponse verifies the most basic flow:
// user sends a message, LLM returns text, events are session → text → done.
func TestIntegration_X1_SimpleTextResponse(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Hello! How can I help you today?"))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Hi there")

	assertFirstAndLast(t, events)
	assertEventSequence(t, events, "session", "text", "done")

	textEv := assertHasEvent(t, events, "text")
	assertEventData(t, textEv, "text", "Hello! How can I help you today?")

	sessionEv := assertHasEvent(t, events, "session")
	assertEventData(t, sessionEv, "isNew", true)
}

// TestIntegration_X2_StreamingText verifies that streaming partial chunks
// are emitted as individual text events, followed by the final text and done.
func TestIntegration_X2_StreamingText(t *testing.T) {
	mockLLM := NewMockLLM(
		StreamChunk("Hello "),
		StreamChunk("world, "),
		StreamFinal("how are you?"),
	)
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Hi")

	assertFirstAndLast(t, events)

	textEvents := eventsOfType(events, "text")
	if len(textEvents) < 2 {
		t.Fatalf("expected at least 2 text events for streaming, got %d", len(textEvents))
	}

	// Verify at least the first chunk is present
	firstText, _ := textEvents[0].Data["text"].(string)
	if firstText != "Hello " {
		t.Errorf("first streaming chunk = %q, want %q", firstText, "Hello ")
	}
}

// TestIntegration_X3_SingleToolCall verifies the tool execution loop:
// LLM requests a function call → tool executes → result fed back → LLM responds with text.
func TestIntegration_X3_SingleToolCall(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM requests a tool call
		ToolCallTurn("list_files", map[string]any{"directory": "/tmp"}),
		// Turn 2: After tool result, LLM responds with text
		TextTurn("I found 3 files in /tmp."),
	)

	listFilesTool := newMockTool("list_files", "List files in a directory", map[string]any{
		"files": []string{"a.txt", "b.txt", "c.txt"},
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{listFilesTool})
	events := runAndCollect(t, env, "List files in /tmp")

	assertFirstAndLast(t, events)

	// The runner emits events from rnr.Run() iterator which includes ADK's
	// internal tool execution. We should see tool_call and tool_result events
	// from the SSE event stream.
	assertEventSequence(t, events, "session", "tool_call", "tool_result", "text", "done")

	toolCallEv := assertHasEvent(t, events, "tool_call")
	assertEventData(t, toolCallEv, "name", "list_files")

	toolResultEv := assertHasEvent(t, events, "tool_result")
	assertEventData(t, toolResultEv, "name", "list_files")

	textEv := assertHasEvent(t, events, "text")
	assertEventData(t, textEv, "text", "I found 3 files in /tmp.")
}

// TestIntegration_X4_MultiToolChain verifies chained tool calls:
// LLM calls tool A → result → LLM calls tool B → result → LLM responds.
func TestIntegration_X4_MultiToolChain(t *testing.T) {
	mockLLM := NewMockLLM(
		ToolCallTurn("read_file", map[string]any{"path": "/tmp/config.yaml"}),
		ToolCallTurn("write_file", map[string]any{"path": "/tmp/config.yaml", "content": "updated"}),
		TextTurn("I've read and updated the config file."),
	)

	readTool := newMockTool("read_file", "Read a file", map[string]any{
		"content": "old config content",
	})
	writeTool := newMockTool("write_file", "Write a file", map[string]any{
		"status": "written",
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{readTool, writeTool})
	events := runAndCollect(t, env, "Update config.yaml")

	assertFirstAndLast(t, events)

	// Should see two tool_call/tool_result pairs
	toolCalls := eventsOfType(events, "tool_call")
	toolResults := eventsOfType(events, "tool_result")
	if len(toolCalls) < 2 {
		t.Errorf("expected at least 2 tool_call events, got %d", len(toolCalls))
	}
	if len(toolResults) < 2 {
		t.Errorf("expected at least 2 tool_result events, got %d", len(toolResults))
	}

	// Verify tool names in order
	if len(toolCalls) >= 2 {
		assertEventData(t, toolCalls[0], "name", "read_file")
		assertEventData(t, toolCalls[1], "name", "write_file")
	}

	assertHasEvent(t, events, "text")
}

// TestIntegration_X5_LLMError verifies that LLM errors are surfaced as error events.
func TestIntegration_X5_LLMError(t *testing.T) {
	mockLLM := NewMockLLM(ErrorTurn("rate_limit_exceeded", "Rate limit exceeded. Please retry after 60s."))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Hello")

	assertFirstAndLast(t, events)
	assertEventSequence(t, events, "session", "error", "done")

	errorEv := assertHasEvent(t, events, "error")
	errStr, _ := errorEv.Data["error"].(string)
	if errStr == "" {
		t.Fatal("error event should have non-empty error string")
	}
	if !strings.Contains(errStr, "rate_limit") {
		t.Errorf("error message should contain 'rate_limit', got: %s", errStr)
	}
}

// TestIntegration_X6_UsageMetadata verifies that usage/token count events
// are emitted when the LLM response includes UsageMetadata.
func TestIntegration_X6_UsageMetadata(t *testing.T) {
	mockLLM := NewMockLLM(TextTurnWithUsage("Here is your answer.", 150, 50, 200))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Tell me something")

	assertFirstAndLast(t, events)
	assertEventSequence(t, events, "session", "text", "usage", "done")

	usageEv := assertHasEvent(t, events, "usage")
	assertEventData(t, usageEv, "input_tokens", 150)
	assertEventData(t, usageEv, "output_tokens", 50)
	assertEventData(t, usageEv, "total_tokens", 200)
}

// TestIntegration_X7_EmptyResponse verifies that an empty LLM response
// (no text, no tool calls) triggers an error event.
func TestIntegration_X7_EmptyResponse(t *testing.T) {
	mockLLM := NewMockLLM(EmptyTurn())
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Hello")

	assertFirstAndLast(t, events)

	errorEv := assertHasEvent(t, events, "error")
	errStr, _ := errorEv.Data["error"].(string)
	if !strings.Contains(errStr, "empty response") {
		t.Errorf("expected 'empty response' in error, got: %s", errStr)
	}
}

// TestIntegration_X8_SessionTitleSkippedWithoutFileStore verifies that when
// fileStore is nil (as in our test setup), the session_title event is NOT emitted
// (because title generation requires a FileStore for persistence).
func TestIntegration_X8_SessionTitleSkippedWithoutFileStore(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Hello!"))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Hi there")

	assertFirstAndLast(t, events)

	// Without a FileStore, session_title should NOT be emitted
	assertNoEvent(t, events, "session_title")
}

// TestIntegration_X9_AnnouncePlanSuppressed verifies that announce_plan
// tool calls and results are suppressed from the event stream (they're
// rendered via the PlanPanel, not as raw tool calls).
func TestIntegration_X9_AnnouncePlanSuppressed(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM calls announce_plan (should be suppressed)
		ToolCallTurn("announce_plan", map[string]any{
			"goal":  "Fix the bug",
			"steps": []any{"Identify root cause", "Apply fix", "Test"},
		}),
		// Turn 2: Final text
		TextTurn("I'll fix this bug now."),
	)

	planTool := newMockTool("announce_plan", "Announce execution plan", map[string]any{
		"status": "announced",
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{planTool})
	events := runAndCollect(t, env, "Fix the login bug")

	assertFirstAndLast(t, events)

	// announce_plan tool calls should be suppressed
	for _, ev := range eventsOfType(events, "tool_call") {
		name, _ := ev.Data["name"].(string)
		if name == "announce_plan" {
			t.Error("announce_plan tool_call should be suppressed from event stream")
		}
	}
	// This fixture has no plan binding: the gate rejects the call. Keep that
	// rejection visible; suppressing it fabricated an apparently accepted plan.
	result := assertHasEvent(t, events, "tool_result")
	assertEventData(t, result, "name", "announce_plan")
	assertNoEvent(t, events, "subtask_progress")

	// But the text response should still appear
	assertHasEvent(t, events, "text")
}

// TestIntegration_X10_AppPreviewDetection verifies that code fences with
// ```astonish-app are detected post-run and emitted as app_preview events.
func TestIntegration_X10_AppPreviewDetection(t *testing.T) {
	appCode := `import React from 'react';

export default function SalesDashboard() {
  return <div>Sales Dashboard</div>;
}`
	responseText := "Here's your app:\n\n```astonish-app\n" + appCode + "\n```\n\nEnjoy!"

	mockLLM := NewMockLLM(TextTurn(responseText))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Create a sales dashboard app")

	assertFirstAndLast(t, events)

	// Should have a text event with the full response
	assertHasEvent(t, events, "text")

	// Should have an app_preview event detected post-run
	appEv := assertHasEvent(t, events, "app_preview")
	code, _ := appEv.Data["code"].(string)
	if !strings.Contains(code, "SalesDashboard") {
		t.Errorf("app_preview code should contain 'SalesDashboard', got: %s", code)
	}

	title, _ := appEv.Data["title"].(string)
	if title != "Sales Dashboard" {
		t.Errorf("app_preview title = %q, want %q", title, "Sales Dashboard")
	}

	version, ok := appEv.Data["version"]
	if !ok {
		t.Error("app_preview should have 'version' field")
	}
	if toString(version) != "1" {
		t.Errorf("app_preview version = %v, want 1", version)
	}

	appID, _ := appEv.Data["appId"].(string)
	if appID == "" {
		t.Error("app_preview should have non-empty 'appId'")
	}
}

// TestIntegration_X3b_ToolCallWithArgs verifies that tool call arguments
// are correctly propagated in the tool_call event.
func TestIntegration_X3b_ToolCallWithArgs(t *testing.T) {
	mockLLM := NewMockLLM(
		ToolCallTurn("shell_command", map[string]any{
			"command": "echo hello",
		}),
		TextTurn("Command executed successfully."),
	)

	shellTool := newMockTool("shell_command", "Run a shell command", map[string]any{
		"stdout":    "hello\n",
		"exit_code": 0,
	})

	env := setupIntegrationTest(t, mockLLM, []tool.Tool{shellTool})
	events := runAndCollect(t, env, "Run echo hello")

	assertFirstAndLast(t, events)

	toolCallEv := assertHasEvent(t, events, "tool_call")
	assertEventData(t, toolCallEv, "name", "shell_command")

	// Verify args are present
	args, ok := toolCallEv.Data["args"].(map[string]any)
	if !ok {
		t.Fatal("tool_call args should be a map")
	}
	cmd, _ := args["command"].(string)
	if cmd != "echo hello" {
		t.Errorf("tool_call args.command = %q, want %q", cmd, "echo hello")
	}
}

// TestIntegration_EventIDs verifies that all events have unique, non-empty IDs.
func TestIntegration_EventIDs(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Hello!"))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Hi")

	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.ID == "" {
			t.Errorf("event of type %q has empty ID", ev.Type)
		}
		if seen[ev.ID] {
			t.Errorf("duplicate event ID %q", ev.ID)
		}
		seen[ev.ID] = true
	}
}

// TestIntegration_SessionID verifies the session event carries the correct session ID.
func TestIntegration_SessionID(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("OK"))
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "ping")

	sessionEv := assertHasEvent(t, events, "session")
	sid, _ := sessionEv.Data["sessionId"].(string)
	if sid != env.Runner.SessionID {
		t.Errorf("session event sessionId = %q, want %q", sid, env.Runner.SessionID)
	}
}

// TestIntegration_DoneIsAlwaysLast verifies that "done" is always the final event,
// even when errors occur.
func TestIntegration_DoneIsAlwaysLast(t *testing.T) {
	tests := []struct {
		name  string
		turns []*MockTurn
	}{
		{"success", []*MockTurn{TextTurn("OK")}},
		{"error", []*MockTurn{ErrorTurn("500", "Internal error")}},
		{"empty", []*MockTurn{EmptyTurn()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockLLM := NewMockLLM(tt.turns...)
			env := setupIntegrationTest(t, mockLLM, nil)
			events := runAndCollect(t, env, "test")

			if len(events) == 0 {
				t.Fatal("expected at least 1 event")
			}
			last := events[len(events)-1]
			if last.Type != "done" {
				t.Errorf("last event type = %q, want 'done'", last.Type)
			}
		})
	}
}

// TestIntegration_MockLLMCallRecording verifies that the MockLLM records
// all LLM requests for assertion.
func TestIntegration_MockLLMCallRecording(t *testing.T) {
	mockLLM := NewMockLLM(TextTurn("Response"))
	env := setupIntegrationTest(t, mockLLM, nil)
	_ = runAndCollect(t, env, "Hello world")

	mockLLM.mu.Lock()
	defer mockLLM.mu.Unlock()
	if len(mockLLM.Calls) == 0 {
		t.Fatal("MockLLM should have recorded at least 1 call")
	}

	// The first call should contain the user's message in the contents
	firstCall := mockLLM.Calls[0]
	found := false
	for _, c := range firstCall.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, "Hello world") {
				found = true
			}
		}
	}
	if !found {
		t.Error("MockLLM first call should contain user message 'Hello world'")
	}
}

// TestIntegration_MultipleTextParts verifies that a response with multiple
// text parts in a single Content emits multiple text events.
func TestIntegration_MultipleTextParts(t *testing.T) {
	mockLLM := NewMockLLM(&MockTurn{
		Parts: []*genai.Part{
			{Text: "First part. "},
			{Text: "Second part."},
		},
		TurnComplete: true,
	})
	env := setupIntegrationTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Tell me two things")

	assertFirstAndLast(t, events)

	textEvents := eventsOfType(events, "text")
	if len(textEvents) < 2 {
		t.Fatalf("expected at least 2 text events for multi-part response, got %d", len(textEvents))
	}
}
