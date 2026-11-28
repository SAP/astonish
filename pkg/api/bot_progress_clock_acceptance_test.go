package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Only the employee evaluation clock advances. Lease/auth/operation deadlines
// remain real: this is accelerated cadence evidence, NOT a wall-clock soak.
type progressClockModel struct {
	model.LLM
	now       time.Time
	t         *testing.T
	installed bool
}

func (m *progressClockModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if c := bot.ProgressControllerFromContext(ctx); c != nil {
		c.Now = func() time.Time { return m.now }
		if !m.installed {
			m.installed = true
			commit := c.Commit
			c.Commit = func(ctx context.Context, o bot.ProgressObservation, d bot.ProgressDecision, at time.Time) error {
				err := commit(ctx, o, d, at)
				if err != nil {
					m.t.Errorf("clock commit: %v settled=%t refs=%v", err, o.Settled, o.EvidenceRefs)
				}
				return err
			}
		}
	}
	return m.LLM.GenerateContent(ctx, req, stream)
}

func TestBotProgressSixHourClockAcceptance(t *testing.T) {
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
			raw, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated six-hour cadence", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
			if err = json.Unmarshal(f.request("PUT", f.path+"/configuration", string(raw), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			*f.policy = nil
			progressResumeFixture(t, f)
			start := time.Now()
			clock := &progressClockModel{LLM: progressEmployeeFixture(progressMeteredModel(f.model)), now: start, t: t}
			cfg := f.workerConfig
			cfg.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return clock, nil
			}
			f.workerConfig = cfg
			steps, evaluations := 0, 0
			var previous store.BotUsage
			assignment := "Read three independent observations and compare them without losing this requirement."
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				t.Logf("clock model steps=%d mime=%s tools=%d", steps, req.Config.ResponseMIMEType, len(req.Config.Tools))
				prompt := ""
				if req.Config != nil && req.Config.SystemInstruction != nil {
					for _, p := range req.Config.SystemInstruction.Parts {
						prompt += p.Text
					}
				}
				if strings.Contains(prompt, "BOT_EMPLOYEE_PROGRESS_V1") && req.Config.ResponseMIMEType == "application/json" {
					evaluations++
					var observation struct {
						Observation bot.ProgressObservation `json:"observation"`
					}
					// The controller keeps its trusted JSON-mode instruction separate
					// from the observation payload with a blank line.
					instruction, payload, ok := strings.Cut(req.Contents[0].Parts[0].Text, "\n\n")
					if !ok || !strings.HasPrefix(instruction, "Return JSON") {
						t.Fatal("progress request lacks JSON-mode instruction and observation payload")
					}
					if err := json.Unmarshal([]byte(payload), &observation); err != nil {
						t.Fatal(err)
					}
					if observation.Observation.Assignment != assignment || (observation.Observation.Settled && len(observation.Observation.EvidenceRefs) != len(progressPrivateResponses(t, f))) {
						t.Fatalf("evaluation lacks measured original work: %+v", observation)
					}
					decision := "continue"
					if evaluations == 2 {
						decision = "replan"
					}
					return &model.LLMResponse{Content: genai.NewContentFromText(fmt.Sprintf(`{"decision":%q,"summary":"Compare the saved observations before answering.","next_steps":["Compare observations"],"evidence_refs":[]}`, decision), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 4, TotalTokenCount: 11}}, true
				}
				if strings.Contains(prompt, "REVIEW") {
					t.Fatal("progress invoked a review model")
				}
				if steps < 3 {
					steps++
					f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
						f.pool.callOverride = nil
						result, err := f.pool.Call("", name, args)
						f.pool.calls--
						clock.now = clock.now.Add(2*time.Hour + time.Second)
						return result, err
					}
					return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("clock-read-%d", steps), Name: "read_file", Args: map[string]any{"path": "receipt.txt"}}}}}}, true
				}
				rows, e := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
				if e != nil || len(rows) != 1 {
					t.Fatalf("run continuity: %v %+v", e, rows)
				}
				previous = rows[0].Usage
				return &model.LLMResponse{Content: genai.NewContentFromText("All three saved observations agree: isolated receipt.", "model")}, true
			}
			var receipt store.BotMessageReceipt
			raw, _ = json.Marshal(map[string]any{"version": f.b.Version, "instructions": assignment, "idempotency_key": "six-hour"})
			if err = json.Unmarshal(f.request("POST", f.path+"/messages", string(raw), 202), &receipt); err != nil {
				t.Fatal(err)
			}
			if err = progressFreshWorker(t, f).Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			run := progressGetRun(t, f, receipt.Run.ID)
			if run.Status != store.RunSucceeded || evaluations != 3 || f.pool.calls != 3 || run.Usage.ToolCalls != 3 || run.Usage.InputTokens < previous.InputTokens || run.Usage.OutputTokens < previous.OutputTokens {
				t.Fatalf("six-hour run failed: %+v failure=%+v evaluations=%d reads=%d", run, run.Report.Failure, evaluations, f.pool.calls)
			}
			if clock.now.Sub(start) < 6*time.Hour {
				t.Fatal("clock did not span six hours")
			}
			history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
			assertProgressReportText(t, history, "bot-report:"+run.ID.String(), "All three saved observations agree: isolated receipt.")
			t.Logf("controller_clock_seconds=%d evaluations=%d run=%s final_publications=1 tool_calls=%d", int(clock.now.Sub(start).Seconds()), evaluations, run.ID, run.Usage.ToolCalls)
		})
	}
}
