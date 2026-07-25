package tui

import (
	"os"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/tui/render"
)

// Theme holds lipgloss styles for the terminal app.
type Theme struct {
	Brand      lipgloss.Style
	Text       lipgloss.Style
	Muted      lipgloss.Style
	User           lipgloss.Style // legacy accent; prefer UserBubble for transcript
	UserBubble     lipgloss.Style // user message surface (subtle bg, no "You" label)
	UserExpandHint lipgloss.Style // right-aligned expand/collapse cue inside user bubble
	Agent          lipgloss.Style // agent body text (no bg, no "Agent" label)
	System     lipgloss.Style
	Error      lipgloss.Style
	Success    lipgloss.Style
	Danger     lipgloss.Style
	Number     lipgloss.Style
	Border     lipgloss.Style
	Header     lipgloss.Style
	Status     lipgloss.Style
	Input      lipgloss.Style
	Activity   lipgloss.Style
	Approval   lipgloss.Style
	CodeGutter lipgloss.Style

	// Composer / footer chrome
	InputBorder      lipgloss.Style
	InputBorderFocus lipgloss.Style
	InputPrompt      lipgloss.Style
	InputPlaceholder lipgloss.Style
	FooterMeta       lipgloss.Style
	Hint             lipgloss.Style

	NoColor bool
}

// DefaultTheme returns a dark-terminal-friendly theme.
func DefaultTheme() Theme {
	noColor := os.Getenv("NO_COLOR") != ""
	if noColor {
		return plainTheme()
	}

	brand := lipgloss.Color("63")   // purple
	muted := lipgloss.Color("245")  // gray
	dim := lipgloss.Color("240")    // dimmer gray (hints)
	text := lipgloss.Color("252")   // near white
	// Subtle elevated surface for user bubbles on dark terminals (Grok-style).
	userSurface := lipgloss.Color("237")
	cyan := lipgloss.Color("51")    // user accent (legacy)
	green := lipgloss.Color("78")   // agent / success
	red := lipgloss.Color("203")    // error / danger
	orange := lipgloss.Color("208") // numbers
	yellow := lipgloss.Color("221") // approval
	border := lipgloss.Color("238") // subtle border

	return Theme{
		Brand: lipgloss.NewStyle().Foreground(brand).Bold(true),
		Text:  lipgloss.NewStyle().Foreground(text),
		Muted: lipgloss.NewStyle().Foreground(muted),
		User:  lipgloss.NewStyle().Foreground(cyan).Bold(true),
		// User bubble: soft gray band; vertical pad applied when composing lines.
		// Width is applied at render time for wrapping.
		UserBubble: lipgloss.NewStyle().
			Foreground(text).
			Background(userSurface).
			Padding(0, 2),
		// Expand/collapse cue: brand-tinted, right-aligned (width set at render).
		UserExpandHint: lipgloss.NewStyle().
			Foreground(brand).
			Background(userSurface).
			Italic(true).
			Align(lipgloss.Right).
			Padding(0, 2),
		// Agent: plain text, no background, no role label.
		// Width is applied at render time for wrapping.
		Agent: lipgloss.NewStyle().Foreground(text),
		System:     lipgloss.NewStyle().Foreground(muted).Italic(true),
		Error:      lipgloss.NewStyle().Foreground(red),
		Success:    lipgloss.NewStyle().Foreground(green),
		Danger:     lipgloss.NewStyle().Foreground(red),
		Number:     lipgloss.NewStyle().Foreground(orange),
		Border:     lipgloss.NewStyle().Foreground(border),
		Header:     lipgloss.NewStyle().Foreground(brand).Bold(true),
		Status:     lipgloss.NewStyle().Foreground(muted),
		Input:      lipgloss.NewStyle().Foreground(text),
		Activity:   lipgloss.NewStyle().Foreground(brand),
		Approval:   lipgloss.NewStyle().Foreground(yellow).Bold(true),
		CodeGutter: lipgloss.NewStyle().Foreground(muted),

		InputBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1),
		InputBorderFocus: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(brand).
			Padding(0, 1),
		InputPrompt:      lipgloss.NewStyle().Foreground(brand).Bold(true),
		InputPlaceholder: lipgloss.NewStyle().Foreground(dim).Italic(true),
		FooterMeta:       lipgloss.NewStyle().Foreground(muted),
		Hint:             lipgloss.NewStyle().Foreground(dim),
		NoColor:          false,
	}
}

func plainTheme() Theme {
	s := lipgloss.NewStyle()
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	// NO_COLOR: still pad user messages so they read as distinct blocks.
	userBubble := lipgloss.NewStyle().Padding(0, 2)
	userHint := lipgloss.NewStyle().Italic(true).Align(lipgloss.Right).Padding(0, 2)
	return Theme{
		Brand: s, Text: s, Muted: s, User: s, UserBubble: userBubble, UserExpandHint: userHint, Agent: s, System: s,
		Error: s, Success: s, Danger: s, Number: s, Border: s,
		Header: s, Status: s, Input: s, Activity: s, Approval: s,
		CodeGutter: s,
		InputBorder: box, InputBorderFocus: box,
		InputPrompt: s, InputPlaceholder: s, FooterMeta: s, Hint: s,
		NoColor: true,
	}
}

// ApplyTextareaStyles strips default AdaptiveColor backgrounds that break
// dark alt-screen UIs and applies theme colors to prompt/placeholder/text.
func (th Theme) ApplyTextareaStyles(ta *textarea.Model) {
	if ta == nil {
		return
	}
	// No background on cursor line — default AdaptiveColor paints a light chip.
	clean := textarea.Style{
		Base:             lipgloss.NewStyle(),
		CursorLine:       lipgloss.NewStyle(),
		CursorLineNumber: th.Muted,
		EndOfBuffer:      th.Muted,
		LineNumber:       th.Muted,
		Placeholder:      th.InputPlaceholder,
		Prompt:           th.InputPrompt,
		Text:             th.Text,
	}
	blurred := clean
	blurred.Prompt = th.Muted
	blurred.Text = th.Muted
	blurred.Placeholder = th.InputPlaceholder

	ta.FocusedStyle = clean
	ta.BlurredStyle = blurred
}

// RenderStyles maps the TUI theme into pure render.Styles for markdown/diff/activity.
func (th Theme) RenderStyles() render.Styles {
	return render.Styles{
		Text:       th.Text,
		Muted:      th.Muted,
		Brand:      th.Brand,
		Success:    th.Success,
		Danger:     th.Danger,
		Number:     th.Number,
		CodeGutter: th.CodeGutter,
		CodeHeader: th.Brand,
		Heading:    th.Brand,
		Bold:       th.Text.Bold(true),
		Italic:     th.Text.Italic(true),
		NoColor:    th.NoColor,
	}
}
