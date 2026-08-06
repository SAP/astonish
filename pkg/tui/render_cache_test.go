package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func newCodeModel(t *testing.T) model {
	t.Helper()
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()
	return m
}

// TestRenderAgentMarkdown_Caches verifies the markdown render cache returns
// consistent output and populates its cache (so finalized transcript blocks are
// not re-highlighted on every streamed event).
func TestRenderAgentMarkdown_Caches(t *testing.T) {
	m := newCodeModel(t)
	content := "# Title\n\nSome **bold** text and `code`.\n\n```go\nfunc main() {}\n```"

	first := m.renderAgentMarkdown(content, 80)
	if first == "" {
		t.Fatal("expected rendered markdown, got empty")
	}
	if len(m.mdCache) != 1 {
		t.Fatalf("expected 1 cache entry, got %d", len(m.mdCache))
	}
	second := m.renderAgentMarkdown(content, 80)
	if second != first {
		t.Fatal("cached render differs from first render")
	}
	if len(m.mdCache) != 1 {
		t.Fatalf("expected cache to be reused (1 entry), got %d", len(m.mdCache))
	}
	// A different width is a different cache key.
	m.renderAgentMarkdown(content, 60)
	if len(m.mdCache) != 2 {
		t.Fatalf("expected 2 cache entries after width change, got %d", len(m.mdCache))
	}
}

// TestWindowResizeClearsMarkdownCache verifies stale-width entries are dropped
// on resize so the cache does not grow one set per historical window size.
func TestWindowResizeClearsMarkdownCache(t *testing.T) {
	m := newCodeModel(t)
	m.renderAgentMarkdown("hello world", 80)
	if len(m.mdCache) == 0 {
		t.Fatal("expected cache to be populated")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	nm := next.(model)
	if nm.mdCache != nil && len(nm.mdCache) != 0 {
		t.Fatalf("expected markdown cache cleared on resize, got %d entries", len(nm.mdCache))
	}
}

// TestEventCoalescing_AppliesBufferedEvents verifies that a single Update on an
// eventMsg drains and applies all events already buffered on the channel, so a
// burst of tool output produces one repaint instead of one per event (which is
// what let the backend outrun the UI and appear frozen).
func TestEventCoalescing_AppliesBufferedEvents(t *testing.T) {
	m := newCodeModel(t)

	// A buffered channel standing in for RunTurn's output. Pre-load several
	// events; the Update should apply the first (delivered as eventMsg) plus all
	// the buffered ones in a single pass.
	ch := make(chan events.Event, 8)
	ch <- events.NewText("second ")
	ch <- events.NewText("third ")
	ch <- events.NewText("fourth")
	m.eventCh = ch
	m.tr.Streaming = true

	next, _ := m.Update(eventMsg(events.NewText("first ")))
	nm := next.(model)

	// All four text fragments should have been applied to the transcript in one
	// Update cycle (coalesced), not just the first.
	var agentText string
	for _, it := range nm.tr.Items {
		if it.Kind == events.ItemAgent {
			agentText += it.Content
		}
	}
	for _, want := range []string{"first", "second", "third", "fourth"} {
		if !strings.Contains(agentText, want) {
			t.Fatalf("expected coalesced agent text to contain %q, got %q", want, agentText)
		}
	}
}

// TestEventCoalescing_ChannelCloseFinalizes verifies that when the event channel
// closes mid-drain, the turn is finalized (Done applied, streaming cleared).
func TestEventCoalescing_ChannelCloseFinalizes(t *testing.T) {
	m := newCodeModel(t)
	ch := make(chan events.Event, 4)
	ch <- events.NewText("partial")
	close(ch) // closes after the buffered event is drained
	m.eventCh = ch
	m.tr.Streaming = true

	next, _ := m.Update(eventMsg(events.NewText("start ")))
	nm := next.(model)

	if nm.eventCh != nil {
		t.Fatal("expected eventCh cleared after channel close")
	}
	if nm.tr.Streaming {
		t.Fatal("expected streaming cleared after turn finalization")
	}
}
