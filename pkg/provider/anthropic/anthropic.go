package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/provider/httpool"
	"github.com/SAP/astonish/pkg/provider/llmerror"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

const (
	apiURL     = "https://api.anthropic.com/v1/messages"
	modelsURL  = "https://api.anthropic.com/v1/models"
	apiVersion = "2023-06-01"
)

// Model cache for avoiding repeated API calls
var (
	modelCacheMu   sync.RWMutex
	modelCache     []ModelInfo
	modelCacheTime time.Time
	modelCacheTTL  = 1 * time.Hour
)

// Provider implements model.LLM for Anthropic.
type Provider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewProvider creates a new Anthropic provider.
func NewProvider(apiKey, modelName string) *Provider {
	return &Provider{
		apiKey: apiKey,
		model:  modelName,
		client: httpool.StreamingClient(),
	}
}

// Name implements model.LLM.
func (p *Provider) Name() string {
	return p.model
}

// GenerateContent implements model.LLM.
func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Convert ADK request to Anthropic request
		anthropicReq, err := p.toAnthropicRequest(req, streaming)
		if err != nil {
			yield(nil, err)
			return
		}

		reqBody, err := json.Marshal(anthropicReq)
		if err != nil {
			yield(nil, err)
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(reqBody))
		if err != nil {
			yield(nil, err)
			return
		}

		httpReq.Header.Set("x-api-key", p.apiKey)
		httpReq.Header.Set("anthropic-version", apiVersion)
		httpReq.Header.Set("content-type", "application/json")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
			}
			yield(nil, llmerror.NewFromResponse("anthropic", resp, body))
			return
		}

		if streaming {
			p.handleStream(resp.Body, yield)
		} else {
			p.handleResponse(resp.Body, yield)
		}
	}
}

func (p *Provider) handleResponse(body io.Reader, yield func(*model.LLMResponse, error) bool) {
	var resp Response
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		yield(nil, err)
		return
	}

	// Convert Anthropic response to ADK response
	llmResp := &model.LLMResponse{
		Content: &genai.Content{
			Role: "model",
		},
	}

	for _, content := range resp.Content {
		if content.Type == "text" {
			llmResp.Content.Parts = append(llmResp.Content.Parts, &genai.Part{Text: content.Text})
		} else if content.Type == "tool_use" {
			var args map[string]any
			if err := json.Unmarshal(content.Input, &args); err != nil {
				// Handle error or use empty map
				args = make(map[string]any)
			}

			llmResp.Content.Parts = append(llmResp.Content.Parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					Name: content.Name,
					Args: args,
					ID:   content.ID,
				},
			})
		}
	}

	llmResp.UsageMetadata = usageMetadata(resp.Usage)

	yield(llmResp, nil)
}

