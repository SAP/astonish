package a2a

import (
	"testing"
	"time"
)

func TestInMemoryTaskStore_CreateAndGet(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	task := store.Create("agent-1", "ctx-123")
	if task.ID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if task.AgentID != "agent-1" {
		t.Fatalf("expected agentID 'agent-1', got %q", task.AgentID)
	}
	if task.ContextID != "ctx-123" {
		t.Fatalf("expected contextID 'ctx-123', got %q", task.ContextID)
	}
	if task.Status.State != TaskStateSubmitted {
		t.Fatalf("expected state submitted, got %q", task.Status.State)
	}

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.ID != task.ID {
		t.Fatalf("expected task %s, got %s", task.ID, got.ID)
	}
}

func TestInMemoryTaskStore_GetNotFound(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	_, err := store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestInMemoryTaskStore_StateTransitions(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	task := store.Create("agent-1", "ctx-1")

	// Valid: submitted -> working
	if err := store.UpdateState(task.ID, TaskStateWorking, nil); err != nil {
		t.Fatalf("submitted->working failed: %v", err)
	}

	// Valid: working -> completed
	msg := &Message{Role: "agent", Parts: []Part{TextPart{Text: "Done"}}}
	if err := store.UpdateState(task.ID, TaskStateCompleted, msg); err != nil {
		t.Fatalf("working->completed failed: %v", err)
	}

	// Invalid: completed -> working (terminal state)
	if err := store.UpdateState(task.ID, TaskStateWorking, nil); err == nil {
		t.Fatal("expected error for completed->working transition")
	}
}

func TestInMemoryTaskStore_InvalidTransition(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	task := store.Create("agent-1", "ctx-1")

	// Invalid: submitted -> completed (must go through working)
	if err := store.UpdateState(task.ID, TaskStateCompleted, nil); err == nil {
		t.Fatal("expected error for submitted->completed transition")
	}
}

func TestInMemoryTaskStore_AgentScoping(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	store.Create("agent-1", "ctx-1")
	store.Create("agent-1", "ctx-2")
	store.Create("agent-2", "ctx-3")

	agent1Tasks := store.GetByAgent("agent-1", TaskFilter{})
	if len(agent1Tasks) != 2 {
		t.Fatalf("expected 2 tasks for agent-1, got %d", len(agent1Tasks))
	}

	agent2Tasks := store.GetByAgent("agent-2", TaskFilter{})
	if len(agent2Tasks) != 1 {
		t.Fatalf("expected 1 task for agent-2, got %d", len(agent2Tasks))
	}
}

func TestInMemoryTaskStore_PushConfig(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	task := store.Create("agent-1", "ctx-1")

	cfg := PushNotificationConfig{URL: "https://example.com/callback", Token: "secret"}
	if err := store.SetPushConfig(task.ID, cfg); err != nil {
		t.Fatalf("SetPushConfig failed: %v", err)
	}

	got := store.GetPushConfig(task.ID)
	if got == nil || got.URL != cfg.URL {
		t.Fatalf("expected push config with URL %s", cfg.URL)
	}

	if err := store.DeletePushConfig(task.ID); err != nil {
		t.Fatalf("DeletePushConfig failed: %v", err)
	}
	if store.GetPushConfig(task.ID) != nil {
		t.Fatal("expected nil push config after delete")
	}
}

func TestInMemoryTaskStore_AddArtifact(t *testing.T) {
	store := NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	task := store.Create("agent-1", "ctx-1")
	artifact := Artifact{
		Name:  "report.md",
		Parts: []Part{TextPart{Text: "# Report"}},
	}
	if err := store.AddArtifact(task.ID, artifact); err != nil {
		t.Fatalf("AddArtifact failed: %v", err)
	}

	got, _ := store.Get(task.ID)
	if len(got.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(got.Artifacts))
	}
	if got.Artifacts[0].Name != "report.md" {
		t.Fatalf("expected artifact name 'report.md', got %q", got.Artifacts[0].Name)
	}
}

func TestTaskState_ValidTransitions(t *testing.T) {
	tests := []struct {
		from  TaskState
		to    TaskState
		valid bool
	}{
		{TaskStateSubmitted, TaskStateWorking, true},
		{TaskStateSubmitted, TaskStateRejected, true},
		{TaskStateSubmitted, TaskStateCanceled, true},
		{TaskStateSubmitted, TaskStateCompleted, false},
		{TaskStateWorking, TaskStateCompleted, true},
		{TaskStateWorking, TaskStateFailed, true},
		{TaskStateWorking, TaskStateCanceled, true},
		{TaskStateWorking, TaskStateInputRequired, true},
		{TaskStateWorking, TaskStateAuthRequired, true},
		{TaskStateWorking, TaskStateSubmitted, false},
		{TaskStateInputRequired, TaskStateWorking, true},
		{TaskStateInputRequired, TaskStateCanceled, true},
		{TaskStateInputRequired, TaskStateCompleted, false},
		{TaskStateCompleted, TaskStateWorking, false},
		{TaskStateFailed, TaskStateWorking, false},
		{TaskStateCanceled, TaskStateWorking, false},
	}
	for _, tt := range tests {
		got := tt.from.ValidTransition(tt.to)
		if got != tt.valid {
			t.Errorf("%s -> %s: expected %v, got %v", tt.from, tt.to, tt.valid, got)
		}
	}
}
