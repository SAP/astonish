package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/provider/bedrock"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// generationHeldReader reaches the gate only after the production parser has
// consumed every preceding frame. Neither EOF nor message_stop is available
// until the HTTP client has asserted observable progress.
type generationHeldReader struct {
	ctx     context.Context
	prefix  *strings.Reader
	suffix  *strings.Reader
	held    chan struct{}
	release <-chan struct{}
	waiting bool
}

func (r *generationHeldReader) Read(p []byte) (int, error) {
	if r.prefix.Len() > 0 {
		return r.prefix.Read(p)
	}
	if !r.waiting {
		r.waiting = true
		close(r.held)
		select {
		case <-r.release:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	return r.suffix.Read(p)
}

type generationProgressLLM struct {
	held             chan struct{}
	release          chan struct{}
	calls            atomic.Int32
	retry            bool
	reportAndThought bool
	waiting          bool
}

func (m *generationProgressLLM) GenerateContent(ctx context.Context, _ *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if !stream {
			yield(nil, fmt.Errorf("generation acceptance requires streaming"))
			return
		}
		call := m.calls.Add(1)
		if m.waiting && call == 1 {
			streaming.Report(ctx, streaming.Progress{Kind: streaming.KindHeartbeat, Phase: streaming.PhaseWaiting, Attempt: 1, MaxAttempts: 1, Elapsed: 45 * time.Second, LastActivity: 45 * time.Second})
		}
		if m.retry {
			if call == 1 {
				streaming.Report(ctx, streaming.Progress{Phase: streaming.PhaseThinking})
				yield(nil, &llmerror.TruncatedStreamError{Provider: "test"})
				return
			}
			call--
		}
		if call > 2 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "Finished."}, {Text: " Final-only block."}}}, TurnComplete: true}, nil)
			return
		}
		prefix := `data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"PRIVATE_REASONING"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"PRIVATE_REASONING"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"PRIVATE_SIGNATURE"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Visible partial answer."}}

data: {"type":"content_block_stop","index":1}

data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"progress-tool-1","name":"progress_probe","input":{}}}

data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"PRIVATE_"}}

data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"ARGUMENT\"}"}}

data: {"type":"content_block_stop","index":3}

data: {"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":" Second visible block."}}

data: {"type":"content_block_stop","index":4}

`
		prefix = strings.ReplaceAll(prefix, "progress-tool-1", fmt.Sprintf("progress-tool-%d", call))
		var r io.Reader = strings.NewReader(prefix + "data: {\"type\":\"message_stop\"}\n\n")
		if call == 1 {
			r = &generationHeldReader{ctx: ctx, prefix: strings.NewReader(prefix), suffix: strings.NewReader("data: {\"type\":\"message_stop\"}\n\n"), held: m.held, release: m.release}
		}
		for response, err := range bedrock.ParseStreamWithProgress(r, func(p streaming.Progress) { streaming.Report(ctx, p) }) {
			if m.reportAndThought && response != nil && response.Content != nil {
				// Some providers report progress and also yield structured Thought
				// parts. Unrelated usage metadata must not reopen that status gate.
				if !yield(&model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5}}, nil) {
					return
				}
				response.Content.Parts = append([]*genai.Part{{Text: "PRIVATE_REASONING", Thought: true}}, response.Content.Parts...)
				var reports sync.WaitGroup
				for range 8 {
					reports.Add(1)
					go func() {
						defer reports.Done()
						streaming.Report(ctx, streaming.Progress{Phase: streaming.PhaseThinking})
					}()
				}
				// Finish callbacks before the final aggregate closes the response;
				// partial Thought handling deliberately races with callbacks.
				if !response.Partial {
					reports.Wait()
				}
				ok := yield(response, err)
				reports.Wait()
				if !ok {
					return
				}
			} else if !yield(response, err) {
				return
			}
		}
	}
}

func TestChatGenerationProgressThoughtIsBoundedAndPrivate(t *testing.T) {
	var turns []*MockTurn
	for range 128 {
		turns = append(turns, &MockTurn{Partial: true, Parts: []*genai.Part{{Text: "PRIVATE_THOUGHT", Thought: true}}})
	}
	turns = append(turns, &MockTurn{TurnComplete: true, Parts: []*genai.Part{{Text: "PRIVATE_THOUGHT", Thought: true}, {Text: "Public answer."}}})
	env := setupIntegrationTest(t, &MockLLM{turns: turns}, nil)
	events := runAndCollect(t, env, "Answer briefly")
	statuses := 0
	for _, event := range events {
		payload, err := json.Marshal(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "PRIVATE_THOUGHT") {
			t.Fatalf("thought leaked: %s", payload)
		}
		if event.Type == "thinking" && event.Data["text"] == "Model is thinking…" {
			statuses++
		}
	}
	if statuses != 1 {
		t.Fatalf("thinking statuses = %d, want 1 for 128 thought chunks plus final aggregate", statuses)
	}
	texts := eventsOfType(events, "text")
	if len(texts) != 1 || texts[0].Data["text"] != "Public answer." {
		t.Fatalf("public answer events = %+v", texts)
	}
}

