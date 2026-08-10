package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// newStreamingModelWithContent builds a model that is streaming with enough
// content to make the viewport scrollable (content exceeds viewport height).
func newStreamingModelWithContent(t *testing.T) model {
	t.Helper()
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   80,
		Height:  20,
	})
	m.ready = true
	m.layout()

	// Add enough content to make the viewport scrollable.
	m.tr.Apply(events.NewUser("start task"))
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "line of output content that fills the viewport")
	}
	m.tr.Apply(events.NewText(strings.Join(lines, "\n")))
	m.tr.Streaming = true
	m.tr.Status = "Thinking…"
	m.refreshViewport()
	return m
}

func TestAutoScrollStaysAtBottomDuringStreaming(t *testing.T) {
	m := newStreamingModelWithContent(t)

	// Precondition: viewport should be at bottom (auto-followed).
	if !m.vp.AtBottom() {
		t.Fatal("expected viewport at bottom after initial refresh during streaming")
	}
	if m.userScrolledUp {
		t.Fatal("expected userScrolledUp=false initially")
	}

	// Simulate more content arriving (another event) and refresh.
	m.tr.Apply(events.NewText("more streaming content"))
	m.refreshViewport()

	if !m.vp.AtBottom() {
		t.Fatal("expected viewport to stay at bottom when user has not scrolled up")
	}
}

func TestAutoScrollRespectsUserScrollUp(t *testing.T) {
	m := newStreamingModelWithContent(t)

	// User scrolls up with pgup during streaming.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(model)

	if !m.userScrolledUp {
		t.Fatal("expected userScrolledUp=true after pgup during streaming")
	}

	// Now simulate new content arriving and viewport refresh.
	prevOffset := m.vp.YOffset
	m.tr.Apply(events.NewText("new content after user scrolled up"))
	m.refreshViewport()

	// The viewport should NOT have jumped to bottom.
	if m.vp.AtBottom() {
		t.Fatal("expected viewport to NOT jump to bottom when user has scrolled up")
	}
	if m.vp.YOffset != prevOffset {
		t.Fatalf("expected viewport offset to remain at %d, got %d", prevOffset, m.vp.YOffset)
	}
}

func TestAutoScrollResumesWhenUserScrollsBack(t *testing.T) {
	m := newStreamingModelWithContent(t)

	// User scrolls up.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(model)

	if !m.userScrolledUp {
		t.Fatal("expected userScrolledUp=true after pgup")
	}

	// User scrolls back down to bottom. We simulate by pressing pgdown
	// multiple times until at bottom.
	for i := 0; i < 50; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		m = next.(model)
		if m.vp.AtBottom() {
			break
		}
	}

	if !m.vp.AtBottom() {
		t.Fatal("expected viewport to reach bottom after scrolling down")
	}
	if m.userScrolledUp {
		t.Fatal("expected userScrolledUp=false after scrolling back to bottom")
	}

	// Now new content should auto-follow again.
	m.tr.Apply(events.NewText("auto-follow resumed content"))
	m.refreshViewport()

	if !m.vp.AtBottom() {
		t.Fatal("expected viewport to follow new content after user scrolled back to bottom")
	}
}

func TestAutoScrollClearsOnNewTurn(t *testing.T) {
	m := newStreamingModelWithContent(t)

	// Simulate user scrolled up.
	m.userScrolledUp = true

	// Simulate turn ending (turnDoneMsg).
	next, _ := m.Update(turnDoneMsg{})
	m = next.(model)

	if m.userScrolledUp {
		t.Fatal("expected userScrolledUp=false after turnDoneMsg")
	}
}

func TestAutoScrollClearsOnTurnError(t *testing.T) {
	m := newStreamingModelWithContent(t)

	// Simulate user scrolled up.
	m.userScrolledUp = true

	// Simulate turn error.
	next, _ := m.Update(turnErrMsg{err: context.Canceled})
	m = next.(model)

	if m.userScrolledUp {
		t.Fatal("expected userScrolledUp=false after turnErrMsg")
	}
}

func TestAutoScrollMouseWheelUpSetsFlag(t *testing.T) {
	m := newStreamingModelWithContent(t)

	// Mouse wheel up during streaming.
	next, _ := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelUp,
		Action: tea.MouseActionPress,
	})
	m = next.(model)

	// If the viewport moved away from bottom, the flag should be set.
	if !m.vp.AtBottom() && !m.userScrolledUp {
		t.Fatal("expected userScrolledUp=true after mouse wheel up moved viewport away from bottom")
	}
}
