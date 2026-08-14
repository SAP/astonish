package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func newStreamingModel(t *testing.T) (model, context.Context) {
	t.Helper()
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code"}},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()

	turnCtx, cancel := context.WithCancel(context.Background())
	m.turnCancel = cancel
	m.tr.Streaming = true
	m.tr.Status = "Thinking…"
	m.tr.Apply(events.NewUser("do the thing"))
	m.refreshViewport()
	return m, turnCtx
}

func transcriptHas(m model, substr string) bool {
	for _, it := range m.tr.Items {
		if strings.Contains(it.Content, substr) {
			return true
		}
	}
	return false
}

func TestEscCancelsInFlightTurn(t *testing.T) {
	m, turnCtx := newStreamingModel(t)

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		t.Fatalf("expected nil cmd after esc cancel, got %#v", cmd)
	}
	m = next.(model)

	if m.quitting {
		t.Fatal("esc must not quit the app while cancelling a turn")
	}
	if m.tr.Streaming {
		t.Fatal("expected Streaming=false after esc cancel")
	}
	if m.tr.Status != "" {
		t.Fatalf("expected empty status after cancel, got %q", m.tr.Status)
	}
	if m.turnCancel != nil {
		t.Fatal("expected turnCancel cleared after cancel")
	}
	if turnCtx.Err() == nil {
		t.Fatal("expected turn context cancelled")
	}
	if !transcriptHas(m, "Turn cancelled.") {
		t.Fatalf("expected 'Turn cancelled.' system message, items=%#v", m.tr.Items)
	}
}

func TestCtrlCCancelsInFlightTurn(t *testing.T) {
	m, turnCtx := newStreamingModel(t)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatalf("expected nil cmd after ctrl+c cancel, got %#v", cmd)
	}
	m = next.(model)

	if m.quitting {
		t.Fatal("ctrl+c must not quit while cancelling a turn")
	}
	if m.tr.Streaming {
		t.Fatal("expected Streaming=false after ctrl+c cancel")
	}
	if turnCtx.Err() == nil {
		t.Fatal("expected turn context cancelled")
	}
	if !transcriptHas(m, "Turn cancelled.") {
		t.Fatalf("expected 'Turn cancelled.' system message, items=%#v", m.tr.Items)
	}
}

func TestEscIdleDoesNotQuit(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		// Esc idle may fall through to textarea/viewport; a nil/no-op batch is fine,
		// but must not be tea.Quit.
		if msg := cmd(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatal("esc must not quit when idle")
			}
		}
	}
	m = next.(model)
	if m.quitting {
		t.Fatal("esc must not set quitting when idle")
	}
	if transcriptHas(m, "Turn cancelled.") {
		t.Fatal("idle esc must not emit Turn cancelled")
	}
}

func TestCtrlCIdleQuits(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(model)
	if !m.quitting {
		t.Fatal("ctrl+c idle should set quitting")
	}
	if cmd == nil {
		t.Fatal("ctrl+c idle should return tea.Quit cmd")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("ctrl+c idle cmd = %#v, want tea.QuitMsg", cmd())
	}
}

func TestStreamingHintsShowEscCancel(t *testing.T) {
	m, _ := newStreamingModel(t)
	out := stripANSI(m.renderHints())
	if !strings.Contains(out, "esc cancel") {
		t.Fatalf("streaming hints should mention esc cancel:\n%s", out)
	}
}
