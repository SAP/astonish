package api

import (
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Parallel workers may start in the reverse of the persisted function-call order.
// Their scheduling must not become a second source of transcript chronology.
func TestChatDelegationCallOrderHTTP(t *testing.T) {
	var emit func(agent.SubTaskProgressEvent)
	var once sync.Once
	delegate, err := functiontool.New(functiontool.Config{Name: "delegate_tasks", Description: "Controlled delegation"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
		once.Do(func() {
			for _, id := range []string{"beta", "alpha"} {
				emit(agent.SubTaskProgressEvent{Type: string(agent.DelegationStart), BatchID: id, Sequence: 1, Tasks: []agent.SubTaskInfo{{Name: "same", Description: id}}})
			}
		})
		return map[string]any{"status": "success"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := ToolCallTurn("delegate_tasks", map[string]any{})
	calls.Parts[0].FunctionCall.ID = "alpha"
	calls.Parts = append(calls.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: "beta", Name: "delegate_tasks", Args: map[string]any{}}})
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("Ready."), calls, TextTurn("Finished.")), []tool.Tool{delegate})
	var sessionID string
	sessionID = toString(assertHasEvent(t, env.Turn(t, "", "bootstrap", false), "session").Data["sessionId"])
	emit = func(e agent.SubTaskProgressEvent) { env.ChatAgent.EmitSubTaskProgress(sessionID, e) }
	events := env.Turn(t, sessionID, "delegate", false)
	var order []string
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type != "subtask_progress" {
			continue
		}
		id := toString(e.Data["batch_id"])
		if id != "" && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	if len(order) != 2 || order[0] != "alpha" || order[1] != "beta" {
		t.Fatalf("live batch order=%v, want persisted call order [alpha beta]", order)
	}
	detail := env.Detail(t, sessionID)
	var restored []string
	for _, msg := range detail.Messages {
		if msg.Type == "subtask_execution" {
			restored = append(restored, msg.Events[0].BatchID)
		}
	}
	if len(restored) != 2 || restored[0] != order[0] || restored[1] != order[1] {
		t.Fatalf("restored=%v live=%v", restored, order)
	}
}
