package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
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

	out := m.renderPlanDocument(events.Item{Content: samplePlanContent}, 70)
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

	out := m.renderPlanDocument(events.Item{Content: samplePlanContent}, 70)
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

func TestRenderPlanDocumentStructuredCard(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Implement feature X

_Last updated: 2025-01-01T00:00:00Z_

## Context

Why this change is needed.

## Phases

### ⟳ Parallel group: wave-1

- [ ] **structured-plan-card** — Replace the markdown box
  - File (new): pkg/tui/plan.go
  - File (modify): pkg/tui/app.go
  - File (delete): pkg/tui/old_plan.go
  Verify: go test ./pkg/tui
  Keep the existing call site.
- [~] **approval-action-bar** — Fix y/n/esc mapping

## What Not To Change

Do not change the plan-mode gate.

## Verification

go test ./pkg/tui -count=1
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "[○] 1") {
		t.Fatalf("expected numbered pending phase:\n%s", plain)
	}
	if !strings.Contains(plain, "[●] 2") {
		t.Fatalf("expected numbered running phase:\n%s", plain)
	}
	if !strings.Contains(plain, "FILES") {
		t.Fatalf("expected FILES section label:\n%s", plain)
	}
	if !strings.Contains(plain, "+") || !strings.Contains(plain, "pkg/tui/plan.go") {
		t.Fatalf("expected + new file kind:\n%s", plain)
	}
	if !strings.Contains(plain, "~") || !strings.Contains(plain, "pkg/tui/app.go") {
		t.Fatalf("expected ~ modify file kind:\n%s", plain)
	}
	if !strings.Contains(plain, "−") || !strings.Contains(plain, "pkg/tui/old_plan.go") {
		t.Fatalf("expected − delete file kind:\n%s", plain)
	}
	if !strings.Contains(plain, "$ go test ./pkg/tui") {
		t.Fatalf("expected verify command:\n%s", plain)
	}
	if !strings.Contains(plain, "CONTEXT") {
		t.Fatalf("expected CONTEXT band:\n%s", plain)
	}
	if !strings.Contains(plain, "WHAT NOT TO CHANGE") {
		t.Fatalf("expected WHAT NOT TO CHANGE band:\n%s", plain)
	}
	if !strings.Contains(plain, "VERIFY") {
		t.Fatalf("expected VERIFY band:\n%s", plain)
	}
	if !strings.Contains(plain, "⟳ wave-1") || !strings.Contains(plain, "EXECUTION ORDER") {
		t.Fatalf("expected EXECUTION ORDER section with wave-1 group:\n%s", plain)
	}
	if strings.Contains(plain, "_Last updated") {
		t.Fatalf("structured card should hide _Last updated:\n%s", plain)
	}
	if strings.Contains(plain, "Legend:") {
		t.Fatalf("structured card should hide checkbox legend:\n%s", plain)
	}
	if !strings.Contains(plain, "ready") && !strings.Contains(plain, "running") {
		t.Fatalf("expected progress footer:\n%s", plain)
	}
}

func TestRenderPlanDocumentFallbackOnUnparseable(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	plain := stripANSI(m.renderPlanDocument(events.Item{Content: "# Execution Plan\n\nnot a real plan yet"}, 70))
	if !strings.Contains(plain, "┌") || !strings.Contains(plain, "└") || !strings.Contains(plain, "│") {
		t.Fatalf("unparseable plan should still produce a bordered box:\n%s", plain)
	}
	if !strings.Contains(plain, "✦") {
		t.Fatalf("unparseable plan should keep the plan icon:\n%s", plain)
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

func TestRenderPlanDocumentWithSummary(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Add caching layer

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **add-cache** — Implement Redis caching
  Summary: This phase adds a Redis-backed cache to reduce database load by 80%.
- [ ] **update-config** — Add cache configuration
  Summary: Exposes cache TTL and connection pool settings to operators.

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "reduce database load by 80%") {
		t.Fatalf("expected summary text for first phase in output:\n%s", plain)
	}
	if !strings.Contains(plain, "cache TTL and connection pool") {
		t.Fatalf("expected summary text for second phase in output:\n%s", plain)
	}
}

func TestRenderPlanDocumentPhaseSeparators(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Multi-phase refactor

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **phase-one** — First step
- [ ] **phase-two** — Second step
- [ ] **phase-three** — Third step
- [ ] **phase-four** — Fourth step

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "────") {
		t.Fatalf("expected thin rule separators (─) between phases when 4 phases present:\n%s", plain)
	}
}

