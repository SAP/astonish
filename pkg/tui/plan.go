package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
)

// renderPlanDocument paints a PLAN.md transcript item. When the document
// parses, it becomes a structured Studio-inspired card (numbered phases, file
// kinds, narrative bands). Unparseable / mid-stream chunks fall back to the
// generic markdown-in-a-box path so history never goes blank.
func (m model) renderPlanDocument(it events.Item, width int) string {
	content := it.Content
	if doc, goal, steps, err := agent.ParsePlanDocument(content); err == nil && len(steps) > 0 {
		return m.renderPlanCard(goal, doc, steps, it, width)
	}
	return m.renderPlanDocumentMarkdown(content, it, width)
}

func (m model) renderPlanCard(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo, it events.Item, width int) string {
	th := m.theme
	if width < 30 {
		width = 30
	}
	inner := width - 6
	if inner < 20 {
		inner = 20
	}

	nFiles := 0
	nNew, nModify, nDelete := 0, 0, 0
	complete, running, pending := 0, 0, 0
	for _, s := range steps {
		nFiles += len(s.Files)
		for _, f := range s.Files {
			switch strings.ToLower(strings.TrimSpace(f.Kind)) {
			case "new", "add", "create":
				nNew++
			case "delete", "remove", "rm":
				nDelete++
			default:
				nModify++
			}
		}
		switch s.Status {
		case "complete":
			complete++
		case "running":
			running++
		case "failed":
			// failed phases still count toward "done" once nothing is pending/running
		default:
			pending++
		}
	}

	var body []string

	// Context band (overview / motivation).
	if ctx := strings.TrimSpace(doc.Context); ctx != "" {
		body = append(body, m.planBand("CONTEXT", ctx, inner)...)
		body = append(body, "")
	}

	// Phase blocks — each phase is a self-contained section separated by rules.
	// Parallel group headers are NOT shown inline; parallelization is summarized
	// at the end as an "Execution Order" section for cleaner scanning.
	for i, s := range steps {
		if i > 0 {
			body = append(body, "")
			rule := strings.Repeat("─", min(inner, 40))
			body = append(body, th.PlanSeparator.Render(rule))
			body = append(body, "")
		}
		body = append(body, m.planPhaseBlock(i+1, s, inner)...)
	}

	// Execution order section — shows parallelization if any groups exist.
	if order := m.planExecutionOrder(steps, inner); len(order) > 0 {
		body = append(body, "")
		rule := strings.Repeat("─", min(inner, 40))
		body = append(body, th.PlanSeparator.Render(rule))
		body = append(body, "")
		body = append(body, order...)
	}

	if guard := strings.TrimSpace(doc.WhatNotToDo); guard != "" {
		body = append(body, "")
		body = append(body, m.planBand("WHAT NOT TO CHANGE", guard, inner)...)
	}
	if ver := strings.TrimSpace(doc.Verification); ver != "" {
		body = append(body, "")
		body = append(body, m.planBand("VERIFY", ver, inner)...)
	}

	footer := planProgressFooter(complete, running, pending, len(steps))
	body = append(body, m.planApprovalLines(it, inner)...)
	title := strings.TrimSpace(goal)
	if title == "" {
		title = "Execution Plan"
	}
	meta := fmt.Sprintf("%d phases", len(steps))
	if nFiles > 0 {
		// Show file kind breakdown when there's a mix of kinds.
		kinds := 0
		if nNew > 0 {
			kinds++
		}
		if nModify > 0 {
			kinds++
		}
		if nDelete > 0 {
			kinds++
		}
		if kinds > 1 {
			var parts []string
			if nNew > 0 {
				parts = append(parts, fmt.Sprintf("%d new", nNew))
			}
			if nModify > 0 {
				parts = append(parts, fmt.Sprintf("%d modify", nModify))
			}
			if nDelete > 0 {
				parts = append(parts, fmt.Sprintf("%d delete", nDelete))
			}
			meta += fmt.Sprintf(" · %d files (%s)", nFiles, strings.Join(parts, ", "))
		} else {
			meta += fmt.Sprintf(" · %d files", nFiles)
		}
	}
	return m.paintPlanFrame(title, meta, body, footer, width, inner)
}

