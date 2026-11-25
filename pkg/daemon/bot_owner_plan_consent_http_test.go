package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

// Real HTTP admission and consent, durable worker checkpoints and actual bounded
// file/shell dispatch. Authentication is supplied by the fixture middleware;
// neither this helper nor the model injects an execution approval.
func testOwnerDefinitionConsentHTTP(t *testing.T, ctx context.Context, repo store.BotStore, ss session.Service, b *store.Bot, admitted store.BotRun, pool *preparedAdmissionHTTPPool, llm *preparedAdmissionHTTPModel, post func(string, any, int) []byte, poll func(), foreign func(string, any), expire func()) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	get := func() *store.BotRun { t.Helper(); r, err := repo.GetRun(ctx, b.ID, admitted.ID); must(err); return r }
	snapshot := func() agent.PlanSnapshot {
		t.Helper()
		s, err := ss.Get(ctx, &session.GetRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: admitted.SessionID.String()})
		must(err)
		v, err := s.Session.State().Get(agent.PlanSnapshotStateKey)
		must(err)
		raw, err := json.Marshal(v)
		must(err)
		var snap agent.PlanSnapshot
		must(json.Unmarshal(raw, &snap))
		return snap
	}
	pending := get()
	checkpoint, err := bot.DecodeApprovalCheckpoint(pending.Checkpoint, *pending)
	must(err)
	approvals, err := repo.ListApprovals(ctx, b.ID, store.BotPage{Limit: 100})
	must(err)
	var approval store.BotApproval
	for _, a := range approvals {
		if a.ID == checkpoint.ApprovalID {
			approval = a
		}
	}
	consent, err := approval.Interaction.PlanDefinition()
	must(err)
	initial := snapshot()
	if !checkpoint.Matches(approval) || consent.RunID != admitted.ID || consent.SessionID != admitted.SessionID.String() || initial.PlanID != consent.PlanID || initial.Sequence != consent.Sequence || initial.Document.Results != "" || initial.Document.Lifecycle != "" {
		t.Fatal("definition wait did not bind persisted original identity")
	}
	route := "runs/" + admitted.ID.String() + "/continue-interactive"
	request := store.BotContinueInteractiveRequest{ExpectedRevision: b.Revision, IdempotencyKey: "owner-definition", ApprovedPlanExecution: true, PlanExecution: &consent}
	stale := consent
	stale.Sequence++
	for name, changed := range map[string]store.BotPlanExecutionConsent{
		"sequence": stale,
		"revision": {RunID: consent.RunID, SessionID: consent.SessionID, PlanID: consent.PlanID, Revision: consent.Revision + 1, Sequence: consent.Sequence},
		"plan":     {RunID: consent.RunID, SessionID: consent.SessionID, PlanID: consent.PlanID + "-foreign", Revision: consent.Revision, Sequence: consent.Sequence},
	} {
		t.Logf("deny mismatched definition %s", name)
		request.PlanExecution = &changed
		post(route, request, 409)
	}
	foreignSession := consent
	foreignSession.SessionID += "-foreign"
	request.PlanExecution = &foreignSession
	post(route, request, 400)
	request.PlanExecution = nil
	post(route, request, 400)
	request.PlanExecution = &consent
	planning := true
	request.PlanMode = &planning
	post(route, request, 400)
	request.PlanMode = nil
	foreign(route, request)
	pool.mu.Lock()
	for _, call := range pool.calls {
		if call == "shell_command" {
			pool.mu.Unlock()
			t.Fatal("definition wait or denied consent executed the verifier")
		}
	}
	pool.mu.Unlock()
	expire() // force fresh authenticated registration, not constructor approval
	llm.mu.Lock()
	llm.script = append(llm.script, configuredAdmissionCall{"update_plan", map[string]any{"step": "verify", "status": "running"}}, configuredAdmissionCall{"update_plan", map[string]any{"step": "verify", "status": "complete"}}, configuredAdmissionCall{"announce_completion", map[string]any{"outcome_observed": "prepared-http-ok executed exactly once", "unverified": "Physical sandbox transport"}})
	llm.mu.Unlock()
	var queued store.BotRun
	must(json.Unmarshal(post(route, request, 202), &queued))
	post(route, request, 409)
	if queued.ID != pending.ID || queued.SessionID != pending.SessionID {
		t.Fatal("consent replaced originating run/session")
	}
	input, err := repo.(interface {
		LoadRunInput(context.Context, store.BotID, store.RunID) (*store.BotRunInput, error)
	}).LoadRunInput(ctx, b.ID, admitted.ID)
	must(err)
	if input.PlanMode {
		t.Fatal("owner execution inherited definition planning mode")
	}
	poll()
	final := get()
	snap := snapshot()
	if final.Status != store.RunSucceeded && final.Status != store.RunPartial || final.Lease.Fence <= pending.Lease.Fence || snap.Document.Results == "" || snap.Steps[0].Status != "complete" {
		t.Fatalf("consent lifecycle failed run=%+v failure=%+v snapshot=%+v", final, final.Report.Failure, snap)
	}
	data, err := os.ReadFile(filepath.Join(pool.root, "PLAN.md"))
	must(err)
	if !strings.Contains(string(data), "## Results") || !strings.Contains(string(data), "prepared-http-ok") {
		t.Fatalf("missing actual verifier Results: %s", data)
	}
	pool.mu.Lock()
	verifies := 0
	for _, n := range pool.calls {
		if n == "shell_command" {
			verifies++
		}
	}
	pool.mu.Unlock()
	if verifies != 1 {
		t.Fatalf("verifier count %d", verifies)
	}
	post(route, request, 409)
	before := pool.count()
	poll()
	if pool.count() != before {
		t.Fatal("completed consent replayed effects")
	}
	t.Logf("actual HTTP owner ContinueInteractiveRequest 202: run/session unchanged, fresh fence %d -> %d, persisted definition -> real verifier once -> Results; stale/completed consent 409", pending.Lease.Fence, final.Lease.Fence)
}
