package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func revisionFixture() PlanSnapshot {
	return PlanSnapshot{PlanID: "plan-1", Revision: 3, Sequence: 8, Goal: "Deliver capability", Document: PlanDocumentInfo{Context: "design", WhatNotToDo: "boundaries", Verification: "acceptance"}, Steps: []PlanStepInfo{
		{Name: "one", Description: "first", Summary: "summary", Details: "details", Outcome: "works", Files: []PlanFileChange{{Path: "pkg/lib/a.go", Kind: "modify"}}, Verify: "go test ./pkg/lib -run One", VerifyKind: VerifyKindUnit, Status: "complete", Evidence: "exit 0"},
		{Name: "two", Description: "second", Summary: "summary", Details: "details", Outcome: "works too", Files: []PlanFileChange{{Path: "pkg/lib/b.go", Kind: "new"}}, Verify: "go test ./pkg/lib -run Two", VerifyKind: VerifyKindUnit, Status: "running"},
	}}
}
func revisionRequest(s PlanSnapshot) PlanRevisionRequest {
	return PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "new information", Steps: s.Definition().Steps}
}
func runtimeFixture() PlanSnapshot {
	s := revisionFixture()
	s.Document.Lifecycle = PlanLifecycleApproved
	for i := range s.Steps {
		s.Steps[i].Status = "complete"
		s.Steps[i].Evidence = "exit 0"
	}
	return s
}
func TestPlanRuntimeScopeRevisionPersistenceFailure(t *testing.T) {
	s := runtimeFixture()
	fail := true
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CommitSnapshot: func(PlanSnapshot) error {
		if fail {
			return errors.New("save failed")
		}
		return nil
	}})
	req := revisionRequest(s)
	req.RequiresApproval = true
	req.Steps[0].Outcome = "new outcome"
	before := r.Snapshot()
	if _, err := r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("failed revision changed accepted state")
	}
	fail = false
	got, err := r.Revise(req)
	if err != nil || got.Kind != PlanRevisionResultApplied || got.Snapshot.PendingRevision != nil {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestPlanRuntimeBootstrapAndSnapshotCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "PLAN.md")
	r := NewPlanRuntime(nil, PlanRuntimeOptions{CommitSnapshot: AtomicPlanSnapshotFile(path)})
	s := runtimeFixture()
	s.PlanID = ""
	s.Revision = 0
	s.Sequence = 0
	if err := r.CommitSnapshot(s); err != nil {
		t.Fatal(err)
	}
	initial := r.Snapshot()
	if initial.PlanID == "" || initial.Revision != 1 || initial.Sequence != 1 {
		t.Fatalf("bootstrap: %+v", initial)
	}
	if err := r.CommitSnapshot(initial); err != nil {
		t.Fatal(err)
	}
	if err := r.CommitSnapshot(initial); PlanRuntimeErrorCode(err) != PlanRuntimeStale {
		t.Fatalf("stale CAS: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, goal, steps, err := ParsePlanDocument(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	restored := NewPlanRuntime(NewPlanState(goal, doc, steps), PlanRuntimeOptions{})
	if restored.Snapshot().PlanID != initial.PlanID || restored.Snapshot().Sequence != 2 {
		t.Fatal("identity lost on restart")
	}
}
func TestChatAgent_PlanRuntimeAdapterPersistence(t *testing.T) {
	c := &ChatAgent{PlanVerify: func(string) (int, string, error) { return 0, "verified", nil }}
	c.SetPlanFilePath(filepath.Join(t.TempDir(), "PLAN.md"))
	s := runtimeFixture()
	s.Steps[0].Status = "pending"
	s.Steps[0].Evidence = ""
	c.SetActivePlan(NewPlanState(s.Goal, s.Document, s.Steps))
	r, err := c.EnsurePlanRuntime()
	if err != nil {
		t.Fatal(err)
	}
	c.MarkActivePlanApproved()
	if result := c.ApplyPlanStepUpdate("one", "complete"); result.Code != PlanStepOK {
		t.Fatal(result)
	}
	if result := c.AnnounceCompletion("done", ""); result.Code != PlanCompletionOK {
		t.Fatal(result)
	}
	if r.Snapshot().Document.Results == "" {
		t.Fatal("adapter did not use runtime")
	}
}

func TestPlanRuntimeRejectsStaleMissingCompletedAndDelegates(t *testing.T) {
	if _, err := NewPlanRuntime(nil, PlanRuntimeOptions{}).Revise(PlanRevisionRequest{}); PlanRuntimeErrorCode(err) != PlanRuntimeNoPlan {
		t.Fatal(err)
	}
	s := runtimeFixture()
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{})
	req := revisionRequest(s)
	req.BaseRevision--
	if _, err := r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimeStale {
		t.Fatal(err)
	}
	req = revisionRequest(s)
	req.Steps[0].Details = "new implementation"
	r.plan.mu.Lock()
	r.plan.taskRegistry["one"] = map[string]bool{"worker": true}
	r.plan.mu.Unlock()
	if _, err := r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
		t.Fatal(err)
	}
	s.Document.Results = "already complete"
	r = NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{})
	if _, err := r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimeCompleted {
		t.Fatal(err)
	}
}

