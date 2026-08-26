package session

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestGenericStrategy_Name(t *testing.T) {
	s := &GenericStrategy{}
	if s.Name() != "platform" {
		t.Errorf("GenericStrategy.Name() = %q, want %q", s.Name(), "platform")
	}
}

func TestCodeStrategy_Name(t *testing.T) {
	s := &CodeStrategy{}
	if s.Name() != "code" {
		t.Errorf("CodeStrategy.Name() = %q, want %q", s.Name(), "code")
	}
}

func TestGenericStrategy_BuildPrompt(t *testing.T) {
	s := &GenericStrategy{}
	contents := []*genai.Content{
		makeContent("user", "What is Go?"),
		makeContent("model", "Go is a programming language."),
	}
	prompt := s.BuildSummarizationPrompt(contents)

	if !strings.Contains(prompt, "CURRENT TASK") {
		t.Error("GenericStrategy prompt should contain CURRENT TASK")
	}
	if !strings.Contains(prompt, "PROGRESS") {
		t.Error("GenericStrategy prompt should contain PROGRESS")
	}
	if !strings.Contains(prompt, "COMPLETED WORK") {
		t.Error("GenericStrategy prompt should contain COMPLETED WORK")
	}
	if !strings.Contains(prompt, "What is Go?") {
		t.Error("GenericStrategy prompt should contain conversation content")
	}
}

func TestCodeStrategy_BuildPrompt_StructuredSections(t *testing.T) {
	s := &CodeStrategy{}
	contents := []*genai.Content{
		makeContent("user", "Implement authentication middleware"),
		makeFuncCallContent("model", "edit_file", map[string]any{"path": "/app/middleware/auth.go"}),
		makeFuncResponseContent("user", "edit_file", map[string]any{"status": "ok"}),
		makeFuncCallContent("model", "write_file", map[string]any{"file_path": "/app/middleware/auth_test.go"}),
		makeFuncResponseContent("user", "write_file", map[string]any{"status": "ok"}),
		makeContent("model", "I've created the auth middleware and its test file."),
	}
	prompt := s.BuildSummarizationPrompt(contents)

	// Should contain all 7 sections
	for _, section := range []string{"OBJECTIVE", "FILES MODIFIED", "TASKS COMPLETED", "TASKS PENDING", "KEY DECISIONS", "ERRORS & FIXES", "CURRENT STATE"} {
		if !strings.Contains(prompt, section) {
			t.Errorf("CodeStrategy prompt should contain section %q", section)
		}
	}

	// Should contain extracted file paths
	if !strings.Contains(prompt, "/app/middleware/auth.go") {
		t.Error("CodeStrategy prompt should contain extracted file path from edit_file")
	}
	if !strings.Contains(prompt, "/app/middleware/auth_test.go") {
		t.Error("CodeStrategy prompt should contain extracted file path from write_file")
	}
}

func TestCodeStrategy_BuildPrompt_WithSessionNotes(t *testing.T) {
	notes := NewSessionNotes()
	notes.SetObjective("Build REST API")
	notes.RecordFileChange("/app/main.go", "modified", "Added routes")
	notes.RecordTaskCompleted("Set up project structure")

	s := &CodeStrategy{SessionNotes: notes}
	contents := []*genai.Content{
		makeContent("user", "Now add the database layer"),
	}
	prompt := s.BuildSummarizationPrompt(contents)

	if !strings.Contains(prompt, "Pre-built session state") {
		t.Error("CodeStrategy prompt should contain pre-built session state header")
	}
	if !strings.Contains(prompt, "Build REST API") {
		t.Error("CodeStrategy prompt should contain session notes objective")
	}
	if !strings.Contains(prompt, "/app/main.go") {
		t.Error("CodeStrategy prompt should contain session notes file paths")
	}
}

