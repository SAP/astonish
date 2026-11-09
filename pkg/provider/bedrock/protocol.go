package bedrock

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strings"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/provider/streaming"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Request represents the Bedrock request payload for Anthropic models.
type Request struct {
	AnthropicVersion string      `json:"anthropic_version"`
	MaxTokens        int         `json:"max_tokens"`
	Messages         []Message   `json:"messages"`
	System           string      `json:"system,omitempty"`
	Temperature      *float64    `json:"temperature,omitempty"`
	Tools            []Tool      `json:"tools,omitempty"`
	ToolChoice       *ToolChoice `json:"tool_choice,omitempty"`
}

// ToolChoiceType is the native Anthropic tool-selection mode.
type ToolChoiceType string

const ToolChoiceAuto ToolChoiceType = "auto"

// ToolChoice permits a final answer without requiring a tool invocation.
type ToolChoice struct {
	Type                   ToolChoiceType `json:"type"`
	DisableParallelToolUse bool           `json:"disable_parallel_tool_use"`
}

// Message represents a message in the Bedrock conversation.
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // Can be string or array of content blocks
}

// Tool represents a tool definition in Bedrock.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ContentBlock represents a block of content in a Bedrock message.
type ContentBlock struct {
	Type      string                  `json:"type"`
	Text      string                  `json:"text,omitempty"`
	ID        string                  `json:"id,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Input     *map[string]interface{} `json:"input,omitempty"` // Pointer so we can control when it's included
	ToolUseID string                  `json:"tool_use_id,omitempty"`
	Content   string                  `json:"content,omitempty"`
	Source    *ImageSource            `json:"source,omitempty"` // For image/document attachments
}

// ImageSource represents the source data for image/document content blocks in Bedrock.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/png", "application/pdf"
	Data      string `json:"data"`       // base64-encoded data
}

// Response represents the Bedrock response payload.
type Usage struct {
	InputTokens                  int  `json:"input_tokens"`
	OutputTokens                 int  `json:"output_tokens"`
	CacheCreationInputTokens     *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens         *int `json:"cache_read_input_tokens"`
	BedrockCacheWriteInputTokens *int `json:"cacheWriteInputTokens"`
	BedrockCacheReadInputTokens  *int `json:"cacheReadInputTokens"`
}

type Response struct {
	Content    []ContentBlock `json:"content"`
	Usage      Usage          `json:"usage"`
	StopReason string         `json:"stop_reason"`
}

// ConvertRequest converts an ADK LLMRequest to a Bedrock Request.
// maxTokens can be 0 to use the default (8192)
func ConvertRequest(req *model.LLMRequest, maxTokens int, skipTemperature bool) (*Request, error) {
	parallelToolCalls, err := common.ParallelToolCalls(req)
	if err != nil {
		return nil, err
	}
	canonicalReq, err := common.CanonicalizeRequestTools(req)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tools: %w", err)
	}
	req = canonicalReq
	if maxTokens <= 0 {
		maxTokens = 8192 // Default fallback
	}
	bedrockReq := &Request{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         make([]Message, 0),
	}
	if !skipTemperature {
		temp := 0.7
		bedrockReq.Temperature = &temp
	}

	// Convert ADK messages to Bedrock messages
	for _, content := range req.Contents {
		role := content.Role
		if role == "model" {
			role = "assistant"
		}

		// Check if this message contains function calls or responses
		hasFunctionCall := false
		hasFunctionResponse := false
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				hasFunctionCall = true
			}
			if part.FunctionResponse != nil {
				hasFunctionResponse = true
			}
		}

		if hasFunctionCall {
			// Convert function calls to tool_use blocks
			var contentBlocks []ContentBlock
			for _, part := range content.Parts {
				if part.FunctionCall != nil {
					// Ensure Input is never nil - Bedrock requires this field for tool_use
					input := part.FunctionCall.Args
					if input == nil {
						input = make(map[string]interface{})
					}
					contentBlocks = append(contentBlocks, ContentBlock{
						Type:  "tool_use",
						ID:    part.FunctionCall.ID,
						Name:  part.FunctionCall.Name,
						Input: &input, // Use pointer
					})
				}
			}
			bedrockReq.Messages = append(bedrockReq.Messages, Message{
				Role:    role,
				Content: contentBlocks,
			})
		} else if hasFunctionResponse {
			// Convert function responses to tool_result blocks
			var contentBlocks []ContentBlock
			for _, part := range content.Parts {
				if part.FunctionResponse != nil {
					resultJSON, _ := json.Marshal(part.FunctionResponse.Response)
					contentBlocks = append(contentBlocks, ContentBlock{
						Type:      "tool_result",
						ToolUseID: part.FunctionResponse.ID,
						Content:   string(resultJSON),
					})
				}
			}
			bedrockReq.Messages = append(bedrockReq.Messages, Message{
				Role:    "user", // Tool results must be from user role
				Content: contentBlocks,
			})
		} else {
			// Regular text message — may also contain InlineData parts (attachments)
			hasInlineData := false
			for _, part := range content.Parts {
				if part.InlineData != nil {
					hasInlineData = true
					break
				}
			}

			if hasInlineData {
				// Build content blocks array (text + image/document blocks)
				var contentBlocks []ContentBlock
				for _, part := range content.Parts {
					if part.Text != "" {
						contentBlocks = append(contentBlocks, ContentBlock{
							Type: "text",
							Text: part.Text,
						})
					}
					if part.InlineData != nil {
						mimeType := part.InlineData.MIMEType
						if strings.HasPrefix(mimeType, "image/") {
							// Images: use "image" content block.
							contentBlocks = append(contentBlocks, ContentBlock{
								Type: "image",
								Source: &ImageSource{
									Type:      "base64",
									MediaType: mimeType,
									Data:      base64.StdEncoding.EncodeToString(part.InlineData.Data),
								},
							})
						} else if mimeType == "application/pdf" {
							// PDFs: use "document" content block.
							contentBlocks = append(contentBlocks, ContentBlock{
								Type: "document",
								Source: &ImageSource{
									Type:      "base64",
									MediaType: mimeType,
									Data:      base64.StdEncoding.EncodeToString(part.InlineData.Data),
								},
							})
						} else {
							// All other file types (text/markdown, text/html, application/json, etc.):
							// Decode to string and send as a text block. The Bedrock API only accepts
							// application/pdf and text/plain for "document" blocks — anything else
							// would be rejected with 400.
							textContent := string(part.InlineData.Data)
							contentBlocks = append(contentBlocks, ContentBlock{
								Type: "text",
								Text: textContent,
							})
						}
					}
				}
				bedrockReq.Messages = append(bedrockReq.Messages, Message{
					Role:    role,
					Content: contentBlocks,
				})
			} else {
				// Plain text message
				var textBuilder strings.Builder
				for _, part := range content.Parts {
					if part.Text != "" {
						textBuilder.WriteString(part.Text)
					}
				}
				bedrockReq.Messages = append(bedrockReq.Messages, Message{
					Role:    role,
					Content: textBuilder.String(),
				})
			}
		}
	}

	// Patch orphaned tool_use blocks: if an assistant message has tool_use blocks
	// without corresponding tool_result blocks in the following user message,
	// inject synthetic error tool_result blocks. This prevents Anthropic/Bedrock
	// 400 errors when tool execution fails mid-stream (e.g., "unknown tool").
	patchOrphanedToolUse(bedrockReq)

	// Handle system instruction
	if req.Config != nil && req.Config.SystemInstruction != nil {
		var sysBuilder strings.Builder
		for _, part := range req.Config.SystemInstruction.Parts {
			sysBuilder.WriteString(part.Text)
		}
		bedrockReq.System = sysBuilder.String()
	}

	// Handle tools
	if req.Config != nil && len(req.Config.Tools) > 0 {
		for _, tool := range req.Config.Tools {
			for _, funcDecl := range tool.FunctionDeclarations {
				bedrockTool := Tool{
					Name:        funcDecl.Name,
					Description: funcDecl.Description,
					InputSchema: make(map[string]interface{}),
				}

				// Convert JSON schema to Bedrock format
				if funcDecl.Parameters != nil {
					bedrockTool.InputSchema["type"] = "object"

					// Marshal and sanitize
					schemaBytes, _ := json.Marshal(funcDecl.Parameters) // Error checked at source usually
					var schemaMap map[string]interface{}
					if err := json.Unmarshal(schemaBytes, &schemaMap); err == nil {
						sanitizeSchema(schemaMap)
						if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
							bedrockTool.InputSchema["properties"] = props
						}
						if required, ok := schemaMap["required"].([]interface{}); ok {
							bedrockTool.InputSchema["required"] = required
						}
					}
				} else if funcDecl.ParametersJsonSchema != nil {
					bedrockTool.InputSchema["type"] = "object"

					// Marshal and sanitize
					schemaBytes, _ := json.Marshal(funcDecl.ParametersJsonSchema)
					var schemaMap map[string]interface{}
					if err := json.Unmarshal(schemaBytes, &schemaMap); err == nil {
						sanitizeSchema(schemaMap)
						if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
							bedrockTool.InputSchema["properties"] = props
						}
						if required, ok := schemaMap["required"].([]interface{}); ok {
							bedrockTool.InputSchema["required"] = required
						}
					}
				}

				bedrockReq.Tools = append(bedrockReq.Tools, bedrockTool)
			}
		}
	}

	// Translate only the explicit per-request option; preserve ordinary defaults.
	if parallelToolCalls != nil && len(bedrockReq.Tools) > 0 {
		bedrockReq.ToolChoice = &ToolChoice{
			Type:                   ToolChoiceAuto,
			DisableParallelToolUse: !*parallelToolCalls,
		}
	}
	return bedrockReq, nil
}

// sanitizeSchema recursively fixes schema types (upper -> lower) for Bedrock
func sanitizeSchema(schema map[string]interface{}) {
	// Fix type case (GenAI uses "STRING", JSON Schema uses "string")
	if t, ok := schema["type"].(string); ok {
		schema["type"] = strings.ToLower(t)
	}

	// Recurse into properties
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for _, prop := range props {
			if propMap, ok := prop.(map[string]interface{}); ok {
				sanitizeSchema(propMap)
			}
		}
	}

	// Recurse into array items
	if items, ok := schema["items"].(map[string]interface{}); ok {
		sanitizeSchema(items)
	}
}

// ParseResponse converts a Bedrock Response body to an ADK LLMResponse.
func ParseResponse(body []byte) (*model.LLMResponse, error) {
	var bedrockResp Response
	if err := json.Unmarshal(body, &bedrockResp); err != nil {
		return nil, fmt.Errorf("failed to decode bedrock response: %w", err)
	}

	// Convert back to ADK response
	var parts []*genai.Part
	for _, block := range bedrockResp.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, &genai.Part{Text: block.Text})
			}
		case "tool_use":
			// Convert tool_use to function call
			var args map[string]interface{}
			if block.Input != nil {
				args = *block.Input
			}
			parts = append(parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					ID:   block.ID,
					Name: block.Name,
					Args: args,
				},
			})
		}
	}

	resp := &model.LLMResponse{
		Content: &genai.Content{
			Role:  "model",
			Parts: parts,
		},
	}

	resp.UsageMetadata = usageMetadata(bedrockResp.Usage)

	return resp, nil
}

type streamStopReason string

const (
	streamStopEndTurn      streamStopReason = "end_turn"
	streamStopSequence     streamStopReason = "stop_sequence"
	streamStopToolUse      streamStopReason = "tool_use"
	streamStopMaxTokens    streamStopReason = "max_tokens"
	streamStopContextLimit streamStopReason = "model_context_window_exceeded"
)

// ParseStream parses a Bedrock SSE stream without progress notifications.
func ParseStream(reader io.Reader) iter.Seq2[*model.LLMResponse, error] {
	return ParseStreamWithProgress(reader, nil)
}

// ParseStreamWithProgress emits partial text immediately, but commits aggregate
// content and validated tools only at message_stop. Progress contains phases only
// and is emitted synchronously on phase changes, never per token.
func ParseStreamWithProgress(reader io.Reader, onProgress func(streaming.Progress)) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		br := bufio.NewReader(reader)
		var usage Usage
		var stopReason streamStopReason
		var parts []*genai.Part
		var text strings.Builder
		var tool *genai.FunctionCall
		var arguments strings.Builder
		argumentsStarted := false
		toolIndex := -1
		var phase streaming.Phase
		setPhase := func(next streaming.Phase) {
			if next != phase {
				phase = next
				if next != "" && onProgress != nil {
					onProgress(streaming.Progress{Phase: next})
				}
			}
		}
		flushText := func() {
			if text.Len() > 0 {
				parts = append(parts, &genai.Part{Text: text.String()})
				text.Reset()
			}
		}
		emitText := func(value string) bool {
			setPhase("")
			if value == "" {
				return true
			}
			text.WriteString(value)
			return yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: value}}}, Partial: true}, nil)
		}
		for {
			line, readErr := br.ReadBytes('\n')
			// Process an unterminated final data line, but never turn a read failure
			// into successful completion.
			if readErr != nil && readErr != io.EOF {
				yield(nil, fmt.Errorf("bedrock stream read: %w", readErr))
				return
			}
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("event:")) {
				event := strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
				if event == "error" || strings.HasSuffix(event, "Exception") {
					yield(nil, fmt.Errorf("bedrock stream error event"))
					return
				}
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				var chunk struct {
					Type         string `json:"type"`
					Index        int    `json:"index"`
					ContentBlock *struct {
						Type  string          `json:"type"`
						ID    string          `json:"id"`
						Name  string          `json:"name"`
						Text  string          `json:"text"`
						Input json.RawMessage `json:"input"`
					} `json:"content_block"`
					Delta *struct {
						Type        string           `json:"type"`
						Text        string           `json:"text"`
						PartialJSON string           `json:"partial_json"`
						StopReason  streamStopReason `json:"stop_reason"`
					} `json:"delta"`
					Message *struct {
						Usage Usage `json:"usage"`
					} `json:"message"`
					Usage *Usage          `json:"usage"`
					Error json.RawMessage `json:"error"`
				}
				data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if err := json.Unmarshal(data, &chunk); err != nil {
					yield(nil, fmt.Errorf("malformed bedrock stream event: %w", err))
					return
				}
				if chunk.Type == "error" || len(chunk.Error) > 0 || strings.HasSuffix(chunk.Type, "Exception") {
					yield(nil, fmt.Errorf("bedrock stream error event"))
					return
				}
				switch chunk.Type {
				case "message_start":
					if chunk.Message != nil {
						usage = chunk.Message.Usage
					}
				case "message_delta":
					if chunk.Delta != nil && chunk.Delta.StopReason != "" {
						if stopReason != "" && stopReason != chunk.Delta.StopReason {
							yield(nil, fmt.Errorf("conflicting bedrock stop reasons: %q and %q", stopReason, chunk.Delta.StopReason))
							return
						}
						stopReason = chunk.Delta.StopReason
					}
					if chunk.Usage != nil {
						usage.OutputTokens = chunk.Usage.OutputTokens
					}
				case "content_block_start":
					if tool != nil || chunk.ContentBlock == nil {
						yield(nil, fmt.Errorf("invalid bedrock content block start"))
						return
					}
					block := chunk.ContentBlock
					switch block.Type {
					case "thinking", "redacted_thinking":
						setPhase(streaming.PhaseThinking)
					case "text":
						if !emitText(block.Text) {
							return
						}
					case "tool_use":
						setPhase(streaming.PhaseToolArguments)
						if block.ID == "" || block.Name == "" {
							yield(nil, fmt.Errorf("invalid bedrock tool identity"))
							return
						}
						args := map[string]interface{}{}
						if len(block.Input) > 0 {
							if err := json.Unmarshal(block.Input, &args); err != nil || args == nil {
								yield(nil, fmt.Errorf("invalid bedrock tool input: expected JSON object"))
								return
							}
						}
						flushText()
						tool = &genai.FunctionCall{ID: block.ID, Name: block.Name, Args: args}
						toolIndex = chunk.Index
						arguments.Reset()
						argumentsStarted = false
					}
				case "content_block_delta":
					if chunk.Delta == nil {
						yield(nil, fmt.Errorf("invalid bedrock content delta"))
						return
					}
					delta := chunk.Delta
					if tool != nil && (delta.Type != "input_json_delta" || chunk.Index != toolIndex) {
						yield(nil, fmt.Errorf("invalid delta inside bedrock tool"))
						return
					}
					switch delta.Type {
					case "thinking_delta", "signature_delta":
						setPhase(streaming.PhaseThinking)
					case "text_delta":
						if tool != nil {
							yield(nil, fmt.Errorf("text inside unfinished bedrock tool"))
							return
						}
						if !emitText(delta.Text) {
							return
						}
					case "input_json_delta":
						if tool == nil || chunk.Index != toolIndex {
							yield(nil, fmt.Errorf("bedrock tool delta without matching start"))
							return
						}
						setPhase(streaming.PhaseToolArguments)
						argumentsStarted = true
						arguments.WriteString(delta.PartialJSON)
					}
				case "content_block_stop":
					if tool != nil {
						if chunk.Index != toolIndex {
							yield(nil, fmt.Errorf("bedrock tool stop index mismatch"))
							return
						}
						if argumentsStarted {
							var args map[string]interface{}
							if err := json.Unmarshal([]byte(arguments.String()), &args); err != nil || args == nil {
								yield(nil, fmt.Errorf("invalid bedrock tool arguments: expected complete JSON object"))
								return
							}
							tool.Args = args
						}
						parts = append(parts, &genai.Part{FunctionCall: tool})
						tool = nil
					}
				case "message_stop":
					// A closed content block only proves its JSON is complete, not that
					// the model finished safely. Never commit truncated/refused tools.
					switch stopReason {
					case "", streamStopEndTurn, streamStopSequence, streamStopToolUse:
						// Older streams omit the reason; message_stop is still required.
					case streamStopMaxTokens, streamStopContextLimit:
						yield(nil, fmt.Errorf("bedrock stream truncated: stop reason %q", stopReason))
						return
					default:
						yield(nil, fmt.Errorf("bedrock stream unsuccessful stop reason %q", stopReason))
						return
					}
					if tool != nil {
						yield(nil, fmt.Errorf("unfinished bedrock tool at message_stop"))
						return
					}
					flushText()
					metadata := usageMetadata(usage)
					if len(parts) > 0 || metadata != nil {
						yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: parts}, UsageMetadata: metadata}, nil)
					}
					return
				}
			}
			if readErr == io.EOF {
				yield(nil, fmt.Errorf("bedrock stream missing message_stop: %w", io.ErrUnexpectedEOF))
				return
			}
		}
	}
}

func usageMetadata(usage Usage) *genai.GenerateContentResponseUsageMetadata {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheCreationInputTokens == nil && usage.CacheReadInputTokens == nil &&
		usage.BedrockCacheWriteInputTokens == nil && usage.BedrockCacheReadInputTokens == nil {
		return nil
	}
	cacheWrite := firstPresent(usage.CacheCreationInputTokens, usage.BedrockCacheWriteInputTokens)
	cacheRead := firstPresent(usage.CacheReadInputTokens, usage.BedrockCacheReadInputTokens)
	promptTokens := usage.InputTokens + valueOrZero(cacheWrite) + valueOrZero(cacheRead)
	metadata := &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     int32(promptTokens),
		CandidatesTokenCount: int32(usage.OutputTokens),
		TotalTokenCount:      int32(promptTokens + usage.OutputTokens),
	}
	if cacheRead != nil {
		metadata.CachedContentTokenCount = int32(*cacheRead)
	}
	return metadata
}

func firstPresent(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// patchOrphanedToolUse scans the messages array for assistant messages containing
// tool_use blocks that lack corresponding tool_result blocks in the following user
// message. For each orphan, a synthetic error tool_result is injected. This prevents
// Anthropic/Bedrock 400 errors when tool execution fails and no FunctionResponse
// event is persisted to the session.
func patchOrphanedToolUse(req *Request) {
	for i := 0; i < len(req.Messages); i++ {
		msg := req.Messages[i]
		if msg.Role != "assistant" {
			continue
		}

		// Extract tool_use IDs from this assistant message
		blocks, ok := msg.Content.([]ContentBlock)
		if !ok {
			continue
		}
		var toolUseIDs []string
		for _, b := range blocks {
			if b.Type == "tool_use" && b.ID != "" {
				toolUseIDs = append(toolUseIDs, b.ID)
			}
		}
		if len(toolUseIDs) == 0 {
			continue
		}

		// Collect tool_result IDs from the immediately following user message
		answeredIDs := make(map[string]bool)
		if i+1 < len(req.Messages) && req.Messages[i+1].Role == "user" {
			switch next := req.Messages[i+1].Content.(type) {
			case []ContentBlock:
				for _, b := range next {
					if b.Type == "tool_result" && b.ToolUseID != "" {
						answeredIDs[b.ToolUseID] = true
					}
				}
			}
		}

		// Find orphaned tool_use IDs (no matching tool_result)
		var orphanIDs []string
		for _, id := range toolUseIDs {
			if !answeredIDs[id] {
				orphanIDs = append(orphanIDs, id)
			}
		}
		if len(orphanIDs) == 0 {
			continue
		}

		// Build synthetic tool_result blocks for orphans
		var resultBlocks []ContentBlock
		for _, id := range orphanIDs {
			resultBlocks = append(resultBlocks, ContentBlock{
				Type:      "tool_result",
				ToolUseID: id,
				Content:   `{"error": "Tool call was not executed due to an internal error."}`,
			})
		}

		// Insert a new user message with the synthetic tool_results
		// right after the assistant message
		syntheticMsg := Message{
			Role:    "user",
			Content: resultBlocks,
		}

		if i+1 < len(req.Messages) && req.Messages[i+1].Role == "user" {
			// The next message is already a user message -- merge the synthetic
			// results into it if it has content blocks, or insert before it otherwise
			switch existing := req.Messages[i+1].Content.(type) {
			case []ContentBlock:
				req.Messages[i+1].Content = append(resultBlocks, existing...)
			default:
				// Next user message is a plain string; insert synthetic message between
				newMsgs := make([]Message, 0, len(req.Messages)+1)
				newMsgs = append(newMsgs, req.Messages[:i+1]...)
				newMsgs = append(newMsgs, syntheticMsg)
				newMsgs = append(newMsgs, req.Messages[i+1:]...)
				req.Messages = newMsgs
				i++ // skip the inserted message
			}
		} else {
			// No following message or it's not a user message; insert synthetic message
			newMsgs := make([]Message, 0, len(req.Messages)+1)
			newMsgs = append(newMsgs, req.Messages[:i+1]...)
			newMsgs = append(newMsgs, syntheticMsg)
			if i+1 < len(req.Messages) {
				newMsgs = append(newMsgs, req.Messages[i+1:]...)
			}
			req.Messages = newMsgs
			i++ // skip the inserted message
		}
	}
}
