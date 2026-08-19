package agent

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPlanMarkdown_StatusMarkers(t *testing.T) {
	// Deterministic timestamp.
	orig := planClock
	planClock = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }
	defer func() { planClock = orig }()

	steps := []planStep{
		{name: "explore", description: "look around", status: "complete"},
		{name: "build", description: "make changes", status: "running"},
		{name: "verify", description: "run tests", status: "pending"},
		{name: "deploy", description: "ship it", status: "failed"},
	}
	md := RenderPlanMarkdown("Do the thing", steps)

	wants := []string{
		"**Goal:** Do the thing",
		"_Last updated: 2024-01-02T03:04:05Z_",
		"- [x] **explore** — look around",
		"- [~] **build** — make changes",
		"- [ ] **verify** — run tests",
		"- [!] **deploy** — ship it",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("rendered plan missing %q\n---\n%s", w, md)
		}
	}
}

func TestRenderPlanMarkdown_EmptySteps(t *testing.T) {
	md := RenderPlanMarkdown("goal", nil)
	if !strings.Contains(md, "_(no phases)_") {
		t.Errorf("expected no-phases marker, got:\n%s", md)
	}
}

func TestParsePlanMarkdown_RoundTrip(t *testing.T) {
	steps := []planStep{
		{name: "explore", description: "look around", status: "complete"},
		{name: "build", description: "make changes", status: "running"},
		{name: "verify", description: "", status: "pending"},
	}
	md := RenderPlanMarkdown("My Goal", steps)

	goal, parsed, err := ParsePlanMarkdown(md)
	if err != nil {
		t.Fatalf("ParsePlanMarkdown error: %v", err)
	}
	if goal != "My Goal" {
		t.Errorf("goal = %q, want %q", goal, "My Goal")
	}
	if len(parsed) != len(steps) {
		t.Fatalf("parsed %d steps, want %d", len(parsed), len(steps))
	}
	for i := range steps {
		if parsed[i].name != steps[i].name {
			t.Errorf("step %d name = %q, want %q", i, parsed[i].name, steps[i].name)
		}
		if parsed[i].status != steps[i].status {
			t.Errorf("step %d status = %q, want %q", i, parsed[i].status, steps[i].status)
		}
		if parsed[i].description != steps[i].description {
			t.Errorf("step %d description = %q, want %q", i, parsed[i].description, steps[i].description)
		}
	}
}

func TestParsePlanMarkdown_NoPhases(t *testing.T) {
	_, _, err := ParsePlanMarkdown("# Execution Plan\n\nsome prose\n")
	if err == nil {
		t.Fatal("expected error for document with no phases")
	}
}

func TestRenderPlanMarkdown_Details(t *testing.T) {
	steps := []planStep{
		{name: "explore", description: "look around", details: "file_tree repo\nread go.mod", status: "running"},
	}
	md := RenderPlanMarkdown("goal", steps)
	if !strings.Contains(md, "  file_tree repo") || !strings.Contains(md, "  read go.mod") {
		t.Errorf("expected indented detail lines, got:\n%s", md)
	}
}

func TestParsePlanMarkdown_DetailsRoundTrip(t *testing.T) {
	steps := []planStep{
		{name: "explore", description: "look around", details: "file_tree repo\nread go.mod", status: "complete"},
		{name: "build", description: "make changes", details: "edit foo.go:42", status: "running"},
		{name: "verify", description: "run tests", details: "", status: "pending"},
	}
	md := RenderPlanMarkdown("My Goal", steps)

	goal, parsed, err := ParsePlanMarkdown(md)
	if err != nil {
		t.Fatalf("ParsePlanMarkdown error: %v", err)
	}
	if goal != "My Goal" {
		t.Errorf("goal = %q", goal)
	}
	if len(parsed) != 3 {
		t.Fatalf("parsed %d steps, want 3", len(parsed))
	}
	if parsed[0].details != "file_tree repo\nread go.mod" {
		t.Errorf("step 0 details = %q", parsed[0].details)
	}
	if parsed[1].details != "edit foo.go:42" {
		t.Errorf("step 1 details = %q", parsed[1].details)
	}
	if parsed[2].details != "" {
		t.Errorf("step 2 details = %q, want empty", parsed[2].details)
	}
	// Status still round-trips with details present.
	if parsed[0].status != "complete" || parsed[1].status != "running" || parsed[2].status != "pending" {
		t.Errorf("status round-trip broken: %q %q %q", parsed[0].status, parsed[1].status, parsed[2].status)
	}
}

