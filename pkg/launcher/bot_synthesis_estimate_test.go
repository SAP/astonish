package launcher

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type synthesisEstimateModel struct {
	calls     int
	tools     []bool
	health    []bool
	deadlines []bool
}

func (*synthesisEstimateModel) Name() string { return "synthesis-estimate" }
func (m *synthesisEstimateModel) GenerateContent(ctx context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		m.tools = append(m.tools, q.Config != nil && len(q.Config.Tools) > 0)
		m.health = append(m.health, streaming.HealthEnabled(ctx))
		_, deadline := ctx.Deadline()
		m.deadlines = append(m.deadlines, deadline)
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Retained findings.", "model"), TurnComplete: true, FinishReason: genai.FinishReasonStop, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 48000, CandidatesTokenCount: 100}}, nil)
	}
}

func TestBotSynthesisFactoryMeasuredPromptContinues(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-prefix", true: "changed-history"}[changed], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			limit := int64(1000000)
			spec := store.BudgetSpec{MaxRuntimeSeconds: 3600, MaxTokens: &limit, RuntimePolicy: execution.NewBotProgressRuntimePolicy()}
			// Construct through the existing factory fixture; activate progress on
			// its captured binding below, without invoking the durable runner.
			f.req.Version.Budget.MaxTokens = &limit
			f.req.Run.Lease.ExpiresAt = time.Now().Add(time.Hour)
			meter, err := bot.RestoreRunBudget(spec, time.Now(), store.BotUsage{InputTokens: 482000})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = meter
			dispatch := &progressDispatchProbe{}
			f.ctx = bot.WithProgressDispatch(f.ctx, dispatch)
			raw := &synthesisEstimateModel{}
			f.cfg.WrapModel = func(_ context.Context, _ model.LLM, b execution.RunBudget) (model.LLM, error) {
				return provider.NewBudgetLLM(raw, b, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
			}
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			binding := botFactoryModelForTest(t, got.Agent.LLM).binding
			binding.req.Version.Budget = spec
			q := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(strings.Repeat("evidence ", 27000), "user")}, Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file"}}}}}}
			generate := func() {
				t.Helper()
				before := meter.Usage()
				calls := raw.calls
				complete := false
				for response, err := range got.Agent.LLM.GenerateContent(f.ctx, q, false) {
					if err != nil {
						t.Fatalf("generation %d failed rather than delivering retained findings: %v", calls+1, err)
					}
					if response != nil && response.Content != nil && response.TurnComplete && response.FinishReason == genai.FinishReasonStop {
						for _, part := range response.Content.Parts {
							complete = complete || (part != nil && part.Text == "Retained findings.")
						}
					}
				}
				if !complete || raw.calls != calls+1 {
					t.Fatalf("expected one successful provider completion: complete=%v calls=%d before=%d", complete, raw.calls, calls)
				}
				if !raw.health[calls] || raw.deadlines[calls] {
					t.Fatalf("progress_v1 generation did not use health without an age deadline: health=%v deadlines=%v", raw.health, raw.deadlines)
				}
				if err := binding.progress.boundary(binding.owner); err != nil {
					t.Fatal(err)
				}
				if dispatch.began != raw.calls || dispatch.settled != raw.calls || dispatch.outcome != store.BotProgressDispatchSettled {
					t.Fatalf("generation bypassed progress dispatch or left an unsettled receipt: calls=%d dispatch=%+v", raw.calls, dispatch)
				}
				after := meter.Usage()
				if after.InputTokens != before.InputTokens+48000 || after.OutputTokens != before.OutputTokens+100 || after.ToolCalls != before.ToolCalls || after.InputTokens+after.OutputTokens > limit {
					t.Fatalf("generation reset, lost, or exceeded the cumulative budget: before=%+v after=%+v", before, after)
				}
			}
			generate()
			if !raw.tools[0] {
				t.Fatal("initial research request unexpectedly entered final synthesis")
			}
			if err := meter.ReserveTool(f.ctx, "read_file"); err != nil {
				t.Fatal(err)
			}
			q.Contents = append(q.Contents, genai.NewContentFromText("Additional source retained.", "user"))
			if changed {
				q.Contents[0] = genai.NewContentFromText(strings.Repeat("different", 27000), "user")
			}
			generate()
			if raw.tools[1] == changed {
				t.Fatalf("research should continue only on proven prefix: changed=%v tools=%v", changed, raw.tools)
			}
			if !changed {
				// Continued requests learn despite newly rendered remaining-budget guidance.
				q.Contents = append(q.Contents, genai.NewContentFromText("Another source retained.", "user"))
				generate()
				if !raw.tools[2] {
					t.Fatal("request-local guidance invalidated measured history")
				}
				// Real research generations spend from the SAME ledger. Stop soon
				// enough that the cold tools-disabled final request can still fit.
				for raw.calls < 12 && !binding.synthesisReserve.synthesis() {
					q.Contents = append(q.Contents, genai.NewContentFromText("Further source retained.", "user"))
					generate()
				}
			}
			if !binding.synthesisReserve.synthesis() || raw.tools[raw.calls-1] {
				t.Fatalf("true pressure did not produce a successful tools-disabled final answer: calls=%d tools=%v", raw.calls, raw.tools)
			}
			usage := meter.Usage()
			if usage.InputTokens != 482000+int64(raw.calls)*48000 || usage.OutputTokens != int64(raw.calls)*100 || usage.ToolCalls != 1 {
				t.Fatalf("final synthesis did not retain the original run ledger: calls=%d usage=%+v", raw.calls, usage)
			}
		})
	}
}
