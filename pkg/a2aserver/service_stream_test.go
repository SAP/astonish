package a2aserver

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
)

func TestSendMessageStream_EmitsToolLifecycleWithoutArgumentsOrResults(t *testing.T) {
	var events []a2a.TaskStatusUpdateEvent
	sink := streamProgressSink{taskID: "task-1", emit: func(event a2a.TaskStatusUpdateEvent) {
		events = append(events, event)
	}}

	sink.ProgressEvent(channels.ProgressEvent{Kind: channels.ProgressToolStarted, ToolName: "perplexity_web_search"})
	sink.ProgressEvent(channels.ProgressEvent{Kind: channels.ProgressToolCompleted, ToolName: "perplexity_web_search"})

	if len(events) != 2 {
		t.Fatalf("got %d lifecycle events, want 2", len(events))
	}
	got := []string{
		events[0].Status.Message.Parts[0].(a2a.TextPart).Text,
		events[1].Status.Message.Parts[0].(a2a.TextPart).Text,
	}
	want := []string{"Using Perplexity Web Search…", "Perplexity Web Search completed."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle text = %q, want %q", got, want)
	}
	for _, text := range got {
		if strings.Contains(text, "query") || strings.Contains(text, "result") {
			t.Fatalf("lifecycle text leaked tool payload: %q", text)
		}
	}
}

func TestSendMessageStream_EmitsWorkingProgressThenCompleted(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := New(Config{
		TaskStore: store,
		Dispatcher: func(ctx context.Context, _ channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
			sink := channels.ProgressSinkFromContext(ctx)
			if sink == nil {
				t.Fatal("stream dispatch context is missing ProgressSink")
			}
			sink.Progress("activity 1")
			return reply(ctx, channels.OutboundMessage{Text: "final"})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []a2a.TaskStatusUpdateEvent
	task, err := service.SendMessageStream(
		context.Background(),
		testIdentity("owner"),
		a2a.SendMessageParams{Message: a2a.Message{Role: "user", Parts: []a2a.Part{a2a.TextPart{Text: "hello"}}}},
		func(event a2a.TaskStatusUpdateEvent) { events = append(events, event) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3: %+v", len(events), events)
	}
	if events[0].Status.State != a2a.TaskStateWorking {
		t.Fatalf("first state = %q, want working", events[0].Status.State)
	}
	if events[0].Status.Message != nil {
		t.Fatalf("initial working event contains message: %+v", events[0].Status.Message)
	}
	progress := events[1].Status.Message.Parts[0].(a2a.TextPart).Text
	if events[1].Status.State != a2a.TaskStateWorking || progress != "activity 1" {
		t.Fatalf("progress event = %+v, want working/activity 1", events[1])
	}
	if events[2].Status.State != a2a.TaskStateCompleted {
		t.Fatalf("terminal state = %q, want completed", events[2].Status.State)
	}
	if events[2].Status.Message != nil {
		t.Fatalf("terminal event contains duplicated response: %+v", events[2].Status.Message)
	}
	if task.Status.State != a2a.TaskStateCompleted || len(task.Artifacts) != 1 {
		t.Fatalf("final task = %+v, want completed task with artifact", task)
	}
	if got := task.Artifacts[0].Parts[0].(a2a.TextPart).Text; got != "final" {
		t.Fatalf("artifact text = %q, want final", got)
	}
}

func TestSendMessage_UnchangedNoSink(t *testing.T) {
	store := a2a.NewInMemoryTaskStore(time.Hour)
	t.Cleanup(store.Close)
	service, err := New(Config{
		TaskStore: store,
		Dispatcher: func(ctx context.Context, _ channels.InboundMessage, reply func(context.Context, channels.OutboundMessage) error) error {
			if sink := channels.ProgressSinkFromContext(ctx); sink != nil {
				t.Fatalf("SendMessage dispatch context unexpectedly has ProgressSink %T", sink)
			}
			return reply(ctx, channels.OutboundMessage{Text: "done"})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	task, err := service.SendMessage(context.Background(), testIdentity("owner"), a2a.SendMessageParams{Message: a2a.Message{Role: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %q, want completed", task.Status.State)
	}
}
