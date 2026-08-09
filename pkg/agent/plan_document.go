package agent

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// PLAN.md is a per-session, on-disk record of the active execution plan. It is
// written whenever a plan is announced (announce_plan) and rewritten on every
// phase transition, so the plan survives context compaction: after older
// messages are summarized, the model can re-read PLAN.md to recover the exact
// phases and their completion status.
//
// The document is intentionally human-readable Markdown with GitHub-style
// checkboxes so it renders cleanly and can be re-parsed back into plan state.

// planStatusMarker maps a planStep status to its checkbox marker.
func planStatusMarker(status string) string {
	switch status {
	case "complete":
		return "x"
	case "running":
		return "~"
	case "failed":
		return "!"
	default: // "pending"
		return " "
	}
}

// planMarkerStatus is the inverse of planStatusMarker.
func planMarkerStatus(marker string) string {
	switch strings.TrimSpace(marker) {
	case "x", "X":
		return "complete"
	case "~":
		return "running"
	case "!":
		return "failed"
	default:
		return "pending"
	}
}

// planFileKindLabel normalizes a PlanFileChange kind to its rendered label.
// Unknown or empty kinds render as "modify".
func planFileKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "new", "add", "create":
		return "new"
	case "delete", "remove", "rm":
		return "delete"
	default:
		return "modify"
	}
}

// RenderPlanFromInfo serializes a plan from the public PlanStepInfo type used
// by announce_plan. It is the exported counterpart of RenderPlanMarkdown for
// callers (e.g. the TUI) that hold PlanStepInfo slices rather than planSteps.
func RenderPlanFromInfo(goal string, steps []PlanStepInfo) string {
	internal := make([]planStep, len(steps))
	for i, s := range steps {
		internal[i] = planStep{
			name:        s.Name,
			description: s.Description,
			details:     s.Details,
			files:       s.Files,
			verify:      s.Verify,
			status:      "pending",
		}
	}
	return RenderPlanMarkdown(goal, internal)
}


// planClock is overridable in tests for a deterministic timestamp.
var planClock = time.Now

func RenderPlanMarkdown(goal string, steps []planStep) string {
	var sb strings.Builder
	sb.WriteString("# Execution Plan\n\n")
	if strings.TrimSpace(goal) != "" {
		sb.WriteString(fmt.Sprintf("**Goal:** %s\n\n", goal))
	}
	sb.WriteString(fmt.Sprintf("_Last updated: %s_\n\n", planClock().UTC().Format(time.RFC3339)))
	sb.WriteString("## Phases\n\n")
	if len(steps) == 0 {
		sb.WriteString("_(no phases)_\n")
		return sb.String()
	}
	for _, s := range steps {
		marker := planStatusMarker(s.status)
		line := fmt.Sprintf("- [%s] **%s**", marker, s.name)
		if strings.TrimSpace(s.description) != "" {
			line += " — " + s.description
		}
		sb.WriteString(line + "\n")
		// Emit affected files as an indented, machine-parseable sub-block so the
		// concrete blast radius (dependency-first, no orphaned code) is recorded
		// and can be re-hydrated after compaction.
		for _, f := range s.files {
			if strings.TrimSpace(f.Path) == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("  - File (%s): %s\n", planFileKindLabel(f.Kind), f.Path))
		}
		// Emit the optional verification command.
		if strings.TrimSpace(s.verify) != "" {
			sb.WriteString("  Verify: " + strings.TrimSpace(s.verify) + "\n")
		}
		// Emit optional richer details as an indented sub-block so the detailed
		// plan (files/commands/approach) survives compaction, not just the label.
		if strings.TrimSpace(s.details) != "" {
			for _, dl := range strings.Split(strings.TrimRight(s.details, "\n"), "\n") {
				sb.WriteString("  " + dl + "\n")
			}
		}
	}
	sb.WriteString("\nLegend: `[ ]` pending · `[~]` running · `[x]` complete · `[!]` failed\n")
	return sb.String()
}

