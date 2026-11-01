package sap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"golang.org/x/net/http2"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Exercise a real HTTP/2 RST_STREAM after flushed output, not a mocked reader.
func TestBedrockStreamHTTP2Continuation(t *testing.T) {
	var attempts atomic.Int32
	var payloads []map[string]json.RawMessage
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("wanted HTTP/2, got %s", r.Proto)
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		payloads = append(payloads, payload)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts.Add(1) == 1 {
			_, _ = io.WriteString(w, sapEvents(sapText, sapTool, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"lost\":"}}`))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = io.WriteString(w, sapEvents(`{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`, sapTool, sapToolStop, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`, sapStop))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := &model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "finish the task"}}}}, Config: &genai.GenerateContentConfig{SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "system unchanged"}}}}}
	before, _ := json.Marshal(request)
	var text strings.Builder
	var final *model.LLMResponse
	tools, finals := 0, 0
	for response, err := range testStreamProvider(server.Client(), server.URL).GenerateContent(ctx, request, true) {
		if err != nil {
			t.Fatal(err)
		}
		if response.Partial {
			for _, part := range response.Content.Parts {
				text.WriteString(part.Text)
			}
		} else {
			final = response
			finals++
			for _, part := range response.Content.Parts {
				if part.FunctionCall != nil {
					tools++
				}
			}
		}
	}
	after, _ := json.Marshal(request)
	if string(before) != string(after) {
		t.Fatal("caller request mutated")
	}
	if attempts.Load() != 2 || text.String() != "hello world" || tools != 1 || finals != 1 {
		t.Fatalf("attempts=%d text=%q tools=%d finals=%d", attempts.Load(), text.String(), tools, finals)
	}
	var aggregate strings.Builder
	for _, part := range final.Content.Parts {
		aggregate.WriteString(part.Text)
	}
	if aggregate.String() != text.String() {
		t.Fatalf("restore text=%q live=%q", aggregate.String(), text.String())
	}
	if final.UsageMetadata == nil || final.UsageMetadata.CandidatesTokenCount != 7 {
		t.Fatal("successful attempt usage lost")
	}
	if string(payloads[0]["system"]) != string(payloads[1]["system"]) {
		t.Fatal("system changed")
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(payloads[1]["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[1].Role != "assistant" || string(messages[1].Content) != `"hello"` || messages[2].Role != "user" {
		t.Fatalf("not a continuation: %s", payloads[1]["messages"])
	}
	if strings.Contains(string(payloads[1]["messages"]), "lost") {
		t.Fatal("incomplete tool replayed")
	}
}

func TestBedrockStreamIdleHTTP2Recovery(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2, got %s", r.Proto)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts.Add(1) == 1 {
			_, _ = io.WriteString(w, sapEvents(sapThinking, sapTool, sapToolStop))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, sapEvents(sapText, sapStop))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	p := testStreamProvider(server.Client(), server.URL)
	if err := WithBedrockStreamTiming(80*time.Millisecond, 10*time.Millisecond)(p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var progress []streaming.Progress
	ctx = streaming.WithProgress(ctx, func(p streaming.Progress) { progress = append(progress, p) })
	finals, tools := 0, 0
	for response, err := range p.GenerateContent(ctx, &model.LLMRequest{}, true) {
		if err != nil {
			t.Fatal(err)
		}
		if !response.Partial {
			finals++
		}
		for _, part := range response.Content.Parts {
			if part.FunctionCall != nil {
				tools++
			}
		}
	}
	heartbeats, recoveries := 0, 0
	for _, p := range progress {
		if p.Kind == streaming.KindHeartbeat {
			heartbeats++
		}
		if p.Kind == streaming.KindRecovery {
			recoveries++
			if p.Attempt != 2 || p.MaxAttempts != maxBedrockStreamSegments {
				t.Fatalf("bad recovery: %+v", p)
			}
		}
	}
	if attempts.Load() != 2 || finals != 1 || tools != 0 || heartbeats == 0 || recoveries != 1 {
		t.Fatalf("attempts=%d finals=%d tools=%d heartbeat=%d recovery=%d", attempts.Load(), finals, tools, heartbeats, recoveries)
	}
}

func TestBedrockStreamIdleAfterPartialTextContinuesWithoutReplay(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: streamRoundTrip(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			body := io.MultiReader(strings.NewReader(sapEvents(sapText)), streamReadFailure{ErrBedrockStreamIdle})
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sapEvents(`{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`, sapStop)))}, nil
	})}

	var live, final strings.Builder
	var gotErr error
	for response, err := range testStreamProvider(client, "http://provider.test").GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil {
			gotErr = err
			continue
		}
		if response == nil || response.Content == nil {
			continue
		}
		for _, part := range response.Content.Parts {
			if response.Partial {
				live.WriteString(part.Text)
			} else {
				final.WriteString(part.Text)
			}
		}
	}
	if attempts != 2 || live.String() != "hello world" || final.String() != live.String() || gotErr != nil {
		t.Fatalf("attempts=%d live=%q final=%q err=%v", attempts, live.String(), final.String(), gotErr)
	}
}

