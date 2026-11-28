package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/common"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestParallelToolCallsWire(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, option := range []any{nil, false, true, "false"} {
				for _, tools := range []bool{false, true} {
					t.Run(fmt.Sprintf("responses=%v/stream=%v/option=%v:%T/tools=%v", responses, streaming, option, option, tools), func(t *testing.T) {
						requests := 0
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests++
							wantPath := "/v1/chat/completions"
							if responses {
								wantPath = "/v1/responses"
							}
							if r.Method != http.MethodPost || r.URL.Path != wantPath {
								t.Errorf("request route = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
							}
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
							}
							got, present := body[common.ParallelToolCallsOption]
							if option != nil && tools {
								if !present || got != option {
									t.Errorf("option=%v present=%v, want %v", got, present, option)
								}
							} else if present {
								t.Error("absent option or tool-less request gained parallel_tool_calls")
							}
							if _, present := body["arbitrary_override"]; present {
								t.Error("unrelated extra forwarded")
							}
							if streaming {
								w.Header().Set("Content-Type", "text/event-stream")
								if responses {
									_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n")
								} else {
									_, _ = fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
								}
							} else {
								w.Header().Set("Content-Type", "application/json")
								if responses {
									_, _ = fmt.Fprint(w, `{"id":"r1","status":"completed","output":[]}`)
								} else {
									_, _ = fmt.Fprint(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
								}
							}
						}))
						defer srv.Close()
						cfg := openai.DefaultConfig("test")
						cfg.BaseURL = srv.URL + "/v1"
						p := NewProvider(openai.NewClientWithConfig(cfg), "test", true)
						p.useResponsesAPI = responses
						req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, Config: &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{ExtraBody: map[string]any{"arbitrary_override": true}}}}
						if option != nil {
							req.Config.HTTPOptions.ExtraBody[common.ParallelToolCallsOption] = option
						}
						if tools {
							req.Config.Tools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", ParametersJsonSchema: map[string]any{"type": "object"}}}}}
						}
						var gotErr error
						for _, err := range p.GenerateContent(t.Context(), req, streaming) {
							if err != nil {
								gotErr = err
							}
						}
						if _, invalid := option.(string); invalid {
							if gotErr == nil || requests != 0 {
								t.Fatalf("invalid option sent: requests=%d err=%v", requests, gotErr)
							}
						} else if gotErr != nil || requests != 1 {
							t.Fatalf("requests=%d err=%v", requests, gotErr)
						}
					})
				}
			}
		}
	}
}