func (m model) planPhaseBlock(n int, s agent.PlanStepInfo, inner int) []string {
	th := m.theme
	desc := strings.TrimSpace(s.Description)
	if desc == "" {
		desc = strings.TrimSpace(s.Name)
	}

	// Phase heading: status glyph + number + description (bold).
	prefix := planStatusGlyph(s.Status, th) + " " + th.Number.Render(fmt.Sprintf("%d", n)) + "  "
	lines := wrapPrefixed(prefix, desc, inner, th.PlanPhaseTitle)

	// Summary / Intent: the plain-English explanation for the approver.
	if summary := strings.TrimSpace(s.Summary); summary != "" {
		lines = append(lines, "")
		lines = append(lines, wrapPrefixed("", summary, inner, th.PlanSummary)...)
	}

	// FILES section.
	if len(s.Files) > 0 {
		hasFile := false
		for _, f := range s.Files {
			if strings.TrimSpace(f.Path) != "" {
				hasFile = true
				break
			}
		}
		if hasFile {
			lines = append(lines, "")
			lines = append(lines, th.PlanSection.Render("FILES"))
			for _, f := range s.Files {
				if strings.TrimSpace(f.Path) == "" {
					continue
				}
				glyph, st := planFileKindStyle(f.Kind, th)
				lines = append(lines, "  "+st.Render(glyph)+" "+f.Path)
			}
		}
	}

	// VERIFY section.
	if v := strings.TrimSpace(s.Verify); v != "" {
		lines = append(lines, "")
		lines = append(lines, th.PlanSection.Render("VERIFY"))
		lines = append(lines, wrapPrefixed("  ", "$ "+v, inner, th.PlanMuted)...)
	}

	// DETAILS section.
	if d := strings.TrimSpace(s.Details); d != "" {
		lines = append(lines, "")
		lines = append(lines, th.PlanSection.Render("DETAILS"))
		detailWidth := inner - 2 // account for "  " indent
		if detailWidth < 20 {
			detailWidth = 20
		}
		md := render.Markdown(d, detailWidth, th.RenderStyles())
		if md == "" {
			// Fallback: plain text if markdown rendering produces nothing.
			md = th.PlanDetail.Width(detailWidth).Render(d)
		}
		for _, dl := range strings.Split(md, "\n") {
			lines = append(lines, "  "+dl)
		}
	}
	return lines
}

// planExecutionOrder renders a compact "EXECUTION ORDER" section at the end of
// the plan that shows which phases can run in parallel. Only shown when at least
// one phase has a parallel group.
func (m model) planExecutionOrder(steps []agent.PlanStepInfo, inner int) []string {
	// Collect groups in order of first appearance.
	type groupInfo struct {
		label string
		names []string
	}
	var groups []groupInfo
	groupIdx := map[string]int{}
	var serial []string

	for _, s := range steps {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = strings.TrimSpace(s.Description)
		}
		if s.ParallelGroup == "" {
			serial = append(serial, name)
		} else {
			if idx, ok := groupIdx[s.ParallelGroup]; ok {
				groups[idx].names = append(groups[idx].names, name)
			} else {
				groupIdx[s.ParallelGroup] = len(groups)
				groups = append(groups, groupInfo{label: s.ParallelGroup, names: []string{name}})
			}
		}
	}

	// Only show if there's actual parallelism.
	if len(groups) == 0 {
		return nil
	}

	th := m.theme
	var lines []string
	lines = append(lines, th.PlanSection.Render("EXECUTION ORDER"))

	for _, g := range groups {
		if len(g.names) > 1 {
			lines = append(lines, "  "+th.PlanMuted.Render("⟳ "+g.label+" (parallel):"))
			for _, name := range g.names {
				lines = append(lines, "    "+th.Text.Render("• "+name))
			}
		} else {
			lines = append(lines, "  "+th.PlanMuted.Render(g.label+": ")+th.Text.Render(g.names[0]))
		}
	}
	for _, name := range serial {
		lines = append(lines, "  "+th.PlanMuted.Render("→ ")+th.Text.Render(name)+" "+th.PlanMuted.Render("(serial)"))
	}
	return lines
}

func (m model) planBand(label, body string, inner int) []string {
	th := m.theme
	lines := []string{th.PlanHeader.Render(label)}
	// Render band body as markdown for proper formatting (inline code,
	// bold, lists) instead of plain muted text.
	md := render.Markdown(body, inner, th.RenderStyles())
	if md == "" {
		// Fallback: plain wrapped text.
		for _, para := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
			if strings.TrimSpace(para) == "" {
				lines = append(lines, "")
				continue
			}
			lines = append(lines, wrapPrefixed("", para, inner, th.PlanMuted)...)
		}
	} else {
		for _, ml := range strings.Split(md, "\n") {
			lines = append(lines, ml)
		}
	}
	return lines
}

