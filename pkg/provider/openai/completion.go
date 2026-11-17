package openai

import (
	"strings"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func completionFinishReason(finish openailib.FinishReason, refused bool) genai.FinishReason {
	if refused {
		return genai.FinishReasonSafety
	}
	switch finish {
	case openailib.FinishReasonStop, openailib.FinishReasonToolCalls:
		return genai.FinishReasonStop
	case openailib.FinishReasonLength:
		return genai.FinishReasonMaxTokens
	case openailib.FinishReasonContentFilter:
		return genai.FinishReasonSafety
	case "":
		return genai.FinishReasonUnspecified
	default:
		return genai.FinishReasonOther
	}
}

func completionError(finish openailib.FinishReason, text string, hasTools, refused bool) error {
	visible := strings.TrimSpace(text) != ""
	if !refused {
		switch finish {
		case openailib.FinishReasonStop:
			if visible || hasTools {
				return nil
			}
		case openailib.FinishReasonToolCalls:
			if hasTools {
				return nil
			}
		}
	}
	return &llmerror.EmptyCompletionError{
		Provider: "OpenAI", FinishReason: completionFinishReason(finish, refused),
		HasVisibleOutput: visible, HasToolCalls: hasTools || finish == openailib.FinishReasonToolCalls, Refused: refused,
	}
}

func chatCompletionError(resp openailib.ChatCompletionResponse) error {
	if len(resp.Choices) == 0 {
		return completionError("", "", false, false)
	}
	choice := resp.Choices[0]
	message := choice.Message
	if err := completionError(choice.FinishReason, message.Content, len(message.ToolCalls) > 0 || message.FunctionCall != nil, message.Refusal != ""); err != nil {
		return err
	}
	// A legacy function_call is not an empty answer; it is unsupported here and
	// must not accidentally qualify for empty-answer correction.
	if message.FunctionCall != nil {
		return &llmerror.EmptyCompletionError{Provider: "OpenAI", FinishReason: genai.FinishReasonOther, HasToolCalls: true}
	}
	for _, tc := range message.ToolCalls {
		if _, err := validatedFunctionCall(tc.ID, tc.Function.Name, tc.Function.Arguments); err != nil {
			return err
		}
	}
	return nil
}

// Failed tool batches must never expose executable calls, even to a consumer
// that examines the response before checking its paired error.
func discardCompletionTools(response *model.LLMResponse) {
	if response.Content == nil {
		return
	}
	parts := response.Content.Parts[:0]
	for _, part := range response.Content.Parts {
		if part.FunctionCall == nil {
			parts = append(parts, part)
		}
	}
	response.Content.Parts = parts
}
