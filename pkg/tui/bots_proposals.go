package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"strings"
	"time"
)

func (m model) openBotDecisions() (tea.Model, tea.Cmd) {
	if len(operationExceptions(m.botSnapshot)) > 0 {
		return m.openBotExceptions()
	}
	// Both views share the same authoritative pending snapshot; explicit shortcuts remain.
	if m.botSnapshot != nil && len(m.botSnapshot.PendingProposals) > 0 {
		return m.openBotProposals()
	}
	return m.botCommand("/bots approvals")
}
func (m model) openBotProposals() (tea.Model, tea.Cmd) {
	cap, ok := m.backend.(backend.BotOverviewBackend)
	if !ok || !m.botSelected() {
		m.tr.Apply(events.NewError("Select a remote Bot with proposal support first."))
		m.refreshViewport()
		return m, nil
	}
	m.bots = botsState{open: true, loading: true, proposals: true, requestID: uuid.New()}
	gen, req := m.botGeneration, m.bots.requestID
	return m, func() tea.Msg {
		snap, err := cap.BotSnapshot(m.ctx)
		result := botsLoadedMsg{generation: gen, requestID: req, snapshot: snap, err: err}
		if err == nil && snap != nil {
			result.proposals = snap.PendingProposals
		}
		return result
	}
}
func (m model) handleBotProposalKey(key string) (tea.Model, tea.Cmd) {
	n := len(m.bots.proposalItems)
	if n == 0 {
		return m, nil
	}
	switch key {
	case "up", "k":
		if m.bots.cursor > 0 {
			m.bots.cursor--
		}
		m.bots.confirm = ""
	case "down", "j":
		if m.bots.cursor+1 < n {
			m.bots.cursor++
		}
		m.bots.confirm = ""
	case "y":
		m.bots.confirm = store.ApprovalApproved
	case "n":
		m.bots.confirm = store.ApprovalDenied
	case "enter":
		if m.bots.confirm == "" {
			m.bots = botsState{}
			return m, nil
		}
		p := m.bots.proposalItems[m.bots.cursor]
		if p.Status != store.BotControlProposed || !p.ExpiresAt.After(time.Now()) || m.bots.err != "" {
			m.bots.confirm = ""
			m.bots.err = "Proposal changed or expired; close and reload to review."
			return m, nil
		}
		cap, ok := m.backend.(backend.BotProposalBackend)
		if !ok {
			m.bots.err = "Proposal decisions unavailable."
			return m, nil
		}
		confirm := m.bots.confirm == store.ApprovalApproved
		m.bots.loading = true
		return m, func() tea.Msg {
			return botDecisionMsg{generation: m.botGeneration, requestID: m.bots.requestID, err: cap.DecideBotProposal(m.ctx, p, confirm)}
		}
	}
	return m, nil
}
func (m model) renderBotProposals() string {
	var s strings.Builder
	s.WriteString("Bot proposals — review saved changes\n\n")
	if m.bots.snapshot != nil {
		fmt.Fprintf(&s, "Pending decisions: %d\n", botPendingCount(m.bots.snapshot))
	}
	switch {
	case m.bots.loading:
		s.WriteString("Loading…")
	case m.bots.err != "":
		s.WriteString("Error: " + m.bots.err)
	case len(m.bots.proposalItems) == 0:
		s.WriteString("No pending proposals.")
	default:
		for i := max(0, m.bots.cursor-2); i < len(m.bots.proposalItems) && i < m.bots.cursor+3; i++ {
			p := m.bots.proposalItems[i]
			mark := "  "
			if i == m.bots.cursor {
				mark = "> "
			}
			fmt.Fprintf(&s, "%s%s [%s] %s\n", mark, p.ID, p.Status, p.Payload.Operation)
		}
		p := m.bots.proposalItems[m.bots.cursor]
		raw, _ := json.MarshalIndent(p.Payload, "", "  ")
		fmt.Fprintf(&s, "\nVersion: %d · revision: %d\nDigest: %s\nExpires: %s\n%s\n", p.Version, p.ExpectedRevision, p.Digest, p.ExpiresAt.Format(time.RFC3339), raw)
	}
	if m.bots.confirm == "" {
		s.WriteString("\n[Cancel] · y confirm change · n reject · Enter cancels · Esc closes")
	} else {
		fmt.Fprintf(&s, "\nConfirm %s for this exact proposal: Enter · Esc cancels", m.bots.confirm)
	}
	s.WriteString("\n/bots approvals reviews tool actions and questions")
	return lipgloss.NewStyle().Width(max(30, m.width-8)).Border(lipgloss.RoundedBorder()).Padding(1, 2).Render(s.String())
}
func botPendingCount(s *store.BotSnapshot) int {
	return len(s.PendingApprovals) + len(s.PendingProposals) + len(operationExceptions(s))
}
func (m model) approveBotContinuation(a store.BotApproval, r *store.BotInteractionResponse) botDecisionMsg {
	msg := botDecisionMsg{generation: m.botGeneration, requestID: m.bots.requestID}
	cap, ok := m.backend.(backend.BotContinuationBackend)
	if !ok {
		msg.err = fmt.Errorf("atomic approval continuation unavailable")
		return msg
	}
	result, err := cap.ApproveBotAndContinue(m.ctx, a, r)
	msg.err = err
	if result != nil {
		msg.continuation = result.Continuation
	}
	return msg
}
