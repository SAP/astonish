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
			part := Part{}
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
			if part.Text == "" && part.InlineData == nil && part.FunctionCall == nil && part.FunctionResponse == nil {
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
	part := &genai.Part{}
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
	if part.Text == "" && part.FunctionCall == nil && part.InlineData == nil {
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
	}, nil
}

// ParseStream parses a Vertex AI SSE stream and yields ADK LLMResponses.
// Streaming text chunks are yielded with Partial=true for live display.
// At stream end, one aggregated non-partial text response is emitted for
// session persistence.
func ParseStream(reader io.Reader) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		bufReader := bufio.NewReader(reader)
		var textAccum strings.Builder
		var usageMetadata *genai.GenerateContentResponseUsageMetadata

		for {
			line, err := bufReader.ReadBytes('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				yield(nil, err)
				return
			}

			// Skip empty lines
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}

			// SSE format: "data: {...}"
			if bytes.HasPrefix(line, []byte("data: ")) {
				data := bytes.TrimPrefix(line, []byte("data: "))

				// Parse the JSON chunk
				var vertexResp Response
				if err := json.Unmarshal(data, &vertexResp); err != nil {
					// Skip malformed chunks
					continue
				}
				if vertexResp.UsageMetadata != nil {
					usageMetadata = vertexResp.UsageMetadata
				}

				if len(vertexResp.Candidates) > 0 {
					candidate := vertexResp.Candidates[0]
					if candidate.Content != nil {
						var parts []*genai.Part
						hasText := false
						hasFunctionCall := false
						hasInlineData := false
						for _, p := range candidate.Content.Parts {
							part := mapPart(p)
							if part == nil {
								continue
							}
							if part.Text != "" {
								hasText = true
							}
							if part.FunctionCall != nil {
								hasFunctionCall = true
							}
							if part.InlineData != nil {
								hasInlineData = true
							}
							parts = append(parts, part)
						}

						if len(parts) > 0 {
							resp := &model.LLMResponse{
								Content: &genai.Content{
									Role:  candidate.Content.Role,
									Parts: parts,
								},
							}

							if hasText && !hasFunctionCall && !hasInlineData {
								// Pure text chunk — accumulate and mark partial
								for _, p := range parts {
									if p.Text != "" {
										textAccum.WriteString(p.Text)
									}
								}
								resp.Partial = true
							} else if (hasFunctionCall || hasInlineData) && textAccum.Len() > 0 {
								// Non-text content after text — emit aggregated text first
								if !yield(&model.LLMResponse{
									Content: &genai.Content{
										Role:  candidate.Content.Role,
										Parts: []*genai.Part{{Text: textAccum.String()}},
									},
								}, nil) {
									return
								}
								textAccum.Reset()
							}

							if !yield(resp, nil) {
								return
							}
						}
					}
				}
			}
		}

		// Emit aggregated text response at stream end.
		if textAccum.Len() > 0 {
			yield(&model.LLMResponse{
				Content: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: textAccum.String()}},
				},
				UsageMetadata: usageMetadata,
			}, nil)
		} else if usageMetadata != nil {
			yield(&model.LLMResponse{
				Content:       &genai.Content{Role: "model"},
				UsageMetadata: usageMetadata,
			}, nil)
		}
	}
}
