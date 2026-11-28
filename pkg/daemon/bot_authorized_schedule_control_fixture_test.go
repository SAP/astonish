package daemon

import (
	"testing"

	"google.golang.org/adk/model"
)

func TestAuthorizedSchedulePausedControlUsesNativeCapability(t *testing.T) {
	m := &authorizedScheduleHTTPModel{controlOnly: true, chatOnly: true, control: map[string]any{"operation": "goal_create"}}
	r := &model.LLMRequest{Tools: map[string]any{"employee_chat": struct{}{}, "propose_bot_control": struct{}{}}}
	for response, err := range m.GenerateContent(t.Context(), r, false) {
		if err != nil || response == nil {
			t.Fatalf("native control: %v", err)
		}
		call := response.Content.Parts[0].FunctionCall
		if call == nil || call.Name != "propose_bot_control" || m.employeeStarted {
			t.Fatalf("paused control dispatched worker: %+v", call)
		}
	}
	for response, err := range m.GenerateContent(t.Context(), r, false) {
		if err != nil || response.Content.Parts[0].FunctionCall != nil {
			t.Fatalf("control acknowledgment retried work: %+v %v", response, err)
		}
	}
	m.control = map[string]any{"operation": "goal_create"}
	delete(r.Tools, "propose_bot_control")
	for response, err := range m.GenerateContent(t.Context(), r, false) {
		if err == nil || response != nil || m.control == nil {
			t.Fatalf("missing native capability acquired authority: %+v %v", response, err)
		}
	}
}
