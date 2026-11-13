package api_test

// Opt-in actual HTTP/worker/SQLite acceptance. Source responses are recorded
// fixtures, NOT fresh network fetches. Every model stage uses the real provider.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func realStockSourceFixture(raw []byte) ([]byte, int, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 65536), 4<<20)
	var sources []map[string]any
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Content   *genai.Content
			Partial   bool
			Timestamp time.Time
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return nil, 0, err
		}
		if event.Partial || event.Content == nil {
			continue
		}
		for _, p := range event.Content.Parts {
			if p == nil || p.FunctionResponse == nil {
				continue
			}
			r := p.FunctionResponse
			// Only observed web responses, not the original model's draft or verdict.
			if r.Name != "web_fetch" {
				continue
			}
			sources = append(sources, map[string]any{"tool": r.Name, "captured_at": event.Timestamp, "response": r.Response})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	result, err := json.MarshalIndent(map[string]any{"provenance": "Recorded web_fetch responses from the user's original Stock Market News session. Historical controlled source fixture; not a current market feed or new external fetch.", "sources": sources}, "", "  ")
	return result, len(sources), err
}

func TestRealStockSourcesFixture(t *testing.T) {
	raw := []byte("data: {\"Content\":{\"parts\":[{\"functionResponse\":{\"name\":\"web_fetch\",\"response\":{\"url\":\"https://example.test\",\"content\":\"recorded source\"}}}]}}\n")
	body, n, err := realStockSourceFixture(raw)
	if err != nil || n != 1 || !strings.Contains(string(body), "recorded source") {
		t.Fatalf("source extraction: n=%d error=%v", n, err)
	}
	if _, _, err := realStockSourceFixture([]byte("data: {bad}\n")); err == nil {
		t.Fatal("invalid recorded JSON accepted")
	}
}

type realStockHTTPRecorder struct {
	inner                     model.LLM
	dir                       string
	mu                        sync.Mutex
	calls, producers, reviews int
	tokens                    int64
	beforeReview              func(*model.LLMRequest) error
	redact                    []string // explicit config secrets; never serialize these
}

