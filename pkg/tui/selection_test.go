package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestSelectionTextSingleAndMultiLine(t *testing.T) {
	lines := []string{"hello world", "second line", "third"}
	got := selectionText(lines, nil, selectionPoint{line: 0, col: 6}, selectionPoint{line: 1, col: 6})
	want := "world\nsecond"
	if got != want {
		t.Fatalf("selectionText=%q want %q", got, want)
	}

	reverse := selectionText(lines, nil, selectionPoint{line: 1, col: 6}, selectionPoint{line: 0, col: 6})
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
		vp:     viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
		width:  80,
		height: 24,
		ready:  true,
	}
	m.refreshViewport()
	y := m.viewportTopY()

	next, _ := m.handleMouse(tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseMotionMsg{X: 7, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseReleaseMsg{X: 7, Y: y, Button: tea.MouseLeft})
	m = next.(model)

	if strings.TrimSpace(copied) != "hello" {
		t.Fatalf("copied=%q want hello", copied)
	}
	if !strings.Contains(m.copyStatus, "Copied") {
		t.Fatalf("missing copy status: %q", m.copyStatus)
	}
}

func TestSelectionTextClampsToContentSpan(t *testing.T) {
	// A user-bubble-style line: margin + border + interior padding + content.
	line := "  │  hello world  │"
	span := userBubbleContentSpan(1, line)
	lines := []string{line}
	spans := [][2]int{span}
	// Select the whole line width including the border columns.
	got := selectionText(lines, spans, selectionPoint{line: 0, col: 0}, selectionPoint{line: 0, col: len([]rune(line))})
	if got != "hello world" {
		t.Fatalf("selectionText clamped=%q want %q", got, "hello world")
	}
	if strings.ContainsRune(got, '│') {
		t.Fatalf("copied text still contains border glyph: %q", got)
	}
}

func TestUserBubbleContentSpan(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string // expected copied substring, "" for chrome rows
	}{
		{"top border", "  ┌────────────┐", ""},
		{"bottom border", "  └────────────┘", ""},
		{"body", "  │  the prompt text  │", "the prompt text"},
		{"empty body", "  │             │", ""},
		{"no borders", "just text", ""}, // no verticals -> treated as chrome
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			span := userBubbleContentSpan(0, tc.line)
			runes := []rune(tc.line)
			got := ""
			if span[0] < span[1] {
				got = string(runes[span[0]:span[1]])
			}
			if got != tc.want {
				t.Fatalf("span content=%q want %q (span=%v line=%q)", got, tc.want, span, tc.line)
			}
		})
	}
}

func TestUserBubbleDragCopyExcludesChrome(t *testing.T) {
	old := writeClipboard
	defer func() { writeClipboard = old }()
	var copied string
	writeClipboard = func(s string) error {
		copied = s
		return nil
	}

	tr := events.NewTranscript()
	tr.Items = []events.Item{{Kind: events.ItemUser, Content: "the prompt text"}}
	th := DefaultTheme()
	th.NoColor = false
	m := model{
		theme:  th,
		tr:     tr,
		vp:     viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
		width:  80,
		height: 24,
		ready:  true,
	}
	m.refreshViewport()

	// Locate the bubble body row (the one carrying the content).
	bodyLine := -1
	for i, line := range m.transcriptPlainLines {
		if strings.Contains(line, "the prompt text") {
			bodyLine = i
			break
		}
	}
	if bodyLine < 0 {
		t.Fatalf("body line not found in %#v", m.transcriptPlainLines)
	}
	top := m.viewportTopY()
	y := top + (bodyLine - m.vp.YOffset())
	rowWidth := len([]rune(m.transcriptPlainLines[bodyLine]))

	// Drag from the far-left border column across the whole row into the
	// right border column — the selection spans the decorative chrome.
	next, _ := m.handleMouse(tea.MouseClickMsg{X: 0, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseMotionMsg{X: rowWidth, Y: y, Button: tea.MouseLeft})
	m = next.(model)
	next, _ = m.handleMouse(tea.MouseReleaseMsg{X: rowWidth, Y: y, Button: tea.MouseLeft})
	m = next.(model)

	if strings.TrimSpace(copied) != "the prompt text" {
		t.Fatalf("copied=%q want %q", copied, "the prompt text")
	}
	if strings.ContainsRune(copied, '│') {
		t.Fatalf("copied text includes border glyph: %q", copied)
	}
}

func TestTranscriptContentSpansAlignWithPlainLines(t *testing.T) {
	tr := events.NewTranscript()
	tr.Items = []events.Item{
		{Kind: events.ItemUser, Content: "hello"},
		{Kind: events.ItemSystem, Content: "world"},
	}
	th := DefaultTheme()
	th.NoColor = false
	m := model{theme: th, tr: tr, width: 80, height: 24, ready: true}
	m.renderTranscript()
	if len(m.transcriptContentSpans) != len(m.transcriptPlainLines) {
		t.Fatalf("spans=%d plainLines=%d", len(m.transcriptContentSpans), len(m.transcriptPlainLines))
	}
}
