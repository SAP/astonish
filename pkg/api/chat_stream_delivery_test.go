package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatStreamChildForwarderSuppressesGenericKeepsImage(t *testing.T) {
	var chatAgent *agent.ChatAgent
	forward, err := functiontool.New(functiontool.Config{Name: "forward_child", Description: "forward child event"}, func(ctx tool.Context, _ map[string]any) (map[string]any, error) {
		chatAgent.ForwardSubTaskEvent(ctx.SessionID(), &adksession.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{Text: "CHILD_TEXT_MUST_NOT_BE_GENERIC"},
			{FunctionCall: &genai.FunctionCall{ID: "child-call", Name: "child_only_tool"}},
			{FunctionResponse: &genai.FunctionResponse{ID: "child-call", Name: "child_only_tool", Response: map[string]any{"ok": true}}},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte("child-image")}},
		}}}})
		return map[string]any{"ok": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(ToolCallTurn("forward_child", map[string]any{}), TextTurn("Parent finished.")), []tool.Tool{forward})
	chatAgent = env.ChatAgent
	events := env.Turn(t, "", "Forward child output", false)
	images := 0
	for _, event := range events {
		if event.Type == "text" && strings.Contains(fmt.Sprint(event.Data["text"]), "CHILD_TEXT_MUST_NOT_BE_GENERIC") {
			t.Fatal("raw child text duplicated")
		}
		if (event.Type == "tool_call" || event.Type == "tool_result") && event.Data["name"] == "child_only_tool" {
			t.Fatal("raw child tool duplicated")
		}
		if event.Type == "image" {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("image side channel count=%d events=%+v", images, events)
	}
	assertHasEvent(t, events, "done")
}

func TestChatStreamDelegationValidationErrorSSE(t *testing.T) {
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprint(structured), func(t *testing.T) {
			delegate, err := tools.NewDelegateTasksTool()
			if err != nil {
				t.Fatal(err)
			}
			env := setupMemoryHTTPAcceptance(t, NewMockLLM(ToolCallTurn("delegate_tasks", map[string]any{"tasks": []any{}}), TextTurn("No tasks were supplied.")), []tool.Tool{delegate})
			mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
			if structured {
				mgr.SubTaskProgress = func(evt agent.SubTaskProgressEvent) { env.ChatAgent.EmitSubTaskProgress(evt.SessionID, evt) }
			}
			old := tools.GetSubAgentManager()
			tools.SetSubAgentManager(mgr)
			t.Cleanup(func() { tools.SetSubAgentManager(old) })
			events := env.Turn(t, "", "Delegate an empty batch", false)
			completed := 0
			for _, event := range events {
				if (event.Type == "tool_call" || event.Type == "tool_result") && event.Data["name"] == "delegate_tasks" {
					t.Fatalf("generic delegation leaked: %+v", event)
				}
				if event.Type == "subtask_progress" && event.Data["event_type"] == "delegation_complete" {
					completed++
					if event.Data["batch_id"] == "" || event.Data["status"] != "error" || !strings.Contains(fmt.Sprint(event.Data["error"]), "No tasks provided") {
						t.Fatalf("missing validation details: %+v", event)
					}
				}
			}
			if completed != 1 {
				t.Fatalf("completion events=%d events=%+v", completed, events)
			}
			assertHasEvent(t, events, "done")
		})
	}
}

// Gate the first flushed chunk, not the socket buffer: this makes backpressure
// deterministic on every OS while the client still consumes real loopback HTTP.
type deliveryGateWriter struct {
	http.ResponseWriter
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *deliveryGateWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	w.once.Do(func() { close(w.entered); <-w.release })
}

func deliveryWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream")
	}
}

func deliveryReadEvent(r *bufio.Reader) (ChatEvent, error) {
	var event ChatEvent
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return event, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" && event.Type != "" {
			return event, nil
		}
		if strings.HasPrefix(line, "event: ") {
			event.Type = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event.Data); err != nil {
				return event, err
			}
		}
	}
}

