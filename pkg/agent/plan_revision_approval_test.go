package agent

import (
	"errors"
	"reflect"
	"testing"
)

func TestPlanningRevisionApprovalIsAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := runtimeFixture()
		commits := 0
		r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{Verify: func(string) (int, string, error) { t.Fatal("revision verified"); return 0, "", nil }, CommitSnapshot: func(next PlanSnapshot) error {
			commits++
			if next.Document.Lifecycle != "" {
				t.Fatal("approval not reset atomically")
			}
			if fail {
				return errors.New("disk full")
			}
			return nil
		}})
		before := r.Snapshot()
		req := revisionRequest(s)
		req.Steps[0].Details = "changed"
		got, err := r.ReviseForPlanning(req)
		if fail {
			if PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed || !reflect.DeepEqual(r.Snapshot(), before) {
				t.Fatalf("failed revision mutated authority: %v", err)
			}
			continue
		}
		if err != nil || commits != 1 || got.Snapshot.PlanID != s.PlanID || got.Snapshot.Revision != s.Revision+1 || got.Snapshot.Document.Lifecycle != "" {
			t.Fatalf("revision: %+v %v", got, err)
		}
		if result := r.UpdateStep("one", "complete"); result.Code != PlanRuntimeUnapproved {
			t.Fatalf("revised definition executed before approval: %+v", result)
		}
		noop := revisionRequest(r.Snapshot())
		if result, err := r.ReviseForPlanning(noop); err != nil || result.Kind != PlanRevisionResultNoop || commits != 1 {
			t.Fatalf("noop: %+v %v", result, err)
		}
	}
}
