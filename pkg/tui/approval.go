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

// handleApprovalKey handles y/n and option keys while awaiting tool or network approval.
// Returns handled=true when the key was consumed.
func (m model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !m.tr.Awaiting {
		return m, nil, false
	}
	if it := m.approvalItem(); it != nil && it.Kind == events.ItemNetworkDenial {
		return m.handleNetworkDenialKey(msg, it)
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

func (m model) handleNetworkDenialKey(msg tea.KeyMsg, it *events.Item) (tea.Model, tea.Cmd, bool) {
	key := msg.String()
	switch key {
	case "y", "Y", "enter", "1":
		return m.submitNetworkGrant(false)
	case "b", "B", "2":
		if denial, ok := firstNetworkDenial(it); ok && denial.BroaderPattern != "" {
			return m.submitNetworkGrant(true)
		}
	case "n", "N", "esc", "3":
		return m.submitNetworkDeny()
	}
	return m, nil, false
}

func (m model) submitNetworkGrant(broader bool) (tea.Model, tea.Cmd, bool) {
	it := m.approvalItem()
	denial, ok := firstNetworkDenial(it)
	if !ok {
		m.tr.ClearApproval()
		m.tr.Apply(events.NewError("No network denial details were available to approve."))
		m.refreshViewport()
		return m, nil, true
	}
	grantBackend, ok := m.backend.(backend.NetworkGrantBackend)
	if !ok {
		m.tr.ClearApproval()
		m.tr.Apply(events.NewError("This terminal backend cannot approve network grants."))
		m.refreshViewport()
		return m, nil, true
	}

	sessionID := first(it.SessionID, m.tr.SessionID, m.info.SessionID)
	sandboxName := it.SandboxName
	if err := grantBackend.ApproveNetworkGrant(m.ctx, sessionID, denial, broader, sandboxName); err != nil {
		m.tr.Apply(events.NewError("Network approval failed: " + err.Error()))
		m.refreshViewport()
		return m, nil, true
	}

	host := denial.Host
	if broader && denial.BroaderPattern != "" {
		host = denial.BroaderPattern
	}
	m.tr.ClearApproval()
	m.ta.Reset()
	if m.ready {
		m.layout()
	}
	m.tr.Apply(events.NewSystem("Network access granted for " + endpointLabel(host, denial.Port) + ". Retrying blocked command…"))
	m.tr.Streaming = true
	m.tr.Status = "Thinking…"
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	msg := fmt.Sprintf("I just approved network access to %s. Please retry the previous command that was blocked by the proxy.", host)
	ch, err := m.backend.RunTurn(turnCtx, msg, backend.TurnOptions{})
	if err != nil {
		cancel()
		m.turnCancel = nil
		m.tr.Apply(events.NewError(err.Error()))
		m.refreshViewport()
		return m, nil, true
	}
	m.eventCh = ch
	return m, waitEvent(ch), true
}

func (m model) submitNetworkDeny() (tea.Model, tea.Cmd, bool) {
	it := m.approvalItem()
	denial, ok := firstNetworkDenial(it)
	if !ok {
		m.tr.ClearApproval()
		m.refreshViewport()
		return m, nil, true
	}
	if grantBackend, ok := m.backend.(backend.NetworkGrantBackend); ok {
		sessionID := first(it.SessionID, m.tr.SessionID, m.info.SessionID)
		if err := grantBackend.DenyNetworkGrant(m.ctx, sessionID, denial, it.SandboxName); err != nil {
			m.tr.Apply(events.NewError("Network denial failed: " + err.Error()))
			m.refreshViewport()
			return m, nil, true
		}
	}
	m.tr.ClearApproval()
	m.tr.Apply(events.NewSystem("Network access denied for " + endpointLabel(denial.Host, denial.Port) + "."))
	m.tr.Streaming = false
	m.tr.Status = ""
	m.refreshViewport()
	return m, nil, true
}

func firstNetworkDenial(it *events.Item) (events.NetworkDenial, bool) {
	if it == nil || len(it.NetworkDenials) == 0 {
		return events.NetworkDenial{}, false
	}
	return it.NetworkDenials[0], true
}

func endpointLabel(host string, port uint32) string {
	if host == "" {
		return "network endpoint"
	}
	if port == 0 {
		return host
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func (m model) renderApprovalOverlay() string {
	th := m.theme
	it := m.approvalItem()
	if it != nil && it.Kind == events.ItemNetworkDenial {
		return m.renderNetworkDenialOverlay(it)
	}
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

func (m model) renderNetworkDenialOverlay(it *events.Item) string {
	th := m.theme
	denial, ok := firstNetworkDenial(it)

	var b strings.Builder
	b.WriteString(th.Approval.Render("⚠ Network access blocked") + "\n\n")
	if !ok {
		b.WriteString(th.Text.Render("The sandbox reported blocked network access, but did not include endpoint details.") + "\n")
		b.WriteString(th.Muted.Render("Open Studio or retry the request after checking pending network grants.") + "\n")
	} else {
		b.WriteString(th.Text.Render("Allow this sandbox to connect to ") + th.Brand.Render(endpointLabel(denial.Host, denial.Port)) + th.Text.Render("?") + "\n")
		if denial.Binary != "" {
			b.WriteString(th.Muted.Render("Binary: ") + th.Text.Render(denial.Binary) + "\n")
		}
		if it.SandboxName != "" {
			b.WriteString(th.Muted.Render("Sandbox: ") + th.Text.Render(it.SandboxName) + "\n")
		}
		if denial.Rationale != "" {
			b.WriteString(th.Muted.Render("Reason: ") + th.Text.Render(compactLine(denial.Rationale, 110)) + "\n")
		}
		if denial.SecurityNotes != "" {
			b.WriteString(th.Approval.Render("Security: ") + th.Text.Render(compactLine(denial.SecurityNotes, 110)) + "\n")
		}
		if len(it.NetworkDenials) > 1 {
			b.WriteString(th.Muted.Render(fmt.Sprintf("Showing first blocked endpoint; %d more will remain pending.", len(it.NetworkDenials)-1)) + "\n")
		}
	}
	b.WriteString("\n")

	hints := []string{"enter/y=allow host"}
	if ok && denial.BroaderPattern != "" {
		hints = append(hints, "b=allow "+denial.BroaderPattern)
	}
	hints = append(hints, "n/esc=deny")
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

func compactLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes > 0 && len([]rune(s)) > maxRunes {
		return string([]rune(s)[:maxRunes-1]) + "…"
	}
	return s
}