func deliveryAssertEvent(t *testing.T, r *bufio.Reader, typ string, seq int) {
	t.Helper()
	event, err := deliveryReadEvent(r)
	if err != nil {
		t.Fatalf("want %s %d: %v", typ, seq, err)
	}
	if event.Type != typ {
		t.Fatalf("want %s %d, got %s", typ, seq, event.Type)
	}
	if typ == "text" && event.Data["seq"] != float64(seq) {
		t.Fatalf("want seq %d, got %v", seq, event.Data)
	}
}

func deliveryBackpressure(t *testing.T, markDone bool) {
	t.Helper()
	runner := newChatRunner("delivery", "user", "app", false)
	t.Cleanup(runner.Stop)
	runner.emitEvent("text", map[string]any{"seq": 0})
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(exited)
		w.Header().Set("Content-Type", "text/event-stream")
		gated := &deliveryGateWriter{ResponseWriter: w, entered: entered, release: release}
		streamRunnerEvents(gated, gated, r.Context(), runner)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	client := server.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	deliveryAssertEvent(t, r, "text", 0)
	deliveryWait(t, entered)
	if runner.IsDone() {
		t.Fatal("first chunk arrived only after completion")
	}
	for i := 1; i <= 512; i++ {
		runner.emitEvent("text", map[string]any{"seq": i})
	}
	runner.emitEvent("done", map[string]any{"done": true})
	if markDone {
		runner.doneMu.Lock()
		runner.done = true
		runner.doneMu.Unlock()
	}
	// A refined title can legitimately follow done, before subscriber closure.
	runner.emitEvent("session_title", map[string]any{"title": "Refined"})
	runner.closeSubscribers()
	unblock()
	for i := 1; i <= 512; i++ {
		deliveryAssertEvent(t, r, "text", i)
	}
	deliveryAssertEvent(t, r, "done", 0)
	deliveryAssertEvent(t, r, "session_title", 0)
	if _, err := deliveryReadEvent(r); err != io.EOF {
		t.Fatalf("want clean EOF, got %v", err)
	}
	deliveryWait(t, exited)
}

func TestChatStreamDeliveryOverflow(t *testing.T)               { deliveryBackpressure(t, false) }
func TestChatStreamDeliveryCompletionDuringReplay(t *testing.T) { deliveryBackpressure(t, true) }

func TestChatStreamDeliveryDisconnect(t *testing.T) {
	runner := newChatRunner("disconnect", "user", "app", false)
	defer runner.Stop()
	runner.emitEvent("text", map[string]any{"seq": 0})
	exited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(exited)
		streamRunnerEvents(w, w.(http.Flusher), r.Context(), runner)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	client := server.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	deliveryAssertEvent(t, bufio.NewReader(resp.Body), "text", 0)
	cancel()
	resp.Body.Close()
	deliveryWait(t, exited)
	if runner.Context().Err() != nil {
		t.Fatal("HTTP cancellation cancelled background runner")
	}
	runner.emitEvent("text", map[string]any{"seq": 1})
	runner.emitEvent("done", map[string]any{"done": true})
	runner.closeSubscribers()
	if len(runner.GetHistory()) != 3 {
		t.Fatal("background runner stopped producing events")
	}
}

type deliveryErrorWriter struct {
	header          http.Header
	writes, flushes int
	failFlush       bool
}

func (w *deliveryErrorWriter) Header() http.Header { return w.header }
func (w *deliveryErrorWriter) WriteHeader(int)     {}
func (w *deliveryErrorWriter) Write(p []byte) (int, error) {
	w.writes++
	if !w.failFlush {
		return 0, errors.New("broken writer")
	}
	return len(p), nil
}
func (w *deliveryErrorWriter) Flush()            { _ = w.FlushError() }
func (w *deliveryErrorWriter) FlushError() error { w.flushes++; return errors.New("broken flush") }

func TestChatStreamDeliveryWriteFailure(t *testing.T) {
	for _, failFlush := range []bool{false, true} {
		t.Run(fmt.Sprint("flush=", failFlush), func(t *testing.T) {
			runner := newChatRunner("write-error", "user", "app", false)
			defer runner.Stop()
			for i := 0; i < 3; i++ {
				runner.emitEvent("text", map[string]any{"seq": i})
			}
			w := &deliveryErrorWriter{header: make(http.Header), failFlush: failFlush}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			streamRunnerEvents(w, w, ctx, runner)
			if w.writes != 1 {
				t.Errorf("reader wrote %d events after write/flush failure; want 1", w.writes)
			}
			if ctx.Err() != nil {
				t.Error("reader waited for cancellation instead of exiting on error")
			}
			if runner.Context().Err() != nil {
				t.Error("write failure cancelled runner")
			}
			runner.subMu.RLock()
			remaining := len(runner.subscribers)
			runner.subMu.RUnlock()
			if remaining != 0 {
				t.Errorf("leaked %d subscribers", remaining)
			}
		})
	}
}

