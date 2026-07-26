package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestRenderHeaderShowsConnectionAndUsage(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 100,
		info: backend.Info{
			ServerURL: "https://astonish.example.com",
			User:      "user@example.com",
		},
		tr: &events.Transcript{LastUsage: &events.Usage{Input: 1200, Output: 3400, Total: 4600}},
	}

	out := stripANSI(m.renderHeader())
	if !strings.Contains(out, "Astonish · https://astonish.example.com · user@example.com") {
		t.Fatalf("header missing connection identity: %q", out)
	}
	if !strings.Contains(out, "Usage 4.6k · in 1.2k · out 3.4k") {
		t.Fatalf("header missing formatted usage: %q", out)
	}
	if got := lipgloss.Width(out); got != 100 {
		t.Fatalf("header width=%d want 100: %q", got, out)
	}
}

func TestRenderHeaderTruncatesToOneLine(t *testing.T) {
	m := model{
		theme: DefaultTheme(),
		width: 48,
		info: backend.Info{
			ServerURL: "https://very-long-astonish-platform.example.com",
			User:      "somebody.with.a.long.name@example.com",
		},
		tr: &events.Transcript{LastUsage: &events.Usage{Input: 1000, Output: 2000, Total: 3000}},
	}

	out := stripANSI(m.renderHeader())
	if strings.Contains(out, "\n") {
		t.Fatalf("header should stay on one line: %q", out)
	}
	if !strings.Contains(out, "Usage 3.0k") {
		t.Fatalf("usage should remain visible: %q", out)
	}
	if got := lipgloss.Width(out); got != 48 {
		t.Fatalf("header width=%d want 48: %q", got, out)
	}
}

func TestFormatTokenCountMatchesStudioStyle(t *testing.T) {
	cases := map[int64]string{
		42:        "42",
		1_200:     "1.2k",
		1_000_000: "1.0M",
	}
	for n, want := range cases {
		if got := formatTokenCount(n); got != want {
			t.Fatalf("formatTokenCount(%d)=%q want %q", n, got, want)
		}
	}
}
