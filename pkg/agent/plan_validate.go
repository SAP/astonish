package agent

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Verify kinds persisted on each plan phase. unit is package tests for
// library-only work. behavior is a command that exercises a user-visible
// outcome (CLI, container, HTTP, UI sequence).
const (
	VerifyKindUnit     = "unit"
	VerifyKindBehavior = "behavior"
	// MaxUnitPhaseFiles is the blast-radius cap for a unit-verified phase.
	// Larger wiring is allowed only when the phase proves a behavior.
	MaxUnitPhaseFiles = 12
)

// runningSurfaceMarkers are path prefixes that implement a running product
// surface. A phase that touches them cannot be proven by go test of stubs.
var runningSurfaceMarkers = []string{
	"cmd/",
	"pkg/api/",
	"pkg/launcher/",
	"pkg/daemon/",
	"pkg/sandbox/",
	"pkg/tui/",
	"pkg/browser/",
	"web/",
}

// NormalizeVerifyKind maps caller-provided verify_kind strings to the
// canonical set. Unknown or empty values return "".
func NormalizeVerifyKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case VerifyKindUnit:
		return VerifyKindUnit
	case VerifyKindBehavior:
		return VerifyKindBehavior
	default:
		return ""
	}
}

// PathTouchesRunningSurface reports whether path is under a running product
// surface (CLI, API, launcher, daemon, sandbox, TUI, browser, Studio web).
func PathTouchesRunningSurface(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return false
	}
	for _, m := range runningSurfaceMarkers {
		if p == strings.TrimSuffix(m, "/") || strings.HasPrefix(p, m) || strings.Contains(p, "/"+m) {
			return true
		}
	}
	return false
}

func fileKindIsDelete(kind string) bool {
	return planFileKindLabel(kind) == "delete"
}

func countPhaseFiles(files []PlanFileChange) int {
	n := 0
	for _, f := range files {
		if strings.TrimSpace(f.Path) != "" {
			n++
		}
	}
	return n
}

func stepLabel(s PlanStepInfo, i int) string {
	if name := strings.TrimSpace(s.Name); name != "" {
		return name
	}
	return fmt.Sprintf("step %d", i+1)
}

