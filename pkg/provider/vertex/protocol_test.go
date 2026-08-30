package vertex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// ---------- ConvertRequest ----------

func TestConvertRequest_BasicMessage(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					{Text: "Hello, world!"},
				},
			},
		},
	}

	vReq, err := ConvertRequest(req, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(vReq.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(vReq.Contents))
	}
	if vReq.Contents[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", vReq.Contents[0].Role)
	}
	if vReq.Contents[0].Parts[0].Text != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", vReq.Contents[0].Parts[0].Text)
	}
}

func TestConvertRequest_DefaultMaxOutputTokens(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{}

	vReq, err := ConvertRequest(req, 0)
	if err != nil {
		t.Fatal(err)
	}

	if vReq.GenerationConfig == nil {
		t.Fatal("expected GenerationConfig to be set")
	}
	if vReq.GenerationConfig.MaxOutputTokens != 8192 {
		t.Errorf("expected default 8192, got %d", vReq.GenerationConfig.MaxOutputTokens)
	}
}

func TestConvertRequest_CustomMaxOutputTokens(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{}

	vReq, err := ConvertRequest(req, 65536)
	if err != nil {
		t.Fatal(err)
	}

	if vReq.GenerationConfig.MaxOutputTokens != 65536 {
		t.Errorf("expected 65536, got %d", vReq.GenerationConfig.MaxOutputTokens)
	}
}

func TestConvertRequest_NegativeMaxOutputTokens(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{}

	vReq, err := ConvertRequest(req, -1)
	if err != nil {
		t.Fatal(err)
	}

	if vReq.GenerationConfig.MaxOutputTokens != 8192 {
		t.Errorf("expected fallback 8192 for negative, got %d", vReq.GenerationConfig.MaxOutputTokens)
	}
}

func TestConvertRequest_SystemInstruction(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{
					{Text: "You are a helpful assistant."},
				},
			},
		},
	}

	vReq, err := ConvertRequest(req, 4096)
	if err != nil {
		t.Fatal(err)
	}

	if vReq.SystemInstruction == nil {
		t.Fatal("expected SystemInstruction to be set")
	}
	if len(vReq.SystemInstruction.Parts) != 1 {
		t.Fatalf("expected 1 system part, got %d", len(vReq.SystemInstruction.Parts))
	}
	if vReq.SystemInstruction.Parts[0].Text != "You are a helpful assistant." {
		t.Errorf("unexpected system text: %q", vReq.SystemInstruction.Parts[0].Text)
	}
}

func TestConvertRequest_FunctionCall(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "model",
				Parts: []*genai.Part{
					{
						FunctionCall: &genai.FunctionCall{
							Name: "get_weather",
							Args: map[string]any{"city": "Berlin"},
						},
					},
				},
			},
		},
	}

	vReq, err := ConvertRequest(req, 4096)
	if err != nil {
		t.Fatal(err)
	}

	if len(vReq.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(vReq.Contents))
	}
	p := vReq.Contents[0].Parts[0]
	if p.FunctionCall == nil {
		t.Fatal("expected FunctionCall to be set")
	}
	if p.FunctionCall.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", p.FunctionCall.Name)
	}
	if p.FunctionCall.Args["city"] != "Berlin" {
		t.Errorf("expected city=Berlin, got %v", p.FunctionCall.Args["city"])
	}
}

func TestConvertRequest_FunctionResponse(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							Name:     "get_weather",
							Response: map[string]any{"temp": 22.5},
						},
					},
				},
			},
		},
	}

	vReq, err := ConvertRequest(req, 4096)
	if err != nil {
		t.Fatal(err)
	}

	p := vReq.Contents[0].Parts[0]
	if p.FunctionResponse == nil {
		t.Fatal("expected FunctionResponse")
	}
	if p.FunctionResponse.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", p.FunctionResponse.Name)
	}
}