func TestChatGenerationProgressHTTP(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, reportAndThought := range []bool{false, true} {
			t.Run(fmt.Sprintf("retry=%t/report-and-thought=%t", retry, reportAndThought), func(t *testing.T) {
				testChatGenerationProgressHTTP(t, retry, reportAndThought)
			})
		}
	}
}

func testChatGenerationProgressHTTP(t *testing.T, retry, reportAndThought bool) {
	provider := &generationProgressLLM{held: make(chan struct{}), release: make(chan struct{}), retry: retry, reportAndThought: reportAndThought, waiting: true}
	var executed atomic.Int32
	probe, err := functiontool.New(functiontool.Config{Name: "progress_probe", Description: "Record a completed tool call"}, func(_ tool.Context, args struct {
		Input string `json:"input"`
	}) (mockToolResult, error) {
		executed.Add(1)
		if args.Input != "PRIVATE_ARGUMENT" {
			return mockToolResult{}, fmt.Errorf("incomplete arguments: %q", args.Input)
		}
		return mockToolResult{Output: map[string]any{"ok": true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := setupIntegrationTest(t, &MockLLM{funcLLM: provider}, []tool.Tool{probe})
	done := make(chan struct{})
	start := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-start:
		case <-env.Runner.Context().Done():
			return
		}
		env.Runner.Run(env.ChatAgent, env.SessionService, env.MockLLM, nil, genai.NewContentFromText("Run the probe", "user"), "Run the probe", true, "", nil, false)
	}()
	defer func() {
		env.Runner.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runner did not stop")
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		streamRunnerEvents(w, w.(http.Flusher), r.Context(), env.Runner)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	// The HTTP connection is established before generation begins.
	close(start)
	wire := client.NewSSEStream(response.Body)
	var events []ChatEvent
	read := func() ChatEvent {
		t.Helper()
		event, err := wire.Next()
		if err != nil {
			t.Fatalf("HTTP progress before provider release: %v; events=%v", err, eventTypes(events))
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(event.Data), &data); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(event.Data, "PRIVATE_REASONING") || strings.Contains(event.Data, "PRIVATE_SIGNATURE") {
			t.Fatalf("private reasoning leaked: %s", event.Data)
		}
		e := ChatEvent{Type: event.Type, Data: data}
		if e.Type == "error" && !(retry && strings.Contains(fmt.Sprint(e.Data["error"]), "stream ended without a finish_reason")) {
			t.Fatalf("runner error: %v", e.Data)
		}
		events = append(events, e)
		return e
	}
	seen := map[string]bool{}
	for len(seen) < 5 {
		e := read()
		if e.Type == "tool_call" || e.Type == "tool_result" || e.Type == "done" {
			t.Fatalf("premature event: %+v", e)
		}
		payload, _ := json.Marshal(e.Data)
		if strings.Contains(string(payload), "PRIVATE_ARGUMENT") || strings.Contains(string(payload), "progress_probe") {
			t.Fatalf("tool payload exposed before stop: %s", payload)
		}
		if e.Type == "thinking" {
			text := fmt.Sprint(e.Data["text"])
			if text == "Model is thinking…" || text == "Preparing tool arguments…" || strings.HasPrefix(text, "Waiting for model response") {
				seen[text] = true
			}
		}
		if e.Type == "text" && (e.Data["text"] == "Visible partial answer." || e.Data["text"] == " Second visible block.") {
			seen[e.Data["text"].(string)] = true
		}
	}
	select {
	case <-provider.held:
	case <-ctx.Done():
		t.Fatal("production parser never reached held-open read")
	}
	if executed.Load() != 0 || env.Runner.IsDone() {
		t.Fatal("provider completed or tool executed before message_stop")
	}
	for _, e := range env.Runner.GetHistory() {
		if e.Type == "tool_call" || e.Type == "tool_result" || e.Type == "done" {
			t.Fatalf("premature buffered event: %+v", e)
		}
	}
	close(provider.release)
	for read().Type != "done" {
	}
	if event, err := wire.Next(); err != io.EOF {
		t.Fatalf("expected HTTP EOF, got %v, %v", event, err)
	}
	if executed.Load() != 2 {
		t.Fatalf("tool executions = %d, want 2", executed.Load())
	}
	if len(eventsOfType(events, "tool_call")) != 2 || len(eventsOfType(events, "tool_result")) != 2 {
		t.Fatalf("tool events: %v", eventTypes(events))
	}
	counts := map[string]int{}
	for _, e := range events {
		if e.Type == "text" || e.Type == "thinking" {
			text, _ := e.Data["text"].(string)
			counts[text]++
		}
	}
	wantCounts := map[string]int{"Model is thinking…": 2, "Preparing tool arguments…": 2, "Visible partial answer.": 2, " Second visible block.": 2, "Finished.": 1, " Final-only block.": 1}
	if retry {
		wantCounts["Model is thinking…"]++
		if len(eventsOfType(events, "retry")) != 1 {
			t.Errorf("expected one truncation retry: %v", eventTypes(events))
		}
	}
	for text, want := range wantCounts {
		if counts[text] != want {
			t.Errorf("%q emitted %d times, want %d", text, counts[text], want)
		}
	}
	assertEventSequence(t, events, "thinking", "text", "thinking", "text", "tool_call", "tool_result", "thinking", "text", "thinking", "text", "tool_call", "tool_result", "text", "text", "done")
}
