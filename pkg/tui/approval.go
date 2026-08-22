package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
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
	if it := m.approvalItem(); it != nil && it.ApprovalKind == "plan" {
		return m.handlePlanApprovalKey(msg)
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

	// Sub-agent authorization: if the approval overlay was triggered by a
	// sub-agent (delegate_tasks), send the response directly to the blocked
	// goroutine instead of calling RunTurn. The sub-agent resumes automatically.
	if m.backend.RespondSubAgentAuth(choice) {
		m.tr.ClearApproval()
		m.ta.Reset()
		if m.ready {
			m.layout()
		}
		m.tr.Apply(events.NewSystem("Approval: " + choice))
		m.tr.Streaming = true
		m.tr.Status = "Thinking…"
		m.refreshViewport()
		// The parent turn is still running (delegate_tasks hasn't returned yet).
		// Resume listening on the existing event channel so subsequent events
		// (tool calls, text, more approvals, turn-done) are processed.
		var cmd tea.Cmd
		if m.eventCh != nil {
			cmd = tea.Batch(waitEvent(m.eventCh), timerTick())
			m.timerResume()
		}
		return m, cmd
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
	m.timerResume()
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
		keys := make([]string, 0, len(it.Args))
		for k := range it.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// Card inner width: terminal width minus border (2×2) and padding (2×2).
		cardW := m.width - 8
		if cardW < 30 {
			cardW = 30
		}
		n := 0
		for _, k := range keys {
			if n >= 4 {
				b.WriteString(th.Muted.Render(fmt.Sprintf("  … +%d more", len(it.Args)-4)) + "\n")
				break
			}
			v := strings.TrimSpace(fmt.Sprintf("%v", it.Args[k]))
			if v == "" {
				continue
			}
			// Render the key prefix on the first line; indent continuation lines.
			prefix := fmt.Sprintf("  %s: ", k)
			bodyWidth := cardW - len(prefix)
			if bodyWidth < 10 {
				bodyWidth = cardW
				prefix = "  "
			}
			// Up to 8 wrapped lines per arg — enough for a realistic multi-line
			// shell script while keeping the overlay at a manageable height.
			wrapped := render.WrapMultiline(v, 8, bodyWidth)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i == 0 {
					b.WriteString(th.Muted.Render(prefix) + th.Text.Render(line) + "\n")
				} else {
					b.WriteString(th.Text.Render(strings.Repeat(" ", len(prefix))+line) + "\n")
				}
			}
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

const (
	planOptApprove          = "Approve & implement"
	planOptRequest          = "Request changes"
	planOptDecline          = "Decline"
	planChangesHint         = "Describe the changes to the plan…"
	planApprovalUserMessage = "I approve this plan. Please start implementing it now, phase by phase."
)

// handlePlanApprovalKey maps plan-approval accelerators without going through
// pickYes/pickNo (those would send n/esc to "Request changes"). Navigation
// keys still move ApprovalCursor; Enter submits the highlighted option.
func (m model) handlePlanApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
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
	case "enter":
		idx := m.tr.ApprovalCursor
		if idx < 0 || idx >= len(opts) {
			idx = 0
		}
		if len(opts) > 0 {
			next, cmd := m.submitApproval(opts[idx])
			return next, cmd, true
		}
		return m, nil, true
	case "y", "Y", "1":
		next, cmd := m.submitApproval(planOptApprove)
		return next, cmd, true
	case "r", "R", "2":
		next, cmd := m.submitApproval(planOptRequest)
		return next, cmd, true
	case "n", "N", "esc", "3":
		next, cmd := m.submitApproval(planOptDecline)
		return next, cmd, true
	}
	return m, nil, false
}

// renderPlanApprovalFooter paints a 3-row amber-bordered action bar that
// replaces the composer during plan approval. The highlighted option is
// bold with a ▸ caret; narrow terminals stack one option per line.
func (m model) renderPlanApprovalFooter() string {
	th := m.theme
	w := m.width
	if w < 12 {
		w = 12
	}
	innerW := w - 2
	contentW := innerW - 2
	if contentW < 8 {
		contentW = 8
	}
	opts := m.approvalOptions()
	cursor := m.tr.ApprovalCursor
	if cursor < 0 || cursor >= len(opts) {
		cursor = 0
	}

	accel := map[string]string{
		planOptApprove: "Enter",
		planOptRequest: "r",
		planOptDecline: "n",
	}
	buttons := make([]string, 0, len(opts))
	for i, o := range opts {
		key := accel[o]
		if key == "" {
			key = fmt.Sprintf("%d", i+1)
		}
		label := fmt.Sprintf(" %s  %s ", key, o)
		if i == cursor {
			buttons = append(buttons, th.PlanHeader.Bold(true).Render("▸"+label))
		} else {
			buttons = append(buttons, th.PlanMuted.Render("["+label+"]"))
		}
	}

	header := th.PlanHeader.Render("✦ Plan Ready · choose how to proceed")
	row := strings.Join(buttons, "  ")
	stacked := lipgloss.Width(stripANSI(row)) > contentW
	var rows []string
	rows = append(rows, header)
	if stacked {
		for _, btn := range buttons {
			rows = append(rows, btn)
		}
	} else {
		rows = append(rows, row)
	}

	border := th.PlanBorder
	var b strings.Builder
	b.WriteString(border.Render("╭" + strings.Repeat("─", innerW) + "╮"))
	for _, line := range rows {
		plainW := lipgloss.Width(stripANSI(line))
		if plainW > contentW {
			line = truncateToWidth(stripANSI(line), contentW)
			plainW = lipgloss.Width(line)
		}
		pad := contentW - plainW
		if pad < 0 {
			pad = 0
		}
		b.WriteByte('\n')
		b.WriteString(border.Render("│"))
		b.WriteString(th.Background.Render(" "))
		b.WriteString(line)
		b.WriteString(th.Background.Render(strings.Repeat(" ", pad+1)))
		b.WriteString(border.Render("│"))
	}
	b.WriteByte('\n')
	b.WriteString(border.Render("╰" + strings.Repeat("─", innerW) + "╯"))
	return b.String()
}

