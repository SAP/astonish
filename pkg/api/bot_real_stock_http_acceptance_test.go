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

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
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

// stockWorkerDiscoveryEvidence deliberately reads trusted private envelopes, not
// Home text or later research calls. The first dispatch must finish the complete
// discovery sequence before its Employee service result can count as evidence.
func stockWorkerDiscoveryEvidence(rows []store.BotTranscriptEvent, sources []byte) (string, error) {
	var worker, dispatch string
	started, settled := false, false
	step := 0
	pending := ""
	var privateFinal string
	names := []string{common.SearchToolsName, common.DescribeToolsName, common.ExecuteToolName}
	for _, row := range rows {
		e := row.Event
		if e == nil || e.Partial {
			continue
		}
		if e.Author == "bot_runtime" {
			value, ok := e.Actions.StateDelta["bot_private_worker_v1"]
			if !ok {
				continue
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			var trace struct {
				WorkerID   string         `json:"worker_id"`
				DispatchID string         `json:"dispatch_id"`
				Phase      string         `json:"phase"`
				Event      *session.Event `json:"event"`
			}
			if err := json.Unmarshal(raw, &trace); err != nil {
				return "", err
			}
			if !started {
				if trace.Phase != "started" || trace.WorkerID == "" || trace.DispatchID == "" {
					return "", fmt.Errorf("first private trace is not a bound dispatch")
				}
				worker, dispatch, started = trace.WorkerID, trace.DispatchID, true
			}
			if trace.WorkerID != worker || trace.DispatchID != dispatch || settled {
				continue // Later dispatches cannot repair this acceptance sequence.
			}
			if trace.Phase == "settled" {
				if step != len(names) || pending != "" || !stockWorkerSourceQuotation(privateFinal, sources) {
					return "", fmt.Errorf("first worker settled without discovery, exact source execution, and useful final text")
				}
				settled = true
				continue
			}
			if trace.Phase != "event" || trace.Event == nil || trace.Event.Partial || trace.Event.Content == nil {
				continue
			}
			for _, p := range trace.Event.Content.Parts {
				if p == nil {
					continue
				}
				if p.FunctionCall != nil && step < len(names) && p.FunctionCall.Name == names[step] {
					call := p.FunctionCall
					if call.ID == "" || pending != "" {
						return "", fmt.Errorf("unpaired private discovery call")
					}
					if step == 0 && call.Args["query"] != "read_file" {
						return "", fmt.Errorf("private search did not use the requested read_file query")
					}
					if step == 1 {
						raw, _ := json.Marshal(call.Args)
						var args agent.DescribeToolsArgs
						if json.Unmarshal(raw, &args) != nil || len(args.Names) != 1 || args.Names[0] != "read_file" {
							return "", fmt.Errorf("private describe did not request read_file")
						}
					}
					if step == 2 {
						name, args, err := common.NormalizeExecuteToolArgs(call.Args)
						if err != nil || name != "read_file" || args["path"] != "/isolated/stock-market-sources.json" {
							return "", fmt.Errorf("private execution did not target the controlled source file")
						}
					}
					pending = call.ID
				}
				if p.FunctionResponse != nil && step < len(names) && p.FunctionResponse.Name == names[step] {
					r := p.FunctionResponse
					if pending == "" || r.ID != pending || !common.ToolResultSucceeded(r.Response, nil) {
						return "", fmt.Errorf("private %s response missing, mismatched, or failed", names[step])
					}
					raw, _ := json.Marshal(r.Response)
					switch step {
					case 0:
						var result struct {
							Matches []struct {
								Name string `json:"tool_name"`
							} `json:"matches"`
						}
						_ = json.Unmarshal(raw, &result)
						found := false
						for _, match := range result.Matches {
							found = found || match.Name == "read_file"
						}
						if !found {
							return "", fmt.Errorf("private search did not discover read_file")
						}
					case 1:
						var result agent.DescribeToolsResult
						_ = json.Unmarshal(raw, &result)
						found := false
						for _, tool := range result.Tools {
							found = found || tool.Name == "read_file" && tool.InputSchema != nil
						}
						if len(result.Errors) != 0 || !found {
							return "", fmt.Errorf("private describe did not return read_file schema")
						}
					case 2:
						if r.Response["content"] != string(sources) {
							return "", fmt.Errorf("private read did not return exact controlled source bytes")
						}
					}
					step++
					pending = ""
				}
				if step == len(names) && p.Text != "" && !p.Thought {
					privateFinal += p.Text
				}
			}
			continue
		}
		if !settled || e.Content == nil {
			continue
		}
		for _, p := range e.Content.Parts {
			if p == nil || p.FunctionResponse == nil || p.FunctionResponse.Name != "employee_chat" {
				continue
			}
			r := p.FunctionResponse.Response
			if r["worker_id"] == worker && r["status"] == string(bot.EmployeeChatCompleted) && r["private_text"] == privateFinal && common.ToolResultSucceeded(r, nil) {
				return worker, nil
			}
		}
	}
	return "", fmt.Errorf("missing completed Employee result for the first private discovery worker")
}

func stockWorkerSourceQuotation(text string, sources []byte) bool {
	var fixture struct {
		Sources any `json:"sources"`
	}
	if json.Unmarshal(sources, &fixture) != nil {
		return false
	}
	quoted, url := false, false
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		case string:
			if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") {
				url = url || strings.Contains(text, v)
				return
			}
			// A substantive verbatim excerpt, not just a tool/path/provenance claim.
			for start := 0; start+60 <= len(v); start++ {
				if strings.Contains(text, v[start:start+60]) {
					quoted = true
					break
				}
			}
		}
	}
	visit(fixture.Sources)
	return quoted && url
}

