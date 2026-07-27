package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

type staticBackend struct {
	info backend.Info
}

func (b staticBackend) Info() backend.Info         { return b.info }
func (b staticBackend) Open(context.Context) error { return nil }
func (b staticBackend) RunTurn(context.Context, string, backend.TurnOptions) (<-chan events.Event, error) {
	return nil, nil
}
func (b staticBackend) ListSessions(context.Context) ([]backend.SessionSummary, error) {
	return nil, nil
}
func (b staticBackend) LoadHistory(context.Context) ([]backend.HistoryEntry, error) { return nil, nil }
func (b staticBackend) ResumeSession(context.Context, string) ([]backend.HistoryEntry, error) {
	return nil, nil
}
func (b staticBackend) DeleteSession(context.Context, string) error { return nil }
func (b staticBackend) NewSession()                                 {}
func (b staticBackend) Close() error                                { return nil }

func TestViewIncludesHeaderAsFirstLine(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{
			ServerURL: "https://astonish.example.com",
			User:      "user@example.com",
			Usage:     &events.Usage{Input: 1200, Output: 3400, Total: 4600},
		}},
		Width:  100,
		Height: 30,
	})
	m.ready = true
	m.theme.NoColor = false
	m.layout()
	m.refreshViewport()

	out := stripANSI(m.View())
	lines := strings.Split(out, "\n")
	firstLine := lines[0]
	if !strings.Contains(firstLine, "Astonish · https://astonish.example.com · user@example.com") {
		t.Fatalf("first line does not contain header identity: %q\nfull view:\n%s", firstLine, out)
	}
	if !strings.Contains(firstLine, "Usage 4.6k") {
		t.Fatalf("first line does not contain usage: %q\nfull view:\n%s", firstLine, out)
	}
	if m.screenHeight() != m.height {
		t.Fatalf("layout height = %d, want %d", m.screenHeight(), m.height)
	}
	if len(lines) != m.paintHeight() {
		t.Fatalf("view paints %d lines, want full terminal height %d", len(lines), m.paintHeight())
	}
}
