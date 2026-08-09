package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// handleApprovalKey handles cursor navigation, y/n and option keys while
// awaiting tool or network approval. Returns handled=true when the key was
// consumed.
//
// Navigation model: the overlay shows the options as a vertical list with a
// cursor (default on the first option, e.g. "Allow"). Up/down (or k/j) move the
// cursor; Enter submits the highlighted option. Number keys 1..9 and the y/n/esc
// shortcuts remain as accelerators.
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
	case "up", "k", "shift+tab", "ctrl+p":
		if len(opts) > 0 {
			m.tr.ApprovalCursor = (m.tr.ApprovalCursor - 1 + len(opts)) % len(opts)
			m.refreshViewport()
		}
		return m, nil, true
	case "down", "j", "tab", "ctrl+n":
		if len(opts) > 0 {
			m.tr.ApprovalCursor = (m.tr.ApprovalCursor + 1) % len(opts)
			m.refreshViewport()
		}
		return m, nil, true
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
		// Submit the highlighted option (cursor defaults to the first, e.g.
		// "Allow", so a bare Enter accepts the safe default).
		idx := m.tr.ApprovalCursor
		if idx < 0 || idx >= len(opts) {
			idx = 0
		}
		if len(opts) > 0 {
			next, cmd := m.submitApproval(opts[idx])
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
	// Plan approval has its own flow: approve switches to Normal mode,
	// request changes stays in plan mode, decline aborts.
	if it := m.approvalItem(); it != nil && it.ApprovalKind == "plan" {
		return m.submitPlanApproval(choice)
	}

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
	m.turnStartedAt = time.Now()
	return m, tea.Batch(waitEvent(ch), timerTick())
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
	host := denial.Host
	if broader && denial.BroaderPattern != "" {
		host = denial.BroaderPattern
	}
	label := endpointLabel(host, denial.Port)

	m.tr.ClearApproval()
	m.ta.Reset()
	if m.ready {
		m.layout()
	}
	m.tr.Apply(events.NewSystem("Approving network access for " + label + "…"))
	m.tr.Streaming = true
	m.tr.Status = "Approving network access…"
	m.refreshViewport()

	turnCtx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	cmd := approveNetworkGrantCmd(turnCtx, grantBackend, m.backend, sessionID, sandboxName, denial, broader, host, label)
	return m, cmd, true
}

func (m model) submitNetworkDeny() (tea.Model, tea.Cmd, bool) {
	it := m.approvalItem()
	denial, ok := firstNetworkDenial(it)
	if !ok {
		m.tr.ClearApproval()
		m.refreshViewport()
		return m, nil, true
	}
	grantBackend, _ := m.backend.(backend.NetworkGrantBackend)
	sessionID := first(it.SessionID, m.tr.SessionID, m.info.SessionID)
	sandboxName := it.SandboxName
	label := endpointLabel(denial.Host, denial.Port)

	m.tr.ClearApproval()
	m.tr.Apply(events.NewSystem("Network access denied for " + label + "."))
	m.tr.Streaming = false
	m.tr.Status = ""
	m.refreshViewport()

	if grantBackend == nil {
		return m, nil, true
	}
	return m, denyNetworkGrantCmd(m.ctx, grantBackend, sessionID, sandboxName, denial), true
}

type networkGrantApprovedMsg struct {
	label string
	host  string
	ch    <-chan events.Event
	err   error
}

type networkGrantDeniedMsg struct {
	err error
}

func approveNetworkGrantCmd(ctx context.Context, grantBackend backend.NetworkGrantBackend, chatBackend backend.Backend, sessionID, sandboxName string, denial events.NetworkDenial, broader bool, host, label string) tea.Cmd {
	return func() tea.Msg {
		if err := grantBackend.ApproveNetworkGrant(ctx, sessionID, denial, broader, sandboxName); err != nil {
			return networkGrantApprovedMsg{label: label, host: host, err: err}
		}
		msg := fmt.Sprintf("I just approved network access to %s. Please retry the previous command that was blocked by the proxy.", host)
		ch, err := chatBackend.RunTurn(ctx, msg, backend.TurnOptions{})
		return networkGrantApprovedMsg{label: label, host: host, ch: ch, err: err}
	}
}

