package agent

import (
	"strings"
	"testing"
)

// graphPlanToolBlocked mirrors the runtime gate decision in chat_agent_run.go
// for Graph-Optimized Plan mode: transition tools + announce_plan/update_plan
// are always allowed; everything else must be permitted by the current phase's
// allow-list.
func graphPlanToolBlocked(name string, phase GraphPlanPhase) bool {
	if IsGraphPlanTransitionTool(name) || name == "announce_plan" || name == "update_plan" {
		return false
	}
	return !GraphPlanPhaseTools(phase)[name]
}

func TestGraphPlanGate_PhaseAllowList(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		phase   GraphPlanPhase
		blocked bool
	}{
		// find_files: allowed in graph phase (for path correction) and beyond.
		{"find_files in graph", "find_files", GraphPlanPhaseGraph, false},
		{"find_files in read", "find_files", GraphPlanPhaseRead, false},
		{"find_files in gap", "find_files", GraphPlanPhaseGap, false},

		// codegraph_explore is available in every phase.
		{"codegraph in graph", "codegraph_explore", GraphPlanPhaseGraph, false},
		{"codegraph in read", "codegraph_explore", GraphPlanPhaseRead, false},
		{"codegraph in gap", "codegraph_explore", GraphPlanPhaseGap, false},

		// read_file: blocked in graph, allowed from read onward.
		{"read_file in graph blocked", "read_file", GraphPlanPhaseGraph, true},
		{"read_file in read", "read_file", GraphPlanPhaseRead, false},
		{"read_file in gap", "read_file", GraphPlanPhaseGap, false},

		// grep_search: blocked in graph/read, allowed in gap.
		{"grep in graph blocked", "grep_search", GraphPlanPhaseGraph, true},
		{"grep in read blocked", "grep_search", GraphPlanPhaseRead, true},
		{"grep in gap", "grep_search", GraphPlanPhaseGap, false},

		// tree-sitter tools: gap only.
		{"code_references in read blocked", "code_references", GraphPlanPhaseRead, true},
		{"code_references in gap", "code_references", GraphPlanPhaseGap, false},

		// delegate_tasks: gap/plan only.
		{"delegate in graph blocked", "delegate_tasks", GraphPlanPhaseGraph, true},
		{"delegate in read blocked", "delegate_tasks", GraphPlanPhaseRead, true},
		{"delegate in gap", "delegate_tasks", GraphPlanPhaseGap, false},

		// write_file / mutators blocked in every phase.
		{"write_file in graph", "write_file", GraphPlanPhaseGraph, true},
		{"write_file in read", "write_file", GraphPlanPhaseRead, true},
		{"write_file in gap", "write_file", GraphPlanPhaseGap, true},
		{"write_file in plan", "write_file", GraphPlanPhasePlan, true},
		{"edit_file in gap", "edit_file", GraphPlanPhaseGap, true},
		{"shell_command in plan", "shell_command", GraphPlanPhasePlan, true},

		// transition tools always allowed.
		{"gplan_reads in graph", "gplan_reads", GraphPlanPhaseGraph, false},
		{"gplan_gaps in read", "gplan_gaps", GraphPlanPhaseRead, false},
		{"gplan_finalize in gap", "gplan_finalize", GraphPlanPhaseGap, false},

		// announce_plan always allowed (recording the plan).
		{"announce_plan in plan", "announce_plan", GraphPlanPhasePlan, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := graphPlanToolBlocked(tc.tool, tc.phase); got != tc.blocked {
				t.Errorf("graphPlanToolBlocked(%q, %q) = %v, want %v", tc.tool, tc.phase, got, tc.blocked)
			}
		})
	}
}

func TestGraphPlanPhaseTools_Additive(t *testing.T) {
	graph := GraphPlanPhaseTools(GraphPlanPhaseGraph)
	read := GraphPlanPhaseTools(GraphPlanPhaseRead)
	gap := GraphPlanPhaseTools(GraphPlanPhaseGap)

	// Each phase must include everything the prior phase allowed.
	for name := range graph {
		if !read[name] {
			t.Errorf("read phase should include graph-phase tool %q", name)
		}
	}
	for name := range read {
		if !gap[name] {
			t.Errorf("gap phase should include read-phase tool %q", name)
		}
	}
}

func TestGraphPlanBlockedMessage_PhaseAware(t *testing.T) {
	// Graph phase message should point to codegraph_explore and gplan_reads.
	msg := GraphPlanBlockedMessage("grep_search", GraphPlanPhaseGraph)
	for _, want := range []string{"grep_search", "codegraph_explore", "gplan_reads"} {
		if !strings.Contains(msg, want) {
			t.Errorf("graph-phase blocked message should mention %q, got %q", want, msg)
		}
	}
	// Read phase message should mention gplan_gaps / gplan_finalize.
	msg = GraphPlanBlockedMessage("grep_search", GraphPlanPhaseRead)
	if !strings.Contains(msg, "gplan_gaps") {
		t.Errorf("read-phase blocked message should mention gplan_gaps, got %q", msg)
	}
	// Mutator in gap/plan phase should say NO-CHANGES.
	msg = GraphPlanBlockedMessage("write_file", GraphPlanPhaseGap)
	if !strings.Contains(strings.ToUpper(msg), "NO-CHANGES") {
		t.Errorf("gap-phase mutator message should mention NO-CHANGES, got %q", msg)
	}
}

func TestGraphPlanModeSystemContext_Discipline(t *testing.T) {
	for _, want := range []string{
		"GRAPH-OPTIMIZED PLAN MODE",
		"codegraph_explore",
		"gplan_reads",
		"gplan_gaps",
		"gplan_finalize",
		"announce_plan",
		"NO-CHANGES",
	} {
		if !strings.Contains(GraphPlanModeSystemContext, want) {
			t.Errorf("GraphPlanModeSystemContext should mention %q", want)
		}
	}
}

func TestSafeTools_IncludesCodegraphAndTransitionTools(t *testing.T) {
	for _, name := range []string{
		"codegraph_explore",
		"gplan_reads",
		"gplan_gaps",
		"gplan_finalize",
	} {
		if !IsToolSafe(name) {
			t.Errorf("expected %q to be in SafeTools (read-only / phase-only, auto-approve)", name)
		}
	}
}
