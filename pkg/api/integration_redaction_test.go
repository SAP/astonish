package api

// =============================================================================
// Integration Tests: Credential Redaction Pipeline
//
// These tests verify that the credential redaction system properly scrubs
// secret values from all output surfaces: SSE text events, tool_call args,
// tool_result payloads, and session history responses.
//
// This is a security-critical test suite. Regressions here mean credentials
// leak to the frontend or session storage in plaintext.
//
// Test IDs:
//   CHAT-070: Tool result redaction in live SSE stream
//   CHAT-071: Text response redaction in live SSE stream
//   CHAT-072: Tool call args redaction in live SSE stream
//   CHAT-073: Session history redaction (eventsToMessages with redactor)
//   CHAT-074: No redaction when redactor is nil (baseline correctness)
// =============================================================================

import (
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/credentials"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const (
	// testSecretValue is the raw secret that MUST never appear in test outputs.
	testSecretValue = "sk-SUPER-SECRET-API-KEY-12345678"
	// testSecretName is the credential name used in redaction markers.
	testSecretName = "test-api-key"
	// testRedactedMarker is what should appear instead of the raw secret.
	testRedactedMarker = "[REDACTED]"
)

// setupRedactionTest extends setupIntegrationTest by wiring a Redactor
// pre-loaded with a known secret. This simulates a user whose credential
// store has been hydrated before the chat run starts.
func setupRedactionTest(t *testing.T, mockLLM *MockLLM, tools []tool.Tool) *integrationTestEnv {
	t.Helper()

	env := setupIntegrationTest(t, mockLLM, tools)

	// Wire a Redactor with the known test secret
	redactor := credentials.NewRedactor()
	redactor.AddSecret(testSecretName, testSecretValue)
	env.ChatAgent.Redactor = redactor

	return env
}

// --- CHAT-070: Tool result contains secret → must be redacted in SSE stream ---

// TestRedaction_ToolResultRedacted verifies that when a tool's output contains
// a raw secret value, the SSE tool_result event shows [REDACTED] instead.
//
// Scenario: The LLM calls a tool, which returns the secret in its output. The
// Redactor replaces the raw value before it reaches the browser.
//
// COVERS: CHAT-070
func TestRedaction_ToolResultRedacted(t *testing.T) {
	// Mock tool that returns a result containing the raw secret.
	// Uses the standard mockToolArgs schema (accepts "input" map).
	leakyTool := newMockTool("fetch_config", "Fetch configuration", map[string]any{
		"config": "api_key=" + testSecretValue + "&region=us-east-1",
	})

	mockLLM := NewMockLLM(
		// Turn 1: LLM calls fetch_config with compatible args
		ToolCallTurn("fetch_config", map[string]any{"input": map[string]any{"key": "production"}}),
		// Turn 2: LLM responds with a summary
		TextTurn("The configuration was loaded successfully."),
	)

	env := setupRedactionTest(t, mockLLM, []tool.Tool{leakyTool})
	events := runAndCollect(t, env, "Show me the production config")

	assertFirstAndLast(t, events)

	// Verify tool_result exists
	toolResults := eventsOfType(events, "tool_result")
	if len(toolResults) == 0 {
		t.Fatal("expected at least one tool_result event")
	}

	// Assert that NO event in the entire stream contains the raw secret
	assertNoSecretInEvents(t, events, testSecretValue)

	// Assert that the tool_result DOES contain the redaction marker
	resultJSON, _ := json.Marshal(toolResults[0].Data)
	if !strings.Contains(string(resultJSON), testRedactedMarker) {
		t.Errorf("tool_result should contain redaction marker %q, got: %s",
			testRedactedMarker, string(resultJSON))
	}
}

// --- CHAT-071: LLM text response echoes secret → must be redacted ---