func denyNetworkGrantCmd(ctx context.Context, grantBackend backend.NetworkGrantBackend, sessionID, sandboxName string, denial events.NetworkDenial) tea.Cmd {
	return func() tea.Msg {
		return networkGrantDeniedMsg{err: grantBackend.DenyNetworkGrant(ctx, sessionID, denial, sandboxName)}
	}
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
	if it != nil && it.ApprovalKind == "plan" {
		return m.renderPlanApprovalOverlay(it)
	}
	if it != nil && it.ApprovalKind == "folder" {
		return m.renderFolderApprovalOverlay(it)
	}
	tool := "tool"
	content := "Approve tool execution?"
	title := "⚠ Approval required"
	opts := m.approvalOptions()
	if it != nil {
		if it.ToolName != "" {
			tool = it.ToolName
		}
		if it.Content != "" {
			content = it.Content
		}
		if it.ApprovalKind == "tool" {
			title = "⚠ Tool authorization required"
		}
	}

	var b strings.Builder
	b.WriteString(th.Approval.Render(title) + "\n\n")
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
	b.WriteString(m.renderApprovalOptions(opts))

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

// renderApprovalOptions renders the authorization options as a vertical,
// cursor-navigable list. The highlighted option (m.tr.ApprovalCursor) shows a
// "❯" caret and is rendered in the accent style; the rest are muted. A compact
// key hint follows. Used by both the tool and folder authorization overlays so
// the interaction is identical.
func (m model) renderApprovalOptions(opts []string) string {
	th := m.theme
	cursor := m.tr.ApprovalCursor
	if cursor < 0 || cursor >= len(opts) {
		cursor = 0
	}
	var b strings.Builder
	for i, o := range opts {
		if i == cursor {
			b.WriteString(th.Approval.Render("❯ "+o) + "\n")
		} else {
			b.WriteString(th.Muted.Render("  "+o) + "\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(renderApprovalHints(th, []approvalHint{
		{Keys: "↑/↓", Label: "move"},
		{Keys: "enter", Label: "select"},
		{Keys: "esc", Label: "deny"},
	}))
	return b.String()
}

// renderFolderApprovalOverlay renders the code-mode folder-access prompt: which
// out-of-project paths a tool wants to touch, with once/session/deny options.
func (m model) renderFolderApprovalOverlay(it *events.Item) string {
	th := m.theme
	tool := first(it.ToolName, "tool")

	var b strings.Builder
	b.WriteString(th.Approval.Render("⚠ Folder access required") + "\n\n")
	b.WriteString(th.Text.Render("Allow ") + th.Brand.Render(tool) +
		th.Text.Render(" to access files outside the project directory?") + "\n")
	if len(it.Paths) > 0 {
		b.WriteString(th.Muted.Render("Path(s):") + "\n")
		for i, p := range it.Paths {
			if i >= 5 {
				b.WriteString(th.Muted.Render(fmt.Sprintf("  … +%d more", len(it.Paths)-5)) + "\n")
				break
			}
			b.WriteString(th.Muted.Render("  • ") + th.Text.Render(compactLine(p, 100)) + "\n")
		}
	}
	b.WriteString("\n")

	opts := m.approvalOptions()
	b.WriteString(m.renderApprovalOptions(opts))

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

	hints := []approvalHint{{Keys: "enter/y", Label: "allow host"}}
	if ok && denial.BroaderPattern != "" {
		hints = append(hints, approvalHint{Keys: "b", Label: "allow " + denial.BroaderPattern})
	}
	hints = append(hints, approvalHint{Keys: "n/esc", Label: "deny"})
	b.WriteString(renderApprovalHints(th, hints))

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

type approvalHint struct {
	Keys  string
	Label string
}

func renderApprovalHints(th Theme, hints []approvalHint) string {
	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		if hint.Label == "" {
			parts = append(parts, th.Approval.Render(hint.Keys))
			continue
		}
		parts = append(parts, th.Approval.Render(hint.Keys)+th.Hint.Render("="+hint.Label))
	}
	return strings.Join(parts, th.Hint.Render("  ·  "))
}

func compactLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes > 0 && len([]rune(s)) > maxRunes {
		return string([]rune(s)[:maxRunes-1]) + "…"
	}
	return s
}

// renderPlanApprovalOverlay renders the plan approval prompt shown after
// announce_plan completes in Plan or Graph Plan mode.
func (m model) renderPlanApprovalOverlay(it *events.Item) string {
	th := m.theme
	var b strings.Builder
	b.WriteString(th.Header.Render("✦ Plan Ready") + "\n\n")
	b.WriteString(th.Text.Render("The plan has been announced. How would you like to proceed?") + "\n\n")
	opts := m.approvalOptions()
	b.WriteString(m.renderApprovalOptions(opts))

	w := m.width - 4
	if w < 30 {
		w = 30
	}
	return th.InputBorderFocus.
		BorderForeground(lipgloss.Color("39")).
		Width(w).
		Padding(1, 2).
		Render(b.String())
}

// submitPlanApproval handles the user's choice in the plan approval dialog.
// "Approve & implement" switches to Normal mode and sends a turn to begin
// execution. "Request changes" lets the user type feedback. "Decline" aborts
// the plan and returns to Normal mode.
func (m model) submitPlanApproval(choice string) (tea.Model, tea.Cmd) {
	m.tr.ClearApproval()
	m.ta.Reset()
	if m.ready {
		m.layout()
	}

	switch choice {
	case "Approve & implement":
		// Switch to Normal mode so the execution turn has full tool access.
		m.planMode = false
		m.graphPlanMode = false
		m.tr.Apply(events.NewSystem("Plan approved. Starting implementation…"))
		m.tr.Streaming = true
		m.tr.Status = "Thinking…"
		m.refreshViewport()

		planPath := ""
		if pb, ok := m.backend.(backend.PlanBackend); ok {
			planPath = pb.ActivePlanFilePath()
		}

		turnCtx, cancel := context.WithCancel(m.ctx)
		m.turnCancel = cancel
		// Send as a normal-mode turn (no plan gate) to begin execution.
		ch, err := m.backend.RunTurn(turnCtx, "I approve this plan. Please start implementing it now, phase by phase.", backend.TurnOptions{SystemContext: agent.BuildPlanExecutionSystemContext(planPath)})
		if err != nil {
			cancel()
			m.turnCancel = nil
			m.tr.Apply(events.NewError(err.Error()))
			m.refreshViewport()
			return m, nil
		}
		m.eventCh = ch
		m.turnStartedAt = time.Now()
		return m, tea.Batch(waitEvent(ch), timerTick())

	case "Request changes":
		// Stay in plan mode so the user can describe what to change.
		m.tr.Apply(events.NewSystem("Describe the changes you'd like to the plan:"))
		m.refreshViewport()
		return m, nil

	case "Decline":
		m.planMode = false
		m.graphPlanMode = false
		m.tr.Apply(events.NewSystem("Plan declined. Returned to Normal mode."))
		m.refreshViewport()
		return m, nil
	}

	m.refreshViewport()
	return m, nil
}
