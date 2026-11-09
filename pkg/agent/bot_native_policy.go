package agent

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
)

// botExecutionContext includes malformed policy-only contexts: those fail closed.
func botExecutionContext(ctx context.Context) bool {
	_, bot := execution.BotRunIdentityFromContext(ctx)
	_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
	return bot || interactive || execution.RunPolicyFromContext(ctx) != nil
}

func botExecutableIdentity(t tool.Tool) string {
	switch t := t.(type) {
	case *lazyProxyTool:
		return BotToolIdentity("mcp:"+t.toolset.serverName, t.Name())
	case *ProtectedTool:
		return botExecutableIdentity(t.Tool)
	default:
		return t.Name()
	}
}

type botCallResolver func(tool.Context, tool.Tool, map[string]any) (tool.Tool, string, map[string]any, error)

// Bot dispatch runs inside the first callback, so secrets are not expanded until
// authorization succeeds. Returning a result prevents ADK executing a second time;
// ADK still runs the existing AfterToolCallbacks to restore/redact/capture.
func botNativeCallbacks(callbacks []llmagent.BeforeToolCallback, resolve botCallResolver) []llmagent.BeforeToolCallback {
	return []llmagent.BeforeToolCallback{func(ctx tool.Context, called tool.Tool, args map[string]any) (map[string]any, error) {
		if !botExecutionContext(ctx) {
			for _, cb := range callbacks {
				out, err := cb(ctx, called, args)
				if out != nil || err != nil {
					return out, err
				}
			}
			return nil, nil
		}
		if discovery, ok := called.(*botDiscoveryTool); ok {
			return discovery.Run(ctx, args)
		}
		if capture, ok := called.(*botProposalCapture); ok {
			return capture.Run(ctx, args)
		}
		target, identity, input := called, botExecutableIdentity(called), args
		if resolve != nil {
			var err error
			target, identity, input, err = resolve(ctx, called, args)
			if err != nil {
				return nil, err
			}
		}
		var authorizedAction execution.ActionDescriptor
		if err := authorizeBotTool(ctx, identity, input, &authorizedAction); err != nil {
			return nil, err
		}
		for _, cb := range callbacks {
			out, err := cb(ctx, called, args)
			if out != nil || err != nil {
				return out, err
			}
		}
		dispatched, err := botAuthorizedInvocationContext(ctx, identity, input, authorizedAction)
		if err != nil {
			return nil, err
		}
		runnable, ok := target.(RunnableTool)
		if !ok {
			return nil, fmt.Errorf("tool %q is not executable", target.Name())
		}
		out, err := runnable.Run(dispatched, input)
		if out == nil && err == nil {
			out = map[string]any{}
		}
		return out, err
	}}
}

// Eager toolsets may start external processes or resolve credentials merely to
// list declarations. Bots only enumerate the known metadata-only lazy adapter.
func botSafeToolset(ctx context.Context, ts tool.Toolset) error {
	if !botExecutionContext(ctx) {
		return nil
	}
	switch ts := ts.(type) {
	case *LazyMCPToolset:
		return nil
	case *FilteredToolset:
		return botSafeToolset(ctx, ts.underlying)
	default:
		return botPolicyDenied("eager toolset startup is not authorized")
	}
}

type botMetadataToolset struct{ tool.Toolset }

func (s botMetadataToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	if err := botSafeToolset(ctx, s.Toolset); err != nil {
		return nil, err
	}
	return s.Toolset.Tools(ctx)
}
func botGuardToolsets(sets []tool.Toolset) []tool.Toolset {
	out := make([]tool.Toolset, 0, len(sets))
	for _, s := range sets {
		out = append(out, botMetadataToolset{s})
	}
	return out
}
