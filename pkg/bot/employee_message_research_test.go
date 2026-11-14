package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotEmployeeResearchLimitationNotExternalUncertainty(t *testing.T) {
	w, report, _ := agentEmployeeFixture(t, store.AgentWorkBlocked)
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	w.Phase = store.BotWorkBlocked
	gap := "I couldn't verify the timing of the reported market moves against primary sources, so I can't provide the requested source-backed explanation."
	w.Unresolved = []string{gap}
	report.Work = store.BuildBotWorkProjection(w)
	report.Summary = gap
	report.Unverified = []string{gap}
	report.EffectState = store.BotReportEffectsReadOnly
	if err := store.SetBotEmployeeReviewLimitation(&report, w, gap); err != nil {
		t.Fatal(err)
	}
	original := w.Candidate.Digest
	m := ComposeEmployeeMessage(context.Background(), store.RunPartial, report, nil, EmployeeMessageOptions{Work: w, Boundary: func(context.Context) error { t.Fatal("narrator boundary called"); return nil }})
	if m.Reason != store.BotEmployeeReasonResearchInadequate || m.Text != gap {
		t.Fatalf("lost concrete limitation: %+v", m)
	}
	for _, warning := range []string{"destination", "duplicate", "retrying", "tool observation", "technical"} {
		if strings.Contains(m.Text, warning) {
			t.Fatalf("research inferred effects: %s", m.Text)
		}
	}
	report.EmployeeMessage = &m
	if err := report.ValidateEmployeeMessage(store.RunPartial, w); err != nil {
		t.Fatal(err)
	}
	report.FinalizeEmployeeMessage(store.RunPartial, w)
	if report.EmployeeMessage.Text != gap || report.ValidateEmployeeMessage(store.RunPartial, w) != nil {
		t.Fatal("finalizer lost limitation")
	}
	if w.Candidate.Digest != original {
		t.Fatal("private draft changed")
	}
	other := w.Clone()
	other.Contract.BotID = uuid.New()
	if report.ValidateEmployeeMessage(store.RunPartial, other) == nil {
		t.Fatal("cross-Bot limitation accepted")
	}
}

func TestBotEmployeeResearchLimitationCannotHideUncertainEffects(t *testing.T) {
	for _, effects := range []store.BotReportEffectState{store.BotReportEffectsUnknown, store.BotReportEffectsUncertain} {
		t.Run(string(effects), func(t *testing.T) {
			w, report, _ := agentEmployeeFixture(t, store.AgentWorkBlocked)
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			w.Phase = store.BotWorkBlocked
			gap := "I couldn't verify the requested source coverage."
			w.Unresolved = []string{gap}
			report.Work = store.BuildBotWorkProjection(w)
			report.Summary, report.Unverified, report.EffectState = gap, []string{gap}, effects
			if err := store.SetBotEmployeeReviewLimitation(&report, w, gap); err != nil {
				t.Fatal(err)
			}
			m := ComposeEmployeeMessage(context.Background(), store.RunPartial, report, nil, EmployeeMessageOptions{Work: w})
			if m.Reason != store.BotEmployeeReasonAgentUnverifiedEffects || !strings.Contains(m.Text, "before retrying") {
				t.Fatalf("effects hidden: %+v", m)
			}
			report.EmployeeMessage = &m
			report.FinalizeEmployeeMessage(store.RunPartial, w)
			if !strings.Contains(report.EmployeeMessage.Text, "before retrying") {
				t.Fatal("finalizer hid effects")
			}
		})
	}
}
