package launcher

import (
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

// botProviderTools preserves Chat's fixed bridge while treating only its two
// local metadata helpers as discovery. execute_tool still authorizes its exact
// resolved target; discovery never creates a grant or spends an action quota.
func botProviderTools(index *agent.ToolIndex, binding *botFactoryBinding) ([]tool.Tool, error) {
	bridge, err := fixedProviderTools(index)
	if err != nil {
		return nil, err
	}
	// Autonomous runs retain their existing policy/classifier path unchanged.
	if binding.req.Mandate == nil && !botFactoryInteractive(binding.req) {
		return bridge, nil
	}
	for i, candidate := range bridge {
		if candidate.Name() != "search_tools" && candidate.Name() != "describe_tools" {
			continue
		}
		name := candidate.Name()
		bridge[i], err = agent.NewBotDiscoveryTool(candidate, func(ctx tool.Context) (tool.Context, error) {
			if ctx == nil || ctx.SessionID() == "" {
				return nil, store.ErrBotForbidden
			}
			bound := botBoundToolContext{Context: ctx, owner: binding.bind(ctx)}
			if err := binding.check(bound); err != nil {
				return nil, err
			}
			for _, disabled := range store.DisabledToolsFromContext(bound) {
				if disabled == name {
					return nil, store.ErrBotForbidden
				}
			}
			if botFactoryInteractive(binding.req) {
				if err := binding.req.Authorization.AuthorizeTool(bound, name); err != nil {
					return nil, err
				}
			} else {
				// The catalog was already filtered to the mandate's exact resources.
				// Discovery requires the capability but is not itself an external action
				// resource; the live mandate was rechecked by binding.check above.
				if err := (execution.CapabilityAuthorizer{}).Authorize(binding.req.Principal, execution.CapabilityToolExecute); err != nil {
					return nil, err
				}
			}
			return bound, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return bridge, nil
}
