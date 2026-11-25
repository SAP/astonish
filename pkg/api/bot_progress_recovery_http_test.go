package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotProgressRecoveryHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			runtimeBefore := databaseRuntimeFileSnapshot(t, f.dataDir)
			*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
			}
			v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
			if err != nil {
				t.Fatal(err)
			}
			v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			body, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated settled recovery acceptance", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			*f.policy = nil
			drain := make(chan error, 1)
			calls := 0
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				calls++
				if calls == 1 {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						drain <- f.worker.Drain(ctx)
					}()
					time.Sleep(50 * time.Millisecond)
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "durable-read", Name: "read_file", Args: map[string]any{"path": "receipt.txt"}}}}}}, true
				}
				// A fresh factory must restore the old tool response, not run it again.
				raw, _ := json.Marshal(req.Contents)
				if !strings.Contains(string(raw), "isolated receipt") {
					t.Errorf("restored employee lost source evidence: %s", raw)
				}
				return &model.LLMResponse{Content: genai.NewContentFromText("The saved receipt confirms isolated receipt.", "model")}, true
			}
			var admitted store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read the receipt and explain it.","idempotency_key":"progress-recovery"}`, f.b.Version), 202), &admitted); err != nil {
				t.Fatal(err)
			}
			if err := f.worker.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-drain; err != nil {
				t.Fatal(err)
			}
			before, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, admitted.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Run.Status != store.RunQueued || before.Progress == nil || before.Progress.Phase != store.BotProgressSuspended || before.Work.Candidate != nil || before.Work.Phase != store.BotWorkWorking {
				t.Fatalf("handoff became terminal: %+v", before)
			}
			if before.Run.Report.EmployeeMessage != nil {
				t.Fatal("handoff published a final employee message")
			}
			if calls != 1 || f.pool.calls != 1 {
				t.Fatalf("handoff dispatched extra work: models=%d tools=%d", calls, f.pool.calls)
			}
			cfg := f.workerConfig
			cfg.WorkerID = uuid.NewString()
			fresh, err := daemon.NewBotWorker(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Stop()
			if err := fresh.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			var after store.BotRun
			if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200), &after); err != nil {
				t.Fatal(err)
			}
			if after.Status != store.RunSucceeded || f.pool.calls != 1 || after.Usage.ToolCalls != 1 || after.Usage.RuntimeNanos < before.Run.Usage.RuntimeNanos || after.Usage.InputTokens < before.Run.Usage.InputTokens || after.Usage.OutputTokens < before.Run.Usage.OutputTokens {
				t.Fatalf("resume failed or repeated effect: run=%+v failure=%+v tools=%d", after, after.Report.Failure, f.pool.calls)
			}
			if after.ID != before.Run.ID || after.SessionID != before.Run.SessionID || calls != 2 {
				t.Fatalf("recovery restarted assignment or invoked review: before=%s/%s after=%s/%s models=%d", before.Run.ID, before.Run.SessionID, after.ID, after.SessionID, calls)
			}
			history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
			assertProgressReportText(t, history, "bot-report:"+after.ID.String(), "The saved receipt confirms isolated receipt.")
			if again := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200)); again != history {
				t.Fatal("recovery reload changed durable event identities/order")
			}
			if calls != 2 || f.pool.calls != 1 {
				t.Fatal("reading restored conversation executed work")
			}
			if runtimeAfter := databaseRuntimeFileSnapshot(t, f.dataDir); !reflect.DeepEqual(runtimeBefore, runtimeAfter) {
				t.Fatal("checkpoint recovery wrote runtime files")
			}
		})
	}
}

// Run the existing shared delegate manager against Bot-linked SQL sessions, then
// read a bounded history without invoking either model or tool dispatch again.
func TestBotDatabaseDelegateStateReplayHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			before := databaseRuntimeFileSnapshot(t, f.dataDir)
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			ss := org.ForUser(f.b.OwnerID.String()).Sessions()
			if scope == store.BotScopeTeam {
				ss = org.ForTeam("team").Sessions()
			}
			const initial = "U.S. markets and major indexes"
			calls := 0
			mdl := &employeeConfiguredSearchFixture{run: func(_ context.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
				calls++
				return &model.LLMResponse{Content: genai.NewContentFromText("Delegate checked major indexes.", "model")}, nil
			}}
			manager := agent.NewSubAgentManager(agent.SubAgentConfig{TaskTimeout: 10 * time.Second, DelegationTimeout: 15 * time.Second})
			manager.LLM, manager.SessionService, manager.AppName, manager.UserID = mdl, ss, "astonish", f.b.OwnerID.String()
			childID := uuid.NewString()
			results := manager.RunTasks(f.ctx, []agent.SubAgentTask{{Name: "index-reader", Description: "Check major indexes", Instructions: "Return the index check", ParentID: f.b.HomeSessionID.String(), ChildSessionID: childID, SessionState: map[string]any{"market_selection": initial}}})
			if len(results) != 1 || results[0].Status != "success" || calls != 1 {
				t.Fatalf("delegate=%+v calls=%d", results, calls)
			}
			children, err := ss.ListChildren(f.ctx, f.b.HomeSessionID.String())
			if err != nil || len(children) != 1 || children[0].ID != childID || children[0].BotID != f.b.ID.String() {
				t.Fatalf("parent linkage=%+v err=%v", children, err)
			}
			compactor := persistentsession.NewCompactor(1024)
			compactor.PreserveRecent = 1
			compactor.LLM = func(context.Context, string) (string, error) { return "Earlier index observations summarized.", nil }
			compacted, err := compactor.CompactContents(f.ctx, []*genai.Content{genai.NewContentFromText(initial, "user"), genai.NewContentFromText("Earlier observations", "model"), genai.NewContentFromText("Keep this latest result", "user")})
			if err != nil {
				t.Fatal(err)
			}
			for i, content := range compacted {
				event := &session.Event{ID: fmt.Sprintf("shared-compaction-%d", i), Author: "model", LLMResponse: model.LLMResponse{Content: content}, Actions: session.EventActions{StateDelta: map[string]any{"compaction_seen": true}}}
				if err = ss.AppendFleetEvent(f.ctx, childID, event); err != nil {
					t.Fatal(err)
				}
			}
			get := func() *session.GetResponse {
				r, e := ss.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.b.OwnerID.String(), SessionID: childID, NumRecentEvents: 1})
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			for range 2 {
				r := get()
				if value, err := r.Session.State().Get("market_selection"); err != nil || value != initial {
					t.Fatalf("bounded reload lost initial state: %v %v", value, err)
				}
				if value, err := r.Session.State().Get("compaction_seen"); err != nil || value != true {
					t.Fatalf("state delta replay: %v %v", value, err)
				}
			}
			events, err := ss.ReadTranscriptEvents(f.ctx, "astonish", f.b.OwnerID.String(), childID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(events)
			if !strings.Contains(string(raw), "Context Summary") || !strings.Contains(string(raw), initial) || calls != 1 {
				t.Fatalf("compaction/reload lost anchor or executed delegate: %s calls=%d", raw, calls)
			}
			if after := databaseRuntimeFileSnapshot(t, f.dataDir); !reflect.DeepEqual(before, after) {
				t.Fatal("delegate/state replay wrote runtime files")
			}
		})
	}
}
