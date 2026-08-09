package tools

import (
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
var planStateCallback func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo)

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
func SetPlanStateCallback(fn func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo)) {
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
	Details       string                `json:"details,omitempty" jsonschema:"Optional richer, concrete plan for this phase: the specific approach and reasoning. Persisted to the session PLAN.md so the detailed plan survives context compaction. Use this to record the actual step-by-step work, not just the label."`
	Files         []PlanFileChangeInput `json:"files,omitempty" jsonschema:"The files this phase will create, modify, or delete — the concrete blast radius. List every affected file (the symbol AND its callers, tests, generated code, migrations, docs) so the plan is complete and has no orphaned code. Order phases dependency-first. Persisted to PLAN.md and shown in the plan UI."`
	Verify        string                `json:"verify,omitempty" jsonschema:"The command that proves this phase is done (build, test, or lint), e.g. 'go test ./pkg/agent/...'. Encodes the 'every phase ends verified' discipline. Persisted to PLAN.md."`
	ParallelGroup string                `json:"parallel_group,omitempty" jsonschema:"Optional concurrency group label. Steps sharing the same non-empty label may execute concurrently. Steps touching different file subtrees with no shared interfaces can share a group. Steps that produce types or files consumed by other steps must remain serial (leave this empty or use distinct labels)."`
}

// AnnouncePlanArgs is the input schema for the announce_plan tool.
type AnnouncePlanArgs struct {
	Goal         string          `json:"goal" jsonschema:"A concise title for the overall plan (e.g., 'Source-Level GitHub Comparison: astonish vs openclaw'). Displayed as the plan header."`
	Steps        []PlanStepInput `json:"steps" jsonschema:"Ordered list of steps to complete the goal. Each step represents a distinct phase of work. Keep it to 3-7 phases; put concrete per-phase detail in the 'details' field."`
	Context      string          `json:"context,omitempty" jsonschema:"Optional narrative section explaining WHY this change is happening: the motivation, the problem being solved, and any relevant background. Persisted to PLAN.md so the executor understands the reasoning behind the plan."`
	WhatNotToDo  string          `json:"what_not_to_do,omitempty" jsonschema:"Optional scope guard listing what must NOT change during this plan. Guards against accidental scope creep: list APIs, files, behaviors, or invariants that must remain untouched. Persisted to PLAN.md."`
	Verification string          `json:"verification,omitempty" jsonschema:"Optional end-to-end smoke test sequence that proves the entire plan succeeded after all phases complete. More comprehensive than per-phase 'verify' commands: describe the full test run, manual checks, or integration tests. Persisted to PLAN.md."`
}

// AnnouncePlanResult is the output of the announce_plan tool.
type AnnouncePlanResult struct {
	Status string `json:"status"`
}

func announcePlan(_ tool.Context, args AnnouncePlanArgs) (AnnouncePlanResult, error) {
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

	// Store the plan in ChatAgent for progression + PLAN.md persistence.
	if planStateCallback != nil {
		planStateCallback(args.Goal, doc, steps)
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

// NewAnnouncePlanTool creates the announce_plan tool.
func NewAnnouncePlanTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "announce_plan",
		Description: `Announce a structured execution plan before starting multi-step work. The plan appears as a visible checklist in the UI and is persisted to a session PLAN.md that survives context compaction. Keep phases high-level (3-7) — each phase is a distinct chunk of work, not an individual tool call. Order phases dependency-first (shared types/interfaces before their consumers).

Document-level sections (set once for the whole plan):
- 'context': WHY this change is needed — motivation, problem, background. Write this when the reason for the change isn't obvious from the goal title.
- 'what_not_to_do': explicit scope guard — list APIs, files, behaviors, or invariants that must NOT change. Guards against accidental scope creep during execution.
- 'verification': the end-to-end smoke test sequence for the entire plan after all phases complete. More comprehensive than per-phase verify commands.

Make each phase complete, not a sketch:
- 'files': list every file the phase touches, each marked 'new'/'modify'/'delete'. Include the symbol AND its callers, tests, generated code, migrations, and docs so nothing is left orphaned or unwired.
- 'details': the concrete approach and reasoning for the phase.
- 'verify': the command that proves the phase is done (build/test/lint).
- 'parallel_group': optional label grouping steps that may run concurrently. Use the same label for phases touching independent file subtrees with no shared interfaces (e.g., 'pkg/agent/' vs 'pkg/tui/' vs 'web/'). Leave empty or use distinct labels for steps whose output feeds another step's input.

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
