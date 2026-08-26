package agent

import (
	"testing"
)

func TestGraphPlanState_InitialPhase(t *testing.T) {
	g := NewGraphPlanState()
	if g.Phase() != GraphPlanPhaseGraph {
		t.Fatalf("new state phase = %q, want %q", g.Phase(), GraphPlanPhaseGraph)
	}
}

func TestGraphPlanState_Transitions(t *testing.T) {
	g := NewGraphPlanState()

	g.Advance(GraphPlanPhaseRead)
	if g.Phase() != GraphPlanPhaseRead {
		t.Fatalf("after Advance(read) phase = %q, want read", g.Phase())
	}

	g.Advance(GraphPlanPhaseGap)
	if g.Phase() != GraphPlanPhaseGap {
		t.Fatalf("after Advance(gap) phase = %q, want gap", g.Phase())
	}

	g.Advance(GraphPlanPhasePlan)
	if g.Phase() != GraphPlanPhasePlan {
		t.Fatalf("after Advance(plan) phase = %q, want plan", g.Phase())
	}
}

func TestGraphPlanState_GraphToGapSkip(t *testing.T) {
	// codegraph-uncovered repos skip straight from graph to gap.
	g := NewGraphPlanState()
	g.Advance(GraphPlanPhaseGap)
	if g.Phase() != GraphPlanPhaseGap {
		t.Fatalf("graph→gap skip failed: phase = %q, want gap", g.Phase())
	}
}

func TestGraphPlanState_Reset(t *testing.T) {
	g := NewGraphPlanState()
	g.Advance(GraphPlanPhasePlan)
	g.Reset()
	if g.Phase() != GraphPlanPhaseGraph {
		t.Fatalf("after Reset phase = %q, want graph", g.Phase())
	}
}

func TestGetOrCreateGraphPlanState_PerSession(t *testing.T) {
	c := &ChatAgent{}
	a := c.GetOrCreateGraphPlanState("session-a")
	if a == nil {
		t.Fatal("expected non-nil state")
	}
	// Same session returns the same object.
	if again := c.GetOrCreateGraphPlanState("session-a"); again != a {
		t.Fatal("GetOrCreateGraphPlanState should return the same instance per session")
	}
	// Different session is isolated.
	b := c.GetOrCreateGraphPlanState("session-b")
	if b == a {
		t.Fatal("different sessions must not share state")
	}
	a.Advance(GraphPlanPhaseGap)
	if b.Phase() != GraphPlanPhaseGraph {
		t.Fatal("advancing one session's state must not affect another")
	}
}

// TestGraphPlanPhaseTools_WebSearchAllowedFromGap locks in that the outbound
// read-only web tools (web_fetch and perplexity_web_search) are blocked in the
// narrow GRAPH and READ phases but unlocked from the GAP phase onward. web_fetch
// is asserted alongside perplexity_web_search so the two never diverge.
func TestGraphPlanPhaseTools_WebSearchAllowedFromGap(t *testing.T) {
	for _, name := range []string{"perplexity_web_search", "web_fetch"} {
		cases := []struct {
			phase GraphPlanPhase
			want  bool
		}{
			{GraphPlanPhaseGraph, false},
			{GraphPlanPhaseRead, false},
			{GraphPlanPhaseGap, true},
			{GraphPlanPhasePlan, true},
		}
		for _, tc := range cases {
			if got := GraphPlanPhaseTools(tc.phase)[name]; got != tc.want {
				t.Errorf("GraphPlanPhaseTools(%q)[%q] = %v, want %v", tc.phase, name, got, tc.want)
			}
		}
	}
}