func TestConvertRequest_Tools(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "search",
							Description: "Search the web",
							ParametersJsonSchema: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"query": map[string]any{"type": "string"},
								},
								"$schema":              "http://json-schema.org/draft-07/schema#",
								"additionalProperties": false,
							},
						},
					},
				},
			},
		},
	}

	vReq, err := ConvertRequest(req, 4096)
	if err != nil {
		t.Fatal(err)
	}

	if len(vReq.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(vReq.Tools))
	}
	fds := vReq.Tools[0].FunctionDeclarations
	if len(fds) != 1 {
		t.Fatalf("expected 1 function declaration, got %d", len(fds))
	}
	if fds[0].Name != "search" {
		t.Errorf("expected 'search', got %q", fds[0].Name)
	}

	// Verify $schema and additionalProperties are stripped
	paramsBytes, _ := json.Marshal(fds[0].Parameters)
	paramsStr := string(paramsBytes)
	if strings.Contains(paramsStr, "$schema") {
		t.Error("expected $schema to be stripped from parameters")
	}
	if strings.Contains(paramsStr, "additionalProperties") {
		t.Error("expected additionalProperties to be stripped from parameters")
	}
	// But "type" and "properties" should remain
	if !strings.Contains(paramsStr, `"type"`) {
		t.Error("expected 'type' to remain in parameters")
	}
}

func TestConvertRequest_ToolConfig(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Config: &genai.GenerateContentConfig{
			ToolConfig: &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode:                 genai.FunctionCallingConfigModeAny,
					AllowedFunctionNames: []string{"search", "browse"},
				},
			},
		},
	}

	vReq, err := ConvertRequest(req, 4096)
	if err != nil {
		t.Fatal(err)
	}

	if vReq.ToolConfig == nil {
		t.Fatal("expected ToolConfig")
	}
	if vReq.ToolConfig.FunctionCallingConfig == nil {
		t.Fatal("expected FunctionCallingConfig")
	}
	if vReq.ToolConfig.FunctionCallingConfig.Mode != string(genai.FunctionCallingConfigModeAny) {
		t.Errorf("expected mode ANY, got %q", vReq.ToolConfig.FunctionCallingConfig.Mode)
	}
	if len(vReq.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) != 2 {
		t.Errorf("expected 2 allowed functions, got %d", len(vReq.ToolConfig.FunctionCallingConfig.AllowedFunctionNames))
	}
}

func TestConvertRequest_MultipleContents(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: "Hi"}}},
			{Role: "model", Parts: []*genai.Part{{Text: "Hello!"}}},
			{Role: "user", Parts: []*genai.Part{{Text: "How are you?"}}},
		},
	}

	vReq, err := ConvertRequest(req, 4096)
	if err != nil {
		t.Fatal(err)
	}

	if len(vReq.Contents) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(vReq.Contents))
	}
	if vReq.Contents[1].Role != "model" {
		t.Errorf("expected role 'model', got %q", vReq.Contents[1].Role)
	}
}

// ---------- ParseResponse ----------

func TestParseResponse_UsageMetadata(t *testing.T) {
	body := `{"candidates":[],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120,"cachedContentTokenCount":75,"thoughtsTokenCount":5,"toolUsePromptTokenCount":3}}`
	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	usage := resp.UsageMetadata
	if usage == nil {
		t.Fatal("UsageMetadata is nil")
	}
	if usage.PromptTokenCount != 100 || usage.CandidatesTokenCount != 20 || usage.TotalTokenCount != 120 ||
		usage.CachedContentTokenCount != 75 || usage.ThoughtsTokenCount != 5 || usage.ToolUsePromptTokenCount != 3 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestParseResponse_UsageMetadataAbsent(t *testing.T) {
	resp, err := ParseResponse([]byte(`{"candidates":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.UsageMetadata != nil {
		t.Fatalf("UsageMetadata = %#v, want nil", resp.UsageMetadata)
	}
}

func TestParseResponse_TextContent(t *testing.T) {
	t.Parallel()
	body := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello from Gemini!"}]
			},
			"finishReason": "STOP"
		}]
	}`

	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	if resp.Content == nil {
		t.Fatal("expected content")
	}
	if resp.Content.Role != "model" {
		t.Errorf("expected role 'model', got %q", resp.Content.Role)
	}
	if len(resp.Content.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(resp.Content.Parts))
	}
	if resp.Content.Parts[0].Text != "Hello from Gemini!" {
		t.Errorf("expected 'Hello from Gemini!', got %q", resp.Content.Parts[0].Text)
	}
}

func TestParseResponse_FunctionCall(t *testing.T) {
	t.Parallel()
	body := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"functionCall": {
						"name": "get_weather",
						"args": {"location": "Tokyo"}
					}
				}]
			}
		}]
	}`

	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	if resp.Content == nil || len(resp.Content.Parts) != 1 {
		t.Fatal("expected 1 part in response")
	}
	fc := resp.Content.Parts[0].FunctionCall
	if fc == nil {
		t.Fatal("expected FunctionCall")
	}
	if fc.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", fc.Name)
	}
	if fc.Args["location"] != "Tokyo" {
		t.Errorf("expected location=Tokyo, got %v", fc.Args["location"])
	}
}

func TestParseResponse_InlineData(t *testing.T) {
	t.Parallel()
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	body := fmt.Sprintf(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"inlineData": {"mimeType": "image/png", "data": %q}}]
			}
		}]
	}`, b64)

	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content == nil || len(resp.Content.Parts) != 1 {
		t.Fatalf("expected 1 part, got %+v", resp.Content)
	}
	id := resp.Content.Parts[0].InlineData
	if id == nil {
		t.Fatal("expected InlineData")
	}
	if id.MIMEType != "image/png" {
		t.Errorf("MIMEType=%q, want image/png", id.MIMEType)
	}
	if !bytes.Equal(id.Data, pngBytes) {
		t.Errorf("data mismatch: got %v", id.Data)
	}
}

