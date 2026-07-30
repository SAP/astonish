package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestSelectionTextSingleAndMultiLine(t *testing.T) {
	lines := []string{"hello world", "second line", "third"}
	got := selectionText(lines, selectionPoint{line: 0, col: 6}, selectionPoint{line: 1, col: 6})
	want := "world\nsecond"
	if got != want {
		t.Fatalf("selectionText=%q want %q", got, want)
	}

	reverse := selectionText(lines, selectionPoint{line: 1, col: 6}, selectionPoint{line: 0, col: 6})
	if reverse != want {
		t.Fatalf("reverse selectionText=%q want %q", reverse, want)
	}
}

func TestRenderTranscriptPlainLinesAlignWithRenderedLines(t *testing.T) {
	tr := events.NewTranscript()
	tr.Items = []events.Item{
		{Kind: events.ItemSystem, Content: "first"},
		{Kind: events.ItemSystem, Content: "second"},
		{Kind: events.ItemSystem, Content: "third"},
	}
	th := DefaultTheme()
	th.NoColor = false
	m := model{theme: th, tr: tr, width: 80, height: 24, ready: true}
	rendered, _, _ := m.renderTranscript()
	renderedLines := strings.Split(rendered, "\n")
	if len(renderedLines) > 0 && renderedLines[len(renderedLines)-1] == "" {
		renderedLines = renderedLines[:len(renderedLines)-1]
	}
	plainLines := m.transcriptPlainLines
	if len(renderedLines) != len(plainLines) {
		t.Fatalf("rendered lines=%d plain lines=%d\nrendered=%q\nplain=%#v", len(renderedLines), len(plainLines), rendered, plainLines)
	}
	for i, line := range renderedLines {
		plain := stripANSI(line)
		if plain != plainLines[i] {
			t.Fatalf("line %d mismatch\nrendered: %q\nplain:    %q", i, plain, plainLines[i])
		}
	}
}

func TestSelectionHighlightUsesSameLineAsSelection(t *testing.T) {
	tr := events.NewTranscript()
	tr.Items = []events.Item{
		{Kind: events.ItemSystem, Content: "first"},
		{Kind: events.ItemSystem, Content: "second"},
		{Kind: events.ItemSystem, Content: "third"},
	}
	th := DefaultTheme()
	th.NoColor = false
	m := model{
		theme:          th,
		tr:             tr,
		width:          80,
		height:         24,
		ready:          true,
		selecting:      true,
		selectionMoved: true,
		selectionStart: selectionPoint{line: 4, col: 2},
		selectionEnd:   selectionPoint{line: 4, col: 8},
	}
	rendered, _, _ := m.renderTranscript()
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if !strings.Contains(lines[4], "\x1b[7m") && !strings.Contains(lines[4], "48;5;252") {
		t.Fatalf("expected selection highlight on selected line 4, got %q", lines[4])
	}
	if strings.Contains(lines[2], "48;5;252") {
		t.Fatalf("line 2 should not carry selection highlight: %q", lines[2])
	}
}

func TestHandleMouseDragCopiesSelection(t *testing.T) {
	old := writeClipboard
	defer func() { writeClipboard = old }()
	var copied string
	writeClipboard = func(s string) error {
		copied = s
		return nil
	}

	tr := events.NewTranscript()
	tr.Items = []events.Item{{Kind: events.ItemSystem, Content: "hello world"}}
	m := model{
		theme:  DefaultTheme(),
		tr:     tr,
		vp:     viewport.New(80, 10),
		width:  80,
		height: 24,
		ready:  true,
	}
	m.refreshViewport()
	y := m.viewportTopY()

	next, _ := m.handleMouse(tea.MouseMsg{X: 2, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseMsg{X: 7, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseMsg{X: 7, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	m = next.(model)

	if strings.TrimSpace(copied) != "hello" {
		t.Fatalf("copied=%q want hello", copied)
	}
	if !strings.Contains(m.copyStatus, "Copied") {
		t.Fatalf("missing copy status: %q", m.copyStatus)
	}
}