func (m model) planApprovalLines(it events.Item, inner int) []string {
	if it.PlanStatus == "" {
		return nil
	}
	lines := []string{"", m.theme.PlanMuted.Render(strings.Repeat("─", min(inner, 24))), ""}
	switch it.PlanStatus {
	case events.PlanPending:
		lines = append(lines, m.theme.PlanHeader.Render("✦ Plan Ready · choose how to proceed"))
		opts := it.Options
		if len(opts) == 0 {
			opts = []string{planOptApprove, planOptRequest, planOptDecline}
		}
		cursor := m.tr.ApprovalCursor
		for i, option := range opts {
			key := []string{"Enter", "r", "n"}[min(i, 2)]
			label := fmt.Sprintf("%s  %s", key, option)
			if i == cursor {
				lines = append(lines, m.theme.PlanHeader.Bold(true).Render("▸ "+label))
			} else {
				lines = append(lines, m.theme.PlanMuted.Render("  "+label))
			}
		}
	case events.PlanApproved:
		lines = append(lines, m.theme.Success.Render("✓ Approved"))
	case events.PlanChangesRequested:
		lines = append(lines, m.theme.PlanHeader.Render("↺ Changes requested"))
	case events.PlanDeclined:
		lines = append(lines, m.theme.Error.Render("✗ Declined"))
	}
	return lines
}

func planStatusGlyph(status string, th Theme) string {
	switch status {
	case "complete":
		return th.Success.Render("[✓]")
	case "running":
		return th.Brand.Render("[●]")
	case "failed":
		return th.Error.Render("[✗]")
	default:
		return th.PlanMuted.Render("[○]")
	}
}

func planFileKindStyle(kind string, th Theme) (string, lipgloss.Style) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "new", "add", "create":
		return "+", th.Success
	case "delete", "remove", "rm":
		return "−", th.Error
	default:
		return "~", th.PlanMuted
	}
}

func planProgressFooter(complete, running, pending, total int) string {
	switch {
	case running > 0:
		return fmt.Sprintf("%d/%d · running", complete, total)
	case pending == 0 && running == 0:
		return fmt.Sprintf("%d/%d done", complete, total)
	default:
		return fmt.Sprintf("%d/%d ready", complete, total)
	}
}

func wrapPrefixed(prefix, text string, width int, textStyle lipgloss.Style) []string {
	if width < 8 {
		width = 8
	}
	prefixW := lipgloss.Width(prefix)
	avail := width - prefixW
	if avail < 8 {
		avail = 8
	}
	wrapped := strings.Split(wrapPlain(text, avail), "\n")
	out := make([]string, 0, len(wrapped))
	indent := strings.Repeat(" ", prefixW)
	for i, w := range wrapped {
		if i == 0 {
			out = append(out, prefix+textStyle.Render(w))
		} else {
			out = append(out, indent+textStyle.Render(w))
		}
	}
	return out
}

func (m model) paintPlanFrame(title, meta string, body []string, footer string, width, inner int) string {
	th := m.theme
	if width < inner+6 {
		width = inner + 6
	}

	titleText := " ✦ " + title + " "
	maxTitle := inner - 4
	if meta != "" {
		maxTitle -= lipgloss.Width(meta) + 3
	}
	if maxTitle < 8 {
		maxTitle = 8
	}
	if lipgloss.Width(titleText) > maxTitle+3 { // include " ✦ " slack
		title = truncateToWidth(title, maxTitle)
		titleText = " ✦ " + title + " "
	}
	titleRendered := th.PlanHeader.Render(titleText)
	metaRendered := ""
	if meta != "" {
		metaRendered = th.PlanMuted.Render(" " + meta + " ")
	}

	var b strings.Builder
	b.WriteString(m.planTopBorder(width, titleRendered, metaRendered))

	writeBody := func(line string) {
		b.WriteByte('\n')
		lineW := lipgloss.Width(line)
		pad := inner - lineW
		if pad < 0 {
			line = truncateToWidth(line, inner)
			pad = inner - lipgloss.Width(line)
			if pad < 0 {
				pad = 0
			}
		}
		b.WriteString(th.PlanBorder.Render("│"))
		b.WriteString(th.Background.Render("  "))
		b.WriteString(line)
		b.WriteString(th.Background.Render(strings.Repeat(" ", pad)))
		b.WriteString(th.Background.Render("  "))
		b.WriteString(th.PlanBorder.Render("│"))
	}

	for _, line := range body {
		writeBody(line)
	}

	if footer != "" {
		if len(body) > 0 {
			writeBody("")
		}
		fw := lipgloss.Width(footer)
		if fw > inner {
			footer = truncateToWidth(footer, inner)
			fw = lipgloss.Width(footer)
		}
		pad := inner - fw
		if pad < 0 {
			pad = 0
		}
		writeBody(th.Background.Render(strings.Repeat(" ", pad)) + th.PlanMuted.Render(footer))
	}

	b.WriteByte('\n')
	b.WriteString(th.PlanBorder.Render("└" + strings.Repeat("─", width-2) + "┘"))
	return b.String()
}

