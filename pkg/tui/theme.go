package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"github.com/alecthomas/chroma/v2"

	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/tui/render"
)

// CodeThemeID is a launch-time Code palette identifier.
type CodeThemeID string

const CodeThemeBlueGray CodeThemeID = "blue-gray"

// ResolveCodeTheme validates a name; an empty name selects the default.
func ResolveCodeTheme(id CodeThemeID) (Theme, error) {
	if id != "" && id != CodeThemeBlueGray {
		return Theme{}, fmt.Errorf("unknown Code theme %q (available: blue-gray)", id)
	}
	return DefaultTheme(), nil
}

// Theme holds lipgloss styles for the terminal app.
type Theme struct {
	CodeThemeID                              CodeThemeID
	Card, ApprovalBorder, AskBorder, Spinner lipgloss.Style
	SyntaxStyle                              *chroma.Style
	TableLayout                              render.TableLayout
	Background                               lipgloss.Style
	Brand                                    lipgloss.Style
	Text                                     lipgloss.Style
	Muted                                    lipgloss.Style
	User                                     lipgloss.Style // legacy accent; prefer UserBubble for transcript
	UserBorder                               lipgloss.Style // manually drawn user-message border
	UserBubble                               lipgloss.Style // user message surface (muted bronze outline, no "You" label)
	UserExpandHint                           lipgloss.Style // right-aligned expand/collapse cue inside user bubble
	Agent                                    lipgloss.Style // agent body text (no bg, no "Agent" label)
	System                                   lipgloss.Style
	Error                                    lipgloss.Style
	Success                                  lipgloss.Style
	Danger                                   lipgloss.Style
	Number                                   lipgloss.Style
	Border                                   lipgloss.Style
	Header                                   lipgloss.Style
	Status                                   lipgloss.Style
	Input                                    lipgloss.Style
	Activity                                 lipgloss.Style
	Approval                                 lipgloss.Style
	CodeGutter                               lipgloss.Style
	DiffAddedBg                              lipgloss.Style // subtle green background for added diff lines
	DiffRemovedBg                            lipgloss.Style // subtle red background for removed diff lines

	// Plan document chrome (bordered document rendering for announce_plan output)
	PlanBorder lipgloss.Style // border outline for the plan document frame
	PlanHeader lipgloss.Style // bold styled title (✦ Execution Plan)
	PlanTitle  lipgloss.Style // distinct accent-colored plan title (card frame)
	PlanMuted  lipgloss.Style // muted text inside the plan (metadata, legend)

	// Plan document chrome — new section-based layout
	PlanPhaseTitle lipgloss.Style // bold phase title (e.g. "Phase 1 — Implement core logic")
	PlanSummary    lipgloss.Style // slightly prominent summary/intent text (normal weight, text color)
	PlanDetail     lipgloss.Style // dimmed/recessed detail text (for deep implementation notes)
	PlanSection    lipgloss.Style // section label within a phase (e.g. "FILES", "VERIFY")
	PlanSeparator  lipgloss.Style // horizontal separator between phases

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
	if os.Getenv("NO_COLOR") != "" {
		th := plainTheme()
		th.CodeThemeID = CodeThemeBlueGray
		th.TableLayout = render.TableLayoutFramed
		return th
	}
	bg := lipgloss.Color("#101922")
	card := lipgloss.Color("#18232e")
	border := lipgloss.Color("#344552")
	text := lipgloss.Color("#e2edf5")
	muted := lipgloss.Color("#9cafbf")
	accent := lipgloss.Color("#86cfee")
	success := lipgloss.Color("#38df92")
	warning := lipgloss.Color("#e5b766")
	danger := lipgloss.Color("#ff7f87")
	base := lipgloss.NewStyle().Background(bg)
	txt := base.Foreground(text)
	meta := base.Foreground(muted)
	brand := base.Foreground(accent)
	box := base.Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
	return Theme{
		CodeThemeID: CodeThemeBlueGray, TableLayout: render.TableLayoutFramed,
		Background: base, Card: meta.Background(card), ApprovalBorder: base.Foreground(warning), AskBorder: brand, Spinner: brand,
		SyntaxStyle: chroma.MustNewStyle("astonish-blue-gray", chroma.StyleEntries{
			chroma.Text: "#e2edf5", chroma.Comment: "#9cafbf italic", chroma.Keyword: "#86cfee bold", chroma.Name: "#e2edf5", chroma.NameFunction: "#86cfee", chroma.LiteralString: "#38df92", chroma.LiteralNumber: "#e5b766", chroma.Operator: "#86cfee", chroma.Error: "#ff7f87",
		}),
		Brand: brand.Bold(true), Text: txt, Muted: meta, User: brand.Bold(true), UserBorder: base.Foreground(border),
		UserBubble: txt.Border(lipgloss.NormalBorder()).BorderForeground(border).Padding(0, 2), UserExpandHint: meta.Italic(true).Align(lipgloss.Right),
		Agent: txt, System: meta.Italic(true), Error: base.Foreground(danger), Success: base.Foreground(success), Danger: base.Foreground(danger), Number: brand,
		Border: base.Foreground(border), Header: brand.Bold(true), Status: meta, Input: txt, Activity: brand, Approval: base.Foreground(warning).Bold(true), CodeGutter: meta,
		DiffAddedBg: txt.Background(lipgloss.Color("#1a3320")), DiffRemovedBg: txt.Background(lipgloss.Color("#3d1f1f")),
		PlanBorder: base.Foreground(border), PlanHeader: brand.Bold(true), PlanTitle: brand.Bold(true), PlanMuted: meta,
		PlanPhaseTitle: txt.Bold(true), PlanSummary: txt, PlanDetail: meta, PlanSection: brand, PlanSeparator: base.Foreground(border),
		InputBorder: box, InputBorderFocus: box, InputBorderPlan: box.BorderForeground(accent), InputPrompt: brand.Bold(true), InputPlaceholder: meta.Italic(true), FooterMeta: meta, Hint: meta, AccentColor: accent,
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

	bg := lipgloss.Color("#111416")            // near-black terminal background
	brand := lipgloss.Color("75")              // steel blue (replaces code-mode's 250 gray)
	muted := lipgloss.Color("245")             // gray (shared)
	dim := lipgloss.Color("240")               // dimmer gray (hints)
	text := lipgloss.Color("252")              // near white
	composerBorder := lipgloss.Color("39")     // bright cyan border (key differentiator)
	cyan := brand                              // user accent (legacy)
	green := lipgloss.Color("78")              // agent / success
	red := lipgloss.Color("203")               // error / danger
	highlight := lipgloss.Color("75")          // blue for numbers (was orange 208)
	userAccent := lipgloss.Color("#3F5A75")    // muted steel-blue user outline (was bronze #75633F)
	yellow := lipgloss.Color("221")            // approval
	border := lipgloss.Color("238")            // subtle separator border
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
		Agent:         lipgloss.NewStyle().Foreground(text).Background(bg),
		System:        lipgloss.NewStyle().Foreground(muted).Background(bg).Italic(true),
		Error:         lipgloss.NewStyle().Foreground(red).Background(bg),
		Success:       lipgloss.NewStyle().Foreground(green).Background(bg),
		Danger:        lipgloss.NewStyle().Foreground(red).Background(bg),
		Number:        lipgloss.NewStyle().Foreground(highlight).Background(bg),
		Border:        lipgloss.NewStyle().Foreground(border).Background(bg),
		Header:        lipgloss.NewStyle().Foreground(brand).Background(bg).Bold(true),
		Status:        lipgloss.NewStyle().Foreground(muted).Background(bg),
		Input:         lipgloss.NewStyle().Foreground(text).Background(bg),
		Activity:      lipgloss.NewStyle().Foreground(brand).Background(bg),
		Approval:      lipgloss.NewStyle().Foreground(yellow).Background(bg).Bold(true),
		CodeGutter:    lipgloss.NewStyle().Foreground(muted).Background(bg),
		DiffAddedBg:   lipgloss.NewStyle().Foreground(text).Background(diffAddedBg),
		DiffRemovedBg: lipgloss.NewStyle().Foreground(text).Background(diffRemovedBg),

		PlanBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Background(bg),
		PlanHeader: lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Background(bg).Bold(true),
		PlanTitle:  lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Background(bg).Bold(true),
		PlanMuted:  lipgloss.NewStyle().Foreground(muted).Background(bg),

		PlanPhaseTitle: lipgloss.NewStyle().Foreground(text).Background(bg).Bold(true),
		PlanSummary:    lipgloss.NewStyle().Foreground(text).Background(bg),
		PlanDetail:     lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Background(bg),
		PlanSection:    lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Background(bg),
		PlanSeparator:  lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Background(bg),

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
		PlanBorder: s, PlanHeader: s, PlanTitle: s, PlanMuted: s,
		PlanPhaseTitle: s, PlanSummary: s, PlanDetail: s, PlanSection: s, PlanSeparator: s,
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
	// Heading hierarchy uses the mode accent color (orange in code mode, cyan in
	// platform) so section headings like "Problem"/"Root causes"/"Boundaries"
	// read as deterministic accent titles, differentiated by weight.
	accent := th.Text.Foreground(th.AccentColor)
	if th.NoColor {
		accent = th.Text
	}
	return render.Styles{
		TableLayout: th.TableLayout, TableSurface: th.Background, TableHeader: accent.Bold(true), TableBorder: th.Border, SyntaxStyle: th.SyntaxStyle,
		Background:    th.Background,
		Text:          th.Text,
		Muted:         th.Muted,
		Brand:         th.Brand,
		Success:       th.Success,
		Danger:        th.Danger,
		Number:        th.Number,
		CodeGutter:    th.CodeGutter,
		CodeHeader:    th.Brand,
		Heading:       accent.Bold(true),
		Heading1:      accent.Bold(true),
		Heading2:      accent.Bold(true),
		Heading3:      accent,
		Bold:          th.Text.Bold(true),
		Italic:        th.Text.Italic(true),
		DiffAddedBg:   th.DiffAddedBg,
		DiffRemovedBg: th.DiffRemovedBg,
		NoColor:       th.NoColor,
	}
}

// planRenderStyles is RenderStyles with HeadingBar on, so plan-card markdown
// (context overview, bands, phase details) carves section headings into rooms.
func (th Theme) planRenderStyles() render.Styles {
	st := th.RenderStyles()
	st.HeadingBar = true
	return st
}

// backgroundSGR is independent of output color profile so reset repair always
// restores the canvas in truecolor terminals without changing explicit bands.
func (th Theme) backgroundSGR() string {
	if th.NoColor {
		return ""
	}
	r, g, b, _ := th.Background.GetBackground().RGBA()
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}
func (th Theme) restoreCanvas(s string) string {
	bg := th.backgroundSGR()
	s = strings.ReplaceAll(s, ansiReset, ansiReset+bg)
	s = strings.ReplaceAll(s, ansiResetShort, ansiResetShort+bg)
	return strings.ReplaceAll(s, ansiDefaultBG, ansiDefaultBG+bg)
}
func (th Theme) spinnerStyle() lipgloss.Style {
	if th.NoColor {
		return lipgloss.NewStyle()
	}
	if th.CodeThemeID != "" {
		return th.Spinner
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
}

func (th Theme) approvalBorderColor() color.Color {
	if th.NoColor {
		return lipgloss.NoColor{}
	}
	if th.CodeThemeID != "" {
		return th.ApprovalBorder.GetForeground()
	}
	return lipgloss.Color("221")
}
func (th Theme) permissionsBorderColor() color.Color {
	if th.NoColor {
		return lipgloss.NoColor{}
	}
	if th.CodeThemeID != "" {
		return th.Border.GetForeground()
	}
	return lipgloss.Color("39")
}
