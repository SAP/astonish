package launcher

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
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	openaiadapter "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/store"
	openai "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type synthesisEmptyModel struct {
	calls    int
	generate func(int, func(*model.LLMResponse, error) bool)
}

func (*synthesisEmptyModel) Name() string { return "empty-final" }
func (m *synthesisEmptyModel) GenerateContent(_ context.Context, _ *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		m.generate(m.calls, yield)
	}
}
func emptySynthesisError() error {
	return &provider.EmptyCompletionError{Provider: "controlled", FinishReason: genai.FinishReasonMaxTokens}
}
func completeSynthesisResponse() *model.LLMResponse {
	return &model.LLMResponse{Content: genai.NewContentFromText("Design from retained evidence. Missing sources remain unavailable.", "model"), TurnComplete: true, FinishReason: genai.FinishReasonStop}
}

func TestBotSynthesisEmptyCompletionSafety(t *testing.T) {
	for _, name := range []string{"recovers", "twice", "tool_then_empty", "empty_then_tool", "partial", "refusal", "unknown", "untyped_empty", "trailing_settlement_error", "cancel", "lease", "revoked", "progress_unsafe", "progress_settlement_error", "budget"} {
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = meter
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			llm := botFactoryModelForTest(t, got.Agent.LLM)
			llm.binding.synthesisReserve.final = true
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			drained := 0
			raw := &synthesisEmptyModel{}
			raw.generate = func(call int, yield func(*model.LLMResponse, error) bool) {
				defer func() { drained++ }()
				if call == 2 && drained != 1 {
					t.Fatal("retry before drain")
				}
				tool := &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file"}}}}}
				if name == "tool_then_empty" && call == 1 || name == "empty_then_tool" && call == 2 {
					yield(tool, nil)
					return
				}
				if call == 2 && name != "twice" && name != "tool_then_empty" {
					yield(completeSynthesisResponse(), nil)
					return
				}
				switch name {
				case "partial":
					yield(&model.LLMResponse{Content: genai.NewContentFromText("incomplete prose", "model"), Partial: true}, nil)
				case "refusal":
					yield(nil, &provider.EmptyCompletionError{Provider: "controlled", FinishReason: genai.FinishReasonStop, Refused: true})
					return
				case "unknown":
					yield(nil, &provider.EmptyCompletionError{Provider: "controlled"})
					return
				case "untyped_empty":
					yield(&model.LLMResponse{TurnComplete: true}, nil)
					return
				case "cancel":
					cancel()
				case "lease":
					f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
				case "revoked":
					llm.binding.verifyIsolation = func(context.Context) error { return store.ErrBotForbidden }
				case "progress_unsafe":
					llm.binding.progress.active++
				case "progress_settlement_error":
					llm.binding.progress.pending = append(llm.binding.progress.pending, func(context.Context) error { return errors.New("settlement failed") })
				case "budget":
					llm.binding.req.Budget = &synthesisFailingBudget{RunBudget: meter}
				}
				yield(&model.LLMResponse{FinishReason: genai.FinishReasonMaxTokens}, emptySynthesisError())
				if name == "trailing_settlement_error" {
					yield(nil, errors.New("settlement failed"))
				}
			}
			llm.binding.req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			progress := &progressDispatchProbe{}
			llm.binding.owner = bot.WithProgressDispatch(llm.binding.owner, progress)
			llm.binding.progress = &botProgressBridge{}
			llm.LLM = &botProgressLLM{LLM: raw, binding: llm.binding}
			var failures, finals int
			for response, err := range llm.GenerateContent(ctx, &model.LLMRequest{}, true) {
				if err != nil {
					failures++
				}
				if response != nil && response.Content != nil {
					finals++
					if name != "recovers" && name != "partial" {
						t.Fatal("invalid output escaped")
					}
				}
			}
			wantCalls := 1
			if name == "recovers" || name == "partial" || name == "twice" || name == "tool_then_empty" || name == "empty_then_tool" {
				wantCalls = 2
			}
			if raw.calls != wantCalls || drained != wantCalls {
				t.Fatalf("calls=%d drained=%d want=%d", raw.calls, drained, wantCalls)
			}
			if name == "recovers" || name == "partial" {
				if finals != 1 || failures != 0 {
					t.Fatalf("finals=%d failures=%d", finals, failures)
				}
			} else if finals != 0 || failures != 1 {
				t.Fatalf("finals=%d failures=%d", finals, failures)
			}
		})
	}
}

