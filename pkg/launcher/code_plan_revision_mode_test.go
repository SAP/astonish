package launcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/SAP/astonish/pkg/tui/backend"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise the actual local turn runner, tool gates, persistence and restore.
// Only the provider is scripted; no paid model or user session is used.
func TestCodePlanRevisionWhilePlanningAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	for _, tc := range []struct {
		name            string
		graph, approved bool
	}{
		{"plan", false, false}, {"graph", true, false},
		{"approved-plan", false, true}, {"approved-graph", true, true},
	} {
		name, graph := tc.name, tc.graph
		t.Run(name, func(t *testing.T) {
			dir, workspace := t.TempDir(), t.TempDir()
			b, m := codePlanAcceptanceBackend(t, dir, workspace, "")
			codePlanAnnounce(t, b, m, false)
			if tc.approved {
				runtime, err := b.localPlanRuntime(context.Background(), b.sessionID, func(string, map[string]any) {})
				if err != nil {
					t.Fatal(err)
				}
				if err := runtime.SetApproved(); err != nil {
					t.Fatal(err)
				}
			}
			before := codePlanSnapshot(t, b)
			steps := before.Definition().Steps
			steps[0].Details = "Reveal the active phase again on every phase transition, even after manual scrolling"
			revision := tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: before.PlanID, BaseRevision: before.Revision, Reason: "User requested different scrolling behavior", Steps: steps}}
			calls := 0
			m.next = func(_ *adkmodel.LLMRequest) *genai.Content {
				calls++
				if calls == 1 {
					return codePlanCall("update_plan", revision, m.calls)
				}
				t.Error("planning revision continued instead of stopping for approval")
				return genai.NewContentFromText("Unexpected continuation", "model")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			got := codePlanRun(t, ctx, b, "Revise the plan without leaving Plan mode", backend.TurnOptions{PlanMode: !graph, GraphPlanMode: graph})
			var presented bool
			for _, ev := range got {
				if ev.PlanSnapshot != nil && ev.PlanSnapshot.Revision == before.Revision+1 && ev.PlanSnapshot.Document.Lifecycle == "" {
					presented = true
				}
			}
			if !presented {
				t.Fatal("revised plan was not presented")
			}
			after := codePlanSnapshot(t, b)
			if calls != 1 || after.PlanID != before.PlanID || after.Revision != before.Revision+1 || after.Steps[0].Details != steps[0].Details || after.Document.Lifecycle != "" || after.Document.Results != "" {
				t.Fatalf("incorrect revised authority: calls=%d snapshot=%+v", calls, after)
			}
			for _, step := range after.Steps {
				if step.Status != "pending" || step.Evidence != "" {
					t.Fatalf("planning executed phase: %+v", step)
				}
			}
			if _, err := os.Stat(filepath.Join(workspace, "marker")); !os.IsNotExist(err) {
				t.Fatalf("planning ran verifier: %v", err)
			}
			raw, err := os.ReadFile(b.planFilePath(b.sessionID))
			if err != nil || !strings.Contains(string(raw), steps[0].Details) {
				t.Fatalf("revision not mirrored: %s %v", raw, err)
			}
			fresh, _ := codePlanAcceptanceBackend(t, dir, workspace, b.sessionID)
			history, err := fresh.ResumeSession(ctx, b.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			tr := projectPlanHistory(history)
			requestIndex, planIndex := -1, -1
			for i, item := range tr.Items {
				if strings.Contains(item.Content, "Revise the plan without leaving Plan mode") && item.PlanSnapshot == nil {
					requestIndex = i
				}
				if item.PlanSnapshot != nil && item.PlanSnapshot.PlanID == after.PlanID {
					if planIndex >= 0 {
						t.Fatal("duplicate restored plan")
					}
					planIndex = i
				}
			}
			if requestIndex < 0 || planIndex <= requestIndex || !tr.Awaiting || tr.ApprovalIdx != planIndex {
				t.Fatalf("restored revision was not presented after request: request=%d plan=%d awaiting=%v", requestIndex, planIndex, tr.Awaiting)
			}
			restored := codePlanSnapshot(t, fresh)
			if !reflect.DeepEqual(after, restored) {
				t.Fatalf("restore lost revision: before=%+v restored=%+v", after, restored)
			}
		})
	}
}