func TestStockWorkerDiscoveryEvidence(t *testing.T) {
	sources := []byte(`{"sources":[{"response":{"url":"https://example.test/news","content":"Historical market source reports a substantial recorded move in equity prices and explains the underlying earnings outlook."}}]}`)
	text := "https://example.test/news Historical market source reports a substantial recorded move in equity prices and explains the underlying earnings outlook."
	trace := func(phase, worker string, parts ...*genai.Part) store.BotTranscriptEvent {
		value := map[string]any{"phase": phase, "worker_id": worker, "dispatch_id": "first"}
		if len(parts) > 0 {
			value["event"] = &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: parts}}}
		}
		return store.BotTranscriptEvent{Event: &session.Event{Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_private_worker_v1": value}}}}
	}
	rows := []store.BotTranscriptEvent{trace("started", "worker")}
	responses := []map[string]any{
		{"matches": []map[string]any{{"tool_name": "read_file"}}},
		{"tools": []map[string]any{{"name": "read_file", "input_schema": map[string]any{"type": "object"}}}},
		{"content": string(sources)},
	}
	for i, name := range []string{"search_tools", "describe_tools", "execute_tool"} {
		args := map[string]any{"query": "read_file"}
		if name == "describe_tools" {
			args = map[string]any{"names": []string{"read_file"}}
		}
		if name == "execute_tool" {
			args = map[string]any{"name": "read_file", "arguments": map[string]any{"path": "/isolated/stock-market-sources.json"}}
		}
		id := fmt.Sprint(i)
		rows = append(rows, trace("event", "worker", &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}), trace("event", "worker", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: responses[i]}}))
	}
	rows = append(rows, trace("event", "worker", &genai.Part{Text: text}), trace("settled", "worker"), store.BotTranscriptEvent{Event: &session.Event{Author: "employee", LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "employee_chat", Response: map[string]any{"worker_id": "worker", "status": "completed", "private_text": text}}}}}}}})
	if worker, err := stockWorkerDiscoveryEvidence(rows, sources); err != nil || worker != "worker" {
		t.Fatalf("valid recorder trace: worker=%s err=%v", worker, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]store.BotTranscriptEvent)
	}{
		{"untrusted", func(r []store.BotTranscriptEvent) { r[0].Event.Author = "model" }},
		{"different-worker", func(r []store.BotTranscriptEvent) {
			r[4] = trace("event", "other", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "1", Name: "describe_tools", Response: responses[1]}})
		}},
		{"failed-search", func(r []store.BotTranscriptEvent) {
			r[2] = trace("event", "worker", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "0", Name: "search_tools", Response: map[string]any{"error": "denied"}}})
		}},
		{"later-path-cannot-substitute", func(r []store.BotTranscriptEvent) { r[0] = trace("started", "earlier-worker") }},
		{"wrong-source-bytes", func(r []store.BotTranscriptEvent) {
			r[6] = trace("event", "worker", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "2", Name: "execute_tool", Response: map[string]any{"content": "claim only"}}})
		}},
		{"incomplete-employee", func(r []store.BotTranscriptEvent) {
			r[len(r)-1].Event.Content.Parts[0].FunctionResponse.Response["status"] = "partial"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(rows)
			var changed []store.BotTranscriptEvent
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(changed)
			if _, err := stockWorkerDiscoveryEvidence(changed, sources); err == nil {
				t.Fatal("invalid trace accepted")
			}
		})
	}
	if stockWorkerSourceQuotation("I read the source file", sources) {
		t.Fatal("unsupported source claim accepted")
	}
}