func TestBedrockIdleHTTP2ContinuationAndExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		text, exhaust, reset bool
	}{
		{"continuation", true, false, false},
		{"exhaustion before text", false, true, false},
		{"exhaustion after text", true, true, false},
		{"idle and reset share budget", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			payloads := make(chan []byte, maxBedrockStreamSegments)
			closed := make(chan struct{}, maxBedrockStreamSegments)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { closed <- struct{}{} }()
				if r.ProtoMajor != 2 {
					t.Errorf("expected HTTP/2")
				}
				payload, _ := io.ReadAll(r.Body)
				payloads <- payload
				n := attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if !tc.exhaust && n == 2 {
					_, _ = io.WriteString(w, sapEvents(`{"type":"content_block_delta","delta":{"type":"text_delta","text":" done"}}`, sapTool, sapToolStop, sapStop))
					return
				}
				if tc.text {
					_, _ = io.WriteString(w, sapEvents(sapText))
				}
				_, _ = io.WriteString(w, sapEvents(sapTool, sapToolStop))
				w.(http.Flusher).Flush()
				if tc.reset && n == 2 {
					panic(http.ErrAbortHandler)
				}
				<-r.Context().Done()
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			p := testStreamProvider(server.Client(), server.URL)
			if err := WithBedrockStreamTiming(70*time.Millisecond, 15*time.Millisecond)(p); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var live, final strings.Builder
			var gotErr error
			tools, finals, recoveries := 0, 0, 0
			ctx = streaming.WithProgress(ctx, func(p streaming.Progress) {
				if p.Kind == streaming.KindRecovery {
					recoveries++
					if p.Attempt != recoveries+1 || p.MaxAttempts != maxBedrockStreamSegments || p.Phase != streaming.PhaseRecovering {
						t.Errorf("recovery=%+v", p)
					}
				}
			})
			for response, err := range p.GenerateContent(ctx, &model.LLMRequest{}, true) {
				if err != nil {
					gotErr = err
					continue
				}
				if !response.Partial {
					finals++
				}
				for _, part := range response.Content.Parts {
					if part.FunctionCall != nil {
						tools++
					}
					if response.Partial {
						live.WriteString(part.Text)
					} else {
						final.WriteString(part.Text)
					}
				}
			}
			if tc.text && !tc.exhaust {
				if gotErr != nil || attempts.Load() != 2 || tools != 1 || finals != 1 || recoveries != 1 || live.String() != "hello done" || final.String() != live.String() {
					t.Fatalf("attempts=%d tools=%d finals=%d recoveries=%d text=%q final=%q err=%v", attempts.Load(), tools, finals, recoveries, live.String(), final.String(), gotErr)
				}
			} else if tc.text {
				if attempts.Load() != maxBedrockStreamSegments || tools != 0 || finals != 0 || recoveries != maxBedrockStreamSegments-1 || !errors.Is(gotErr, ErrBedrockStreamIdle) {
					t.Fatalf("attempts=%d tools=%d finals=%d recoveries=%d text=%q err=%v", attempts.Load(), tools, finals, recoveries, live.String(), gotErr)
				}
				var interrupted *llmerror.InterruptedStreamError
				if !errors.As(gotErr, &interrupted) || llmerror.IsRetryable(gotErr) {
					t.Fatalf("unsafe error: %v", gotErr)
				}
			} else if tc.exhaust {
				if attempts.Load() != maxBedrockStreamSegments || tools != 0 || finals != 0 || recoveries != maxBedrockStreamSegments-1 || !errors.Is(gotErr, ErrBedrockStreamIdle) {
					t.Fatalf("attempts=%d tools=%d finals=%d recoveries=%d err=%v", attempts.Load(), tools, finals, recoveries, gotErr)
				}
				var interrupted *llmerror.InterruptedStreamError
				if errors.As(gotErr, &interrupted) || llmerror.IsRetryable(gotErr) {
					t.Fatalf("unsafe error: %v", gotErr)
				}
			} else if gotErr != nil || attempts.Load() != 2 || tools != 1 || finals != 1 || live.String() != "hello done" || final.String() != live.String() {
				t.Fatalf("attempts=%d tools=%d finals=%d text=%q final=%q err=%v", attempts.Load(), tools, finals, live.String(), final.String(), gotErr)
			}
			for n := 0; n < int(attempts.Load()); n++ {
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("attempt handler not closed")
				}
				payload := <-payloads
				var request struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.Unmarshal(payload, &request); err != nil {
					t.Fatal(err)
				}
				if tc.text && n > 0 {
					if len(request.Messages) != 2 || request.Messages[0].Role != "assistant" || request.Messages[0].Content != strings.Repeat("hello", n) {
						t.Fatalf("bad continuation: %s", payload)
					}
				} else if len(request.Messages) != 0 {
					t.Fatalf("unexpected history: %s", payload)
				}
			}
		})
	}
}