func (m *realStockHTTPRecorder) Name() string { return m.inner.Name() }
func (m *realStockHTTPRecorder) save(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(m.redact) > 0 {
		var safe any
		if json.Unmarshal(raw, &safe) != nil {
			return fmt.Errorf("diagnostic redaction failed")
		}
		safe = redactStockDiagnostic(safe)
		raw, err = json.MarshalIndent(safe, "", "  ")
		if err != nil {
			return fmt.Errorf("diagnostic redaction failed")
		}
		for _, secret := range m.redact {
			if secret == "" {
				continue
			}
			encoded, _ := json.Marshal(secret)
			raw = []byte(strings.ReplaceAll(string(raw), string(encoded[1:len(encoded)-1]), "[redacted]"))
		}
	}
	f, err := os.OpenFile(filepath.Join(m.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (m *realStockHTTPRecorder) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		prompt := ""
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				if p != nil {
					prompt += p.Text
				}
			}
		}
		stage := "producer"
		for _, s := range []string{"BOT_SKILL_ORIENT_V1", "BOT_WORK_PREPARE_V1", "BOT_WORK_REVIEW_V1", "BOT_EMPLOYEE_MESSAGE", "BOT_SKILL_LEARN"} {
			if strings.HasPrefix(prompt, s) {
				stage = s
				break
			}
		}
		m.mu.Lock()
		m.calls++
		n := m.calls
		if stage == "BOT_WORK_REVIEW_V1" {
			m.reviews++
		} else if len(req.Tools) > 0 || (req.Config != nil && len(req.Config.Tools) > 0) {
			m.producers++
		}
		exhausted := n > 24 || m.tokens > 500000
		m.mu.Unlock()
		if exhausted {
			yield(nil, fmt.Errorf("acceptance safety ceiling reached (24 calls/500000 actual tokens)"))
			return
		}
		prefix := fmt.Sprintf("%02d-%s", n, stage)
		if err := m.save(prefix+"-request.json", req); err != nil {
			yield(nil, err)
			return
		}
		if stage == "BOT_WORK_REVIEW_V1" && m.beforeReview != nil {
			if err := m.beforeReview(req); err != nil {
				yield(nil, err)
				return
			}
		}
		i := 0
		for response, err := range m.inner.GenerateContent(ctx, req, streaming) {
			if err != nil && len(m.redact) > 0 {
				err = fmt.Errorf("same-provider model call failed (details withheld)")
			}
			i++
			if saveErr := m.save(fmt.Sprintf("%s-response-%04d.json", prefix, i), map[string]any{"response": response, "error": fmt.Sprint(err)}); saveErr != nil {
				yield(nil, saveErr)
				return
			}
			if response != nil && !response.Partial && response.UsageMetadata != nil {
				m.mu.Lock()
				m.tokens += int64(response.UsageMetadata.TotalTokenCount)
				m.mu.Unlock()
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func TestBotRealStockHTTPAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_STOCK_HTTP") != "1" {
		t.Skip("paid real-provider HTTP acceptance; explicitly opt in")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	configRaw, err := os.ReadFile(filepath.Join(root, ".tmp", "employee-native-config.json"))
	if err != nil {
		t.Fatal("private provider config unavailable")
	}
	var cfg struct {
		URL   string `json:"provider_url"`
		Key   string `json:"provider_api_key"`
		Model string `json:"model"`
	}
	if json.Unmarshal(configRaw, &cfg) != nil || cfg.URL == "" || cfg.Key == "" || cfg.Model == "" {
		t.Fatal("explicit provider configuration missing")
	}
	if cfg.URL == "http://host.docker.internal:4000" {
		cfg.URL = "http://127.0.0.1:4000"
	}
	raw, err := os.ReadFile(filepath.Join(root, ".tmp", "real-review-stock-session.sse"))
	if err != nil {
		t.Fatal(err)
	}
	sources, count, err := realStockSourceFixture(raw)
	if err != nil || count != 9 {
		t.Fatalf("expected nine recorded web sources, got %d: %v", count, err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".tmp"), "real-stock-http-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Logf("private artifacts: %s; model=%s; controlled recorded sources=%d (not fresh fetches)", dir, cfg.Model, count)
	rec := &realStockHTTPRecorder{inner: litellm.NewProvider(cfg.Key, cfg.URL, cfg.Model), dir: dir}
	save := func(name string, v any) {
		t.Helper()
		if err := rec.save(name, v); err != nil {
			t.Fatal(err)
		}
	}
	save("sources.json", json.RawMessage(sources))
	f := newInteractiveHTTPFixture(t, "read_file", nil, []string{"chat", "tool:execute"})
	f.worker.Stop() // The controlled model has never run; replace its entire worker.
	f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", fmt.Sprintf(`{"provider":"litellm","model":%q}`, cfg.Model), 200)
	f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
		if name != "read_file" || args["path"] != "/isolated/stock-market-sources.json" {
			return nil, fmt.Errorf("controlled source fixture only supports read_file /isolated/stock-market-sources.json, got %s", name)
		}
		return json.Marshal(map[string]any{"content": string(sources), "path": "/isolated/stock-market-sources.json"})
	}
	f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 200000, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return rec, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if len(q.Version.Autonomy.AllowedTools) != 0 {
			return nil, fmt.Errorf("unexpected added autonomy")
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer f.worker.Stop()
	historyPath := f.path + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
	var candidates []string
	rec.beforeReview = func(req *model.LLMRequest) error {
		if len(req.Tools) > 0 || (req.Config != nil && len(req.Config.Tools) > 0) {
			return fmt.Errorf("reviewer obtained tools")
		}
		var work store.BotWorkState
		if len(req.Contents) == 0 || len(req.Contents[0].Parts) == 0 {
			return fmt.Errorf("missing review work")
		}
		if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work); err != nil {
			return err
		}
		if work.Candidate == nil || work.Candidate.Text == "" {
			return fmt.Errorf("missing actual producer candidate")
		}
		candidates = append(candidates, work.Candidate.Text)
		history := f.request("GET", historyPath, "", 200)
		if strings.Contains(string(history), "bot-report:") {
			return fmt.Errorf("report published before acceptance")
		}
		return rec.save(fmt.Sprintf("home-before-review-%d.json", len(candidates)), string(history))
	}
	instructions := "Stock Market News. Read /isolated/stock-market-sources.json for the nine recorded source responses supplied for this briefing. These are historical captured sources, not a fresh external fetch; base the briefing on that material and make its timing and limitations clear."
	body, _ := json.Marshal(map[string]any{"version": f.b.Version, "instructions": instructions, "idempotency_key": "real-stock-once"})
	var receipt store.BotMessageReceipt
	admission := f.request("POST", f.path+"/messages", string(body), 202)
	save("admission.json", json.RawMessage(admission))
	if err = json.Unmarshal(admission, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Run.IsInteractiveChat() {
		t.Fatal("not actual interactive HTTP admission")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	started := time.Now()
	pollErr := f.worker.Poll(ctx)
	var run store.BotRun
	runRaw := f.request("GET", f.path+"/runs/"+receipt.Run.ID.String(), "", 200)
	save("run.json", json.RawMessage(runRaw))
	if err = json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	history := f.request("GET", historyPath, "", 200)
	save("home.json", string(history))
	save("execution.json", map[string]any{"elapsed_seconds": time.Since(started).Seconds(), "calls": rec.calls, "producer_calls": rec.producers, "review_calls": rec.reviews, "tokens": rec.tokens, "source_read_calls": f.pool.calls, "poll_error": fmt.Sprint(pollErr), "note": "Actual HTTP owner admission, production worker/ChatRunner and scoped temporary SQLite; real model for all stages; controlled recorded source read tool; no external source effects."})
	t.Logf("status=%s calls=%d producer=%d reviewer=%d tool_reads=%d elapsed=%s poll_error=%v", run.Status, rec.calls, rec.producers, rec.reviews, f.pool.calls, time.Since(started).Round(time.Second), pollErr)
	if pollErr != nil {
		t.Fatal(pollErr)
	}
	if run.Status != store.RunSucceeded || run.Report.Quality == nil || run.Report.Quality.Outcome != store.BotWorkAccepted || run.Report.Summary == "" {
		t.Fatalf("no accepted publication; inspect %s/run.json and raw responses (status=%s)", dir, run.Status)
	}
	if rec.producers == 0 || rec.reviews == 0 || f.pool.calls == 0 {
		t.Fatal("missing actual producer/reviewer/source execution")
	}
	id := "bot-report:" + run.ID.String()
	if strings.Count(string(history), id) != 1 {
		t.Fatal("accepted report not visible exactly once")
	}
	quoted, _ := json.Marshal(run.Report.Summary)
	if !strings.Contains(string(history), string(quoted)) {
		t.Fatal("Home does not contain exact accepted summary")
	}
	for _, draft := range candidates {
		if draft == run.Report.Summary {
			continue
		}
		q, _ := json.Marshal(draft)
		if strings.Contains(string(history), string(q)) {
			t.Fatal("rejected candidate published")
		}
	}
	// Reconcile only the already-terminal assignment; never resubmit or retry it.
	if err = f.worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded := f.request("GET", historyPath, "", 200)
	save("home-reloaded.json", string(reloaded))
	if strings.Count(string(reloaded), id) != 1 {
		t.Fatal("duplicate publication after reconcile")
	}
	if rec.calls == 0 {
		t.Fatal("no provider calls")
	}
}
