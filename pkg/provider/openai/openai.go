package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Provider implements model.LLM for OpenAI.
type Provider struct {
	client              *openai.Client
	model               string
	supportsJSONMode    bool
	maxCompletionTokens int // If > 0, sets max_completion_tokens in the request

	// thinkingMode is set to 1 (atomically) the first time we observe
	// reasoning_content in a model response. Once set it never resets.
	// Used by patchMissingReasoningContent to know whether the model
	// requires reasoning_content on assistant messages with tool_calls,
	// even when the conversation history has no reasoning yet (first-turn).
	thinkingMode atomic.Bool
}

// NewProvider creates a new OpenAI provider.
func NewProvider(client *openai.Client, modelName string, supportsJSONMode bool) *Provider {
	return &Provider{
		client:           client,
		model:            modelName,
		supportsJSONMode: supportsJSONMode,
	}
}

// NewProviderWithMaxTokens creates a new OpenAI provider with explicit max_completion_tokens.
// This is needed for providers like OpenRouter where we fetch the limit from API metadata.
func NewProviderWithMaxTokens(client *openai.Client, modelName string, supportsJSONMode bool, maxCompletionTokens int) *Provider {
	return &Provider{
		client:              client,
		model:               modelName,
		supportsJSONMode:    supportsJSONMode,
		maxCompletionTokens: maxCompletionTokens,
	}
}