func TestBedrockStreamTimingValidation(t *testing.T) {
	for _, timing := range [][2]time.Duration{{0, time.Second}, {time.Second, 0}, {-1, time.Second}, {time.Second, -1}} {
		p, err := NewProviderWithConfig(context.Background(), "model", "", "", "", "", "", WithBedrockStreamTiming(timing[0], timing[1]))
		if p != nil || err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Fatalf("p=%v err=%v", p, err)
		}
	}
	p := &Provider{}
	if err := WithBedrockStreamTiming(time.Second, time.Millisecond)(p); err != nil || p.bedrockIdleTimeout != time.Second || p.bedrockHeartbeatInterval != time.Millisecond {
		t.Fatalf("timing not applied: %v", err)
	}
	if defaultBedrockIdleTimeout != 120*time.Second || defaultBedrockHeartbeatInterval != 15*time.Second {
		t.Fatal("unexpected defaults")
	}
}

func TestBedrockContinuationPayloadRejectsNullHistory(t *testing.T) {
	for _, original := range []string{`null`, `{}`, `{"messages":null}`, `{"messages":{}}`, `{"messages":"history"}`} {
		t.Run(original, func(t *testing.T) {
			payload, err := bedrockContinuationPayload([]byte(original), "already delivered")
			if err == nil || payload != nil {
				t.Fatalf("expected rejection without a replacement payload: payload=%s err=%v", payload, err)
			}
		})
	}
}

