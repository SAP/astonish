package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"strings"
	"testing"
)

type stateDetailBackend struct{ staticBackend }

func (stateDetailBackend) LoadDelegationDetail(context.Context, events.DelegationTaskState) ([]events.DelegationActivity, error) {
	return nil, nil
}

func TestDelegationDetailLoadingErrorRetryStaleRollback(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: stateDetailBackend{}, Width: 80, Height: 24})
	start := events.NewDelegationStart([]events.DelegationTask{{Name: "worker", TaskID: "t", BatchID: "b", ChildSessionID: "c"}})
	m.tr.LoadHistory([]events.HistoryMsg{{Delegation: &start}})
	next, cmd := m.openDelegationDetail(0, 0)
	m = next.(model)
	if cmd == nil || !m.delegationDetail.loading {
		t.Fatal("missing loading")
	}
	m.layoutDelegationDetail()
	if !strings.Contains(m.renderDelegationDetail(), "Loading") {
		t.Fatal("no loading UI")
	}
	old := m.delegationDetail.request
	next, _ = m.Update(delegationDetailLoadedMsg{request: old, err: fmt.Errorf("offline")})
	m = next.(model)
	if !strings.Contains(m.renderDelegationDetail(), "Retry") {
		t.Fatal("no retry UI")
	}
	next, cmd = m.handleDelegationDetailKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = next.(model)
	if cmd == nil || !m.delegationDetail.loading {
		t.Fatal("retry did not fetch")
	}
	next, _ = m.Update(delegationDetailLoadedMsg{request: old, activity: []events.DelegationActivity{{Type: "text", Text: "stale"}}})
	m = next.(model)
	if !m.delegationDetail.loading {
		t.Fatal("stale result accepted")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.delegationDetail.loading || len(m.tr.Items[0].DelegationTasks[0].Activity) != 0 {
		t.Fatal("empty result not settled")
	}
	if !strings.Contains(stripANSI(m.renderDelegationDetail()), "No activity") {
		t.Fatal("empty detail missing explanation")
	}
	next, _ = m.applyRolledBack(rolledBackMsg{entries: []backend.HistoryEntry{{Delegation: &start}}})
	m = next.(model)
	if m.delegationDetail.open || len(m.tr.Items[0].DelegationTasks) != 1 || m.tr.DelegationActive {
		t.Fatal("rollback lost attempts or retained active overlay")
	}
	next, _ = m.Update(delegationDetailLoadedMsg{request: old, activity: []events.DelegationActivity{{Type: "text", Text: "stale"}}})
	m = next.(model)
	if len(m.tr.Items[0].DelegationTasks[0].Activity) != 0 {
		t.Fatal("stale post-rollback result applied")
	}
}
