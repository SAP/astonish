package vertex

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

// Request represents the Vertex AI request payload.
type Request struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
}

type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature []byte            `json:"thoughtSignature,omitempty"`
	Text             string            `json:"text,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
}

// InlineData represents inline binary data (images, PDFs, etc.) for Vertex AI.
type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64-encoded
}

type FunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type FunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type FunctionDeclaration struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters,omitempty"` // JSON Schema
}

type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // AUTO, ANY, NONE
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type GenerationConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	TopP            float64 `json:"topP,omitempty"`
	TopK            int     `json:"topK,omitempty"`
}

// Response represents the Vertex AI response payload.
type Response struct {
	Candidates    []Candidate                                 `json:"candidates"`
	UsageMetadata *genai.GenerateContentResponseUsageMetadata `json:"usageMetadata,omitempty"`
}

type Candidate struct {
	Content      *Content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
}

// ConvertRequest converts an ADK LLMRequest to a Vertex AI Request.
// maxOutputTokens can be 0 to use the default (8192)
func ConvertRequest(req *model.LLMRequest, maxOutputTokens int) (*Request, error) {
	canonicalReq, err := common.CanonicalizeRequestTools(req)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tools: %w", err)
	}
	req = canonicalReq
	if maxOutputTokens <= 0 {
		maxOutputTokens = 8192 // Default fallback
	}
	vertexReq := &Request{
		Contents: make([]Content, 0),
		GenerationConfig: &GenerationConfig{
			Temperature:     0.7, // Default
			MaxOutputTokens: maxOutputTokens,
		},
	}

	// Convert Contents
	for _, c := range req.Contents {
		parts := make([]Part, 0)
		for _, p := range c.Parts {
			part := Part{Thought: p.Thought, ThoughtSignature: p.ThoughtSignature}
			if p.Text != "" {
				part.Text = p.Text
			}
			if p.InlineData != nil {
				part.InlineData = &InlineData{
					MimeType: p.InlineData.MIMEType,
					Data:     base64.StdEncoding.EncodeToString(p.InlineData.Data),
				}
			}
			if p.FunctionCall != nil {
				part.FunctionCall = &FunctionCall{
					Name: p.FunctionCall.Name,
					Args: p.FunctionCall.Args,
				}
			}
			if p.FunctionResponse != nil {
				part.FunctionResponse = &FunctionResponse{
					Name:     p.FunctionResponse.Name,
					Response: p.FunctionResponse.Response,
				}
			}
			// Skip empty parts — Vertex/Gemini 400s on `{}` parts.
			if part.Text == "" && part.InlineData == nil && part.FunctionCall == nil && part.FunctionResponse == nil && len(part.ThoughtSignature) == 0 {
				continue
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			continue
		}
		vertexReq.Contents = append(vertexReq.Contents, Content{
			Role:  c.Role,
			Parts: parts,
		})
	}

	// Convert System Instruction
	if req.Config != nil && req.Config.SystemInstruction != nil {
		parts := make([]Part, 0)
		for _, p := range req.Config.SystemInstruction.Parts {
			if p.Text == "" {
				continue
			}
			parts = append(parts, Part{Text: p.Text})
		}
		if len(parts) > 0 {
			vertexReq.SystemInstruction = &Content{
				Parts: parts,
			}
		}
	}

	// Convert Tools
	if req.Config != nil && len(req.Config.Tools) > 0 {
		for _, t := range req.Config.Tools {
			fds := make([]FunctionDeclaration, 0)
			for _, fd := range t.FunctionDeclarations {
				params := sanitizeToolParameters(fd)
				fds = append(fds, FunctionDeclaration{
					Name:        fd.Name,
					Description: fd.Description,
					Parameters:  params,
				})
			}
			if len(fds) == 0 {
				continue
			}
			vertexReq.Tools = append(vertexReq.Tools, Tool{
				FunctionDeclarations: fds,
			})
		}
	}

	// Convert ToolConfig (FunctionCallingConfig)
	if req.Config != nil && req.Config.ToolConfig != nil {
		vertexReq.ToolConfig = &ToolConfig{
			FunctionCallingConfig: &FunctionCallingConfig{
				Mode: string(req.Config.ToolConfig.FunctionCallingConfig.Mode),
			},
		}
		if len(req.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) > 0 {
			vertexReq.ToolConfig.FunctionCallingConfig.AllowedFunctionNames = req.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames
		}
	} else {
		// fmt.Println("[VERTEX DEBUG] No ToolConfig found in request")
	}

	return vertexReq, nil
}

// sanitizeToolParameters converts an ADK function declaration's parameter
// schema into a Vertex/Gemini-compatible JSON Schema. Gemini's REST API
// rejects several JSON Schema keywords that ADK/jsonschema-go emit
// (notably nested additionalProperties and $schema).
func sanitizeToolParameters(fd *genai.FunctionDeclaration) any {
	raw := fd.ParametersJsonSchema
	if raw == nil && fd.Parameters != nil {
		raw = fd.Parameters
	}
	if raw == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}

	schemaBytes, err := json.Marshal(raw)
	if err != nil {
		return raw
	}
	var paramsMap map[string]any
	if err := json.Unmarshal(schemaBytes, &paramsMap); err != nil {
		return raw
	}
	sanitizeSchema(paramsMap)
	if len(paramsMap) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return paramsMap
}