// TestRedaction_TextResponseRedacted verifies that if the LLM's text response
// contains a raw secret (e.g., it echoes a resolved credential), the SSE text
// event is redacted before reaching the browser.
//
// COVERS: CHAT-071
func TestRedaction_TextResponseRedacted(t *testing.T) {
	mockLLM := NewMockLLM(
		// Turn 1: LLM responds with text that includes the raw secret
		TextTurn("Your API key is: " + testSecretValue + ". Keep it safe!"),
	)

	env := setupRedactionTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "What is my API key?")

	assertFirstAndLast(t, events)

	// Assert the raw secret NEVER appears in any event
	assertNoSecretInEvents(t, events, testSecretValue)

	// Assert that the text event contains the redaction marker
	textEvents := eventsOfType(events, "text")
	if len(textEvents) == 0 {
		t.Fatal("expected at least one text event")
	}

	foundMarker := false
	for _, ev := range textEvents {
		if text, ok := ev.Data["text"].(string); ok {
			if strings.Contains(text, testRedactedMarker) {
				foundMarker = true
			}
		}
	}
	if !foundMarker {
		t.Errorf("text events should contain redaction marker %q", testRedactedMarker)
	}
}

// --- CHAT-072: Tool call args contain secret → must be redacted in SSE ---

// TestRedaction_ToolCallArgsRedacted verifies that when the LLM passes a
// credential value in tool call arguments, the SSE tool_call event shows the
// redacted form, not the raw secret.
//
// COVERS: CHAT-072
func TestRedaction_ToolCallArgsRedacted(t *testing.T) {
	dummyTool := newMockTool("http_request", "Make an HTTP request", map[string]any{
		"status": 200,
		"body":   "ok",
	})

	mockLLM := NewMockLLM(
		// Turn 1: LLM calls http_request with the secret in the Authorization header
		ToolCallTurn("http_request", map[string]any{
			"url":     "https://api.example.com/data",
			"headers": map[string]any{"Authorization": "Bearer " + testSecretValue},
		}),
		// Turn 2: LLM summarizes
		TextTurn("The request returned 200 OK."),
	)

	env := setupRedactionTest(t, mockLLM, []tool.Tool{dummyTool})
	events := runAndCollect(t, env, "Fetch data from the API using my key")

	assertFirstAndLast(t, events)

	// Assert the raw secret NEVER appears in any event
	assertNoSecretInEvents(t, events, testSecretValue)

	// Verify tool_call args are redacted
	toolCalls := eventsOfType(events, "tool_call")
	if len(toolCalls) == 0 {
		t.Fatal("expected at least one tool_call event")
	}

	callJSON, _ := json.Marshal(toolCalls[0].Data)
	if strings.Contains(string(callJSON), testSecretValue) {
		t.Fatalf("tool_call event must NOT contain raw secret, got: %s", string(callJSON))
	}
	if !strings.Contains(string(callJSON), testRedactedMarker) {
		t.Errorf("tool_call event should contain redaction marker %q, got: %s",
			testRedactedMarker, string(callJSON))
	}
}

// --- CHAT-073: Session history must be redacted when loaded ---

// TestRedaction_SessionHistoryRedacted verifies that eventsToMessages() applies
// the Redactor to text and tool results when building the session history
// response. This is the defense-in-depth layer that catches secrets persisted
// in session storage.
//
// COVERS: CHAT-073
func TestRedaction_SessionHistoryRedacted(t *testing.T) {
	// Build a mock ADK session with events containing raw secrets
	events := newMockSessionEvents(
		// User message
		modelEvent("user", "Show me my credentials"),
		// Agent text response containing raw secret
		modelEvent("model", "Your API key is "+testSecretValue),
		// Tool call with secret in args
		toolCallEvent("shell_command", map[string]any{
			"command": "echo " + testSecretValue,
		}),
		// Tool result with secret in output
		toolResultEvent("shell_command", map[string]any{
			"stdout": testSecretValue,
			"code":   0,
		}),
		// Agent final response
		modelEvent("model", "Done. The key "+testSecretValue+" was used."),
	)

	// Create a Redactor with the test secret
	redactor := credentials.NewRedactor()
	redactor.AddSecret(testSecretName, testSecretValue)

	// Call eventsToMessages WITH the redactor (the fix we applied)
	messages := eventsToMessages(events, redactor)

	// Serialize all messages to JSON and check for secret leakage
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("failed to marshal messages: %v", err)
	}

	fullOutput := string(messagesJSON)
	if strings.Contains(fullOutput, testSecretValue) {
		t.Fatalf("session history MUST NOT contain raw secret %q\nFull output:\n%s",
			testSecretValue, fullOutput)
	}

	// Verify the redaction marker IS present (proves redaction ran, not just absence)
	if !strings.Contains(fullOutput, testRedactedMarker) {
		t.Errorf("session history should contain redaction marker %q\nFull output:\n%s",
			testRedactedMarker, fullOutput)
	}
}

