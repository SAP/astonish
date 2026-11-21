package agent

import (
	"encoding/json"
	"strings"

	"google.golang.org/adk/session"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/question"
)

// codePlanInputRequired observes structured interaction results, never narration.
// calls belongs to one invocation and must be retained between sequential events.
// Merely requesting a tool is not a boundary: failed questions can be repaired.
func codePlanInputRequired(event *session.Event, calls map[string]string) bool {
	if event == nil || event.Content == nil {
		return false
	}
	// Register every call first, including when calls and results share an event.
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionCall == nil || calls == nil {
			continue
		}
		call := part.FunctionCall
		name := call.Name
		if name == common.ExecuteToolName {
			name, _, _ = common.NormalizeExecuteToolArgs(call.Args)
			// Also recognize a tool_name event envelope for observation only;
			// never mutate the provider event or change dispatcher acceptance.
			if name == "" && call.Args["name"] == nil {
				if alias, ok := call.Args["tool_name"].(string); ok {
					name, _, _ = common.NormalizeExecuteToolArgs(map[string]any{
						"name": alias, "arguments": call.Args["arguments"],
					})
				}
			}
		}
		if call.ID != "" && name != "" {
			calls[call.ID] = name
		}
	}
	inputRequired := false
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		response := part.FunctionResponse
		name := response.Name
		if target := calls[response.ID]; target != "" {
			name = target
			delete(calls, response.ID)
		}
		if _, toolName, ok := parseMCPToolRef(name); ok && toolName != "" {
			name = toolName
		}
		if codePlanResponseRequiresInput(name, response.Response) {
			inputRequired = true
		}
	}
	return inputRequired
}

func codePlanResponseRequiresInput(name string, result map[string]any) bool {
	if result == nil {
		return false
	}
	if _, failed := result["error"]; failed {
		return false
	}
	if success, ok := result["success"].(bool); ok && !success {
		return false
	}
	status, _ := result["status"].(string)
	switch status {
	case "error", "failed", "failure", "denied", "cancelled", "canceled":
		return false
	}
	for _, key := range []string{"awaiting_approval", "requires_user_input", "awaiting_user_input", "human_handoff"} {
		if yes, _ := result[key].(bool); yes {
			return true
		}
	}
	switch status {
	case "pending_approval", "approval_required", "pending_authorization":
		return true
	}
	switch name {
	case "ask_user":
		// askUser returns status=ok and a validated question, not a boolean
		// awaiting-input flag (pkg/docs/slides/tools.go, AskUserResult).
		var q question.Question
		data, err := json.Marshal(result)
		return status == "ok" && err == nil && json.Unmarshal(data, &q) == nil &&
			strings.TrimSpace(q.QuestionID) != "" && q.Validate() == nil
	case "browser_request_human":
		// BrowserRequestHumanResult explicitly reports successful handoff setup.
		success, _ := result["success"].(bool)
		return success
	default:
		return false
	}
}
