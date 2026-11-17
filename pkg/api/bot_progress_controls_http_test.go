package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Each case uses HTTP admission and the production worker against owned SQLite.
// Draining at the first settled read leaves a real durable context and ledger.
func progressControlFixture(t *testing.T, scope store.BotScope) (*interactiveHTTPFixture, store.BotRun) {
	t.Helper()
	f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
	*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
		return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
	}
	v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
	if err != nil {
		t.Fatal(err)
	}
	v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
	raw, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated progress controls", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
	if err = json.Unmarshal(f.request("PUT", f.path+"/configuration", string(raw), 200), &f.b); err != nil {
		t.Fatal(err)
	}
	*f.policy = nil
	f.db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
	if err = json.Unmarshal(f.request("POST", f.path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"Isolated explicit activation"}`, f.b.Revision), 200), &f.b); err != nil {
		t.Fatal(err)
	}
	drained := make(chan error, 1)
	calls := 0
	f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		calls++
		if calls == 1 {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				drained <- f.worker.Drain(ctx)
			}()
			time.Sleep(50 * time.Millisecond)
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "control-read", Name: "read_file", Args: map[string]any{"path": "/isolated/receipt.txt"}}}}}}, true
		}
		raw, _ := json.Marshal(req.Contents)
		if !strings.Contains(string(raw), "isolated receipt") {
			t.Errorf("lost saved evidence: %s", raw)
		}
		return &model.LLMResponse{Content: genai.NewContentFromText("The saved evidence confirms isolated receipt.", "model")}, true
	}
	var receipt store.BotMessageReceipt
	if err = json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read the receipt and explain it.","idempotency_key":"control-assignment"}`, f.b.Version), 202), &receipt); err != nil {
		t.Fatal(err)
	}
	if err = f.worker.Poll(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-drained; err != nil {
		t.Fatal(err)
	}
	run := progressGetRun(t, f, receipt.Run.ID)
	if run.Status != store.RunQueued || run.Progress == nil || f.pool.calls != 1 {
		t.Fatalf("not a durable settled checkpoint: %+v", run)
	}
	return f, run
}
func progressGetRun(t *testing.T, f *interactiveHTTPFixture, id uuid.UUID) store.BotRun {
	t.Helper()
	var run store.BotRun
	if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+id.String(), "", 200), &run); err != nil {
		t.Fatal(err)
	}
	return run
}
func progressPause(t *testing.T, f *interactiveHTTPFixture, run store.BotRun) store.BotRun {
	t.Helper()
	if err := json.Unmarshal(f.request("POST", f.path+"/pause", fmt.Sprintf(`{"expected_revision":%d,"reason":"Owner pause"}`, f.b.Revision), 200), &f.b); err != nil {
		t.Fatal(err)
	}
	paused := progressGetRun(t, f, run.ID)
	if paused.Status != store.RunInterrupted || paused.Progress == nil || paused.Progress.Phase != store.BotProgressSuspended || paused.Progress.Reason != store.BotProgressReasonPaused || paused.ContinuationReason != store.BotContinuationRequired || paused.Usage.ToolCalls != run.Usage.ToolCalls {
		t.Fatalf("pause lost assignment: %+v", paused)
	}
	return paused
}
func progressFreshWorker(t *testing.T, f *interactiveHTTPFixture) *daemon.BotWorker {
	t.Helper()
	cfg := f.workerConfig
	cfg.WorkerID = uuid.NewString()
	w, err := daemon.NewBotWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	return w
}
func TestBotProgressLifecycleHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, before := progressControlFixture(t, scope)
			paused := progressPause(t, f, before)
			privatePaused, err := f.repo.GetRun(f.ctx, before.BotID, before.ID)
			if err != nil || privatePaused.CallerCeiling == nil {
				t.Fatalf("missing private caller ceiling: %v", err)
			}
			path := f.path + "/runs/" + before.ID.String() + "/continue-interactive"
			body := func(rev int64, key string) string {
				return fmt.Sprintf(`{"kind":"progress_v1","checkpoint_revision":%d,"expected_revision":%d,"idempotency_key":%q}`, rev, f.b.Revision, key)
			}
			f.request("POST", path, body(paused.Progress.Revision+1, "stale"), 409)
			f.request("POST", path, body(paused.Progress.Revision, "resume"), 202)
			f.request("POST", path, body(paused.Progress.Revision, "resume"), 202)
			queued := progressGetRun(t, f, before.ID)
			if queued.Status != store.RunQueued || !reflect.DeepEqual(queued.Usage, paused.Usage) {
				t.Fatalf("continuation reset usage: %+v", queued)
			}
			privateQueued, err := f.repo.GetRun(f.ctx, before.BotID, before.ID)
			if err != nil || !reflect.DeepEqual(privateQueued.CallerCeiling, privatePaused.CallerCeiling) {
				t.Fatal("continuation renewed still-live caller authority")
			}
			if err := progressFreshWorker(t, f).Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			after := progressGetRun(t, f, before.ID)
			if after.Status != store.RunSucceeded || f.pool.calls != 1 || after.SessionID != before.SessionID || after.Usage.RuntimeNanos < before.Usage.RuntimeNanos {
				t.Fatalf("unsafe continuation: %+v", after)
			}
			current, err := f.repo.Get(f.ctx, f.b.ID)
			if err != nil || current.Status != store.BotPaused {
				t.Fatalf("continuation enabled recurrence: %+v %v", current, err)
			}
		})
		t.Run(string(scope)+"/lost-authority", func(t *testing.T) {
			f, before := progressControlFixture(t, scope)
			paused := progressPause(t, f, before)
			privatePaused, err := f.repo.GetRun(f.ctx, before.BotID, before.ID)
			if err != nil || privatePaused.CallerCeiling == nil {
				t.Fatalf("missing private caller ceiling: %v", err)
			}
			p := privatePaused.CallerCeiling.Principal
			id := execution.BotRunIdentity{BotID: before.BotID, RunID: before.ID, Version: before.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			f.registry.Cancel(id)
			// A persisted principal/tenant alone is not fresh authentication evidence.
			forged, err := execution.WithPrincipal(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			forged = store.WithTenantContext(forged, &store.TenantContext{UserID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
			forged = store.WithBotMessageAdmission(forged, f.registry)
			q := store.BotContinueInteractiveRequest{BotID: before.BotID, RunID: before.ID, Kind: store.BotContinuationProgress, CheckpointRevision: paused.Progress.Revision, ExpectedRevision: f.b.Revision, IdempotencyKey: "fresh-auth"}
			if _, err := f.repo.(store.BotInteractiveContinuationStore).ContinueInteractive(forged, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("forged continuation accepted: %v", err)
			}
			held, err := f.repo.GetRun(f.ctx, before.BotID, before.ID)
			if err != nil || held.Status != paused.Status || !reflect.DeepEqual(held.Usage, paused.Usage) || !reflect.DeepEqual(held.CallerCeiling, privatePaused.CallerCeiling) {
				t.Fatal("rejected continuation mutated durable assignment")
			}
			raw, _ := json.Marshal(q)
			f.request("POST", f.path+"/runs/"+before.ID.String()+"/continue-interactive", string(raw), 202)
			queued, err := f.repo.GetRun(f.ctx, before.BotID, before.ID)
			if err != nil || queued.CallerCeiling == nil || queued.Status != store.RunQueued || !reflect.DeepEqual(queued.Usage, paused.Usage) || !reflect.DeepEqual(queued.CallerCeiling.Principal, privatePaused.CallerCeiling.Principal) {
				t.Fatal("fresh authenticated continuation expanded authority or reset usage")
			}
			if err := progressFreshWorker(t, f).Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			after := progressGetRun(t, f, before.ID)
			if after.Status != store.RunSucceeded || f.pool.calls != 1 || after.SessionID != before.SessionID {
				t.Fatalf("lost-authority recovery replayed work or lost context: %+v", after)
			}
		})
	}
}
func TestBotProgressRestartHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, before := progressControlFixture(t, scope)
			paused := progressPause(t, f, before)
			if err := progressFreshWorker(t, f).Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			held := progressGetRun(t, f, before.ID)
			if held.Status != paused.Status || held.Progress.Revision != paused.Progress.Revision || f.pool.calls != 1 {
				t.Fatalf("restart ignored pause: %+v", held)
			}
			f.request("POST", f.path+"/runs/"+before.ID.String()+"/cancel", "{}", 202)
			cancelled := progressGetRun(t, f, before.ID)
			if cancelled.Status != store.RunCancelled || cancelled.ContinuationReason != "" {
				t.Fatalf("cancel retained continuation: %+v", cancelled)
			}
			f.request("POST", f.path+"/runs/"+before.ID.String()+"/continue-interactive", fmt.Sprintf(`{"kind":"progress_v1","checkpoint_revision":%d,"expected_revision":%d,"idempotency_key":"after-cancel"}`, paused.Progress.Revision, f.b.Revision), 409)
		})
	}
}
func TestBotProgressDeleteHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, before := progressControlFixture(t, scope)
			f.request("DELETE", f.path+"/permanent", fmt.Sprintf(`{"expected_revision":%d,"confirm":true}`, f.b.Revision), 202)
			cancelled := progressGetRun(t, f, before.ID)
			if cancelled.Status != store.RunCancelled || cancelled.ContinuationReason != "" {
				t.Fatalf("deletion retained execution eligibility: %+v", cancelled)
			}
			f.request("POST", f.path+"/runs/"+before.ID.String()+"/continue-interactive", fmt.Sprintf(`{"kind":"progress_v1","checkpoint_revision":%d,"expected_revision":%d,"idempotency_key":"after-delete"}`, before.Progress.Revision, f.b.Revision), 409)
			if err := progressFreshWorker(t, f).Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			if f.pool.calls != 1 {
				t.Fatal("deleted assignment executed")
			}
		})
	}
}
func TestBotProgressStateSSE(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, before := progressControlFixture(t, scope)
			progressPause(t, f, before)
			var snapshot store.BotSnapshot
			if err := json.Unmarshal(f.request("GET", f.path+"/snapshot", "", 200), &snapshot); err != nil {
				t.Fatal(err)
			}
			var expected store.BotRunState
			for _, state := range snapshot.RunStates {
				if state.RunID == before.ID {
					expected = state
				}
			}
			if expected.Progress == nil || expected.StateSequence < 1 || expected.CommittedAt.IsZero() {
				t.Fatalf("missing committed projection: %+v", expected)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "GET", f.server.URL+f.path+"/sessions/"+f.b.HomeSessionID.String()+"/events?scope="+string(scope), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("SSE %d", resp.StatusCode)
			}
			scan := bufio.NewScanner(resp.Body)
			scan.Buffer(make([]byte, 4096), 1<<20)
			var found store.BotRunState
			var sequence int64
			for scan.Scan() {
				line := scan.Text()
				if line == "event: replay_complete" {
					break
				}
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				raw := strings.TrimPrefix(line, "data: ")
				if strings.Contains(raw, "dispatch_ledger") || strings.Contains(raw, "context_ref") || strings.Contains(raw, "reserved_usage") {
					t.Fatalf("private progress leaked: %s", raw)
				}
				var event struct {
					Actions struct {
						StateDelta map[string]json.RawMessage `json:"stateDelta"`
					} `json:"actions"`
				}
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					t.Fatal(err)
				}
				stateRaw := event.Actions.StateDelta[store.BotRunStateKey]
				if stateRaw == nil {
					continue
				}
				var state store.BotRunState
				if err := json.Unmarshal(stateRaw, &state); err != nil {
					t.Fatal(err)
				}
				if state.RunID != before.ID {
					continue
				}
				if state.StateSequence <= sequence {
					t.Fatalf("unordered states: %d <= %d", state.StateSequence, sequence)
				}
				sequence = state.StateSequence
				found = state
			}
			if !reflect.DeepEqual(expected, found) {
				t.Fatalf("snapshot/live divergence: expected %+v got %+v", expected, found)
			}
		})
	}
}
