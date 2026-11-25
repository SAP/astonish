package api_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider/httpool"
	openaiprovider "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/store"
	goopenai "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

var botHealthLongGeneration = flag.Bool("bot-health-long-generation", false, "run isolated real-clock 130-second Bot Responses transport acceptance")

func TestBotGenerationHealthHTTP130Seconds(t *testing.T) {
	if !*botHealthLongGeneration {
		t.Skip("explicit opt-in: -args -bot-health-long-generation")
	}
	runBotGenerationHealthHTTP(t, 130*time.Second, 10*time.Second)
}

func TestBotGenerationHealthHTTPQuick(t *testing.T) {
	runBotGenerationHealthHTTP(t, 150*time.Millisecond, 25*time.Millisecond)
}

// Both tests use precisely the same production worker/factory/budget/dispatch
// path. The quick test is not evidence for exceeding the old elapsed deadline.
type generationHealthScenario int

const (
	generationHealthHealthy generationHealthScenario = iota
	generationHealthStalled
	generationHealthCancelled
	generationHealthRevoked
)

func TestBotGenerationHealthHTTPStops(t *testing.T) {
	for name, scenario := range map[string]generationHealthScenario{"stalled": generationHealthStalled, "cancelled": generationHealthCancelled, "revoked": generationHealthRevoked} {
		t.Run(name, func(t *testing.T) { runBotGenerationHealthHTTP(t, 0, 25*time.Millisecond, scenario) })
	}
}

