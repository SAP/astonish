package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func makeEvent(id, author, text string) *adksession.Event {
	return &adksession.Event{
		ID:        id,
		Author:    author,
		Timestamp: time.Now(),
		Actions:   adksession.EventActions{},
		LLMResponse: adkmodel.LLMResponse{
			Content: genai.NewContentFromText(text, genai.RoleUser),
		},
	}
}

func TestTranscript_WriteHeaderAndReadEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-001"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	if !tr.Exists() {
		t.Fatal("Exists() = false after WriteHeader")
	}

	events, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 0 {
		t.Errorf("ReadEvents() len = %d, want 0", len(events))
	}
}

func TestTranscript_AppendAndReadEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-002"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	events := []*adksession.Event{
		makeEvent("e1", "user", "Hello"),
		makeEvent("e2", "model", "Hi there"),
		makeEvent("e3", "user", "How are you?"),
	}

	for _, ev := range events {
		if err := tr.AppendEvent(ev); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", ev.ID, err)
		}
	}

	got, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ReadEvents() len = %d, want 3", len(got))
	}

	for i, ev := range got {
		if ev.ID != events[i].ID {
			t.Errorf("event[%d].ID = %q, want %q", i, ev.ID, events[i].ID)
		}
		if ev.Author != events[i].Author {
			t.Errorf("event[%d].Author = %q, want %q", i, ev.Author, events[i].Author)
		}
	}
}

func TestTranscript_EventCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-003"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	if count := tr.EventCount(); count != 0 {
		t.Errorf("EventCount() = %d, want 0", count)
	}

	for i := 0; i < 5; i++ {
		if err := tr.AppendEvent(makeEvent("e", "user", "msg")); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	if count := tr.EventCount(); count != 5 {
		t.Errorf("EventCount() = %d, want 5", count)
	}
}

func TestTranscript_ExistsNonexistent(t *testing.T) {
	tr := NewTranscript(filepath.Join(t.TempDir(), "nope.jsonl"))
	if tr.Exists() {
		t.Error("Exists() = true for nonexistent file")
	}
}

func TestTranscript_ReadEventsNonexistent(t *testing.T) {
	tr := NewTranscript(filepath.Join(t.TempDir(), "nope.jsonl"))
	events, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v, want nil for nonexistent file", err)
	}
	if len(events) != 0 {
		t.Errorf("ReadEvents() len = %d, want 0", len(events))
	}
}

func TestTranscript_EventWithStateDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-004"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	ev := makeEvent("e1", "model", "response")
	ev.Actions.StateDelta = map[string]any{
		"key1": "value1",
		"key2": float64(42),
	}

	if err := tr.AppendEvent(ev); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	got, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReadEvents() len = %d, want 1", len(got))
	}

	delta := got[0].Actions.StateDelta
	if delta == nil {
		t.Fatal("StateDelta is nil")
		return
	}
	if v, ok := delta["key1"].(string); !ok || v != "value1" {
		t.Errorf("StateDelta[key1] = %v, want %q", delta["key1"], "value1")
	}
	if v, ok := delta["key2"].(float64); !ok || v != 42 {
		t.Errorf("StateDelta[key2] = %v, want 42", delta["key2"])
	}
}

func TestTranscript_WriteHeaderCreatesSubdirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-005"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	if !tr.Exists() {
		t.Error("Exists() = false after WriteHeader with nested dirs")
	}
}

func TestTranscript_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-006"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	// Append a valid event
	if err := tr.AppendEvent(makeEvent("e1", "user", "hello")); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	// Manually inject a malformed line
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	f.Write([]byte("this is not json\n"))
	f.Close()

	// Append another valid event
	if err := tr.AppendEvent(makeEvent("e2", "model", "world")); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	got, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ReadEvents() len = %d, want 2 (malformed line skipped)", len(got))
	}
}

