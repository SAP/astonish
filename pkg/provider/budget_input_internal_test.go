package provider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type inputHistoryLLM struct{ name string }

func (m *inputHistoryLLM) Name() string { return m.name }
func (*inputHistoryLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return nil
}
func inputHistoryFixture(t *testing.T) (*BudgetLLM, *model.LLMRequest) {
	t.Helper()
	b, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	llm, err := NewBudgetLLM(&inputHistoryLLM{name: "model"}, b, BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
	if err != nil {
		t.Fatal(err)
	}
	req := &model.LLMRequest{Model: "model", Contents: []*genai.Content{genai.NewContentFromText(strings.Repeat("history ", 10000), genai.RoleUser)}, Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("system", genai.RoleUser), Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read_file", Description: "read"}}}}}}
	return llm, req
}
func TestBudgetInputHistoryExactPrefixAndBoundedDelta(t *testing.T) {
	llm, req := inputHistoryFixture(t)
	original, snapshot, err := llm.estimateInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	llm.inputHistory.observe(snapshot, 32824)
	appended := genai.NewContentFromText(strings.Repeat("🦊漢字\"", 1000), genai.RoleUser)
	req.Contents = append(req.Contents, appended)
	got, _, err := llm.estimateInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(appended)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(32824 + 256 + 64 + len(data))
	if got != want || got >= original {
		t.Fatalf("estimate=%d want=%d original=%d", got, want, original)
	}
	req.Contents = append(req.Contents, genai.NewContentFromText(strings.Repeat("z", 100000), genai.RoleUser))
	got, _, err = llm.estimateInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got <= 100000 {
		t.Fatalf("large appended text discounted: %d", got)
	}
}
func TestBudgetInputHistoryNeverCapsObservedTokensAtBytes(t *testing.T) {
	llm, req := inputHistoryFixture(t)
	bound, snapshot, err := llm.estimateInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	llm.inputHistory.observe(snapshot, bound+1000)
	got, _, err := llm.estimateInput(context.Background(), req)
	if err != nil || got != bound+1000+256 {
		t.Fatalf("authoritative count reduced: %d %v", got, err)
	}
}

func TestBudgetInputHistoryInvalidation(t *testing.T) {
	for _, mode := range []string{"history", "compaction", "system", "tools", "model", "inner-model", "config", "media", "cached", "different-wrapper"} {
		t.Run(mode, func(t *testing.T) {
			llm, req := inputHistoryFixture(t)
			_, snapshot, err := llm.estimateInput(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			llm.inputHistory.observe(snapshot, 32824)
			switch mode {
			case "history":
				req.Contents[0].Parts[0].Text += "changed"
			case "compaction":
				req.Contents = nil
			case "system":
				req.Config.SystemInstruction.Parts[0].Text += "changed"
			case "tools":
				req.Config.Tools[0].FunctionDeclarations[0].Description += "changed"
			case "model":
				req.Model = "other"
			case "inner-model":
				llm.inner.(*inputHistoryLLM).name = "other"
			case "config":
				req.Config.MaxOutputTokens = 123
			case "media":
				req.Contents = append(req.Contents, &genai.Content{Parts: []*genai.Part{{FileData: &genai.FileData{FileURI: "remote"}}}})
			case "cached":
				req.Config.CachedContent = "remote"
			case "different-wrapper":
				llm, _ = inputHistoryFixture(t)
			}
			want, wantErr := estimateBudgetInput(context.Background(), req)
			got, _, err := llm.estimateInput(context.Background(), req)
			if (err != nil) != (wantErr != nil) || got != want {
				t.Fatalf("%s: got=%d/%v want byte fallback=%d/%v", mode, got, err, want, wantErr)
			}
		})
	}
}
func TestBudgetInputHistoryRuntimeToolImplementationsAreNotPrompt(t *testing.T) {
	llm, req := inputHistoryFixture(t)
	_, snapshot, err := llm.estimateInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	llm.inputHistory.observe(snapshot, 32824)
	// ADK excludes runtime implementations from request JSON. Model-facing
	// declarations are in Config.Tools, covered by the invalidation test.
	req.Tools = map[string]any{"new": func() {}}
	got, _, err := llm.estimateInput(context.Background(), req)
	if err != nil || got != 32824+256 {
		t.Fatalf("estimate=%d err=%v", got, err)
	}
}

func TestBudgetInputHistoryCustomEstimatorAndConcurrency(t *testing.T) {
	llm, req := inputHistoryFixture(t)
	llm.options.EstimateInput = func(context.Context, *model.LLMRequest) (int64, error) { return 1234, nil }
	n, snapshot, err := llm.estimateInput(context.Background(), req)
	if err != nil || n != 1234 || snapshot != nil {
		t.Fatalf("custom estimator changed: %d %v %v", n, snapshot, err)
	}
	llm.options.EstimateInput = nil
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			_, snapshot, err := llm.estimateInput(context.Background(), req)
			if err != nil {
				t.Error(err)
				return
			}
			llm.inputHistory.observe(snapshot, int64(32000+i))
		})
	}
	wg.Wait()
	n, _, err = llm.estimateInput(context.Background(), req)
	if err != nil || n != 32029+256 {
		t.Fatalf("concurrent maximum lost: %d %v", n, err)
	}
}

type historyResponseLLM struct {
	response *model.LLMResponse
	err      error
	cancel   context.CancelFunc
}

func (*historyResponseLLM) Name() string { return "history-response" }
func (m *historyResponseLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.cancel != nil {
			m.cancel()
		}
		yield(m.response, m.err)
	}
}

type historySettlementFailure struct{ execution.RunBudget }

func (historySettlementFailure) SettleModel(context.Context, uuid.UUID, execution.BudgetUsage) error {
	return errors.New("settlement failure")
}

func TestBudgetInputHistoryOnlyLearnsCompleteSuccessfulPromptUsage(t *testing.T) {
	for _, mode := range []string{"success", "partial", "output-only", "tool-only-usage", "missing", "zero", "error", "interrupted", "response-error", "cancelled", "consumer-stop", "settlement-error"} {
		t.Run(mode, func(t *testing.T) {
			llm, req := inputHistoryFixture(t)
			m := &historyResponseLLM{response: &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 32824, CandidatesTokenCount: 194}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "partial":
				m.response.Partial = true
			case "output-only":
				m.response.UsageMetadata.PromptTokenCount = 0
			case "tool-only-usage":
				m.response.UsageMetadata.PromptTokenCount = 0
				m.response.UsageMetadata.ToolUsePromptTokenCount = 32824
			case "missing":
				m.response.UsageMetadata = nil
			case "zero":
				m.response.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{}
			case "error":
				m.err = errors.New("provider failure")
			case "interrupted":
				m.response.Interrupted = true
			case "response-error":
				m.response.ErrorCode = "failed"
			case "cancelled":
				m.cancel = cancel
			case "settlement-error":
				llm.budget = historySettlementFailure{llm.budget}
			}
			llm.inner = m
			for range llm.GenerateContent(ctx, req, true) {
				if mode == "consumer-stop" {
					break
				}
			}
			if learned := llm.inputHistory.last != nil; learned != (mode == "success") {
				t.Fatalf("learned=%v for %s", learned, mode)
			}
		})
	}
}
