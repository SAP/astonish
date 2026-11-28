package daemon

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync"
	"testing"

	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// daemonEmployeeFixture routes legacy controlled worker scripts through the real
// Employee service. Private execution and its responses remain runtime-owned.
type daemonEmployeeFixture struct {
	model.LLM
	mu         sync.Mutex
	dispatched bool
}

func mediateDaemonModel(inner model.LLM) model.LLM { return &daemonEmployeeFixture{LLM: inner} }
func fixtureDeclarations(r *model.LLMRequest) map[string]bool {
	names := map[string]bool{}
	for name := range r.Tools {
		names[name] = true
	}
	if r.Config != nil {
		for _, ts := range r.Config.Tools {
			for _, d := range ts.FunctionDeclarations {
				names[d.Name] = true
			}
		}
	}
	return names
}
func fixturePrivateText(r *model.LLMRequest) string {
	text := ""
	for _, c := range r.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == "employee_chat" {
				payload := p.FunctionResponse.Response
				if nested, ok := payload["result"]; ok {
					raw, _ := json.Marshal(nested)
					var decoded map[string]any
					if json.Unmarshal(raw, &decoded) == nil {
						payload = decoded
					}
				}
				if actual, ok := payload["private_text"].(string); ok {
					text = actual
				}
			}
		}
	}
	return text
}
func (m *daemonEmployeeFixture) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		names := fixtureDeclarations(r)
		if names["employee_chat"] {
			m.mu.Lock()
			done := m.dispatched
			m.dispatched = true
			m.mu.Unlock()
			if !done {
				yield(configuredFixtureCall("employee_chat", map[string]any{"message": "Perform the requested scoped work and return the actual result and evidence."}), nil)
				return
			}
			text := fixturePrivateText(r)
			yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
			return
		}
		if r.Config != nil && r.Config.SystemInstruction != nil {
			raw, _ := json.Marshal(r.Config.SystemInstruction)
			if strings.Contains(string(raw), "BOT_EMPLOYEE_PRESENTATION_V1") {
				var facts struct {
					SourceDigest string               `json:"source_digest"`
					Operations   []store.BotOperation `json:"operations"`
				}
				for _, c := range r.Contents {
					for _, p := range c.Parts {
						_ = json.Unmarshal([]byte(p.Text), &facts)
					}
				}
				text := "No schedule application is verified."
				for _, op := range facts.Operations {
					text = fmt.Sprintf("The %s request is %s.", op.Payload.Operation, op.Status)
					if op.Status == store.BotOperationApplied && op.Receipt != nil && op.Receipt.Schedule != nil {
						text = fmt.Sprintf("The schedule request is applied; verified enabled %t, removed %t.", op.Receipt.Schedule.Enabled, op.Receipt.Schedule.Removed)
					}
				}
				encoded, _ := json.Marshal(map[string]any{"source_digest": facts.SourceDigest, "text": text})
				yield(&model.LLMResponse{Content: genai.NewContentFromText(string(encoded), "model"), TurnComplete: true}, nil)
				return
			}
			if strings.Contains(string(raw), "BOT_EMPLOYEE_REPAIR_V1") {
				text := fixturePrivateText(r)
				raw, _ = json.Marshal(map[string]any{"text": text})
				yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
				return
			}
		}
		for response, err := range m.LLM.GenerateContent(ctx, r, stream) {
			if !yield(response, err) {
				return
			}
		}
	}
}
func TestDaemonEmployeeFixtureRoutesActualPrivateResult(t *testing.T) {
	m := mediateDaemonModel(&workerTestModel{})
	owner := &model.LLMRequest{Tools: map[string]any{"employee_chat": struct{}{}}}
	for r, err := range m.GenerateContent(t.Context(), owner, false) {
		if err != nil || r.Content.Parts[0].FunctionCall.Name != "employee_chat" {
			t.Fatal("missing genuine service dispatch")
		}
	}
	owner.Contents = []*genai.Content{{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "employee_chat", Response: map[string]any{"private_text": "actual denied source"}}}}}}
	for r, err := range m.GenerateContent(t.Context(), owner, false) {
		if err != nil || r.Content.Parts[0].Text != "actual denied source" {
			t.Fatal("private result replaced")
		}
	}
}