func TestRenderPlanDocumentDetailsRenderedAsMarkdown(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Implement feature Y

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **impl-feature** — Build the feature
  Use the adapter pattern to decouple the interface from the implementation.

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "adapter pattern") {
		t.Fatalf("expected details text rendered in output:\n%s", plain)
	}
	if !strings.Contains(plain, "DETAILS") {
		t.Fatalf("expected DETAILS section label:\n%s", plain)
	}
}

func TestRenderPlanDocumentFileKindBreakdown(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Refactor storage layer

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **refactor-storage** — Restructure storage package
  - File (new): pkg/storage/cache.go
  - File (modify): pkg/storage/db.go
  - File (modify): pkg/storage/config.go
  - File (delete): pkg/storage/legacy.go

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "1 new") {
		t.Fatalf("expected '1 new' in file kind breakdown:\n%s", plain)
	}
	if !strings.Contains(plain, "2 modify") {
		t.Fatalf("expected '2 modify' in file kind breakdown:\n%s", plain)
	}
	if !strings.Contains(plain, "1 delete") {
		t.Fatalf("expected '1 delete' in file kind breakdown:\n%s", plain)
	}
}

func TestPlanDocumentRoundTrip_Summary(t *testing.T) {
	steps := []agent.PlanStepInfo{
		{
			Name:        "setup-infra",
			Description: "Provision cloud resources",
			Summary:     "Creates the VPC, subnets, and security groups needed for the service.",
		},
		{
			Name:        "deploy-service",
			Description: "Deploy the microservice",
			Summary:     "Builds and deploys the container image to the new infrastructure.",
			Files: []agent.PlanFileChange{
				{Path: "deploy/main.tf", Kind: "modify"},
			},
			Verify: "terraform plan",
		},
	}
	doc := agent.PlanDocumentInfo{
		Context:      "We need dedicated infrastructure for the new service.",
		Verification: "curl https://service.example.com/health",
	}

	rendered := agent.RenderPlanFromInfoWithDoc("Deploy new service", doc, steps)

	parsedDoc, parsedGoal, parsedSteps, err := agent.ParsePlanDocument(rendered)
	if err != nil {
		t.Fatalf("ParsePlanDocument failed: %v", err)
	}
	if parsedGoal != "Deploy new service" {
		t.Fatalf("goal mismatch: got %q", parsedGoal)
	}
	if parsedDoc.Context != doc.Context {
		t.Fatalf("context mismatch: got %q", parsedDoc.Context)
	}
	if parsedDoc.Verification != doc.Verification {
		t.Fatalf("verification mismatch: got %q", parsedDoc.Verification)
	}
	if len(parsedSteps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(parsedSteps))
	}
	if parsedSteps[0].Summary != steps[0].Summary {
		t.Fatalf("step 0 summary mismatch: got %q, want %q", parsedSteps[0].Summary, steps[0].Summary)
	}
	if parsedSteps[1].Summary != steps[1].Summary {
		t.Fatalf("step 1 summary mismatch: got %q, want %q", parsedSteps[1].Summary, steps[1].Summary)
	}
}

func TestRenderPlanDocumentSectionLabels(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Full section label test

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **all-sections** — Phase with all sections
  - File (new): pkg/foo/bar.go
  - File (modify): pkg/foo/baz.go
  Verify: go test ./pkg/foo/...
  Use dependency injection for the new adapter layer.

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "FILES") {
		t.Fatalf("expected FILES section label:\n%s", plain)
	}
	if !strings.Contains(plain, "VERIFY") {
		t.Fatalf("expected VERIFY section label:\n%s", plain)
	}
	if !strings.Contains(plain, "DETAILS") {
		t.Fatalf("expected DETAILS section label:\n%s", plain)
	}
}

func TestRenderPlanDocumentSummaryProminent(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()

	content := `# Execution Plan

**Goal:** Summary prominence test

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **with-summary** — Add the caching layer
  Summary: Introduces a Redis-backed cache to cut latency by 50%.
  Wire up the cache client in the service constructor.

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	plain := stripANSI(m.renderPlanDocument(events.Item{Content: content}, 80))

	if !strings.Contains(plain, "Add the caching layer") {
		t.Fatalf("expected description text in output:\n%s", plain)
	}
	if !strings.Contains(plain, "cut latency by 50%") {
		t.Fatalf("expected summary text in output:\n%s", plain)
	}
	if !strings.Contains(plain, "Wire up the cache client") {
		t.Fatalf("expected details text in output:\n%s", plain)
	}
}
