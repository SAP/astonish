package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

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
	if strings.Contains(plain, "CONTEXT") {
		t.Fatalf("CONTEXT band label should be dropped (overview renders label-free):\n%s", plain)
	}
	if !strings.Contains(plain, "Why this change is needed") {
		t.Fatalf("expected context overview content rendered without a label:\n%s", plain)
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

func TestRenderPlanDocumentCodeBlockFrameAlignment(t *testing.T) {
	// Regression: code blocks with long lines inside a plan card's DETAILS or
	// CONTEXT section must not push the right │ border out of alignment.
	// Every rendered line between the top and bottom border must have exactly
	// the same visible width (i.e. │ … content … │ is consistent).
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 40})
	m.ready = true
	m.layout()

	// A plan with a fenced code block in the details field containing a
	// deliberately long line (150 chars) that must be soft-wrapped inside the card.
	content := "# Execution Plan\n\n**Goal:** Code block alignment test\n\n_Last updated: 2025-01-01T00:00:00Z_\n\n## Context\n\nThis plan contains a code block.\n\n## Phases\n\n- [ ] **phase-one** — Render code blocks\n  Details: Implement the feature.\n  ```go\n  " + strings.Repeat("x", 150) + "\n  ```\n\nLegend: `[ ]` pending\n"

	out := m.renderPlanDocument(events.Item{Content: content}, 90)
	plain := stripANSI(out)

	lines := strings.Split(plain, "\n")
	// Find lines that are part of the frame (contain │ on the right side).
	// All such content lines should have the same total width.
	var frameWidth int
	for _, line := range lines {
		if strings.HasPrefix(line, "┌") || strings.HasPrefix(line, "└") {
			frameWidth = lipgloss.Width(line)
			break
		}
	}
	if frameWidth == 0 {
		t.Fatal("could not detect frame width from border line")
	}
	for i, line := range lines {
		if !strings.Contains(line, "│") {
			continue
		}
		// Skip separator lines (all dashes).
		stripped := strings.TrimRight(line, " ")
		if strings.HasPrefix(stripped, "┌") || strings.HasPrefix(stripped, "└") {
			continue
		}
		w := lipgloss.Width(line)
		if w != frameWidth {
			t.Errorf("line %d has width %d, want %d: %q", i, w, frameWidth, line)
		}
	}
}

// TestPlanCard_TitleUsesPlanTitleStyle verifies the plan card's title line is
// rendered with the distinct PlanTitle accent (orange 208 in code mode), not
// the band-label PlanHeader style. In NO_COLOR mode the title text is present.
func TestPlanCard_TitleUsesPlanTitleStyle(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()
	// Ensure the code-mode (orange) theme is active for the color assertion.
	m.theme = DefaultTheme()

	content := `# Execution Plan

**Goal:** Distinct title styling

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **phase-one** — First step

Legend: ` + "`[ ]`" + ` pending
`
	out := m.renderPlanDocument(events.Item{Content: content}, 80)

	// The title text must appear (icon + goal).
	plain := stripANSI(out)
	if !strings.Contains(plain, "Distinct title styling") {
		t.Fatalf("expected plan title text in output:\n%s", plain)
	}

	// The title line must carry the PlanTitle accent (orange 208). Find the
	// rendered line containing the title and assert it uses the 208 foreground.
	var titleLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(stripANSI(l), "Distinct title styling") {
			titleLine = l
			break
		}
	}
	if titleLine == "" {
		t.Fatalf("could not find rendered title line in output:\n%s", out)
	}
	if !strings.Contains(titleLine, "38;5;208") {
		t.Fatalf("expected PlanTitle accent (208) on the title line, got:\n%q", titleLine)
	}

	// NO_COLOR: title text still present, no ANSI escapes.
	m.theme = plainTheme()
	plainOut := m.renderPlanDocument(events.Item{Content: content}, 80)
	if !strings.Contains(plainOut, "Distinct title styling") {
		t.Fatalf("expected title text in NO_COLOR output:\n%s", plainOut)
	}
}

