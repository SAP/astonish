package events

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
)

func TestPlanSnapshotKeepsFollowingAnswerSeparate(t *testing.T) {
	for _, mode := range []struct {
		name   string
		linear bool
	}{{"code", true}, {"chat", false}} {
		for _, restored := range []bool{false, true} {
			name := mode.name + "/live"
			if restored {
				name = mode.name + "/restored"
			}
			t.Run(name, func(t *testing.T) {
				tr := NewTranscript()
				tr.LinearThread = mode.linear
				snapshot := agent.PlanSnapshot{
					PlanID: "answer-boundary", Revision: 1, Sequence: 1, Goal: "Implement the change",
					Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleCompleted, Results: "Verified."},
					Steps:    []agent.PlanStepInfo{{Name: "implement", Status: "complete", Evidence: "exit 0"}},
				}
				ev := Event{Kind: KindPlanSnapshot, PlanSnapshot: &snapshot}
				if restored {
					tr.LoadHistory([]HistoryMsg{{Kind: "delegation", Delegation: &ev}})
				} else {
					tr.Apply(ev)
				}
				if len(tr.Items) != 1 || tr.Items[0].Kind != ItemPlan || tr.Items[0].PlanSnapshot == nil {
					t.Fatalf("missing snapshot-backed plan: %+v", tr.Items)
				}
				planContent := tr.Items[0].Content
				answer := ""
				for _, chunk := range []string{"Implementation finished. ", "All checks passed."} {
					tr.Apply(Event{Kind: KindText, Text: chunk})
					answer += chunk
					if len(tr.Items) != 2 || tr.Items[0].Kind != ItemPlan || tr.Items[0].Content != planContent || tr.Items[1].Kind != ItemAgent || tr.Items[1].Content != answer {
						t.Fatalf("answer chunk must stay outside the plan: %+v", tr.Items)
					}
					if tr.Items[1].Provisional != !mode.linear {
						t.Fatalf("streaming answer provisional = %v, linear = %v", tr.Items[1].Provisional, mode.linear)
					}
				}
				tr.Apply(Event{Kind: KindDone})
				if len(tr.Items) != 2 || tr.Items[1].Kind != ItemAgent || tr.Items[1].Content != answer || tr.Items[1].Provisional {
					t.Fatalf("done must retain the separate final answer: %+v", tr.Items)
				}

				// A later authoritative render must update only the plan, never
				// erase answer text that was appended after the snapshot.
				snapshot.Sequence++
				snapshot.Goal = "Updated plan goal"
				tr.Apply(Event{Kind: KindPlanSnapshot, PlanSnapshot: &snapshot})
				if len(tr.Items) != 2 || tr.Items[0].Kind != ItemPlan || tr.Items[0].PlanSnapshot.Sequence != snapshot.Sequence || !strings.Contains(tr.Items[0].Content, snapshot.Goal) || strings.Contains(tr.Items[0].Content, answer) || tr.Items[1].Kind != ItemAgent || tr.Items[1].Content != answer || tr.Items[1].Provisional {
					t.Fatalf("snapshot update must preserve the answer: %+v", tr.Items)
				}
			})
		}
	}
}

func TestLegacyStreamedPlanKeepsTextChunks(t *testing.T) {
	for name, chunks := range map[string][]string{
		"whole header": {"# Execution Plan\n", "\n**Goal:** Legacy plan", "\n\nMore plan details."},
		"split header": {"# Execution ", "Plan\n", "\n**Goal:** Legacy plan", "\n\nMore plan details."},
	} {
		t.Run(name, func(t *testing.T) {
			tr := NewTranscript()
			tr.LinearThread = true
			for _, chunk := range chunks {
				tr.Apply(Event{Kind: KindText, Text: chunk})
			}
			tr.Apply(Event{Kind: KindDone})
			if len(tr.Items) != 1 || tr.Items[0].Kind != ItemPlan || tr.Items[0].PlanSnapshot != nil || tr.Items[0].Content != strings.Join(chunks, "") {
				t.Fatalf("legacy plan chunks must remain one complete document: %+v", tr.Items)
			}
		})
	}
}