// realStockReadClassifier describes only the fixture's mechanically restricted
// read transport. Unknown targets retain production's fail-closed classification.
type realStockReadClassifier struct{}

func (realStockReadClassifier) DescribeAction(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	action, err := (launcher.BotCatalogueClassifier{}).DescribeAction(ctx, name, raw)
	if err != nil || name != "read_file" {
		return action, err
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil || args["path"] != "/isolated/stock-market-sources.json" {
		return action, store.ErrBotForbidden
	}
	// callOverride enforces this same exact target and never performs writes.
	action.Effect, action.ConstraintsVerified = execution.EffectRead, true
	return action, nil
}

type realStockPrivateGuard struct {
	repo store.BotStore
	req  bot.RunRequest
}

func (g realStockPrivateGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	deny := execution.PolicyDecision{Kind: execution.PolicyDeny}
	worker, private := agent.BotPrivateWorkerExecution(ctx)
	id, present := execution.BotRunIdentityFromContext(ctx)
	p, authenticated := execution.PrincipalFromContext(ctx)
	want := execution.BotRunIdentity{BotID: g.req.Bot.ID, RunID: g.req.Run.ID, Version: g.req.Version.Version, ActorID: g.req.Principal.Subject, OrgSlug: g.req.Principal.OrgSlug, TeamSlug: g.req.Principal.TeamSlug}
	if !private || !present || id != want || worker != id || !authenticated || p.Validate() != nil || p.Subject != id.ActorID || p.OrgSlug != id.OrgSlug || p.TeamSlug != id.TeamSlug || p.Authentication != g.req.Principal.Authentication || execution.RunPolicyFromContext(ctx) != nil || g.req.Authorization == nil {
		return deny, store.ErrBotForbidden
	}
	if err := g.req.Authorization.AuthorizeTool(ctx, action.Tool); err != nil {
		return deny, err
	}
	if err := g.req.Budget.Check(ctx); err != nil {
		return deny, err
	}
	b, err := g.repo.Get(ctx, g.req.Bot.ID)
	if err != nil {
		return deny, err
	}
	r, err := g.repo.GetRun(ctx, g.req.Bot.ID, g.req.Run.ID)
	if err != nil {
		return deny, err
	}
	if b == nil || r == nil || !g.req.Run.Intent.AllowsStatus(b.Status) || b.Version != g.req.Version.Version || b.OwnerID != g.req.Bot.OwnerID || b.Home != g.req.Bot.Home || r.Status != store.RunRunning || r.ExecutionMode != g.req.Run.ExecutionMode || r.Intent != g.req.Run.Intent || r.SessionID != g.req.Run.SessionID || r.Lease.Fence != g.req.Run.Lease.Fence || r.Lease.WorkerID != g.req.Run.Lease.WorkerID || !time.Now().Before(r.Lease.ExpiresAt) {
		return deny, store.ErrBotConflict
	}
	if action.Tool != "read_file" || action.Effect != execution.EffectRead || !action.ConstraintsVerified {
		return deny, store.ErrBotForbidden
	}
	classified, err := (realStockReadClassifier{}).DescribeAction(ctx, action.Tool, action.Arguments)
	if err != nil {
		return deny, err
	}
	digest, err := action.Digest()
	exact, exactErr := classified.Digest()
	if err != nil || exactErr != nil || digest != exact || ctx.Err() != nil {
		return deny, store.ErrBotForbidden
	}
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, nil
}

func TestRealStockFixtureAuthority(t *testing.T) {
	classifier := realStockReadClassifier{}
	for _, tc := range []struct {
		name    string
		raw     string
		allowed bool
	}{{"read_file", `{"path":"/isolated/stock-market-sources.json"}`, true}, {"read_file", `{"path":"/etc/passwd"}`, false}, {"read_file", `{bad}`, false}, {"write_file", `{"path":"/isolated/stock-market-sources.json"}`, false}} {
		action, err := classifier.DescribeAction(context.Background(), tc.name, json.RawMessage(tc.raw))
		if got := err == nil && action.Effect == execution.EffectRead && action.ConstraintsVerified; got != tc.allowed {
			t.Fatalf("classification %s %s: allowed=%v error=%v", tc.name, tc.raw, got, err)
		}
	}
	action, err := classifier.DescribeAction(context.Background(), "read_file", json.RawMessage(`{"path":"/isolated/stock-market-sources.json"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision, err := (realStockPrivateGuard{}).Authorize(context.Background(), action); err == nil || decision.Kind != execution.PolicyDeny {
		t.Fatal("fixture allowed a read without the original private execution binding")
	}
}

func stockEmployeePublicationID(history []byte, summary string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(history)))
	scanner.Buffer(make([]byte, 65536), 4<<20)
	kind, id, count := "", "", 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if kind != "text" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame struct {
			Text       string `json:"text"`
			Projection struct {
				EventID string `json:"event_id"`
			} `json:"_projection"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			return "", err
		}
		if frame.Text == summary {
			count++
			id = frame.Projection.EventID
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if count != 1 || id == "" || summary == "" {
		return "", fmt.Errorf("expected one exact projected Employee report, got %d", count)
	}
	return id, nil
}

func TestStockEmployeePublicationID(t *testing.T) {
	one := []byte("event: text\ndata: {\"text\":\"report\",\"_projection\":{\"event_id\":\"stable\"}}\n\nevent: bot_run_state\ndata: {\"text\":\"report\"}\n")
	if id, err := stockEmployeePublicationID(one, "report"); err != nil || id != "stable" {
		t.Fatalf("projected report: %s %v", id, err)
	}
	for _, history := range [][]byte{nil, append(append([]byte(nil), one...), one...), []byte("event: text\ndata: {bad}\n"), []byte("event: text\ndata: {\"text\":\"report\"}\n")} {
		if _, err := stockEmployeePublicationID(history, "report"); err == nil {
			t.Fatal("missing, duplicate or malformed publication accepted")
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
	rec := &realStockHTTPRecorder{inner: litellm.NewProvider(cfg.Key, cfg.URL, cfg.Model), dir: dir, redact: []string{cfg.Key}}
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
	// Preserve the fixture's retained Home backend and scoped file preparation:
	// production Chat construction reads critical Home documents before any model.
	// Replacing those resources with journeyHomeBackend leaves startup unsupported.
	workerConfig := f.workerConfig
	workerConfig.ContextWindow = 200000
	workerConfig.ModelOptions = provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true}
	workerConfig.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return rec, nil
	}
	prepare := workerConfig.Prepare
	workerConfig.Prepare = func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if len(q.Version.Autonomy.AllowedTools) != 0 {
			return nil, fmt.Errorf("unexpected added autonomy")
		}
		resources, err := prepare(c, q, s)
		if err != nil || resources == nil {
			return resources, err
		}
		// Private Chat requires the same explicit classifier/live guard boundary
		// as daemon production even for interactive runs; no autonomy is added.
		resources.PolicyRuntime = agent.BotPolicyRuntime{Classifier: realStockReadClassifier{}, Guard: realStockPrivateGuard{repo: f.repo, req: q}}
		return resources, nil
	}
	f.worker, err = daemon.NewBotWorker(workerConfig)
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
	instructions := "Stock Market News. Before writing the briefing, you (Employee) must use employee_chat to direct your FIRST private Chat worker to perform this exact discovery sequence in one dispatch: call search_tools with query read_file; call describe_tools with names [read_file] and inspect its schema; then call execute_tool with name read_file and arguments containing path /isolated/stock-market-sources.json. The worker must return useful source content including exact quotations and source URLs from the observed file, not merely claim it read the file. Use the completed worker result to write your Employee report. Do not substitute Employee-side research, a later worker dispatch, direct read_file, or fresh web fetching for this sequence. The file contains nine historical recorded source responses, not a fresh external fetch; base the briefing on that material and make its timing and limitations clear."
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
	save("execution.json", map[string]any{"elapsed_seconds": time.Since(started).Seconds(), "calls": rec.calls, "producer_calls": rec.producers, "review_calls": rec.reviews, "tokens": rec.tokens, "source_read_calls": f.pool.calls, "poll_error": fmt.Sprint(pollErr), "note": "Actual HTTP owner admission, production worker/ChatRunner and scoped temporary SQLite; real model for all stages; controlled recorded source read tool; no external source effects; not physical sandbox or fresh web verification."})
	t.Logf("status=%s calls=%d producer=%d reviewer=%d tool_reads=%d elapsed=%s poll_error=%v", run.Status, rec.calls, rec.producers, rec.reviews, f.pool.calls, time.Since(started).Round(time.Second), pollErr)
	if pollErr != nil {
		t.Fatal(pollErr)
	}
	principal, ok := execution.PrincipalFromContext(f.ctx)
	if !ok {
		t.Fatal("missing fixture principal")
	}
	ods, err := f.db.ForOrg(principal.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	reader := ods.ForTeam(principal.TeamSlug).Sessions().(store.BotTranscriptReader)
	var rows []store.BotTranscriptEvent
	var cursor int64
	for {
		page, err := reader.ReadBotTranscript(f.ctx, run.SessionID, cursor, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		rows = append(rows, page...)
		cursor = page[len(page)-1].Cursor
	}
	save("private-worker-transcript.json", rows)
	workerID, discoveryErr := stockWorkerDiscoveryEvidence(rows, sources)
	if discoveryErr != nil {
		t.Fatalf("private worker discovery acceptance: %v; inspect %s/private-worker-transcript.json", discoveryErr, dir)
	}
	save("private-worker-discovery.json", map[string]any{"worker_id": workerID, "sequence": []string{"search_tools", "describe_tools(read_file)", "execute_tool(read_file)"}, "employee_result": "completed", "note": "Persisted first-dispatch trace; controlled historical source read, not fresh web or physical sandbox proof."})
	// Agent-led completion has no legacy reviewer Quality record. Validate its
	// source-bound Employee envelope and finished work instead of inventing one.
	if run.Status != store.RunSucceeded || run.Report.Summary == "" || run.Report.ValidateEmployeeMessageIntegrity(run.Status) != nil || run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || run.Report.Work == nil || run.Report.Work.Phase != store.BotWorkFinished || run.Report.Work.Disposition != store.AgentWorkAnswered || !stockWorkerSourceQuotation(run.Report.Summary, sources) {
		t.Fatalf("no verified Employee publication; inspect %s/run.json and raw responses (status=%s)", dir, run.Status)
	}
	if rec.producers == 0 || f.pool.calls == 0 {
		t.Fatal("missing actual producer/source execution")
	}
	publicationID, err := stockEmployeePublicationID(history, run.Report.Summary)
	if err != nil {
		t.Fatal(err)
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
	if reloadedID, err := stockEmployeePublicationID(reloaded, run.Report.Summary); err != nil || reloadedID != publicationID {
		t.Fatalf("publication changed or duplicated after reconcile: id=%s error=%v", reloadedID, err)
	}
	if rec.calls == 0 {
		t.Fatal("no provider calls")
	}
}
