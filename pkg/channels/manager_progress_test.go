package channels

import (
	"reflect"
	"testing"

	"google.golang.org/genai"
)

type managerProgressSink struct {
	texts  []string
	events []ProgressEvent
}

func (s *managerProgressSink) Progress(text string) {
	s.texts = append(s.texts, text)
}

func (s *managerProgressSink) ProgressEvent(event ProgressEvent) {
	s.events = append(s.events, event)
}

func TestAdvanceBatchTextEmitsOnlyProvenIntermediateTurns(t *testing.T) {
	sink := &managerProgressSink{}
	var final string

	final = advanceBatchText(sink, final, "activity 1", true)
	if !reflect.DeepEqual(sink.texts, []string{"activity 1"}) {
		t.Fatalf("tool-bearing progress was not emitted immediately: %v", sink.texts)
	}
	final = advanceBatchText(sink, final, "activity 2", false)
	final = advanceBatchText(sink, final, "final answer", false)

	want := []string{"activity 1", "activity 2"}
	if !reflect.DeepEqual(sink.texts, want) {
		t.Fatalf("progress texts = %v, want %v", sink.texts, want)
	}
	if final != "final answer" {
		t.Fatalf("final text = %q, want final answer", final)
	}
}

func TestAdvanceBatchTextFlushesPendingAtToolBoundary(t *testing.T) {
	sink := &managerProgressSink{}
	final := advanceBatchText(sink, "I’ll inspect the inventory.", "", true)

	if !reflect.DeepEqual(sink.texts, []string{"I’ll inspect the inventory."}) {
		t.Fatalf("pending narration was not emitted when tool execution began: %v", sink.texts)
	}
	if final != "" {
		t.Fatalf("final text = %q, want empty while tool execution continues", final)
	}
}

func TestAdvanceBatchTextDoesNotDeduplicateEqualTurns(t *testing.T) {
	sink := &managerProgressSink{}
	final := advanceBatchText(sink, "same text", "same text", false)

	if !reflect.DeepEqual(sink.texts, []string{"same text"}) {
		t.Fatalf("equal but distinct progress turn was suppressed: %v", sink.texts)
	}
	if final != "same text" {
		t.Fatalf("final text = %q, want same text", final)
	}
}

func TestEmitProgress_NilSinkIsNoOp(t *testing.T) {
	emitProgress(nil, "ignored")
}

func TestEmitToolProgressPreservesLifecycleWithoutPayloads(t *testing.T) {
	sink := &managerProgressSink{}

	emitToolProgress(sink, ProgressEvent{Kind: ProgressToolStarted, ToolName: "perplexity_web_search"})
	emitToolProgress(sink, ProgressEvent{Kind: ProgressToolCompleted, ToolName: "perplexity_web_search"})

	want := []ProgressEvent{
		{Kind: ProgressToolStarted, ToolName: "perplexity_web_search"},
		{Kind: ProgressToolCompleted, ToolName: "perplexity_web_search"},
	}
	if !reflect.DeepEqual(sink.events, want) {
		t.Fatalf("tool progress events = %#v, want %#v", sink.events, want)
	}
	if len(sink.texts) != 0 {
		t.Fatalf("tool progress leaked into legacy text sink: %v", sink.texts)
	}
}

func TestProgressToolTrackerReportsEffectiveExecuteToolIdentity(t *testing.T) {
	tracker := newProgressToolTracker()
	call := &genai.FunctionCall{
		ID:   "call-1",
		Name: "execute_tool",
		Args: map[string]any{
			"name":      "perplexity_web_search",
			"arguments": map[string]any{"query": "Apple news", "token": "secret"},
		},
	}

	if got := tracker.call(call); got != "perplexity_web_search" {
		t.Fatalf("call identity = %q, want perplexity_web_search", got)
	}
	if got := tracker.response(&genai.FunctionResponse{ID: "call-1", Name: "execute_tool"}); got != "perplexity_web_search" {
		t.Fatalf("response identity = %q, want perplexity_web_search", got)
	}
}

func TestProgressToolTrackerKeepsDiscoveryToolIdentity(t *testing.T) {
	tracker := newProgressToolTracker()
	if got := tracker.call(&genai.FunctionCall{Name: "search_tools"}); got != "search_tools" {
		t.Fatalf("call identity = %q, want search_tools", got)
	}
	if got := tracker.response(&genai.FunctionResponse{Name: "search_tools"}); got != "search_tools" {
		t.Fatalf("response identity = %q, want search_tools", got)
	}
}
