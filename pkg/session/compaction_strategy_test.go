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

	// Should contain all 8 sections
	for _, section := range []string{"OBJECTIVE", "FILES MODIFIED", "TASKS COMPLETED", "TASKS PENDING", "KEY DECISIONS", "ERRORS & FIXES", "PENDING USER REQUESTS", "CURRENT STATE"} {
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

func TestCodeStrategy_ImageAttachmentNoted(t *testing.T) {
	s := &CodeStrategy{}
	contents := []*genai.Content{
		makeContent("user", "Look at this bug"),
		{
			Parts: []*genai.Part{
				{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte("fake")}},
			},
			Role: "user",
		},
		makeContent("model", "I can see the issue"),
	}
	prompt := s.BuildSummarizationPrompt(contents)

	if !strings.Contains(prompt, "[user attached a file/image]") {
		t.Error("CodeStrategy prompt should note image attachments")
	}
}

func TestCodeStrategy_UserMessageHigherTextBudget(t *testing.T) {
	s := &CodeStrategy{}
	// Create a user message that's between 800 and 2000 chars — it should NOT be truncated
	userText := strings.Repeat("This is a detailed bug report describing the issue. ", 25) // ~1275 chars
	modelText := strings.Repeat("The model analyzes the issue thoroughly. ", 25)           // ~1000 chars

	contents := []*genai.Content{
		makeContent("user", userText),
		makeContent("model", modelText),
	}
	prompt := s.BuildSummarizationPrompt(contents)

	// User text should NOT be truncated (it's under 2000)
	if strings.Contains(prompt, "This is a detailed bug report describing the issue. This is a detailed") && strings.Contains(prompt, "...") {
		// Check that user text appears fully
	}
	// Just verify the user text is longer than the model text in the prompt
	userIdx := strings.Index(prompt, "[user]:")
	modelIdx := strings.Index(prompt, "[model]:")
	if userIdx < 0 || modelIdx < 0 {
		t.Fatal("expected both user and model text in prompt")
	}
	userSection := prompt[userIdx:modelIdx]
	modelSection := prompt[modelIdx:]

	// User text (~1275 chars) should survive fully; model text (~1000 chars) should be truncated at 800
	if len(userSection) < 1200 {
		t.Errorf("user text should be fully preserved (len=%d), expected >1200", len(userSection))
	}
	// Model text gets 800 char limit, so the model section should be shorter
	if len(modelSection) > 900 {
		t.Errorf("model text should be truncated to ~800 chars (section len=%d)", len(modelSection))
	}
}

func TestGenericStrategy_ImageAttachmentNoted(t *testing.T) {
	s := &GenericStrategy{}
	contents := []*genai.Content{
		makeContent("user", "Here's a screenshot"),
		{
			Parts: []*genai.Part{
				{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte("fake")}},
			},
			Role: "user",
		},
		makeContent("model", "I see the screenshot"),
	}
	prompt := s.BuildSummarizationPrompt(contents)

	if !strings.Contains(prompt, "[user attached a file/image]") {
		t.Error("GenericStrategy prompt should note image attachments")
	}
}

func TestSessionNotes_PendingRequestsRendered(t *testing.T) {
	notes := NewSessionNotes()
	notes.PendingRequests = []string{
		"Debug panel shows no injection data",
		"memory_search not called proactively",
	}
	rendered := notes.Render()

	if !strings.Contains(rendered, "PENDING USER REQUESTS") {
		t.Error("Render should include PENDING USER REQUESTS section")
	}
	if !strings.Contains(rendered, "Debug panel shows no injection data") {
		t.Error("Render should include pending request text")
	}
	if !strings.Contains(rendered, "memory_search not called proactively") {
		t.Error("Render should include all pending requests")
	}
}

func TestSessionNotes_PendingRequestsInIsEmpty(t *testing.T) {
	notes := NewSessionNotes()
	if !notes.IsEmpty() {
		t.Error("fresh notes should be empty")
	}
	notes.PendingRequests = []string{"fix the bug"}
	if notes.IsEmpty() {
		t.Error("notes with pending requests should NOT be empty")
	}
}
