package daemon

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotGenerationContextAndExplicitOptions(t *testing.T) {
	ctx := provider.WithConfinedProviderSecrets(context.Background())
	cfg := &config.AppConfig{Providers: map[string]config.ProviderConfig{"scoped": {"type": "sap_ai_core"}}}
	llm := newBotGenerationModel(ctx, &workerTestModel{}, "scoped", "gpt-5.6-terra", cfg)
	if got := botModelContextWindow(ctx, llm); got != 272000 {
		t.Fatalf("context=%d", got)
	}
	if got := botModelBudgetOptions(llm, provider.BudgetLLMOptions{}); got.MaxOutputTokens != 32768 || !got.SupportsOutputLimit {
		t.Fatalf("options=%+v", got)
	}
	cfg.General.ContextLength = 32768
	override := newBotGenerationModel(ctx, &workerTestModel{}, "scoped", "gpt-5.6-terra", cfg)
	if got := botModelContextWindow(ctx, override); got != 32768 {
		t.Fatalf("override=%d", got)
	}
	if got := botModelContextWindow(ctx, llm); got != 272000 {
		t.Fatal("source config mutation changed resolved limits")
	}
	if got := botModelContextWindow(ctx, &workerTestModel{}); got != provider.DefaultContextWindow {
		t.Fatalf("fallback=%d", got)
	}
	explicit := provider.BudgetLLMOptions{MaxOutputTokens: 1024, SupportsOutputLimit: false, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 17, nil }}
	got := botModelBudgetOptions(llm, explicit)
	if got.MaxOutputTokens != 1024 || got.SupportsOutputLimit || got.EstimateInput == nil {
		t.Fatalf("explicit options changed: %+v", got)
	}
}

func TestNewBotGenerationModelNumericCapabilities(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                         string
		window, capacity, wantWindow int
		wantOutput                   int32
	}{
		{"live metadata", 1050000, 128000, 1050000, 32768},
		{"small output capacity", 64000, 4096, 64000, 4096},
		{"unknown capabilities", 0, 0, provider.DefaultContextWindow, provider.DefaultBudgetOutputTokens},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &workerTestModel{}
			llm := NewBotGenerationModel(inner, tc.window, tc.capacity)
			if llm.Name() != inner.Name() {
				t.Fatal("model identity changed")
			}
			if factoryContext := botModelContextWindow(ctx, llm); factoryContext != tc.wantWindow {
				t.Fatalf("factory context=%d want=%d", factoryContext, tc.wantWindow)
			}
			if options := botModelBudgetOptions(llm, provider.BudgetLLMOptions{}); options.MaxOutputTokens != tc.wantOutput || !options.SupportsOutputLimit {
				t.Fatalf("factory options=%+v", options)
			}
		})
	}
}

type generationCaptureModel struct{ cap int32 }

func (*generationCaptureModel) Name() string { return "gpt-5.6-terra" }
func (m *generationCaptureModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.cap = r.Config.MaxOutputTokens
		yield(&model.LLMResponse{Content: genai.NewContentFromText("final", "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 5000}}, nil)
	}
}

func TestBotGenerationBudgetAndRequestClamp(t *testing.T) {
	for _, tt := range []struct {
		name          string
		tokens        int64
		request, want int32
	}{
		{"reasoning headroom", 100000, 0, 32768},
		{"request bound", 100000, 8000, 8000},
		{"remaining budget", 6100, 0, 6000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &tt.tokens}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			inner := &generationCaptureModel{}
			llm := NewBotGenerationModel(inner, 1050000, 128000)
			// These are the exact callbacks used by NewBotWorker's factory.
			if factoryContext := botModelContextWindow(context.Background(), llm); factoryContext != 1050000 {
				t.Fatalf("factory context=%d", factoryContext)
			}
			opts := botModelBudgetOptions(llm, provider.BudgetLLMOptions{EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }})
			wrapped, err := provider.NewBudgetLLM(llm, budget, opts)
			if err != nil {
				t.Fatal(err)
			}
			req := &model.LLMRequest{Config: &genai.GenerateContentConfig{MaxOutputTokens: tt.request}}
			for _, err := range wrapped.GenerateContent(context.Background(), req, false) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if inner.cap != tt.want || req.Config.MaxOutputTokens != tt.request {
				t.Fatalf("cap=%d request=%d", inner.cap, req.Config.MaxOutputTokens)
			}
			if usage := budget.Usage(); usage.InputTokens != 100 || usage.OutputTokens != 5000 {
				t.Fatalf("usage=%+v", usage)
			}
		})
	}
}