func TestRenderPlanMarkdown_FilesAndVerify(t *testing.T) {
	steps := []planStep{
		{
			name:        "types",
			description: "add plan fields",
			files: []PlanFileChange{
				{Path: "pkg/agent/sub_agent.go", Kind: "modify"},
				{Path: "pkg/agent/plan_new.go", Kind: "new"},
				{Path: "pkg/agent/old.go", Kind: "delete"},
			},
			verify: "go test ./pkg/agent/...",
			status: "running",
		},
	}
	md := RenderPlanMarkdown("goal", steps)
	wants := []string{
		"  - File (modify): pkg/agent/sub_agent.go",
		"  - File (new): pkg/agent/plan_new.go",
		"  - File (delete): pkg/agent/old.go",
		"  Verify: go test ./pkg/agent/...",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("rendered plan missing %q\n---\n%s", w, md)
		}
	}
}

func TestParsePlanMarkdown_FilesAndVerifyRoundTrip(t *testing.T) {
	steps := []planStep{
		{
			name:        "types",
			description: "add plan fields",
			files: []PlanFileChange{
				{Path: "pkg/agent/sub_agent.go", Kind: "modify"},
				{Path: "pkg/agent/plan_new.go", Kind: "new"},
			},
			verify:  "go test ./pkg/agent/...",
			details: "extend the struct\nwire the tool",
			status:  "complete",
		},
		{name: "plain", description: "no files", status: "pending"},
	}
	md := RenderPlanMarkdown("My Goal", steps)

	_, parsed, err := ParsePlanMarkdown(md)
	if err != nil {
		t.Fatalf("ParsePlanMarkdown error: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d steps, want 2", len(parsed))
	}

	// Files round-trip with kind + path preserved and ordered.
	if len(parsed[0].files) != 2 {
		t.Fatalf("step 0 files = %d, want 2 (%+v)", len(parsed[0].files), parsed[0].files)
	}
	if parsed[0].files[0].Path != "pkg/agent/sub_agent.go" || parsed[0].files[0].Kind != "modify" {
		t.Errorf("step 0 file[0] = %+v", parsed[0].files[0])
	}
	if parsed[0].files[1].Path != "pkg/agent/plan_new.go" || parsed[0].files[1].Kind != "new" {
		t.Errorf("step 0 file[1] = %+v", parsed[0].files[1])
	}
	// Verify round-trips.
	if parsed[0].verify != "go test ./pkg/agent/..." {
		t.Errorf("step 0 verify = %q", parsed[0].verify)
	}
	// Details still round-trip alongside files/verify.
	if parsed[0].details != "extend the struct\nwire the tool" {
		t.Errorf("step 0 details = %q", parsed[0].details)
	}
	// A phase with no files/verify stays empty (no spurious entries).
	if len(parsed[1].files) != 0 || parsed[1].verify != "" {
		t.Errorf("step 1 should have no files/verify, got files=%+v verify=%q", parsed[1].files, parsed[1].verify)
	}
}

