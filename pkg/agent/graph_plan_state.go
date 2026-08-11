package agent

import "sync"

// GraphPlanPhase identifies the current stage of the Graph-Optimized Plan mode
// state machine. The current phase determines the runtime tool allow-list
// enforced by the BeforeToolCallback gate in ChatAgent.Run.
type GraphPlanPhase string

const (
	// GraphPlanPhaseGraph is the initial phase: only codegraph_explore is
	// available so the model must drive discovery through the knowledge graph
	// before reading files or falling back to broad search.
	GraphPlanPhaseGraph GraphPlanPhase = "graph"
	// GraphPlanPhaseRead unlocks read_file (plus codegraph_explore) so the
	// model reads exactly the regions it identified in the graph phase.
	GraphPlanPhaseRead GraphPlanPhase = "read"
	// GraphPlanPhaseGap unlocks the complementary read-only tools (grep_search,
	// find_files, tree-sitter, web_fetch, memory_*) and delegate_tasks for
	// filling genuine gaps codegraph could not answer.
	GraphPlanPhaseGap GraphPlanPhase = "gap"
	// GraphPlanPhasePlan unlocks announce_plan so the model can record the
	// finalized plan.
	GraphPlanPhasePlan GraphPlanPhase = "plan"
)

// GraphPlanState tracks the current phase for a single Graph-Optimized Plan
// session. It mirrors PlanState's concurrency style (a mutex guarding all
// access) so the gate and the transition tools can read/advance it safely from
// the ADK tool-callback goroutines.
type GraphPlanState struct {
	mu    sync.Mutex
	phase GraphPlanPhase
}

// NewGraphPlanState returns a state machine starting in the graph phase.
func NewGraphPlanState() *GraphPlanState {
	return &GraphPlanState{phase: GraphPlanPhaseGraph}
}

// Phase returns the current phase. Thread-safe.
func (g *GraphPlanState) Phase() GraphPlanPhase {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.phase
}

// Advance transitions to the given phase. Thread-safe. Any target is accepted;
// the transition tools encode the legal orderings.
func (g *GraphPlanState) Advance(to GraphPlanPhase) {
	g.mu.Lock()
	g.phase = to
	g.mu.Unlock()
}

// Reset returns the machine to the initial graph phase. Called at the start of
// each new user turn so a fresh plan always begins with the graph phase.
func (g *GraphPlanState) Reset() {
	g.mu.Lock()
	g.phase = GraphPlanPhaseGraph
	g.mu.Unlock()
}

// graphPlanTransitionTools are the phase-transition tools. They only mutate the
// phase state (never the filesystem/shell), so the gate always allows them
// regardless of the current phase.
var graphPlanTransitionTools = map[string]bool{
	"gplan_reads":    true,
	"gplan_gaps":     true,
	"gplan_finalize": true,
}

// IsGraphPlanTransitionTool reports whether the named tool is a phase-transition
// tool that is always allowed in Graph-Optimized Plan mode.
func IsGraphPlanTransitionTool(name string) bool {
	return graphPlanTransitionTools[name]
}

// GraphPlanPhaseTools returns the additive allow-list of tool names permitted in
// the given phase (in addition to the always-allowed transition tools and
// announce_plan/update_plan). This is the single source of truth for the
// runtime gate. The lists are additive: each phase includes everything the
// prior phases allowed.
func GraphPlanPhaseTools(phase GraphPlanPhase) map[string]bool {
	allowed := map[string]bool{}

	// graph phase: only codegraph exploration.
	add := func(names ...string) {
		for _, n := range names {
			allowed[n] = true
		}
	}
	add("codegraph_explore", "find_files")

	if phase == GraphPlanPhaseGraph {
		return allowed
	}

	// read phase (and beyond): unlock reading identified regions.
	add("read_file", "read_pdf", "filter_json")

	if phase == GraphPlanPhaseRead {
		return allowed
	}

	// gap phase (and beyond): unlock complementary read-only tools + delegation.
	add(
		"grep_search",
		"find_files",
		"file_tree",
		"repo_map",
		"code_definition",
		"code_references",
		"web_fetch",
		"memory_search",
		"memory_get",
		"skill_lookup",
		"delegate_tasks",
	)

	if phase == GraphPlanPhaseGap {
		return allowed
	}

	// plan phase: unlock recording the finalized plan.
	add("announce_plan", "update_plan")
	return allowed
}
