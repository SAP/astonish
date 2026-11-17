package api_test

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestBotProgressRecoveryHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
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
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "durable-read", Name: "read_file", Args: map[string]any{"path": "/isolated/receipt.txt"}}}}}}, true
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
			if strings.Count(history, "bot-report:"+after.ID.String()) != 1 {
				t.Fatalf("final not exactly once: %s", history)
			}
		})
	}
}