// --- CHAT-074: Verify that nil redactor passes through (no panic, no redaction) ---

// TestRedaction_NilRedactorNoRedaction verifies that eventsToMessages with a nil
// redactor still works correctly — returning the raw text without panicking.
// This is the baseline for personal mode where credentials might not exist.
//
// COVERS: CHAT-074
func TestRedaction_NilRedactorNoRedaction(t *testing.T) {
	events := newMockSessionEvents(
		modelEvent("model", "The secret is "+testSecretValue),
	)

	// nil redactor — should not panic, should not redact
	messages := eventsToMessages(events, nil)

	messagesJSON, _ := json.Marshal(messages)
	fullOutput := string(messagesJSON)

	// With nil redactor, the raw secret SHOULD appear (no redaction applied)
	if !strings.Contains(fullOutput, testSecretValue) {
		t.Errorf("with nil redactor, raw text should pass through unchanged")
	}
}

// --- CHAT-075: Streaming partial text events are redacted when they contain the full secret ---

// TestRedaction_StreamingPartialTextRedacted verifies that streaming partial
// text events that contain a complete secret value are properly redacted.
//
// Note: If a secret is split across multiple streaming chunks (e.g., first half
// in chunk 1, second half in chunk 2), per-chunk redaction cannot catch it.
// This is a known architectural limitation mitigated by:
//   - Tool outputs arriving as complete (non-partial) events
//   - BeforeToolCallback substituting placeholders before execution
//   - AfterToolCallback redacting complete tool results
//
// This test verifies the case where the secret appears within a single chunk.
//
// COVERS: CHAT-075
func TestRedaction_StreamingPartialTextRedacted(t *testing.T) {
	// Simulate streaming where one chunk contains the complete secret
	mockLLM := NewMockLLM(
		// Chunk 1: innocent text
		StreamChunk("Here is your key: "),
		// Chunk 2: contains the full secret in one chunk
		StreamChunk(testSecretValue),
		// Chunk 3: trailing text
		StreamChunk(" — keep it safe!"),
		// Final non-partial
		StreamFinal(""),
	)

	env := setupRedactionTest(t, mockLLM, nil)
	events := runAndCollect(t, env, "Tell me the API key")

	assertFirstAndLast(t, events)

	// Assert the raw secret NEVER appears in any individual text event
	assertNoSecretInEvents(t, events, testSecretValue)

	// Verify the redaction marker appears in the text events
	foundMarker := false
	for _, ev := range eventsOfType(events, "text") {
		if text, ok := ev.Data["text"].(string); ok {
			if strings.Contains(text, testRedactedMarker) {
				foundMarker = true
			}
		}
	}
	if !foundMarker {
		t.Errorf("expected redaction marker %q in streamed text events", testRedactedMarker)
	}
}

// =============================================================================
// Helpers
// =============================================================================

// assertNoSecretInEvents fails if any event in the stream contains the raw secret.
func assertNoSecretInEvents(t *testing.T, events []ChatEvent, secret string) {
	t.Helper()
	for i, ev := range events {
		evJSON, _ := json.Marshal(ev.Data)
		if strings.Contains(string(evJSON), secret) {
			t.Fatalf("event[%d] type=%q contains raw secret!\nData: %s",
				i, ev.Type, string(evJSON))
		}
	}
}

// --- Mock session event builders ---

// mockSessionEvents implements session.Events for testing eventsToMessages.
type mockSessionEvents struct {
	events []*session.Event
}

func (m *mockSessionEvents) Len() int                { return len(m.events) }
func (m *mockSessionEvents) At(i int) *session.Event { return m.events[i] }
func (m *mockSessionEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, e := range m.events {
			if !yield(e) {
				return
			}
		}
	}
}

func newMockSessionEvents(events ...*session.Event) *mockSessionEvents {
	return &mockSessionEvents{events: events}
}

func modelEvent(role, text string) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role:  role,
				Parts: []*genai.Part{{Text: text}},
			},
		},
	}
}

func toolCallEvent(name string, args map[string]any) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{
					{FunctionCall: &genai.FunctionCall{Name: name, Args: args, ID: "call_1"}},
				},
			},
		},
	}
}

func toolResultEvent(name string, result map[string]any) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{
					{FunctionResponse: &genai.FunctionResponse{Name: name, Response: result, ID: "call_1"}},
				},
			},
		},
	}
}
