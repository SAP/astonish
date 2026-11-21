package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

const planSidebarWidth = 28

type planLayoutMode uint8

const (
	planLayoutHidden planLayoutMode = iota
	planLayoutSidebar
	planLayoutCompact
)

// Only ephemeral navigation is kept here. Progress always comes from Transcript.
type planPanelState struct {
	owner    *events.Transcript
	identity string
	awaiting bool
	mode     planLayoutMode
	open     bool
	sidebar  viewport.Model
	detail   viewport.Model

	// Browsing never disables future transitions. Only a changed selected phase
	// requests positioning; hidden layouts defer that request until visible.
	target        string
	revealPending bool
	spans         []planPhaseCardSpan
	revision      int64
}

func (m model) planProgress() planProgressView {
	if m.info.Mode != "code" || m.tr == nil {
		return planProgressView{}
	}
	return latestPlanProgress(m.tr.Items).withStreaming(m.tr.Streaming)
}

func (m model) planLayout() planLayoutMode {
	if m.info.Mode != "code" || m.tr == nil {
		return planLayoutHidden
	}
	present := false
	for i := len(m.tr.Items) - 1; i >= 0; i-- {
		if m.tr.Items[i].Kind == events.ItemPlan {
			present = true
			break
		}
	}
	if !present {
		return planLayoutHidden
	}
	if m.width >= 72+1+planSidebarWidth {
		return planLayoutSidebar
	}
	return planLayoutCompact
}

func (m model) transcriptWidth() int {
	if m.planLayout() == planLayoutSidebar {
		return m.width - planSidebarWidth - 1
	}
	return m.width
}

func (m model) compactPlanHeight() int {
	if m.planLayout() == planLayoutCompact {
		return 2
	}
	return 0
}

func (m *model) syncPlanPanel() {
	v := m.planProgress()
	identity := ""
	if v.Present {
		for i := len(m.tr.Items) - 1; i >= 0; i-- {
			if m.tr.Items[i].Kind == events.ItemPlan {
				identity = fmt.Sprintf("%s/%s/%d", m.tr.SessionID, v.PlanID, i)
				break
			}
		}
	}
	if m.planPanel.owner != m.tr || m.planPanel.identity != identity || !v.Present {
		m.planPanel = planPanelState{owner: m.tr, identity: identity, sidebar: viewport.New(), detail: viewport.New()}
	}
	if m.tr != nil {
		m.planPanel.awaiting = m.tr.Awaiting
	}
	m.planPanel.mode = m.planLayout()
	m.layoutPlanPanel()
}

func planLifecycleLabel(v planProgressView) string {
	switch v.State {
	case planProgressUnavailable:
		return "Progress unavailable"
	case planProgressAwaitingApproval:
		return "Awaiting approval"
	case planProgressChangesRequested:
		return "Changes requested"
	case planProgressDeclined:
		return "Declined"
	case planProgressActive:
		return "Executing"
	case planProgressReviewPaused:
		return "Paused for phase review"
	case planProgressFailed:
		return "Phase failed"
	case planProgressAwaitingCompletionRecord:
		return "Awaiting completion record"
	case planProgressCompleted:
		return "Completed · Results recorded"
	default:
		return "Idle · incomplete"
	}
}

func planCountsLabel(v planProgressView) string {
	if !v.Available || !v.PercentAvailable {
		return "Progress: unavailable"
	}
	return fmt.Sprintf("%d/%d phases complete · %d%%", v.Counts.Complete, v.Counts.Total, v.Percent)
}