func TestRenderPlanMarkdown_DocumentSections(t *testing.T) {
	orig := planClock
	planClock = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }
	defer func() { planClock = orig }()

	doc := PlanDocumentInfo{
		Context:      "We need to improve the plan format.",
		WhatNotToDo:  "Do not touch update_plan or PlanState runtime logic.",
		Verification: "make test-unit\ngo test ./...",
	}
	steps := []planStep{
		{name: "data", description: "extend types", status: "pending"},
	}
	md := renderPlanMarkdownWithDoc("My Plan", doc, steps)

	wants := []string{
		"## Context\n\nWe need to improve the plan format.",
		"## What Not To Change\n\nDo not touch update_plan or PlanState runtime logic.",
		"## Verification\n\nmake test-unit",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("rendered plan missing %q\n---\n%s", w, md)
		}
	}
	// Context must appear before ## Phases.
	ctxIdx := strings.Index(md, "## Context")
	phasesIdx := strings.Index(md, "## Phases")
	if ctxIdx < 0 || phasesIdx < 0 || ctxIdx > phasesIdx {
		t.Errorf("Context section must appear before Phases; ctxIdx=%d phasesIdx=%d", ctxIdx, phasesIdx)
	}
	// WhatNotToDo and Verification must appear after all phases.
	wntIdx := strings.Index(md, "## What Not To Change")
	vIdx := strings.Index(md, "## Verification")
	if wntIdx < 0 || vIdx < 0 || wntIdx < phasesIdx || vIdx < wntIdx {
		t.Errorf("post-phases sections in wrong order: phases=%d wnt=%d ver=%d", phasesIdx, wntIdx, vIdx)
	}
}

func TestParsePlanMarkdown_DocumentSectionsRoundTrip(t *testing.T) {
	orig := planClock
	planClock = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }
	defer func() { planClock = orig }()

	doc := PlanDocumentInfo{
		Context:      "Context text here.\nLine two.",
		WhatNotToDo:  "Leave X alone.",
		Verification: "go test ./...",
	}
	steps := []planStep{
		{name: "step-a", description: "do a", status: "pending"},
		{name: "step-b", description: "do b", status: "complete"},
	}
	md := renderPlanMarkdownWithDoc("Goal", doc, steps)

	parsedDoc, goal, parsed, err := parsePlanMarkdownFull(md)
	if err != nil {
		t.Fatalf("parsePlanMarkdownFull error: %v", err)
	}
	if goal != "Goal" {
		t.Errorf("goal = %q", goal)
	}
	if parsedDoc.Context != doc.Context {
		t.Errorf("Context = %q, want %q", parsedDoc.Context, doc.Context)
	}
	if parsedDoc.WhatNotToDo != doc.WhatNotToDo {
		t.Errorf("WhatNotToDo = %q, want %q", parsedDoc.WhatNotToDo, doc.WhatNotToDo)
	}
	if parsedDoc.Verification != doc.Verification {
		t.Errorf("Verification = %q, want %q", parsedDoc.Verification, doc.Verification)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d steps, want 2", len(parsed))
	}
	if parsed[0].name != "step-a" || parsed[1].name != "step-b" {
		t.Errorf("step names = %q, %q", parsed[0].name, parsed[1].name)
	}
	if parsed[1].status != "complete" {
		t.Errorf("step-b status = %q, want complete", parsed[1].status)
	}
}

func TestRenderPlanMarkdown_ParallelGroups(t *testing.T) {
	steps := []planStep{
		{name: "data-layer", description: "extend types", parallelGroup: "backend", status: "pending"},
		{name: "system-prompt", description: "update guidance", parallelGroup: "backend", status: "pending"},
		{name: "web-panel", description: "update UI", parallelGroup: "frontend", status: "pending"},
		{name: "cleanup", description: "final cleanup", parallelGroup: "", status: "pending"},
	}
	md := RenderPlanMarkdown("Goal", steps)

	wants := []string{
		"### ⟳ Parallel group: backend",
		"### ⟳ Parallel group: frontend",
		"### (serial)",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("rendered plan missing %q\n---\n%s", w, md)
		}
	}
	// backend group header must precede data-layer step.
	backendIdx := strings.Index(md, "### ⟳ Parallel group: backend")
	dataIdx := strings.Index(md, "**data-layer**")
	if backendIdx < 0 || dataIdx < 0 || backendIdx > dataIdx {
		t.Errorf("backend group header must appear before data-layer step")
	}
	// serial header must precede cleanup step.
	serialIdx := strings.Index(md, "### (serial)")
	cleanupIdx := strings.Index(md, "**cleanup**")
	if serialIdx < 0 || cleanupIdx < 0 || serialIdx > cleanupIdx {
		t.Errorf("(serial) header must appear before cleanup step")
	}
}

