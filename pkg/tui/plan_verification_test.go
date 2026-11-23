package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestPlanVerificationRetryProjection(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: &staticBackend{}, Width: 120, Height: 30})
	defer m.cancel()
	m.info.Mode = "code"
	s := agent.PlanSnapshot{PlanID: "plan", Revision: 1, Sequence: 1, Steps: []agent.PlanStepInfo{{Name: "verify", Status: "failed", Evidence: "timeout"}}}
	s.Document.Lifecycle = agent.PlanLifecycleApproved
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	p := agent.PlanVerificationProgress{PlanID: "plan", Revision: 1, Step: "verify", Stage: agent.PlanVerificationRunning, Elapsed: 2 * time.Second}
	m.applyEvent(events.NewPlanVerification(p))
	if v := m.planProgress(); v.Counts.Failed != 0 || v.Counts.Running != 1 {
		t.Fatalf("retry still failed: %+v", v)
	}
	if !strings.Contains(m.renderLiveStatus(), "Verifying phase: verify") {
		t.Fatal("verification invisible")
	}
	m.verification.received = time.Now().Add(-3 * time.Second)
	if !strings.Contains(m.verificationLabel(), "5s") {
		t.Fatal("verification timer frozen")
	}
	p.Stage = agent.PlanVerificationFinished
	m.applyEvent(events.NewPlanVerification(p))
	if m.verificationLabel() != "" || m.planProgress().Counts.Failed != 1 {
		t.Fatal("failure evidence concealed")
	}
	s.Sequence++
	s.Steps[0].Status = "complete"
	s.Steps[0].Evidence = "exit 0"
	m.applyEvent(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
	if m.planProgress().Counts.Failed != 0 {
		t.Fatal("passing retry not reflected")
	}
}