func TestBedrockContinuationPayloadPreservesHistory(t *testing.T) {
	original := []byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":1234,"system":"unchanged","temperature":0.2,"tools":[{"name":"run"}],"extra":{"future":true},"messages":[{"role":"user","content":"task"},{"role":"assistant","content":[{"type":"tool_use","id":"completed","name":"run","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"completed","content":"already done"}]}]}`)
	got, err := bedrockContinuationPayload(original, "partial 🧪\ntext")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(original, &before)
	_ = json.Unmarshal(got, &after)
	for key, value := range before {
		if key != "messages" && string(after[key]) != string(value) {
			t.Fatalf("changed %s", key)
		}
	}
	var oldMessages, newMessages []json.RawMessage
	_ = json.Unmarshal(before["messages"], &oldMessages)
	_ = json.Unmarshal(after["messages"], &newMessages)
	if len(newMessages) != len(oldMessages)+2 {
		t.Fatal("incorrect history length")
	}
	for i, value := range oldMessages {
		if string(value) != string(newMessages[i]) {
			t.Fatal("completed tool history changed")
		}
	}
	var textMessage struct{ Content string }
	_ = json.Unmarshal(newMessages[3], &textMessage)
	if textMessage.Content != "partial 🧪\ntext" {
		t.Fatal("text altered")
	}
	for _, bad := range []string{`bad`, `{}`, `{"messages":{}}`} {
		if _, err := bedrockContinuationPayload([]byte(bad), "text"); err == nil {
			t.Fatalf("accepted malformed payload %s", bad)
		}
	}
}

func TestBedrockStreamContinuationTransitions(t *testing.T) {
	reset := http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}
	for _, tc := range []struct {
		name           string
		secondText     bool
		terminalStatus int
		cancel         bool
	}{
		{name: "reset before continuation output"},
		{name: "reset after more text", secondText: true},
		{name: "continuation rate limit is not outer replay", terminalStatus: 429},
		{name: "cancel continuation", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := 0
			var payloads []string
			client := &http.Client{Transport: streamRoundTrip(func(r *http.Request) (*http.Response, error) {
				attempts++
				data, _ := io.ReadAll(r.Body)
				payloads = append(payloads, string(data))
				if attempts == 2 && tc.terminalStatus != 0 {
					return &http.Response{StatusCode: tc.terminalStatus, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("overloaded"))}, nil
				}
				if attempts == 2 && tc.cancel {
					cancel()
					return nil, ctx.Err()
				}
				input := sapEvents(sapText)
				if attempts == 2 && !tc.secondText {
					input = ""
				}
				var reader io.Reader = io.MultiReader(strings.NewReader(input), streamReadFailure{reset})
				if attempts == 3 {
					reader = strings.NewReader(sapEvents(`{"type":"content_block_delta","delta":{"type":"text_delta","text":" done"}}`, sapStop))
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(reader)}, nil
			})}
			var live, aggregate strings.Builder
			var gotErr error
			for response, err := range testStreamProvider(client, "http://provider.test").GenerateContent(ctx, &model.LLMRequest{}, true) {
				if err != nil {
					gotErr = err
					continue
				}
				if response != nil && response.Content != nil {
					for _, part := range response.Content.Parts {
						if response.Partial {
							live.WriteString(part.Text)
						} else {
							aggregate.WriteString(part.Text)
						}
					}
				}
			}
			if tc.terminalStatus != 0 || tc.cancel {
				var interrupted *llmerror.InterruptedStreamError
				if attempts != 2 || !errors.As(gotErr, &interrupted) || llmerror.IsRetryable(gotErr) || aggregate.Len() != 0 {
					t.Fatalf("attempts=%d err=%v aggregate=%q", attempts, gotErr, aggregate.String())
				}
				if tc.cancel && !errors.Is(gotErr, context.Canceled) {
					t.Fatalf("lost cancel: %v", gotErr)
				}
				return
			}
			want := "hello done"
			prefix := "hello"
			if tc.secondText {
				want = "hellohello done"
				prefix = "hellohello"
			}
			if attempts != 3 || gotErr != nil || live.String() != want || aggregate.String() != want {
				t.Fatalf("attempts=%d err=%v live=%q final=%q", attempts, gotErr, live.String(), aggregate.String())
			}
			var request struct {
				Messages []struct{ Role, Content string }
			}
			if err := json.Unmarshal([]byte(payloads[2]), &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) != 2 || request.Messages[0].Content != prefix {
				t.Fatalf("accumulated continuation wrong: %s", payloads[2])
			}
		})
	}
}

func TestBedrockStreamContinuationBounded(t *testing.T) {
	attempts := 0
	reset := http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}
	client := &http.Client{Transport: streamRoundTrip(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(io.MultiReader(strings.NewReader(sapEvents(sapText)), streamReadFailure{reset}))}, nil
	})}
	var finalErr error
	finals := 0
	for response, err := range testStreamProvider(client, "http://provider.test").GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil {
			finalErr = err
		}
		if response != nil && !response.Partial {
			finals++
		}
	}
	var interrupted *llmerror.InterruptedStreamError
	if attempts != maxBedrockStreamSegments || finals != 0 || !errors.As(finalErr, &interrupted) || llmerror.IsRetryable(finalErr) {
		t.Fatalf("attempts=%d finals=%d error=%v", attempts, finals, finalErr)
	}
}