func TestPlanRuntimeAtomicFailure(t *testing.T) {
	s := runtimeFixture()
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CommitSnapshot: func(candidate PlanSnapshot) error {
		candidate.Steps[0].Files[0].Path = "corrupt"
		return errors.New("disk full")
	}})
	before := r.Snapshot()
	req := revisionRequest(before)
	req.Steps[0].Details = "new implementation"
	if _, err := r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatalf("error %v", err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("failed persistence changed memory")
	}
	if got := r.UpdateStep("one", "failed"); got.Code != PlanRuntimePersistFailed {
		t.Fatal(got)
	}
	if got := r.Complete("done", ""); got.Code != PlanRuntimePersistFailed {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("failed status/completion changed memory")
	}
}
func TestPlanRuntimeInvalidationAndBusy(t *testing.T) {
	s := runtimeFixture()
	s.Steps[0].ParallelGroup = "wave"
	s.Steps[1].ParallelGroup = "wave"
	third := s.Steps[1]
	third.Name = "three"
	third.ParallelGroup = ""
	third.Files = []PlanFileChange{{Path: "pkg/lib/c.go", Kind: "modify"}}
	third.Verify = "go test ./pkg/lib -run Three"
	s.Steps = append(s.Steps, third)
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{})
	req := revisionRequest(s)
	req.Steps[0].Details = "changed"
	got, err := r.Revise(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot.Steps[0].Status != "pending" || got.Snapshot.Steps[0].Evidence != "" || got.Snapshot.Steps[1].Evidence != "exit 0" || got.Snapshot.Steps[2].Evidence != "" {
		t.Fatalf("invalidation: %+v", got.Snapshot.Steps)
	}
	s = runtimeFixture()
	r = NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{})
	r.plan.SetStepStatus("two", "running")
	req = revisionRequest(s)
	req.Steps[0].Details = "changed"
	if _, err = r.Revise(req); PlanRuntimeErrorCode(err) != PlanRuntimeBusy {
		t.Fatalf("expected busy: %v", err)
	}
	req = revisionRequest(s)
	req.Steps[0].Description = "clearer"
	got, err = r.Revise(req)
	if err != nil || got.Snapshot.Steps[0].Evidence != "exit 0" {
		t.Fatalf("description discarded evidence: %+v %v", got, err)
	}
}
func TestPlanRuntimeLegacyPendingMigration(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			s := runtimeFixture()
			if !approved {
				s.Document.Lifecycle = ""
			}
			s.PendingRevision = &PlanRevisionProposal{ID: "obsolete", BaseRevision: s.Revision, Candidate: s.Definition()}
			s.PendingRevision.Candidate.Goal = "OBSOLETE CANDIDATE"
			path := filepath.Join(t.TempDir(), "PLAN.md")
			fail := true
			r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CommitSnapshot: func(next PlanSnapshot) error {
				if fail {
					return errors.New("disk full")
				}
				return AtomicPlanSnapshotFile(path)(next)
			}})
			before := r.Snapshot()
			if err := r.Bootstrap(); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, r.Snapshot()) {
				t.Fatal("failed migration changed state")
			}
			fail = false
			if err := r.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			got := r.Snapshot()
			if got.PendingRevision != nil || got.Document.PendingRevision != nil || got.Revision != s.Revision || got.Sequence != s.Sequence+1 || got.Document.Lifecycle != s.Document.Lifecycle || !reflect.DeepEqual(got.Definition(), s.Definition()) {
				t.Fatalf("migration changed accepted plan: %+v", got)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, goal, steps, err := ParsePlanDocument(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			restored := NewPlanRuntime(NewPlanState(goal, doc, steps), PlanRuntimeOptions{})
			if err := restored.Bootstrap(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, restored.Snapshot()) {
				t.Fatal("migration not durable or not idempotent")
			}
			for _, decision := range []PlanRevisionDecision{PlanRevisionApprove, PlanRevisionReject, PlanRevisionRequestChanges} {
				if _, err := restored.ResolveRevision(s.PlanID, "obsolete", s.Revision, decision); PlanRuntimeErrorCode(err) != PlanRuntimeRevisionObsolete {
					t.Fatal(err)
				}
			}
			if approved {
				completion := NewPlanRuntimeFromSnapshot(got, PlanRuntimeOptions{Verify: func(string) (int, string, error) { t.Fatal("completion reran verification"); return 0, "", nil }}).Complete("restored evidence", "")
				if completion.Code != PlanCompletionOK {
					t.Fatalf("migration blocked completion: %+v", completion)
				}
			}
			// Migration leaves revisions and status operations usable without consent
			// to the obsolete candidate, but never substitutes for initial approval.
			req := revisionRequest(restored.Snapshot())
			req.Steps[1].Details += "; revised after migration"
			if result, err := restored.Revise(req); err != nil || result.Kind != PlanRevisionResultApplied {
				t.Fatalf("migration blocked fresh revision: %+v %v", result, err)
			}
			status := restored.UpdateStep("two", "running")
			if approved && status.Code != PlanStepOK || !approved && status.Code != PlanRuntimeUnapproved {
				t.Fatalf("migration broke initial-consent/status contract: %+v", status)
			}
		})
	}
}
func TestPlanRuntimeImmediateRevisionPreservesInflightEvidence(t *testing.T) {
	s := runtimeFixture()
	s.Steps[0].Status = "pending"
	s.Steps[0].Evidence = ""
	s.Steps[0].ParallelGroup = "wave"
	s.Steps[1].ParallelGroup = "wave"
	started, release := make(chan struct{}), make(chan struct{})
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { close(started); <-release; return 0, "verified", nil }})
	result := make(chan PlanStepApplyResult)
	go func() { result <- r.UpdateStep("one", "complete") }()
	<-started
	if got := r.UpdateStep("one", "complete"); got.Code != PlanRuntimeBusy {
		t.Fatal(got)
	}
	req := revisionRequest(r.Snapshot())
	req.Steps[1].Outcome = "new independent outcome"
	if _, err := r.Revise(req); err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := <-result; got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if !passingPlanEvidence(r.Snapshot().Steps[0].Evidence) {
		t.Fatal("inflight evidence lost")
	}
}
func TestPlanRuntimeStaleEvidence(t *testing.T) {
	s := runtimeFixture()
	s.Steps[0].Status = "pending"
	s.Steps[0].Evidence = ""
	started, release := make(chan struct{}), make(chan struct{})
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { close(started); <-release; return 0, "stale", nil }})
	result := make(chan PlanStepApplyResult)
	go func() { result <- r.UpdateStep("one", "complete") }()
	<-started
	r.plan.SetStepStatus("one", "failed")
	r.plan.SetStepStatus("one", "pending")
	close(release)
	if got := <-result; got.Code != PlanRuntimeStale {
		t.Fatal(got)
	}
	if r.Snapshot().Steps[0].Evidence != "" {
		t.Fatal("stale evidence saved")
	}
}
func TestPlanRuntimeConcurrentCompletionAndDeepCopy(t *testing.T) {
	r := NewPlanRuntimeFromSnapshot(runtimeFixture(), PlanRuntimeOptions{})
	copy := r.Snapshot()
	copy.Steps[0].Files[0].Path = "mutated"
	if r.Snapshot().Steps[0].Files[0].Path == "mutated" {
		t.Fatal("snapshot aliases state")
	}
	var wg sync.WaitGroup
	results := make(chan PlanCompletionResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- r.Complete("done", "") }()
	}
	wg.Wait()
	close(results)
	success := 0
	for result := range results {
		if result.Code == PlanCompletionOK {
			success++
		} else if result.Code != PlanRuntimeCompleted {
			t.Fatal(result)
		}
	}
	if success != 1 {
		t.Fatalf("completion applied %d times", success)
	}
	c := &ChatAgent{activePlanRuntime: r}
	ctx := WithPlanRuntime(context.Background(), nil)
	if value, bound := PlanRuntimeFromContext(ctx); value != nil || !bound || c.RuntimeForContext(ctx) != nil {
		t.Fatal("nil binding fell back")
	}
}
func TestPlanRuntimeLegacyIdentityAssignedOnce(t *testing.T) {
	s := runtimeFixture()
	s.PlanID = ""
	s.Revision = 0
	s.Sequence = 0
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{})
	if err := r.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	first := r.Snapshot()
	if first.PlanID == "" || first.Revision != 1 {
		t.Fatalf("missing identity: %+v", first)
	}
	if err := r.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, r.Snapshot()) {
		t.Fatal("bootstrap assigned identity twice")
	}
}