// GenerateContent implements model.LLM.
func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	req = common.CanonicalizeRequestTools(req)
	return func(yield func(*model.LLMResponse, error) bool) {
		messages := p.toOpenAIMessages(req)

		// Extract tools if present
		var tools []openai.Tool
		if req.Config != nil && len(req.Config.Tools) > 0 {
			for _, t := range req.Config.Tools {
				for _, fd := range t.FunctionDeclarations {
					params := sanitizeToolParams(fd.ParametersJsonSchema)
					tools = append(tools, openai.Tool{
						Type: openai.ToolTypeFunction,
						Function: &openai.FunctionDefinition{
							Name:        fd.Name,
							Description: fd.Description,
							Parameters:  params,
						},
					})
				}
			}
		}

		openAIReq := openai.ChatCompletionRequest{
			Model:    p.model,
			Messages: messages,
			Tools:    tools,
		}

		// StreamOptions is only valid when streaming is enabled.
		// Some providers (e.g., OpenAI, OpenRouter) reject it otherwise.
		if streaming {
			openAIReq.StreamOptions = &openai.StreamOptions{
				IncludeUsage: true,
			}
		}

		// Apply max_completion_tokens if configured
		// This is critical for OpenRouter to avoid their low defaults
		if p.maxCompletionTokens > 0 {
			openAIReq.MaxCompletionTokens = p.maxCompletionTokens
		}

		// Per-request overrides from Config (e.g., title generation with
		// MaxOutputTokens: 100). These take precedence over provider-level defaults.
		if req.Config != nil {
			if req.Config.MaxOutputTokens > 0 {
				openAIReq.MaxCompletionTokens = int(req.Config.MaxOutputTokens)
			}
			if req.Config.Temperature != nil {
				openAIReq.Temperature = *req.Config.Temperature
			}
		}

		if req.Config != nil && len(req.Config.StopSequences) > 0 {
			openAIReq.Stop = req.Config.StopSequences
		}

		// Check for JSON mode request
		// Note: Some providers (Groq, Google) do not support JSON mode combined with tools.
		// If tools are present, we prioritize tools and disable JSON mode enforcement.
		if p.supportsJSONMode && req.Config != nil && req.Config.ResponseMIMEType == "application/json" && len(tools) == 0 {
			openAIReq.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			}
		}

		if streaming {
			stream, err := p.client.CreateChatCompletionStream(ctx, openAIReq)
			if err != nil {
				yield(nil, wrapOpenAIError(err))
				return
			}
			defer stream.Close()

			// Accumulate tool calls for streaming
			toolCallAccumulator := make(map[int]*openai.ToolCall)

			// Accumulate text for streaming — each chunk is yielded with
			// Partial=true for live display, and at stream end we emit one
			// aggregated non-partial response for session persistence.
			var textAccum strings.Builder

			// Accumulate reasoning_content (DeepSeek thinking mode) for streaming.
			// Partial reasoning chunks are yielded with Thought=true for live
			// thinking indicator display. The aggregated reasoning is included
			// in the final response for session persistence.
			var reasoningAccum strings.Builder

			// Track whether the LLM sent a proper finish_reason. If the stream
			// ends (io.EOF) without any finish_reason, the response was truncated
			// — typically by a gateway timeout or connection drop.
			finishReasonSeen := false

			// Capture token usage from the final stream chunk (sent when
			// StreamOptions.IncludeUsage is true).
			var streamUsage *genai.GenerateContentResponseUsageMetadata

			for {
				resp, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					// Stream ended — build final aggregated parts.
					var finalParts []*genai.Part
					if reasoningAccum.Len() > 0 {
						finalParts = append(finalParts, &genai.Part{Text: reasoningAccum.String(), Thought: true})
					}
					if textAccum.Len() > 0 {
						finalParts = append(finalParts, &genai.Part{Text: textAccum.String()})
					}

					if !finishReasonSeen && textAccum.Len() > 0 {
						// Emit whatever we accumulated (so it gets persisted)
						// then return an error so the caller knows the response is incomplete.
						yield(&model.LLMResponse{
							Content: &genai.Content{
								Role:  "model",
								Parts: finalParts,
							},
							UsageMetadata: streamUsage,
						}, nil)
						yield(nil, fmt.Errorf("LLM stream ended without a finish_reason — the response was likely truncated by a gateway timeout or connection drop"))
						return
					}
					// Normal completion: emit aggregated response at stream end
					if len(finalParts) > 0 {
						yield(&model.LLMResponse{
							Content: &genai.Content{
								Role:  "model",
								Parts: finalParts,
							},
							UsageMetadata: streamUsage,
						}, nil)
					}
					return
				}
				if err != nil {
					yield(nil, wrapOpenAIError(err))
					return
				}

				// Capture token usage from the final chunk (OpenAI sends usage
				// on the last chunk when StreamOptions.IncludeUsage is true).
				if resp.Usage != nil {
					streamUsage = &genai.GenerateContentResponseUsageMetadata{
						PromptTokenCount:     int32(resp.Usage.PromptTokens),
						CandidatesTokenCount: int32(resp.Usage.CompletionTokens),
						TotalTokenCount:      int32(resp.Usage.TotalTokens),
					}
				}

				// Handle tool call deltas
				if len(resp.Choices) > 0 {
					choice := resp.Choices[0]

					// Track finish_reason — any non-empty value means the LLM
					// completed its response normally (not a premature disconnect).
					if choice.FinishReason != "" {
						finishReasonSeen = true
					}

					// Accumulate reasoning_content (DeepSeek thinking mode).
					// Yield each chunk as a partial Thought part for live display.
					if choice.Delta.ReasoningContent != "" {
						p.thinkingMode.Store(true)
						reasoningAccum.WriteString(choice.Delta.ReasoningContent)
						if !yield(&model.LLMResponse{
							Content: &genai.Content{
								Role:  "model",
								Parts: []*genai.Part{{Text: choice.Delta.ReasoningContent, Thought: true}},
							},
							Partial: true,
						}, nil) {
							return
						}
					}

					// Accumulate tool calls
					for _, tc := range choice.Delta.ToolCalls {
						if tc.Index != nil {
							idx := *tc.Index
							if _, exists := toolCallAccumulator[idx]; !exists {
								toolCallAccumulator[idx] = &openai.ToolCall{
									Index: tc.Index,
									Function: openai.FunctionCall{
										Name:      tc.Function.Name,
										Arguments: tc.Function.Arguments,
									},
									ID:   tc.ID,
									Type: tc.Type,
								}
							} else {
								// Update existing
								if tc.Function.Name != "" {
									toolCallAccumulator[idx].Function.Name += tc.Function.Name
								}
								if tc.Function.Arguments != "" {
									toolCallAccumulator[idx].Function.Arguments += tc.Function.Arguments
								}
								if tc.ID != "" {
									toolCallAccumulator[idx].ID = tc.ID
								}
							}
						}
					}

					// Check for finish reason
					if choice.FinishReason == openai.FinishReasonToolCalls ||
						(choice.FinishReason == openai.FinishReasonStop && len(toolCallAccumulator) > 0) {

						// Emit aggregated text before tool calls (model may
						// produce a preamble like "Let me check that for you"
						// before invoking tools). Include reasoning if present.
						if textAccum.Len() > 0 || reasoningAccum.Len() > 0 {
							var preambleParts []*genai.Part
							if reasoningAccum.Len() > 0 {
								preambleParts = append(preambleParts, &genai.Part{Text: reasoningAccum.String(), Thought: true})
							}
							if textAccum.Len() > 0 {
								preambleParts = append(preambleParts, &genai.Part{Text: textAccum.String()})
							}
							if !yield(&model.LLMResponse{
								Content: &genai.Content{
									Role:  "model",
									Parts: preambleParts,
								},
							}, nil) {
								return
							}
							textAccum.Reset()
							reasoningAccum.Reset()
						}

						// Emit all accumulated tool calls
						var parts []*genai.Part

						// Sort by index to maintain deterministic order
						indices := make([]int, 0, len(toolCallAccumulator))
						for idx := range toolCallAccumulator {
							indices = append(indices, idx)
						}
						sort.Ints(indices)

						for _, idx := range indices {
							tc := toolCallAccumulator[idx]
							var args map[string]any
							if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
								// Try to recover or just use empty
								args = make(map[string]any)
							}

							parts = append(parts, &genai.Part{
								FunctionCall: &genai.FunctionCall{
									Name: tc.Function.Name,
									Args: args,
									ID:   tc.ID,
								},
							})
						}

						if len(parts) > 0 {
							yield(&model.LLMResponse{
								Content: &genai.Content{
									Role:  "model",
									Parts: parts,
								},
								UsageMetadata: streamUsage,
							}, nil)
						}
						return
					}
				}

				llmResp := p.toLLMResponseStream(resp)
				// Only yield if there's content (ignore empty tool call deltas from toLLMResponseStream)
				if llmResp.Content != nil && len(llmResp.Content.Parts) > 0 {
					// Accumulate text and mark as partial for live display;
					// the aggregated non-partial response is emitted at stream end.
					if text := llmResp.Content.Parts[0].Text; text != "" {
						textAccum.WriteString(text)
						llmResp.Partial = true
					}
					if !yield(llmResp, nil) {
						return
					}
				}
			}
		} else {
			resp, err := p.client.CreateChatCompletion(ctx, openAIReq)
			if err != nil {
				yield(nil, wrapOpenAIError(err))
				return
			}
			llmResp := p.toLLMResponse(resp)
			yield(llmResp, nil)
		}
	}
}