func TestParsePlanMarkdown_ParallelGroupsRoundTrip(t *testing.T) {
	orig := planClock
	planClock = func() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }
	defer func() { planClock = orig }()

	steps := []planStep{
		{name: "data-layer", description: "extend types", parallelGroup: "backend", status: "pending"},
		{name: "system-prompt", description: "update prompt", parallelGroup: "backend", status: "complete"},
		{name: "web-panel", description: "update UI", parallelGroup: "frontend", status: "running"},
		{name: "cleanup", description: "cleanup", parallelGroup: "", status: "pending"},
	}
	md := RenderPlanMarkdown("Goal", steps)

	_, goal, parsed, err := parsePlanMarkdownFull(md)
	if err != nil {
		t.Fatalf("parsePlanMarkdownFull error: %v", err)
	}
	if goal != "Goal" {
		t.Errorf("goal = %q", goal)
	}
	if len(parsed) != 4 {
		t.Fatalf("parsed %d steps, want 4", len(parsed))
	}
	for i, want := range steps {
		if parsed[i].parallelGroup != want.parallelGroup {
			t.Errorf("step %d (%q) parallelGroup = %q, want %q", i, want.name, parsed[i].parallelGroup, want.parallelGroup)
		}
		if parsed[i].status != want.status {
			t.Errorf("step %d status = %q, want %q", i, parsed[i].status, want.status)
		}
	}
}

