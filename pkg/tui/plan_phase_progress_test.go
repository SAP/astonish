package tui

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestPhaseProgressPlaceholderOmitted(t *testing.T) {
	for _, live := range []bool{false, true} {
		m := sidebarTestModel(120, 30)
		m.tr.Streaming = live
		for name, text := range map[string]string{"cards": m.renderPlanPhaseCards(24), "detail": m.renderPlanProgressBody(100, true)} {
			plain := stripANSI(text)
			if strings.Contains(plain, "Progress: unavailable") {
				t.Errorf("%s still shows per-phase placeholder", name)
			}
			if !strings.Contains(plain, "running") {
				t.Errorf("%s lost running status", name)
			}
		}
		if !strings.Contains(m.renderPlanProgressHeader(100), "1/12 phases complete") {
			t.Error("overall progress lost")
		}
		m.tr.Items = []events.Item{{Kind: events.ItemPlan, Content: "malformed plan"}}
		for name, text := range map[string]string{"cards": m.renderPlanPhaseCards(100), "detail": m.renderPlanProgressBody(100, true)} {
			if !strings.Contains(stripANSI(text), "The latest plan could not be read. Progress: unavailable") {
				t.Errorf("%s lost genuine plan-read error", name)
			}
		}
		m.cancel()
	}
}
