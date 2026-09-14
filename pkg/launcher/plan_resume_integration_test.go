package launcher

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/events"
)

// TestPlanResumesAfterCompactionAndRestart drives the full user-visible chain
// the durability bug broke: approve → compact → restart → resume → complete.
func TestPlanResumesAfterCompactionAndRestart(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	id := seedSession(t, b, "implement the plan")
	planPath := b.planFilePath(id)

	// --- ChatAgent #1: announce and approve ---
	c1 := &agent.ChatAgent{}
	c1.SetPlanFilePath(planPath)
	plan := agent.NewPlanState("Durable plans", agent.PlanDocumentInfo{}, []agent.PlanStepInfo{
		{Name: "phase-one", Description: "first", Verify: "true", VerifyKind: "unit", Status: "complete", Evidence: "exit 0; ok"},
		{Name: "phase-two", Description: "second", Verify: "true", VerifyKind: "unit"},
	})
	if !c1.TrySetActivePlan(plan) {
		t.Fatal("announce should be accepted")
	}
	c1.MarkActivePlanApproved()

	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(data), "## Status") {
		t.Fatalf("approval not persisted to PLAN.md:\n%s", data)
	}

	// --- Session records the approval the way RecordPlanDecision does ---
	sess := getSession(t, b, id)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:        "plan-decision-approved",
		Author:    "system",
		Timestamp: time.Now(),
		Actions: adksession.EventActions{StateDelta: map[string]any{
			planLifecycleStateKey: string(events.PlanApproved),
		}},
	}); err != nil {
		t.Fatalf("append plan decision: %v", err)
	}

	// --- Simulate compaction ---
	compacted := []*adksession.Event{{
		ID:          "summary",
		Author:      "model",
		LLMResponse: modelTextResponse("[Context Summary — earlier messages compacted]"),
	}}
	if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, compacted); err != nil {
		t.Fatalf("ArchiveAndReplaceEvents: %v", err)
	}

	// --- Simulate restart: fresh ChatAgent, nothing in memory ---
	c2 := &agent.ChatAgent{}
	c2.SetPlanFilePath(planPath)
	if c2.IsActivePlanApproved() {
		t.Fatal("fresh agent must not start sealed")
	}

	if !b.shouldContinueApprovedPlan(ctx, id, planPath, c2) {
		t.Fatal("restarted session must keep executing the approved plan")
	}

	if err := c2.RestoreApprovedPlan(); err != nil {
		t.Fatalf("RestoreApprovedPlan: %v", err)
	}
	if !c2.IsActivePlanApproved() {
		t.Fatal("restored plan must be sealed")
	}
	restored := c2.GetActivePlan()
	if restored == nil {
		t.Fatal("restored plan is nil")
	}
	_, steps := restored.SnapshotInfo()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if steps[0].Status != "complete" || !strings.Contains(steps[0].Evidence, "exit 0") {
		t.Fatalf("phase one not resumed as complete with evidence: %+v", steps[0])
	}
	if steps[1].Status != "pending" {
		t.Fatalf("phase two status = %q, want pending (resume, not restart)", steps[1].Status)
	}

	// --- Finish the interrupted work ---
	restored.SetStepStatus("phase-two", "complete")
	res := c2.AnnounceCompletion("plans survive restart and compaction", "")
	if res.Code != agent.PlanCompletionOK {
		t.Fatalf("AnnounceCompletion code = %q (%s), want ok", res.Code, res.Message)
	}
	final, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(final), "## Results") {
		t.Fatalf("PLAN.md has no Results section after completion:\n%s", final)
	}
}

// TestSessionStateSurvivesCompactionForRoutingBadges guards the positional
// matching contract loadHistory depends on: routing events must survive
// compaction, in order.
func TestSessionStateSurvivesCompactionForRoutingBadges(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	id := seedSession(t, b, "route me")
	sess := getSession(t, b, id)

	appendState := func(evID, tier, model string) {
		t.Helper()
		if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
			ID:        evID,
			Author:    "system",
			Timestamp: time.Now(),
			Actions: adksession.EventActions{StateDelta: map[string]any{
				routingInfoStateKey: map[string]any{"tier": tier, "model": model},
			}},
		}); err != nil {
			t.Fatalf("append routing event: %v", err)
		}
	}
	appendState("route-1", "strong", "m1")
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:          "mid",
		Author:      "model",
		LLMResponse: modelTextResponse("some answer"),
	}); err != nil {
		t.Fatalf("append content event: %v", err)
	}
	appendState("route-2", "weak", "m2")

	compacted := []*adksession.Event{{
		ID:          "summary",
		Author:      "model",
		LLMResponse: modelTextResponse("[Context Summary]"),
	}}
	if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, compacted); err != nil {
		t.Fatalf("ArchiveAndReplaceEvents: %v", err)
	}

	reloaded := getSession(t, b, id)
	var routeIDs []string
	for ev := range reloaded.Events().All() {
		if len(ev.Actions.StateDelta) > 0 && ev.LLMResponse.Content == nil {
			routeIDs = append(routeIDs, ev.ID)
		}
	}
	if len(routeIDs) != 2 || routeIDs[0] != "route-1" || routeIDs[1] != "route-2" {
		t.Fatalf("routing events after compaction = %v, want [route-1 route-2]", routeIDs)
	}
}

func modelTextResponse(text string) adkmodel.LLMResponse {
	return adkmodel.LLMResponse{Content: genai.NewContentFromText(text, genai.RoleModel)}
}
