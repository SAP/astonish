package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

func progressSnapshot(statuses ...string) agent.PlanSnapshot {
	s := agent.PlanSnapshot{PlanID: "progress", Revision: 1, Sequence: 1, Goal: "Progress fixture",
		Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved}}
	for i, status := range statuses {
		s.Steps = append(s.Steps, agent.PlanStepInfo{Name: fmt.Sprintf("phase-%d", i+1), Description: fmt.Sprintf("Phase %d", i+1), Status: status})
	}
	return s
}

func projectSnapshot(s agent.PlanSnapshot) planProgressView {
	return latestPlanProgress([]events.Item{{Kind: events.ItemPlan, PlanStatus: events.PlanApproved, PlanSnapshot: &s}})
}

func TestPlanProgressLongUnicodeAndMalformedLatest(t *testing.T) {
	s := progressSnapshot("complete", "running", "future-status", "")
	s.Goal = strings.Repeat("日本語の計画 🧪 e\u0301 ", 100)
	for i := range s.Steps {
		s.Steps[i].Name = fmt.Sprintf("段階-%d", i)
		s.Steps[i].Description = strings.Repeat("長い説明 ", 100)
		s.Steps[i].Summary = strings.Repeat("詳細な要約 ", 100)
	}
	v := projectSnapshot(s)
	if v.Goal != s.Goal || len(v.Phases) != len(s.Steps) || v.Percent != 25 || v.Counts.Unknown != 1 || v.Counts.Pending != 1 {
		t.Fatalf("long projection lost data: %+v", v)
	}
	for i, p := range v.Phases {
		if p.Name != s.Steps[i].Name || p.Description != s.Steps[i].Description || p.Summary != s.Steps[i].Summary {
			t.Fatalf("phase %d was truncated in the authoritative projection", i)
		}
	}
	for _, content := range []string{"", "計画ではありません 🧪", strings.Repeat("not a plan\n", 1000)} {
		got := latestPlanProgress([]events.Item{
			{Kind: events.ItemPlan, PlanSnapshot: &s},
			{Kind: events.ItemPlan, Content: content},
		})
		if !got.Present || got.Available || got.PercentAvailable || got.State != planProgressUnavailable || len(got.Phases) != 0 || got.Goal != "" {
			t.Fatalf("malformed latest leaked old progress: %+v", got)
		}
	}
}

func TestPlanProgressRevisionLiveRestoreParity(t *testing.T) {
	tr := events.NewTranscript()
	original := progressSnapshot("complete", "complete", "complete")
	original.Document.Results = "Original evidence"
	first := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &original}
	tr.Apply(first)
	if got := latestPlanProgress(tr.Items); got.Percent != 100 || got.State != planProgressCompleted {
		t.Fatalf("original = %+v", got)
	}
	revised := original.Clone()
	revised.Revision++
	revised.Sequence++
	revised.Goal = "Revised acceptance"
	revised.Document.Results = ""
	revised.Steps[1].Name = "new-verification"
	revised.Steps[1].Description = "Revalidate changed scope"
	revised.Steps[1].Status, revised.Steps[2].Status = "pending", "pending"
	revised.Steps[1].Evidence, revised.Steps[2].Evidence = "", ""
	second := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &revised}
	tr.Apply(second)
	got := latestPlanProgress(tr.Items)
	if got.Percent != 33 || got.Revision != 2 || got.Sequence != 2 || got.State != planProgressIdleIncomplete || got.Phases[1].Name != "new-verification" || got.Goal != revised.Goal {
		t.Fatalf("revision failed to replace completed projection: %+v", got)
	}
	restored := events.NewTranscript()
	restored.LoadHistory([]events.HistoryMsg{{Delegation: &first}, {Delegation: &second}})
	if want := latestPlanProgress(restored.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("revision live=%+v restored=%+v", got, want)
	}
	tr.Apply(first)
	if !reflect.DeepEqual(got, latestPlanProgress(tr.Items)) {
		t.Fatal("stale completed revision resurrected invalidated evidence")
	}
}

