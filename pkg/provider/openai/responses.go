package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// requiresResponsesAPI identifies OpenAI models whose tool calling is only
// supported through /v1/responses. Keep OpenAI-compatible providers on Chat
// Completions; only the first-party OpenAI factory enables this behavior.
func requiresResponsesAPI(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	return strings.HasPrefix(name, "gpt-6")
}

func toResponsesRequest(req *model.LLMRequest, modelName string, messages []openai.ChatCompletionMessage, tools []openai.Tool, maxOutputTokens int) openai.CreateResponseRequest {
	input := make([]any, 0, len(messages))
	instructions := ""

	for _, msg := range messages {
		switch msg.Role {
		case openai.ChatMessageRoleSystem, openai.ChatMessageRoleDeveloper:
			if instructions != "" {
				instructions += "\n"
			}
			instructions += msg.Content
		case openai.ChatMessageRoleTool:
			input = append(input, openai.ResponseFunctionCallOutput{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		default:
			content := any(msg.Content)
			hasContent := msg.Content != ""
			if len(msg.MultiContent) > 0 {
				parts := make([]any, 0, len(msg.MultiContent))
				for _, part := range msg.MultiContent {
					switch part.Type {
					case openai.ChatMessagePartTypeText:
						parts = append(parts, openai.ResponseInputText{Type: "input_text", Text: part.Text})
					case openai.ChatMessagePartTypeImageURL:
						if part.ImageURL != nil {
							parts = append(parts, openai.ResponseInputImage{Type: "input_image", ImageURL: part.ImageURL.URL, Detail: string(part.ImageURL.Detail)})
						}
					}
				}
				content = parts
				hasContent = len(parts) > 0
			}
			if hasContent {
				input = append(input, openai.ResponseInputMessage{Type: "message", Role: msg.Role, Content: content})
			}
			for _, call := range msg.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		}
	}

	responseTools := make([]openai.ResponseTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Function != nil {
			responseTools = append(responseTools, openai.NewResponseFunctionTool(*tool.Function))
		}
	}

	responseReq := openai.CreateResponseRequest{
		Model:           modelName,
		Instructions:    instructions,
		Input:           input,
		Tools:           responseTools,
		MaxOutputTokens: maxOutputTokens,
	}
	if req.Config != nil {
		responseReq.Temperature = req.Config.Temperature
		if req.Config.ResponseMIMEType == "application/json" && len(responseTools) == 0 {
			responseReq.Text = &openai.ResponseTextConfig{Format: &openai.ResponseTextFormat{Type: "json_object"}}
			if req.Config.ResponseJsonSchema != nil {
				responseReq.Text.Format = &openai.ResponseTextFormat{Type: "json_schema", Name: "structured_response", Schema: req.Config.ResponseJsonSchema, Strict: true}
			}
		}
	}
	return responseReq
}

func (p *Provider) generateResponsesStream(ctx context.Context, req openai.CreateResponseRequest, yield func(*model.LLMResponse, error) bool) {
	stream, err := p.client.CreateResponseStream(ctx, req)
	if err != nil {
		yield(nil, wrapOpenAIError(err))
		return
	}
	defer stream.Close()

	var finalResponse *openai.CreateResponseResponse
	var thinkingReported, toolsReported, outputSeen bool
	fail := func(err error) {
		if outputSeen {
			err = llmerror.NewInterruptedStream(err)
		}
		yield(nil, err)
	}
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if finalResponse == nil {
				fail(fmt.Errorf("OpenAI Responses stream ended without response.completed"))
				return
			}
			llmResp, convertErr := responseToLLMResponse(*finalResponse)
			if convertErr != nil {
				fail(convertErr)
				return
			}
			yield(llmResp, nil)
			return
		}
		if err != nil {
			fail(wrapOpenAIError(err))
			return
		}

		switch event.Type {
		case openai.ResponseStreamEventOutputTextDelta:
			outputSeen = outputSeen || event.Delta != ""
			if event.Delta != "" && !yield(&model.LLMResponse{
				Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: event.Delta}}},
				Partial: true,
			}, nil) {
				return
			}
		case openai.ResponseStreamEventFunctionArgumentsDelta:
			if event.Delta != "" && !toolsReported {
				toolsReported = true
				streaming.Report(ctx, streaming.Progress{Phase: streaming.PhaseToolArguments})
			}
		case openai.ResponseStreamEventReasoningSummaryTextDelta, openai.ResponseStreamEventReasoningTextDelta:
			if event.Delta != "" && !thinkingReported {
				thinkingReported = true
				streaming.Report(ctx, streaming.Progress{Phase: streaming.PhaseThinking})
			}
			outputSeen = outputSeen || event.Delta != ""
			if event.Delta != "" && !yield(&model.LLMResponse{
				Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: event.Delta, Thought: true}}},
				Partial: true,
			}, nil) {
				return
			}
		case openai.ResponseStreamEventCompleted:
			finalResponse = event.Response
		case openai.ResponseStreamEventFailed, openai.ResponseStreamEventIncomplete, openai.ResponseStreamEventError:
			message := event.Message
			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}
			if message == "" {
				message = string(event.Type)
			}
			fail(fmt.Errorf("OpenAI Responses stream failed: %s", message))
			return
		}
	}
}

func responseToLLMResponse(resp openai.CreateResponseResponse) (*model.LLMResponse, error) {
	if resp.Error != nil {
		return nil, fmt.Errorf("OpenAI response failed: %s", resp.Error.Message)
	}
	if resp.Status == openai.ResponseStatusIncomplete {
		reason := "unknown reason"
		if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason != "" {
			reason = resp.IncompleteDetails.Reason
		}
		return nil, fmt.Errorf("OpenAI response incomplete: %s", reason)
	}

	if resp.Status != openai.ResponseStatusCompleted {
		return nil, fmt.Errorf("OpenAI response did not complete successfully: %s", resp.Status)
	}

	parts := make([]*genai.Part, 0, len(resp.Output))
	for _, raw := range resp.Output {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal OpenAI response output: %w", err)
		}
		var item openai.ResponseOutputItem
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("decode OpenAI response output: %w", err)
		}
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Text != "" {
					parts = append(parts, &genai.Part{Text: content.Text})
				} else if content.Refusal != "" {
					parts = append(parts, &genai.Part{Text: content.Refusal})
				}
			}
		case "function_call":
			call, err := validatedFunctionCall(item.CallID, item.Name, item.Arguments)
			if err != nil {
				return nil, err
			}
			parts = append(parts, &genai.Part{FunctionCall: call})
		}
	}

	var usage *genai.GenerateContentResponseUsageMetadata
	if resp.Usage != nil {
		usage = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(resp.Usage.InputTokens),
			CandidatesTokenCount: int32(resp.Usage.OutputTokens),
			TotalTokenCount:      int32(resp.Usage.TotalTokens),
		}
		if resp.Usage.InputTokensDetails != nil {
			usage.CachedContentTokenCount = int32(resp.Usage.InputTokensDetails.CachedTokens)
		}
	}
	return &model.LLMResponse{
		Content:       &genai.Content{Role: "model", Parts: parts},
		UsageMetadata: usage,
	}, nil
}