// ParsePlanMarkdown re-hydrates a PLAN.md document back into a goal and steps.
// It tolerates minor formatting variance (extra whitespace) and ignores lines
// that are not phase checkboxes. Returns an error only when no phases are found.
func ParsePlanMarkdown(md string) (goal string, steps []planStep, err error) {
	scanner := bufio.NewScanner(strings.NewReader(md))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var detailLines []string
	// flushDetails attaches accumulated indented detail lines to the last phase.
	flushDetails := func() {
		if len(steps) > 0 && len(detailLines) > 0 {
			steps[len(steps)-1].details = strings.Join(detailLines, "\n")
		}
		detailLines = nil
	}
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		// Goal line: **Goal:** <text>
		if strings.HasPrefix(trimmed, "**Goal:**") {
			flushDetails()
			goal = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Goal:**"))
			continue
		}
		// Phase line: - [<marker>] **<name>** — <description>
		if strings.HasPrefix(trimmed, "- [") {
			flushDetails()
			if step, ok := parsePlanPhaseLine(trimmed); ok {
				steps = append(steps, step)
			}
			continue
		}
		// Indented, non-empty line following a phase = that phase's detail block.
		// Stop collecting at the legend footer.
		if len(steps) > 0 && strings.HasPrefix(raw, "  ") && trimmed != "" {
			if strings.HasPrefix(trimmed, "Legend:") {
				flushDetails()
				continue
			}
			// Affected-file line: "- File (<kind>): <path>". Recognized before
			// the generic detail collector so the structured blast radius
			// round-trips losslessly.
			if kind, path, ok := parsePlanFileLine(trimmed); ok {
				steps[len(steps)-1].files = append(steps[len(steps)-1].files,
					PlanFileChange{Path: path, Kind: kind})
				continue
			}
			// Verification line: "Verify: <command>".
			if strings.HasPrefix(trimmed, "Verify:") {
				steps[len(steps)-1].verify = strings.TrimSpace(strings.TrimPrefix(trimmed, "Verify:"))
				continue
			}
			detailLines = append(detailLines, trimmed)
			continue
		}
		// Any other non-indented line ends the current detail block.
		if trimmed != "" {
			flushDetails()
		}
	}
	flushDetails()
	if scanErr := scanner.Err(); scanErr != nil {
		return "", nil, fmt.Errorf("failed to scan plan document: %w", scanErr)
	}
	if len(steps) == 0 {
		return goal, nil, fmt.Errorf("no plan phases found in document")
	}
	return goal, steps, nil
}

// parsePlanPhaseLine parses a single "- [x] **name** — description" line.
func parsePlanPhaseLine(line string) (planStep, bool) {
	rest := strings.TrimPrefix(line, "- [")
	close := strings.Index(rest, "]")
	if close < 0 {
		return planStep{}, false
	}
	marker := rest[:close]
	rest = strings.TrimSpace(rest[close+1:])

	// Strip bold markers around the name.
	name := rest
	description := ""
	if idx := strings.Index(rest, " — "); idx >= 0 {
		name = strings.TrimSpace(rest[:idx])
		description = strings.TrimSpace(rest[idx+len(" — "):])
	}
	name = strings.TrimSpace(strings.Trim(name, "*"))
	if name == "" {
		return planStep{}, false
	}
	return planStep{
		name:        name,
		description: description,
		status:      planMarkerStatus(marker),
	}, true
}

// parsePlanFileLine parses a single "- File (<kind>): <path>" line back into its
// kind and path. Returns ok=false for lines that do not match the shape.
func parsePlanFileLine(line string) (kind, path string, ok bool) {
	rest := strings.TrimPrefix(line, "- File (")
	if rest == line {
		return "", "", false
	}
	closeIdx := strings.Index(rest, ")")
	if closeIdx < 0 {
		return "", "", false
	}
	kind = planFileKindLabel(rest[:closeIdx])
	rest = strings.TrimSpace(rest[closeIdx+1:])
	rest = strings.TrimPrefix(rest, ":")
	path = strings.TrimSpace(rest)
	if path == "" {
		return "", "", false
	}
	return kind, path, true
}
