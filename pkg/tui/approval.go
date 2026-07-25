package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// handleApprovalKey handles y/n and option keys while awaiting tool approval.
// Returns handled=true when the key was consumed.
func (m model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !m.tr.Awaiting {
		return m, nil, false
	}
	opts := m.approvalOptions()
	key := msg.String()

	switch key {
	case "esc":
		next, cmd := m.submitApproval(pickNo(opts))
		return next, cmd, true
	case "y", "Y":
		next, cmd := m.submitApproval(pickYes(opts))
		return next, cmd, true
	case "n", "N":
		next, cmd := m.submitApproval(pickNo(opts))
		return next, cmd, true
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(key[0] - '1')
		if idx >= 0 && idx < len(opts) {
			next, cmd := m.submitApproval(opts[idx])
			return next, cmd, true
		}
	case "enter":
		// Default to first option (usually Yes)
		if len(opts) > 0 {
			next, cmd := m.submitApproval(opts[0])
			return next, cmd, true
		}
	}
	return m, nil, false
}

func (m model) approvalOptions() []string {
	if m.tr.ApprovalIdx >= 0 && m.tr.ApprovalIdx < len(m.tr.Items) {
		it := m.tr.Items[m.tr.ApprovalIdx]
		if len(it.Options) > 0 {
			return it.Options
		}
	}
	return []string{"Yes", "No"}
}

func (m model) approvalItem() *events.Item {
	if m.tr.ApprovalIdx >= 0 && m.tr.ApprovalIdx < len(m.tr.Items) {
		return &m.tr.Items[m.tr.ApprovalIdx]
	}
	return nil
}

func pickYes(opts []string) string {
	for _, o := range opts {
		if strings.EqualFold(o, "yes") || strings.EqualFold(o, "y") || strings.EqualFold(o, "approve") {
			return o
		}
	}
	if len(opts) > 0 {
		return opts[0]
	}
	return "Yes"
}

func pickNo(opts []string) string {
	for _, o := range opts {
		if strings.EqualFold(o, "no") || strings.EqualFold(o, "n") || strings.EqualFold(o, "deny") || strings.EqualFold(o, "reject") {
			return o
		}
	}
	if len(opts) > 1 {
		return opts[1]
	}
	return "No"
}

func (m model) submitApproval(choice string) (tea.Model, tea.Cmd) {
	m.tr.ClearApproval()
	m.ta.Reset()
	if m.ready {
		m.layout()
	}
	m.tr.Apply(events.NewSystem("Approval: " + choice))
	m.tr.Streaming = true
	m.tr.Status = "Thinking…"
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	// Approval responses must not inherit plan-mode instructions; they are part
	// of the already-running tool approval protocol.
	ch, err := m.backend.RunTurn(turnCtx, choice, backend.TurnOptions{})
	if err != nil {
		cancel()
		m.turnCancel = nil
		m.tr.Apply(events.NewError(err.Error()))
		m.refreshViewport()
		return m, nil
	}
	m.eventCh = ch
	return m, waitEvent(ch)
}

func (m model) renderApprovalOverlay() string {
	th := m.theme
	it := m.approvalItem()
	tool := "tool"
	content := "Approve tool execution?"
	opts := m.approvalOptions()
	if it != nil {
		if it.ToolName != "" {
			tool = it.ToolName
		}
		if it.Content != "" {
			content = it.Content
		}
	}

	var b strings.Builder
	b.WriteString(th.Approval.Render("⚠ Approval required") + "\n\n")
	b.WriteString(th.Text.Render(content) + "\n")
	b.WriteString(th.Muted.Render("Tool: ") + th.Brand.Render(tool) + "\n")
	if it != nil && len(it.Args) > 0 {
		n := 0
		for k, raw := range it.Args {
			if n >= 4 {
				b.WriteString(th.Muted.Render(fmt.Sprintf("  … +%d more", len(it.Args)-4)) + "\n")
				break
			}
			v := fmt.Sprintf("%v", raw)
			v = strings.ReplaceAll(v, "\n", " ")
			if len(v) > 60 {
				v = v[:59] + "…"
			}
			b.WriteString(th.Muted.Render(fmt.Sprintf("  %s: ", k)) + th.Text.Render(v) + "\n")
			n++
		}
	}
	b.WriteString("\n")
	var hints []string
	for i, o := range opts {
		hints = append(hints, fmt.Sprintf("%d=%s", i+1, o))
	}
	hints = append(hints, "y=yes", "n=no", "esc=deny")
	b.WriteString(th.Hint.Render(strings.Join(hints, "  ·  ")))

	w := m.width - 4
	if w < 30 {
		w = 30
	}
	return th.InputBorderFocus.
		BorderForeground(lipgloss.Color("221")).
		Width(w).
		Padding(1, 2).
		Render(b.String())
}