// Name implements model.LLM.
func (p *Provider) Name() string {
	return p.model
}

func (p *Provider) toOpenAIMessages(req *model.LLMRequest) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage

	// System instruction
	if req.Config != nil && req.Config.SystemInstruction != nil {
		var sb strings.Builder
		for _, part := range req.Config.SystemInstruction.Parts {
			sb.WriteString(part.Text)
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: sb.String(),
		})
	}

	// Track tool call IDs to map responses back to calls
	// Map from function name to tool call ID
	lastToolCallIDs := make(map[string]string)

	for _, c := range req.Contents {
		role := openai.ChatMessageRoleUser
		if c.Role == "model" {
			role = openai.ChatMessageRoleAssistant
		} else if c.Role == "function" {
			role = openai.ChatMessageRoleTool
		}

		// Check if it contains FunctionResponse, override role to Tool
		// This is necessary because ADK might label it as 'user'
		for _, part := range c.Parts {
			if part.FunctionResponse != nil {
				role = openai.ChatMessageRoleTool
				break
			}
		}

		if role == openai.ChatMessageRoleTool {
			// Handle tool outputs
			for _, part := range c.Parts {
				if part.FunctionResponse != nil {
					var content string
					// Marshal response to JSON string for better LLM comprehension
					contentBytes, err := json.Marshal(part.FunctionResponse.Response)
					if err != nil {
						// Fallback to string representation if marshaling fails
						content = fmt.Sprintf("%v", part.FunctionResponse.Response)
					} else {
						content = string(contentBytes)
					}

					// Response is map[string]any
					m := part.FunctionResponse.Response
					if res, ok := m["result"]; ok {
						// If there's a specific "result" key, prefer that, but still JSON encode it if it's complex
						if resStr, ok := res.(string); ok {
							content = resStr
						} else {
							resBytes, _ := json.Marshal(res)
							content = string(resBytes)
						}
					}

					// Use real ID if available
					id := part.FunctionResponse.ID
					if id == "" {
						// Fallback: Try to find matching ID from previous calls
						if lastID, ok := lastToolCallIDs[part.FunctionResponse.Name]; ok {
							id = lastID
						} else {
							id = "call_" + part.FunctionResponse.Name
						}
					}

					messages = append(messages, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    content,
						ToolCallID: id,
					})
				}
			}
		} else {
			// User or Assistant
			var sb strings.Builder
			var reasoningSB strings.Builder
			var toolCalls []openai.ToolCall
			var multiParts []openai.ChatMessagePart
			hasInlineData := false

			for _, part := range c.Parts {
				if part.Text != "" {
					// Separate reasoning/thought content from regular content.
					// DeepSeek requires reasoning_content to be passed back as a
					// separate field on assistant messages.
					if part.Thought && role == openai.ChatMessageRoleAssistant {
						reasoningSB.WriteString(part.Text)
					} else {
						sb.WriteString(part.Text)
					}
				}
				if part.InlineData != nil && role == openai.ChatMessageRoleUser {
					hasInlineData = true
					mime := part.InlineData.MIMEType
					if strings.HasPrefix(mime, "image/") || mime == "application/pdf" {
						// Images and PDFs: send as image_url data URL (GPT-4o supports both)
						dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(part.InlineData.Data)
						multiParts = append(multiParts, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeImageURL,
							ImageURL: &openai.ChatMessageImageURL{
								URL:    dataURL,
								Detail: openai.ImageURLDetailAuto,
							},
						})
					} else {
						// Text-based files (txt, md, json, xml, yaml, html, etc.):
						// Decode to string and include as a text part with filename context.
						// OpenAI doesn't have a native "file" content type for chat.
						filename := part.InlineData.DisplayName
						if filename == "" {
							filename = "attachment"
						}
						textContent := string(part.InlineData.Data)
						multiParts = append(multiParts, openai.ChatMessagePart{
							Type: openai.ChatMessagePartTypeText,
							Text: fmt.Sprintf("[Attached file: %s]\n%s", filename, textContent),
						})
					}
				}
				if part.FunctionCall != nil {
					// Marshal args to JSON string; use "{}" for nil/empty args
					// because some providers (e.g. Anthropic via Bifrost) reject
					// "null" as toolUse input.
					argsStr := "{}"
					if part.FunctionCall.Args != nil {
						if b, err := json.Marshal(part.FunctionCall.Args); err == nil && string(b) != "null" {
							argsStr = string(b)
						}
					}

					// Use real ID if available
					id := part.FunctionCall.ID
					if id == "" {
						id = "call_" + part.FunctionCall.Name
					}

					// Store ID for matching response
					lastToolCallIDs[part.FunctionCall.Name] = id

					toolCalls = append(toolCalls, openai.ToolCall{
						ID:   id,
						Type: openai.ToolTypeFunction,
						Function: openai.FunctionCall{
							Name:      part.FunctionCall.Name,
							Arguments: argsStr,
						},
					})
				}
			}

			msg := openai.ChatCompletionMessage{
				Role: role,
			}
			if hasInlineData {
				// Use MultiContent for messages with inline data (images, files)
				if sb.Len() > 0 {
					multiParts = append([]openai.ChatMessagePart{{
						Type: openai.ChatMessagePartTypeText,
						Text: sb.String(),
					}}, multiParts...)
				}
				msg.MultiContent = multiParts
			} else {
				msg.Content = sb.String()
			}
			if reasoningSB.Len() > 0 {
				msg.ReasoningContent = reasoningSB.String()
			}
			if len(toolCalls) > 0 {
				msg.ToolCalls = toolCalls
			}
			messages = append(messages, msg)
		}
	}

	// Post-process: merge consecutive messages with the same role.
	// OpenAI requires strict user/assistant alternation. Consecutive user
	// messages (e.g. from failed requests where no model response was stored)
	// cause HTTP 400 errors. Tool messages are left alone since each carries
	// its own tool_call_id.
	messages = mergeConsecutiveSameRole(messages)

	// Post-process: patch orphaned tool_calls. If an assistant message has
	// tool_calls but there are no corresponding tool-role messages with
	// matching tool_call_id, inject synthetic error responses. This prevents
	// 400 errors when a tool call hung or crashed mid-stream without
	// persisting a result to the session history.
	messages = patchOrphanedToolCalls(messages)

	// Post-process: fix thinking-mode history. DeepSeek requires
	// reasoning_content on every assistant message that contains tool_calls.
	// If the reasoning was lost (stream interruption, pre-fix sessions,
	// model skipped thinking, etc.) inject a minimal placeholder so the
	// API doesn't reject the request with a 400 error.
	messages = patchMissingReasoningContent(messages, p.thinkingMode.Load())
	// Post-process: ensure conversation doesn't end with a tool message.
	// Some OpenAI-compatible providers (e.g. kimi-k2.5) reject requests
	// where the final message has role "tool". The standard OpenAI flow
	// expects the model to process tool results and continue, but these
	// providers require a user message after tool responses. We append
	// a minimal user prompt to trigger the model to process the results.
	// NOTE: Only apply for kimi models. Mistral (and most other providers)
	// reject a user message between tool results and the next assistant turn.
	if strings.Contains(p.model, "kimi") {
		messages = ensureNotEndingWithTool(messages)
	}

	return messages
}

