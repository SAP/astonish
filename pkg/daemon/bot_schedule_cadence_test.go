package daemon

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Controlled provider decisions, real HTTP admission/factory/runner/tools/worker.
// The update ID is learned from the actual list response, never from the fixture.
type scheduleCadenceModel struct {
	t       *testing.T
	step    int
	pending func(string)
	bridge  bool
}

func (m *scheduleCadenceModel) Name() string { return "cadence-regression" }
func (m *scheduleCadenceModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var call *genai.FunctionCall
		switch m.step {
		case 0:
			call = &genai.FunctionCall{ID: "cadence-list", Name: "list_scheduled_jobs", Args: map[string]any{"filter": "daily report"}}
		case 1:
			response := cadenceResponse(r, "cadence-list")
			jobs, _ := response["jobs"].([]any)
			if len(jobs) != 1 {
				m.t.Errorf("filtered list must find persisted instructions despite generated job name: %v", response)
				yield(nil, fmt.Errorf("cadence listing missing"))
				return
			}
			row := jobs[0].(map[string]any)
			call = &genai.FunctionCall{ID: "cadence-update", Name: "update_scheduled_job", Args: map[string]any{"job_id": row["id"], "schedule": "*/2 * * * *"}}
		case 2:
			response := cadenceResponse(r, "cadence-update")
			id, _ := response["operation_id"].(string)
			if response["status"] != string(store.BotOperationPending) || id == "" {
				m.t.Errorf("not durable pending: %v", response)
				yield(nil, fmt.Errorf("missing pending operation"))
				return
			}
			m.pending(id)
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Done, cadence changed.", "model"), TurnComplete: true}, nil)
			m.step++
			return
		default:
			yield(nil, fmt.Errorf("unexpected cadence retry"))
			return
		}
		if m.bridge {
			call = &genai.FunctionCall{ID: call.ID, Name: "execute_tool", Args: map[string]any{"name": call.Name, "arguments": call.Args}}
		}
		m.step++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}
func cadenceResponse(r *model.LLMRequest, id string) map[string]any {
	for _, c := range r.Contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.ID == id {
				return p.FunctionResponse.Response
			}
		}
	}
	return nil
}
