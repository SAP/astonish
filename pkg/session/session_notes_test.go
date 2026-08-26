package session

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSessionNotes_NewIsEmpty(t *testing.T) {
	n := NewSessionNotes()
	if !n.IsEmpty() {
		t.Error("new SessionNotes should be empty")
	}
}

func TestSessionNotes_RecordFileChange(t *testing.T) {
	n := NewSessionNotes()
	n.RecordFileChange("/app/main.go", "modified", "Added main function")

	if n.IsEmpty() {
		t.Error("should not be empty after recording file change")
	}
	n.mu.RLock()
	note, ok := n.FilesModified["/app/main.go"]
	n.mu.RUnlock()
	if !ok {
		t.Error("file should be recorded")
	} else {
		if note.Action != "modified" {
			t.Errorf("action = %q, want 'modified'", note.Action)
		}
		if note.Description != "Added main function" {
			t.Errorf("description = %q, want 'Added main function'", note.Description)
		}
	}
}

func TestSessionNotes_RecordFileChange_DeduplicatesAndUpgrades(t *testing.T) {
	n := NewSessionNotes()
	n.RecordFileChange("/app/main.go", "read", "Initial read")
	n.RecordFileChange("/app/main.go", "modified", "Added routes")

	n.mu.RLock()
	count := len(n.FilesModified)
	note := n.FilesModified["/app/main.go"]
	n.mu.RUnlock()

	if count != 1 {
		t.Errorf("should deduplicate: got %d entries, want 1", count)
	}
	if note.Action != "modified" {
		t.Errorf("action should be upgraded to 'modified', got %q", note.Action)
	}
	if note.Description != "Added routes" {
		t.Errorf("description should be updated, got %q", note.Description)
	}
}

func TestSessionNotes_RecordFileChange_DoesNotDowngrade(t *testing.T) {
	n := NewSessionNotes()
	n.RecordFileChange("/app/main.go", "modified", "Edited")
	n.RecordFileChange("/app/main.go", "read", "Re-read")

	n.mu.RLock()
	note := n.FilesModified["/app/main.go"]
	n.mu.RUnlock()
	if note.Action != "modified" {
		t.Errorf("action should NOT be downgraded: got %q, want 'modified'", note.Action)
	}
}

func TestSessionNotes_TaskLifecycle(t *testing.T) {
	n := NewSessionNotes()
	n.AddTaskPending("implement auth")
	n.AddTaskPending("write tests")

	n.mu.RLock()
	pendingCount := len(n.TasksPending)
	n.mu.RUnlock()
	if pendingCount != 2 {
		t.Fatalf("pending = %d, want 2", pendingCount)
	}

	n.RemoveTaskPending("implement auth")

	n.mu.RLock()
	pendingCount = len(n.TasksPending)
	completedCount := len(n.TasksCompleted)
	firstCompleted := ""
	if completedCount > 0 {
		firstCompleted = n.TasksCompleted[0]
	}
	n.mu.RUnlock()

	if pendingCount != 1 {
		t.Errorf("pending after remove = %d, want 1", pendingCount)
	}
	if completedCount != 1 {
		t.Errorf("completed after remove = %d, want 1", completedCount)
	}
	if firstCompleted != "implement auth" {
		t.Errorf("completed[0] = %q, want 'implement auth'", firstCompleted)
	}
}

func TestSessionNotes_RecordTaskCompleted(t *testing.T) {
	n := NewSessionNotes()
	n.RecordTaskCompleted("setup project")
	n.RecordTaskCompleted("configure CI")

	n.mu.RLock()
	count := len(n.TasksCompleted)
	n.mu.RUnlock()
	if count != 2 {
		t.Errorf("completed = %d, want 2", count)
	}
}

func TestSessionNotes_RecordDecision(t *testing.T) {
	n := NewSessionNotes()
	n.RecordDecision("Use PostgreSQL for persistence")
	n.RecordDecision("Interface-based DI")

	n.mu.RLock()
	count := len(n.Decisions)
	n.mu.RUnlock()
	if count != 2 {
		t.Errorf("decisions = %d, want 2", count)
	}
}

