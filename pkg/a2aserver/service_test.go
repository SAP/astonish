package a2aserver

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
)

type testDispatcher struct {
	messages []channels.InboundMessage
	reply    string
	err      error
}

func (d *testDispatcher) dispatch(ctx context.Context, msg channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
	d.messages = append(d.messages, msg)
	if d.err != nil {
		return d.err
	}
	return reply(ctx, channels.OutboundMessage{Text: d.reply})
}

func newTestService(t *testing.T, d *testDispatcher) (*Service, *a2a.InMemoryTaskStore) {
	t.Helper()
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	svc, err := New(Config{TaskStore: store, Dispatcher: d.dispatch})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func TestA2AServiceSendMessageCorrelatesReply(t *testing.T) {
	d := &testDispatcher{reply: "done"}
	svc, _ := newTestService(t, d)
	task, err := svc.SendMessage(context.Background(), Identity{AgentID: "agent:user", UserID: "user", OrgID: "acme"}, a2a.SendMessageParams{Message: a2a.Message{Role: "user", Parts: []a2a.Part{a2a.TextPart{Text: "hello"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status.State != a2a.TaskStateCompleted || task.Status.Message == nil || task.Status.Message.Parts[0].(a2a.TextPart).Text != "done" {
		t.Fatalf("unexpected task: %+v", task.Status)
	}
	if len(d.messages) != 1 || d.messages[0].ThreadID != "a2a:direct:user:"+task.ContextID || d.messages[0].RoutingHint == nil {
		t.Fatalf("unexpected dispatch: %+v", d.messages)
	}
}

func TestA2AServiceAsyncCompletionAndOwnership(t *testing.T) {
	d := &testDispatcher{reply: "async"}
	svc, _ := newTestService(t, d)
	task, err := svc.SendMessage(context.Background(), Identity{AgentID: "owner"}, a2a.SendMessageParams{Message: a2a.Message{Role: "user"}, Configuration: &a2a.TaskConfig{ReturnImmediately: true}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for task.Status.State != a2a.TaskStateCompleted && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		task, _ = svc.GetTask("owner", task.ID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("async task not completed: %s", task.Status.State)
	}
	if _, err := svc.GetTask("other", task.ID); err == nil {
		t.Fatal("cross-principal task access succeeded")
	}
	if err := svc.CancelTask("other", task.ID); err == nil {
		t.Fatal("cross-principal cancellation succeeded")
	}
}

func TestA2AServiceNormalizeParts(t *testing.T) {
	got := NormalizePartsToText([]a2a.Part{a2a.TextPart{Text: "Hello"}, a2a.FilePart{Name: "doc.pdf"}})
	if got != "Hello\n[file: doc.pdf]" {
		t.Fatalf("got %q", got)
	}
}
