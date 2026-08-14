package tui

import (
	"image/color"
	"os"

	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/tui/render"
)

// Theme holds lipgloss styles for the terminal app.
type Theme struct {
	Background     lipgloss.Style
	Brand          lipgloss.Style
	Text           lipgloss.Style
	Muted          lipgloss.Style
	User           lipgloss.Style // legacy accent; prefer UserBubble for transcript
	UserBorder     lipgloss.Style // manually drawn user-message border
	UserBubble     lipgloss.Style // user message surface (muted bronze outline, no "You" label)
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
	DiffAddedBg    lipgloss.Style // subtle green background for added diff lines
	DiffRemovedBg  lipgloss.Style // subtle red background for removed diff lines

	// Plan document chrome (bordered document rendering for announce_plan output)
	PlanBorder lipgloss.Style // border outline for the plan document frame
	PlanHeader lipgloss.Style // bold styled title (✦ Execution Plan)
	PlanMuted  lipgloss.Style // muted text inside the plan (metadata, legend)

	// Composer / footer chrome
	InputBorder      lipgloss.Style
	InputBorderFocus lipgloss.Style
	InputBorderPlan  lipgloss.Style
	InputPrompt      lipgloss.Style
	InputPlaceholder lipgloss.Style
	FooterMeta       lipgloss.Style
	Hint             lipgloss.Style

	// AccentColor is the mode-identifying brand accent used by welcome titles
	// and other brand-identity style builders. Code mode = orange (208), Platform = cyan (39).
	// The composer border in normal mode uses a separate neutral color (see composerBorderStyle).
	AccentColor color.Color

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
	userAccent := lipgloss.Color("#75633F") // muted bronze user-message outline
	yellow := lipgloss.Color("221")         // approval
	border := lipgloss.Color("238")         // subtle separator border
	diffAddedBg := lipgloss.Color("#1a3320")   // subtle dark green for added lines
	diffRemovedBg := lipgloss.Color("#3d1f1f") // subtle dark red for removed lines

	return Theme{
		Background: lipgloss.NewStyle().Background(bg),
		Brand:      lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		Text:       lipgloss.NewStyle().Foreground(text).Background(bg),
		Muted:      lipgloss.NewStyle().Foreground(muted).Background(bg),
		User:       lipgloss.NewStyle().Foreground(cyan).Background(bg).Bold(true),
		UserBorder: lipgloss.NewStyle().Foreground(userAccent).Background(bg),
		// User bubble: muted bronze outline, black interior.
		// Width is applied at render time for wrapping.
		UserBubble: lipgloss.NewStyle().
			Foreground(text).
			Background(bg).
			Border(lipgloss.NormalBorder()).
			BorderForeground(userAccent).
			Padding(0, 2),
		// Expand/collapse cue: bronze-tinted, right-aligned inside the outlined bubble.
		UserExpandHint: lipgloss.NewStyle().
			Foreground(userAccent).
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
		DiffAddedBg:   lipgloss.NewStyle().Foreground(text).Background(diffAddedBg),
		DiffRemovedBg: lipgloss.NewStyle().Foreground(text).Background(diffRemovedBg),

		PlanBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("172")).Background(bg),
		PlanHeader: lipgloss.NewStyle().Foreground(lipgloss.Color("172")).Background(bg).Bold(true),
		PlanMuted:  lipgloss.NewStyle().Foreground(muted).Background(bg),

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
			BorderForeground(lipgloss.Color("172")).
			Padding(0, 1),
		InputPrompt:      lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		InputPlaceholder: lipgloss.NewStyle().Foreground(dim).Background(bg).Italic(true),
		FooterMeta:       lipgloss.NewStyle().Foreground(muted).Background(bg),
		Hint:             lipgloss.NewStyle().Foreground(dim).Background(bg),
		AccentColor:      orange,
		NoColor:          false,
	}
}