// unsupportedSchemaKeys are JSON Schema keywords Gemini/Vertex reject in
// functionDeclarations.parameters (and nested object schemas).
var unsupportedSchemaKeys = map[string]bool{
	"$schema":               true,
	"$id":                   true,
	"$defs":                 true,
	"$ref":                  true,
	"definitions":           true,
	"additionalProperties":  true,
	"unevaluatedProperties": true,
	"patternProperties":     true,
}

// sanitizeSchema recursively strips Gemini-unsupported JSON Schema keywords
// and normalizes nullable type unions produced by jsonschema-go for pointer
// fields (e.g. ["null","integer"] → "integer").
func sanitizeSchema(schema map[string]any) {
	for k := range unsupportedSchemaKeys {
		delete(schema, k)
	}

	if t, ok := schema["type"]; ok {
		schema["type"] = normalizeSchemaType(t)
	}

	if props, ok := schema["properties"].(map[string]any); ok {
		for _, prop := range props {
			if propMap, ok := prop.(map[string]any); ok {
				sanitizeSchema(propMap)
			}
		}
	}

	switch items := schema["items"].(type) {
	case map[string]any:
		sanitizeSchema(items)
	case []any:
		for _, item := range items {
			if itemMap, ok := item.(map[string]any); ok {
				sanitizeSchema(itemMap)
			}
		}
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			for _, item := range arr {
				if itemMap, ok := item.(map[string]any); ok {
					sanitizeSchema(itemMap)
				}
			}
		}
	}
}

// normalizeSchemaType converts JSON Schema type unions that include null
// (from Go pointer fields) into a single non-null type. Gemini rejects
// type arrays in function parameter schemas.
func normalizeSchemaType(t any) any {
	arr, ok := t.([]any)
	if !ok {
		return t
	}
	var nonNull any
	for _, v := range arr {
		s, ok := v.(string)
		if !ok || s == "null" {
			continue
		}
		if nonNull != nil {
			// Multiple non-null types — keep the original union.
			return t
		}
		nonNull = s
	}
	if nonNull != nil {
		return nonNull
	}
	return t
}

// mapPart converts a Vertex Part into a genai.Part. Returns nil when the part
// has no usable fields (empty after dropping unknown content).
func mapPart(p Part) *genai.Part {
	part := &genai.Part{Thought: p.Thought, ThoughtSignature: p.ThoughtSignature}
	if p.Text != "" {
		part.Text = p.Text
	}
	if p.FunctionCall != nil {
		part.FunctionCall = &genai.FunctionCall{
			Name: p.FunctionCall.Name,
			Args: p.FunctionCall.Args,
		}
	}
	if p.InlineData != nil && p.InlineData.Data != "" {
		raw, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
		if err != nil {
			// Some providers omit padding; try raw encoding as a fallback.
			raw, err = base64.RawStdEncoding.DecodeString(p.InlineData.Data)
		}
		if err == nil && len(raw) > 0 {
			part.InlineData = &genai.Blob{
				MIMEType: p.InlineData.MimeType,
				Data:     raw,
			}
		}
	}
	if part.Text == "" && part.FunctionCall == nil && part.InlineData == nil && len(part.ThoughtSignature) == 0 {
		return nil
	}
	return part
}

// ParseResponse converts a Vertex AI Response body to an ADK LLMResponse.
func ParseResponse(body []byte) (*model.LLMResponse, error) {
	var vertexResp Response
	if err := json.Unmarshal(body, &vertexResp); err != nil {
		return nil, fmt.Errorf("failed to decode vertex response: %w", err)
	}

	if len(vertexResp.Candidates) == 0 {
		return &model.LLMResponse{UsageMetadata: vertexResp.UsageMetadata}, nil
	}

	candidate := vertexResp.Candidates[0]
	if candidate.Content == nil {
		return &model.LLMResponse{UsageMetadata: vertexResp.UsageMetadata}, nil
	}

	var parts []*genai.Part
	for _, p := range candidate.Content.Parts {
		if part := mapPart(p); part != nil {
			parts = append(parts, part)
		}
	}

	return &model.LLMResponse{
		Content: &genai.Content{
			Role:  candidate.Content.Role,
			Parts: parts,
		},
		UsageMetadata: vertexResp.UsageMetadata,
		FinishReason:  genai.FinishReason(candidate.FinishReason),
		TurnComplete:  genai.FinishReason(candidate.FinishReason) == genai.FinishReasonStop,
	}, nil
}

// ParseStream parses a Vertex AI SSE stream without progress notifications.
func ParseStream(reader io.Reader) iter.Seq2[*model.LLMResponse, error] {
	return ParseStreamWithProgress(reader, nil)
}

