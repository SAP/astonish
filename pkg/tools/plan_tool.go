package tools

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// planProgressCallback is set by the launcher to emit plan events through
// the same SubTaskProgress pipeline used by delegate_tasks. This allows
// plan events to flow through ChatAgent.SubTaskProgressCallback → ChatRunner
// → SSE → frontend without any new callback wiring.
var planProgressCallback func(event agent.SubTaskProgressEvent)

// planStateCallback is set by the launcher to store the announced plan in
// ChatAgent.PlanState, enabling both automatic sub-task progression and
// explicit model-driven updates via update_plan.
// planStateCallback returns whether the announcement was accepted. A false
// result means the active approved plan won a lifecycle race and must remain.
var planStateCallback func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool

// planStepUpdateCallback is set by the launcher to apply an explicit step
// status transition from the update_plan tool onto the active PlanState. It
// returns the canonical (name, status) actually applied, or ("", "") if there
// is no active plan or the named step was not found.
var planStepUpdateCallback func(step, status string) (name, appliedStatus string)

// SetPlanProgressCallback sets the callback used by plan tools to emit SSE events.
func SetPlanProgressCallback(fn func(event agent.SubTaskProgressEvent)) {
	planProgressCallback = fn
}

// SetPlanStateCallback sets the callback used by announce_plan to store the
// plan in ChatAgent for progression.
func SetPlanStateCallback(fn func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool) {
	planStateCallback = fn
}

// SetPlanStepUpdateCallback sets the callback used by update_plan to apply an
// explicit step status transition onto the active plan.
func SetPlanStepUpdateCallback(fn func(step, status string) (string, string)) {
	planStepUpdateCallback = fn
}

// --- announce_plan tool ---

// PlanFileChangeInput describes a single file a plan phase will touch.
type PlanFileChangeInput struct {
	Path string `json:"path" jsonschema:"Path to the file this phase will touch (relative to the repo root or absolute)."`
	Kind string `json:"kind,omitempty" jsonschema:"How the file changes: 'new' (created), 'modify' (edited), or 'delete' (removed). Defaults to 'modify' when omitted."`
}

// PlanStepInput describes a single step in a plan.
type PlanStepInput struct {
	Name          string                `json:"name" jsonschema:"Short identifier for this step (e.g., 'explore-repos', 'analyze-implementations', 'write-report'). Use this exact value as the 'step' argument to update_plan."`
	Description   string                `json:"description" jsonschema:"Human-readable label for this phase (e.g., 'Explore both repository structures and dependencies')."`
	Details       string                `json:"details" jsonschema:"REQUIRED. Self-contained implementation spec for this phase: exact function/type/method names, signature changes, call-site updates, and test names. Execution must proceed from this text without re-investigating the repo. Persisted to PLAN.md."`
	Summary       string                `json:"summary,omitempty" jsonschema:"REQUIRED. 1-2 sentence plain-English explanation of what this phase accomplishes from the user's perspective. Plans with steps missing summaries are rejected."`
	Files         []PlanFileChangeInput `json:"files" jsonschema:"REQUIRED. The files this phase will create, modify, or delete — the concrete blast radius. List every affected file (the symbol AND its callers, tests, generated code, migrations, docs) so the plan is complete and has no orphaned code. Order phases dependency-first. Persisted to PLAN.md and shown in the plan UI."`
	Verify        string                `json:"verify,omitempty" jsonschema:"The command that proves this phase is done (build, test, or lint), e.g. 'go test ./pkg/agent/...'. Encodes the 'every phase ends verified' discipline. Persisted to PLAN.md."`
	ParallelGroup string                `json:"parallel_group,omitempty" jsonschema:"Optional concurrency group label. Steps sharing the same non-empty label may execute concurrently. Steps touching different file subtrees with no shared interfaces can share a group. Steps that produce types or files consumed by other steps must remain serial (leave this empty or use distinct labels)."`
}

// AnnouncePlanArgs is the input schema for the announce_plan tool.
type AnnouncePlanArgs struct {
	Goal         string          `json:"goal" jsonschema:"A concise title for the overall plan (e.g., 'Source-Level GitHub Comparison: astonish vs openclaw'). Displayed as the plan header."`
	Steps        []PlanStepInput `json:"steps" jsonschema:"Ordered list of steps to complete the goal. Each step represents a distinct phase of work. Keep it to 3-7 phases; put concrete per-phase detail in the 'details' field."`
	Context      string          `json:"context,omitempty" jsonschema:"REQUIRED narrative section explaining WHY this change is happening: the problem, the approach, user flows, and design decisions. Plans without context are rejected."`
	WhatNotToDo  string          `json:"what_not_to_do,omitempty" jsonschema:"Optional scope guard listing what must NOT change during this plan. Guards against accidental scope creep: list APIs, files, behaviors, or invariants that must remain untouched. Persisted to PLAN.md."`
	Verification string          `json:"verification,omitempty" jsonschema:"Optional end-to-end smoke test sequence that proves the entire plan succeeded after all phases complete. More comprehensive than per-phase 'verify' commands: describe the full test run, manual checks, or integration tests. Persisted to PLAN.md."`
}

