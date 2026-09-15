package a2achan

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
)

func TestA2AChannel_Lifecycle(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	ch := New(&Config{
		TaskStore: store,
		BaseURL:   "http://localhost:9393",
	}, nil)

	if ch.ID() != "a2a" {
		t.Fatalf("expected ID 'a2a', got %q", ch.ID())
	}
	if ch.Name() != "A2A Protocol" {
		t.Fatalf("expected name 'A2A Protocol', got %q", ch.Name())
	}

	// Start
	handler := func(ctx context.Context, msg channels.InboundMessage) error {
		return nil
	}
	if err := ch.Start(context.Background(), handler); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	status := ch.Status()
	if !status.Connected {
		t.Fatal("expected connected after Start")
	}

	// Stop
	if err := ch.Stop(context.Background()); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	status = ch.Status()
	if status.Connected {
		t.Fatal("expected disconnected after Stop")
	}
}

func TestA2AChannel_SendResponse(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	ch := New(&Config{
		TaskStore: store,
		BaseURL:   "http://localhost:9393",
	}, nil)

	// Register pending
	respCh := ch.RegisterPending("task-123")

	// Send response
	target := channels.Target{ThreadID: "task-123"}
	msg := channels.OutboundMessage{Text: "Hello from agent"}
	if err := ch.Send(context.Background(), target, msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Should receive on pending channel
	select {
	case got := <-respCh:
		if got.Text != "Hello from agent" {
			t.Fatalf("expected 'Hello from agent', got %q", got.Text)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for response")
	}

	ch.UnregisterPending("task-123")
}

func TestSessionKey(t *testing.T) {
	// With user ID (identity propagation)
	key := SessionKey("agent-1", "user-123", "ctx-abc")
	if key != "a2a:direct:user-123:ctx-abc" {
		t.Fatalf("unexpected key with user: %q", key)
	}

	// Without user ID (agent-scoped)
	key = SessionKey("agent-1", "", "ctx-abc")
	if key != "a2a:direct:agent-1:ctx-abc" {
		t.Fatalf("unexpected key without user: %q", key)
	}
}

func TestNormalizePartsToText(t *testing.T) {
	parts := []a2a.Part{
		a2a.TextPart{Text: "Hello"},
		a2a.TextPart{Text: "World"},
		a2a.FilePart{Name: "doc.pdf"},
	}
	got := NormalizePartsToText(parts)
	if got != "Hello\nWorld\n[file: doc.pdf]" {
		t.Fatalf("unexpected normalized text: %q", got)
	}
}

func TestNormalizePartsToText_KindPart(t *testing.T) {
	var params a2a.SendMessageParams
	if err := json.Unmarshal([]byte(`{
		"message": {
			"role": "user",
			"parts": [{"kind": "text", "text": "List the devices per site."}]
		}
	}`), &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}

	if got := NormalizePartsToText(params.Message.Parts); got != "List the devices per site." {
		t.Fatalf("normalized text = %q, want %q", got, "List the devices per site.")
	}
}

func TestA2AChannel_HandleGetTask_Scoping(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	ch := New(&Config{
		TaskStore: store,
		BaseURL:   "http://localhost:9393",
	}, nil)

	// Create task owned by agent-1
	task := store.Create("agent-1", "ctx-1")

	// Agent 1 can access
	_, err := ch.HandleGetTask("agent-1", task.ID)
	if err != nil {
		t.Fatalf("agent-1 should access own task: %v", err)
	}

	// Agent 2 cannot access
	_, err = ch.HandleGetTask("agent-2", task.ID)
	if err == nil {
		t.Fatal("agent-2 should NOT access agent-1's task")
	}
}

func TestA2AChannel_HandleSendMessage_Claims(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	ch := New(&Config{
		TaskStore: store,
		BaseURL:   "http://localhost:9393",
	}, nil)

	// Start channel with a handler that sends a response back
	handler := func(ctx context.Context, msg channels.InboundMessage) error {
		// Verify inbound message fields derived from claims
		if msg.SenderID != "user-42" {
			t.Errorf("expected SenderID 'user-42', got %q", msg.SenderID)
		}
		if msg.SenderName != "service-a" {
			t.Errorf("expected SenderName 'service-a', got %q", msg.SenderName)
		}
		if msg.RoutingHint == nil || msg.RoutingHint.OrgSlug != "acme" {
			t.Errorf("expected OrgSlug 'acme', got %+v", msg.RoutingHint)
		}
		// Send response to unblock synchronous wait
		target := channels.Target{ThreadID: msg.ID}
		return ch.Send(ctx, target, channels.OutboundMessage{Text: "done"})
	}
	if err := ch.Start(context.Background(), handler); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer ch.Stop(context.Background())

	// Claims with actor (delegated token)
	claims := &a2a.A2ATokenClaims{
		Issuer:          "https://auth.example.com",
		ActorIdentifier: "service-a",
		UserIdentifier:  "user-42",
		OrgID:           "acme",
	}

	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hello agent"}},
		},
	}

	task, err := ch.HandleSendMessage(context.Background(), claims, params)
	if err != nil {
		t.Fatalf("HandleSendMessage failed: %v", err)
	}

	// Verify task was created with composite agentID
	if task.AgentID != "service-a:user-42" {
		t.Errorf("expected agentID 'service-a:user-42', got %q", task.AgentID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("expected state completed, got %q", task.Status.State)
	}
}

func TestA2AChannel_HandleSendMessage_ReturnsFinalTurn(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	ch := New(&Config{
		TaskStore: store,
		BaseURL:   "http://localhost:9393",
	}, nil)

	// ChannelManager.isBatchChannelID collapses A2A turns before delivery, so
	// the synchronous handler receives one Send containing the final answer.
	handler := func(ctx context.Context, msg channels.InboundMessage) error {
		return ch.Send(ctx, channels.Target{ThreadID: msg.ID}, channels.OutboundMessage{Text: "final answer"})
	}
	if err := ch.Start(context.Background(), handler); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer ch.Stop(context.Background())

	task, err := ch.HandleSendMessage(context.Background(), &a2a.A2ATokenClaims{
		Issuer:         "https://auth.example.com",
		UserIdentifier: "user-direct",
	}, a2a.SendMessageParams{Message: a2a.Message{
		Role:  "user",
		Parts: []a2a.Part{a2a.TextPart{Text: "List the devices per site."}},
	}})
	if err != nil {
		t.Fatalf("HandleSendMessage failed: %v", err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %q, want %q", task.Status.State, a2a.TaskStateCompleted)
	}
	if task.Status.Message == nil || len(task.Status.Message.Parts) != 1 {
		t.Fatalf("status message = %#v, want one text part", task.Status.Message)
	}
	statusText, ok := task.Status.Message.Parts[0].(a2a.TextPart)
	if !ok || statusText.Text != "final answer" {
		t.Fatalf("status message part = %#v, want TextPart{Text: %q}", task.Status.Message.Parts[0], "final answer")
	}
	if len(task.Artifacts) != 1 || len(task.Artifacts[0].Parts) != 1 {
		t.Fatalf("artifacts = %#v, want one response artifact with one part", task.Artifacts)
	}
	artifactText, ok := task.Artifacts[0].Parts[0].(a2a.TextPart)
	if !ok || artifactText.Text != "final answer" {
		t.Fatalf("artifact part = %#v, want TextPart{Text: %q}", task.Artifacts[0].Parts[0], "final answer")
	}
}

func TestA2AChannel_HandleSendMessage_DirectUser(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	defer store.Close()

	ch := New(&Config{
		TaskStore: store,
		BaseURL:   "http://localhost:9393",
	}, nil)

	// Start channel with a handler that sends a response back
	handler := func(ctx context.Context, msg channels.InboundMessage) error {
		if msg.SenderName != "direct" {
			t.Errorf("expected SenderName 'direct' for no-actor claim, got %q", msg.SenderName)
		}
		target := channels.Target{ThreadID: msg.ID}
		return ch.Send(ctx, target, channels.OutboundMessage{Text: "ok"})
	}
	if err := ch.Start(context.Background(), handler); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer ch.Stop(context.Background())

	// Claims without actor (direct user token)
	claims := &a2a.A2ATokenClaims{
		Issuer:         "https://auth.example.com",
		UserIdentifier: "user-direct",
	}

	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hi"}},
		},
	}

	task, err := ch.HandleSendMessage(context.Background(), claims, params)
	if err != nil {
		t.Fatalf("HandleSendMessage failed: %v", err)
	}

	// Verify task was created with user-only agentID (no actor prefix)
	if task.AgentID != "user-direct" {
		t.Errorf("expected agentID 'user-direct', got %q", task.AgentID)
	}
}