func TestPlanProgressProjection(t *testing.T) {
	for complete, percent := range []int{0, 33, 66, 100} {
		t.Run(fmt.Sprintf("%d-of-3", complete), func(t *testing.T) {
			s := progressSnapshot("pending", "pending", "pending")
			for i := 0; i < complete; i++ {
				s.Steps[i].Status = "complete"
			}
			v := projectSnapshot(s)
			if !v.Present || !v.Available || !v.PercentAvailable || v.Percent != percent || v.Counts.Complete != complete || v.Counts.Total != 3 {
				t.Fatalf("projection = %+v", v)
			}
		})
	}
	t.Run("missing-and-unknown-status", func(t *testing.T) {
		v := projectSnapshot(progressSnapshot("", "future", "complete"))
		if v.Counts.Pending != 1 || v.Counts.Unknown != 1 || v.Percent != 33 || v.Phases[0].State != planPhasePending || v.Phases[1].State != planPhaseUnknown {
			t.Fatalf("projection = %+v", v)
		}
	})
	t.Run("parallel", func(t *testing.T) {
		s := progressSnapshot("running", "running", "pending")
		s.Steps[0].ParallelGroup, s.Steps[1].ParallelGroup = "wave-1", "wave-1"
		v := projectSnapshot(s).withStreaming(true)
		if v.Counts.Running != 2 || v.Percent != 0 || !v.Phases[0].Current || !v.Phases[1].Current || !v.Phases[0].Live || !v.Phases[1].Live || v.Phases[2].Current {
			t.Fatalf("parallel projection = %+v", v)
		}
	})
	t.Run("no-plan", func(t *testing.T) {
		if latestPlanProgress([]events.Item{{Kind: events.ItemAgent, Content: samplePlanContent}}).Present {
			t.Fatal("agent prose became authority")
		}
	})
	t.Run("empty", func(t *testing.T) {
		v := projectSnapshot(progressSnapshot())
		if !v.Present || !v.Available || v.PercentAvailable || v.State == planProgressCompleted {
			t.Fatalf("empty projection = %+v", v)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		v := latestPlanProgress([]events.Item{{Kind: events.ItemPlan, Content: samplePlanContent}})
		if !v.Available || v.Counts.Total != 4 || v.Counts.Complete != 1 || v.Counts.Failed != 1 || v.Percent != 25 {
			t.Fatalf("legacy projection = %+v", v)
		}
	})
	t.Run("snapshot-preferred-over-content", func(t *testing.T) {
		s := progressSnapshot("pending")
		v := latestPlanProgress([]events.Item{{Kind: events.ItemPlan, Content: samplePlanContent, PlanSnapshot: &s}})
		if v.Counts.Total != 1 || v.Percent != 0 {
			t.Fatalf("snapshot lost authority: %+v", v)
		}
	})
}

func TestPlanProgressLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		status                     events.PlanStatus
		lifecycle, results, review string
		steps                      []string
		want                       planProgressState
	}{
		{"awaiting-approval", events.PlanPending, "", "", "", []string{"pending"}, planProgressAwaitingApproval},
		{"decline-precedes-results", events.PlanDeclined, "", "recorded", "", []string{"complete"}, planProgressDeclined},
		{"changes-precede-approval", events.PlanChangesRequested, "", "", "", []string{"pending"}, planProgressChangesRequested},
		{"results-precede-review", events.PlanApproved, agent.PlanLifecycleCompleted, "recorded", "one", []string{"complete"}, planProgressCompleted},
		{"review-precedes-running-and-failed", events.PlanApproved, agent.PlanLifecycleExecuting, "", "one", []string{"complete", "failed", "running"}, planProgressReviewPaused},
		{"failed-precedes-running", events.PlanApproved, agent.PlanLifecycleExecuting, "", "", []string{"failed", "running"}, planProgressFailed},
		{"running", events.PlanApproved, agent.PlanLifecycleExecuting, "", "", []string{"running"}, planProgressActive},
		{"all-complete-without-results", events.PlanApproved, agent.PlanLifecycleExecuting, "", "", []string{"complete"}, planProgressAwaitingCompletionRecord},
		{"lifecycle-alone-is-not-results", events.PlanApproved, agent.PlanLifecycleCompleted, "", "", []string{"complete"}, planProgressAwaitingCompletionRecord},
		{"ready", events.PlanApproved, agent.PlanLifecycleApproved, "", "", []string{"pending"}, planProgressIdleIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := progressSnapshot(tc.steps...)
			s.Document.Lifecycle, s.Document.Results, s.Document.CodeReviewPending = tc.lifecycle, tc.results, tc.review
			v := latestPlanProgress([]events.Item{{Kind: events.ItemPlan, PlanStatus: tc.status, PlanSnapshot: &s}})
			if v.State != tc.want {
				t.Fatalf("state = %q, want %q", v.State, tc.want)
			}
		})
	}
	t.Run("failure-retry-review-results-revision", func(t *testing.T) {
		tr := events.NewTranscript()
		s := progressSnapshot("running", "pending", "pending")
		apply := func(want planProgressState, percent int) {
			t.Helper()
			s.Sequence++
			tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s})
			v := latestPlanProgress(tr.Items)
			if v.State != want || v.Percent != percent {
				t.Fatalf("projection = %+v", v)
			}
		}
		apply(planProgressActive, 0)
		s.Steps[0].Status = "failed"
		apply(planProgressFailed, 0)
		s.Steps[0].Status = "running"
		apply(planProgressActive, 0)
		s.Steps[0].Status, s.Document.CodeReviewPending = "complete", "phase-1"
		apply(planProgressReviewPaused, 33)
		s.Document.CodeReviewPending = ""
		s.Steps[1].Status = "complete"
		apply(planProgressIdleIncomplete, 66)
		s.Steps[2].Status = "complete"
		apply(planProgressAwaitingCompletionRecord, 100)
		s.Document.Results = "Verified all phases"
		apply(planProgressCompleted, 100)
		s.Revision++
		s.Document.Results = ""
		s.Steps[1].Status, s.Steps[2].Status = "pending", "pending"
		s.Steps[1].Evidence, s.Steps[2].Evidence = "", ""
		apply(planProgressIdleIncomplete, 33)
	})
}

