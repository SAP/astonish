package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

const samplePlanContent = `# Execution Plan

**Goal:** Implement feature X

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [x] **step-one** — Implement the core logic
- [~] **step-two** — Write unit tests
- [ ] **step-three** — Update documentation
- [!] **step-four** — Deploy to production

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`

func TestPlanContentDetectedAsItemPlan(t *testing.T) {
	tr := events.NewTranscript()
	tr.LinearThread = true
	tr.Apply(events.NewText(samplePlanContent))
	// In linear thread mode, the item should already be ItemPlan.
	if len(tr.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(tr.Items))
	}
	if tr.Items[0].Kind != events.ItemPlan {
		t.Fatalf("expected ItemPlan, got %q", tr.Items[0].Kind)
	}
}

func TestPlanContentDetectedInLinearThreadStreaming(t *testing.T) {
	tr := events.NewTranscript()
	tr.LinearThread = true
	// Simulate streaming: plan arrives in chunks.
	tr.Apply(events.NewText("# Execution Plan\n"))
	tr.Apply(events.NewText("\n**Goal:** Test streaming\n"))
	tr.Apply(events.NewText("\n## Phases\n\n- [ ] **step-one** — Something\n"))

	if len(tr.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(tr.Items))
	}
	if tr.Items[0].Kind != events.ItemPlan {
		t.Fatalf("expected ItemPlan after streaming, got %q", tr.Items[0].Kind)
	}
}

func TestPlanContentPromotedOnFinalize(t *testing.T) {
	// Non-linear (Studio) mode: plan is provisional until Done.
	tr := events.NewTranscript()
	tr.LinearThread = false
	tr.Apply(events.NewText(samplePlanContent))
	// While streaming, it's still ItemAgent (provisional).
	if len(tr.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(tr.Items))
	}
	if tr.Items[0].Kind != events.ItemAgent {
		t.Fatalf("expected ItemAgent while provisional, got %q", tr.Items[0].Kind)
	}
	// Finalize (KindDone).
	tr.Apply(events.NewDone())
	if tr.Items[0].Kind != events.ItemPlan {
		t.Fatalf("expected ItemPlan after finalize, got %q", tr.Items[0].Kind)
	}
}

func TestNonPlanContentStaysItemAgent(t *testing.T) {
	tr := events.NewTranscript()
	tr.LinearThread = true
	tr.Apply(events.NewText("Here is a regular response about plans.\n"))
	if len(tr.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(tr.Items))
	}
	if tr.Items[0].Kind != events.ItemAgent {
		t.Fatalf("expected ItemAgent for non-plan content, got %q", tr.Items[0].Kind)
	}
}

func TestRenderPlanDocumentHasBorders(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	out := m.renderPlanDocument(samplePlanContent, 70)
	plain := stripANSI(out)

	if !strings.Contains(plain, "┌") || !strings.Contains(plain, "┐") {
		t.Fatalf("expected top border corners in output:\n%s", plain)
	}
	if !strings.Contains(plain, "└") || !strings.Contains(plain, "┘") {
		t.Fatalf("expected bottom border corners in output:\n%s", plain)
	}
	if !strings.Contains(plain, "│") {
		t.Fatalf("expected side borders in output:\n%s", plain)
	}
	if !strings.Contains(plain, "✦") {
		t.Fatalf("expected plan icon ✦ in header:\n%s", plain)
	}
	if !strings.Contains(plain, "Implement feature X") {
		t.Fatalf("expected goal text in header:\n%s", plain)
	}
}

func TestRenderPlanDocumentStatusIcons(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	out := m.renderPlanDocument(samplePlanContent, 70)
	plain := stripANSI(out)

	if !strings.Contains(plain, "[✓]") {
		t.Fatalf("expected [✓] for complete step in output:\n%s", plain)
	}
	if !strings.Contains(plain, "[●]") {
		t.Fatalf("expected [●] for running step in output:\n%s", plain)
	}
	if !strings.Contains(plain, "[○]") {
		t.Fatalf("expected [○] for pending step in output:\n%s", plain)
	}
	if !strings.Contains(plain, "[✗]") {
		t.Fatalf("expected [✗] for failed step in output:\n%s", plain)
	}
}

func TestRenderTranscriptPlanItem(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()
	m.theme = plainTheme()

	// Push a plan item directly.
	m.tr.Items = append(m.tr.Items, events.Item{
		Kind:    events.ItemPlan,
		Content: samplePlanContent,
	})

	out, hits, _ := m.renderTranscript()
	if out == "" {
		t.Fatal("expected non-empty transcript render")
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit region for plan item")
	}
	if hits[0].kind != events.ItemPlan {
		t.Fatalf("expected hit region kind ItemPlan, got %q", hits[0].kind)
	}
}

func TestHistoryLoadPlanItem(t *testing.T) {
	tr := events.NewTranscript()
	tr.LinearThread = true
	tr.LoadHistory([]events.HistoryMsg{
		{Kind: "user", Text: "create a plan"},
		{Kind: "agent", Text: samplePlanContent},
	})

	// Find the plan item.
	found := false
	for _, it := range tr.Items {
		if it.Kind == events.ItemPlan {
			found = true
			break
		}
	}
	if !found {
		kinds := make([]string, len(tr.Items))
		for i, it := range tr.Items {
			kinds[i] = string(it.Kind)
		}
		t.Fatalf("expected an ItemPlan in loaded history, got kinds: %v", kinds)
	}
}

func TestPlanDocumentContentSpan(t *testing.T) {
	// Border row (no copyable content).
	topBorder := "┌─ ✦ Plan ────────────────────┐"
	span := planDocumentContentSpan(0, topBorder)
	if span != [2]int{0, 0} {
		t.Fatalf("border row should have no content span, got %v", span)
	}

	// Body row with content.
	bodyRow := "│  Step one: do something       │"
	span = planDocumentContentSpan(1, bodyRow)
	if span[0] >= span[1] {
		t.Fatalf("body row should have non-empty content span, got %v", span)
	}
	// Extracted content should not include the border chars or padding.
	runes := []rune(bodyRow)
	extracted := string(runes[span[0]:span[1]])
	if strings.Contains(extracted, "│") {
		t.Fatalf("extracted content should not contain border chars, got %q", extracted)
	}
	if !strings.Contains(extracted, "Step one") {
		t.Fatalf("extracted content should contain the body text, got %q", extracted)
	}
}
