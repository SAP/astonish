package agent

import (
	"fmt"
	"sync"
)

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

// GraphPlanState tracks the current phase and bounded exploration budget for a
// single Graph-Optimized Plan session. All access is guarded because parallel
// tool calls may enter the gate concurrently.
type GraphPlanState struct {
	mu       sync.Mutex
	phase    GraphPlanPhase
	counters GraphPlanCounters
}

// GraphPlanCounters is a snapshot of exploration charged to the current turn.
type GraphPlanCounters struct {
	GraphQueries    int
	FileReads       int
	GapCalls        int
	DelegationCalls int
	DelegatedTasks  int
	Total           int
}

const (
	GraphPlanMaxGraphQueries    = 4
	GraphPlanMaxFileReads       = 12
	GraphPlanMaxGapCalls        = 12
	GraphPlanMaxDelegationCalls = 1
	GraphPlanMaxDelegatedTasks  = 6
	GraphPlanMaxExploration     = 24
)

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

// Counters returns a concurrency-safe budget snapshot.
func (g *GraphPlanState) Counters() GraphPlanCounters {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counters
}

// ChargeExploration atomically reserves budget for an exploration tool. An
// empty message means the call is allowed; a non-empty message explains the
// transition the model must take instead. Rejected calls are not charged.
func (g *GraphPlanState) ChargeExploration(name string, args map[string]any) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	next := g.counters
	switch name {
	case "codegraph_explore":
		next.GraphQueries++
		if next.GraphQueries > GraphPlanMaxGraphQueries {
			return g.limitMessageLocked("codegraph query", GraphPlanMaxGraphQueries)
		}
	case "read_file", "read_pdf", "filter_json":
		next.FileReads++
		if next.FileReads > GraphPlanMaxFileReads {
			return g.limitMessageLocked("file read", GraphPlanMaxFileReads)
		}
	case "delegate_tasks":
		next.DelegationCalls++
		next.DelegatedTasks += delegationTaskCount(args)
		if next.DelegationCalls > GraphPlanMaxDelegationCalls {
			return g.limitMessageLocked("delegation call", GraphPlanMaxDelegationCalls)
		}
		if next.DelegatedTasks > GraphPlanMaxDelegatedTasks {
			return g.limitMessageLocked("delegated task", GraphPlanMaxDelegatedTasks)
		}
	default:
		if !GraphPlanPhaseTools(g.phase)[name] {
			return ""
		}
		next.GapCalls++
		if next.GapCalls > GraphPlanMaxGapCalls {
			return g.limitMessageLocked("gap-filling call", GraphPlanMaxGapCalls)
		}
	}

	next.Total++
	if next.Total > GraphPlanMaxExploration {
		return "Graph-plan exploration hard limit reached. Call gplan_finalize now and announce the plan from the evidence already collected."
	}
	g.counters = next
	return ""
}

func delegationTaskCount(args map[string]any) int {
	if tasks, ok := args["tasks"].([]any); ok {
		return len(tasks)
	}
	// A decoding shape we cannot inspect still represents at least one task.
	return 1
}

func (g *GraphPlanState) limitMessageLocked(kind string, limit int) string {
	transition := "gplan_finalize"
	switch g.phase {
	case GraphPlanPhaseGraph:
		transition = "gplan_reads (or gplan_gaps if codegraph has no coverage)"
	case GraphPlanPhaseRead:
		transition = "gplan_gaps (or gplan_finalize if no gaps remain)"
	}
	return fmt.Sprintf("Graph-plan %s limit reached (%d). Stop researching and call %s.", kind, limit, transition)
}

// Advance transitions to the given phase. Thread-safe. Any target is accepted;
// the transition tools encode the legal orderings.
func (g *GraphPlanState) Advance(to GraphPlanPhase) {
	g.mu.Lock()
	g.phase = to
	g.mu.Unlock()
}

// Reset returns the machine and all budgets to the initial graph phase.
func (g *GraphPlanState) Reset() {
	g.mu.Lock()
	g.phase = GraphPlanPhaseGraph
	g.counters = GraphPlanCounters{}
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
// update_plan). announce_plan is allowed only in the PLAN phase. This is the
// single source of truth for the runtime gate. The lists are additive: each
// phase includes everything the prior phases allowed.
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