// TestPlanCard_RichOverview_EndToEnd renders a plan whose Context carries a
// structured overview (Problem heading, a fenced flow diagram, a files table,
// and a Boundaries heading) and asserts the two tracks combine: a colored
// PlanTitle, the table rendered inside the CONTEXT band, the fenced block, and
// at least two visually distinct heading styles.
func TestPlanCard_RichOverview_EndToEnd(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 80, Height: 24})
	m.ready = true
	m.layout()
	m.theme = DefaultTheme()

	content := "# Execution Plan\n\n" +
		"**Goal:** Rich overview end to end\n\n" +
		"_Last updated: 2025-01-01T00:00:00Z_\n\n" +
		"## Context\n\n" +
		"## Problem\n\nThe overview is too flat.\n\n" +
		"```text\nrequest --> transport --> refresh --> persist\n```\n\n" +
		"| Component | Change |\n| --- | --- |\n| renderer | tiered headings |\n| theme | plan title |\n\n" +
		"## Boundaries\n\nDo not change the parser grammar.\n\n" +
		"## Phases\n\n" +
		"- [ ] **phase-one** — First step\n\n" +
		"Legend: `[ ]` pending\n"

	out := m.renderPlanDocument(events.Item{Content: content}, 80)
	plain := stripANSI(out)

	// (a) colored plan title.
	var titleLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(stripANSI(l), "Rich overview end to end") {
			titleLine = l
			break
		}
	}
	if titleLine == "" || !strings.Contains(titleLine, "38;5;208") {
		t.Fatalf("expected colored PlanTitle (208) on the title line, got:\n%q", titleLine)
	}

	// (b) NO "CONTEXT" band label — the overview flows straight from the title.
	if strings.Contains(plain, "CONTEXT") {
		t.Fatalf("did not expect a CONTEXT band label; overview should be label-free:\n%s", plain)
	}

	// (c) the overview content renders: the table rows and the fenced flow block.
	if !strings.Contains(plain, "tiered headings") || !strings.Contains(plain, "plan title") {
		t.Fatalf("expected the markdown table rows rendered in the overview:\n%s", plain)
	}
	if !strings.Contains(plain, "request --> transport --> refresh --> persist") {
		t.Fatalf("expected the fenced flow diagram rendered in the overview:\n%s", plain)
	}

	// (d) section headings (Problem / Boundaries) render in the code-mode accent
	// (orange 208), matching the reference's colored section titles.
	if !strings.Contains(plain, "Problem") || !strings.Contains(plain, "Boundaries") {
		t.Fatalf("expected Problem and Boundaries section headings in the overview:\n%s", plain)
	}
	var problemLine string
	for _, l := range strings.Split(out, "\n") {
		s := stripANSI(l)
		if strings.Contains(s, "Problem") && !strings.Contains(s, "too flat") {
			problemLine = l
			break
		}
	}
	if problemLine == "" || !strings.Contains(problemLine, "38;5;208") {
		t.Fatalf("expected the 'Problem' section heading to render in the accent color (208), got:\n%q", problemLine)
	}

	// (e) vertical pace: a full-width heading bar under Problem, then a blank
	// line, then the body — so the section reads as a room, not a packed wall.
	var problemIdx = -1
	plainLines := strings.Split(plain, "\n")
	for i, l := range plainLines {
		if strings.Contains(l, "Problem") && !strings.Contains(l, "too flat") {
			problemIdx = i
			break
		}
	}
	if problemIdx < 0 || problemIdx+2 >= len(plainLines) {
		t.Fatalf("expected Problem heading plus a rule and a blank line after it:\n%s", plain)
	}
	if !strings.Contains(plainLines[problemIdx+1], "─") {
		t.Fatalf("expected a heading bar under Problem, got %q", plainLines[problemIdx+1])
	}
	blankInterior := strings.Trim(strings.ReplaceAll(plainLines[problemIdx+2], "│", ""), " ─")
	if blankInterior != "" {
		t.Fatalf("expected a blank line after the Problem heading bar, got %q", plainLines[problemIdx+2])
	}
	if !strings.Contains(plainLines[problemIdx+3], "too flat") {
		t.Fatalf("expected the Problem body after the blank line, got %q", plainLines[problemIdx+3])
	}
}



