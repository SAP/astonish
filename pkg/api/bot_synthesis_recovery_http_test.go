package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This is running HTTP acceptance, not a direct synthesis-wrapper test. The
// production worker/factory owns admission, metering, reserve transitions and
// publication; only the model and isolated tool-node transport are controlled.
func TestBotSynthesisRecoveryHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, missingTerminal := range []bool{false, true} {
			name := string(scope) + "/output_limit"
			if missingTerminal {
				name = string(scope) + "/missing_terminal"
			}
			t.Run(name, func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, scope)
				f.worker.Stop()
				version, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
				if err != nil {
					t.Fatal(err)
				}
				// A genuinely small, owner-reviewed run budget enters synthesis
				// after the first real tool read, without sleeps or a fake clock.
				limit := int64(2000)
				version.Budget.MaxTokens = &limit
				body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated synthesis recovery HTTP acceptance", Autonomy: version.Autonomy, Budget: version.Budget, Delivery: version.Delivery})
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
					t.Fatal(err)
				}
				workerConfig := f.workerConfig
				// Deterministic model-scoped accounting, not a second allowance.
				workerConfig.ModelOptions.EstimateInput = func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }
				var meter interface{ Usage() store.BotUsage }
				prepare := workerConfig.Prepare
				preparations := 0
				workerConfig.Prepare = func(ctx context.Context, req bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					preparations++
					meter, _ = req.Budget.(interface{ Usage() store.BotUsage })
					return prepare(ctx, req, scope)
				}
				worker, err := daemon.NewBotWorker(workerConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
				const draft = "UNPUBLISHED_TRUNCATED_DRAFT: the receipt indicates"
				const thought = "PRIVATE_SYNTHESIS_REASONING"
				const final = "The scoped research file contains isolated receipt."
				calls := 0
				var ceiling int32
				f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					calls++
					response := &model.LLMResponse{Content: genai.NewContentFromText(final, "model"), FinishReason: genai.FinishReasonStop, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}
					if calls == 1 {
						response.Content.Parts = []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "recovery-receipt", Name: "read_file", Args: map[string]any{"path": "/isolated/receipt.txt"}}}}
						return response, true
					}
					raw, _ := json.Marshal(req.Contents)
					if !strings.Contains(string(raw), "Synthesis reserve has begun") || !strings.Contains(string(raw), "isolated receipt") {
						t.Errorf("synthesis did not retain real tool evidence: %s", raw)
					}
					if len(req.Tools) != 0 || req.Config == nil || len(req.Config.Tools) != 0 || req.Config.ToolConfig != nil || req.Config.MaxOutputTokens <= 0 {
						t.Error("synthesis/correction retained tools or lacked an output ceiling")
					}
					switch calls {
					case 2:
						ceiling = req.Config.MaxOutputTokens
						response.Content.Parts = []*genai.Part{{Text: thought, Thought: true}, {Text: draft}}
						response.TurnComplete = false
						response.FinishReason = genai.FinishReasonMaxTokens
						if missingTerminal {
							response.FinishReason = ""
						}
					case 3:
						if missingTerminal {
							t.Error("missing terminal metadata incorrectly authorized a retry")
						}
						if req.Config.MaxOutputTokens != ceiling || !strings.Contains(string(raw), draft) || strings.Contains(string(raw), thought) {
							t.Error("correction changed ceiling, lost visible draft, or retained private reasoning")
						}
						correction := req.Contents[len(req.Contents)-1].Parts[0].Text
						if !strings.Contains(correction, "complete") || !strings.Contains(correction, "concise") {
							t.Error("correction is not a complete concise rewrite")
						}
						if meter == nil {
							t.Error("production run budget missing")
						} else if usage := meter.Usage(); usage.InputTokens != 110 || usage.OutputTokens != 6+int64(ceiling) || usage.ToolCalls != 1 {
							// Usage includes this correction's active 100-token input
							// and ceiling-sized output reservation, plus settled 10/6.
							t.Errorf("original budget lost prior settlement or correction reservation: %+v", usage)
						}
					default:
						t.Errorf("unexpected model/review/recovery call %d", calls)
					}
					return response, true
				}

				// Attach before admission: this checks live delivery, not just two
				// reads of restored history. The bounded context owns the SSE wait.
				streamCtx, cancelStream := context.WithTimeout(f.ctx, 20*time.Second)
				defer cancelStream()
				home := f.path + "/sessions/" + f.b.HomeSessionID.String()
				request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, f.server.URL+home+"/events?scope="+string(scope), nil)
				if err != nil {
					t.Fatal(err)
				}
				live, err := f.server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer live.Body.Close()
				if live.StatusCode != http.StatusOK {
					t.Fatalf("live stream status=%d", live.StatusCode)
				}
				var admitted store.BotMessageReceipt
				if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read /isolated/receipt.txt and report its contents.","idempotency_key":"synthesis-recovery"}`, f.b.Version), 202), &admitted); err != nil {
					t.Fatal(err)
				}
				if err := worker.Poll(f.ctx); err != nil {
					t.Fatal(err)
				}
				var run store.BotRun
				runJSON := f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200)
				if err := json.Unmarshal(runJSON, &run); err != nil {
					t.Fatal(err)
				}
				wantCalls := 3
				if missingTerminal {
					wantCalls = 2
					if run.Status == store.RunSucceeded || run.Status == store.RunRunning || !strings.Contains(string(runJSON), "reason=missing_terminal") {
						t.Fatalf("missing-terminal did not fail closed with evidence: %s", runJSON)
					}
				} else if run.Status != store.RunSucceeded || run.Report.Summary != final {
					t.Fatalf("recovery failed: status=%s report=%+v", run.Status, run.Report)
				}
				if calls != wantCalls || preparations != 1 || f.model.reviews != 0 || f.pool.calls != 1 || run.Usage.ToolCalls != 1 || run.Usage.InputTokens != int64(wantCalls*5) || run.Usage.OutputTokens != int64(wantCalls*3) {
					t.Fatalf("extra work or lost cumulative usage: calls=%d preparations=%d reviews=%d reads=%d usage=%+v", calls, preparations, f.model.reviews, f.pool.calls, run.Usage)
				}
				marker := "bot-report:" + run.ID.String()
				var liveText strings.Builder
				scanner := bufio.NewScanner(live.Body)
				for scanner.Scan() {
					liveText.WriteString(scanner.Text())
					liveText.WriteByte('\n')
					if strings.Contains(scanner.Text(), marker) {
						break
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				cancelStream()
				assertPublication := func(label, text string) {
					t.Helper()
					if strings.Contains(text, draft) || strings.Contains(text, thought) {
						t.Fatalf("%s leaked unaccepted model output: %s", label, text)
					}
					if strings.Count(text, marker) != 1 {
						t.Fatalf("%s does not contain exactly one final publication: %s", label, text)
					}
					if !missingTerminal && strings.Count(text, final) != 1 {
						t.Fatalf("%s missing exactly one complete answer: %s", label, text)
					}
					if missingTerminal && strings.Contains(text, final) {
						t.Fatalf("%s published an answer after missing-terminal failure", label)
					}
				}
				assertPublication("live", liveText.String())
				for range 2 {
					assertPublication("restored", string(f.request("GET", home+"/stream", "", 200)))
					if err := worker.Poll(f.ctx); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("scope=%s missing_terminal=%t status=%s model_calls=%d preparations=%d reads=%d input_tokens=%d output_tokens=%d live_final=1 restored_final=1 draft_leaks=0", scope, missingTerminal, run.Status, calls, preparations, f.pool.calls, run.Usage.InputTokens, run.Usage.OutputTokens)
			})
		}
	}
}
