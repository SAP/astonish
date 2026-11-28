package api_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// The progress fixture follows the actual Employee/private Chat split. Only
// private Chat consumes the source script; tool-free progress and presentation
// requests retain their own protocols. No source response is manufactured.
type progressEmployeeModel struct {
	model.LLM
	started bool
	final   string
}

func progressEmployeeFixture(llm model.LLM) model.LLM { return &progressEmployeeModel{LLM: llm} }

func (m *progressEmployeeModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		prompt := ""
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				prompt += p.Text
			}
		}
		employee := strings.Contains(prompt, "You are the Employee and the only author")
		if req.Config != nil {
			for _, ts := range req.Config.Tools {
				for _, d := range ts.FunctionDeclarations {
					if d.Name == "employee_chat" {
						employee = true
					}
				}
			}
		}
		if strings.Contains(prompt, "BOT_EMPLOYEE_PRESENTATION_V1") {
			var facts struct {
				Digest string `json:"source_digest"`
			}
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					_ = json.Unmarshal([]byte(p.Text), &facts)
				}
			}
			raw, _ := json.Marshal(map[string]string{"source_digest": facts.Digest, "text": m.final})
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
			return
		}
		if employee {
			if !m.started {
				m.started = true
				yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "progress-private", Name: "employee_chat", Args: map[string]any{"message": "Carry out the original assignment using the scoped source tools and saved evidence."}}}}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
			} else {
				yield(&model.LLMResponse{Content: genai.NewContentFromText(m.final, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
			}
			return
		}
		rawRequest, _ := json.Marshal(req.Contents)
		for response, err := range m.LLM.GenerateContent(ctx, req, stream) {
			if !strings.Contains(prompt, "BOT_EMPLOYEE_PROGRESS_V1") && response != nil && response.Content != nil {
				for _, p := range response.Content.Parts {
					if p.Text != "" && !p.Thought && response.FinishReason != genai.FinishReasonMaxTokens && (!strings.Contains(string(rawRequest), "Synthesis reserve has begun") || response.TurnComplete || response.FinishReason == genai.FinishReasonStop) {
						m.final = p.Text
					}
				}
			}
			if !yield(response, err) {
				return
			}
		}
	}
}