func TestSessionNotes_RecordError(t *testing.T) {
	n := NewSessionNotes()
	n.RecordError("compilation failed", "fixed import", true)
	n.RecordError("test timeout", "", false)

	n.mu.RLock()
	errCount := len(n.Errors)
	firstResolved := n.Errors[0].Resolved
	secondResolved := n.Errors[1].Resolved
	n.mu.RUnlock()

	if errCount != 2 {
		t.Fatalf("errors = %d, want 2", errCount)
	}
	if !firstResolved {
		t.Error("first error should be resolved")
	}
	if secondResolved {
		t.Error("second error should be unresolved")
	}
}

func TestSessionNotes_SetObjective(t *testing.T) {
	n := NewSessionNotes()
	n.SetObjective("Build REST API")
	n.mu.RLock()
	obj := n.Objective
	n.mu.RUnlock()
	if obj != "Build REST API" {
		t.Errorf("objective = %q, want 'Build REST API'", obj)
	}
	if n.IsEmpty() {
		t.Error("should not be empty after setting objective")
	}
}

func TestSessionNotes_SetCurrentState(t *testing.T) {
	n := NewSessionNotes()
	n.SetCurrentState("Writing authentication middleware")
	n.mu.RLock()
	state := n.CurrentState
	n.mu.RUnlock()
	if state != "Writing authentication middleware" {
		t.Errorf("current state mismatch")
	}
}

func TestSessionNotes_Render(t *testing.T) {
	n := NewSessionNotes()
	n.SetObjective("Build REST API")
	n.RecordFileChange("/app/main.go", "modified", "Added routes")
	n.RecordTaskCompleted("Project setup")
	n.AddTaskPending("Add tests")
	n.RecordDecision("Use chi router")
	n.RecordError("compile error", "fixed import", true)
	n.SetCurrentState("Implementing handlers")

	output := n.Render()

	for _, section := range []string{
		"## OBJECTIVE",
		"## FILES MODIFIED",
		"## TASKS COMPLETED",
		"## TASKS PENDING",
		"## KEY DECISIONS",
		"## ERRORS & FIXES",
		"## CURRENT STATE",
	} {
		if !strings.Contains(output, section) {
			t.Errorf("render output should contain %q", section)
		}
	}

	if !strings.Contains(output, "Build REST API") {
		t.Error("render should contain objective")
	}
	if !strings.Contains(output, "/app/main.go") {
		t.Error("render should contain file path")
	}
	if !strings.Contains(output, "Project setup") {
		t.Error("render should contain completed task")
	}
	if !strings.Contains(output, "Add tests") {
		t.Error("render should contain pending task")
	}
	if !strings.Contains(output, "Use chi router") {
		t.Error("render should contain decision")
	}
	if !strings.Contains(output, "compile error") {
		t.Error("render should contain error")
	}
	if !strings.Contains(output, "Implementing handlers") {
		t.Error("render should contain current state")
	}
}

func TestSessionNotes_Render_Empty(t *testing.T) {
	n := NewSessionNotes()
	output := n.Render()

	// Should still render all sections with defaults
	if !strings.Contains(output, "(not yet determined)") {
		t.Error("empty objective should show placeholder")
	}
	if !strings.Contains(output, "(none yet)") {
		t.Error("empty sections should show placeholder")
	}
}

func TestSessionNotes_RecordToolOutcome_EditFile(t *testing.T) {
	n := NewSessionNotes()
	n.RecordToolOutcome("edit_file", map[string]any{"path": "/app/handler.go"}, nil, nil)

	n.mu.RLock()
	note, ok := n.FilesModified["/app/handler.go"]
	n.mu.RUnlock()
	if !ok {
		t.Error("edit_file should record file")
	} else if note.Action != "modified" {
		t.Errorf("action = %q, want 'modified'", note.Action)
	}
}

