package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type workStreamEvent struct {
	response *model.LLMResponse
	err      error
}

type workStreamModel struct {
	t        *testing.T
	attempts [][]workStreamEvent
	calls    int
}

func (m *workStreamModel) Name() string { return "work-stream" }
func (m *workStreamModel) GenerateContent(_ context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.t.Helper()
	if !stream {
		m.t.Error("assessment did not request streaming")
	}
	if len(request.Tools) != 0 || len(request.Config.Tools) != 0 {
		m.t.Error("assessment enabled tools")
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls > len(m.attempts) {
			m.t.Error("unexpected assessment retry")
			return
		}
		for _, event := range m.attempts[m.calls-1] {
			if !yield(event.response, event.err) {
				return
			}
		}
	}
}

func workStreamText(text string, partial bool, input, output int32) workStreamEvent {
	return workStreamEvent{response: &model.LLMResponse{
		Content: genai.NewContentFromText(text, "model"), Partial: partial,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: input, CandidatesTokenCount: output},
	}}
}

func TestBotWorkAssessmentStream(t *testing.T) {
	valid := `{"value":"complete"}`
	streamErr := errors.New("stream transport failed")
	for _, tc := range []struct {
		name          string
		events        []workStreamEvent
		wantErr       string
		input, output int64
	}{
		{name: "split-and-cumulative-final", events: []workStreamEvent{workStreamText(`{"value":`, true, 4, 1), workStreamText(`"complete"}`, true, 4, 3), workStreamText(valid, false, 4, 3)}, input: 4, output: 3},
		{name: "authoritative-final", events: []workStreamEvent{workStreamText(`{"value":"obsolete"}`, true, 4, 5), workStreamText(valid, false, 4, 3)}, input: 4, output: 5},
		{name: "nonstream-compatible-final", events: []workStreamEvent{workStreamText(valid, false, 4, 3)}, input: 4, output: 3},
		{name: "repeated-final-usage", events: []workStreamEvent{workStreamText(valid, false, 4, 3), {response: &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 4, CandidatesTokenCount: 3}}}}, input: 4, output: 3},
		{name: "incomplete-valid-json", events: []workStreamEvent{workStreamText(valid, true, 4, 3)}, wantErr: "incomplete", input: 4, output: 3},
		{name: "empty-stream", wantErr: "incomplete"},
		{name: "interrupted-valid-json", events: []workStreamEvent{workStreamText(valid, true, 4, 3), {response: &model.LLMResponse{Interrupted: true}}}, wantErr: "interrupted", input: 4, output: 3},
		{name: "response-error-code", events: []workStreamEvent{workStreamText(valid, true, 4, 3), {response: &model.LLMResponse{ErrorCode: "failed"}}}, wantErr: "failed", input: 4, output: 3},
		{name: "response-error-message", events: []workStreamEvent{workStreamText(valid, true, 4, 3), {response: &model.LLMResponse{ErrorMessage: "private-error"}}}, wantErr: "failed", input: 4, output: 3},
		{name: "iterator-error-after-final", events: []workStreamEvent{workStreamText(valid, false, 4, 3), {err: streamErr}}, wantErr: streamErr.Error(), input: 4, output: 3},
		{name: "usage-on-error", events: []workStreamEvent{workStreamText(valid, true, 4, 3), {response: &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 6}}, err: streamErr}}, wantErr: streamErr.Error(), input: 5, output: 6},
		{name: "tool-after-valid-json", events: []workStreamEvent{workStreamText(valid, true, 4, 3), {response: &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "forbidden"}}}}}}}, wantErr: "tool call", input: 4, output: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{tc.events}}
			out := struct {
				Value string `json:"value"`
			}{Value: "unchanged"}
			usage := store.BotUsage{InputTokens: 10, OutputTokens: 20, ToolCalls: 2}
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			if tc.wantErr == "" {
				if err != nil || out.Value != "complete" {
					t.Fatalf("output=%+v err=%v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) || out.Value != "unchanged" {
				t.Fatalf("want failure %q without destination mutation; output=%+v err=%v", tc.wantErr, out, err)
			}
			if llm.calls != 1 || usage.InputTokens != 10+tc.input || usage.OutputTokens != 20+tc.output || usage.ToolCalls != 2 {
				t.Fatalf("calls=%d usage=%+v", llm.calls, usage)
			}
		})
	}
}

// writeBotWorkTestSSE exercises the actual provider parser, including a usage
// snapshot after finish_reason. Truncation deliberately omits finish_reason.
func writeBotWorkTestSSE(t *testing.T, w http.ResponseWriter, deltas []string, truncated bool) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, delta := range deltas {
		text, err := json.Marshal(delta)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\n", text)
		w.(http.Flusher).Flush()
	}
	if !truncated {
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}
	_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n")
	if !truncated {
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}
	w.(http.Flusher).Flush()
}

func TestBotWorkAssessmentStreamHTTP(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "truncated-valid-json"}[truncated], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var wire struct {
					Stream bool  `json:"stream"`
					Tools  []any `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
				}
				if !wire.Stream || len(wire.Tools) != 0 {
					t.Errorf("invalid assessment request: %+v", wire)
				}
				writeBotWorkTestSSE(t, w, []string{`{"value":`, `"complete"}`}, truncated)
			}))
			defer server.Close()
			ctx, req, _ := botChatTestRequest(t)
			llm := openai_compat.NewProvider(ctx, "test-only", server.URL, "claude-4.8-opus", false)
			out := struct {
				Value string `json:"value"`
			}{Value: "unchanged"}
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			want := "complete"
			if truncated {
				want = "unchanged"
			}
			if (err != nil) != truncated || out.Value != want || calls != 1 || usage.InputTokens != 4 || usage.OutputTokens != 2 {
				t.Fatalf("output=%+v calls=%d usage=%+v err=%v", out, calls, usage, err)
			}
		})
	}
}

func TestBotWorkAssessmentStreamFormatRetryAccounting(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failing-second-call"}[failing], func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			second := []workStreamEvent{workStreamText(`{"value":1}`, true, 7, 4), workStreamText(`{"value":1}`, false, 7, 4)}
			if failing {
				second[1].err = errors.New("second call failed")
			}
			llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{
				{workStreamText(`{`, true, 4, 2), workStreamText(`{`, false, 4, 2)}, second,
			}}
			var out any
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			if (err != nil) != failing || llm.calls != 2 || usage.InputTokens != 11 || usage.OutputTokens != 6 {
				t.Fatalf("calls=%d usage=%+v err=%v", llm.calls, usage, err)
			}
		})
	}
}
