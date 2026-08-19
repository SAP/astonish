package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestRenderPlanCardOwnsPendingApprovalControls(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 90, Height: 30})
	m.ready = true
	m.layout()
	item := events.Item{
		Kind:         events.ItemPlan,
		Content:      samplePlanContent,
		ApprovalKind: "plan",
		PlanStatus:   events.PlanPending,
		Options:      []string{planOptApprove, planOptRequest, planOptDecline},
	}

	plain := stripANSI(m.renderPlanDocument(item, 80))
	for _, want := range []string{"Plan Ready", planOptApprove, planOptRequest, planOptDecline} {
		if !strings.Contains(plain, want) {
			t.Fatalf("atomic plan card missing %q:\n%s", want, plain)
		}
	}
}

func TestRenderPlanCardReplacesControlsWithSettledStatus(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 90, Height: 30})
	m.ready = true
	m.layout()
	item := events.Item{Kind: events.ItemPlan, Content: samplePlanContent, ApprovalKind: "plan", PlanStatus: events.PlanApproved}

	plain := stripANSI(m.renderPlanDocument(item, 80))
	if !strings.Contains(plain, "Approved") {
		t.Fatalf("settled plan missing Approved status:\n%s", plain)
	}
	if strings.Contains(plain, planOptRequest) || strings.Contains(plain, planOptDecline) {
		t.Fatalf("settled plan still shows approval controls:\n%s", plain)
	}
}