// mergeConsecutiveSameRole collapses adjacent messages that share the same role
// into a single message. This handles two cases:
//
//  1. Consecutive text-only user/assistant messages are joined with "\n".
//  2. An assistant text message followed by an assistant tool_calls message
//     are merged into one message carrying both content and tool_calls.
//     During streaming the provider emits text chunks and tool calls as
//     separate events, so they end up as separate Contents in session history.
//     Many OpenAI-compatible providers (e.g. kimi, Mistral) reject two
//     consecutive assistant messages — the OpenAI spec allows a single
//     assistant message to carry both content and tool_calls simultaneously.
//
// Tool messages are never merged because each carries a distinct tool_call_id.
func mergeConsecutiveSameRole(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	if len(messages) <= 1 {
		return messages
	}

	merged := make([]openai.ChatCompletionMessage, 0, len(messages))
	merged = append(merged, messages[0])

	for i := 1; i < len(messages); i++ {
		prev := &merged[len(merged)-1]
		cur := messages[i]

		sameRole := prev.Role == cur.Role
		bothAssistant := prev.Role == openai.ChatMessageRoleAssistant && cur.Role == openai.ChatMessageRoleAssistant

		// Case 1: merge consecutive text-only user or assistant messages.
		mergeable := cur.Role == openai.ChatMessageRoleUser || cur.Role == openai.ChatMessageRoleAssistant
		noToolCalls := len(prev.ToolCalls) == 0 && len(cur.ToolCalls) == 0
		noToolID := prev.ToolCallID == "" && cur.ToolCallID == ""

		if sameRole && mergeable && noToolCalls && noToolID {
			if prev.Content != "" && cur.Content != "" {
				prev.Content = prev.Content + "\n" + cur.Content
			} else if cur.Content != "" {
				prev.Content = cur.Content
			}
			mergeReasoningContent(prev, cur)
			continue
		}

		// Case 2: assistant text message followed by assistant tool_calls message.
		// Streaming produces these as separate events — merge them so providers
		// that enforce strict message alternation don't reject the request.
		if bothAssistant && len(prev.ToolCalls) == 0 && len(cur.ToolCalls) > 0 && prev.ToolCallID == "" {
			// Absorb cur's tool calls (and any text) into prev.
			if cur.Content != "" {
				if prev.Content != "" {
					prev.Content = prev.Content + "\n" + cur.Content
				} else {
					prev.Content = cur.Content
				}
			}
			mergeReasoningContent(prev, cur)
			prev.ToolCalls = cur.ToolCalls
			continue
		}

		// Case 3: assistant tool_calls message followed by assistant text message
		// (less common, but handle symmetrically).
		if bothAssistant && len(prev.ToolCalls) > 0 && len(cur.ToolCalls) == 0 && cur.ToolCallID == "" {
			if cur.Content != "" {
				if prev.Content != "" {
					prev.Content = prev.Content + "\n" + cur.Content
				} else {
					prev.Content = cur.Content
				}
			}
			mergeReasoningContent(prev, cur)
			continue
		}

		merged = append(merged, cur)
	}

	return merged
}

