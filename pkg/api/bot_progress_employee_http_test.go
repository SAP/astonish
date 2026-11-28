package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// assertProgressReportText counts public text frames, not occurrences of the
// report identity (which also appears in canonical projection metadata).
func assertProgressReportText(t *testing.T, history, invocationID, wantText string) {
	t.Helper()
	scan := bufio.NewScanner(strings.NewReader(history))
	scan.Buffer(make([]byte, 4096), 1<<20)
	var event string
	var data []string
	count := 0
	consume := func() {
		if event != "text" {
			return
		}
		var payload struct {
			InvocationID string `json:"invocationId"`
			Text         string `json:"text"`
		}
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.InvocationID == invocationID {
			count++
			if payload.Text != wantText {
				t.Fatalf("report text = %q, want %q", payload.Text, wantText)
			}
		}
	}
	for scan.Scan() {
		line := scan.Text()
		switch {
		case line == "":
			consume()
			event, data = "", nil
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	consume()
	if count != 1 {
		t.Fatalf("final text publications = %d, want 1: %s", count, history)
	}
}

// Real HTTP admission, owner factory, worker and SQLite persistence. Provider
// responses and the isolated tool-node transport are controlled, never live.
func TestBotProgressEmployeeHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newProgressHTTPFixture(t, scope)
			*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
			}
			v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
			if err != nil {
				t.Fatal(err)
			}
			v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			v.Budget.RuntimePolicy.EvaluationIntervalSeconds = 1
			body, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated employee progress acceptance", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			// This fixture exercises caller-bound interactive authority, not durable
			// mandate issuance. The resolver above authorizes the reviewed policy.
			*f.policy = nil
			progressResumeFixture(t, f)
			evaluations, steps := 0, 0
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				text := ""
				if req.Config != nil && req.Config.SystemInstruction != nil {
					for _, p := range req.Config.SystemInstruction.Parts {
						text += p.Text
					}
				}
				if strings.Contains(text, "BOT_EMPLOYEE_PROGRESS_V1") {
					evaluations++
					decision := "continue"
					if evaluations == 2 {
						decision = "replan"
					}
					return &model.LLMResponse{Content: genai.NewContentFromText(fmt.Sprintf(`{"decision":%q,"summary":"The file evidence is available; compare the remaining observations.","next_steps":["Compare observations before answering"],"evidence_refs":[]}`, decision), "model")}, true
				}
				steps++
				if steps <= 3 {
					time.Sleep(1100 * time.Millisecond)
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("read-%d", steps), Name: "read_file", Args: map[string]any{"path": "receipt.txt"}}}}}}, true
				}
				return &model.LLMResponse{Content: genai.NewContentFromText("The three observations agree: isolated receipt.", "model")}, true
			}
			var admitted store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read the receipt three times and compare observations.","idempotency_key":"progress-employee"}`, f.b.Version), 202), &admitted); err != nil {
				t.Fatal(err)
			}
			if err := f.worker.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			var run store.BotRun
			if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200), &run); err != nil {
				t.Fatal(err)
			}
			if run.Status != store.RunSucceeded || evaluations < 2 || f.pool.calls != 3 {
				t.Fatalf("status=%s evaluations=%d reads=%d report=%+v failure=%+v", run.Status, evaluations, f.pool.calls, run.Report, run.Report.Failure)
			}
			if run.Usage.ToolCalls != 3 {
				t.Fatalf("usage reset or duplicated: %+v", run.Usage)
			}
			history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
			assertProgressReportText(t, history, "bot-report:"+run.ID.String(), "The three observations agree: isolated receipt.")
		})
	}
}