// PlatformTheme returns a theme variant for the platform/chat mode. It uses a
// cool blue/cyan accent family instead of code mode's warm orange/amber,
// creating an instant visual distinction: orange = local code mode,
// blue = authenticated platform chat.
func PlatformTheme() Theme {
	noColor := os.Getenv("NO_COLOR") != ""
	if noColor {
		return plainTheme()
	}

	bg := lipgloss.Color("#000000")         // true black terminal background
	brand := lipgloss.Color("75")           // steel blue (replaces code-mode's 250 gray)
	muted := lipgloss.Color("245")          // gray (shared)
	dim := lipgloss.Color("240")            // dimmer gray (hints)
	text := lipgloss.Color("252")           // near white
	composerBorder := lipgloss.Color("39")  // bright cyan border (key differentiator)
	cyan := brand                           // user accent (legacy)
	green := lipgloss.Color("78")           // agent / success
	red := lipgloss.Color("203")            // error / danger
	highlight := lipgloss.Color("75")       // blue for numbers (was orange 208)
	userAccent := lipgloss.Color("#3F5A75") // muted steel-blue user outline (was bronze #75633F)
	yellow := lipgloss.Color("221")         // approval
	border := lipgloss.Color("238")         // subtle separator border
	diffAddedBg := lipgloss.Color("#1a3320")   // subtle dark green for added lines
	diffRemovedBg := lipgloss.Color("#3d1f1f") // subtle dark red for removed lines

	return Theme{
		Background: lipgloss.NewStyle().Background(bg),
		Brand:      lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		Text:       lipgloss.NewStyle().Foreground(text).Background(bg),
		Muted:      lipgloss.NewStyle().Foreground(muted).Background(bg),
		User:       lipgloss.NewStyle().Foreground(cyan).Background(bg).Bold(true),
		UserBorder: lipgloss.NewStyle().Foreground(userAccent).Background(bg),
		// User bubble: muted steel-blue outline, black interior.
		UserBubble: lipgloss.NewStyle().
			Foreground(text).
			Background(bg).
			Border(lipgloss.NormalBorder()).
			BorderForeground(userAccent).
			Padding(0, 2),
		UserExpandHint: lipgloss.NewStyle().
			Foreground(userAccent).
			Background(bg).
			Italic(true).
			Align(lipgloss.Right),
		Agent:      lipgloss.NewStyle().Foreground(text).Background(bg),
		System:     lipgloss.NewStyle().Foreground(muted).Background(bg).Italic(true),
		Error:      lipgloss.NewStyle().Foreground(red).Background(bg),
		Success:    lipgloss.NewStyle().Foreground(green).Background(bg),
		Danger:     lipgloss.NewStyle().Foreground(red).Background(bg),
		Number:     lipgloss.NewStyle().Foreground(highlight).Background(bg),
		Border:     lipgloss.NewStyle().Foreground(border).Background(bg),
		Header:     lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		Status:     lipgloss.NewStyle().Foreground(muted).Background(bg),
		Input:      lipgloss.NewStyle().Foreground(text).Background(bg),
		Activity:   lipgloss.NewStyle().Foreground(brand).Background(bg),
		Approval:   lipgloss.NewStyle().Foreground(yellow).Background(bg).Bold(true),
		CodeGutter: lipgloss.NewStyle().Foreground(muted).Background(bg),
		DiffAddedBg:   lipgloss.NewStyle().Foreground(text).Background(diffAddedBg),
		DiffRemovedBg: lipgloss.NewStyle().Foreground(text).Background(diffRemovedBg),

		PlanBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Background(bg),
		PlanHeader: lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Background(bg).Bold(true),
		PlanMuted:  lipgloss.NewStyle().Foreground(muted).Background(bg),

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
			BorderForeground(composerBorder).
			Padding(0, 1),
		InputPrompt:      lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		InputPlaceholder: lipgloss.NewStyle().Foreground(dim).Background(bg).Italic(true),
		FooterMeta:       lipgloss.NewStyle().Foreground(muted).Background(bg),
		Hint:             lipgloss.NewStyle().Foreground(dim).Background(bg),
		AccentColor:      composerBorder,
		NoColor:          false,
	}
}

// ThemeForMode returns the appropriate accent theme for a backend mode string.
// "code" → warm orange/amber (DefaultTheme), "platform" → cool blue/cyan (PlatformTheme).
func ThemeForMode(mode string) Theme {
	if mode == "platform" {
		return PlatformTheme()
	}
	return DefaultTheme()
}

func plainTheme() Theme {
	s := lipgloss.NewStyle()
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	// NO_COLOR: keep an outline so user messages read as distinct blocks.
	userBubble := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 2)
	userHint := lipgloss.NewStyle().Italic(true).Align(lipgloss.Right)
	return Theme{
		Background: s,
		Brand:      s, Text: s, Muted: s, User: s, UserBorder: s, UserBubble: userBubble, UserExpandHint: userHint, Agent: s, System: s,
		Error: s, Success: s, Danger: s, Number: s, Border: s,
		Header: s, Status: s, Input: s, Activity: s, Approval: s,
		CodeGutter:  s,
		DiffAddedBg: s, DiffRemovedBg: s,
		PlanBorder: s, PlanHeader: s, PlanMuted: s,
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
	clean := textarea.StyleState{
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

	ta.SetStyles(textarea.Styles{
		Focused: clean,
		Blurred: blurred,
	})
}

// RenderStyles maps the TUI theme into pure render.Styles for markdown/diff/activity.
func (th Theme) RenderStyles() render.Styles {
	return render.Styles{
		Background:    th.Background,
		Text:          th.Text,
		Muted:         th.Muted,
		Brand:         th.Brand,
		Success:       th.Success,
		Danger:        th.Danger,
		Number:        th.Number,
		CodeGutter:    th.CodeGutter,
		CodeHeader:    th.Brand,
		Heading:       th.Brand,
		Bold:          th.Text.Bold(true),
		Italic:        th.Text.Italic(true),
		DiffAddedBg:   th.DiffAddedBg,
		DiffRemovedBg: th.DiffRemovedBg,
		NoColor:       th.NoColor,
	}
}
