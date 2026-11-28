package tui

import (
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

type planVerificationState struct {
	owner    *events.Transcript
	progress agent.PlanVerificationProgress
	received time.Time
}

func (m *model) applyPlanVerification(ev events.Event) {
	p, ok := ev.Meta["progress"].(agent.PlanVerificationProgress)
	if !ok || m.info.Mode != "code" {
		return
	}
	v := latestPlanProgress(m.tr.Items)
	if p.PlanID != v.PlanID || p.Revision != v.Revision {
		return
	}
	m.verification = planVerificationState{owner: m.tr, progress: p, received: time.Now()}
}

func (m model) verificationLabel() string {
	s := m.verification
	v := latestPlanProgress(m.tr.Items)
	if s.owner != m.tr || s.progress.PlanID != v.PlanID || s.progress.Revision != v.Revision || s.progress.Stage != agent.PlanVerificationRunning {
		return ""
	}
	elapsed := s.progress.Elapsed + time.Since(s.received)
	return "Verifying phase: " + s.progress.Step + " · " + formatDuration(elapsed)
}

func (m model) withVerification(v planProgressView) planProgressView {
	if m.verificationLabel() == "" {
		return v
	}
	for i := range v.Phases {
		p := &v.Phases[i]
		if p.Name != m.verification.progress.Step {
			continue
		}
		switch p.State {
		case planPhaseFailed:
			v.Counts.Failed--
		case planPhasePending:
			v.Counts.Pending--
		}
		if p.State != planPhaseRunning {
			v.Counts.Running++
		}
		p.State, p.Current, p.Live = planPhaseRunning, true, true
	}
	v.State = planProgressActive
	return v
}