func TestSessionNotes_RecordToolOutcome_WriteFile(t *testing.T) {
	n := NewSessionNotes()
	n.RecordToolOutcome("write_file", map[string]any{"file_path": "/app/new.go"}, nil, nil)

	n.mu.RLock()
	note, ok := n.FilesModified["/app/new.go"]
	n.mu.RUnlock()
	if !ok {
		t.Error("write_file should record file")
	} else if note.Action != "created" {
		t.Errorf("action = %q, want 'created'", note.Action)
	}
}

func TestSessionNotes_RecordToolOutcome_ReadFile(t *testing.T) {
	n := NewSessionNotes()
	n.RecordToolOutcome("read_file", map[string]any{"path": "/app/config.go"}, nil, nil)

	n.mu.RLock()
	note, ok := n.FilesModified["/app/config.go"]
	n.mu.RUnlock()
	if !ok {
		t.Error("read_file should record file")
	} else if note.Action != "read" {
		t.Errorf("action = %q, want 'read'", note.Action)
	}
}

func TestSessionNotes_RecordToolOutcome_Error(t *testing.T) {
	n := NewSessionNotes()
	n.RecordToolOutcome("shell_command", map[string]any{"command": "go build"}, nil, fmt.Errorf("exit code 1"))

	n.mu.RLock()
	errCount := len(n.Errors)
	firstErr := ""
	if errCount > 0 {
		firstErr = n.Errors[0].Error
	}
	n.mu.RUnlock()

	if errCount != 1 {
		t.Fatalf("errors = %d, want 1", errCount)
	}
	if !strings.Contains(firstErr, "shell_command failed") {
		t.Errorf("error = %q, should contain 'shell_command failed'", firstErr)
	}
}

func TestSessionNotes_RecordToolOutcome_ShellExitCode(t *testing.T) {
	n := NewSessionNotes()
	n.RecordToolOutcome("shell_command",
		map[string]any{"command": "go test ./..."},
		map[string]any{"exit_code": float64(1)},
		nil,
	)

	n.mu.RLock()
	errCount := len(n.Errors)
	firstErr := ""
	if errCount > 0 {
		firstErr = n.Errors[0].Error
	}
	n.mu.RUnlock()

	if errCount != 1 {
		t.Fatalf("errors = %d, want 1", errCount)
	}
	if !strings.Contains(firstErr, "go test") {
		t.Errorf("error should mention command, got %q", firstErr)
	}
}

func TestSessionNotes_Clone(t *testing.T) {
	n := NewSessionNotes()
	n.SetObjective("Test cloning")
	n.RecordFileChange("/a.go", "modified", "change")
	n.RecordTaskCompleted("task1")

	clone := n.Clone()

	// Modify original
	n.SetObjective("Changed")
	n.RecordFileChange("/b.go", "created", "new")

	// Clone should be unchanged
	if clone.Objective != "Test cloning" {
		t.Errorf("clone objective mutated: %q", clone.Objective)
	}
	if len(clone.FilesModified) != 1 {
		t.Errorf("clone files mutated: %d", len(clone.FilesModified))
	}
}

func TestSessionNotes_ThreadSafety(t *testing.T) {
	n := NewSessionNotes()
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n.RecordFileChange(fmt.Sprintf("/file%d.go", i), "modified", "concurrent")
		}(i)
	}
	wg.Wait()

	n.mu.RLock()
	count := len(n.FilesModified)
	n.mu.RUnlock()
	if count != 100 {
		t.Errorf("expected 100 files, got %d", count)
	}
}

func TestSessionNotes_ActionPriority(t *testing.T) {
	tests := []struct {
		action   string
		priority int
	}{
		{"created", 4},
		{"deleted", 3},
		{"modified", 2},
		{"read", 1},
		{"unknown", 0},
	}
	for _, tt := range tests {
		if got := actionPriority(tt.action); got != tt.priority {
			t.Errorf("actionPriority(%q) = %d, want %d", tt.action, got, tt.priority)
		}
	}
}