func (m model) planTopBorder(width int, titleRendered, metaRendered string) string {
	th := m.theme
	titleW := lipgloss.Width(titleRendered)
	metaW := lipgloss.Width(metaRendered)
	avail := width - 2 // minus ┌ ┐
	leftW := 1
	rest := avail - leftW - titleW - metaW
	if rest < 0 {
		metaRendered = ""
		rest = avail - leftW - titleW
	}
	if rest < 0 {
		rest = 0
	}
	return th.PlanBorder.Render("┌"+strings.Repeat("─", leftW)) +
		titleRendered +
		th.PlanBorder.Render(strings.Repeat("─", rest)) +
		metaRendered +
		th.PlanBorder.Render("┐")
}

// renderPlanDocumentMarkdown is the fallback path for unparseable / partial
// PLAN.md text: generic goldmark inside the same bordered frame.
func (m model) renderPlanDocumentMarkdown(content string, it events.Item, width int) string {
	th := m.theme
	if width < 30 {
		width = 30
	}
	inner := width - 6
	if inner < 20 {
		inner = 20
	}

	title := "Execution Plan"
	for _, line := range strings.SplitN(content, "\n", 10) {
		if strings.HasPrefix(line, "**Goal:**") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "**Goal:**"))
			if len(title) > inner-10 {
				title = title[:inner-13] + "…"
			}
			break
		}
	}

	body := content
	if strings.HasPrefix(body, "# Execution Plan\n") {
		body = strings.TrimPrefix(body, "# Execution Plan\n")
	}
	body = strings.TrimSpace(body)

	md := render.Markdown(body, inner, th.RenderStyles())
	if md == "" {
		md = th.Text.Width(inner).Render(body)
	}
	md = m.stylePlanCheckboxes(md)

	var bodyLines []string
	if md != "" {
		bodyLines = strings.Split(md, "\n")
	}
	bodyLines = append(bodyLines, m.planApprovalLines(it, inner)...)
	return m.paintPlanFrame(title, "", bodyLines, "", width, inner)
}

// stylePlanCheckboxes replaces plain markdown checkbox markers in rendered plan
// text with colored status indicators for visual clarity.
func (m model) stylePlanCheckboxes(rendered string) string {
	th := m.theme
	rendered = strings.ReplaceAll(rendered, "[x]", th.Success.Render("[✓]"))
	rendered = strings.ReplaceAll(rendered, "[X]", th.Success.Render("[✓]"))
	rendered = strings.ReplaceAll(rendered, "[~]", th.Brand.Render("[●]"))
	rendered = strings.ReplaceAll(rendered, "[ ]", th.PlanMuted.Render("[○]"))
	rendered = strings.ReplaceAll(rendered, "[!]", th.Error.Render("[✗]"))
	return rendered
}

// planDocumentContentSpan returns the [start,end) rune-column range of real
// content within a rendered plan document line, excluding the border characters
// and interior padding. Used for drag-to-copy selection.
func planDocumentContentSpan(_ int, plain string) [2]int {
	runes := []rune(plain)
	first := -1
	last := -1
	for i, r := range runes {
		if r == '│' {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 || last <= first {
		return [2]int{0, 0}
	}
	start := first + 1
	end := last
	for start < end && runes[start] == ' ' {
		start++
	}
	for end > start && runes[end-1] == ' ' {
		end--
	}
	return [2]int{start, end}
}