func (p *Provider) handleStream(body io.Reader, yield func(*model.LLMResponse, error) bool) {
	scanner := bufio.NewScanner(body)

	// State for accumulating tool calls
	var currentToolID string
	var currentToolName string
	var currentToolInput strings.Builder
	var isCollectingTool bool
	var currentBlockIndex int

	// Accumulate text for streaming — each chunk is yielded with
	// Partial=true for live display, and at stream end we emit one
	// aggregated non-partial response for session persistence.
	var textAccum strings.Builder

	// Accumulate token usage from message_start and message_delta events.
	var usage Usage

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event StreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				usage = event.Message.Usage
			}

		case "message_delta":
			if event.Usage != nil {
				usage.OutputTokens = event.Usage.OutputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				// Emit aggregated text before tool calls (model may produce
				// a preamble like "Let me check that" before invoking tools).
				if textAccum.Len() > 0 {
					if !yield(&model.LLMResponse{
						Content: &genai.Content{
							Role:  "model",
							Parts: []*genai.Part{{Text: textAccum.String()}},
						},
					}, nil) {
						return
					}
					textAccum.Reset()
				}

				isCollectingTool = true
				currentToolID = event.ContentBlock.ID
				currentToolName = event.ContentBlock.Name
				currentToolInput.Reset()
				currentBlockIndex = event.Index
			}

		case "content_block_delta":
			if event.Delta != nil {
				if event.Delta.Type == "text_delta" {
					textAccum.WriteString(event.Delta.Text)
					if !yield(&model.LLMResponse{
						Content: &genai.Content{
							Role: "model",
							Parts: []*genai.Part{
								{Text: event.Delta.Text},
							},
						},
						Partial: true,
					}, nil) {
						return
					}
				} else if event.Delta.Type == "input_json_delta" && isCollectingTool {
					currentToolInput.WriteString(event.Delta.PartialJson)
				}
			}

		case "content_block_stop":
			if isCollectingTool && event.Index == currentBlockIndex {
				// Tool call complete
				var args map[string]any
				if err := json.Unmarshal([]byte(currentToolInput.String()), &args); err != nil {
					// Handle error
					args = make(map[string]any)
				}

				yield(&model.LLMResponse{
					Content: &genai.Content{
						Role: "model",
						Parts: []*genai.Part{
							{
								FunctionCall: &genai.FunctionCall{
									Name: currentToolName,
									Args: args,
									ID:   currentToolID,
								},
							},
						},
					},
				}, nil)

				isCollectingTool = false
				currentToolInput.Reset()
			}
		}
	}

	usageMetadata := usageMetadata(usage)

	// Emit aggregated text response at stream end
	if textAccum.Len() > 0 {
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: textAccum.String()}},
			},
			UsageMetadata: usageMetadata,
		}, nil)
	} else if usageMetadata != nil {
		// No trailing text — attach usage to a content-less response so
		// the ChatRunner still sees the token counts (e.g. tool-only turns).
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role: "model",
			},
			UsageMetadata: usageMetadata,
		}, nil)
	}
}

