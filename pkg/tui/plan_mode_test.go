package tui

import (
	"context"
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

func TestTogglePlanModeDoesNotWriteTranscriptMessages(t *testing.T) {
	m := model{tr: events.NewTranscript()}
	m.togglePlanMode()
	if !m.planMode {
		t.Fatal("expected plan mode enabled")
	}
	if len(m.tr.Items) != 0 {
		t.Fatalf("mode toggle should not write transcript messages, got %#v", m.tr.Items)
	}

	m.togglePlanMode()
	if m.planMode {
		t.Fatal("expected plan mode disabled")
	}
	if len(m.tr.Items) != 0 {
		t.Fatalf("mode toggle should stay silent, got %#v", m.tr.Items)
	}
}

func TestRenderComposerShowsModeInBottomBorder(t *testing.T) {
	m := newTestComposerModel(80)
	out := stripANSI(m.renderComposer())
	if !strings.Contains(out, " Normal ") {
		t.Fatalf("normal composer should show Normal mode label:\n%s", out)
	}
	if strings.Contains(out, " Plan ") {
		t.Fatalf("normal composer should not show Plan mode label:\n%s", out)
	}

	m.planMode = true
	out = stripANSI(m.renderComposer())
	if !strings.Contains(out, " Plan ") {
		t.Fatalf("plan composer should show Plan mode label:\n%s", out)
	}
}

func newTestComposerModel(width int) model {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: width, Height: 24})
	m.ready = true
	m.layout()
	return m
}