type synthesisFailingBudget struct{ execution.RunBudget }

func (*synthesisFailingBudget) Check(context.Context) error { return errors.New("budget exhausted") }

// Exercises the real HTTP adapter inside the production factory's progress /
// shared-budget composition, without paid providers or replaying any tool.
func TestBotSynthesisEmptyCompletionHTTP(t *testing.T) {
	testBotSynthesisCompletionHTTP(t, "")
}

func TestBotSynthesisOutputLimitCompletionHTTP(t *testing.T) {
	testBotSynthesisCompletionHTTP(t, "Visible partial design from retained evidence")
}

func testBotSynthesisCompletionHTTP(t *testing.T, draft string) {
	t.Helper()
	f := newBotFactoryFixture(t)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	requests := []openai.ChatCompletionRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests = append(requests, q)
		if q.Stream || len(q.Tools) != 0 || q.MaxCompletionTokens != 128 {
			t.Errorf("not bounded tool-free synthesis: %+v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":%q,"reasoning_content":"private reasoning"}}],"usage":{"prompt_tokens":10,"completion_tokens":128,"total_tokens":138}}`, draft)
			return
		}
		// Usage includes the second call's live reservation at this point.
		correction := q.Messages[len(q.Messages)-1].Content
		if (draft != "" && !strings.Contains(correction, draft)) || strings.Contains(correction, "private reasoning") {
			t.Error("correction lost visible draft or included private reasoning")
		}
		if meter.Usage().OutputTokens != 256 {
			t.Errorf("attempt reservations not metered: %+v", meter.Usage())
		}
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"# Design\nUse retained evidence. Missing source remains unavailable."}}],"usage":{"prompt_tokens":12,"completion_tokens":15,"total_tokens":27}}`)
	}))
	defer server.Close()
	cfg := openai.DefaultConfig("controlled")
	cfg.BaseURL = server.URL
	raw := openaiadapter.NewProvider(openai.NewClientWithConfig(cfg), "controlled-reasoner", true)
	f.cfg.WrapModel = func(_ context.Context, _ model.LLM, b execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(raw, b, provider.BudgetLLMOptions{MaxOutputTokens: 128, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 10, nil }})
	}
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	llm := botFactoryModelForTest(t, got.Agent.LLM)
	llm.binding.synthesisReserve.final = true
	query := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Prepare a design using already retained evidence.", "user")}, Config: &genai.GenerateContentConfig{MaxOutputTokens: 256, Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file"}}}}}}
	finals := 0
	for response, err := range llm.GenerateContent(f.ctx, query, true) {
		if err != nil {
			t.Fatal(err)
		}
		if response != nil && response.Content != nil {
			finals++
			if !strings.Contains(response.Content.Parts[0].Text, "# Design") {
				t.Fatalf("unexpected output %+v", response)
			}
		}
	}
	if finals != 1 || len(requests) != 2 {
		t.Fatalf("finals=%d requests=%d", finals, len(requests))
	}
	if u := meter.Usage(); u.InputTokens != 22 || u.OutputTokens != 143 || u.ToolCalls != 0 {
		t.Fatalf("same budget usage %+v", u)
	}
	if !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].Content, "final generation ended without an answer") {
		t.Fatal("missing bounded empty correction")
	}
	if query.Config.MaxOutputTokens != 256 || len(query.Config.Tools) != 1 || len(query.Contents) != 1 {
		t.Fatal("caller request mutated")
	}

	// Repeat through the real API adapter, including durable work publication.
	requests = nil
	f.req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Instructions: f.req.Instructions, Charter: f.req.Version.Charter})
	repo := &synthesisWorkRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, work: f.req.Work.Clone()}
	f.services.Bots, f.services.PersonalBots = repo, repo
	meter, err = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 300}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		resources, err := factory(ctx, req)
		if err == nil {
			botFactoryModelForTest(t, resources.Agent.LLM).binding.synthesisReserve.final = true
		}
		return resources, err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunSucceeded || result.Report.EmployeeMessage == nil || !strings.Contains(result.Report.EmployeeMessage.Text, "# Design") {
		t.Fatalf("final design not published: %+v", result)
	}
	if len(requests) != 2 || meter.Usage().ToolCalls != 0 || meter.Usage().OutputTokens != 143 {
		t.Fatalf("API replayed work or lost usage: calls=%d usage=%+v", len(requests), meter.Usage())
	}
}