func TestParseResponse_SkipsEmptyParts(t *testing.T) {
	t.Parallel()
	body := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{}, {"text": "hi"}]
			}
		}]
	}`
	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content.Parts) != 1 || resp.Content.Parts[0].Text != "hi" {
		t.Fatalf("expected only text part, got %+v", resp.Content.Parts)
	}
}

func TestParseResponse_EmptyCandidates(t *testing.T) {
	t.Parallel()
	body := `{"candidates": []}`

	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	// Should return empty response, not error
	if resp.Content != nil {
		t.Errorf("expected nil content for empty candidates, got %+v", resp.Content)
	}
}

func TestParseResponse_NilContent(t *testing.T) {
	t.Parallel()
	body := `{"candidates": [{"finishReason": "SAFETY"}]}`

	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != nil {
		t.Errorf("expected nil content, got %+v", resp.Content)
	}
}

func TestParseResponse_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := ParseResponse([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseResponse_MultipleParts(t *testing.T) {
	t.Parallel()
	body := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"text": "I'll search for that."},
					{"functionCall": {"name": "search", "args": {"q": "test"}}}
				]
			}
		}]
	}`

	resp, err := ParseResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	if len(resp.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(resp.Content.Parts))
	}
	if resp.Content.Parts[0].Text != "I'll search for that." {
		t.Errorf("unexpected text: %q", resp.Content.Parts[0].Text)
	}
	if resp.Content.Parts[1].FunctionCall == nil {
		t.Error("expected FunctionCall in second part")
	}
}

// ---------- ParseStream ----------

func TestParseStream_UsageMetadata(t *testing.T) {
	reader := strings.NewReader("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":2,\"totalTokenCount\":102,\"cachedContentTokenCount\":75}}\n\n")
	var final *model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		if !resp.Partial {
			final = resp
		}
	}
	if final == nil || final.UsageMetadata == nil {
		t.Fatal("final response has no usage metadata")
	}
	usage := final.UsageMetadata
	if usage.PromptTokenCount != 100 || usage.CandidatesTokenCount != 2 || usage.TotalTokenCount != 102 || usage.CachedContentTokenCount != 75 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestParseStream_UsageOnlyChunk(t *testing.T) {
	reader := strings.NewReader("data: {\"usageMetadata\":{\"promptTokenCount\":10,\"totalTokenCount\":10,\"cachedContentTokenCount\":4}}\n\n")
	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, resp)
	}
	if len(responses) != 1 || responses[0].UsageMetadata == nil {
		t.Fatalf("responses = %#v", responses)
	}
	if responses[0].UsageMetadata.CachedContentTokenCount != 4 {
		t.Fatalf("CachedContentTokenCount = %d, want 4", responses[0].UsageMetadata.CachedContentTokenCount)
	}
}

func TestParseStream_SingleChunk(t *testing.T) {
	t.Parallel()
	input := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}