func usageMetadata(usage Usage) *genai.GenerateContentResponseUsageMetadata {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheCreationInputTokens == nil && usage.CacheReadInputTokens == nil {
		return nil
	}
	promptTokens := usage.InputTokens + valueOrZero(usage.CacheCreationInputTokens) + valueOrZero(usage.CacheReadInputTokens)
	metadata := &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     int32(promptTokens),
		CandidatesTokenCount: int32(usage.OutputTokens),
		TotalTokenCount:      int32(promptTokens + usage.OutputTokens),
	}
	if usage.CacheReadInputTokens != nil {
		metadata.CachedContentTokenCount = int32(*usage.CacheReadInputTokens)
	}
	return metadata
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (p *Provider) toAnthropicRequest(req *model.LLMRequest, streaming bool) (*Request, error) {
	canonicalReq, err := common.CanonicalizeRequestTools(req)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tools: %w", err)
	}
	req = canonicalReq
	var messages []Message
	var system string

	// Extract system instruction
	if req.Config != nil && req.Config.SystemInstruction != nil {
		var sb strings.Builder
		for _, part := range req.Config.SystemInstruction.Parts {
			sb.WriteString(part.Text)
		}
		system = sb.String()
	}

	for _, c := range req.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}

		var content []Content
		for _, part := range c.Parts {
			if part.Text != "" {
				content = append(content, Content{
					Type: "text",
					Text: part.Text,
				})
			}

			if part.InlineData != nil {
				mimeType := part.InlineData.MIMEType
				if strings.HasPrefix(mimeType, "image/") {
					// Images: use "image" content block.
					content = append(content, Content{
						Type: "image",
						Source: &ContentSource{
							Type:      "base64",
							MediaType: mimeType,
							Data:      base64.StdEncoding.EncodeToString(part.InlineData.Data),
						},
					})
				} else if mimeType == "application/pdf" {
					// PDFs: use "document" content block.
					content = append(content, Content{
						Type: "document",
						Source: &ContentSource{
							Type:      "base64",
							MediaType: mimeType,
							Data:      base64.StdEncoding.EncodeToString(part.InlineData.Data),
						},
					})
				} else {
					// All other file types (text/markdown, text/html, application/json, etc.):
					// Decode to string and send as a text block. The Anthropic API only
					// accepts application/pdf and text/plain for "document" blocks.
					textContent := string(part.InlineData.Data)
					content = append(content, Content{
						Type: "text",
						Text: textContent,
					})
				}
			}

			if part.FunctionCall != nil {
				argsBytes, _ := json.Marshal(part.FunctionCall.Args)
				content = append(content, Content{
					Type:  "tool_use",
					ID:    part.FunctionCall.ID,
					Name:  part.FunctionCall.Name,
					Input: argsBytes,
				})
			}

			if part.FunctionResponse != nil {
				// Tool results are user messages
				role = "user"

				resBytes, err := json.Marshal(part.FunctionResponse.Response)
				if err != nil {
					resBytes = []byte(fmt.Sprintf("%v", part.FunctionResponse.Response))
				}

				content = append(content, Content{
					Type:      "tool_result",
					ToolUseID: part.FunctionResponse.ID,
					Content:   string(resBytes),
				})
			}
		}

		if len(content) > 0 {
			messages = append(messages, Message{
				Role:    role,
				Content: content,
			})
		}
	}

	// Patch orphaned tool_use blocks: if an assistant message has tool_use blocks
	// without corresponding tool_result blocks in the following user message,
	// inject synthetic error tool_result blocks. This prevents Anthropic 400 errors
	// when tool execution fails mid-stream (e.g., "unknown tool").
	messages = patchOrphanedToolUse(messages)

	// Map tools
	var tools []Tool
	if req.Config != nil && len(req.Config.Tools) > 0 {
		for _, t := range req.Config.Tools {
			for _, fd := range t.FunctionDeclarations {
				schemaBytes, _ := json.Marshal(fd.ParametersJsonSchema)
				tools = append(tools, Tool{
					Name:        fd.Name,
					Description: fd.Description,
					InputSchema: schemaBytes,
				})
			}
		}
	}

	return &Request{
		Model:     p.model,
		Messages:  messages,
		System:    system,
		MaxTokens: 64000,
		Stream:    streaming,
		Tools:     tools,
	}, nil
}

// Structs for Anthropic API