func TestPlanProgressLiveRestoreParity(t *testing.T) {
	live := events.NewTranscript()
	live.Apply(events.NewUser("Execute plan"))
	s := progressSnapshot("complete", "running", "running")
	ev := events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &s}
	live.Apply(ev)
	restored := events.NewTranscript()
	restored.LoadHistory([]events.HistoryMsg{{Kind: "user", Text: "Execute plan"}, {Delegation: &ev}})
	a, b := latestPlanProgress(live.Items), latestPlanProgress(restored.Items)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("live %+v != restored %+v", a, b)
	}
	if !live.Streaming || restored.Streaming {
		t.Fatal("fixture failed to distinguish streaming and restored")
	}
	lp, rp := a.withStreaming(live.Streaming), b.withStreaming(restored.Streaming)
	if lp.State != planProgressActive || rp.State != planProgressIdleIncomplete || lp.Percent != rp.Percent {
		t.Fatalf("presentation live=%+v restored=%+v", lp, rp)
	}
	for i := 1; i <= 2; i++ {
		if !lp.Phases[i].Live || rp.Phases[i].Live || !rp.Phases[i].Current || rp.Phases[i].State != planPhaseRunning {
			t.Fatal("restored phase was presented as a live worker or lost recorded state")
		}
	}
	if a.Phases[1].Live {
		t.Fatal("presentation mutated static projection")
	}
	live.Apply(events.NewDone())
	if got := latestPlanProgress(live.Items).withStreaming(live.Streaming); !reflect.DeepEqual(got, rp) {
		t.Fatalf("finished turn != restored: %+v vs %+v", got, rp)
	}
}

func TestPlanProgressLatestIdentity(t *testing.T) {
	tr := events.NewTranscript()
	old := progressSnapshot("complete")
	old.PlanID, old.Sequence = "older", 100
	tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &old})
	latest := progressSnapshot("running", "pending")
	latest.PlanID, latest.Sequence = "newer", 2
	tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &latest})
	baseline := latestPlanProgress(tr.Items)
	for _, seq := range []int64{1, 2} {
		stale := latest.Clone()
		stale.Sequence, stale.Steps[0].Status = seq, "complete"
		tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &stale})
		if !reflect.DeepEqual(baseline, latestPlanProgress(tr.Items)) {
			t.Fatal("stale/duplicate event changed progress")
		}
	}
	old.Sequence++
	old.Steps[0].Status = "failed"
	tr.Apply(events.Event{Kind: events.KindPlanSnapshot, PlanSnapshot: &old})
	if !reflect.DeepEqual(baseline, latestPlanProgress(tr.Items)) {
		t.Fatal("late older identity replaced latest plan")
	}
	for _, content := range []string{"not a plan", "# Execution Plan\n\n## Phases\n"} {
		items := append(append([]events.Item(nil), tr.Items...), events.Item{Kind: events.ItemPlan, Content: content})
		v := latestPlanProgress(items)
		if !v.Present || v.PercentAvailable || v.PlanID == latest.PlanID || len(v.Phases) != 0 {
			t.Fatalf("malformed/empty latest fell back: %+v", v)
		}
	}
	tr.Reset()
	if latestPlanProgress(tr.Items).Present {
		t.Fatal("session reset retained plan")
	}
}