func TestCodeStrategy_ExtractsFilePaths(t *testing.T) {
	contents := []*genai.Content{
		makeFuncCallContent("model", "edit_file", map[string]any{"path": "/a/b.go"}),
		makeFuncCallContent("model", "write_file", map[string]any{"file_path": "/c/d.go"}),
		makeFuncCallContent("model", "read_file", map[string]any{"path": "/e/f.go"}),
		makeFuncCallContent("model", "shell_command", map[string]any{"working_dir": "/proj"}),
		makeFuncCallContent("model", "grep_search", map[string]any{"search_path": "/src"}),
	}

	paths := extractFilePathsFromContents(contents)

	if paths["/a/b.go"] != "edited" {
		t.Errorf("expected /a/b.go -> edited, got %q", paths["/a/b.go"])
	}
	if paths["/c/d.go"] != "written" {
		t.Errorf("expected /c/d.go -> written, got %q", paths["/c/d.go"])
	}
	if paths["/e/f.go"] != "read" {
		t.Errorf("expected /e/f.go -> read, got %q", paths["/e/f.go"])
	}
	if paths["/proj"] != "shell (working_dir)" {
		t.Errorf("expected /proj -> shell (working_dir), got %q", paths["/proj"])
	}
	if paths["/src"] != "searched" {
		t.Errorf("expected /src -> searched, got %q", paths["/src"])
	}
}

func TestCodeStrategy_PromptCap(t *testing.T) {
	s := &CodeStrategy{}
	// Generate a very large conversation
	var contents []*genai.Content
	for i := 0; i < 100; i++ {
		contents = append(contents, makeContent("user", strings.Repeat("x", 1000)))
	}
	prompt := s.BuildSummarizationPrompt(contents)
	if len(prompt) > 40100 { // 40000 + small allowance for truncation suffix
		t.Errorf("CodeStrategy prompt should be capped at ~40000 chars, got %d", len(prompt))
	}
	if !strings.Contains(prompt, "truncated for summarization") {
		t.Error("truncated prompt should contain truncation notice")
	}
}

func TestSummarize_UsesStrategy(t *testing.T) {
	c := NewCompactor(200000)
	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "SUMMARY", nil
	}
	c.SetStrategy(&CodeStrategy{})

	contents := []*genai.Content{
		makeContent("user", "Build a web server"),
		makeFuncCallContent("model", "edit_file", map[string]any{"path": "main.go"}),
		makeFuncResponseContent("user", "edit_file", map[string]any{"status": "ok"}),
	}

	_, err := c.summarize(context.Background(), contents)
	if err != nil {
		t.Fatalf("summarize error: %v", err)
	}

	// Should use CodeStrategy format, not generic
	if !strings.Contains(capturedPrompt, "OBJECTIVE") {
		t.Error("expected CodeStrategy prompt with OBJECTIVE section")
	}
	if strings.Contains(capturedPrompt, "CURRENT TASK:") {
		t.Error("should NOT use GenericStrategy CURRENT TASK format when CodeStrategy is set")
	}
}

func TestSummarize_DefaultsToGenericStrategy(t *testing.T) {
	c := NewCompactor(200000)
	var capturedPrompt string
	c.LLM = func(ctx context.Context, prompt string) (string, error) {
		capturedPrompt = prompt
		return "SUMMARY", nil
	}
	// No strategy set — should use GenericStrategy

	contents := []*genai.Content{
		makeContent("user", "Hello"),
		makeContent("model", "Hi there"),
	}

	_, err := c.summarize(context.Background(), contents)
	if err != nil {
		t.Fatalf("summarize error: %v", err)
	}

	if !strings.Contains(capturedPrompt, "CURRENT TASK") {
		t.Error("default should use GenericStrategy with CURRENT TASK format")
	}
}

func TestCompactor_SetStrategy(t *testing.T) {
	c := NewCompactor(200000)
	if c.StrategyName() != "platform" {
		t.Errorf("default StrategyName() = %q, want 'platform'", c.StrategyName())
	}
	c.SetStrategy(&CodeStrategy{})
	if c.StrategyName() != "code" {
		t.Errorf("after SetStrategy, StrategyName() = %q, want 'code'", c.StrategyName())
	}
	c.SetStrategy(nil)
	if c.StrategyName() != "platform" {
		t.Errorf("after SetStrategy(nil), StrategyName() = %q, want 'platform'", c.StrategyName())
	}
}
