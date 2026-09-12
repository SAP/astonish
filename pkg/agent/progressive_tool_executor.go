package agent

import (
	"fmt"

	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

// ProgressiveResultState is a stable machine-readable outcome for callers that
// drive Astonish tools outside the native ChatAgent loop.
type ProgressiveResultState string

const (
	ProgressiveStateReady               ProgressiveResultState = "ready"
	ProgressiveStateContextRequired     ProgressiveResultState = "context_required"
	ProgressiveStateContextStale        ProgressiveResultState = "context_stale"
	ProgressiveStateAuthorizationDenied ProgressiveResultState = "authorization_denied"
	ProgressiveStateApprovalRequired    ProgressiveResultState = "approval_required"
	ProgressiveStateExecutionError      ProgressiveResultState = "execution_error"
)

// ProgressiveToolResult is the protected result of one logical tool invocation.
type ProgressiveToolResult struct {
	State  ProgressiveResultState `json:"state"`
	Tool   string                 `json:"tool,omitempty"`
	Output map[string]any         `json:"output,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// ProgressiveToolExecutor executes a logical tool through the same resolver and
// security context used by the native progressive bridge. It deliberately calls
// the resolved runnable instance, retaining sandbox and request wrappers.
type ProgressiveToolExecutor struct {
	Agent    *ChatAgent
	Resolver deferredToolResolver
}

// NewProgressiveToolExecutor creates an executor that preserves first-party
// precedence and request-scoped MCP/A2A tool lookup.
func NewProgressiveToolExecutor(chat *ChatAgent) *ProgressiveToolExecutor {
	var index *ToolIndex
	if chat != nil {
		index = chat.ToolIndex
	}
	return &ProgressiveToolExecutor{Agent: chat, Resolver: deferredToolResolver{index: index}}
}

// Execute resolves, authorizes, substitutes credentials, invokes, restores, and
// redacts a single tool call. Approval policy remains represented by the native
// tool wrappers; callers receive their structured output unchanged.
func (e *ProgressiveToolExecutor) Execute(ctx tool.Context, name string, args map[string]any) ProgressiveToolResult {
	if e == nil || e.Agent == nil {
		return ProgressiveToolResult{State: ProgressiveStateExecutionError, Error: "progressive executor is not configured"}
	}
	resolved, _, err := e.Resolver.resolve(ctx, name)
	if err != nil {
		return ProgressiveToolResult{State: ProgressiveStateExecutionError, Error: err.Error()}
	}
	if isToolDisabled(ctx, resolved.Name()) {
		return ProgressiveToolResult{State: ProgressiveStateExecutionError, Tool: resolved.Name(), Error: fmt.Sprintf("tool %q is disabled", resolved.Name())}
	}
	if principal, ok := execution.PrincipalFromContext(ctx); ok {
		if err := (execution.CapabilityAuthorizer{}).Authorize(principal, execution.CapabilityToolExecute); err != nil {
			return ProgressiveToolResult{State: ProgressiveStateAuthorizationDenied, Tool: resolved.Name(), Error: err.Error()}
		}
	}
	runner, ok := resolved.(runnableDeferredTool)
	if !ok {
		return ProgressiveToolResult{State: ProgressiveStateExecutionError, Tool: resolved.Name(), Error: fmt.Sprintf("tool %q is not executable", resolved.Name())}
	}
	if args == nil {
		args = map[string]any{}
	}

	var restores []func()
	defer func() {
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
	}()
	var resolver credentials.CredentialResolver
	if cs := store.CredentialStoreFromContext(ctx); cs != nil {
		resolver = credentials.NewStoreAdapter(cs)
	} else {
		resolver = e.Agent.CredentialStore
	}
	if resolver != nil {
		credentials.RegisterResolvedWithRedactor(args, resolver, e.Agent.Redactor)
		var shellFields []string
		if resolved.Name() == "shell_command" || resolved.Name() == "process_write" {
			shellFields = []string{"command"}
		}
		restores = append(restores, credentials.SubstituteAndRestore(args, resolver, shellFields...))
	}
	if e.Agent.PendingSecrets != nil {
		restores = append(restores, e.Agent.PendingSecrets.SubstituteAndRestore(args))
	}

	output, err := runner.Run(ctx, args)
	output = e.Agent.finalizeProgressiveToolOutput(resolved, args, output, err)
	if err != nil {
		return ProgressiveToolResult{State: ProgressiveStateExecutionError, Tool: resolved.Name(), Output: output, Error: err.Error()}
	}
	if status, _ := output["status"].(string); status == "pending_authorization" || status == "approval_required" {
		return ProgressiveToolResult{State: ProgressiveStateApprovalRequired, Tool: resolved.Name(), Output: output}
	}
	return ProgressiveToolResult{State: ProgressiveStateReady, Tool: resolved.Name(), Output: output}
}

// finalizeProgressiveToolOutput applies the output protections shared by direct
// MCP execution and native chat's AfterToolCallback.
func (c *ChatAgent) finalizeProgressiveToolOutput(t tool.Tool, input, output map[string]any, err error) map[string]any {
	if c == nil || t == nil {
		return output
	}
	redacted := output
	if c.Redactor != nil && output != nil {
		redacted = c.Redactor.RedactMap(output)
	}
	redacted = c.extractAndStripImages(redacted)
	if err == nil {
		switch t.Name() {
		case "write_file":
			if path, ok := input["file_path"].(string); ok && path != "" {
				c.CaptureFileArtifact(resolveAbsPath(path), t.Name())
			}
		case "edit_file":
			if path, ok := input["path"].(string); ok && path != "" {
				c.CaptureFileArtifact(resolveAbsPath(path), t.Name())
			}
		case "browser_stop_recording":
			if path, ok := redacted["path"].(string); ok && path != "" {
				c.CaptureFileArtifact(resolveAbsPath(path), t.Name())
			}
		case "run_drill":
			captureRunDrillArtifacts(c.CaptureFileArtifact, redacted)
		}
	}
	if t.Name() == "run_flow" && redacted != nil {
		redacted = c.extractAndStripFlowOutput(redacted)
	}
	return redacted
}