type Request struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream,omitempty"`
	Tools     []Tool    `json:"tools,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

type Content struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // For tool_use
	Name      string          `json:"name,omitempty"`        // For tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // For tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // For tool_result
	Content   string          `json:"content,omitempty"`     // For tool_result (can be string or list of content blocks)
	IsError   bool            `json:"is_error,omitempty"`    // For tool_result
	Source    *ContentSource  `json:"source,omitempty"`      // For image/document attachments
}

// ContentSource represents the source data for image/document content blocks.
type ContentSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/png", "application/pdf"
	Data      string `json:"data"`       // base64-encoded data
}

type Usage struct {
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

type Response struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Role    string    `json:"role"`
	Content []Content `json:"content"`
	Usage   Usage     `json:"usage"`
}

type StreamEvent struct {
	Type         string       `json:"type"`
	Delta        *StreamDelta `json:"delta,omitempty"`
	ContentBlock *Content     `json:"content_block,omitempty"` // For content_block_start
	Index        int          `json:"index,omitempty"`
	// message_start carries the full message envelope with usage.
	Message *struct {
		Usage Usage `json:"usage"`
	} `json:"message,omitempty"`
	// message_delta carries final usage (output tokens).
	Usage *Usage `json:"usage,omitempty"`
}

type StreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJson string `json:"partial_json,omitempty"` // For tool_use input delta
}

// ModelInfo represents model information for UI display
type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// ModelsAPIResponse represents the response from /v1/models
type ModelsAPIResponse struct {
	Data    []ModelsAPIModel `json:"data"`
	HasMore bool             `json:"has_more"`
	FirstID string           `json:"first_id"`
	LastID  string           `json:"last_id"`
}

// ModelsAPIModel represents a single model in the API response
type ModelsAPIModel struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

// fetchModels fetches all models from the Anthropic API, handling pagination
func fetchModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	var allModels []ModelInfo
	afterID := ""

	client := httpool.Client(30 * time.Second)

	for {
		url := modelsURL
		if afterID != "" {
			url = fmt.Sprintf("%s?after_id=%s", modelsURL, afterID)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", apiVersion)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch models: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
			}
			return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
		}

		var modelsResp ModelsAPIResponse
		if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

		for _, m := range modelsResp.Data {
			allModels = append(allModels, ModelInfo{
				ID:          m.ID,
				Name:        m.DisplayName, // Use display name as the main name
				DisplayName: m.DisplayName,
				CreatedAt:   m.CreatedAt,
			})
		}

		if !modelsResp.HasMore {
			break
		}
		afterID = modelsResp.LastID
	}

	return allModels, nil
}

// ListModelsWithMetadata returns all models with metadata, using cache
func ListModelsWithMetadata(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	modelCacheMu.RLock()
	if modelCache != nil && time.Since(modelCacheTime) < modelCacheTTL {
		result := modelCache
		modelCacheMu.RUnlock()
		return result, nil
	}
	modelCacheMu.RUnlock()

	// Fetch fresh data
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()

	// Double-check after acquiring write lock
	if modelCache != nil && time.Since(modelCacheTime) < modelCacheTTL {
		return modelCache, nil
	}

	models, err := fetchModels(ctx, apiKey)
	if err != nil {
		return nil, err
	}

	modelCache = models
	modelCacheTime = time.Now()

	return models, nil
}

// ListModels returns a simple list of model IDs for backward compatibility
func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	models, err := ListModelsWithMetadata(ctx, apiKey)
	if err != nil {
		return nil, err
	}

	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// patchOrphanedToolUse scans the messages array for assistant messages containing
// tool_use blocks that lack corresponding tool_result blocks in the following user
// message. For each orphan, a synthetic error tool_result is injected. This prevents
// Anthropic 400 errors when tool execution fails and no FunctionResponse event is
// persisted to the session.
func patchOrphanedToolUse(messages []Message) []Message {
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != "assistant" {
			continue
		}

		// Extract tool_use IDs from this assistant message
		var toolUseIDs []string
		for _, c := range msg.Content {
			if c.Type == "tool_use" && c.ID != "" {
				toolUseIDs = append(toolUseIDs, c.ID)
			}
		}
		if len(toolUseIDs) == 0 {
			continue
		}

		// Collect tool_result IDs from the immediately following user message
		answeredIDs := make(map[string]bool)
		if i+1 < len(messages) && messages[i+1].Role == "user" {
			for _, c := range messages[i+1].Content {
				if c.Type == "tool_result" && c.ToolUseID != "" {
					answeredIDs[c.ToolUseID] = true
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
		var resultBlocks []Content
		for _, id := range orphanIDs {
			resultBlocks = append(resultBlocks, Content{
				Type:      "tool_result",
				ToolUseID: id,
				IsError:   true,
			})
		}

		// Insert synthetic tool_results into the conversation
		if i+1 < len(messages) && messages[i+1].Role == "user" {
			// Merge into the existing user message
			messages[i+1].Content = append(resultBlocks, messages[i+1].Content...)
		} else {
			// Insert a new user message with the synthetic tool_results
			syntheticMsg := Message{
				Role:    "user",
				Content: resultBlocks,
			}
			newMsgs := make([]Message, 0, len(messages)+1)
			newMsgs = append(newMsgs, messages[:i+1]...)
			newMsgs = append(newMsgs, syntheticMsg)
			if i+1 < len(messages) {
				newMsgs = append(newMsgs, messages[i+1:]...)
			}
			messages = newMsgs
			i++ // skip the inserted message
		}
	}
	return messages
}