// AnnouncePlanResult is the output of the announce_plan tool.
type AnnouncePlanResult struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func announcePlan(ctx tool.Context, args AnnouncePlanArgs) (AnnouncePlanResult, error) {
	// Defense in depth: the ChatAgent BeforeTool gate normally rejects this call,
	// but checking the request-scoped flag here prevents a stale/racing invocation
	// from replacing PlanState even if it reaches the function body directly.
	if ctx != nil {
		if po := agent.PromptOverridesFromContext(ctx); po != nil && po.ApprovedPlanExecution {
			return AnnouncePlanResult{Status: "blocked_approved_plan_execution"}, nil
		}
	}

	if msg := incompletePlanMessage(args.Context, args.Steps); msg != "" {
		return AnnouncePlanResult{Status: "incomplete_plan", Message: msg}, nil
	}

	steps := make([]agent.PlanStepInfo, len(args.Steps))
	for i, s := range args.Steps {
		files := make([]agent.PlanFileChange, 0, len(s.Files))
		for _, f := range s.Files {
			if f.Path == "" {
				continue
			}
			files = append(files, agent.PlanFileChange{Path: f.Path, Kind: f.Kind})
		}
		steps[i] = agent.PlanStepInfo{
			Name:          s.Name,
			Description:   s.Description,
			Details:       s.Details,
			Summary:       s.Summary,
			Files:         files,
			Verify:        s.Verify,
			ParallelGroup: s.ParallelGroup,
		}
	}

	doc := agent.PlanDocumentInfo{
		Context:      args.Context,
		WhatNotToDo:  args.WhatNotToDo,
		Verification: args.Verification,
	}

	// Store the plan in ChatAgent for progression + PLAN.md persistence. A
	// lifecycle rejection is a no-op: do not emit another approval event.
	if planStateCallback != nil && !planStateCallback(args.Goal, doc, steps) {
		return AnnouncePlanResult{Status: "blocked_active_approved_plan"}, nil
	}

	// Emit SSE event for frontend rendering.
	if planProgressCallback != nil {
		planProgressCallback(agent.SubTaskProgressEvent{
			Type:             "plan_announced",
			PlanGoal:         args.Goal,
			PlanSteps:        steps,
			PlanContext:      args.Context,
			PlanWhatNotToDo:  args.WhatNotToDo,
			PlanVerification: args.Verification,
		})
	}

	return AnnouncePlanResult{Status: "ok"}, nil
}

