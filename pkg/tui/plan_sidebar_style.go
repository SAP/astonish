package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Sidebar-specific surfaces do not alter the transcript or document theme.
type planPanelStyles struct {
	surface, border, title, muted, card, success, active, failed lipgloss.Style
}

func (m model) planPanelStyles() planPanelStyles {
	th := m.theme
	if th.NoColor {
		p := lipgloss.NewStyle()
		return planPanelStyles{p, p, p, p, p, p, p, p}
	}
	return planPanelStyles{th.Background, th.Border, th.Text.Bold(true), th.Muted, th.Card, th.Success.Bold(true), th.Approval.Bold(true), th.Danger.Bold(true)}
}

func (m model) planPanelStatus(v planProgressView) (string, lipgloss.Style) {
	s := m.planPanelStyles()
	switch v.State {
	case planProgressCompleted:
		return "✓ COMPLETED", s.success
	case planProgressFailed:
		return "✗ Phase failed", s.failed
	case planProgressActive:
		return "● Executing", s.active
	case planProgressReviewPaused:
		return "◷ Paused for phase review", s.active
	default:
		return "○ " + planLifecycleLabel(v), s.muted
	}
}

// A single metrics path owns both viewport sizing and visible header rendering.
// Short panes keep the original concise facts, reserving space for phase cards.
func (m model) planPanelHeader(width, height int) string {
	v := m.planProgress()
	s := m.planPanelStyles()
	title := "PLAN PROGRESS"
	badge := "Ctrl+G"
	heading := s.title
	if m.theme.CodeThemeID != "" && !m.theme.NoColor {
		heading = m.theme.PlanHeader
	}
	lines := []string{heading.Render(title) + strings.Repeat(" ", max(1, width-len(title)-len(badge))) + s.muted.Render(badge)}
	rule := s.border.Render(strings.Repeat("─", width))
	status, style := m.planPanelStatus(v)
	goal := v.Goal
	if goal == "" {
		goal = "Plan"
	}
	goalLines := strings.Split(wrapPlain(goal, width), "\n")
	if height >= 36 {
		lines = append(lines, "", s.muted.Render(strings.Join(goalLines[:min(2, len(goalLines))], "\n")), "", style.Render(wrapPlain(status, width)))
		if v.State == planProgressCompleted {
			lines = append(lines, s.muted.Render("Results recorded"))
		}
		lines = append(lines, "", rule, "")
	} else if height >= 12 {
		lines = append(lines, s.muted.Render(truncateToWidth(goal, width)), style.Render(wrapPlain(status, width)))
		if v.State == planProgressCompleted {
			lines = append(lines, s.muted.Render("Results recorded"))
		}
	} else {
		lines = append(lines, style.Render(truncateToWidth(status, width)))
	}
	if v.PercentAvailable {
		fraction := fmt.Sprintf("%d/%d phases complete", v.Counts.Complete, v.Counts.Total)
		if height >= 36 {
			lines = append(lines, s.title.Render(fraction), s.title.Render(fmt.Sprintf("%d%%", v.Percent)))
		} else {
			lines = append(lines, s.title.Render(fraction), s.title.Render(fmt.Sprintf("%d%%", v.Percent)))
		}
		filled := width * v.Percent / 100
		lines = append(lines, s.success.Render(strings.Repeat("━", filled))+s.border.Render(strings.Repeat("─", width-filled)))
	} else {
		lines = append(lines, s.muted.Render("Progress: unavailable"))
	}
	if height >= 36 {
		col := (width - 2) / 3
		last := width - 2 - 2*col
		stat := func(value int, label string, w int) string {
			return s.title.Width(w).Align(lipgloss.Center).Render(fmt.Sprint(value)) + "\n" + s.muted.Width(w).Align(lipgloss.Center).Render(label)
		}
		divider := s.border.Render("│") + "\n" + s.border.Render("│")
		lines = append(lines, "", lipgloss.JoinHorizontal(lipgloss.Top, stat(v.Counts.Running, "running", col), divider, stat(v.Counts.Pending, "pending", col), divider, stat(v.Counts.Failed, "failed", last)), "", rule, "", s.title.Render("PHASES"))
	} else {
		if height >= 18 && (v.Counts.Running > 1 || v.Counts.Failed > 0) {
			lines = append(lines, s.muted.Render(fmt.Sprintf("%d running · %d failed", v.Counts.Running, v.Counts.Failed)))
		}
		if height >= 18 {
			lines = append(lines, rule, s.title.Render("PHASES"))
		}
	}
	return strings.Join(lines, "\n")
}