`
	reader := strings.NewReader(input)

	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	// 1 partial chunk + 1 aggregated final
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses (partial + aggregated), got %d", len(responses))
	}
	if !responses[0].Partial {
		t.Error("expected first response to be Partial")
	}
	if responses[1].Partial {
		t.Error("expected final response to not be Partial")
	}
	if responses[1].Content.Parts[0].Text != "Hello" {
		t.Errorf("expected 'Hello', got %q", responses[1].Content.Parts[0].Text)
	}
}

func TestParseStream_MultipleChunks(t *testing.T) {
	t.Parallel()
	input := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":" World"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]}}]}

`
	reader := strings.NewReader(input)

	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	// 3 partial chunks + 1 aggregated final
	if len(responses) != 4 {
		t.Fatalf("expected 4 responses (3 partial + 1 aggregated), got %d", len(responses))
	}
	// Check partials
	for i := 0; i < 3; i++ {
		if !responses[i].Partial {
			t.Errorf("expected response[%d] to be Partial", i)
		}
	}
	// Check final aggregated
	if responses[3].Partial {
		t.Error("expected final response to not be Partial")
	}
	if responses[3].Content.Parts[0].Text != "Hello World!" {
		t.Errorf("expected 'Hello World!', got %q", responses[3].Content.Parts[0].Text)
	}
}

func TestParseStream_InlineDataOnly(t *testing.T) {
	t.Parallel()
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	input := fmt.Sprintf("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":%q}}]}}]}\n\n", b64)
	reader := strings.NewReader(input)

	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}
	if len(responses) != 1 {
		t.Fatalf("expected 1 response for image-only chunk, got %d", len(responses))
	}
	if responses[0].Partial {
		t.Error("image-only response should not be Partial")
	}
	if responses[0].Content == nil || len(responses[0].Content.Parts) != 1 {
		t.Fatalf("expected 1 part, got %+v", responses[0].Content)
	}
	id := responses[0].Content.Parts[0].InlineData
	if id == nil || !bytes.Equal(id.Data, pngBytes) {
		t.Fatalf("expected inline image bytes, got %+v", id)
	}
}

func TestParseStream_EmptyCandidates(t *testing.T) {
	t.Parallel()
	input := `data: {"candidates":[]}

`
	reader := strings.NewReader(input)

	var count int
	for _, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 responses for empty candidates, got %d", count)
	}
}

func TestParseStream_MalformedChunkSkipped(t *testing.T) {
	t.Parallel()
	input := `data: not-valid-json

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]}}]}

`
	reader := strings.NewReader(input)

	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		responses = append(responses, resp)
	}

	// 1 partial + 1 aggregated final (malformed chunk skipped)
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses (malformed skipped, partial + aggregated), got %d", len(responses))
	}
	if responses[1].Content.Parts[0].Text != "OK" {
		t.Errorf("expected 'OK', got %q", responses[1].Content.Parts[0].Text)
	}
}

func TestParseStream_NonDataLinesIgnored(t *testing.T) {
	t.Parallel()
	input := `: comment line
event: message
data: {"candidates":[{"content":{"role":"model","parts":[{"text":"data"}]}}]}

`
	reader := strings.NewReader(input)

	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, resp)
	}

	// 1 partial + 1 aggregated final
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses (partial + aggregated), got %d", len(responses))
	}
}

func TestParseStream_FunctionCallInStream(t *testing.T) {
	t.Parallel()
	input := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{"q":"test"}}}]}}]}

`
	reader := strings.NewReader(input)

	var responses []*model.LLMResponse
	for resp, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, resp)
	}

	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	fc := responses[0].Content.Parts[0].FunctionCall
	if fc == nil {
		t.Fatal("expected FunctionCall")
	}
	if fc.Name != "search" {
		t.Errorf("expected 'search', got %q", fc.Name)
	}
}

func TestParseStream_EmptyInput(t *testing.T) {
	t.Parallel()
	reader := strings.NewReader("")

	var count int
	for _, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 responses for empty input, got %d", count)
	}
}

func TestParseStream_NilContentInCandidate(t *testing.T) {
	t.Parallel()
	input := `data: {"candidates":[{"finishReason":"STOP"}]}

`
	reader := strings.NewReader(input)

	var count int
	for _, err := range ParseStream(reader) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	// Nil content candidates should not yield a response
	if count != 0 {
		t.Errorf("expected 0 responses for nil content, got %d", count)
	}
}
