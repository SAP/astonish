package agent

import (
	"sync"
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
	if got := g.ChargeExploration("codegraph_explore", nil); got != "" {
		t.Fatalf("initial charge blocked: %s", got)
	}
	g.Reset()
	if g.Phase() != GraphPlanPhaseGraph {
		t.Fatalf("after Reset phase = %q, want graph", g.Phase())
	}
	if got := g.Counters(); got != (GraphPlanCounters{}) {
		t.Fatalf("after Reset counters = %+v, want zero", got)
	}
}

func TestGraphPlanState_EnforcesGraphBudget(t *testing.T) {
	g := NewGraphPlanState()
	for i := 0; i < GraphPlanMaxGraphQueries; i++ {
		if got := g.ChargeExploration("codegraph_explore", nil); got != "" {
			t.Fatalf("graph call %d blocked early: %s", i+1, got)
		}
	}
	if got := g.ChargeExploration("codegraph_explore", nil); got == "" {
		t.Fatal("expected graph query over budget to be blocked")
	}
	if got := g.Counters(); got.GraphQueries != GraphPlanMaxGraphQueries || got.Total != GraphPlanMaxGraphQueries {
		t.Fatalf("rejected call must not be charged: %+v", got)
	}
}

func TestGraphPlanState_EnforcesDelegationTaskBudget(t *testing.T) {
	g := NewGraphPlanState()
	g.Advance(GraphPlanPhaseGap)
	tasks := make([]any, GraphPlanMaxDelegatedTasks+1)
	if got := g.ChargeExploration("delegate_tasks", map[string]any{"tasks": tasks}); got == "" {
		t.Fatal("expected oversized delegation to be blocked")
	}
	if got := g.Counters(); got != (GraphPlanCounters{}) {
		t.Fatalf("rejected delegation must not be charged: %+v", got)
	}
}

func TestGraphPlanState_ConcurrentCharging(t *testing.T) {
	g := NewGraphPlanState()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.ChargeExploration("codegraph_explore", nil)
		}()
	}
	wg.Wait()
	if got := g.Counters().GraphQueries; got != GraphPlanMaxGraphQueries {
		t.Fatalf("concurrent graph charges = %d, want %d", got, GraphPlanMaxGraphQueries)
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