func TestTranscript_LargeEventContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	if err := tr.WriteHeader("sess-007"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}

	// Create an event with a large text body (100KB)
	largeText := strings.Repeat("A", 100*1024)
	ev := makeEvent("big", "model", largeText)

	if err := tr.AppendEvent(ev); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	got, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReadEvents() len = %d, want 1", len(got))
	}

	// Verify content survived round-trip
	parts := got[0].Content.Parts
	if len(parts) == 0 {
		t.Fatal("Content.Parts is empty")
		return
	}
	if parts[0].Text != largeText {
		t.Errorf("text length = %d, want %d", len(parts[0].Text), len(largeText))
	}
}

func TestTranscript_Rewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	tr := NewTranscript(path)

	// Write initial transcript with 5 events
	if err := tr.WriteHeader("sess-rewrite"); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := tr.AppendEvent(makeEvent("e", "user", "original")); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	// Rewrite with only 2 events (simulating compaction)
	newEvents := []*adksession.Event{
		makeEvent("summary", "user", "compacted summary"),
		makeEvent("recent", "model", "latest response"),
	}
	if err := tr.Rewrite("sess-rewrite", newEvents); err != nil {
		t.Fatalf("Rewrite() error = %v", err)
	}

	// Read back and verify
	got, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReadEvents() len = %d, want 2", len(got))
	}
	if got[0].ID != "summary" {
		t.Errorf("event[0].ID = %q, want %q", got[0].ID, "summary")
	}
	if got[1].ID != "recent" {
		t.Errorf("event[1].ID = %q, want %q", got[1].ID, "recent")
	}
}

// --- oversized line handling ---

func writeOversizedLine(t *testing.T, path string, withNewline bool) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	chunk := strings.Repeat("A", 1024*1024)
	for i := 0; i < 12; i++ {
		if _, err := f.WriteString(chunk); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if withNewline {
		if _, err := f.WriteString("\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func eventText(e *adksession.Event) string {
	if e == nil || e.LLMResponse.Content == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range e.LLMResponse.Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func TestTranscript_OversizedLineSkippedNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	tr := NewTranscript(path)
	if err := tr.WriteHeader("s"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendEvent(makeEvent("e1", "user", "hello")); err != nil {
		t.Fatal(err)
	}
	writeOversizedLine(t, path, true)
	if err := tr.AppendEvent(makeEvent("e2", "user", "world")); err != nil {
		t.Fatal(err)
	}

	events, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events (2 real + warning), got %d", len(events))
	}
	if !strings.Contains(eventText(events[2]), "skipped") {
		t.Fatalf("expected skip warning, got %q", eventText(events[2]))
	}
}

func TestTranscript_OversizedLineAtEOFWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	tr := NewTranscript(path)
	if err := tr.WriteHeader("s"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendEvent(makeEvent("e1", "user", "hello")); err != nil {
		t.Fatal(err)
	}
	writeOversizedLine(t, path, false)

	events, err := tr.ReadEvents()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 real + warning), got %d", len(events))
	}
}

func TestTranscript_NoWarningEventWhenAllLinesFit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	tr := NewTranscript(path)
	if err := tr.WriteHeader("s"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendEvent(makeEvent("e1", "user", "hello")); err != nil {
		t.Fatal(err)
	}
	events, err := tr.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestTranscript_ScanOversizedCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	tr := NewTranscript(path)
	if err := tr.WriteHeader("s"); err != nil {
		t.Fatal(err)
	}
	writeOversizedLine(t, path, true)

	skipped, nbytes, err := tr.ScanOversized()
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", skipped)
	}
	if nbytes < 12*1024*1024 {
		t.Fatalf("expected >=12MB, got %d", nbytes)
	}
}

func TestTranscript_RedactPreservesOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	tr := NewTranscript(path)
	if err := tr.WriteHeader("s"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendEvent(makeEvent("e1", "user", "SECRET")); err != nil {
		t.Fatal(err)
	}
	writeOversizedLine(t, path, true)

	if err := tr.RedactTranscript(func(s string) string {
		return strings.ReplaceAll(s, "SECRET", "[redacted]")
	}); err != nil {
		t.Fatalf("redact: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 12*1024*1024 {
		t.Fatalf("oversized line was dropped; size=%d", info.Size())
	}
	skipped, _, err := tr.ScanOversized()
	if err != nil || skipped != 1 {
		t.Fatalf("expected oversized line preserved, skipped=%d err=%v", skipped, err)
	}
}