// planPhaseCardSpan uses the same rendered rows as the viewport content. Phase
// names are stable identities; indexes describe only the current display order.
type planPhaseCardSpan struct {
	name       string
	index      int
	start, end int // inclusive start, exclusive end (not including the card gap)
}

func (m model) renderPlanPhaseCards(width int) string {
	content, _ := m.layoutPlanPhaseCards(width)
	return content
}

func (m model) layoutPlanPhaseCards(width int) (string, []planPhaseCardSpan) {
	v := m.planProgress()
	s := m.planPanelStyles()
	if !v.Available {
		return s.muted.Render(wrapPlain("The latest plan could not be read. Progress: unavailable", width)), nil
	}
	if len(v.Phases) == 0 {
		return s.muted.Render("No phases recorded"), nil
	}
	var cards []string
	var spans []planPhaseCardSpan
	row := 0
	for index, p := range v.Phases {
		title, icon, qualification := planPhasePresentation(p)
		accent := s.muted
		switch p.State {
		case planPhaseComplete:
			accent = s.success
		case planPhaseRunning:
			accent = s.active
		case planPhaseFailed:
			accent = s.failed
		}
		textWidth := max(1, width-4)
		titleLines := strings.Split(wrapPlain(title, textWidth), "\n")
		rows := []string{}
		for i, line := range titleLines {
			prefix := "  "
			if i == 0 {
				prefix = accent.Background(s.card.GetBackground()).Render(icon) + " "
			}
			titleStyle := s.card
			if !m.theme.NoColor {
				titleStyle = titleStyle.Bold(true).Foreground(s.title.GetForeground())
			}
			rows = append(rows, prefix+titleStyle.Render(line))
		}
		if qualification != "" {
			for _, line := range strings.Split(wrapPlain(qualification, textWidth), "\n") {
				rows = append(rows, "  "+s.card.Render(line))
			}
		}
		// Explicit painted padding keeps resets within the card surface.
		for i, row := range rows {
			rows[i] = s.card.Render(" ") + row + s.card.Render(strings.Repeat(" ", max(0, width-1-lipgloss.Width(row))))
			if !m.theme.NoColor {
				rows[i] = restorePlanSurface(rows[i], s.card)
			}
		}
		padding := s.card.Render(strings.Repeat(" ", max(1, width)))
		rows = append([]string{padding}, rows...)
		rows = append(rows, padding)
		card := strings.Join(rows, "\n")
		cards = append(cards, card)
		end := row + lipgloss.Height(card)
		spans = append(spans, planPhaseCardSpan{name: p.Name, index: index, start: row, end: end})
		row = end + 1
	}
	return strings.Join(cards, "\n\n"), spans
}

// restorePlanSurface restores the owning surface after nested style resets.
// The screen painter may insert the conversation background after a reset;
// this explicit background follows it before any printable padding or text.
// Inner card restorations remain later in the stream and take precedence.
func restorePlanSurface(text string, surface lipgloss.Style) string {
	r, g, b, _ := surface.GetBackground().RGBA()
	bg := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
	text = strings.ReplaceAll(text, ansiReset, ansiReset+bg)
	text = strings.ReplaceAll(text, ansiResetShort, ansiResetShort+bg)
	return strings.ReplaceAll(text, ansiDefaultBG, ansiDefaultBG+bg)
}

func (m model) planPanelFrame(content string, height int) string {
	if height <= 0 {
		return ""
	}
	width := planSidebarWidth
	s := m.planPanelStyles()
	if height < 3 {
		return planRows(content, width, height)
	}
	inner := width - 4
	rows := strings.Split(planRows(content, inner, height-2), "\n")
	out := []string{s.border.Render("╭" + strings.Repeat("─", width-2) + "╮")}
	for _, row := range rows {
		if !m.theme.NoColor {
			row = restorePlanSurface(row, s.surface)
		}
		out = append(out, s.border.Render("│")+s.surface.Render(" ")+s.surface.Render(row)+s.surface.Render(" ")+s.border.Render("│"))
	}
	out = append(out, s.border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(out, "\n")
}
