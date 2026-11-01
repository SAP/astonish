package common

import (
	"encoding/json"
	"fmt"
	"strings"
)

const ExecuteToolName = "execute_tool"
const DescribeToolsName = "describe_tools"
const SearchToolsName = "search_tools"

// NormalizeExecuteToolArgs is shared by execution, authorization and event
// projection. Recovery is bounded to one wrapper and never crosses discovery.
// Returned arguments retain their identity for credential substitution callbacks.
func NormalizeExecuteToolArgs(raw any) (string, map[string]any, error) {
	envelope, ok := raw.(map[string]any)
	if !ok {
		return "", nil, executeEnvelopeError()
	}
	name, args, err := decodeExecutionEnvelope(envelope)
	if err != nil {
		return "", nil, err
	}
	if name == "" || name == ExecuteToolName {
		name, args, err = decodeExecutionEnvelope(args)
		if err != nil {
			return "", nil, err
		}
	}
	if name == "" || name == ExecuteToolName {
		return "", nil, executeEnvelopeError()
	}
	if name == DescribeToolsName || name == SearchToolsName {
		return "", nil, fmt.Errorf("call %s directly, not through execute_tool; execute_tool requires a tool name and arguments", name)
	}
	return name, args, nil
}

func executeEnvelopeError() error {
	return fmt.Errorf("execute_tool requires a nonempty string name and an explicit arguments object; only one execution wrapper is supported")
}

func decodeExecutionEnvelope(raw map[string]any) (string, map[string]any, error) {
	name := ""
	if value, exists := raw["name"]; exists {
		var ok bool
		name, ok = value.(string)
		if !ok {
			return "", nil, executeEnvelopeError()
		}
		name = strings.TrimSpace(name)
	}
	var args map[string]any
	switch value := raw["arguments"].(type) {
	case map[string]any:
		args = value
	case string:
		if json.Unmarshal([]byte(value), &args) != nil {
			return "", nil, executeEnvelopeError()
		}
	default:
		return "", nil, executeEnvelopeError()
	}
	if args == nil {
		return "", nil, executeEnvelopeError()
	}
	return name, args, nil
}

// ToolResultSucceeded gates artifacts on completed executions, not approvals or
// absent responses. Legacy successful tools need not supply a status field.
func ToolResultSucceeded(result map[string]any, err error) bool {
	if err != nil || result == nil {
		return false
	}
	if _, exists := result["error"]; exists {
		return false
	}
	if success, ok := result["success"].(bool); ok && !success {
		return false
	}
	if status, ok := result["status"].(string); ok {
		switch status {
		case "pending_approval", "approval_required", "pending_authorization", "error", "failed", "failure", "denied", "cancelled", "canceled":
			return false
		}
	}
	return true
}