// mergeReasoningContent absorbs cur's ReasoningContent into prev.
// Used when merging consecutive assistant messages to preserve DeepSeek
// reasoning_content across message boundaries.
func mergeReasoningContent(prev *openai.ChatCompletionMessage, cur openai.ChatCompletionMessage) {
	if cur.ReasoningContent == "" {
		return
	}
	if prev.ReasoningContent != "" {
		prev.ReasoningContent = prev.ReasoningContent + "\n" + cur.ReasoningContent
	} else {
		prev.ReasoningContent = cur.ReasoningContent
	}
}

// patchOrphanedToolCalls scans the message history for assistant messages
// with tool_calls that have no corresponding tool-role response message.
// For each orphan, a synthetic error tool response is injected so the API
// doesn't reject the request with a 400 error.
//
// This handles the scenario where a tool call hung, crashed, or timed out
// without persisting a result to the session history — leaving a dangling
// tool_call that breaks the conversation permanently.
func patchOrphanedToolCalls(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	// Collect all tool_call_ids that have responses.
	answeredIDs := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == openai.ChatMessageRoleTool && msg.ToolCallID != "" {
			answeredIDs[msg.ToolCallID] = true
		}
	}

	// Scan assistant messages for unanswered tool_calls.
	var result []openai.ChatCompletionMessage
	for _, msg := range messages {
		result = append(result, msg)

		if msg.Role != openai.ChatMessageRoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}

		// Find orphaned tool_call IDs in this assistant message.
		var orphans []openai.ToolCall
		for _, tc := range msg.ToolCalls {
			if !answeredIDs[tc.ID] {
				orphans = append(orphans, tc)
			}
		}

		if len(orphans) == 0 {
			continue
		}

		// Inject synthetic error responses for each orphan.
		for _, tc := range orphans {
			result = append(result, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: tc.ID,
				Content:    `{"error": "Tool call was not executed (timed out or crashed)."}`,
			})
			// Mark as answered so we don't double-patch.
			answeredIDs[tc.ID] = true
		}
	}

	return result
}