func TestPlanRevisionClassification(t *testing.T) {
	cases := []struct {
		name string
		edit func(*PlanRevisionRequest)
		want PlanRevisionClassification
	}{
		{"noop", func(r *PlanRevisionRequest) {}, PlanRevisionNoop},
		{"canonical noop", func(r *PlanRevisionRequest) {
			r.Steps[0].Files[0].Path = "/workspace/pkg/lib/./a.go"
			r.Steps[0].Files[0].Kind = ""
		}, PlanRevisionNoop},
		{"description", func(r *PlanRevisionRequest) { r.Steps[0].Description = "clarified" }, PlanRevisionImmediate},
		{"summary", func(r *PlanRevisionRequest) { r.Steps[0].Summary = "clarified" }, PlanRevisionImmediate},
		{"details", func(r *PlanRevisionRequest) { r.Steps[0].Details = "clarified" }, PlanRevisionImmediate},
		{"verify", func(r *PlanRevisionRequest) { r.Steps[0].Verify = "go test ./pkg/lib -run Better" }, PlanRevisionImmediate},
		{"deprecated approval flag ignored", func(r *PlanRevisionRequest) { r.Steps[0].Details = "clarified"; r.RequiresApproval = true }, PlanRevisionImmediate},
		{"outcome", func(r *PlanRevisionRequest) { r.Steps[0].Outcome = "different" }, PlanRevisionImmediate},
		{"file", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "pkg/lib/c.go" }, PlanRevisionImmediate},
		{"kind", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Kind = "delete" }, PlanRevisionImmediate},
		{"verify kind", func(r *PlanRevisionRequest) { r.Steps[0].VerifyKind = VerifyKindBehavior }, PlanRevisionImmediate},
		{"order", func(r *PlanRevisionRequest) { r.Steps[0], r.Steps[1] = r.Steps[1], r.Steps[0] }, PlanRevisionImmediate},
		{"name", func(r *PlanRevisionRequest) { r.Steps[0].Name = "renamed" }, PlanRevisionImmediate},
		{"group", func(r *PlanRevisionRequest) { r.Steps[0].ParallelGroup = "wave" }, PlanRevisionImmediate},
		{"remove", func(r *PlanRevisionRequest) { r.Steps = r.Steps[:1] }, PlanRevisionImmediate},
		{"goal", func(r *PlanRevisionRequest) { g := "new goal"; r.Goal = &g }, PlanRevisionImmediate},
		{"document", func(r *PlanRevisionRequest) {
			d := revisionFixture().Document.Definition()
			d.Context = "new design"
			r.Document = &d
		}, PlanRevisionImmediate},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := revisionFixture()
			before := s.Clone()
			r := revisionRequest(s)
			tt.edit(&r)
			got, err := BuildPlanRevisionCandidate(s, r, "/workspace", "")
			if err != nil {
				t.Fatal(err)
			}
			if got.Classification != tt.want {
				t.Fatalf("got %s want %s", got.Classification, tt.want)
			}
			if !reflect.DeepEqual(s, before) {
				t.Fatal("mutated snapshot")
			}
			if got.Definition.Steps[0].StepInfo().Evidence != "" || got.Definition.Steps[0].StepInfo().Status != "" {
				t.Fatal("runtime data entered definition")
			}
		})
	}
}
func TestPlanRevisionValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*PlanRevisionRequest)
	}{
		{"identity", func(r *PlanRevisionRequest) { r.PlanID = "other" }},
		{"stale", func(r *PlanRevisionRequest) { r.BaseRevision-- }},
		{"reason", func(r *PlanRevisionRequest) { r.Reason = " " }},
		{"empty", func(r *PlanRevisionRequest) { r.Steps = nil }},
		{"duplicate name", func(r *PlanRevisionRequest) { r.Steps[1].Name = r.Steps[0].Name }},
		{"bad name", func(r *PlanRevisionRequest) { r.Steps[0].Name = "\n" }},
		{"duplicate path", func(r *PlanRevisionRequest) {
			r.Steps[0].Files = append(r.Steps[0].Files, PlanFileChange{Path: "/workspace/pkg/lib/a.go"})
		}},
		{"empty path", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = " " }},
		{"newline path", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "a\nb" }},
		{"root", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "." }},
		{"malformed path", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "a\x00b" }},
		{"bad kind", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Kind = "rename" }},
		{"missing details", func(r *PlanRevisionRequest) { r.Steps[0].Details = "" }},
		{"missing summary", func(r *PlanRevisionRequest) { r.Steps[0].Summary = "" }},
		{"missing outcome", func(r *PlanRevisionRequest) { r.Steps[0].Outcome = "" }},
		{"missing verify", func(r *PlanRevisionRequest) { r.Steps[0].Verify = "" }},
		{"bad verify kind", func(r *PlanRevisionRequest) { r.Steps[0].VerifyKind = "bad" }},
		{"running surface unit", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "cmd/main.go" }},
		{"delete surface without replacement", func(r *PlanRevisionRequest) {
			r.Steps[0].Files[0] = PlanFileChange{Path: "cmd/main.go", Kind: "delete"}
			r.Steps[0].VerifyKind = VerifyKindBehavior
		}},
		{"parallel overlap", func(r *PlanRevisionRequest) {
			r.Steps[0].ParallelGroup = "wave"
			r.Steps[1].ParallelGroup = "wave"
			r.Steps[1].Files[0].Path = "/workspace/pkg/lib/a.go"
		}},
		{"serial same verify", func(r *PlanRevisionRequest) { r.Steps[1].Verify = r.Steps[0].Verify }},
		{"document incomplete", func(r *PlanRevisionRequest) { r.Document = &PlanDocumentDefinition{Context: "changed"} }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := revisionFixture()
			r := revisionRequest(s)
			tt.edit(&r)
			if _, err := BuildPlanRevisionCandidate(s, r, "/workspace", ""); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	t.Run("legacy untouched", func(t *testing.T) {
		s := revisionFixture()
		s.Steps[0] = PlanStepInfo{Name: "one"}
		r := revisionRequest(s)
		r.Steps[1].Details = "clarify"
		if _, err := BuildPlanRevisionCandidate(s, r, "/workspace", ""); err != nil {
			t.Fatal(err)
		}
		r.Steps[0].Description = "changed"
		if _, err := BuildPlanRevisionCandidate(s, r, "/workspace", ""); err == nil {
			t.Fatal("incomplete changed legacy accepted")
		}
	})
	t.Run("graph acceptance", func(t *testing.T) {
		s := revisionFixture()
		s.Document.GraphAcceptance = "acceptance"
		r := revisionRequest(s)
		d := s.Document.Definition()
		d.Verification = "other"
		r.Document = &d
		if _, err := BuildPlanRevisionCandidate(s, r, "/workspace", ""); err != nil {
			t.Fatal("revised acceptance rejected", err)
		}
	})
	t.Run("delete after behavior", func(t *testing.T) {
		s := revisionFixture()
		r := revisionRequest(s)
		r.Steps[0].VerifyKind = VerifyKindBehavior
		r.Steps[1].VerifyKind = VerifyKindBehavior
		r.Steps[1].Files[0] = PlanFileChange{Path: "cmd/old.go", Kind: "delete"}
		if _, err := BuildPlanRevisionCandidate(s, r, "/workspace", ""); err != nil {
			t.Fatal(err)
		}
	})
}
func TestPlanSnapshotDeepCopy(t *testing.T) {
	s := revisionFixture()
	p := &PlanRevisionProposal{ID: "proposal", Candidate: s.Definition(), Diff: PlanRevisionDiff{ChangedSteps: []string{"one"}, AddedSteps: []string{"new"}, RemovedSteps: []string{"old"}}}
	s.PendingRevision = p
	s.Document.PendingRevision = p
	c := s.Clone()
	c.Steps[0].Files[0].Path = "changed"
	c.PendingRevision.Candidate.Steps[0].Files[0].Path = "changed"
	c.PendingRevision.Diff.ChangedSteps[0] = "changed"
	c.PendingRevision.Diff.AddedSteps[0] = "changed"
	c.PendingRevision.Diff.RemovedSteps[0] = "changed"
	c.Document.PendingRevision.Candidate.Steps[0].Name = "changed"
	if s.Steps[0].Files[0].Path == "changed" || s.PendingRevision.Candidate.Steps[0].Files[0].Path == "changed" || s.PendingRevision.Diff.ChangedSteps[0] == "changed" || s.PendingRevision.Diff.AddedSteps[0] == "changed" || s.PendingRevision.Diff.RemovedSteps[0] == "changed" || s.Document.PendingRevision.Candidate.Steps[0].Name == "changed" {
		t.Fatal("clone aliases source")
	}
}
func TestPlanDocumentRevisionRoundTrip(t *testing.T) {
	s := revisionFixture()
	s.Document.PlanID = s.PlanID
	s.Document.Revision = s.Revision
	s.Document.Sequence = s.Sequence
	s.Document.RevisionNote = "Approved clarification"
	s.Document.GraphAcceptance = "acceptance"
	s.Document.Lifecycle = PlanLifecycleExecuting
	s.Document.PendingRevision = &PlanRevisionProposal{ID: "proposal-1", BaseRevision: s.Revision, Reason: "broader scope", Candidate: s.Definition(), Diff: PlanRevisionDiff{ChangedSteps: []string{"one"}}}
	md := RenderPlanFromInfoWithDoc(s.Goal, s.Document, s.Steps)
	if !strings.Contains(md, "## Plan Metadata\n\n```json") || !strings.Contains(md, "Pending proposal proposal-1") {
		t.Fatal(md)
	}
	doc, goal, steps, err := ParsePlanDocument(md)
	if err != nil {
		t.Fatal(err)
	}
	if goal != s.Goal || !reflect.DeepEqual(doc, s.Document) || !reflect.DeepEqual(steps, s.Steps) {
		t.Fatalf("roundtrip mismatch doc=%+v steps=%+v", doc, steps)
	}
	if _, _, _, err := ParsePlanDocument(strings.Replace(md, `"revision": 3`, `"revision": "invalid"`, 1)); err == nil {
		t.Fatal("invalid metadata accepted")
	}
}
func TestPlanDocumentLegacyRevisionDefaults(t *testing.T) {
	doc, _, _, err := ParsePlanDocument("# Execution Plan\n\n**Goal:** legacy\n\n## Phases\n\n- [x] **one** — old plan\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.PlanID != "" || doc.Revision != 0 || doc.Sequence != 0 || doc.PendingRevision != nil {
		t.Fatalf("legacy metadata not zero: %+v", doc)
	}
}

func TestRestoreApprovedPlanDiscardsLegacyPendingWithoutConsent(t *testing.T) {
	for _, lifecycle := range []string{"", PlanLifecycleApproved} {
		t.Run(lifecycle, func(t *testing.T) {
			s := runtimeFixture()
			s.Document.Lifecycle = lifecycle
			s.PendingRevision = &PlanRevisionProposal{ID: "old", Candidate: PlanDefinition{Goal: "obsolete"}}
			path := filepath.Join(t.TempDir(), "PLAN.md")
			if err := AtomicPlanSnapshotFile(path)(s); err != nil {
				t.Fatal(err)
			}
			c := &ChatAgent{}
			c.SetPlanFilePath(path)
			if err := c.RestoreApprovedPlan(); err != nil {
				t.Fatal(err)
			}
			if c.IsActivePlanApproved() != (lifecycle != "") {
				t.Fatal("restore changed initial approval")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, goal, _, err := ParsePlanDocument(string(raw))
			if err != nil || doc.PendingRevision != nil || goal != s.Goal || doc.Lifecycle != lifecycle {
				t.Fatalf("restore failed migration: %+v %v", doc, err)
			}
		})
	}
}
