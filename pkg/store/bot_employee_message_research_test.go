package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func researchLimitationFixture(t *testing.T) (*BotWorkState, BotReport) {
	t.Helper()
	w := NewBotAgentWorkState(BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Compare the requested primary sources."})
	w.EmployeeReviewPolicy = BotEmployeeReviewEvidenceBoundV1
	w.Phase, w.AgentWork.Disposition = BotWorkBlocked, AgentWorkBlocked
	w.Candidate = &BotWorkCandidate{Revision: 1, EventID: "draft", Text: "Unsupported draft."}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	gap := "I couldn't inspect the requested primary sources, so the comparison remains incomplete."
	w.Unresolved = []string{gap}
	r := BotReport{Work: BuildBotWorkProjection(w), Summary: gap, Unverified: []string{gap}, EffectState: BotReportEffectsReadOnly}
	if err := SetBotEmployeeReviewLimitation(&r, w, gap); err != nil {
		t.Fatal(err)
	}
	return w, r
}

func TestEmployeeResearchLimitationBinding(t *testing.T) {
	for _, mutation := range []string{"none", "bot", "run", "revision", "candidate", "text", "effects", "unknown-effects"} {
		t.Run(mutation, func(t *testing.T) {
			w, r := researchLimitationFixture(t)
			m := NewBotEmployeeFallback(RunPartial, r, BotEmployeeReasonResearchInadequate)
			r.EmployeeMessage = &m
			if r.ValidateEmployeeMessage(RunPartial, w) != nil {
				t.Fatal("valid limitation rejected")
			}
			switch mutation {
			case "bot":
				r.ReviewLimitation.BotID = uuid.New()
			case "run":
				r.ReviewLimitation.RunID = uuid.New()
			case "revision":
				r.ReviewLimitation.WorkRevision++
			case "candidate":
				r.ReviewLimitation.CandidateDigest = strings.Repeat("a", 64)
			case "text":
				r.ReviewLimitation.Text = "Invented limitation."
			case "effects":
				r.EffectState = BotReportEffectsUncertain
			case "unknown-effects":
				r.EffectState = BotReportEffectsUnknown
			}
			if mutation != "none" && r.ValidateEmployeeMessage(RunPartial, w) == nil {
				t.Fatal("mutated report accepted")
			}
		})
	}
}

func TestEmployeeResearchLimitationRejectsDraftAndUnscopedReview(t *testing.T) {
	for _, mutation := range []string{"draft", "scope", "policy", "phase", "diagnostics-url"} {
		t.Run(mutation, func(t *testing.T) {
			w, r := researchLimitationFixture(t)
			text := w.Unresolved[0]
			switch mutation {
			case "draft":
				text = w.Candidate.Text
				w.Unresolved = []string{text}
			case "scope":
				r.Work.BotID = uuid.New()
			case "policy":
				w.EmployeeReviewPolicy = ""
			case "phase":
				w.Phase = BotWorkWorking
			case "diagnostics-url":
				text = "provider secret https://private.invalid"
				w.Unresolved = []string{text}
			}
			if SetBotEmployeeReviewLimitation(&r, w, text) == nil {
				t.Fatal("unsafe limitation minted")
			}
		})
	}
}

func TestEmployeeEffectFactsTakePrecedenceWithoutUnverifiedProse(t *testing.T) {
	w, r := researchLimitationFixture(t)
	r.EffectState = BotReportEffectsUncertain
	r.Unverified = nil
	m := NewBotEmployeeFallback(RunPartial, r, BotEmployeeReasonResearchInadequate)
	if m.Reason != BotEmployeeReasonAgentUnverifiedEffects || !strings.Contains(m.Text, "duplicate actions") {
		t.Fatal(m.Text)
	}
	r.EmployeeMessage = nil
	r.FinalizeEmployeeMessage(RunPartial, w)
	if r.EmployeeMessage.Reason != BotEmployeeReasonAgentUnverifiedEffects {
		t.Fatal("finalizer ignored typed uncertainty")
	}
}