func TestParsePlanMarkdown_Summary(t *testing.T) {
	md := `# Execution Plan

**Goal:** Test Summary Field

_Last updated: 2025-01-01T00:00:00Z_

## Phases

- [ ] **step-one** — Implement the core logic
  Summary: Adds the new data model so the frontend can display results
  - File (modify): pkg/api/handler.go
  Verify: go test ./pkg/api/...
  Concrete implementation details here
- [ ] **step-two** — Write unit tests
  Summary: Ensures the new handler doesn't break existing behavior

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	doc, goal, steps, err := ParsePlanDocument(md)
	if err != nil {
		t.Fatalf("ParsePlanDocument error: %v", err)
	}
	_ = doc
	if goal != "Test Summary Field" {
		t.Errorf("goal = %q, want %q", goal, "Test Summary Field")
	}
	if len(steps) != 2 {
		t.Fatalf("parsed %d steps, want 2", len(steps))
	}
	if steps[0].Summary != "Adds the new data model so the frontend can display results" {
		t.Errorf("step 0 Summary = %q, want %q", steps[0].Summary, "Adds the new data model so the frontend can display results")
	}
	if steps[1].Summary != "Ensures the new handler doesn't break existing behavior" {
		t.Errorf("step 1 Summary = %q, want %q", steps[1].Summary, "Ensures the new handler doesn't break existing behavior")
	}
	// Also verify that other fields parsed correctly alongside summary.
	if len(steps[0].Files) != 1 || steps[0].Files[0].Path != "pkg/api/handler.go" {
		t.Errorf("step 0 files = %+v, want 1 file", steps[0].Files)
	}
	if steps[0].Verify != "go test ./pkg/api/..." {
		t.Errorf("step 0 Verify = %q", steps[0].Verify)
	}
	if steps[0].Details != "Concrete implementation details here" {
		t.Errorf("step 0 Details = %q", steps[0].Details)
	}
}

func TestRenderPlanFromInfoWithDoc_Summary(t *testing.T) {
	orig := planClock
	planClock = func() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }
	defer func() { planClock = orig }()

	steps := []PlanStepInfo{
		{
			Name:        "step-one",
			Description: "Implement the core logic",
			Summary:     "Adds the new data model so the frontend can display results",
			Files:       []PlanFileChange{{Path: "pkg/api/handler.go", Kind: "modify"}},
			Verify:      "go test ./pkg/api/...",
		},
		{
			Name:        "step-two",
			Description: "Write unit tests",
			Summary:     "Ensures the new handler doesn't break existing behavior",
		},
		{
			Name:        "step-three",
			Description: "No summary here",
		},
	}
	doc := PlanDocumentInfo{}
	md := RenderPlanFromInfoWithDoc("Test Summary Render", doc, steps)

	wants := []string{
		"  Summary: Adds the new data model so the frontend can display results",
		"  Summary: Ensures the new handler doesn't break existing behavior",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("rendered plan missing %q\n---\n%s", w, md)
		}
	}
	// Step three has no summary, so "step-three" should NOT be followed by a Summary line.
	stepThreeIdx := strings.Index(md, "**step-three**")
	if stepThreeIdx < 0 {
		t.Fatal("step-three not found in rendered markdown")
	}
	afterStepThree := md[stepThreeIdx:]
	// Find the next newline after the step-three header line.
	nlIdx := strings.Index(afterStepThree, "\n")
	if nlIdx >= 0 {
		nextLine := ""
		rest := afterStepThree[nlIdx+1:]
		if nlIdx2 := strings.Index(rest, "\n"); nlIdx2 >= 0 {
			nextLine = rest[:nlIdx2]
		} else {
			nextLine = rest
		}
		if strings.Contains(nextLine, "Summary:") {
			t.Errorf("step-three should not have a Summary line, but got: %q", nextLine)
		}
	}
}

func TestPlanDocument_RoundTrip_Summary(t *testing.T) {
	orig := planClock
	planClock = func() time.Time { return time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC) }
	defer func() { planClock = orig }()

	steps := []PlanStepInfo{
		{
			Name:        "alpha",
			Description: "First phase",
			Summary:     "Lays the groundwork for the feature",
			Files:       []PlanFileChange{{Path: "pkg/core/model.go", Kind: "new"}},
			Verify:      "go build ./pkg/core/...",
			Details:     "Create the model struct",
		},
		{
			Name:        "beta",
			Description: "Second phase",
			Summary:     "Wires up the API endpoints",
		},
		{
			Name:        "gamma",
			Description: "Third phase with no summary",
		},
	}
	doc := PlanDocumentInfo{
		Context:      "Adding a new feature.",
		WhatNotToDo:  "Don't change the database schema.",
		Verification: "go test ./...",
	}

	// Render
	md := RenderPlanFromInfoWithDoc("Round Trip Goal", doc, steps)

	// Parse back
	parsedDoc, goal, parsedSteps, err := ParsePlanDocument(md)
	if err != nil {
		t.Fatalf("ParsePlanDocument error: %v", err)
	}
	if goal != "Round Trip Goal" {
		t.Errorf("goal = %q, want %q", goal, "Round Trip Goal")
	}
	if parsedDoc.Context != doc.Context {
		t.Errorf("Context = %q, want %q", parsedDoc.Context, doc.Context)
	}
	if len(parsedSteps) != 3 {
		t.Fatalf("parsed %d steps, want 3", len(parsedSteps))
	}

	// Verify summaries round-trip.
	if parsedSteps[0].Summary != "Lays the groundwork for the feature" {
		t.Errorf("step 0 Summary = %q, want %q", parsedSteps[0].Summary, "Lays the groundwork for the feature")
	}
	if parsedSteps[1].Summary != "Wires up the API endpoints" {
		t.Errorf("step 1 Summary = %q, want %q", parsedSteps[1].Summary, "Wires up the API endpoints")
	}
	if parsedSteps[2].Summary != "" {
		t.Errorf("step 2 Summary = %q, want empty", parsedSteps[2].Summary)
	}

	// Also verify other fields still round-trip alongside summary.
	if parsedSteps[0].Verify != "go build ./pkg/core/..." {
		t.Errorf("step 0 Verify = %q", parsedSteps[0].Verify)
	}
	if len(parsedSteps[0].Files) != 1 || parsedSteps[0].Files[0].Path != "pkg/core/model.go" {
		t.Errorf("step 0 Files = %+v", parsedSteps[0].Files)
	}
	if parsedSteps[0].Details != "Create the model struct" {
		t.Errorf("step 0 Details = %q", parsedSteps[0].Details)
	}
}

func TestParsePlanMarkdown_BackwardCompat(t *testing.T) {
	// A PLAN.md without any Summary lines — should parse fine with empty Summary fields.
	md := `# Execution Plan

**Goal:** Legacy Plan

_Last updated: 2024-06-01T10:00:00Z_

## Phases

- [x] **setup** — Initialize the project
  - File (new): go.mod
  Verify: go build ./...
  Create go module
- [~] **implement** — Write the code
  - File (modify): main.go
- [ ] **test** — Add tests

Legend: ` + "`[ ]`" + ` pending · ` + "`[~]`" + ` running · ` + "`[x]`" + ` complete · ` + "`[!]`" + ` failed
`
	_, goal, steps, err := ParsePlanDocument(md)
	if err != nil {
		t.Fatalf("ParsePlanDocument error: %v", err)
	}
	if goal != "Legacy Plan" {
		t.Errorf("goal = %q, want %q", goal, "Legacy Plan")
	}
	if len(steps) != 3 {
		t.Fatalf("parsed %d steps, want 3", len(steps))
	}

	// All Summary fields should be empty.
	for i, s := range steps {
		if s.Summary != "" {
			t.Errorf("step %d (%q) Summary = %q, want empty", i, s.Name, s.Summary)
		}
	}

	// Verify other fields still parse correctly (backward compat).
	if steps[0].Status != "complete" {
		t.Errorf("step 0 status = %q, want complete", steps[0].Status)
	}
	if steps[1].Status != "running" {
		t.Errorf("step 1 status = %q, want running", steps[1].Status)
	}
	if steps[2].Status != "pending" {
		t.Errorf("step 2 status = %q, want pending", steps[2].Status)
	}
	if len(steps[0].Files) != 1 || steps[0].Files[0].Kind != "new" {
		t.Errorf("step 0 files = %+v", steps[0].Files)
	}
	if steps[0].Verify != "go build ./..." {
		t.Errorf("step 0 Verify = %q", steps[0].Verify)
	}
	if steps[0].Details != "Create go module" {
		t.Errorf("step 0 Details = %q", steps[0].Details)
	}
	if steps[0].Name != "setup" || steps[1].Name != "implement" || steps[2].Name != "test" {
		t.Errorf("names = %q, %q, %q", steps[0].Name, steps[1].Name, steps[2].Name)
	}
}

func TestParsePlanDocument_ExportsStatusAndFiles(t *testing.T) {
	md := `# Execution Plan

**Goal:** Ship it

## Context

Why we are doing this.

## Phases

- [x] **one** — First
  - File (new): pkg/tui/plan.go
  - File (modify): pkg/tui/app.go
  Verify: go test ./pkg/tui
  details live here
- [ ] **two** — Second

## What Not To Change

Do not touch the gate.

## Verification

go test ./pkg/tui
`
	doc, goal, steps, err := ParsePlanDocument(md)
	if err != nil {
		t.Fatalf("ParsePlanDocument: %v", err)
	}
	if goal != "Ship it" {
		t.Fatalf("goal = %q", goal)
	}
	if doc.Context == "" || doc.WhatNotToDo == "" || doc.Verification == "" {
		t.Fatalf("document sections not parsed: %+v", doc)
	}
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if steps[0].Status != "complete" || steps[1].Status != "pending" {
		t.Fatalf("statuses = %q/%q", steps[0].Status, steps[1].Status)
	}
	if len(steps[0].Files) != 2 || steps[0].Files[0].Kind != "new" || steps[0].Verify == "" {
		t.Fatalf("files/verify not exported: %+v", steps[0])
	}
}