// ValidateAnnouncedPlan returns a rejection message if the plan is file-shaped
// or missing the capability contract (outcome + verify). An empty return means
// the plan is structurally acceptable. Slice-by-capability guidance is included
// in the message so the model can self-correct.
func ValidateAnnouncedPlan(doc PlanDocumentInfo, steps []PlanStepInfo) string {
	var missing []string

	if strings.TrimSpace(doc.Context) == "" {
		missing = append(missing, "plan context (the 'context' field must explain what the change does, why, and how)")
	}
	if strings.TrimSpace(doc.WhatNotToDo) == "" {
		missing = append(missing, "what_not_to_do (name the interfaces, files, and behaviors that must not change)")
	}
	if strings.TrimSpace(doc.Verification) == "" {
		missing = append(missing, "verification (the end-to-end command sequence that proves the user-visible outcome)")
	}

	type fileRef struct {
		step string
		path string
	}
	groupFiles := map[string][]fileRef{}
	serialVerify := map[string]string{} // verify command → first serial step name

	for i, s := range steps {
		label := stepLabel(s, i)
		if strings.TrimSpace(s.Details) == "" {
			missing = append(missing, fmt.Sprintf("%s: details", label))
		}
		if strings.TrimSpace(s.Summary) == "" {
			missing = append(missing, fmt.Sprintf("%s: summary", label))
		}
		if strings.TrimSpace(s.Outcome) == "" {
			missing = append(missing, fmt.Sprintf("%s: outcome (one sentence the user can check without reading the diff)", label))
		}
		if strings.TrimSpace(s.Verify) == "" {
			missing = append(missing, fmt.Sprintf("%s: verify (the command the runtime will run)", label))
		}
		kind := NormalizeVerifyKind(s.VerifyKind)
		if kind == "" {
			missing = append(missing, fmt.Sprintf("%s: verify_kind (must be 'unit' or 'behavior')", label))
		}

		nFiles := countPhaseFiles(s.Files)
		if nFiles == 0 {
			missing = append(missing, fmt.Sprintf("%s: files", label))
		}

		touchesRunning := false
		deletesRunning := false
		for _, f := range s.Files {
			path := strings.TrimSpace(f.Path)
			if path == "" {
				continue
			}
			if PathTouchesRunningSurface(path) {
				touchesRunning = true
				if fileKindIsDelete(f.Kind) {
					deletesRunning = true
				}
			}
		}

		if kind == VerifyKindUnit && touchesRunning {
			missing = append(missing, fmt.Sprintf("%s: verify_kind must be 'behavior' because this phase touches a running surface (cmd/api/launcher/daemon/sandbox/tui/browser/web) — go test is not proof", label))
		}
		if kind == VerifyKindUnit && nFiles > MaxUnitPhaseFiles {
			missing = append(missing, fmt.Sprintf("%s: %d files with verify_kind=unit (max %d). Split by user-visible capability, not by layer, or use verify_kind=behavior", label, nFiles, MaxUnitPhaseFiles))
		}
		if deletesRunning && kind == VerifyKindUnit {
			missing = append(missing, fmt.Sprintf("%s: deleting a running surface cannot use verify_kind=unit", label))
		}
		if deletesRunning {
			priorBehavior := false
			for _, prev := range steps[:i] {
				if NormalizeVerifyKind(prev.VerifyKind) == VerifyKindBehavior {
					priorBehavior = true
					break
				}
			}
			if !priorBehavior {
				missing = append(missing, fmt.Sprintf("%s: deleting a running surface requires an earlier phase with verify_kind=behavior (do not rip out the old system before the replacement works)", label))
			}
		}

		group := strings.TrimSpace(s.ParallelGroup)
		if group != "" {
			for _, f := range s.Files {
				path := strings.TrimSpace(f.Path)
				if path == "" {
					continue
				}
				groupFiles[group] = append(groupFiles[group], fileRef{step: label, path: path})
			}
		} else if v := strings.TrimSpace(s.Verify); v != "" {
			if other, ok := serialVerify[v]; ok {
				missing = append(missing, fmt.Sprintf("%s and %s share the same verify command %q — phases must be independently testable; slice by capability", other, label, v))
			} else {
				serialVerify[v] = label
			}
		}
	}

	for group, refs := range groupFiles {
		seen := map[string]string{}
		for _, r := range refs {
			if other, ok := seen[r.path]; ok && other != r.step {
				missing = append(missing, fmt.Sprintf("parallel_group %q shares file %s between %s and %s", group, r.path, other, r.step))
			}
			if _, ok := seen[r.path]; !ok {
				seen[r.path] = r.step
			}
		}
	}

	if len(missing) == 0 {
		return ""
	}
	return "Plan rejected — slice by user-visible capability, not by layer (not \"all API files\" then \"all CLI files\"). Each phase needs an outcome the user can check and a verify command the runtime will run. Missing or invalid: " + strings.Join(missing, "; ")
}

// AcceptanceCoveredByVerification reports whether the recorded investigation
// acceptance sequence is present in the plan-level verification text.
func AcceptanceCoveredByVerification(verification, acceptance string) bool {
	acc := strings.TrimSpace(acceptance)
	if acc == "" {
		return true
	}
	return strings.Contains(strings.TrimSpace(verification), acc)
}

// GraphPlanAnnounceMessage returns a rejection message when Graph-Optimized
// Plan mode is active and the plan's verification does not cover the
// acceptance sequence recorded at gplan_finalize. Empty means the gate passes.
func GraphPlanAnnounceMessage(graphMode bool, verification, acceptance string) string {
	if !graphMode {
		return ""
	}
	acc := strings.TrimSpace(acceptance)
	if acc == "" {
		return "Plan rejected — call gplan_finalize with 'acceptance' (the user-visible sequence that proves the job) before announce_plan."
	}
	if !AcceptanceCoveredByVerification(verification, acc) {
		return "Plan rejected — 'verification' must include the acceptance sequence recorded at gplan_finalize: " + acc
	}
	return ""
}
