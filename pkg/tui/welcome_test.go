package tui

import (
	"context"
	"os"
	"path/filepath"
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

func TestWelcomeCodeModeShowsCodeCard(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code", WorkingDir: "/tmp/my-project"}},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()
	m.refreshViewport()

	out := stripANSI(m.View())
	if !strings.Contains(out, "✦ Astonish Code") {
		t.Fatalf("code-mode welcome card missing 'Astonish Code' title:\n%s", out)
	}
	if !strings.Contains(out, "local AI coding tool") {
		t.Fatalf("code-mode welcome card missing code-focused description:\n%s", out)
	}
	if !strings.Contains(out, "Astonish intelligence") {
		t.Fatalf("code-mode welcome card missing approval notice:\n%s", out)
	}
	if strings.Contains(out, "no prompts") {
		t.Fatalf("code-mode welcome card should not claim no-prompts without auto-approve:\n%s", out)
	}
	if strings.Contains(out, "Connected to your platform") {
		t.Fatalf("code-mode welcome card should not claim a platform connection:\n%s", out)
	}
	if !strings.Contains(out, "/rollback") {
		t.Fatalf("code-mode welcome card should surface the /rollback hint:\n%s", out)
	}
	if !strings.Contains(out, "my-project") {
		t.Fatalf("code-mode welcome card should show the working directory:\n%s", out)
	}
}

func TestWelcomeCodeModeAutoApproveNotice(t *testing.T) {
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code", WorkingDir: "/tmp/my-project", AutoApprove: true}},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()
	m.refreshViewport()

	out := stripANSI(m.View())
	if !strings.Contains(out, "no prompts") {
		t.Fatalf("code-mode welcome card should reflect auto-approve state:\n%s", out)
	}
}

func TestWelcomeCodeModeAbbreviatesHomeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	m := newModel(context.Background(), Config{
		Backend: staticBackend{info: backend.Info{Mode: "code", WorkingDir: filepath.Join(home, "code", "widget")}},
		Width:   100,
		Height:  30,
	})
	m.ready = true
	m.layout()
	m.refreshViewport()

	out := stripANSI(m.View())
	if !strings.Contains(out, "~"+string(filepath.Separator)+"code") {
		t.Fatalf("code-mode welcome card should abbreviate the home directory to ~:\n%s", out)
	}
	if strings.Contains(out, home) {
		t.Fatalf("code-mode welcome card should not show the full home path:\n%s", out)
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