func runBotGenerationHealthHTTP(t *testing.T, duration, cadence time.Duration, scenarios ...generationHealthScenario) {
	scenario := generationHealthHealthy
	if len(scenarios) > 0 {
		scenario = scenarios[0]
	}
	f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "receipt.txt"}, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	defer f.worker.Stop()
	*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
		return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
	}
	v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
	if err != nil {
		t.Fatal(err)
	}
	v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
	if v.Budget.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds != 120 || v.Budget.RuntimePolicy.GenerationHealth.CheckIntervalSeconds != 10 {
		t.Fatal("unexpected trusted health policy")
	}
	if scenario != generationHealthHealthy {
		v.Budget.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds = 2
		v.Budget.RuntimePolicy.GenerationHealth.CheckIntervalSeconds = 1
	}
	body, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Isolated real HTTP generation health acceptance", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
	if err := json.Unmarshal(f.request("PUT", f.path+"/configuration", string(body), 200), &f.b); err != nil {
		t.Fatal(err)
	}
	*f.policy = nil // Same explicitly reviewed caller-bound authority as employee HTTP acceptance.
	var productions, requests, completions, pulses atomic.Int64
	started := make(chan time.Time, 1)
	release := make(chan struct{})
	var releaseOnce atomic.Bool
	releaseFinal := func() {
		if releaseOnce.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer releaseFinal()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var wire struct {
			Model        string `json:"model"`
			Instructions string `json:"instructions"`
			Input        []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
			Stream          bool `json:"stream"`
			MaxOutputTokens int  `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/responses" || wire.Model != "gpt-6" || wire.MaxOutputTokens <= 0 {
			t.Errorf("wrong provider request path=%s model=%s bound=%d", r.URL.Path, wire.Model, wire.MaxOutputTokens)
			http.Error(w, "wrong request", 400)
			return
		}
		req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText(wire.Instructions, "system")}}
		for _, input := range wire.Input {
			var text string
			if json.Unmarshal(input.Content, &text) == nil {
				req.Contents = append(req.Contents, genai.NewContentFromText(text, genai.Role(input.Role)))
			}
		}
		response, auxiliary := scriptedEmployeeReview(req)
		if !auxiliary {
			response, auxiliary = f.model.assessment(req)
		}
		if !auxiliary && strings.Contains(wire.Instructions, "BOT_EMPLOYEE_PROGRESS_V1") {
			response = &model.LLMResponse{Content: genai.NewContentFromText(`{"decision":"continue","summary":"The scoped receipt is available.","next_steps":["Return the scoped observation"],"evidence_refs":[]}`, "model")}
			auxiliary = true
		}
		output := []any{}
		if auxiliary {
			if response == nil || response.Content == nil {
				t.Error("missing auxiliary fixture response")
				return
			}
			output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": response.Content.Parts[0].Text}}})
		} else if productions.Add(1) == 1 {
			output = append(output, map[string]any{"type": "function_call", "call_id": "receipt-read", "name": "read_file", "arguments": `{"path":"receipt.txt"}`})
		} else {
			if !wire.Stream {
				t.Error("health-enabled main generation did not use Responses SSE")
				http.Error(w, "stream required", 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
			w.(http.Flusher).Flush()
			started <- time.Now()
			ticker := time.NewTicker(cadence)
			defer ticker.Stop()
			for waiting := true; waiting; {
				select {
				case <-r.Context().Done():
					return
				case <-release:
					waiting = false
				case <-ticker.C:
					if scenario == generationHealthStalled {
						continue
					}
					// Real upstream bytes, not local monitor heartbeats. None are public output.
					fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"PRIVATE_HEALTH_REASONING\"}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"PRIVATE_PARTIAL_TOOL\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"PRIVATE_PARTIAL_ANSWER\"}\n\n")
					w.(http.Flusher).Flush()
					pulses.Add(1)
				}
			}
			output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Observed scoped result."}}})
		}
		final := map[string]any{"id": "response-local", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8}}
		if wire.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": final})
			fmt.Fprintf(w, "data: %s\n\n", raw)
			w.(http.Flusher).Flush()
			completions.Add(1)
			// Deliberately keep socket open: completion, not EOF, must release the worker.
			<-r.Context().Done()
		} else {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(final)
			completions.Add(1)
		}
	}))
	defer upstream.Close()
	cfg := goopenai.DefaultConfig("local-only-not-a-credential")
	cfg.BaseURL = upstream.URL
	cfg.HTTPClient = httpool.StreamingClient()
	actual := openaiprovider.NewProviderWithResponses(goopenai.NewClientWithConfig(cfg), "gpt-6", true, 100)
	f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"openai","model":"gpt-6"}`, 200)
	workerConfig := f.workerConfig
	var revoked atomic.Bool
	if scenario == generationHealthRevoked {
		prepare := workerConfig.Prepare
		workerConfig.Prepare = func(ctx context.Context, req bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
			safe, err := prepare(ctx, req, scope)
			if err != nil {
				return safe, err
			}
			verify := safe.VerifyIsolation
			safe.VerifyIsolation = func(ctx context.Context) error {
				if revoked.Load() {
					return store.ErrBotForbidden
				}
				return verify(ctx)
			}
			return safe, nil
		}
	}
	workerConfig.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return actual, nil
	}
	worker, err := daemon.NewBotWorker(workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	var admitted store.BotMessageReceipt
	if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"instructions":"Read the scoped receipt and report the observation.","idempotency_key":"health-transport"}`, f.b.Version), 202), &admitted); err != nil {
		t.Fatal(err)
	}
	pollCtx, cancel := context.WithTimeout(f.ctx, duration+45*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- worker.Poll(pollCtx) }()
	var began time.Time
	select {
	case began = <-started:
	case err := <-finished:
		t.Fatalf("worker stopped before long generation: %v", err)
	case <-pollCtx.Done():
		t.Fatal("generation never started")
	}
	if scenario != generationHealthHealthy {
		switch scenario {
		case generationHealthCancelled:
			f.request("POST", f.path+"/runs/"+admitted.Run.ID.String()+"/cancel", "{}", 202)
		case generationHealthRevoked:
			revoked.Store(true) // Controlled trusted authority callback rejects while upstream stays active.
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("health/control did not stop in-flight generation")
		}
		var stopped store.BotRun
		if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200), &stopped); err != nil {
			t.Fatal(err)
		}
		if stopped.Status == store.RunSucceeded || stopped.Status == store.RunRunning {
			t.Fatalf("unsafe status after stop: %s", stopped.Status)
		}
		if scenario == generationHealthCancelled && stopped.Status != store.RunCancelled && stopped.Status != store.RunInterrupted {
			t.Fatalf("cancel status=%s", stopped.Status)
		}
		history := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
		for _, secret := range []string{"PRIVATE_HEALTH_REASONING", "PRIVATE_PARTIAL_TOOL", "PRIVATE_PARTIAL_ANSWER", "Observed scoped result."} {
			if strings.Contains(history, secret) {
				t.Fatalf("published unfinished output %s", secret)
			}
		}
		if completions.Load() != 1 || f.pool.calls != 1 || stopped.Usage.ToolCalls != 1 || stopped.Usage.InputTokens < 5 || stopped.Usage.OutputTokens < 3 {
			t.Fatalf("lost settled usage or completed cancelled generation: completions=%d calls=%d usage=%+v", completions.Load(), f.pool.calls, stopped.Usage)
		}
		t.Logf("scenario=%d elapsed=%s status=%s completed_responses=%d usage=%+v private_stream_leaks=0", scenario, time.Since(began), stopped.Status, completions.Load(), stopped.Usage)
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case err := <-finished:
		t.Fatalf("worker ended before required real duration: %v", err)
	case <-pollCtx.Done():
		t.Fatal(pollCtx.Err())
	}
	elapsed := time.Since(began)
	if elapsed < duration {
		t.Fatalf("real elapsed %s < required %s", elapsed, duration)
	}
	if pulses.Load() == 0 || duration >= 130*time.Second && pulses.Load() < 12 {
		t.Fatalf("missing real upstream event cadence: %d", pulses.Load())
	}
	var during store.BotRun
	if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200), &during); err != nil {
		t.Fatal(err)
	}
	if during.Status != store.RunRunning {
		t.Fatalf("same run not running after %s: %s", elapsed, during.Status)
	}
	assertPrivate := func(history string) {
		t.Helper()
		for _, secret := range []string{"PRIVATE_HEALTH_REASONING", "PRIVATE_PARTIAL_TOOL", "PRIVATE_PARTIAL_ANSWER"} {
			if strings.Contains(history, secret) {
				t.Fatalf("private stream data published: %s", secret)
			}
		}
	}
	before := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
	assertPrivate(before)
	if strings.Contains(before, "bot-report:"+admitted.Run.ID.String()) || strings.Contains(before, "Observed scoped result.") {
		t.Fatal("published final before response.completed")
	}
	releaseFinal()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-pollCtx.Done():
		t.Fatal("worker did not finish at response.completed")
	}
	var run store.BotRun
	if err := json.Unmarshal(f.request("GET", f.path+"/runs/"+admitted.Run.ID.String(), "", 200), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != during.ID || run.Lease.Fence != during.Lease.Fence {
		t.Fatal("generation switched run/lease attempt")
	}
	if run.Status != store.RunSucceeded {
		t.Fatalf("status=%s report=%+v failure=%+v", run.Status, run.Report, run.Report.Failure)
	}
	if productions.Load() != 2 || f.pool.calls != 1 || run.Usage.ToolCalls != 1 {
		t.Fatalf("duplicated/restarted work: generations=%d reads=%d usage=%+v", productions.Load(), f.pool.calls, run.Usage)
	}
	if run.Usage.InputTokens != requests.Load()*5 || run.Usage.OutputTokens != requests.Load()*3 {
		t.Fatalf("cumulative usage lost: requests=%d usage=%+v", requests.Load(), run.Usage)
	}
	after := string(f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200))
	assertPrivate(after)
	if canonicalHTTPReportCount(t, after, "bot-report:"+run.ID.String()) != 1 || canonicalHTTPTextCount(t, after, "Observed scoped result.") != 1 {
		t.Fatalf("missing exactly-one Home final: %s", after)
	}
	t.Logf("upstream_event_batches=%d same_lease_fence=%d", pulses.Load(), run.Lease.Fence)
	t.Logf("real_elapsed=%s run=%s status=%s model=gpt-6 health_idle=120s check=10s requests=%d completions=%d tool_calls=%d input_tokens=%d output_tokens=%d home_final=1 private_stream_leaks=0", elapsed, run.ID, run.Status, requests.Load(), completions.Load(), run.Usage.ToolCalls, run.Usage.InputTokens, run.Usage.OutputTokens)
}
