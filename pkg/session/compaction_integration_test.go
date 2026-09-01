package session

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestCodeStrategy_ProducesStructuredSummary verifies that the CodeStrategy
// produces a structured 8-section summary from a realistic code session.
func TestCodeStrategy_ProducesStructuredSummary(t *testing.T) {
	c := NewCompactor(200000)
	c.PreserveRecent = 2

	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "## OBJECTIVE\nBuild REST API\n\n## FILES MODIFIED\n- main.go — added routes\n\n## TASKS COMPLETED\n- Setup project\n\n## TASKS PENDING\n- Add tests\n\n## KEY DECISIONS\n- Use chi router\n\n## ERRORS & FIXES\n- None\n\n## CURRENT STATE\nImplementing handlers", nil
	}
	c.SetStrategy(&CodeStrategy{})

	// Build a realistic code session with multiple tool calls
	contents := []*genai.Content{
		makeContent("user", "Build a REST API with Go"),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "/app/go.mod"}),
		makeFuncResponseContent("user", "read_file", map[string]any{"content": "module app"}),
		makeFuncCallContent("model", "edit_file", map[string]any{"path": "/app/main.go"}),
		makeFuncResponseContent("user", "edit_file", map[string]any{"status": "ok"}),
		makeFuncCallContent("model", "write_file", map[string]any{"file_path": "/app/handlers/user.go"}),
		makeFuncResponseContent("user", "write_file", map[string]any{"status": "ok"}),
		makeFuncCallContent("model", "shell_command", map[string]any{"command": "go build ./...", "working_dir": "/app"}),
		makeFuncResponseContent("user", "shell_command", map[string]any{"exit_code": float64(0)}),
		makeContent("model", "The REST API is set up. Let me add the test file."),
		makeFuncCallContent("model", "write_file", map[string]any{"file_path": "/app/handlers/user_test.go"}),
		makeFuncResponseContent("user", "write_file", map[string]any{"status": "ok"}),
	}

	result, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	// Verify the prompt used the code strategy format
	if !strings.Contains(capturedPrompt, "## OBJECTIVE") {
		t.Error("prompt should contain OBJECTIVE section")
	}
	if !strings.Contains(capturedPrompt, "## FILES MODIFIED") {
		t.Error("prompt should contain FILES MODIFIED section")
	}

	// Verify file paths were extracted
	if !strings.Contains(capturedPrompt, "/app/main.go") {
		t.Error("prompt should contain extracted file path /app/main.go")
	}
	if !strings.Contains(capturedPrompt, "/app/handlers/user.go") {
		t.Error("prompt should contain extracted file path /app/handlers/user.go")
	}

	// Verify the result contains the structured summary
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result[0].Parts[0].Text, "## OBJECTIVE") {
		t.Error("compacted summary should contain structured sections")
	}
}

// TestSessionNotes_RenderedAsPreBuiltState verifies that SessionNotes.Render()
// output is accepted by CodeStrategy as pre-built notes.
func TestSessionNotes_RenderedAsPreBuiltState(t *testing.T) {
	notes := NewSessionNotes()
	notes.SetObjective("Implement authentication")
	notes.RecordFileChange("/app/auth/middleware.go", "created", "JWT validation middleware")
	notes.RecordFileChange("/app/auth/token.go", "created", "Token generation")
	notes.RecordTaskCompleted("Set up project structure")
	notes.RecordTaskCompleted("Create middleware skeleton")
	notes.AddTaskPending("Add token refresh")
	notes.RecordDecision("Use RS256 for JWT signing")
	notes.RecordError("jwt.Parse failed on empty string", "added nil check", true)
	notes.SetCurrentState("Writing token refresh handler")

	c := NewCompactor(200000)
	c.PreserveRecent = 1
	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "SUMMARY", nil
	}
	c.SetStrategy(&CodeStrategy{SessionNotes: notes})

	contents := []*genai.Content{
		makeContent("user", "Continue with the refresh token"),
		makeContent("model", "Working on it"),
	}

	_, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	// Verify pre-built notes are included
	if !strings.Contains(capturedPrompt, "Pre-built session state") {
		t.Error("prompt should contain pre-built session state header")
	}
	if !strings.Contains(capturedPrompt, "Implement authentication") {
		t.Error("prompt should contain objective from notes")
	}
	if !strings.Contains(capturedPrompt, "/app/auth/middleware.go") {
		t.Error("prompt should contain file paths from notes")
	}
	if !strings.Contains(capturedPrompt, "Use RS256 for JWT signing") {
		t.Error("prompt should contain decisions from notes")
	}
	if !strings.Contains(capturedPrompt, "Add token refresh") {
		t.Error("prompt should contain pending tasks from notes")
	}
}

// TestGenericStrategy_StillProducesOldFormat verifies that the GenericStrategy
// still produces the CURRENT TASK/PROGRESS/COMPLETED format.
func TestGenericStrategy_StillProducesOldFormat(t *testing.T) {
	c := NewCompactor(200000)
	c.PreserveRecent = 1
	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "CURRENT TASK: Testing\nPROGRESS: Done\nCOMPLETED: Nothing", nil
	}
	// Explicitly set GenericStrategy
	c.SetStrategy(&GenericStrategy{})

	contents := []*genai.Content{
		makeContent("user", "Hello"),
		makeContent("model", "Hi"),
	}

	_, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	if !strings.Contains(capturedPrompt, "CURRENT TASK") {
		t.Error("GenericStrategy prompt should contain CURRENT TASK")
	}
	if strings.Contains(capturedPrompt, "## OBJECTIVE") {
		t.Error("GenericStrategy should NOT contain OBJECTIVE section")
	}
}

// TestCompactor_NilStrategy_DefaultsToGeneric verifies backward compatibility:
// a Compactor with nil Strategy uses GenericStrategy.
func TestCompactor_NilStrategy_DefaultsToGeneric(t *testing.T) {
	c := NewCompactor(200000)
	c.PreserveRecent = 1
	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "SUMMARY", nil
	}
	// Do NOT set Strategy — nil = GenericStrategy

	contents := []*genai.Content{
		makeContent("user", "Hello"),
		makeContent("model", "Hi"),
	}

	_, err := c.CompactContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CompactContents error: %v", err)
	}

	if !strings.Contains(capturedPrompt, "CURRENT TASK") {
		t.Error("nil Strategy should default to GenericStrategy")
	}
}
