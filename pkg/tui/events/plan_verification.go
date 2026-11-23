package events

import "github.com/SAP/astonish/pkg/agent"

const KindPlanVerification Kind = "plan_verification"

func NewPlanVerification(p agent.PlanVerificationProgress) Event {
	return Event{Kind: KindPlanVerification, Meta: map[string]any{"progress": p}}
}
