package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestNewModelShowsWelcomeInsteadOfGreetingTranscript(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{ServerURL: "https://astonish.example.com", User: "user@example.com"}},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()
	m.refreshViewport()

	if len(m.tr.Items) != 0 {
		t.Fatalf("new chat should not add a greeting transcript item: %#v", m.tr.Items)
	}
	out := stripANSI(m.View())
	if !strings.Contains(out, "✦ Astonish") {
		t.Fatalf("welcome card missing Astonish title:\n%s", out)
	}
	if !strings.Contains(out, "Build, investigate, and operate") {
		t.Fatalf("welcome card missing meaningful description:\n%s", out)
	}
	welcome := stripANSI(m.renderWelcome())
	if strings.Contains(welcome, "…") {
		t.Fatalf("welcome card should avoid truncated ellipsis text:\n%s", welcome)
	}
}

func TestWelcomeDisappearsAfterFirstMessage(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.refreshViewport()
	if !strings.Contains(stripANSI(m.View()), "✦ Astonish") {
		t.Fatal("expected welcome before conversation starts")
	}

	m.tr.Apply(events.NewUser("hello"))
	m.refreshViewport()
	out := stripANSI(m.View())
	if strings.Contains(out, "✦ Astonish") {
		t.Fatalf("welcome should disappear once transcript starts:\n%s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("user message should render after welcome disappears:\n%s", out)
	}
}

func TestStartNewSessionReturnsToWelcome(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	m.ready = true
	m.layout()
	m.tr.Apply(events.NewUser("old conversation"))
	m.tr.Apply(events.NewDone())
	m.refreshViewport()

	next, _ := m.startNewSession()
	m = next.(model)
	out := stripANSI(m.View())
	if !strings.Contains(out, "✦ Astonish") {
		t.Fatalf("new session should return to welcome:\n%s", out)
	}
	if strings.Contains(out, "old conversation") {
		t.Fatalf("old transcript should be cleared:\n%s", out)
	}
}