// ParseStreamWithProgress yields partial text immediately, but holds final content
// (including tools) until a valid finishReason and clean EOF. Vertex may send usage
// after the finishReason, so that field alone is not a transport terminator.
// Progress is synchronous, payload-free, and emitted at most once per phase.
func ParseStreamWithProgress(reader io.Reader, onProgress func(streaming.Progress)) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		br := bufio.NewReader(reader)
		var parts []*genai.Part
		var text strings.Builder
		var usage *genai.GenerateContentResponseUsageMetadata
		var finish genai.FinishReason
		role := "model"
		seenProgress := make(map[streaming.Phase]bool)
		report := func(phase streaming.Phase) {
			if !seenProgress[phase] {
				seenProgress[phase] = true
				if onProgress != nil {
					onProgress(streaming.Progress{Phase: phase})
				}
			}
		}
		flushText := func() {
			if text.Len() > 0 {
				parts = append(parts, &genai.Part{Text: text.String()})
				text.Reset()
			}
		}
		for {
			line, readErr := br.ReadBytes('\n')
			if readErr != nil && readErr != io.EOF {
				yield(nil, fmt.Errorf("read vertex stream: %w", readErr))
				return
			}
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("data:")) {
				data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				var response *Response
				if err := json.Unmarshal(data, &response); err != nil || response == nil {
					// Do not put response payloads (including arguments) in diagnostics.
					yield(nil, fmt.Errorf("invalid vertex stream JSON response"))
					return
				}
				if response.UsageMetadata != nil {
					usage = response.UsageMetadata
				}
				// Preserve the adapter's first-candidate selection policy.
				if len(response.Candidates) > 0 {
					candidate := response.Candidates[0]
					if finish != "" && candidate.Content != nil && len(candidate.Content.Parts) > 0 {
						yield(nil, fmt.Errorf("vertex stream content after finishReason"))
						return
					}
					var partial []*genai.Part
					if candidate.Content != nil {
						if candidate.Content.Role != "" {
							role = candidate.Content.Role
						}
						for _, p := range candidate.Content.Parts {
							if p.Thought {
								report(streaming.PhaseThinking)
							}
							if p.FunctionCall != nil {
								report(streaming.PhaseToolArguments)
								if strings.TrimSpace(p.FunctionCall.Name) == "" || p.FunctionCall.Args == nil {
									yield(nil, fmt.Errorf("invalid vertex stream function call: name and object arguments required"))
									return
								}
							}
							part := mapPart(p)
							if part == nil {
								continue
							}
							// Only ordinary text may be merged. Thought signatures belong
							// to their original parts and must survive the next request.
							if part.Text != "" && !part.Thought && len(part.ThoughtSignature) == 0 && part.FunctionCall == nil && part.InlineData == nil {
								text.WriteString(part.Text)
							} else {
								flushText()
								parts = append(parts, part)
							}
							if part.Text != "" && !part.Thought && part.FunctionCall == nil && part.InlineData == nil {
								partial = append(partial, part)
							}
						}
					}
					if candidate.FinishReason != "" {
						next := genai.FinishReason(candidate.FinishReason)
						if !validStreamFinish(next) || (finish != "" && finish != next) {
							yield(nil, fmt.Errorf("invalid vertex stream finishReason"))
							return
						}
						finish = next
					}
					if len(partial) > 0 && !yield(&model.LLMResponse{
						Content: &genai.Content{Role: role, Parts: partial}, Partial: true,
					}, nil) {
						return
					}
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		if finish == "" {
			yield(nil, fmt.Errorf("vertex stream ended without finishReason: %w", io.ErrUnexpectedEOF))
			return
		}
		switch finish {
		case genai.FinishReasonStop:
			// Only STOP certifies successful completion for text and tools alike.
		case genai.FinishReasonMaxTokens:
			yield(nil, fmt.Errorf("vertex stream truncated: finishReason MAX_TOKENS"))
			return
		default:
			yield(nil, fmt.Errorf("vertex stream unsuccessful finishReason %q", finish))
			return
		}
		flushText()
		if len(parts) > 0 || usage != nil {
			yield(&model.LLMResponse{Content: &genai.Content{Role: role, Parts: parts}, UsageMetadata: usage, FinishReason: finish, TurnComplete: finish == genai.FinishReasonStop}, nil)
		}
	}
}

// validStreamFinish recognizes terminal markers, not successful completion.
// Filtered and text-limit finishes still produce errors after partial text is
// emitted; only STOP permits the final aggregate (including tools) to be committed.
// Unspecified, continuation, malformed calls and unknown reasons are rejected.
func validStreamFinish(reason genai.FinishReason) bool {
	switch reason {
	case genai.FinishReasonStop, genai.FinishReasonMaxTokens,
		genai.FinishReasonSafety, genai.FinishReasonRecitation,
		genai.FinishReasonLanguage, genai.FinishReasonOther,
		genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent,
		genai.FinishReasonSPII, genai.FinishReasonImageSafety,
		genai.FinishReasonImageProhibitedContent, genai.FinishReasonNoImage,
		genai.FinishReasonImageRecitation, genai.FinishReasonImageOther:
		return true
	default:
		return false
	}
}