// ensureNotEndingWithTool appends a synthetic user message when the
// conversation ends with a tool-role message. Some OpenAI-compatible providers
// (notably kimi-k2.5) return HTTP 400 when the last message is a tool response
// — they require a user message to follow. Standard OpenAI allows the model to
// pick up from a tool response directly, but this workaround is harmless for
// providers that don't need it: the model simply sees an extra "continue" nudge.
func ensureNotEndingWithTool(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	if len(messages) == 0 {
		return messages
	}
	last := messages[len(messages)-1]
	if last.Role == openai.ChatMessageRoleTool {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: "Above are the tool results. Process them and continue.",
		})
	}
	return messages
}

// patchMissingReasoningContent ensures every assistant message with tool_calls
// in a thinking-mode conversation has reasoning_content. DeepSeek requires
// reasoning_content on assistant messages that contain tool_calls. If reasoning
// was lost (stream interruption, pre-fix sessions, model skipped thinking,
// preamble event not persisted, filter bug, etc.) inject a minimal placeholder.
//
// providerThinkingMode is true when the provider has already observed
// reasoning_content in any response during this session — this handles the
// first-turn edge case where the conversation history has no reasoning yet
// but the model is known to be a thinking model.
//
// As a fallback, the function also scans conversation history for any
// existing reasoning_content (covers cases where the provider instance
// was recreated, e.g. after a daemon restart mid-session).
func patchMissingReasoningContent(messages []openai.ChatCompletionMessage, providerThinkingMode bool) []openai.ChatCompletionMessage {
	isThinking := providerThinkingMode
	if !isThinking {
		// Fallback: scan conversation history.
		for _, msg := range messages {
			if msg.ReasoningContent != "" {
				isThinking = true
				break
			}
		}
	}
	if !isThinking {
		return messages
	}

	patched := 0
	for i := range messages {
		msg := &messages[i]
		if msg.Role != openai.ChatMessageRoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		if msg.ReasoningContent != "" {
			continue // already has reasoning
		}
		// Inject a minimal placeholder so DeepSeek doesn't reject the request.
		msg.ReasoningContent = "."
		patched++
		slog.Debug("patched missing reasoning_content on assistant tool_calls message",
			"component", "openai-provider", "content_preview", truncateForLog(msg.Content, 80))
	}

	if patched > 0 {
		slog.Debug("patched thinking-mode history",
			"component", "openai-provider", "patched_messages", patched)
	}
	return messages
}

