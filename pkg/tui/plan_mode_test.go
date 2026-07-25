package tui

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestTurnOptionsPlanMode(t *testing.T) {
	m := model{}
	if got := m.turnOptions(); got.SystemContext != "" {
		t.Fatalf("plan mode off SystemContext = %q, want empty", got.SystemContext)
	}

	m.planMode = true
	got := m.turnOptions()
	if got.SystemContext == "" {
		t.Fatal("plan mode on should send system context")
	}
}

func TestTogglePlanMode(t *testing.T) {
	m := model{tr: events.NewTranscript()}
	m.togglePlanMode()
	if !m.planMode {
		t.Fatal("expected plan mode enabled")
	}
	if len(m.tr.Items) == 0 || !strings.Contains(m.tr.Items[len(m.tr.Items)-1].Content, "Plan mode enabled") {
		t.Fatalf("expected enabled system notice, got %#v", m.tr.Items)
	}

	m.togglePlanMode()
	if m.planMode {
		t.Fatal("expected plan mode disabled")
	}
	if !strings.Contains(m.tr.Items[len(m.tr.Items)-1].Content, "Plan mode disabled") {
		t.Fatalf("expected disabled system notice, got %#v", m.tr.Items[len(m.tr.Items)-1])
	}
}