func incompletePlanMessage(context string, steps []PlanStepInput) string {
	var missing []string
	if strings.TrimSpace(context) == "" {
		missing = append(missing, "plan context (the 'context' field must explain what the change does, why, and how)")
	}
	for i, s := range steps {
		label := strings.TrimSpace(s.Name)
		if label == "" {
			label = fmt.Sprintf("step %d", i+1)
		}
		if strings.TrimSpace(s.Details) == "" {
			missing = append(missing, fmt.Sprintf("%s: details", label))
		}
		hasFile := false
		for _, f := range s.Files {
			if strings.TrimSpace(f.Path) != "" {
				hasFile = true
				break
			}
		}
		if !hasFile {
			missing = append(missing, fmt.Sprintf("%s: files", label))
		}
		if strings.TrimSpace(s.Summary) == "" {
			missing = append(missing, fmt.Sprintf("%s: summary", label))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "Plan rejected — each phase needs 'details' (implementation spec), 'files' (blast radius), and 'summary' (user-facing explanation). The plan as a whole needs a 'context' section explaining what/why/how. Missing: " + strings.Join(missing, "; ")
}

// NewAnnouncePlanTool creates the announce_plan tool.
func NewAnnouncePlanTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "announce_plan",
		Description: `Announce a structured execution plan before starting multi-step work. The plan appears as a visible checklist in the UI and is persisted to a session PLAN.md that survives context compaction. Keep phases high-level (3-7) — each phase is a distinct chunk of work, not an individual tool call. Order phases dependency-first (shared types/interfaces before their consumers).

Document-level sections (set once for the whole plan):
- 'context': REQUIRED — plans without context are rejected. Write the problem, approach, user flows (for UI changes), and design decisions. This is a design document preamble, not a one-liner.
- 'what_not_to_do': REQUIRED scope guard — name the specific interfaces, files, and behaviors that must NOT change.
- 'verification': the end-to-end smoke test sequence for the entire plan after all phases complete. More comprehensive than per-phase verify commands.

Make each phase complete, not a sketch. 'details' and 'files' are required:
- 'files': list every file the phase touches, each marked 'new'/'modify'/'delete'. Include the symbol AND its callers, tests, generated code, migrations, and docs so nothing is left orphaned or unwired.
- 'details': a self-contained implementation spec — exact symbols, signature changes, call-site updates, test names. Execution must proceed from this text without re-investigating the repo.
- 'summary': REQUIRED — plans with steps missing summaries are rejected. 1-2 sentence explanation of what this phase accomplishes from the USER's perspective.
- 'verify': the command that proves the phase is done (build/test/lint).
- 'parallel_group': Structure the plan in waves. Before submitting, group phases by which ones can start simultaneously — a phase can start when it has no dependency on an unfinished phase's output (types, files, compiled symbols). Assign all phases in the same wave the same label (e.g. 'wave-1', 'wave-2'). Serial phases — where one phase produces something another phase needs — get no label or a distinct label. Most plans have at least two waves; a plan where every phase is unlabeled should only happen when every phase strictly depends on the previous one. Independence is about type/file dependencies, not package boundaries: two phases in the same package can be in the same wave if they touch disjoint files and neither produces a symbol the other consumes.

Completeness and design quality:
Before calling announce_plan, verify you have covered: (1) both frontend and backend if the change crosses the boundary, (2) terminal TUI if you changed Studio Chat rendering, (3) docs/architecture/ if the subsystem has documentation, (4) tests for every file you modify, (5) any breaking changes surfaced explicitly.
For UI changes, specify behavior per mode and per state (empty, error, populated). For state machines, define every transition. Use typed constants over string comparisons.

Tracking progress:
- For work you do yourself on the main thread (edit_file, shell_command, etc.), call update_plan to mark each phase 'running' when you start it and 'complete' (or 'failed') when you finish. This keeps the checklist and PLAN.md accurate as you go, and lets you recover exactly where you were after a context summary.
- For delegated work, set the plan_step field on each delegate_tasks task; those phases progress automatically from sub-task lifecycle events.

After a context summary, re-read PLAN.md to recover the plan and mark the next phase running.`,
	}, announcePlan)
}

// --- update_plan tool ---

// UpdatePlanArgs is the input schema for the update_plan tool.
type UpdatePlanArgs struct {
	Step   string `json:"step" jsonschema:"The 'name' of the plan phase to update (exactly as given to announce_plan)."`
	Status string `json:"status" jsonschema:"New status for the phase: 'running' (you are starting it), 'complete' (finished successfully), or 'failed' (could not complete)."`
}

// UpdatePlanResult is the output of the update_plan tool.
type UpdatePlanResult struct {
	Status  string `json:"status"`
	Step    string `json:"step,omitempty"`
	Applied string `json:"applied,omitempty"`
}

func updatePlan(_ tool.Context, args UpdatePlanArgs) (UpdatePlanResult, error) {
	if planStepUpdateCallback == nil {
		return UpdatePlanResult{Status: "no_active_plan"}, nil
	}
	name, applied := planStepUpdateCallback(args.Step, args.Status)
	if name == "" {
		return UpdatePlanResult{Status: "step_not_found"}, nil
	}

	// Emit SSE so the UI checklist reflects the transition.
	if planProgressCallback != nil {
		planProgressCallback(agent.SubTaskProgressEvent{
			Type:       "plan_step_update",
			StepName:   name,
			StepStatus: applied,
		})
	}

	return UpdatePlanResult{Status: "ok", Step: name, Applied: applied}, nil
}

// NewUpdatePlanTool creates the update_plan tool.
func NewUpdatePlanTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "update_plan",
		Description: `Update the status of a phase in the plan you announced with announce_plan. Use this to keep the checklist and the session PLAN.md accurate as you work on the main thread: mark a phase 'running' when you start it and 'complete' (or 'failed') when you finish. This is how progress is tracked for work you do yourself (not delegated) — do it as you go so the plan reflects reality and you can resume precisely after a context summary.`,
	}, updatePlan)
}
