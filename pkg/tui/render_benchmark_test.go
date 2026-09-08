package tui

import (
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

// BenchmarkRenderTranscript200Items measures refreshViewport with a fully
// populated 200-item session. After the first call the item render cache is
// warm, so subsequent iterations exercise the cached fast path — the expected
// bottleneck in long-running sessions.
func BenchmarkRenderTranscript200Items(b *testing.B) {
	m := newCodeModel(b)
	for i := 0; i < 50; i++ {
		m.tr.Apply(events.Event{Kind: events.KindUser, Text: "Tell me about feature"})
		m.tr.Apply(events.Event{Kind: events.KindText, Text: "Here is a response with code:\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\nExplanation."})
		m.tr.Apply(events.Event{Kind: events.KindToolCall, ToolName: "read_file"})
		m.tr.Apply(events.Event{Kind: events.KindToolResult, ToolName: "read_file", Text: "file contents"})
		m.tr.Apply(events.Event{Kind: events.KindDone})
	}
	// Warm up the cache.
	m.refreshViewport()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.refreshViewport()
	}
}

// BenchmarkRenderTranscriptWithSelection measures refreshViewport when a drag
// selection spans part of the transcript. This was the most painful path before
// the fix: applySelectionToBlock was called for every item in the transcript.
func BenchmarkRenderTranscriptWithSelection(b *testing.B) {
	m := newCodeModel(b)
	for i := 0; i < 50; i++ {
		m.tr.Apply(events.Event{Kind: events.KindUser, Text: "Question"})
		m.tr.Apply(events.Event{Kind: events.KindText, Text: "Response text with some **bold** and _italic_ content."})
		m.tr.Apply(events.Event{Kind: events.KindDone})
	}
	// Warm up the cache.
	m.refreshViewport()
	// Simulate an active drag selection spanning a narrow range in the middle.
	m.selecting = true
	m.selectionMoved = true
	m.selectionStart = selectionPoint{line: 10, col: 0}
	m.selectionEnd = selectionPoint{line: 14, col: 20}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.refreshViewport()
	}
}

// BenchmarkRenderTranscriptColdCache measures the cold-cache path — first call
// after a session load where nothing is cached yet.
func BenchmarkRenderTranscriptColdCache(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		m := newCodeModel(b)
		for j := 0; j < 50; j++ {
			m.tr.Apply(events.Event{Kind: events.KindUser, Text: "Question"})
			m.tr.Apply(events.Event{Kind: events.KindText, Text: "Response"})
			m.tr.Apply(events.Event{Kind: events.KindDone})
		}
		b.StartTimer()
		m.refreshViewport()
	}
}
