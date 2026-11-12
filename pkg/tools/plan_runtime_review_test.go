package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
)

func reviewAnnounceArgs() AnnouncePlanArgs {
	return AnnouncePlanArgs{Goal: "capability", Context: "design", WhatNotToDo: "scope", Verification: "User runs the acceptance sequence", Steps: []PlanStepInput{{Name: "one", Description: "first", Summary: "summary", Details: "implementation", Outcome: "works", Files: []PlanFileChangeInput{{Path: "pkg/lib/a.go", Kind: "modify"}}, Verify: "go test ./pkg/lib", VerifyKind: agent.VerifyKindUnit}}}
}

func TestGraphAnnounceApproveRestoreReplacesAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "PLAN.md")
	r := agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{CommitSnapshot: agent.AtomicPlanSnapshotFile(path)})
	args := reviewAnnounceArgs()
	acceptance := args.Verification
	base := WithPlanCallbacks(context.Background(), PlanCallbacks{Acceptance: func() string { return acceptance }})
	ctx := scopedBasicContext{base: agent.WithPlanRuntime(agent.WithPromptOverrides(base, &agent.PromptOverrides{GraphPlanMode: true}), r)}
	result, err := announcePlan(ctx, args)
	if err != nil || result.Status != "ok" {
		t.Fatalf("%+v %v", result, err)
	}
	if r.Snapshot().Document.GraphAcceptance != acceptance {
		t.Fatal("announce lost graph acceptance")
	}
	if err := r.SetApproved(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, goal, steps, err := agent.ParsePlanDocument(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	restored := agent.NewPlanRuntime(agent.NewPlanState(goal, doc, steps), agent.PlanRuntimeOptions{})
	s := restored.Snapshot()
	definition := s.Definition().Document
	definition.Verification = "different acceptance"
	_, err = restored.Revise(agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "drop original acceptance", Document: &definition, Steps: s.Definition().Steps})
	if err != nil {
		t.Fatal("revision could not replace historical acceptance", err)
	}
	if restored.Snapshot().Document.GraphAcceptance != definition.Verification {
		t.Fatal("graph acceptance metadata not revised consistently")
	}
}

func TestScopedInitialPlanReplacementAuthorization(t *testing.T) {
	r := agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{})
	ctx := scopedBasicContext{base: agent.WithPlanRuntime(context.Background(), r)}
	args := reviewAnnounceArgs()
	if result, err := announcePlan(ctx, args); err != nil || result.Status != "ok" {
		t.Fatalf("%+v %v", result, err)
	}
	args.Goal = "replacement"
	if result, _ := announcePlan(ctx, args); result.Status != "blocked_active_approved_plan" {
		t.Fatal(result)
	}
	if err := r.AllowInitialPlanReplacement(); err != nil {
		t.Fatal(err)
	}
	if result, err := announcePlan(ctx, args); err != nil || result.Status != "ok" {
		t.Fatalf("%+v %v", result, err)
	}
	if result, _ := announcePlan(ctx, args); result.Status != "blocked_active_approved_plan" {
		t.Fatal(result)
	}
}

func TestScopedUnapprovedUpdateNeverVerifies(t *testing.T) {
	for _, po := range []*agent.PromptOverrides{{}, {PlanMode: true}, {GraphPlanMode: true}} {
		calls := 0
		r := agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{Verify: func(string) (int, string, error) { calls++; return 0, "", nil }})
		ctx := scopedBasicContext{base: agent.WithPlanRuntime(context.Background(), r)}
		if result, _ := announcePlan(ctx, reviewAnnounceArgs()); result.Status != "ok" {
			t.Fatal(result)
		}
		ctx.base = agent.WithPromptOverrides(ctx.base, po)
		for _, status := range []string{"running", "complete", "done"} {
			result, err := updatePlan(ctx, UpdatePlanArgs{Step: "one", Status: status})
			if err != nil || result.Status != agent.PlanRuntimeUnapproved {
				t.Fatalf("%+v %v", result, err)
			}
		}
		if calls != 0 {
			t.Fatalf("unapproved verifier ran %d times", calls)
		}
	}
}
