package launcher

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type reasoningDispatchProbe struct {
	progressDispatchProbe
	usage store.BotUsage
}

func (p *reasoningDispatchProbe) Settle(ctx context.Context, id uuid.UUID, outcome store.BotProgressDispatchOutcome, usage store.BotUsage, evidence string) error {
	p.usage = usage
	return p.progressDispatchProbe.Settle(ctx, id, outcome, usage, evidence)
}

type reasoningDispatchModel struct {
	usage *genai.GenerateContentResponseUsageMetadata
}

func (reasoningDispatchModel) Name() string { return "reasoning-test" }
func (m reasoningDispatchModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, u := range []*genai.GenerateContentResponseUsageMetadata{m.usage, m.usage, {}} {
			if !yield(&model.LLMResponse{UsageMetadata: u}, nil) {
				return
			}
		}
		yield(nil, errors.New("provider failure after usage"))
	}
}
func TestBotProgressDispatchReasoningUsage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		usage         genai.GenerateContentResponseUsageMetadata
		input, output int64
	}{
		{"reasoning_only", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ThoughtsTokenCount: 4096, TotalTokenCount: 4106}, 10, 4096},
		{"mixed_exact_once", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 96, ThoughtsTokenCount: 4000, TotalTokenCount: 4106}, 10, 4096},
		{"total_fallback", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, TotalTokenCount: 4106}, 10, 4096},
		{"negative_metadata", genai.GenerateContentResponseUsageMetadata{PromptTokenCount: -10, CandidatesTokenCount: -96, ThoughtsTokenCount: -4000, TotalTokenCount: -4106}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &reasoningDispatchProbe{}
			b := progressBindingForTest(&p.progressDispatchProbe)
			b.owner = bot.WithProgressDispatch(b.owner, p)
			llm := &botProgressLLM{LLM: reasoningDispatchModel{usage: &tc.usage}, binding: b}
			for range llm.GenerateContent(b.owner, &model.LLMRequest{}, true) {
			}
			if p.began != 1 || p.settled != 0 {
				t.Fatalf("premature settlement: %+v", p)
			}
			for range 2 {
				if err := b.progress.boundary(b.owner); err != nil {
					t.Fatal(err)
				}
			}
			if p.settled != 1 || p.usage.InputTokens != tc.input || p.usage.OutputTokens != tc.output {
				t.Fatalf("settled=%d usage=%+v want input=%d output=%d", p.settled, p.usage, tc.input, tc.output)
			}
		})
	}
}
