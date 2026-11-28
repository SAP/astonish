package api

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotWorkAssessmentReasoningUsage(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty-reasoning", true: "format-retry"}[retry], func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			usageFrame := workStreamEvent{response: &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ThoughtsTokenCount: 4096}}}
			attempts := [][]workStreamEvent{{usageFrame, usageFrame}}
			if retry {
				attempts[0] = append(attempts[0], workStreamText("not json", false, 10, 0))
				attempts = append(attempts, []workStreamEvent{workStreamText(`{"value":"ok"}`, false, 10, 0), usageFrame, usageFrame})
			}
			llm := &workStreamModel{t: t, attempts: attempts}
			var out struct {
				Value string `json:"value"`
			}
			usage := store.BotUsage{InputTokens: 3, OutputTokens: 7}
			err := botWorkJSON(ctx, req, llm, botWorkPreparePrompt, map[string]string{"source": "original"}, &out, &usage)
			if (err == nil) != retry {
				t.Fatalf("retry=%v err=%v", retry, err)
			}
			calls := int64(len(attempts))
			if usage.InputTokens != 3+10*calls || usage.OutputTokens != 7+4096*calls || int64(llm.calls) != calls {
				t.Fatalf("calls=%d usage=%+v", llm.calls, usage)
			}
		})
	}
}

func TestBotAgentCheckReasoningUsage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "error"}[fail], func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			saved := &agentExecutionWorkStore{state: store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})}
			service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved}, usage: store.BotUsage{InputTokens: 3, OutputTokens: 7}}
			calls := 0
			service.llm = &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				calls++
				if q.Config.MaxOutputTokens != 2048 {
					t.Fatalf("changed check capacity: %d", q.Config.MaxOutputTokens)
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					r := &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, ThoughtsTokenCount: 4096}}
					if fail {
						yield(r, errors.New("failed"))
						return
					}
					if yield(r, nil) {
						yield(r, nil)
					}
				}
			}}
			q := bot.BotWorkCheckRequest{CallID: "reasoning-check", Question: "Check draft"}
			receipt, err := service.Check(ctx, q)
			if err != nil || receipt.Status != store.BotWorkCheckFailed {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			if service.usage.InputTokens != 13 || service.usage.OutputTokens != 4103 {
				t.Fatalf("usage=%+v", service.usage)
			}
			if _, err := service.Check(ctx, q); err != nil || calls != 1 || service.usage.OutputTokens != 4103 {
				t.Fatalf("replay calls=%d usage=%+v err=%v", calls, service.usage, err)
			}
		})
	}
}
