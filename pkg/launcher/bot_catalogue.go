package launcher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// BotCatalogueTools reuses Chat's concrete schemas and sandbox implementations.
// scoped contains request-local browser, flow or MCP tools prepared by the trusted
// resource factory; none are discovered from daemon-global configuration. The
// caller MUST wrap this catalogue with newBotBoundTool before exposing it.
func BotCatalogueTools(client sandbox.ToolNodeClient, sessionID string, scoped ...tool.Tool) ([]tool.Tool, error) {
	if client == nil || sessionID == "" {
		return nil, store.ErrBotUnsupported
	}
	native, err := tools.GetIsolatedInternalTools()
	if err != nil {
		return nil, err
	}
	processes, err := tools.GetProcessTools()
	if err != nil {
		return nil, err
	}
	native = append(native, processes...)
	result := make([]tool.Tool, 0, len(native))
	seen := map[string]bool{}
	confined := map[string]bool{}
	for _, candidate := range scoped {
		if _, ok := candidate.(*botConfinedFileTool); ok {
			confined[candidate.Name()] = true
		}
	}
	for _, t := range native {
		if confined[t.Name()] {
			continue
		}
		seen[t.Name()] = true
		// Credential-bearing HTTP needs the session vault provisioning adapter; do
		// not silently run the node's unauthenticated/global credential fallback.
		if t.Name() == "http_request" {
			continue
		}
		result = append(result, &botSandboxTool{Tool: t, client: client, sessionID: sessionID})
	}
	for _, t := range scoped {
		if t == nil {
			return nil, store.ErrBotUnsupported
		}
		if seen[t.Name()] && t.Name() != "http_request" {
			return nil, fmt.Errorf("duplicate Bot capability %q", t.Name())
		}
		seen[t.Name()] = true
		result = append(result, t)
	}
	return result, nil

}

// Unlike NodeTool, this adapter cannot select a child/viewer sandbox or fall back
// to a host tool. Schema and argument normalization remain the real Chat contract.
type botSandboxTool struct {
	tool.Tool
	client    sandbox.ToolNodeClient
	sessionID string
}

func (t *botSandboxTool) Declaration() *genai.FunctionDeclaration {
	return t.Tool.(common.ToolWithDeclaration).Declaration()
}
func (t *botSandboxTool) ProcessRequest(_ tool.Context, r *model.LLMRequest) error {
	if r == nil {
		return store.ErrBotInvalid
	}
	if r.Tools == nil {
		r.Tools = map[string]any{}
	}
	if _, ok := r.Tools[t.Name()]; ok {
		return fmt.Errorf("duplicate tool %q", t.Name())
	}
	r.Tools[t.Name()] = t
	if r.Config == nil {
		r.Config = &genai.GenerateContentConfig{}
	}
	r.Config.Tools = append(r.Config.Tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{t.Declaration()}})
	return nil
}
func (t *botSandboxTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	client := t.client
	var readExecuted bool
	if t.Name() == "web_fetch" {
		client = &botPublicFetchClient{ToolNodeClient: client, executed: &readExecuted}
	}
	return sandbox.RunNodeTool(ctx, t.Tool, client, t.sessionID, t.sessionID, args, func(values, result map[string]any, err error) {
		if err != nil {
			result = map[string]any{"error": err.Error()}
		}
		recordBuiltinToolObservationWithRead(ctx, t.sessionID, t.Name(), values, result, readExecuted && err == nil, args)
	})
}

type botPublicFetchClient struct {
	sandbox.ToolNodeClient
	executed *bool
}

func (c *botPublicFetchClient) Call(session, name string, args map[string]any) (json.RawMessage, error) {
	return c.CallContext(context.Background(), session, name, args)
}
func (c *botPublicFetchClient) CallContext(ctx context.Context, session, _ string, args map[string]any) (json.RawMessage, error) {
	var raw json.RawMessage
	var err error
	if client, ok := c.ToolNodeClient.(sandbox.ContextCallingToolClient); ok {
		raw, err = client.CallContext(ctx, session, tools.BotPublicFetchOperation, args)
	} else {
		raw, err = c.ToolNodeClient.Call(session, tools.BotPublicFetchOperation, args)
	}
	if err != nil {
		return raw, err
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return raw, nil
	}
	marker, marked := envelope["read_executed"]
	if !marked {
		return raw, nil
	} // no execution attestation
	var executed bool
	var response map[string]any
	if len(envelope) != 2 || json.Unmarshal(marker, &executed) != nil || !executed || json.Unmarshal(envelope["response"], &response) != nil || response == nil {
		return nil, fmt.Errorf("invalid first-party fetch execution envelope")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	*c.executed = true
	return envelope["response"], nil
}

// No name or model-provided argument is proof that an external effect is safe.
// Unclassified tools remain discoverable and require exact durable approval.
type BotCatalogueClassifier struct{}

var _ agent.BotActionClassifier = BotCatalogueClassifier{}

func (BotCatalogueClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	action := execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown}
	switch name {
	case botWorkReadTool, botWorkUpdateTool, botEvidenceReadTool, botCheckWorkTool, "bot_skill_list", "bot_skill_read":
		// These are lease-bound local bookkeeping/evidence and Bot-owned
		// method reads, not external effects. The ordinary policy still checks allowed tools,
		// child ceilings, disabled tools and budgets before dispatch.
		action.Effect, action.ConstraintsVerified = execution.EffectRead, true
	case "memory_save", "bot_skill_use", "bot_skill_save":
		// The production memory adapter is Bot/session-prefiltered and its
		// transactional backend checks the live lease before every mutation.
		action.Effect, action.ConstraintsVerified = execution.EffectReversibleWrite, true
	case "memory_delete":
		action.Effect, action.ConstraintsVerified = execution.EffectDelete, true
	case "ask_user", "filter_json", "git_diff_add_line_numbers", "memory_search", "memory_get", "read_task_result", "announce_plan", "update_plan", "announce_completion":
		action.Effect, action.ConstraintsVerified = execution.EffectRead, true
	}
	return action, nil
}
