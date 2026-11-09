package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/google/uuid"
	"strings"
	"time"
)

type botOperationBackend interface {
	DecideBotOperation(context.Context, store.BotOperationView, bool) error
}

func operationExceptions(s *store.BotSnapshot) []store.BotOperationView {
	out := []store.BotOperationView{}
	if s == nil {
		return out
	}
	for _, v := range s.Operations {
		if v.Validate() == nil && v.BotID == s.Bot.ID && v.HomeSessionID == s.Bot.HomeSessionID && v.Status == store.BotOperationBlocked && v.Decision == store.BotOperationRequireAuthorization && v.Exception != nil && v.Exception.OperationID == v.OperationID {
			out = append(out, v)
		}
	}
	return out
}
func (m model) openBotExceptions() (tea.Model, tea.Cmd) {
	cap, ok := m.backend.(backend.BotOverviewBackend)
	if !ok || !m.botSelected() {
		return m, nil
	}
	m.bots = botsState{open: true, loading: true, exceptions: true, requestID: uuid.New()}
	gen, req := m.botGeneration, m.bots.requestID
	return m, func() tea.Msg {
		snap, err := cap.BotSnapshot(m.ctx)
		return botsLoadedMsg{generation: gen, requestID: req, snapshot: snap, err: err}
	}
}
func (m model) handleBotExceptionKey(key string) (tea.Model, tea.Cmd) {
	items := operationExceptions(m.bots.snapshot)
	if len(items) == 0 || m.bots.err != "" {
		return m, nil
	}
	switch key {
	case "up", "k":
		if m.bots.cursor > 0 {
			m.bots.cursor--
		}
		m.bots.confirm = ""
	case "down", "j":
		if m.bots.cursor+1 < len(items) {
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
		if m.bots.cursor >= len(items) {
			return m, nil
		}
		v := items[m.bots.cursor]
		if !v.Exception.ExpiresAt.After(time.Now()) {
			m.bots.err = "Exception expired; close and reload."
			return m, nil
		}
		if current, ok := m.tr.BotOperations[v.OperationID.String()]; ok && (current.Revision != v.Revision || current.StateSequence != v.StateSequence) {
			m.bots.err = "Exception changed; close and reload."
			return m, nil
		}
		cap, ok := m.backend.(botOperationBackend)
		if !ok {
			m.bots.err = "Exception decisions unavailable."
			return m, nil
		}
		approve := m.bots.confirm == store.ApprovalApproved
		m.bots.loading = true
		return m, func() tea.Msg {
			return botDecisionMsg{generation: m.botGeneration, requestID: m.bots.requestID, err: cap.DecideBotOperation(m.ctx, v, approve)}
		}
	}
	return m, nil
}
func (m model) renderBotExceptions() string {
	var s strings.Builder
	s.WriteString("Authorization exceptions — bounded decisions\n\n")
	items := operationExceptions(m.bots.snapshot)
	switch {
	case m.bots.loading:
		s.WriteString("Loading…")
	case m.bots.err != "":
		s.WriteString("Error: " + m.bots.err)
	case len(items) == 0:
		s.WriteString("No pending authorization exceptions.")
	default:
		for i, v := range items {
			if i < max(0, m.bots.cursor-2) || i > m.bots.cursor+2 {
				continue
			}
			mark := "  "
			if i == m.bots.cursor {
				mark = "> "
			}
			fmt.Fprintf(&s, "%s%s\n%s\n", mark, v.Summary, v.Blocker)
			if i == m.bots.cursor {
				for _, field := range v.Exception.Fields {
					value := field.Value
					if field.Caption == "Change" {
						value = strings.ReplaceAll(value, "_", " ")
					}
					fmt.Fprintf(&s, "%s: %s\n", field.Caption, value)
				}
				fmt.Fprintf(&s, "Permission requested for this saved action only.\nExpires: %s\n", v.Exception.ExpiresAt.UTC().Format(time.RFC3339Nano))
			}
		}
		if m.bots.confirm == "" {
			s.WriteString("\n[Cancel] · y approve once · n deny · Enter cancels · Esc closes")
		} else {
			fmt.Fprintf(&s, "\nConfirm %s for this exact exception: Enter · Esc cancels", m.bots.confirm)
		}
	}
	return lipgloss.NewStyle().Width(max(30, m.width-8)).Border(lipgloss.RoundedBorder()).Padding(1, 2).Render(s.String())
}
