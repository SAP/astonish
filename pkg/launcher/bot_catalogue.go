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
	"google.golang.org/adk/tool/functiontool"
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
	for _, t := range native {
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
	declarations := tools.GetAllFlowToolDeclarations()
	declarations = append(declarations,
		tools.ToolDeclaration{Name: "run_flow", Description: "Execute a saved flow"},
		tools.ToolDeclaration{Name: "search_flows", Description: "Search saved flows"},
	)
	for _, d := range declarations {
		if seen[d.Name] && d.Name != "http_request" {
			continue
		}
		// A supplied HTTP adapter supersedes the explicitly unavailable default.
		found := false
		for _, t := range result {
			if t.Name() == d.Name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		name := d.Name
		t, err := functiontool.New(functiontool.Config{Name: name, Description: d.Description + ". Requires a request-scoped backend; unavailable adapters fail explicitly."}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
			return nil, fmt.Errorf("%w: %s requires a scoped credential, effect and auxiliary-budget adapter", store.ErrBotUnsupported, name)
		})
		if err != nil {
			return nil, err
		}
		result = append(result, t)
		seen[name] = true
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	values, err = common.NormalizeFileToolArguments(t.Name(), values)
	if err != nil {
		return nil, err
	}
	if err = common.ValidateToolArguments(t.Tool, values); err != nil {
		return nil, err
	}
	output, err := t.client.Call(t.sessionID, t.Name(), values)
	if err != nil {
		// Record the failed access, not a successful effect. Keep the original
		// transport error so ordinary error handling and cancellation are intact.
		recordBuiltinToolObservation(ctx, t.sessionID, t.Name(), values, map[string]any{"error": err.Error()})
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(output, &result); err != nil {
		return nil, err
	}
	recordBuiltinToolObservation(ctx, t.sessionID, t.Name(), values, result)
	return result, nil
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