func TestChatStreamDeliveryLateSubscription(t *testing.T) {
	runner := newChatRunner("late", "user", "app", false)
	defer runner.Stop()
	runner.emitEvent("done", map[string]any{"done": true})
	runner.closeSubscribers()
	runner.closeSubscribers() // closure is idempotent
	ch := runner.Subscribe("late")
	defer runner.Unsubscribe("late")
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("late subscription must be closed")
		}
	default:
		t.Fatal("late subscription left open after runner closure")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamRunnerEvents(w, w.(http.Flusher), r.Context(), runner)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	deliveryAssertEvent(t, r, "done", 0)
	if _, err := deliveryReadEvent(r); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestChatStreamDeliveryTitleAfterDone(t *testing.T) {
	runner := newChatRunner("title-after-done", "user", "app", false)
	defer runner.Stop()
	runner.emitEvent("done", map[string]any{"done": true})
	runner.doneMu.Lock()
	runner.done = true
	runner.doneMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamRunnerEvents(w, w.(http.Flusher), r.Context(), runner)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	deliveryAssertEvent(t, r, "done", 0)
	runner.emitEvent("session_title", map[string]any{"title": "Refined"})
	runner.closeSubscribers()
	deliveryAssertEvent(t, r, "session_title", 0)
	if _, err := deliveryReadEvent(r); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestChatStreamDeliveryConcurrentCloseSubscribe(t *testing.T) {
	for i := 0; i < 100; i++ {
		runner := newChatRunner("concurrent-close", "user", "app", false)
		var wg sync.WaitGroup
		var ch <-chan ChatEvent
		wg.Add(2)
		go func() { defer wg.Done(); ch = runner.Subscribe("reader") }()
		go func() { defer wg.Done(); runner.closeSubscribers() }()
		wg.Wait()
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("expected closed subscriber")
			}
		default:
			t.Fatal("close/subscribe race left subscriber open")
		}
		runner.Stop()
	}
}

func TestChatStreamDeliveryHistoryCursor(t *testing.T) {
	runner := newChatRunner("cursor", "user", "app", false)
	defer runner.Stop()
	for i := 0; i < 3; i++ {
		runner.emitEvent("text", map[string]any{"seq": i})
	}
	history := runner.GetHistoryFrom(1)
	if len(history) != 2 || history[0].Data["seq"] != 1 {
		t.Fatalf("wrong history suffix: %v", history)
	}
	history[0].Type = "modified-copy"
	if runner.GetHistoryFrom(1)[0].Type != "text" {
		t.Fatal("history exposes runner's backing slice")
	}
	if len(runner.GetHistoryFrom(3)) != 0 || len(runner.GetHistoryFrom(100)) != 0 {
		t.Fatal("cursor beyond history returned events")
	}
	if len(runner.GetHistoryFrom(-1)) != 3 {
		t.Fatal("negative cursor should replay full history")
	}
}

func TestChatStreamDeliveryHeartbeat(t *testing.T) {
	runner := newChatRunner("heartbeat", "user", "app", false)
	defer runner.Stop()
	exited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(exited)
		w.Header().Set("Content-Type", "text/event-stream")
		streamRunnerEvents(w, w.(http.Flusher), r.Context(), runner)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 20 * time.Second
	started := time.Now()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ":") {
		t.Fatalf("want heartbeat comment, got %q: %v", line, err)
	}
	if elapsed := time.Since(started); elapsed < 14*time.Second || elapsed > 19*time.Second {
		t.Fatalf("heartbeat took %v, want ~15s", elapsed)
	}
	if runner.IsDone() {
		t.Fatal("heartbeat requires a live runner")
	}
	resp.Body.Close()
	deliveryWait(t, exited)
}
