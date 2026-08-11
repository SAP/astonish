package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
)

func TestGraphPlanTransitionTools_AdvancePhases(t *testing.T) {
	orig := graphPlanAdvanceCallback
	defer func() { graphPlanAdvanceCallback = orig }()

	var got agent.GraphPlanPhase
	SetGraphPlanAdvanceCallback(func(to agent.GraphPlanPhase) error {
		got = to
		return nil
	})

	// gplan_reads → read phase. Use real files so path validation passes.
	if res, err := graphPlanReads(nil, GraphPlanReadsArgs{
		ReadList: []GraphPlanReadEntry{
			{Path: "graph_plan_tool.go", Reason: "def"},
			{Path: "graph_plan_tool_test.go"},
		},
	}); err != nil || res.Status != "ok" || res.Count != 2 {
		t.Fatalf("gplan_reads unexpected: %+v err=%v", res, err)
	}
	if got != agent.GraphPlanPhaseRead {
		t.Fatalf("gplan_reads advanced to %q, want read", got)
	}

	// gplan_gaps with gaps → gap phase.
	if res, err := graphPlanGaps(nil, GraphPlanGapsArgs{
		Gaps: []GraphPlanGapEntry{{Question: "q", WhyCodegraphInsufficient: "config"}},
	}); err != nil || res.Status != "ok" || res.Phase != string(agent.GraphPlanPhaseGap) {
		t.Fatalf("gplan_gaps unexpected: %+v err=%v", res, err)
	}
	if got != agent.GraphPlanPhaseGap {
		t.Fatalf("gplan_gaps advanced to %q, want gap", got)
	}

	// gplan_finalize → plan phase.
	if res, err := graphPlanFinalize(nil, GraphPlanFinalizeArgs{}); err != nil || res.Status != "ok" {
		t.Fatalf("gplan_finalize unexpected: %+v err=%v", res, err)
	}
	if got != agent.GraphPlanPhasePlan {
		t.Fatalf("gplan_finalize advanced to %q, want plan", got)
	}
}

func TestGraphPlanGaps_EmptySkipsToPlan(t *testing.T) {
	orig := graphPlanAdvanceCallback
	defer func() { graphPlanAdvanceCallback = orig }()

	var got agent.GraphPlanPhase
	SetGraphPlanAdvanceCallback(func(to agent.GraphPlanPhase) error {
		got = to
		return nil
	})

	// Empty gaps list means "no gaps" → skip straight to plan phase.
	res, err := graphPlanGaps(nil, GraphPlanGapsArgs{})
	if err != nil || res.Status != "ok" {
		t.Fatalf("gplan_gaps(empty) unexpected: %+v err=%v", res, err)
	}
	if got != agent.GraphPlanPhasePlan {
		t.Fatalf("empty gplan_gaps advanced to %q, want plan (skip)", got)
	}
}

func TestGraphPlanTools_NoActiveCallback(t *testing.T) {
	orig := graphPlanAdvanceCallback
	defer func() { graphPlanAdvanceCallback = orig }()
	graphPlanAdvanceCallback = nil

	res, err := graphPlanReads(nil, GraphPlanReadsArgs{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Status != "no_active_graph_plan" {
		t.Fatalf("expected no_active_graph_plan status, got %q", res.Status)
	}
}

func TestGraphPlanReads_RejectsMissingPaths(t *testing.T) {
	orig := graphPlanAdvanceCallback
	defer func() { graphPlanAdvanceCallback = orig }()

	advanced := false
	SetGraphPlanAdvanceCallback(func(to agent.GraphPlanPhase) error {
		advanced = true
		return nil
	})

	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.go")
	if err := os.WriteFile(realFile, []byte("package p"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fakePath := filepath.Join(dir, "does_not_exist.go")

	res, err := graphPlanReads(nil, GraphPlanReadsArgs{
		ReadList: []GraphPlanReadEntry{
			{Path: realFile, Reason: "exists"},
			{Path: fakePath, Reason: "guessed"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "bad_paths" {
		t.Errorf("status = %q, want bad_paths", res.Status)
	}
	if len(res.Missing) != 1 || res.Missing[0] != fakePath {
		t.Errorf("missing = %v, want [%s]", res.Missing, fakePath)
	}
	if advanced {
		t.Error("phase must NOT advance when paths are invalid")
	}
}

func TestGraphPlanReads_AcceptsValidPaths(t *testing.T) {
	orig := graphPlanAdvanceCallback
	defer func() { graphPlanAdvanceCallback = orig }()

	var got agent.GraphPlanPhase
	SetGraphPlanAdvanceCallback(func(to agent.GraphPlanPhase) error {
		got = to
		return nil
	})

	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	for _, f := range []string{fileA, fileB} {
		if err := os.WriteFile(f, []byte("package p"), 0600); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	res, err := graphPlanReads(nil, GraphPlanReadsArgs{
		ReadList: []GraphPlanReadEntry{
			{Path: fileA, Reason: "callers"},
			{Path: fileB + ":10-40", Reason: "region"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("status = %q, want ok; missing=%v", res.Status, res.Missing)
	}
	if res.Count != 2 {
		t.Errorf("count = %d, want 2", res.Count)
	}
	if got != agent.GraphPlanPhaseRead {
		t.Errorf("phase advanced to %q, want read", got)
	}
}

