package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/provider/httpool"
	"github.com/SAP/astonish/pkg/provider/streaming"
)

const completedHealthResponse = `{"type":"response.completed","response":{"id":"resp_health","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},{"type":"function_call","call_id":"call_health","name":"safe_tool","arguments":"{\"ok\":true}"}],"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}}`

func healthResponsesProvider(url string) *Provider {
	cfg := goopenai.DefaultConfig("fixture")
	cfg.BaseURL = url
	cfg.HTTPClient = httpool.StreamingClient()
	return NewProviderWithResponses(goopenai.NewClientWithConfig(cfg), "gpt-6", true, 500)
}

func TestUnmonitoredResponsesRetainsNonStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] == true {
			t.Error("unmonitored nonstreaming call forced to SSE")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"function_call","call_id":"only_tool","name":"safe_tool","arguments":"{}"}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}`)
	}))
	defer server.Close()
	count := 0
	for response, err := range healthResponsesProvider(server.URL).GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if !response.TurnComplete || response.FinishReason != genai.FinishReasonStop {
			t.Fatalf("completed response lost terminal metadata: %+v", response)
		}
		if response.Partial || response.UsageMetadata.TotalTokenCount != 10 || response.Content.Parts[0].FunctionCall.ID != "only_tool" {
			t.Fatal("tool-only usage lost")
		}
	}
	if count != 1 {
		t.Fatal(count)
	}
}

func TestMonitoredResponsesBufferedCompletedWithoutEOF(t *testing.T) {
	for _, partials := range []bool{false, true} {
		t.Run(fmt.Sprintf("partials=%v", partials), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["stream"] != true || body["max_output_tokens"] != float64(77) {
					t.Errorf("request settings: %v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []string{
					`{"type":"response.reasoning_text.delta","delta":"private thought"}`,
					`{"type":"response.output_text.delta","delta":"done"}`,
					`{"type":"response.function_call_arguments.delta","delta":"{\"ok\":"}`,
					completedHealthResponse,
				} {
					fmt.Fprintf(w, "data: %s\n\n", event)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done() // Deliberately never sends EOF before client closes.
			}))
			defer server.Close()
			parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ctx, stop := streaming.StartHealthMonitor(parent, streaming.HealthOptions{IdleTimeout: time.Second, CheckInterval: 10 * time.Millisecond})
			defer stop()
			responses := []*model.LLMResponse{}
			for response, err := range healthResponsesProvider(server.URL).GenerateContent(ctx, &model.LLMRequest{Config: &genai.GenerateContentConfig{MaxOutputTokens: 77}}, partials) {
				if err != nil {
					t.Fatal(err)
				}
				responses = append(responses, response)
			}
			if err := stop(); err != nil {
				t.Fatalf("waited for EOF: %v", err)
			}
			want := 1
			if partials {
				want = 3
			}
			if len(responses) != want {
				t.Fatalf("got %d responses, want %d", len(responses), want)
			}
			final := responses[len(responses)-1]
			if !final.TurnComplete || final.FinishReason != genai.FinishReasonStop {
				t.Fatalf("completed SSE response lost terminal metadata: %+v", final)
			}
			if final.Partial || final.UsageMetadata.TotalTokenCount != 25 || len(final.Content.Parts) != 2 || final.Content.Parts[1].FunctionCall.ID != "call_health" || final.Content.Parts[1].FunctionCall.Args["ok"] != true {
				t.Fatalf("final content/usage lost: %+v", final)
			}
		})
	}
}

func TestMonitoredResponsesRejectIncompleteAndInvalidTools(t *testing.T) {
	for name, event := range map[string]string{
		"eof":           `{"type":"response.function_call_arguments.delta","delta":"{"}`,
		"incomplete":    `{"type":"response.incomplete","response":{"status":"incomplete"}}`,
		"failed":        `{"type":"response.failed"}`,
		"missing_final": `{"type":"response.completed"}`,
		"invalid_tools": `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"tool","arguments":"{"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", event)
			}))
			defer server.Close()
			ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{IdleTimeout: time.Second})
			defer stop()
			failed := false
			for response, err := range healthResponsesProvider(server.URL).GenerateContent(ctx, &model.LLMRequest{}, false) {
				if response != nil {
					t.Fatal("published unvalidated output")
				}
				failed = failed || err != nil
			}
			if !failed {
				t.Fatal("missing failure")
			}
		})
	}
}

func TestMonitoredResponsesSSECommentsAreRealActivity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 15; i++ {
			fmt.Fprint(w, ": actual upstream keepalive\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		fmt.Fprintf(w, "data: %s\n\n", completedHealthResponse)
	}))
	defer server.Close()
	ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{IdleTimeout: 200 * time.Millisecond, CheckInterval: 10 * time.Millisecond})
	defer stop()
	count := 0
	for _, err := range healthResponsesProvider(server.URL).GenerateContent(ctx, &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 {
		t.Fatal(count)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitoredResponsesIdleCauseAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, stop := streaming.StartHealthMonitor(context.Background(), streaming.HealthOptions{IdleTimeout: 40 * time.Millisecond, CheckInterval: 5 * time.Millisecond})
	defer stop()
	var got error
	for _, err := range healthResponsesProvider(server.URL).GenerateContent(ctx, &model.LLMRequest{}, false) {
		got = err
	}
	var stalled *streaming.GenerationStalledError
	if !errors.As(got, &stalled) || !errors.As(stop(), &stalled) {
		t.Fatalf("missing typed idle cause: %v", got)
	}
}
