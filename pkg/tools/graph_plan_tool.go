package tools

import (
	"os"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/pathscope"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// graphPlanAdvanceCallback is set by the launcher to advance the per-session
// Graph-Optimized Plan phase state machine. It mirrors the SetPlanStateCallback
// wiring pattern in plan_tool.go: the launcher owns the ChatAgent and its
// per-session GraphPlanState, so the tool package stays decoupled from it.
//
// The callback returns an error only for genuine wiring problems; a nil error
// means the phase advanced (or was already at/past the target).
var graphPlanAdvanceCallback func(to agent.GraphPlanPhase) error

// SetGraphPlanAdvanceCallback registers the callback used by the gplan_*
// transition tools to advance the active session's Graph-Optimized Plan phase.
func SetGraphPlanAdvanceCallback(fn func(to agent.GraphPlanPhase) error) {
	graphPlanAdvanceCallback = fn
}

func advanceGraphPlan(to agent.GraphPlanPhase) string {
	if graphPlanAdvanceCallback == nil {
		return "no_active_graph_plan"
	}
	if err := graphPlanAdvanceCallback(to); err != nil {
		return "error"
	}
	return "ok"
}

// --- gplan_reads tool ---

// GraphPlanReadEntry is a single planned file read synthesized from the graph
// phase.
type GraphPlanReadEntry struct {
	Path   string `json:"path" jsonschema:"Path to the file (or file:line region) you need to read, relative to the repo root or absolute."`
	Reason string `json:"reason,omitempty" jsonschema:"Why you need to read this region — what the code graph told you about it (e.g. 'declaration of Foo, changed by this task' or 'caller of Bar identified via call edges')."`
}

// GraphPlanReadsArgs is the input schema for gplan_reads.
type GraphPlanReadsArgs struct {
	ReadList []GraphPlanReadEntry `json:"read_list" jsonschema:"The synthesized, non-repetitive list of regions to read next, derived from your codegraph_explore queries. Compound your findings: do not list anything already in your context. Advancing to the READ phase unlocks read_file for exactly these regions."`
}

// GraphPlanReadsResult is the output of gplan_reads.
type GraphPlanReadsResult struct {
	Status  string   `json:"status"`
	Phase   string   `json:"phase,omitempty"`
	Count   int      `json:"count,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

func graphPlanReads(_ tool.Context, args GraphPlanReadsArgs) (GraphPlanReadsResult, error) {
	// Validate that every listed path exists before committing to the READ phase.
	// If any paths are missing, return bad_paths without advancing — the model
	// must correct the list (e.g. via find_files) and retry.
	var missing []string
	for _, entry := range args.ReadList {
		p := pathscope.ExpandHome(entry.Path)
		// Strip optional :line or :line-line suffix (e.g. "pkg/store/context.go:1-30").
		if idx := strings.Index(p, ":"); idx != -1 {
			p = p[:idx]
		}
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, entry.Path)
		}
	}
	if len(missing) > 0 {
		return GraphPlanReadsResult{
			Status:  "bad_paths",
			Missing: missing,
		}, nil
	}

	status := advanceGraphPlan(agent.GraphPlanPhaseRead)
	if status != "ok" {
		return GraphPlanReadsResult{Status: status}, nil
	}
	return GraphPlanReadsResult{
		Status: "ok",
		Phase:  string(agent.GraphPlanPhaseRead),
		Count:  len(args.ReadList),
	}, nil
}

// NewGraphPlanReadsTool creates the gplan_reads transition tool.
func NewGraphPlanReadsTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "gplan_reads",
		Description: `Graph-Optimized Plan mode ONLY. Record the synthesized read list you derived from codegraph_explore and advance from the GRAPH phase to the READ phase (which unlocks read_file for exactly these regions).

Call this once you have queried the code graph and know precisely which regions you need to read. Only include paths that codegraph_explore explicitly returned — do NOT guess or infer filenames. Compound your graph findings into a non-repetitive list — do not include anything already in your context, and do not re-query the graph for it. Each entry is a path (optionally file:line) plus a short reason drawn from what the graph told you.

If any path does not exist on the filesystem, the tool returns {"status":"bad_paths","missing":[...]} and does NOT advance the phase. To recover: call find_files to locate the correct filename, then retry gplan_reads with corrected paths.`,
	}, graphPlanReads)
}

// --- gplan_gaps tool ---

// GraphPlanGapEntry is a single genuine gap codegraph could not answer.
type GraphPlanGapEntry struct {
	Question                  string `json:"question" jsonschema:"The specific open question you still need to answer to produce a complete plan."`
	WhyCodegraphInsufficient  string `json:"why_codegraph_insufficient,omitempty" jsonschema:"Why codegraph_explore could not answer this (e.g. 'language not indexed', 'non-code config file', 'string-literal usage not in call graph')."`
}

// GraphPlanGapsArgs is the input schema for gplan_gaps.
type GraphPlanGapsArgs struct {
	Gaps []GraphPlanGapEntry `json:"gaps,omitempty" jsonschema:"The genuine gaps codegraph could not fill. Advancing to the GAP phase unlocks the complementary read-only tools (grep_search, find_files, tree-sitter, web_fetch, memory_*) and delegate_tasks. Leave empty ONLY to skip: an empty list means you have no gaps and advances straight to the PLAN phase instead."`
}

// GraphPlanGapsResult is the output of gplan_gaps.
type GraphPlanGapsResult struct {
	Status string `json:"status"`
	Phase  string `json:"phase,omitempty"`
	Count  int    `json:"count,omitempty"`
}

func graphPlanGaps(_ tool.Context, args GraphPlanGapsArgs) (GraphPlanGapsResult, error) {
	// An empty gap list is a legitimate "skip" — there is nothing codegraph
	// missed, so jump straight to the PLAN phase.
	target := agent.GraphPlanPhaseGap
	if len(args.Gaps) == 0 {
		target = agent.GraphPlanPhasePlan
	}
	status := advanceGraphPlan(target)
	if status != "ok" {
		return GraphPlanGapsResult{Status: status}, nil
	}
	return GraphPlanGapsResult{
		Status: "ok",
		Phase:  string(target),
		Count:  len(args.Gaps),
	}, nil
}

// NewGraphPlanGapsTool creates the gplan_gaps transition tool.
func NewGraphPlanGapsTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "gplan_gaps",
		Description: `Graph-Optimized Plan mode ONLY. Declare the genuine gaps codegraph could not answer and advance to the GAP phase, which unlocks the complementary read-only tools (grep_search, find_files, file_tree, repo_map, code_definition, code_references, web_fetch, memory_search, memory_get, skill_lookup) and delegate_tasks.

Use this when — after querying the graph and reading the identified regions — real open questions remain that the code graph structurally cannot answer. In the GAP phase, prefer delegate_tasks with read-only 'tools' filters to fan out independent gap questions in parallel.

Two special cases:
- Call this from the GRAPH phase (before reading) if codegraph returned no coverage at all (unsupported language / not indexed) — it will unlock the fallback tools immediately.
- Pass an EMPTY gaps list if you have no gaps: that skips the GAP phase and advances straight to the PLAN phase.`,
	}, graphPlanGaps)
}

// --- gplan_finalize tool ---

// GraphPlanFinalizeArgs is the input schema for gplan_finalize.
type GraphPlanFinalizeArgs struct {
	Notes string `json:"notes,omitempty" jsonschema:"Optional short note on why investigation is complete (e.g. 'all callers and tests enumerated; ready to plan')."`
}

// GraphPlanFinalizeResult is the output of gplan_finalize.
type GraphPlanFinalizeResult struct {
	Status string `json:"status"`
	Phase  string `json:"phase,omitempty"`
}

func graphPlanFinalize(_ tool.Context, _ GraphPlanFinalizeArgs) (GraphPlanFinalizeResult, error) {
	status := advanceGraphPlan(agent.GraphPlanPhasePlan)
	if status != "ok" {
		return GraphPlanFinalizeResult{Status: status}, nil
	}
	return GraphPlanFinalizeResult{
		Status: "ok",
		Phase:  string(agent.GraphPlanPhasePlan),
	}, nil
}

// NewGraphPlanFinalizeTool creates the gplan_finalize transition tool.
func NewGraphPlanFinalizeTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "gplan_finalize",
		Description: `Graph-Optimized Plan mode ONLY. Declare investigation complete and advance to the PLAN phase, which unlocks announce_plan.

Call this once you can name every file you would change and why — from the READ phase (no gaps) or after closing all gaps in the GAP phase. Immediately follow with announce_plan to record the finalized, dependency-first plan.`,
	}, graphPlanFinalize)
}