// renderPlanApprovalOverlay renders the plan approval prompt shown after
// announce_plan completes in Plan or Graph Plan mode. If the plan carries a
// Context narrative section, the first few lines are shown below the prompt so
// the user can see the plan's rationale without leaving the overlay.
func (m model) renderPlanApprovalOverlay(it *events.Item) string {
	th := m.theme
	var b strings.Builder
	b.WriteString(th.Header.Render("✦ Plan Ready") + "\n\n")
	b.WriteString(th.Text.Render("The plan has been announced. How would you like to proceed?") + "\n")
	if it.PlanContext != "" {
		// Show up to 3 lines of context (space-constrained overlay).
		lines := strings.SplitN(strings.TrimSpace(it.PlanContext), "\n", 4)
		if len(lines) > 3 {
			lines = lines[:3]
			lines[2] += " …"
		}
		b.WriteString("\n")
		b.WriteString(th.Muted.Render("Context:") + "\n")
		for _, l := range lines {
			b.WriteString(th.Muted.Render("  "+compactLine(l, 100)) + "\n")
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
		BorderForeground(lipgloss.Color("39")).
		Width(w).
		Padding(1, 2).
		Render(b.String())
}

// planApprovalStartedMsg reports completion of asynchronous plan-decision
// persistence and approved execution launch.
type planApprovalStartedMsg struct {
	ch     <-chan events.Event
	cancel context.CancelFunc
	err    error
}

// submitPlanApproval handles the user's choice in the plan approval dialog.
// "Approve & implement" switches to Normal mode and sends a turn to begin
// execution. "Request changes" lets the user type feedback. "Decline" aborts
// the plan and returns to Normal mode.
func (m model) submitPlanApproval(choice string) (tea.Model, tea.Cmd) {
	if choice == planOptApprove {
		if m.planApprovalPending || m.eventCh != nil {
			return m, nil
		}
		m.planApprovalPending = true
		m.planMode = false
		m.graphPlanMode = false
		m.tr.Apply(events.NewSystem("Plan approved. Preparing implementation…"))
		m.refreshViewport()

		ctx := m.ctx
		b := m.backend
		planPath := ""
		if pb, ok := b.(backend.PlanBackend); ok {
			planPath = pb.ActivePlanFilePath()
		}
		return m, func() tea.Msg {
			if lifecycle, ok := b.(backend.PlanLifecycleBackend); ok {
				if err := lifecycle.RecordPlanDecision(ctx, events.PlanApproved); err != nil {
					return planApprovalStartedMsg{err: fmt.Errorf("failed to save plan decision: %w", err)}
				}
			}
			turnCtx, cancel := context.WithCancel(ctx)
			ch, err := b.RunTurn(turnCtx, planApprovalUserMessage, backend.TurnOptions{
				SystemContext:         agent.BuildPlanExecutionSystemContext(planPath),
				ApprovedPlanExecution: true,
			})
			if err != nil {
				cancel()
				return planApprovalStartedMsg{err: err}
			}
			return planApprovalStartedMsg{ch: ch, cancel: cancel}
		}
	}

	status := events.PlanDeclined
	if choice == planOptRequest {
		status = events.PlanChangesRequested
	}
	if lifecycle, ok := m.backend.(backend.PlanLifecycleBackend); ok {
		if err := lifecycle.RecordPlanDecision(m.ctx, status); err != nil {
			m.tr.Apply(events.NewError("Failed to save plan decision: " + err.Error()))
			m.refreshViewport()
			return m, nil
		}
	}
	m.tr.ResolvePlan(status)
	m.ta.Reset()
	if m.ready {
		m.layout()
	}

	switch choice {
	case planOptRequest:
		m.tr.Apply(events.NewSystem("Describe the changes you'd like to the plan:"))
		m.ta.Placeholder = planChangesHint
		m.ta.Focus()
	case planOptDecline:
		m.planMode = false
		m.graphPlanMode = false
		m.tr.Apply(events.NewSystem("Plan declined. Returned to Normal mode."))
	}
	m.refreshViewport()
	return m, nil
}
