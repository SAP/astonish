package tui

import (
	"os"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/tui/render"
)

// Theme holds lipgloss styles for the terminal app.
type Theme struct {
	Background     lipgloss.Style
	Brand          lipgloss.Style
	Text           lipgloss.Style
	Muted          lipgloss.Style
	User           lipgloss.Style // legacy accent; prefer UserBubble for transcript
	UserBubble     lipgloss.Style // user message surface (orange outline, no "You" label)
	UserExpandHint lipgloss.Style // right-aligned expand/collapse cue inside user bubble
	Agent          lipgloss.Style // agent body text (no bg, no "Agent" label)
	System         lipgloss.Style
	Error          lipgloss.Style
	Success        lipgloss.Style
	Danger         lipgloss.Style
	Number         lipgloss.Style
	Border         lipgloss.Style
	Header         lipgloss.Style
	Status         lipgloss.Style
	Input          lipgloss.Style
	Activity       lipgloss.Style
	Approval       lipgloss.Style
	CodeGutter     lipgloss.Style

	// Composer / footer chrome
	InputBorder      lipgloss.Style
	InputBorderFocus lipgloss.Style
	InputBorderPlan  lipgloss.Style
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

	bg := lipgloss.Color("#000000")         // true black terminal background
	brand := lipgloss.Color("250")          // light gray accent (formerly purple)
	muted := lipgloss.Color("245")          // gray
	dim := lipgloss.Color("240")            // dimmer gray (hints)
	text := lipgloss.Color("252")           // near white
	composerBorder := lipgloss.Color("246") // softened composer border
	cyan := brand                           // user accent (legacy)
	green := lipgloss.Color("78")           // agent / success
	red := lipgloss.Color("203")            // error / danger
	orange := lipgloss.Color("208")         // numbers
	softOrange := lipgloss.Color("172")     // comfortable user-message outline
	yellow := lipgloss.Color("221")         // approval
	border := lipgloss.Color("238")         // subtle separator border

	return Theme{
		Background: lipgloss.NewStyle().Background(bg),
		Brand:      lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		Text:       lipgloss.NewStyle().Foreground(text).Background(bg),
		Muted:      lipgloss.NewStyle().Foreground(muted).Background(bg),
		User:       lipgloss.NewStyle().Foreground(cyan).Background(bg).Bold(true),
		// User bubble: orange outline, black interior.
		// Width is applied at render time for wrapping.
		UserBubble: lipgloss.NewStyle().
			Foreground(text).
			Background(bg).
			Border(lipgloss.NormalBorder()).
			BorderForeground(softOrange).
			Padding(0, 2),
		// Expand/collapse cue: orange-tinted, right-aligned inside the outlined bubble.
		UserExpandHint: lipgloss.NewStyle().
			Foreground(softOrange).
			Background(bg).
			Italic(true).
			Align(lipgloss.Right),
		// Agent: plain text, no background, no role label.
		// Width is applied at render time for wrapping.
		Agent:      lipgloss.NewStyle().Foreground(text).Background(bg),
		System:     lipgloss.NewStyle().Foreground(muted).Background(bg).Italic(true),
		Error:      lipgloss.NewStyle().Foreground(red).Background(bg),
		Success:    lipgloss.NewStyle().Foreground(green).Background(bg),
		Danger:     lipgloss.NewStyle().Foreground(red).Background(bg),
		Number:     lipgloss.NewStyle().Foreground(orange).Background(bg),
		Border:     lipgloss.NewStyle().Foreground(border).Background(bg),
		Header:     lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		Status:     lipgloss.NewStyle().Foreground(muted).Background(bg),
		Input:      lipgloss.NewStyle().Foreground(text).Background(bg),
		Activity:   lipgloss.NewStyle().Foreground(brand).Background(bg),
		Approval:   lipgloss.NewStyle().Foreground(yellow).Background(bg).Bold(true),
		CodeGutter: lipgloss.NewStyle().Foreground(muted).Background(bg),

		InputBorder: lipgloss.NewStyle().
			Background(bg).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(composerBorder).
			Padding(0, 1),
		InputBorderFocus: lipgloss.NewStyle().
			Background(bg).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(composerBorder).
			Padding(0, 1),
		InputBorderPlan: lipgloss.NewStyle().
			Background(bg).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("214")).
			Padding(0, 1),
		InputPrompt:      lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		InputPlaceholder: lipgloss.NewStyle().Foreground(dim).Background(bg).Italic(true),
		FooterMeta:       lipgloss.NewStyle().Foreground(muted).Background(bg),
		Hint:             lipgloss.NewStyle().Foreground(dim).Background(bg),
		NoColor:          false,
	}
}

func plainTheme() Theme {
	s := lipgloss.NewStyle()
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	// NO_COLOR: keep an outline so user messages read as distinct blocks.
	userBubble := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 2)
	userHint := lipgloss.NewStyle().Italic(true).Align(lipgloss.Right)
	return Theme{
		Background: s,
		Brand:      s, Text: s, Muted: s, User: s, UserBubble: userBubble, UserExpandHint: userHint, Agent: s, System: s,
		Error: s, Success: s, Danger: s, Number: s, Border: s,
		Header: s, Status: s, Input: s, Activity: s, Approval: s,
		CodeGutter:  s,
		InputBorder: box, InputBorderFocus: box, InputBorderPlan: box,
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
		Base:             th.Input,
		CursorLine:       th.Input,
		CursorLineNumber: th.Muted,
		EndOfBuffer:      th.Input,
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
		Background: th.Background,
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