func planCurrentNames(v planProgressView) string {
	var names []string
	for _, p := range v.Phases {
		if p.Current {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return "Current: none"
	}
	return "Current: " + strings.Join(names, ", ")
}

func planRows(text string, width, height int) string {
	if height <= 0 {
		return ""
	}
	rows := strings.Split(text, "\n")
	out := make([]string, height)
	for i := range out {
		if i < len(rows) {
			out[i] = truncateToWidth(rows[i], max(1, width))
		}
		out[i] += strings.Repeat(" ", max(0, width-lipgloss.Width(out[i])))
	}
	return strings.Join(out, "\n")
}

func (m model) renderPlanProgressHeader(width int) string {
	v := m.planProgress()
	goal := v.Goal
	if goal == "" {
		goal = "Plan"
	}
	goal = planRows(wrapPlain(goal, width), width, 2)
	lines := []string{"Plan progress · ctrl+g", goal, wrapPlain(planLifecycleLabel(v), width), wrapPlain(planCountsLabel(v), width)}
	if v.PercentAvailable {
		cells := max(1, width-2)
		filled := cells * v.Percent / 100
		lines = append(lines, "["+strings.Repeat("=", filled)+strings.Repeat("-", cells-filled)+"]")
	}
	if v.Available {
		lines = append(lines, wrapPlain(fmt.Sprintf("%d running · %d pending · %d failed", v.Counts.Running, v.Counts.Pending, v.Counts.Failed), width))
		if v.Counts.Unknown > 0 {
			lines = append(lines, fmt.Sprintf("%d unknown", v.Counts.Unknown))
		}
		lines = append(lines, wrapPlain(planCurrentNames(v), width))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderPlanProgressBody(width int, full bool) string {
	v := m.planProgress()
	if !v.Available {
		return wrapPlain("The latest plan could not be read. Progress: unavailable", width)
	}
	if len(v.Phases) == 0 {
		return "No phases recorded"
	}
	var b strings.Builder
	var steps []agent.PlanStepInfo
	if full {
		for i := len(m.tr.Items) - 1; i >= 0; i-- {
			it := m.tr.Items[i]
			if it.Kind != events.ItemPlan {
				continue
			}
			if it.PlanSnapshot != nil {
				steps = it.PlanSnapshot.Steps
			} else {
				_, _, steps, _ = agent.ParsePlanDocument(it.Content)
			}
			break
		}
	}
	for i, p := range v.Phases {
		title := p.Description
		if title == "" {
			title = p.Name
		}
		fmt.Fprintf(&b, "%s\n", wrapPlain(fmt.Sprintf("%d. %s", i+1, title), width))
		if title != p.Name && p.Name != "" {
			fmt.Fprintf(&b, "%s\n", wrapPlain(p.Name, width))
		}
		status := string(p.State)
		if p.State == planPhasePending {
			status = "pending · not started"
		}
		if p.Current && !p.Live {
			status += " · recorded, not live"
		}
		if p.Live {
			status += " · live"
		}
		fmt.Fprintf(&b, "%s\n", wrapPlain(status, width))
		if p.ParallelGroup != "" {
			fmt.Fprintf(&b, "%s\n", wrapPlain("Group: "+p.ParallelGroup, width))
		}
		if p.Current {
			fmt.Fprintln(&b, "Progress: unavailable")
		}
		if full {
			if p.Summary != "" {
				fmt.Fprintf(&b, "%s\n", wrapPlain(p.Summary, width))
			}
			if i < len(steps) && steps[i].Evidence != "" {
				fmt.Fprintf(&b, "%s\n", wrapPlain("Evidence: "+steps[i].Evidence, width))
			}
		}
		fmt.Fprintln(&b)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Select in plan order, not live-worker order. Restored running phases are
// eligible without changing their recorded/not-live presentation.
func planSidebarTarget(v planProgressView) string {
	if !v.Available {
		return ""
	}
	switch v.State {
	case planProgressCompleted, planProgressDeclined, planProgressChangesRequested, planProgressAwaitingApproval, planProgressUnavailable:
		return ""
	}
	for _, p := range v.Phases {
		if p.Current {
			return p.Name
		}
	}
	for _, p := range v.Phases {
		if v.ReviewPending != "" && p.Name == v.ReviewPending {
			return p.Name
		}
	}
	for _, p := range v.Phases {
		if p.State == planPhaseFailed {
			return p.Name
		}
	}
	return ""
}

func (m *model) layoutPlanPanel() {
	if m.planLayout() == planLayoutHidden {
		return
	}
	v := m.planProgress()
	target := planSidebarTarget(v)
	if target != m.planPanel.target {
		m.planPanel.target = target
		m.planPanel.revealPending = target != ""
	}
	h := m.vp.Height() + m.stickyHeaderLines
	inner := planSidebarWidth - 4
	headerH := lipgloss.Height(m.planPanelHeader(inner, h))
	offset := m.planPanel.sidebar.YOffset()
	content, spans := m.layoutPlanPhaseCards(inner)
	// A definition revision may reflow cards above the reader. Preserve the
	// first visible surviving card and the row within it, not its old index.
	if !m.planPanel.revealPending && v.Revision != m.planPanel.revision {
		for _, old := range m.planPanel.spans {
			if old.end <= offset || old.start >= offset+m.planPanel.sidebar.Height() {
				continue
			}
			for _, span := range spans {
				if span.name == old.name {
					offset = span.start + min(max(0, offset-old.start), span.end-span.start-1)
					break
				}
			}
			break
		}
	}
	m.planPanel.spans, m.planPanel.revision = spans, v.Revision
	m.planPanel.sidebar.SetWidth(inner)
	m.planPanel.sidebar.SetHeight(max(1, h-2-headerH))
	m.planPanel.sidebar.SetContent(content)
	m.planPanel.sidebar.SetYOffset(offset)
	if m.planPanel.revealPending && m.planLayout() == planLayoutSidebar && !m.planPanel.open && h >= 10 && h-2-headerH > 0 {
		for _, span := range spans {
			if span.name != target {
				continue
			}
			offset = m.planPanel.sidebar.YOffset()
			height := m.planPanel.sidebar.Height()
			switch {
			case span.end-span.start > height, span.start < offset:
				offset = span.start
			case span.end > offset+height:
				offset = span.end - height
			}
			m.planPanel.sidebar.SetYOffset(offset)
			m.planPanel.revealPending = false
			break
		}
	}
	if m.planPanel.open {
		offset = m.planPanel.detail.YOffset()
		m.planPanel.detail.SetWidth(max(1, m.width))
		m.planPanel.detail.SetHeight(max(1, m.screenHeight()-2))
		m.planPanel.detail.SetContent(m.renderPlanProgressHeader(max(1, m.width)) + "\n\n" + m.renderPlanProgressBody(max(1, m.width), true))
		m.planPanel.detail.SetYOffset(offset)
	}
}

func (m model) renderPlanSidebar(height int) string {
	if height < 10 {
		v := m.planProgress()
		return planRows(planCountsLabel(v)+"\n"+planLifecycleLabel(v), planSidebarWidth, height)
	}
	header := m.planPanelHeader(planSidebarWidth-4, height)
	return m.planPanelFrame(header+"\n"+m.planPanel.sidebar.View(), height)
}

func (m model) renderCompactPlanProgress() string {
	v := m.planProgress()
	first := "Plan · " + planCountsLabel(v) + " · ctrl+g · " + planLifecycleLabel(v)
	second := planCurrentNames(v) + " · " + v.Goal
	return planRows(first+"\n"+second, max(1, m.width), 2)
}

// conversationBody is shared by normal, plan-approval and tool-approval layouts.
// The panel spans the sticky header and transcript; composer and chrome stay wide.
func (m model) conversationBody(height int) string {
	var left string
	if sticky := m.stickyUserMessage(); sticky != "" && m.stickyHeaderLines > 0 {
		left = m.paintTranscriptBlock(padBlock(m.renderStickyUserHeader(sticky, contentWidth(m.transcriptWidth()), m.stickyExpanded))) + "\n"
	}
	left += m.vp.View()
	compactH := m.compactPlanHeight()
	bodyH := max(1, height-compactH)
	left = planRows(left, max(1, m.transcriptWidth()), bodyH)
	switch m.planLayout() {
	case planLayoutSidebar:
		divider := strings.TrimSuffix(strings.Repeat(m.theme.Border.Render("│")+"\n", bodyH), "\n")
		return lipgloss.JoinHorizontal(lipgloss.Top, left, divider, m.renderPlanSidebar(bodyH))
	case planLayoutCompact:
		return m.renderCompactPlanProgress() + "\n" + left
	default:
		return left
	}
}

func (m model) planModalBlocked() bool {
	return m.fileViewer.open || m.delegationDetail.open || m.bots.open || m.sessions.open || m.modelPicker.open || m.webSearchPicker.open || m.skillsPicker.open || m.rollback.open || m.reauthProvider != ""
}

func (m model) handlePlanProgressKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.planLayout() == planLayoutHidden || m.planModalBlocked() {
		return m, nil, false
	}
	if msg.String() == "ctrl+g" {
		m.planPanel.open = !m.planPanel.open
		m.layoutPlanPanel()
		return m, nil, true
	}
	if !m.planPanel.open || msg.String() == "ctrl+c" {
		return m, nil, false
	}
	if msg.String() == "esc" {
		m.planPanel.open = false
		m.layoutPlanPanel()
		return m, nil, true
	}
	var cmd tea.Cmd
	m.planPanel.detail, cmd = m.planPanel.detail.Update(msg)
	return m, cmd, true
}

func (m model) handlePlanProgressMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	if m.planLayout() == planLayoutHidden || m.planModalBlocked() {
		return m, nil, false
	}
	mouse := msg.Mouse()
	if m.planPanel.open {
		var cmd tea.Cmd
		m.planPanel.detail, cmd = m.planPanel.detail.Update(msg)
		return m, cmd, true
	}
	compact := m.planLayout() == planLayoutCompact && mouse.Y >= 2 && mouse.Y < 4
	panel := m.planLayout() == planLayoutSidebar && mouse.X >= m.transcriptWidth() && mouse.Y >= 2 && mouse.Y < m.viewportTopY()+m.vp.Height()
	if !compact && !panel {
		return m, nil, false
	}
	headerEnd := 3 + lipgloss.Height(m.planPanelHeader(planSidebarWidth-4, m.vp.Height()+m.stickyHeaderLines))
	if _, ok := msg.(tea.MouseWheelMsg); ok && panel && mouse.X > m.transcriptWidth() && mouse.Y >= headerEnd && mouse.Y < headerEnd+m.planPanel.sidebar.Height() && m.vp.Height()+m.stickyHeaderLines >= 10 {
		var cmd tea.Cmd
		m.planPanel.sidebar, cmd = m.planPanel.sidebar.Update(msg)
		return m, cmd, true
	}
	if _, ok := msg.(tea.MouseClickMsg); ok && mouse.Button == tea.MouseLeft && (compact || mouse.Y < 3+lipgloss.Height(m.planPanelHeader(planSidebarWidth-4, m.vp.Height()+m.stickyHeaderLines))) {
		m.planPanel.open = true
		m.layoutPlanPanel()
	}
	return m, nil, true
}

func (m model) renderFullPlanProgress() string {
	return planRows("Plan progress · esc back · ctrl+g close · ↑↓ / pgup pgdown scroll\n"+m.planPanel.detail.View(), max(1, m.width), max(1, m.screenHeight()))
}
