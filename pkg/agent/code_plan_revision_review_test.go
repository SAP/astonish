package agent

import (
	"errors"
	"reflect"
	"testing"
)

func revisionReviewFixture() PlanSnapshot {
	s := codeExecutionFixture(CodePlanReviewEachPhase)
	third := s.Steps[1]
	third.Name = "three"
	third.Verify = "printf three"
	s.Steps = append(s.Steps, third)
	for i := 0; i < 2; i++ {
		s.Steps[i].Status = "complete"
		s.Steps[i].Evidence = "exit 0"
	}
	s.Document.CodeReviewPending = "TWO"
	return s
}

func TestCodePlanRevisionReviewReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		edit       func(*PlanDefinition)
		checkpoint string
		executable string
	}{
		{"removed owner", func(d *PlanDefinition) { d.Steps = append(d.Steps[:1], d.Steps[2:]...) }, "", "three"},
		{"renamed owner", func(d *PlanDefinition) { d.Steps[1].Name = "replacement" }, "", "replacement"},
		{"inserted prerequisite", func(d *PlanDefinition) {
			p := d.Steps[0]
			p.Name = "inserted"
			p.Verify = "printf inserted"
			d.Steps = append([]PlanStepDefinition{p}, d.Steps...)
		}, "", "inserted"},
		{"invalidated prerequisite", func(d *PlanDefinition) { d.Steps[0].Verify = "printf corrected" }, "", "one"},
		{"owner correction", func(d *PlanDefinition) { d.Steps[1].Verify = "printf corrected" }, "TWO", "two"},
		{"later correction", func(d *PlanDefinition) { d.Steps[2].Verify = "printf corrected" }, "TWO", ""},
		{"owner presentation", func(d *PlanDefinition) { d.Steps[1].Summary = "Corrected explanation" }, "TWO", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := revisionReviewFixture()
			d := original.Definition()
			tc.edit(&d)
			r := NewPlanRuntimeFromSnapshot(original, PlanRuntimeOptions{CodeExecution: true})
			got, err := r.Revise(PlanRevisionRequest{PlanID: original.PlanID, BaseRevision: original.Revision, Reason: "Correct reviewed definition", Steps: d.Steps})
			if err != nil {
				t.Fatal(err)
			}
			if got.Snapshot.Document.CodeReviewPending != tc.checkpoint {
				t.Fatalf("checkpoint %q, want %q", got.Snapshot.Document.CodeReviewPending, tc.checkpoint)
			}
			// Reconstructed authority must have exactly the same admission boundaries.
			for _, restored := range []bool{false, true} {
				candidate := r
				if restored {
					candidate = NewPlanRuntimeFromSnapshot(got.Snapshot, PlanRuntimeOptions{CodeExecution: true})
				}
				if blocked := candidate.UpdateStep("three", "running"); tc.executable != "three" && blocked.Code != CodePlanReviewRequired {
					t.Fatalf("later phase bypass: %+v", blocked)
				}
				candidate = NewPlanRuntimeFromSnapshot(got.Snapshot, PlanRuntimeOptions{CodeExecution: true})
				if tc.executable == "" {
					if err := candidate.AdmitCodeDelegates([]SubAgentTask{{TaskID: "later", PlanStep: "three"}}); PlanRuntimeErrorCode(err) != CodePlanReviewRequired {
						t.Fatalf("delegate bypass: %v", err)
					}
					continue
				}
				if result := candidate.UpdateStep(tc.executable, "running"); result.Code != PlanStepOK {
					t.Fatalf("necessary correction blocked: %+v", result)
				}
				if err := candidate.AdmitCodeDelegates([]SubAgentTask{{TaskID: "correction", PlanStep: tc.executable}}); err != nil {
					t.Fatalf("correction delegate blocked: %v", err)
				}
			}
		})
	}
}

func TestCodePlanRevisionReviewSameWavePrerequisite(t *testing.T) {
	s := revisionReviewFixture()
	for i := range s.Steps {
		s.Steps[i].ParallelGroup = "wave"
	}
	d := s.Definition()
	d.Steps[0].Verify = "printf corrected"
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true})
	got, err := r.Revise(PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "Correct prerequisite", Steps: d.Steps})
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot.Document.CodeReviewPending != "" {
		t.Fatal("same-wave prerequisite stranded checkpoint")
	}
	if result := r.UpdateStep("two", "running"); result.Code != CodePlanReviewRequired {
		t.Fatal(result)
	}
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "later", PlanStep: "two"}}); PlanRuntimeErrorCode(err) != CodePlanReviewRequired {
		t.Fatal(err)
	}
	if result := r.UpdateStep("one", "running"); result.Code != PlanStepOK {
		t.Fatal(result)
	}
}

func TestCodePlanRevisionReviewAtomicFailures(t *testing.T) {
	for _, kind := range []string{"stale", "busy", "rejected", "noop", "persistence"} {
		t.Run(kind, func(t *testing.T) {
			s := revisionReviewFixture()
			if kind == "busy" {
				s.Steps[1].Status = "running"
			}
			commits := 0
			r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true, CommitSnapshot: func(PlanSnapshot) error { commits++; return errors.New("disk unavailable") }})
			before := r.Snapshot()
			d := before.Definition()
			d.Steps[1].Name = "replacement"
			req := PlanRevisionRequest{PlanID: before.PlanID, BaseRevision: before.Revision, Reason: "Correct owner", Steps: d.Steps}
			want := PlanRuntimePersistFailed
			switch kind {
			case "stale":
				req.BaseRevision--
				want = PlanRuntimeStale
			case "busy":
				want = PlanRuntimeBusy
			case "rejected":
				req.Reason = ""
				want = PlanRuntimeInvalid
			case "noop":
				req.Steps = before.Definition().Steps
			}
			result, err := r.Revise(req)
			if kind == "noop" {
				if err != nil || result.Kind != PlanRevisionResultNoop {
					t.Fatal(result, err)
				}
			} else if PlanRuntimeErrorCode(err) != want {
				t.Fatalf("error %v, want %s", err, want)
			}
			if !reflect.DeepEqual(before, r.Snapshot()) {
				t.Fatal("failed revision changed authority or evidence")
			}
			if kind != "persistence" && commits != 0 {
				t.Fatal("invalid revision persisted")
			}
		})
	}
}

func TestCodePlanRevisionReviewOtherModes(t *testing.T) {
	for _, mode := range []CodePlanExecutionMode{CodePlanRunToCompletion, ""} {
		s := revisionReviewFixture()
		s.Document.CodeExecutionMode = mode
		s.Document.CodeReviewPending = ""
		d := s.Definition()
		d.Steps[1].Name = "replacement"
		r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true})
		got, err := r.Revise(PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "Rename owner", Steps: d.Steps})
		if err != nil || got.Snapshot.Document.CodeExecutionMode != mode || got.Snapshot.Document.CodeReviewPending != "" {
			t.Fatal(got, err)
		}
		if result := r.UpdateStep("replacement", "running"); result.Code != PlanStepOK {
			t.Fatal(result)
		}
		if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "replacement", PlanStep: "replacement"}}); err != nil {
			t.Fatal(err)
		}
	}
}
