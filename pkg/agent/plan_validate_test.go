package agent

import (
	"strings"
	"testing"
)

func validLibStep(name string) PlanStepInfo {
	return PlanStepInfo{
		Name:        name,
		Description: "library change",
		Details:     "change the helper",
		Summary:     "Helper no longer panics on empty input",
		Outcome:     "go test of the helper package passes and documents the empty-input case",
		Files:       []PlanFileChange{{Path: "pkg/agent/plan_validate.go", Kind: "modify"}},
		Verify:      "go test ./pkg/agent/ -run TestValidateAnnouncedPlan",
		VerifyKind:  VerifyKindUnit,
	}
}

func validBehaviorStep(name, file, verify string) PlanStepInfo {
	return PlanStepInfo{
		Name:        name,
		Description: "runtime change",
		Details:     "implement the capability",
		Summary:     "The product does the thing the user asked for",
		Outcome:     "A running session container exists and docker inspect succeeds",
		Files:       []PlanFileChange{{Path: file, Kind: "modify"}},
		Verify:      verify,
		VerifyKind:  VerifyKindBehavior,
	}
}

func validDoc(t *testing.T) PlanDocumentInfo {
	t.Helper()
	return PlanDocumentInfo{
		Context:      "THE PROBLEM: users cannot X. THE APPROACH: do Y. BOUNDARIES: leave Z alone.",
		WhatNotToDo:  "Do not change the Backend interface signatures.",
		Verification: "docker inspect astonish-session-test && go test ./pkg/sandbox/docker/...",
	}
}