// truncateForLog shortens a string for debug log output.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (p *Provider) toLLMResponse(resp openai.ChatCompletionResponse) *model.LLMResponse {
	if len(resp.Choices) == 0 {
		return &model.LLMResponse{}
	}
	choice := resp.Choices[0]

	var parts []*genai.Part

	// Capture reasoning_content (DeepSeek thinking mode) as a Thought part.
	// Must appear before regular content so session history preserves ordering.
	if choice.Message.ReasoningContent != "" {
		p.thinkingMode.Store(true)
		parts = append(parts, &genai.Part{Text: choice.Message.ReasoningContent, Thought: true})
	}

	if choice.Message.Content != "" {
		parts = append(parts, &genai.Part{Text: choice.Message.Content})
	}

	for _, tc := range choice.Message.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = make(map[string]any)
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				Name: tc.Function.Name,
				Args: args,
				ID:   tc.ID,
			},
		})
	}

	return &model.LLMResponse{
		Content: &genai.Content{
			Role:  "model",
			Parts: parts,
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(resp.Usage.PromptTokens),
			CandidatesTokenCount: int32(resp.Usage.CompletionTokens),
			TotalTokenCount:      int32(resp.Usage.TotalTokens),
		},
	}
}

func (p *Provider) toLLMResponseStream(resp openai.ChatCompletionStreamResponse) *model.LLMResponse {
	if len(resp.Choices) == 0 {
		return &model.LLMResponse{}
	}
	choice := resp.Choices[0]

	var parts []*genai.Part
	if choice.Delta.Content != "" {
		parts = append(parts, &genai.Part{Text: choice.Delta.Content})
	}

	// Note: We do NOT handle tool calls here anymore, as they are accumulated in GenerateContent

	return &model.LLMResponse{
		Content: &genai.Content{
			Role:  "model",
			Parts: parts,
		},
	}
}

// wrapOpenAIError converts go-openai library errors into structured LLMError
// types that support retry classification. The library returns *APIError for
// HTTP-level failures and *RequestError for transport-level failures.
func wrapOpenAIError(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		// Build a detailed message that includes Code and Type when available,
		// since apiErr.Message alone may be empty for non-standard endpoints.
		msg := apiErr.Message
		if msg == "" {
			// Reconstruct from available fields for better debugging
			var parts []string
			if apiErr.Type != "" {
				parts = append(parts, apiErr.Type)
			}
			if code, ok := apiErr.Code.(string); ok && code != "" {
				parts = append(parts, code)
			}
			if len(parts) > 0 {
				msg = strings.Join(parts, ": ")
			}
		}
		return llmerror.NewLLMError("openai", apiErr.HTTPStatusCode, msg, "")
	}

	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return llmerror.NewLLMError("openai", reqErr.HTTPStatusCode, reqErr.Error(), string(reqErr.Body))
	}

	// Unknown error type — return as-is
	return err
}

// sanitizeToolParams ensures tool parameter schemas are compatible with
// OpenAI-compatible endpoints. Some servers (e.g. LM Studio) crash when
// a parameter schema has "type": "object" without a "properties" field.
// This adds an empty "properties": {} in that case.
//
// The schema may arrive as a map[string]any or as a typed struct (e.g.
// *jsonschema.Schema from the ADK). For typed structs, we round-trip
// through JSON to get a mutable map.
func sanitizeToolParams(params any) any {
	if params == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}

	m, ok := params.(map[string]any)
	if !ok {
		// Typed struct (e.g. *jsonschema.Schema) — round-trip via JSON
		data, err := json.Marshal(params)
		if err != nil {
			return params
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return params
		}
	}

	if m["type"] == "object" {
		if _, hasProps := m["properties"]; !hasProps {
			m["properties"] = map[string]any{}
		}
	}
	return m
}