func TestPathTouchesRunningSurface(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"pkg/agent/plan.go", false},
		{"pkg/sandbox/docker/backend.go", true},
		{"./pkg/api/run_handler.go", true},
		{"/Users/x/Projects/astonish/pkg/launcher/chat_factory.go", true},
		{"cmd/astonish/sandbox.go", true},
		{"web/src/App.tsx", true},
		{"docs/architecture/sandbox.md", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := PathTouchesRunningSurface(tc.path); got != tc.want {
			t.Errorf("PathTouchesRunningSurface(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestValidateAnnouncedPlan_AcceptsLibraryUnitPhase(t *testing.T) {
	if msg := ValidateAnnouncedPlan(validDoc(t), []PlanStepInfo{validLibStep("fix-helper")}); msg != "" {
		t.Fatalf("valid library plan rejected: %s", msg)
	}
}

func TestValidateAnnouncedPlan_AcceptsBehaviorSlice(t *testing.T) {
	steps := []PlanStepInfo{
		validBehaviorStep("create-session", "pkg/sandbox/docker/session.go", "docker inspect astonish-session-test"),
		validBehaviorStep("delete-incus", "pkg/sandbox/incus_backend.go", "grep -r pkg/sandbox/incus --include='*.go' . | wc -l"),
	}
	steps[1].Files = []PlanFileChange{{Path: "pkg/sandbox/incus_backend.go", Kind: "delete"}}
	if msg := ValidateAnnouncedPlan(validDoc(t), steps); msg != "" {
		t.Fatalf("valid behavior slice rejected: %s", msg)
	}
}

func TestValidateAnnouncedPlan_RejectsMissingContract(t *testing.T) {
	doc := PlanDocumentInfo{}
	steps := []PlanStepInfo{{Name: "a", Description: "d"}}
	msg := ValidateAnnouncedPlan(doc, steps)
	if msg == "" {
		t.Fatal("expected rejection")
	}
	for _, want := range []string{"context", "what_not_to_do", "verification", "details", "files", "summary", "outcome", "verify", "verify_kind"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "slice by user-visible capability") {
		t.Errorf("message should tell the model how to split, got %s", msg)
	}
}

func TestValidateAnnouncedPlan_RejectsUnitOnRunningSurface(t *testing.T) {
	step := validLibStep("wire-docker")
	step.Files = []PlanFileChange{{Path: "pkg/sandbox/docker/backend.go", Kind: "modify"}}
	msg := ValidateAnnouncedPlan(validDoc(t), []PlanStepInfo{step})
	if msg == "" || !strings.Contains(msg, "behavior") {
		t.Fatalf("want running-surface unit rejection, got %q", msg)
	}
}

func TestValidateAnnouncedPlan_RejectsOversizedUnitPhase(t *testing.T) {
	step := validLibStep("too-big")
	step.Files = nil
	for i := 0; i < MaxUnitPhaseFiles+1; i++ {
		step.Files = append(step.Files, PlanFileChange{Path: "pkg/agent/file" + strings.Repeat("x", i) + ".go", Kind: "modify"})
	}
	msg := ValidateAnnouncedPlan(validDoc(t), []PlanStepInfo{step})
	if msg == "" || !strings.Contains(msg, "files") {
		t.Fatalf("want oversized unit rejection, got %q", msg)
	}
}

func TestValidateAnnouncedPlan_RejectsSharedSerialVerify(t *testing.T) {
	a := validLibStep("one")
	b := validLibStep("two")
	b.Files = []PlanFileChange{{Path: "pkg/agent/plan_document.go", Kind: "modify"}}
	b.Verify = a.Verify
	msg := ValidateAnnouncedPlan(validDoc(t), []PlanStepInfo{a, b})
	if msg == "" || !strings.Contains(msg, "share the same verify") {
		t.Fatalf("want shared verify rejection, got %q", msg)
	}
}

func TestValidateAnnouncedPlan_RejectsParallelFileOverlap(t *testing.T) {
	a := validLibStep("wave-a")
	a.ParallelGroup = "wave-1"
	b := validLibStep("wave-b")
	b.ParallelGroup = "wave-1"
	b.Files = []PlanFileChange{{Path: a.Files[0].Path, Kind: "modify"}}
	b.Verify = "go test ./pkg/agent/ -run TestB"
	msg := ValidateAnnouncedPlan(validDoc(t), []PlanStepInfo{a, b})
	if msg == "" || !strings.Contains(msg, "parallel_group") {
		t.Fatalf("want parallel file overlap rejection, got %q", msg)
	}
}

func TestValidateAnnouncedPlan_RejectsDeleteWithoutPriorBehavior(t *testing.T) {
	del := validBehaviorStep("delete-incus", "pkg/sandbox/incus_backend.go", "test ! -e pkg/sandbox/incus")
	del.Files = []PlanFileChange{{Path: "pkg/sandbox/incus_backend.go", Kind: "delete"}}
	msg := ValidateAnnouncedPlan(validDoc(t), []PlanStepInfo{del})
	if msg == "" || !strings.Contains(msg, "earlier phase") {
		t.Fatalf("want delete-first rejection, got %q", msg)
	}
}

func TestAcceptanceCoveredByVerification(t *testing.T) {
	if !AcceptanceCoveredByVerification("docker inspect foo && go test", "docker inspect foo") {
		t.Fatal("expected coverage")
	}
	if AcceptanceCoveredByVerification("go test ./...", "docker inspect foo") {
		t.Fatal("did not expect coverage")
	}
	if !AcceptanceCoveredByVerification("anything", "") {
		t.Fatal("empty acceptance is covered")
	}
}

func TestGraphPlanAnnounceMessage(t *testing.T) {
	if msg := GraphPlanAnnounceMessage(false, "go test", ""); msg != "" {
		t.Fatalf("non-graph mode should pass, got %q", msg)
	}
	if msg := GraphPlanAnnounceMessage(true, "docker inspect foo", ""); msg == "" || !strings.Contains(msg, "gplan_finalize") {
		t.Fatalf("missing acceptance: %q", msg)
	}
	if msg := GraphPlanAnnounceMessage(true, "go test ./...", "docker inspect foo"); msg == "" || !strings.Contains(msg, "docker inspect foo") {
		t.Fatalf("verification must include acceptance: %q", msg)
	}
	if msg := GraphPlanAnnounceMessage(true, "docker inspect foo && go test", "docker inspect foo"); msg != "" {
		t.Fatalf("covered verification rejected: %q", msg)
	}
}
